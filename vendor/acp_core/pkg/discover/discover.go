package discover

import (
	"acp_core/pkg/discover/config"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/mvcc/mvccpb"
)

//CallbackUpdate 每次返还变化
type CallbackUpdate interface {
	//TODO:
	//weight自动发现更改实现暂时不 Ready
	UpdateEndpoints(operation config.Operation, endpoints ...*config.Endpoint)
	//when watch occurred error,will exec this method
	Error(error)
}

//Callback 每次返回全部节点
type Callback interface {
	UpdateEndpoints(endpoints ...*config.Endpoint)
	//when watch occurred error,will exec this method
	Error(error)
}

//Discover 后端服务自动发现
type Discover interface {
	AddProject(name string, callback Callback)
	AddUpdateProject(name string, callback CallbackUpdate)
	Stop()
}

//GetDiscover 获取服务发现管理器
func GetDiscover(opt config.DiscoverConfig) (Discover, error) {
	if opt.EtcdClusterEndpoints == nil || len(opt.EtcdClusterEndpoints) == 0 {
		opt.EtcdClusterEndpoints = []string{"127.0.0.1:2379"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   opt.EtcdClusterEndpoints,
		DialTimeout: time.Second * 10,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	etcdD := &etcdDiscover{
		projects: make(map[string]CallbackUpdate),
		ctx:      ctx,
		cancel:   cancel,
		client:   client,
		prefix:   "/traefik",
	}
	return etcdD, nil
}

type etcdDiscover struct {
	projects map[string]CallbackUpdate
	lock     sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	client   *clientv3.Client
	prefix   string
}
type defaultCallBackUpdate struct {
	endpoints map[string]*config.Endpoint
	callback  Callback
	lock      sync.Mutex
}

func (d *defaultCallBackUpdate) UpdateEndpoints(operation config.Operation, endpoints ...*config.Endpoint) {
	d.lock.Lock()
	defer d.lock.Unlock()
	switch operation {
	case config.ADD:
		for _, e := range endpoints {
			if old, ok := d.endpoints[e.Name]; !ok {
				d.endpoints[e.Name] = e
			} else {
				if e.Mode == 0 {
					old.URL = e.URL
				}
				if e.Mode == 1 {
					old.Weight = e.Weight
				}
				if e.Mode == 2 {
					old.URL = e.URL
					old.Weight = e.Weight
				}
			}
		}
	case config.SYNC:
		for _, e := range endpoints {
			if old, ok := d.endpoints[e.Name]; !ok {
				d.endpoints[e.Name] = e
			} else {
				if e.Mode == 0 {
					old.URL = e.URL
				}
				if e.Mode == 1 {
					old.Weight = e.Weight
				}
				if e.Mode == 2 {
					old.URL = e.URL
					old.Weight = e.Weight
				}
			}
		}
	case config.DELETE:
		for _, e := range endpoints {
			if e.Mode == 0 {
				if old, ok := d.endpoints[e.Name]; ok {
					old.URL = ""
				}
			}
			if e.Mode == 1 {
				if old, ok := d.endpoints[e.Name]; ok {
					old.Weight = 0
				}
			}
			if e.Mode == 2 {
				if _, ok := d.endpoints[e.Name]; ok {
					delete(d.endpoints, e.Name)
				}
			}
		}
	case config.UPDATE:
		for _, e := range endpoints {
			if e.Mode == 0 {
				if old, ok := d.endpoints[e.Name]; ok {
					old.URL = e.URL
				}
			}
			if e.Mode == 1 {
				if old, ok := d.endpoints[e.Name]; ok {
					old.Weight = e.Weight
				}
			}
			if e.Mode == 2 {
				if old, ok := d.endpoints[e.Name]; ok {
					old.URL = e.URL
					old.Weight = e.Weight
				}
			}
		}
	}
	var re []*config.Endpoint
	for _, v := range d.endpoints {
		if v.URL != "" {
			re = append(re, v)
		}
	}
	d.callback.UpdateEndpoints(re...)
}

func (d *defaultCallBackUpdate) Error(err error) {
	d.callback.Error(err)
}

func (e *etcdDiscover) AddProject(name string, callback Callback) {
	e.lock.Lock()
	defer e.lock.Unlock()
	if _, ok := e.projects[name]; !ok {
		cal := &defaultCallBackUpdate{
			callback:  callback,
			endpoints: make(map[string]*config.Endpoint),
		}
		e.projects[name] = cal
		go e.discover(name, cal)
	}
}

func (e *etcdDiscover) AddUpdateProject(name string, callback CallbackUpdate) {
	e.lock.Lock()
	defer e.lock.Unlock()
	if _, ok := e.projects[name]; !ok {
		e.projects[name] = callback
		go e.discover(name, callback)
	}
}

func (e *etcdDiscover) Stop() {
	e.cancel()
}

func (e *etcdDiscover) removeProject(name string) {
	e.lock.Lock()
	defer e.lock.Unlock()
	if _, ok := e.projects[name]; ok {
		delete(e.projects, name)
	}
}

func (e *etcdDiscover) discover(name string, callback CallbackUpdate) {
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	defer e.removeProject(name)
	endpoints := e.list(name)
	if endpoints != nil && len(endpoints) > 0 {
		callback.UpdateEndpoints(config.SYNC, endpoints...)
	}
	watch := e.client.Watch(ctx, fmt.Sprintf("%s/backends/%s/servers", e.prefix, name), clientv3.WithPrefix())
	for {
		select {
		case <-e.ctx.Done():
			return
		case res := <-watch:
			if err := res.Err(); err != nil {
				callback.Error(err)
				return
			}
			for _, event := range res.Events {
				if event.Kv != nil {
					var end *config.Endpoint
					if strings.HasSuffix(string(event.Kv.Key), "/url") { //服务地址变化
						kstep := strings.Split(string(event.Kv.Key), "/")
						if len(kstep) > 2 {
							serverName := kstep[len(kstep)-2]
							serverURL := string(event.Kv.Value)
							end = &config.Endpoint{Name: serverName, URL: serverURL, Mode: 0}
						}
					}
					if strings.HasSuffix(string(event.Kv.Key), "/weight") { //获取服务地址
						kstep := strings.Split(string(event.Kv.Key), "/")
						if len(kstep) > 2 {
							serverName := kstep[len(kstep)-2]
							serverWeight := string(event.Kv.Value)
							weight, _ := strconv.Atoi(serverWeight)
							end = &config.Endpoint{Name: serverName, Weight: weight, Mode: 1}
						}
					}
					if end != nil { //获取服务地址
						switch event.Type {
						case mvccpb.DELETE:
							callback.UpdateEndpoints(config.DELETE, end)
						case mvccpb.PUT:
							if event.Kv.Version == 1 {
								callback.UpdateEndpoints(config.ADD, end)
							} else {
								callback.UpdateEndpoints(config.UPDATE, end)
							}
						}
					}
				}
			}
		}

	}
}
func (e *etcdDiscover) list(name string) []*config.Endpoint {
	ctx, cancel := context.WithTimeout(e.ctx, time.Second*10)
	defer cancel()
	res, err := e.client.Get(ctx, fmt.Sprintf("%s/backends/%s/servers", e.prefix, name), clientv3.WithPrefix())
	if err != nil {
		logrus.Errorf("list all servers of %s error.%s", name, err.Error())
		return nil
	}
	if res.Count == 0 {
		return nil
	}
	return makeEndpointForKvs(res.Kvs)
}

func makeEndpointForKvs(kvs []*mvccpb.KeyValue) (res []*config.Endpoint) {
	var ends = make(map[string]*config.Endpoint)
	for _, kv := range kvs {
		if strings.HasSuffix(string(kv.Key), "/url") { //获取服务地址
			kstep := strings.Split(string(kv.Key), "/")
			if len(kstep) > 2 {
				serverName := kstep[len(kstep)-2]
				serverURL := string(kv.Value)
				if en, ok := ends[serverName]; ok {
					en.URL = serverURL
				} else {
					ends[serverName] = &config.Endpoint{Name: serverName, URL: serverURL}
				}
			}
		}
		if strings.HasSuffix(string(kv.Key), "/weight") { //获取服务权重
			kstep := strings.Split(string(kv.Key), "/")
			if len(kstep) > 2 {
				serverName := kstep[len(kstep)-2]
				serverWeight := string(kv.Value)
				if en, ok := ends[serverName]; ok {
					var err error
					en.Weight, err = strconv.Atoi(serverWeight)
					if err != nil {
						logrus.Error("get server weight error.", err.Error())
					}
				} else {
					weight, err := strconv.Atoi(serverWeight)
					if err != nil {
						logrus.Error("get server weight error.", err.Error())
					}
					ends[serverName] = &config.Endpoint{Name: serverName, Weight: weight}
				}
			}
		}
	}
	for _, v := range ends {
		res = append(res, v)
	}
	return
}
