/*
Copyright 2017 Goodrain Inc. All rights reserved.

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

package region

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/golang/glog"

	"github.com/Sirupsen/logrus"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/pkg/api/v1"

	"sync"

	"github.com/coreos/etcd/clientv3"
)

var (
	//NetType 网络类型
	NetType = "midonet"
	//CustomFile 配置文件地址
	CustomFile = "/etc/goodrain/grkubelet.conf"
)

//HTTPTimeOut 超时
var HTTPTimeOut = time.Duration(25 * time.Second)

var configMap map[string]string

var eventLogServers = []string{"http://127.0.0.1:6363"}
var etcdV3Endpoints = []string{"127.0.0.1:2379"}
var etcdV2Endpoints = []string{"http://127.0.0.1:2379"}
var minport = 11000
var maxport = 20000

type Custom struct {
	ctx           context.Context
	cancel        context.CancelFunc
	hostPortStore *HostPortStore
}

func GetCustom() *Custom {
	ctx, cancel := context.WithCancel(context.Background())
	return &Custom{
		ctx:    ctx,
		cancel: cancel,
	}
}
func (c *Custom) Start(customFile string, kubelet bool) (err error) {
	ParseConfig(customFile)
	if kubelet {
		c.hostPortStore, err = GetHostPortStore()
		if err != nil {
			glog.Error("start host port store manager error.", err.Error())
			return err
		}
	}
	go func() {
		c.discoverEventServer()
	}()
	return nil
}
func (c *Custom) discoverEventServer() {
	go wait.Until(func() {
		logrus.Info("start discover event server endpoints")
		servers := GetEventLogInstance()
		if servers != nil && len(servers) > 0 {
			eventLogServers = servers
		}
	}, time.Minute*2, c.ctx.Done())
}

func (c *Custom) Stop() {
	c.cancel()
	if c.hostPortStore != nil {
		c.hostPortStore.Stop()
	}
	logrus.Info("Custom manager Stoped")
}

//ParseConfig 解析配置文件
func ParseConfig(customFile string) {
	configMap = make(map[string]string)
	CustomFile = customFile
	f, err := os.Open(CustomFile)
	if err == nil {
		buf := bufio.NewReader(f)
		for {
			line, err := buf.ReadString('\n')
			line = strings.TrimSpace(line)
			arr := strings.Split(line, "=")
			if len(arr) == 2 {
				configMap[strings.TrimSpace(arr[0])] = strings.TrimSpace(arr[1])
			}
			if err != nil {
				if err == io.EOF {
					break
				}
				continue
			}
		}
	}
	if min, ok := configMap["minport"]; ok {
		m, err := strconv.Atoi(min)
		if err == nil {
			minport = m
		}
	}
	if max, ok := configMap["maxport"]; ok {
		m, err := strconv.Atoi(max)
		if err == nil {
			maxport = m
		}
	}
	if etcdv3, ok := configMap["etcdv3"]; ok {
		etcdV3Endpoints = strings.Split(etcdv3, ",")
	}
	if etcdv2, ok := configMap["etcdv2"]; ok {
		etcdV2Endpoints = strings.Split(etcdv2, ",")
	}
	setLogFile()
}
func setLogFile() {
	logpath := "/var/log/kubelet-custom"
	if lf, ok := configMap["logpath"]; ok {
		logpath = lf
	}

	_, err := os.Stat(logpath)
	if os.IsNotExist(err) {
		os.Mkdir(logpath, os.ModeDir)
	}
	logFile, err := os.OpenFile(path.Join(logpath, "custom.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, os.ModePerm)
	if err != nil {
		logrus.Warning("Open log file error. so log will be writed in stderr")
		logrus.SetOutput(os.Stderr)
	} else {
		logrus.SetOutput(logFile)
	}
}

var once sync.Once

//SetNetType 设置网络类型
func SetNetType(netType string) {
	once.Do(func() {
		NetType = netType
	})
}

var defaultHostPortStore *HostPortStore

func GetHostPortStore() (*HostPortStore, error) {
	if defaultHostPortStore != nil {
		return defaultHostPortStore, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   etcdV3Endpoints,
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		logrus.Errorf("release host port error.%s", err.Error())
		cancel()
		return nil, err
	}
	store := &HostPortStore{
		store:  make(chan int, 3),
		ctx:    ctx,
		cancel: cancel,
		cli:    cli,
	}
	defaultHostPortStore = store
	store.Sync()
	go store.Produced()
	return store, nil
}

func (s *HostPortStore) Stop() {
	s.cancel()
	select {
	case port := <-s.store:
		s.ReleaseHostPort(port)
	default:
		s.cli.Close()
	}
}

type HostPortStore struct {
	store           chan int
	ctx             context.Context
	cancel          context.CancelFunc
	cli             *clientv3.Client
	getHostPortLock sync.Mutex
}

func (s *HostPortStore) saveUsedPort(ports []int) error {
	su, err := json.Marshal(ports)
	if err != nil {
		return err
	}
	_, err = s.cli.Put(s.ctx, fmt.Sprintf("/store/hosts/%s/usedport", ReadHostUUID()), string(su))
	if err != nil {
		return err
	}
	return nil
}

func (s *HostPortStore) Consum() int {
	ctx, cancel := context.WithTimeout(s.ctx, time.Second*6)
	defer cancel()
	select {
	case port := <-s.store:
		return port
	case <-ctx.Done():
		logrus.Error("get host map port timeout.")
		return 0
	}
}

func (s *HostPortStore) Sync() {
	ctx, cancel := context.WithTimeout(s.ctx, time.Second*3)
	defer cancel()
	res, err := s.cli.Get(ctx, "/store/pods/", clientv3.WithPrefix())
	if err != nil {
		logrus.Error("get pod host port map info when sync port error.", err.Error())
	}
	if res.Count == 0 {
		return
	}
	var realUsedPort []int
	for _, kv := range res.Kvs {
		if strings.HasSuffix(string(kv.Key), "/mapport") {
			port, err := strconv.Atoi(string(kv.Value))
			if err == nil {
				realUsedPort = append(realUsedPort, port)
			}
		}
	}
	sort.Ints(realUsedPort)
	UniqueSlice(&realUsedPort)
	s.saveUsedPort(realUsedPort)
}

func UniqueSlice(slice *[]int) {
	found := make(map[int]bool)
	total := 0
	for i, val := range *slice {
		if _, ok := found[val]; !ok {
			found[val] = true
			(*slice)[total] = (*slice)[i]
			total++
		}
	}
	*slice = (*slice)[:total]
}

//Produced 生产port
func (s *HostPortStore) Produced() {
	for {
		selectport := s.selectPort()
		if selectport == 0 {
			logrus.Error("Produced can not select a port to be uesd. waitting 3 seconds and retry.")
			time.Sleep(time.Second * 3)
			continue
		}
		select {
		case <-s.ctx.Done():
			return
		case s.store <- selectport:
		}
	}
}

func (s *HostPortStore) selectPort() int {
	s.getHostPortLock.Lock()
	defer s.getHostPortLock.Unlock()
	var selectport int
	res, err := s.cli.Get(s.ctx, fmt.Sprintf("/store/hosts/%s/usedport", ReadHostUUID()))
	if err != nil {
		logrus.Errorf("get port map error.%s", err.Error())
	}
	if res.Count == 0 { //第一个端口分配
		if err := s.saveUsedPort([]int{minport}); err != nil {
			logrus.Errorf("get port map error select port .%s", err.Error())
		}
		selectport = minport
	} else {
		for _, kv := range res.Kvs {
			if string(kv.Key) == fmt.Sprintf("/store/hosts/%s/usedport", ReadHostUUID()) {
				var ports []int
				err = json.Unmarshal(kv.Value, &ports)
				if err != nil {
					logrus.Errorf("get port map error unmarshal used port.%s", err.Error())
				}
				sort.Ints(ports)
				var max int
				if len(ports) > 0 {
					max = ports[len(ports)-1]
				} else {
					max = minport - 1
				}
				if max < maxport {
					ports = append(ports, max+1)
					if err := s.saveUsedPort(ports); err != nil {
						logrus.Errorf("get port map error select port .%s", err.Error())
					}
					selectport = max + 1
				}
				wantselect := minport
				for _, used := range ports {
					if used-wantselect > 0 {
						if err := s.saveUsedPort(append(ports, wantselect)); err != nil {
							logrus.Errorf("get port map error select port .%s", err.Error())
							time.Sleep(time.Second * 3)
							continue
						} else {
							selectport = wantselect
						}
					}
					wantselect = used + 1
				}
			}
		}
	}
	return selectport
}

//ReleaseHostPort 释放端口
func (s *HostPortStore) ReleaseHostPort(releasePorts ...int) {
	s.getHostPortLock.Lock()
	defer s.getHostPortLock.Unlock()
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(s.ctx, time.Second*10)
		defer cancel()
		if len(releasePorts) > 0 {
			res, err := s.cli.Get(ctx, fmt.Sprintf("/store/hosts/%s/usedport", ReadHostUUID()))
			if err != nil {
				logrus.Error("get used port info error when release host port.", err.Error())
				time.Sleep(time.Second * 3)
				continue
			}
			for _, kv := range res.Kvs {
				if string(kv.Key) == fmt.Sprintf("/store/hosts/%s/usedport", ReadHostUUID()) {
					var ports []int
					err = json.Unmarshal(kv.Value, &ports)
					if err != nil {
						logrus.Errorf("get port map error unmarshal used port.%s", err.Error())
						time.Sleep(time.Second * 3)
						continue
					}
					sort.Ints(ports)
					for _, rep := range releasePorts {
						for i := range ports {
							if ports[i] == rep {
								ports = append(ports[:i], ports[i+1:]...)
								break
							}
							if ports[i] > rep {
								break
							}
						}
					}
					su, err := json.Marshal(ports)
					if err != nil {
						logrus.Error("release port marshal error.", err.Error())
						continue
					}
					_, err = s.cli.Put(ctx, fmt.Sprintf("/store/hosts/%s/usedport", ReadHostUUID()), string(su))
					if err != nil {
						logrus.Error("release port put used port info error.", err.Error())
						continue
					}
				}
			}
		}
		break
	}
}

//ReleaseHostPortByPod 释放POD端口
func (s *HostPortStore) ReleaseHostPortByPod(podName string) {
	logrus.Infof("start release host port for pod %s", podName)
	ctx, cancel := context.WithTimeout(s.ctx, time.Second*10)
	defer cancel()
	res, err := s.cli.Get(ctx, fmt.Sprintf("/store/pods/%s/ports", podName), clientv3.WithPrefix())
	if err != nil {
		logrus.Error("get pod host port map info when release port error.", err.Error())
	}
	if res.Count == 0 {
		return
	}
	var releasePort []int
	for _, kv := range res.Kvs {
		logrus.Info(string(kv.Key))
		port, err := strconv.Atoi(string(kv.Value))
		if err == nil {
			releasePort = append(releasePort, port)
		}
	}
	s.ReleaseHostPort(releasePort...)
	if _, err := s.cli.Delete(ctx, fmt.Sprintf("/store/pods/%s/ports", podName), clientv3.WithPrefix()); err != nil {
		logrus.Error("delete pod port map info error.", err.Error())
	}
}

//GetHostPort 获取端口
func (s *HostPortStore) GetHostPort(containerPort string, podName string) string {
	ctx, cancel := context.WithTimeout(s.ctx, time.Second*10)
	defer cancel()
	res, _ := s.cli.Get(ctx, fmt.Sprintf("/store/pod/%s/outerport/%s/mapport", podName, containerPort))
	if res.Count != 0 {
		//释放掉原端口
		s.ReleaseHostPortByPod(podName)
	}
	selectPort := s.Consum()
	_, err := s.cli.Put(ctx, fmt.Sprintf("/store/pods/%s/ports/%s/mapport", podName, containerPort), fmt.Sprintf("%d", selectPort))
	if err != nil {
		logrus.Errorf("get a host port for pod %s and save to etcd error", podName)
		return ""
	}
	logrus.Infof("select host port (%d) for pod %s", selectPort, podName)
	return fmt.Sprintf("%d", selectPort)
}

//HostPortInfo 主机端口
type HostPortInfo struct {
	CtnID         string `json:"ctn_id"`     //container id
	ReplicaID     string `json:"replica_id"` //rc id
	DeployVersion string `json:"deploy_version"`
	PodName       string `json:"pod_name"`
}

//ReadHostUUID get local host uuid
func ReadHostUUID() string {
	var result = "0000-0000-0000"
	if NetType == "midonet" {
		f, err := os.Open(configMap["UUID_file"])
		if err != nil {
			return result
		}
		defer f.Close()
		bfRd := bufio.NewReader(f)
		for {
			line, _ := bfRd.ReadBytes('\n')
			str := string(line)
			if strings.Contains(str, "host_uuid") {
				var uuid = strings.Split(str, "=")[1]
				result = uuid
				result = strings.Replace(result, "\n", "", -1)
				break
			}
		}
	} else {
		return configMap["host_id"]
	}
	return result
}

func parseJSON(body []byte) map[string]string {
	j2 := make(map[string]interface{})
	json.Unmarshal(body, &j2)
	dataMap := map[string]string{}
	for k, v := range j2 {
		switch vv := v.(type) {
		case string:
			dataMap[k] = vv
		case int:
			dataMap[k] = strconv.Itoa(vv)
		case int8:
			dataMap[k] = strconv.Itoa(int(vv))
		case int16:
			dataMap[k] = strconv.Itoa(int(vv))
		case int32:
			dataMap[k] = strconv.Itoa(int(vv))
		case int64:
			dataMap[k] = strconv.Itoa(int(vv))
		case float32:
			dataMap[k] = strconv.Itoa(int(vv))
		case float64:
			dataMap[k] = strconv.Itoa(int(vv))
		default:
			fmt.Println("default=", vv)
		}
	}
	return dataMap
}

//GetEventID 获取操作ID 从环境变量
func GetEventID(pod *v1.Pod) string {
	if len(pod.Spec.Containers) > 0 {
		for _, env := range pod.Spec.Containers[0].Env {
			if env.Name == "EVENT_ID" {
				return env.Value
			}
		}
	}
	return ""
}

//EventLog 日志
func EventLog(pod *v1.Pod, message, level string) {
	eventID := GetEventID(pod)
	var para = "{\"event_id\":\"" + eventID + "\",\"message\":\"" + message + "\",\"time\":\"" + time.Now().Format(time.RFC3339) + "\",\"level\":\"" + level + "\"}"
	var jsonStr = []byte(para)
	for _, add := range eventLogServers {
		url := add + "/event_push"
		if !strings.HasPrefix(url, "http") {
			url = "http://" + url
		}
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
		if err != nil {
			logrus.Error("new send event message request error.", err.Error())
			continue
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			logrus.Error("Send event message to server error.", err.Error())
			continue
		}
		if res != nil && res.StatusCode != 200 {
			rb, _ := ioutil.ReadAll(res.Body)
			logrus.Error("Post EventMessage Error:" + string(rb))
		}
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
		if res != nil && res.StatusCode == 200 {
			break
		}
		continue
	}
}

//GetEventLogInstance 获取EventLogInstance
func GetEventLogInstance() []string {
	var clusterAddress []string
	res, err := http.DefaultClient.Get(fmt.Sprintf("%s/v2/keys/event/instance", etcdV2Endpoints[0]))
	if err != nil {
		logrus.Errorf("Error get docker log instance from etcd: %v", err)
		return nil
	}
	if res != nil && res.Body != nil {
		defer res.Body.Close()
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		logrus.Error("read get instance body data error. ", err.Error())
		return nil
	}
	nodes := gjson.GetBytes(body, "node.nodes").Array()
	if nodes != nil {
		for _, node := range nodes {
			if value, ok := node.Map()["value"]; ok {
				hostIP := gjson.Get(value.String(), "HostIP").String()
				webPort := gjson.Get(value.String(), "WebPort").Int()
				if hostIP != "" && webPort != 0 {
					clusterAddress = append(clusterAddress, fmt.Sprintf("http://%s:%d", hostIP, webPort))
				}
			}
		}
	}
	return clusterAddress
}
