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

package master

import (
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apiserver/pkg/registry/generic"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/healthz"
	serverstorage "k8s.io/apiserver/pkg/server/storage"
	"k8s.io/kubernetes/cmd/kube-apiserver/app/options"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/v1"
	apiv1 "k8s.io/kubernetes/pkg/api/v1"
	appsv1beta1 "k8s.io/kubernetes/pkg/apis/apps/v1beta1"
	authenticationv1 "k8s.io/kubernetes/pkg/apis/authentication/v1"
	authenticationv1beta1 "k8s.io/kubernetes/pkg/apis/authentication/v1beta1"
	authorizationapiv1 "k8s.io/kubernetes/pkg/apis/authorization/v1"
	authorizationapiv1beta1 "k8s.io/kubernetes/pkg/apis/authorization/v1beta1"
	autoscalingapiv1 "k8s.io/kubernetes/pkg/apis/autoscaling/v1"
	batchapiv1 "k8s.io/kubernetes/pkg/apis/batch/v1"
	certificatesapiv1beta1 "k8s.io/kubernetes/pkg/apis/certificates/v1beta1"
	extensionsapiv1beta1 "k8s.io/kubernetes/pkg/apis/extensions/v1beta1"
	policyapiv1beta1 "k8s.io/kubernetes/pkg/apis/policy/v1beta1"
	rbacapi "k8s.io/kubernetes/pkg/apis/rbac/v1alpha1"
	rbacv1beta1 "k8s.io/kubernetes/pkg/apis/rbac/v1beta1"
	settingsapi "k8s.io/kubernetes/pkg/apis/settings/v1alpha1"
	storageapiv1 "k8s.io/kubernetes/pkg/apis/storage/v1"
	storageapiv1beta1 "k8s.io/kubernetes/pkg/apis/storage/v1beta1"
	corev1client "k8s.io/kubernetes/pkg/client/clientset_generated/clientset/typed/core/v1"
	coreclient "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset/typed/core/internalversion"
	kubeletclient "k8s.io/kubernetes/pkg/kubelet/client"
	"k8s.io/kubernetes/pkg/master/thirdparty"
	"k8s.io/kubernetes/pkg/master/tunneler"
	"k8s.io/kubernetes/pkg/routes"
	nodeutil "k8s.io/kubernetes/pkg/util/node"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"

	// RESTStorage installers
	appsrest "k8s.io/kubernetes/pkg/registry/apps/rest"
	authenticationrest "k8s.io/kubernetes/pkg/registry/authentication/rest"
	authorizationrest "k8s.io/kubernetes/pkg/registry/authorization/rest"
	autoscalingrest "k8s.io/kubernetes/pkg/registry/autoscaling/rest"
	batchrest "k8s.io/kubernetes/pkg/registry/batch/rest"
	certificatesrest "k8s.io/kubernetes/pkg/registry/certificates/rest"
	corerest "k8s.io/kubernetes/pkg/registry/core/rest"
	extensionsrest "k8s.io/kubernetes/pkg/registry/extensions/rest"
	policyrest "k8s.io/kubernetes/pkg/registry/policy/rest"
	rbacrest "k8s.io/kubernetes/pkg/registry/rbac/rest"
	settingsrest "k8s.io/kubernetes/pkg/registry/settings/rest"
	storagerest "k8s.io/kubernetes/pkg/registry/storage/rest"
	"k8s.io/kubernetes/pkg/util/license"
)

const (
	// DefaultEndpointReconcilerInterval is the default amount of time for how often the endpoints for
	// the kubernetes Service are reconciled.
	DefaultEndpointReconcilerInterval = 10 * time.Second
)

type Config struct {
	GenericConfig *genericapiserver.Config

	ClientCARegistrationHook ClientCARegistrationHook

	APIResourceConfigSource  serverstorage.APIResourceConfigSource
	StorageFactory           serverstorage.StorageFactory
	EnableCoreControllers    bool
	EndpointReconcilerConfig EndpointReconcilerConfig
	EventTTL                 time.Duration
	KubeletClientConfig      kubeletclient.KubeletClientConfig

	// Used to start and monitor tunneling
	Tunneler          tunneler.Tunneler
	EnableUISupport   bool
	EnableLogsSupport bool
	ProxyTransport    http.RoundTripper

	// Values to build the IP addresses used by discovery
	// The range of IPs to be assigned to services with type=ClusterIP or greater
	ServiceIPRange net.IPNet
	// The IP address for the GenericAPIServer service (must be inside ServiceIPRange)
	APIServerServiceIP net.IP
	// Port for the apiserver service.
	APIServerServicePort int

	// TODO, we can probably group service related items into a substruct to make it easier to configure
	// the API server items and `Extra*` fields likely fit nicely together.

	// The range of ports to be assigned to services with type=NodePort or greater
	ServiceNodePortRange utilnet.PortRange
	// Additional ports to be exposed on the GenericAPIServer service
	// extraServicePorts is injectable in the event that more ports
	// (other than the default 443/tcp) are exposed on the GenericAPIServer
	// and those ports need to be load balanced by the GenericAPIServer
	// service because this pkg is linked by out-of-tree projects
	// like openshift which want to use the GenericAPIServer but also do
	// more stuff.
	ExtraServicePorts []api.ServicePort
	// Additional ports to be exposed on the GenericAPIServer endpoints
	// Port names should align with ports defined in ExtraServicePorts
	ExtraEndpointPorts []api.EndpointPort
	// If non-zero, the "kubernetes" services uses this port as NodePort.
	KubernetesServiceNodePort int

	// Number of masters running; all masters must be started with the
	// same value for this field. (Numbers > 1 currently untested.)
	MasterCount int
	// License config
	LicenseFile string
	LicenseType string
}

// EndpointReconcilerConfig holds the endpoint reconciler and endpoint reconciliation interval to be
// used by the master.
type EndpointReconcilerConfig struct {
	Reconciler EndpointReconciler
	Interval   time.Duration
}

// Master contains state for a Kubernetes cluster master/api server.
type Master struct {
	GenericAPIServer *genericapiserver.GenericAPIServer

	ClientCARegistrationHook ClientCARegistrationHook
	NodeClient               corev1client.NodeInterface
}

type completedConfig struct {
	*Config
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() completedConfig {
	c.GenericConfig.Complete()

	serviceIPRange, apiServerServiceIP, err := DefaultServiceIPRange(c.ServiceIPRange)
	if err != nil {
		glog.Fatalf("Error determining service IP ranges: %v", err)
	}
	if c.ServiceIPRange.IP == nil {
		c.ServiceIPRange = serviceIPRange
	}
	if c.APIServerServiceIP == nil {
		c.APIServerServiceIP = apiServerServiceIP
	}

	discoveryAddresses := genericapiserver.DefaultDiscoveryAddresses{DefaultAddress: c.GenericConfig.ExternalAddress}
	discoveryAddresses.DiscoveryCIDRRules = append(discoveryAddresses.DiscoveryCIDRRules,
		genericapiserver.DiscoveryCIDRRule{IPRange: c.ServiceIPRange, Address: net.JoinHostPort(c.APIServerServiceIP.String(), strconv.Itoa(c.APIServerServicePort))})
	c.GenericConfig.DiscoveryAddresses = discoveryAddresses

	if c.ServiceNodePortRange.Size == 0 {
		// TODO: Currently no way to specify an empty range (do we need to allow this?)
		// We should probably allow this for clouds that don't require NodePort to do load-balancing (GCE)
		// but then that breaks the strict nestedness of ServiceType.
		// Review post-v1
		c.ServiceNodePortRange = options.DefaultServiceNodePortRange
		glog.Infof("Node port range unspecified. Defaulting to %v.", c.ServiceNodePortRange)
	}

	// enable swagger UI only if general UI support is on
	c.GenericConfig.EnableSwaggerUI = c.GenericConfig.EnableSwaggerUI && c.EnableUISupport

	if c.EndpointReconcilerConfig.Interval == 0 {
		c.EndpointReconcilerConfig.Interval = DefaultEndpointReconcilerInterval
	}

	if c.EndpointReconcilerConfig.Reconciler == nil {
		// use a default endpoint reconciler if nothing is set
		endpointClient := coreclient.NewForConfigOrDie(c.GenericConfig.LoopbackClientConfig)
		c.EndpointReconcilerConfig.Reconciler = NewMasterCountEndpointReconciler(c.MasterCount, endpointClient)
	}

	// this has always been hardcoded true in the past
	c.GenericConfig.EnableMetrics = true

	return completedConfig{c}
}

// SkipComplete provides a way to construct a server instance without config completion.
func (c *Config) SkipComplete() completedConfig {
	return completedConfig{c}
}

// New returns a new instance of Master from the given config.
// Certain config fields will be set to a default value if unset.
// Certain config fields must be specified, including:
//   KubeletClientConfig
func (c completedConfig) New() (*Master, error) {
	if reflect.DeepEqual(c.KubeletClientConfig, kubeletclient.KubeletClientConfig{}) {
		return nil, fmt.Errorf("Master.New() called with empty config.KubeletClientConfig")
	}

	s, err := c.Config.GenericConfig.SkipComplete().New() // completion is done in Complete, no need for a second time
	if err != nil {
		return nil, err
	}

	if c.EnableUISupport {
		routes.UIRedirect{}.Install(s.HandlerContainer)
	}
	if c.EnableLogsSupport {
		routes.Logs{}.Install(s.HandlerContainer)
	}
	nodeCli := corev1client.NewForConfigOrDie(c.GenericConfig.LoopbackClientConfig).Nodes()

	m := &Master{
		GenericAPIServer: s,
		NodeClient:       nodeCli,
	}

	// install legacy rest storage
	if c.APIResourceConfigSource.AnyResourcesForVersionEnabled(apiv1.SchemeGroupVersion) {
		legacyRESTStorageProvider := corerest.LegacyRESTStorageProvider{
			StorageFactory:       c.StorageFactory,
			ProxyTransport:       c.ProxyTransport,
			KubeletClientConfig:  c.KubeletClientConfig,
			EventTTL:             c.EventTTL,
			ServiceIPRange:       c.ServiceIPRange,
			ServiceNodePortRange: c.ServiceNodePortRange,
			LoopbackClientConfig: c.GenericConfig.LoopbackClientConfig,
		}
		m.InstallLegacyAPI(c.Config, c.Config.GenericConfig.RESTOptionsGetter, legacyRESTStorageProvider)
	}

	// The order here is preserved in discovery.
	// If resources with identical names exist in more than one of these groups (e.g. "deployments.apps"" and "deployments.extensions"),
	// the order of this list determines which group an unqualified resource name (e.g. "deployments") should prefer.
	restStorageProviders := []RESTStorageProvider{
		authenticationrest.RESTStorageProvider{Authenticator: c.GenericConfig.Authenticator},
		authorizationrest.RESTStorageProvider{Authorizer: c.GenericConfig.Authorizer},
		autoscalingrest.RESTStorageProvider{},
		batchrest.RESTStorageProvider{},
		certificatesrest.RESTStorageProvider{},
		extensionsrest.RESTStorageProvider{ResourceInterface: thirdparty.NewThirdPartyResourceServer(s, c.StorageFactory)},
		policyrest.RESTStorageProvider{},
		rbacrest.RESTStorageProvider{Authorizer: c.GenericConfig.Authorizer},
		settingsrest.RESTStorageProvider{},
		storagerest.RESTStorageProvider{},
		// keep apps after extensions so legacy clients resolve the extensions versions of shared resource names.
		// See https://github.com/kubernetes/kubernetes/issues/42392
		appsrest.RESTStorageProvider{},
	}
	m.InstallAPIs(c.Config.APIResourceConfigSource, c.Config.GenericConfig.RESTOptionsGetter, restStorageProviders...)

	if c.Tunneler != nil {
		m.installTunneler(c.Tunneler, nodeCli)
	}

	if err := m.GenericAPIServer.AddPostStartHook("ca-registration", c.ClientCARegistrationHook.PostStartHook); err != nil {
		glog.Fatalf("Error registering PostStartHook %q: %v", "ca-registration", err)
	}
	return m, nil
}

func (m *Master) InstallLegacyAPI(c *Config, restOptionsGetter generic.RESTOptionsGetter, legacyRESTStorageProvider corerest.LegacyRESTStorageProvider) {
	legacyRESTStorage, apiGroupInfo, err := legacyRESTStorageProvider.NewLegacyRESTStorage(restOptionsGetter)
	if err != nil {
		glog.Fatalf("Error building core storage: %v", err)
	}

	if c.EnableCoreControllers {
		coreClient := coreclient.NewForConfigOrDie(c.GenericConfig.LoopbackClientConfig)
		bootstrapController := c.NewBootstrapController(legacyRESTStorage, coreClient, coreClient)
		if err := m.GenericAPIServer.AddPostStartHook("bootstrap-controller", bootstrapController.PostStartHook); err != nil {
			glog.Fatalf("Error registering PostStartHook %q: %v", "bootstrap-controller", err)
		}
	}

	if err := m.GenericAPIServer.InstallLegacyAPIGroup(genericapiserver.DefaultLegacyAPIPrefix, &apiGroupInfo); err != nil {
		glog.Fatalf("Error in registering group versions: %v", err)
	}
}

func (m *Master) installTunneler(nodeTunneler tunneler.Tunneler, nodeClient corev1client.NodeInterface) {
	nodeTunneler.Run(nodeAddressProvider{nodeClient}.externalAddresses)
	m.GenericAPIServer.AddHealthzChecks(healthz.NamedCheck("SSH Tunnel Check", tunneler.TunnelSyncHealthChecker(nodeTunneler)))
	prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "apiserver_proxy_tunnel_sync_latency_secs",
		Help: "The time since the last successful synchronization of the SSH tunnels for proxy requests.",
	}, func() float64 { return float64(nodeTunneler.SecondsSinceSync()) })
}

//以下为监测license代码段：

func isReadyNodeInfo(node *v1.Node) bool {
	for ix := range node.Status.Conditions {
		condition := &node.Status.Conditions[ix]
		if condition.Reason == "KubeletReady" {
			return true
		}
	}
	return false
}

//CheckLicense licnese信息检测
func (m *Master) CheckLicense(licenseFile, licenseType string, stopCh <-chan struct{}) {
	//step1 wait for 10 senconds
	time.Sleep(10 * time.Second)
	//step2 start check license
	tick := time.NewTicker(time.Second * 20)
	m.doCheck(licenseFile, licenseType)
	for {
		select {
		case <-stopCh:
			return
		case <-tick.C:

		}
		glog.V(2).Info("Start check license info.")
		m.doCheck(licenseFile, licenseType)
	}
}

//ExitAllNode 下线全部节点
func (m *Master) ExitAllNode() {
	nodes, err := m.NodeClient.List(metav1.ListOptions{})
	if err != nil {
		glog.Errorf("下线节点发生错误。" + err.Error())
		return
	}
	for ix := range nodes.Items {
		node := &nodes.Items[ix]
		if isReadyNodeInfo(node) {
			err := m.excitNode(node.Name)
			if err != nil {
				glog.Errorf("下线节点发生错误。" + err.Error())
			}
		}
	}
}

//ExitMinNode 下线资源最小节点
func (m *Master) ExitMinNode(tag string) {
	nodes, err := m.NodeClient.List(metav1.ListOptions{})
	if err != nil {
		glog.Errorf("下线节点发生错误。" + err.Error())
		return
	}
	var nodeName string
	var min int64
	for ix := range nodes.Items {
		node := &nodes.Items[ix]
		if isReadyNodeInfo(node) {
			if tag == "node" {
				err := m.excitNode(node.Name)
				if err != nil {
					glog.Errorf("下线节点发生错误。" + err.Error())
				}
				return
			}
			if tag == "cpu" {
				if ix == 0 {
					min = node.Status.Capacity.Cpu().Value()
					nodeName = node.Name
				} else {
					if min > node.Status.Capacity.Cpu().Value() {
						min = node.Status.Capacity.Cpu().Value()
						nodeName = node.Name
					}
				}
			}
			if tag == "memory" {
				if ix == 0 {
					min = node.Status.Capacity.Memory().Value()
					nodeName = node.Name
				} else {
					if min > node.Status.Capacity.Memory().Value() {
						min = node.Status.Capacity.Memory().Value()
						nodeName = node.Name
					}
				}
			}
		}
	}
	err = m.excitNode(nodeName)
	if err != nil {
		glog.Errorf("下线节点发生错误。" + err.Error())
	}
}

func (m *Master) doCheck(licenseFile, licenseType string) {
	var LicenseInfo license.Info
	var err error
	if licenseType == "online" {
		LicenseInfo, err = license.ReadLicenseFromConsole("a905b993b5035122abe7be3a5c13ce2e55047981", licenseFile)
		if err != nil {
			glog.Error("在线获取LICENSE获取错误,系统退出。如有疑问请联系客服。错误原因:" + err.Error())
			return
		}
	} else {
		LicenseInfo, err = license.ReadLicenseFromFile(licenseFile)
		if err != nil {
			glog.Error("在线获取LICENSE获取错误,系统退出。如有疑问请联系客服。错误原因:" + err.Error())
			return
		}
	}
	//step1 check time
	endTime, err := time.Parse("2006-01-02 15:04:05", LicenseInfo.EndTime)
	if err != nil {
		glog.Error("解析LICENSE过期时间错误，集群退出.", err.Error())
		m.ExitAllNode()
		return
	}

	if endTime.Before(time.Now()) {
		glog.Error("LICENSE过期时间已到，集群退出.请联系客服")
		m.ExitAllNode()
		return
	}
	if time.Now().After(endTime.AddDate(0, -1, 0)) {
		glog.Errorf("你的LICENSE将于%s过期.为了不影响你的使用，请与我们客服联系。", LicenseInfo.EndTime)
	}
	nodesInfo, err := m.getAllNodeInfo()
	if err != nil || nodesInfo == nil {
		return
	}
	//start check resources
	glog.V(2).Infof("集群资源情况:内存%dGB,CPU %d核,节点%d个。", nodesInfo["memorys"], nodesInfo["cpus"], nodesInfo["nodes"])

	//step2 check node number
	if nodesInfo["nodes"] > LicenseInfo.Node {
		glog.Error("集群节点数量超过授权值，下线节点.请联系客服")
		m.ExitMinNode("node")
		return
	}

	//step3 check memory
	if nodesInfo["memorys"] > LicenseInfo.Memory {
		glog.Error("集群内存总数超过授权值，下线节点.请联系客服")
		m.ExitMinNode("memory")
		return
	}
	//step4 check cpu
	if nodesInfo["cpus"] > LicenseInfo.CPU {
		glog.Error("集群CPU核总数超过授权值，下线节点.请联系客服")
		m.ExitMinNode("cpu")
		return
	}
}

func (m *Master) getAllNodeInfo() (map[string]int64, error) {
	nodes, err := m.NodeClient.List(metav1.ListOptions{})
	if err != nil {
		glog.Error("list all node info error.", err.Error())
		return nil, err
	}
	nodeMap := make(map[string]int64)
	var memory, readyNodes, cpu int64
	for ix := range nodes.Items {
		node := &nodes.Items[ix]
		if isReadyNodeInfo(node) {
			readyNodes++
			cpu += node.Status.Capacity.Cpu().Value()
			memory += node.Status.Capacity.Memory().Value()
		}
	}
	//集群总cpu核数
	nodeMap["cpus"] = cpu
	//集群总内存数（b->GB）
	nodeMap["memorys"] = memory / 1024 / 1024 / 1024
	//集群ready的节点数
	nodeMap["nodes"] = readyNodes
	return nodeMap, nil
}

func (m *Master) excitNode(nodeName string) error {
	err := m.NodeClient.Delete(nodeName, &metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	return nil
}

// RESTStorageProvider is a factory type for REST storage.
type RESTStorageProvider interface {
	GroupName() string
	NewRESTStorage(apiResourceConfigSource serverstorage.APIResourceConfigSource, restOptionsGetter generic.RESTOptionsGetter) (genericapiserver.APIGroupInfo, bool)
}

// InstallAPIs will install the APIs for the restStorageProviders if they are enabled.
func (m *Master) InstallAPIs(apiResourceConfigSource serverstorage.APIResourceConfigSource, restOptionsGetter generic.RESTOptionsGetter, restStorageProviders ...RESTStorageProvider) {
	apiGroupsInfo := []genericapiserver.APIGroupInfo{}

	for _, restStorageBuilder := range restStorageProviders {
		groupName := restStorageBuilder.GroupName()
		if !apiResourceConfigSource.AnyResourcesForGroupEnabled(groupName) {
			glog.V(1).Infof("Skipping disabled API group %q.", groupName)
			continue
		}
		apiGroupInfo, enabled := restStorageBuilder.NewRESTStorage(apiResourceConfigSource, restOptionsGetter)
		if !enabled {
			glog.Warningf("Problem initializing API group %q, skipping.", groupName)
			continue
		}
		glog.V(1).Infof("Enabling API group %q.", groupName)

		if postHookProvider, ok := restStorageBuilder.(genericapiserver.PostStartHookProvider); ok {
			name, hook, err := postHookProvider.PostStartHook()
			if err != nil {
				glog.Fatalf("Error building PostStartHook: %v", err)
			}
			if err := m.GenericAPIServer.AddPostStartHook(name, hook); err != nil {
				glog.Fatalf("Error registering PostStartHook %q: %v", name, err)
			}
		}

		apiGroupsInfo = append(apiGroupsInfo, apiGroupInfo)
	}

	for i := range apiGroupsInfo {
		if err := m.GenericAPIServer.InstallAPIGroup(&apiGroupsInfo[i]); err != nil {
			glog.Fatalf("Error in registering group versions: %v", err)
		}
	}
}

type nodeAddressProvider struct {
	nodeClient corev1client.NodeInterface
}

func (n nodeAddressProvider) externalAddresses() ([]string, error) {
	preferredAddressTypes := []apiv1.NodeAddressType{
		apiv1.NodeExternalIP,
		apiv1.NodeLegacyHostIP,
	}
	nodes, err := n.nodeClient.List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	addrs := []string{}
	for ix := range nodes.Items {
		node := &nodes.Items[ix]
		addr, err := nodeutil.GetPreferredNodeAddress(node, preferredAddressTypes)
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

func DefaultAPIResourceConfigSource() *serverstorage.ResourceConfig {
	ret := serverstorage.NewResourceConfig()
	ret.EnableVersions(
		apiv1.SchemeGroupVersion,
		extensionsapiv1beta1.SchemeGroupVersion,
		batchapiv1.SchemeGroupVersion,
		authenticationv1.SchemeGroupVersion,
		authenticationv1beta1.SchemeGroupVersion,
		autoscalingapiv1.SchemeGroupVersion,
		appsv1beta1.SchemeGroupVersion,
		policyapiv1beta1.SchemeGroupVersion,
		rbacv1beta1.SchemeGroupVersion,
		rbacapi.SchemeGroupVersion,
		settingsapi.SchemeGroupVersion,
		storageapiv1.SchemeGroupVersion,
		storageapiv1beta1.SchemeGroupVersion,
		certificatesapiv1beta1.SchemeGroupVersion,
		authorizationapiv1.SchemeGroupVersion,
		authorizationapiv1beta1.SchemeGroupVersion,
	)

	// all extensions resources except these are disabled by default
	ret.EnableResources(
		extensionsapiv1beta1.SchemeGroupVersion.WithResource("daemonsets"),
		extensionsapiv1beta1.SchemeGroupVersion.WithResource("deployments"),
		extensionsapiv1beta1.SchemeGroupVersion.WithResource("ingresses"),
		extensionsapiv1beta1.SchemeGroupVersion.WithResource("networkpolicies"),
		extensionsapiv1beta1.SchemeGroupVersion.WithResource("replicasets"),
		extensionsapiv1beta1.SchemeGroupVersion.WithResource("thirdpartyresources"),
		extensionsapiv1beta1.SchemeGroupVersion.WithResource("podsecuritypolicies"),
	)

	return ret
}
