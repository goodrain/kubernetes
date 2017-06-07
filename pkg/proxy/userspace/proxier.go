/*
Copyright 2014 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package userspace

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/types"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/proxy"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	utilproxy "k8s.io/kubernetes/pkg/proxy/util"
	utilexec "k8s.io/kubernetes/pkg/util/exec"
)

type portal struct {
	ip         net.IP
	port       int
	isExternal bool
}

// ServiceInfo contains information and state for a particular proxied service
type ServiceInfo struct {
	// Timeout is the the read/write timeout (used for UDP connections)
	Timeout time.Duration
	// ActiveClients is the cache of active UDP clients being proxied by this proxy for this service
	ActiveClients *ClientCache
	// ServiceRef is a full object reference to the the service described by this ServiceInfo
	ServiceRef api.ObjectReference

	isAliveAtomic       int32 // Only access this with atomic ops
	portal              portal
	protocol            api.Protocol
	proxyPort           int
	socket              ProxySocket
	nodePort            int
	loadBalancerStatus  api.LoadBalancerStatus
	sessionAffinityType api.ServiceAffinity
	stickyMaxAgeMinutes int
	// Deprecated, but required for back-compat (including e2e)
	externalIPs []string
}

func (info *ServiceInfo) setAlive(b bool) {
	var i int32
	if b {
		i = 1
	}
	atomic.StoreInt32(&info.isAliveAtomic, i)
}

func (info *ServiceInfo) IsAlive() bool {
	return atomic.LoadInt32(&info.isAliveAtomic) != 0
}

func logTimeout(err error) bool {
	if e, ok := err.(net.Error); ok {
		if e.Timeout() {
			glog.V(3).Infof("connection to endpoint closed due to inactivity")
			return true
		}
	}
	return false
}

// ProxySocketFunc is a function which constructs a ProxySocket from a protocol, ip, and port
type ProxySocketFunc func(protocol api.Protocol, ip net.IP, port int) (ProxySocket, error)

// Proxier is a simple proxy for TCP connections between a localhost:lport
// and services that provide the actual implementations.
type Proxier struct {
	loadBalancer    LoadBalancer
	mu              sync.Mutex // protects serviceMap
	serviceMap      map[proxy.ServicePortName]*ServiceInfo
	syncPeriod      time.Duration
	minSyncPeriod   time.Duration // unused atm, but plumbed through
	udpIdleTimeout  time.Duration
	portMapMutex    sync.Mutex
	portMap         map[portMapKey]*portMapValue
	numProxyLoops   int32 // use atomic ops to access this; mostly for testing
	listenIP        net.IP
	hostIP          net.IP
	proxyPorts      PortAllocator
	makeProxySocket ProxySocketFunc
	exec            utilexec.Interface
}

// assert Proxier is a ProxyProvider
var _ proxy.ProxyProvider = &Proxier{}

// A key for the portMap.  The ip has to be a string because slices can't be map
// keys.
type portMapKey struct {
	ip       string
	port     int
	protocol api.Protocol
}

func (k *portMapKey) String() string {
	return fmt.Sprintf("%s:%d/%s", k.ip, k.port, k.protocol)
}

// A value for the portMap
type portMapValue struct {
	owner  proxy.ServicePortName
	socket interface {
		Close() error
	}
}

var (
	// ErrProxyOnLocalhost is returned by NewProxier if the user requests a proxier on
	// the loopback address. May be checked for by callers of NewProxier to know whether
	// the caller provided invalid input.
	ErrProxyOnLocalhost = fmt.Errorf("cannot proxy on localhost")
)

// IsProxyLocked returns true if the proxy could not acquire the lock on iptables.
func IsProxyLocked(err error) bool {
	return strings.Contains(err.Error(), "holding the xtables lock")
}

// NewProxier returns a new Proxier given a LoadBalancer and an address on
// which to listen.  Because of the iptables logic, It is assumed that there
// is only a single Proxier active on a machine. An error will be returned if
// the proxier cannot be started due to an invalid ListenIP (loopback) or
// if iptables fails to update or acquire the initial lock. Once a proxier is
// created, it will keep iptables up to date in the background and will not
// terminate if a particular iptables call fails.
func NewProxier(loadBalancer LoadBalancer, listenIP net.IP, exec utilexec.Interface, pr utilnet.PortRange, syncPeriod, minSyncPeriod, udpIdleTimeout time.Duration) (*Proxier, error) {
	return NewCustomProxier(loadBalancer, listenIP, exec, pr, syncPeriod, minSyncPeriod, udpIdleTimeout, newProxySocket)
}

// NewCustomProxier functions similarly to NewProxier, returing a new Proxier
// for the given LoadBalancer and address.  The new proxier is constructed using
// the ProxySocket constructor provided, however, instead of constructing the
// default ProxySockets.
func NewCustomProxier(loadBalancer LoadBalancer, listenIP net.IP, exec utilexec.Interface, pr utilnet.PortRange, syncPeriod, minSyncPeriod, udpIdleTimeout time.Duration, makeProxySocket ProxySocketFunc) (*Proxier, error) {
	// if listenIP.Equal(localhostIPv4) || listenIP.Equal(localhostIPv6) {
	// 	return nil, ErrProxyOnLocalhost
	// }

	hostIP, err := utilnet.ChooseHostInterface()
	if err != nil {
		return nil, fmt.Errorf("failed to select a host interface: %v", err)
	}

	err = setRLimit(64 * 1000)
	if err != nil {
		return nil, fmt.Errorf("failed to set open file handler limit: %v", err)
	}

	proxyPorts := newPortAllocator(pr)

	glog.V(2).Infof("Setting proxy IP to %v and initializing iptables", hostIP)
	return createProxier(loadBalancer, listenIP, exec, hostIP, proxyPorts, syncPeriod, minSyncPeriod, udpIdleTimeout, makeProxySocket)
}

func createProxier(loadBalancer LoadBalancer, listenIP net.IP, exec utilexec.Interface, hostIP net.IP, proxyPorts PortAllocator, syncPeriod, minSyncPeriod, udpIdleTimeout time.Duration, makeProxySocket ProxySocketFunc) (*Proxier, error) {
	// convenient to pass nil for tests..
	if proxyPorts == nil {
		proxyPorts = newPortAllocator(utilnet.PortRange{})
	}
	return &Proxier{
		loadBalancer: loadBalancer,
		serviceMap:   make(map[proxy.ServicePortName]*ServiceInfo),
		portMap:      make(map[portMapKey]*portMapValue),
		syncPeriod:   syncPeriod,
		// plumbed through if needed, not used atm.
		minSyncPeriod:   minSyncPeriod,
		udpIdleTimeout:  udpIdleTimeout,
		listenIP:        listenIP,
		hostIP:          hostIP,
		proxyPorts:      proxyPorts,
		makeProxySocket: makeProxySocket,
		exec:            exec,
	}, nil
}

// Sync is called to immediately synchronize the proxier state to iptables
func (proxier *Proxier) Sync() {
	proxier.cleanupStaleStickySessions()
}

// SyncLoop runs periodic work.  This is expected to run as a goroutine or as the main loop of the app.  It does not return.
func (proxier *Proxier) SyncLoop() {
	t := time.NewTicker(proxier.syncPeriod)
	defer t.Stop()
	for {
		<-t.C
		glog.V(6).Infof("Periodic sync")
		proxier.Sync()
	}
}

// clean up any stale sticky session records in the hash map.
func (proxier *Proxier) cleanupStaleStickySessions() {
	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	for name := range proxier.serviceMap {
		proxier.loadBalancer.CleanupStaleStickySessions(name)
	}
}

// This assumes proxier.mu is not locked.
func (proxier *Proxier) stopProxy(service proxy.ServicePortName, info *ServiceInfo) error {
	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	return proxier.stopProxyInternal(service, info)
}

// This assumes proxier.mu is locked.
func (proxier *Proxier) stopProxyInternal(service proxy.ServicePortName, info *ServiceInfo) error {
	delete(proxier.serviceMap, service)
	info.setAlive(false)
	err := info.socket.Close()
	port := info.socket.ListenPort()
	proxier.proxyPorts.Release(port)
	return err
}

func (proxier *Proxier) getServiceInfo(service proxy.ServicePortName) (*ServiceInfo, bool) {
	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	info, ok := proxier.serviceMap[service]
	return info, ok
}

func (proxier *Proxier) setServiceInfo(service proxy.ServicePortName, info *ServiceInfo) {
	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	proxier.serviceMap[service] = info
}

// addServiceOnPort starts listening for a new service, returning the ServiceInfo.
// Pass proxyPort=0 to allocate a random port. The timeout only applies to UDP
// connections, for now.
func (proxier *Proxier) addServiceOnPort(service proxy.ServicePortName, serviceRef api.ObjectReference, protocol api.Protocol, proxyPort int, timeout time.Duration) (*ServiceInfo, error) {
	sock, err := proxier.makeProxySocket(protocol, proxier.listenIP, proxyPort)
	if err != nil {
		return nil, err
	}
	_, portStr, err := net.SplitHostPort(sock.Addr().String())
	if err != nil {
		sock.Close()
		return nil, err
	}
	portNum, err := strconv.Atoi(portStr)
	if err != nil {
		sock.Close()
		return nil, err
	}
	si := &ServiceInfo{
		Timeout:       timeout,
		ActiveClients: newClientCache(),
		ServiceRef:    serviceRef,

		isAliveAtomic:       1,
		proxyPort:           portNum,
		protocol:            protocol,
		socket:              sock,
		sessionAffinityType: api.ServiceAffinityNone, // default
		stickyMaxAgeMinutes: 180,                     // TODO: parameterize this in the API.
	}
	proxier.setServiceInfo(service, si)

	glog.V(2).Infof("Proxying for service %q on %s port %d", service, protocol, portNum)
	go func(service proxy.ServicePortName, proxier *Proxier) {
		defer runtime.HandleCrash()
		atomic.AddInt32(&proxier.numProxyLoops, 1)
		sock.ProxyLoop(service, si, proxier.loadBalancer)
		atomic.AddInt32(&proxier.numProxyLoops, -1)
	}(service, proxier)

	return si, nil
}

// OnServiceUpdate manages the active set of service proxies.
// Active service proxies are reinitialized if found in the update set or
// shutdown if missing from the update set.
func (proxier *Proxier) OnServiceUpdate(services []api.Service) {
	glog.V(4).Infof("Received update notice: %+v", services)
	activeServices := make(map[proxy.ServicePortName]bool) // use a map as a set
	for i := range services {
		service := &services[i]

		// if ClusterIP is "None" or empty, skip proxying
		if !api.IsServiceIPSet(service) {
			glog.V(3).Infof("Skipping service %s due to clusterIP = %q", types.NamespacedName{Namespace: service.Namespace, Name: service.Name}, service.Spec.ClusterIP)
			continue
		}

		// TODO: should this just be api.GetReference?
		svcGVK := service.GetObjectKind().GroupVersionKind()
		svcRef := api.ObjectReference{
			Kind:            svcGVK.Kind,
			Namespace:       service.Namespace,
			Name:            service.Name,
			UID:             service.UID,
			APIVersion:      svcGVK.GroupVersion().String(),
			ResourceVersion: service.ResourceVersion,
		}

		for i := range service.Spec.Ports {
			servicePort := &service.Spec.Ports[i]
			serviceName := proxy.ServicePortName{NamespacedName: types.NamespacedName{Namespace: service.Namespace, Name: service.Name}, Port: servicePort.Name}
			activeServices[serviceName] = true
			serviceIP := net.ParseIP(service.Spec.ClusterIP)
			info, exists := proxier.getServiceInfo(serviceName)
			// TODO: check health of the socket?  What if ProxyLoop exited?
			if exists && sameConfig(info, service, servicePort) {
				// Nothing changed.
				continue
			}
			if exists {
				err := proxier.stopProxy(serviceName, info)
				if err != nil {
					glog.Errorf("Failed to stop service %q: %v", serviceName, err)
				}
			}

			// proxyPort, err := proxier.proxyPorts.AllocateNext()
			// if err != nil {
			// 	glog.Errorf("failed to allocate proxy port for service %q: %v", serviceName, err)
			// 	continue
			// }

			glog.V(1).Infof("Adding new service %q at %s:%d/%s", serviceName, serviceIP, servicePort.Port, servicePort.Protocol)
			info, err := proxier.addServiceOnPort(serviceName, svcRef, servicePort.Protocol, int(servicePort.Port), proxier.udpIdleTimeout)
			if err != nil {
				glog.Errorf("Failed to start proxy for %q: %v", serviceName, err)
				continue
			}
			info.portal.ip = serviceIP
			info.portal.port = int(servicePort.Port)
			info.externalIPs = service.Spec.ExternalIPs
			// Deep-copy in case the service instance changes
			info.loadBalancerStatus = *api.LoadBalancerStatusDeepCopy(&service.Status.LoadBalancer)
			info.nodePort = int(servicePort.NodePort)
			info.sessionAffinityType = service.Spec.SessionAffinity
			glog.V(4).Infof("info: %#v", info)
			proxier.loadBalancer.NewService(serviceName, info.sessionAffinityType, info.stickyMaxAgeMinutes)
		}
	}

	staleUDPServices := sets.NewString()
	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	for name, info := range proxier.serviceMap {
		if !activeServices[name] {
			glog.V(1).Infof("Stopping service %q", name)

			if proxier.serviceMap[name].protocol == api.ProtocolUDP {
				staleUDPServices.Insert(proxier.serviceMap[name].portal.ip.String())
			}
			err := proxier.stopProxyInternal(name, info)
			if err != nil {
				glog.Errorf("Failed to stop service %q: %v", name, err)
			}
			proxier.loadBalancer.DeleteService(name)
		}
	}

	utilproxy.DeleteServiceConnections(proxier.exec, staleUDPServices.List())
}

func sameConfig(info *ServiceInfo, service *api.Service, port *api.ServicePort) bool {
	if info.protocol != port.Protocol || info.portal.port != int(port.Port) || info.nodePort != int(port.NodePort) {
		return false
	}
	if !info.portal.ip.Equal(net.ParseIP(service.Spec.ClusterIP)) {
		return false
	}
	if !ipsEqual(info.externalIPs, service.Spec.ExternalIPs) {
		return false
	}
	if !api.LoadBalancerStatusEqual(&info.loadBalancerStatus, &service.Status.LoadBalancer) {
		return false
	}
	if info.sessionAffinityType != service.Spec.SessionAffinity {
		return false
	}
	return true
}

func ipsEqual(lhs, rhs []string) bool {
	if len(lhs) != len(rhs) {
		return false
	}
	for i := range lhs {
		if lhs[i] != rhs[i] {
			return false
		}
	}
	return true
}

func isLocalIP(ip net.IP) (bool, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false, err
	}
	for i := range addrs {
		intf, _, err := net.ParseCIDR(addrs[i].String())
		if err != nil {
			return false, err
		}
		if ip.Equal(intf) {
			return true, nil
		}
	}
	return false, nil
}

// Used below.
var zeroIPv4 = net.ParseIP("0.0.0.0")
var localhostIPv4 = net.ParseIP("127.0.0.1")

var zeroIPv6 = net.ParseIP("::0")
var localhostIPv6 = net.ParseIP("::1")

func isTooManyFDsError(err error) bool {
	return strings.Contains(err.Error(), "too many open files")
}

func isClosedError(err error) bool {
	// A brief discussion about handling closed error here:
	// https://code.google.com/p/go/issues/detail?id=4373#c14
	// TODO: maybe create a stoppable TCP listener that returns a StoppedError
	return strings.HasSuffix(err.Error(), "use of closed network connection")
}
