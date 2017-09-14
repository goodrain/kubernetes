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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"

	"k8s.io/kubernetes/pkg/api/v1"

	"sync"

	"github.com/coreos/etcd/clientv3"
	"github.com/golang/glog"
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
var etcdEndpoints = []string{"127.0.0.1:2379"}
var minport = 11000
var maxport = 20000

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
	if etcd, ok := configMap["etcd"]; ok {
		etcdEndpoints = strings.Split(etcd, ",")
	}
	go func() {
		servers := GetEventLogInstance()
		if servers != nil && len(servers) > 0 {
			eventLogServers = servers
		}
	}()
}

var once sync.Once

//SetNetType 设置网络类型
func SetNetType(netType string) {
	once.Do(func() {
		NetType = netType
	})
}

//GetHostPortMap 端口映射分配
func GetHostPortMap(containerPort string, podName string) string {
	for i := 0; i < 3; i++ {
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   etcdEndpoints,
			DialTimeout: 10 * time.Second,
		})
		if err != nil {
			glog.Errorf("get port map error.%s", err.Error())
			time.Sleep(time.Second * 3)
			continue
		}
		defer cli.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		res, _ := cli.Get(ctx, fmt.Sprintf("/store/pod/%s/outerport/%s/mapport", podName, containerPort))
		if res.Count != 0 {
			//释放掉原端口
			ReleaseHostPort(podName)
		}
		res, err = cli.Get(ctx, fmt.Sprintf("/store/host/%s/usedport", ReadHostUUID()))
		if err != nil {
			glog.Errorf("get port map error.%s", err.Error())
			time.Sleep(time.Second * 3)
			continue
		}
		if res.Count == 0 { //第一个端口分配
			if err := selectPort(ctx, cli, strconv.Itoa(minport), podName, containerPort, []int{minport}); err != nil {
				glog.Errorf("get port map error select port .%s", err.Error())
				time.Sleep(time.Second * 3)
				continue
			}
			return strconv.Itoa(minport)
		}
		for _, kv := range res.Kvs {
			if string(kv.Key) == fmt.Sprintf("/store/host/%s/usedport", ReadHostUUID()) {
				var ports []int
				err = json.Unmarshal(kv.Value, &ports)
				if err != nil {
					glog.Errorf("get port map error unmarshal used port.%s", err.Error())
					time.Sleep(time.Second * 3)
					continue
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
					if err := selectPort(ctx, cli, fmt.Sprintf("%d", max+1), podName, containerPort, ports); err != nil {
						glog.Errorf("get port map error select port .%s", err.Error())
						time.Sleep(time.Second * 3)
						continue
					}
					return fmt.Sprintf("%d", max+1)
				}
				wantselect := minport
				for _, used := range ports {
					if used-wantselect > 0 {
						if err := selectPort(ctx, cli, fmt.Sprintf("%d", wantselect), podName, containerPort, append(ports, wantselect)); err != nil {
							glog.Errorf("get port map error select port .%s", err.Error())
							time.Sleep(time.Second * 3)
							continue
						} else {
							return fmt.Sprintf("%d", wantselect)
						}
					}
					wantselect = used + 1
				}
			}
		}
	}
	glog.Errorf("can not select a map port for pod %s port %s", podName, containerPort)
	return "0"
}

//ReleaseHostPort 释放POD 使用的端口
func ReleaseHostPort(podName string) {
	for i := 0; i < 3; i++ {
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   etcdEndpoints,
			DialTimeout: 10 * time.Second,
		})
		if err != nil {
			glog.Errorf("release host port error.%s", err.Error())
			time.Sleep(time.Second * 3)
			continue
		}
		defer cli.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		res, err := cli.Get(ctx, fmt.Sprintf("/store/pod/%s/outerport", podName), clientv3.WithRange("usedport"))
		if err != nil {
			glog.Error("get pod host port map info error.", err.Error())
			time.Sleep(3 * time.Second)
			continue
		}
		var releasePort []int
		for _, kv := range res.Kvs {
			port, err := strconv.Atoi(string(kv.Value))
			if err == nil {
				releasePort = append(releasePort, port)
			}
		}
		if len(releasePort) > 0 {
			res, err := cli.Get(ctx, fmt.Sprintf("/store/host/%s/usedport", ReadHostUUID()))
			if err != nil {
				glog.Error("delete pod port map info error.", err.Error())
				time.Sleep(time.Second * 3)
				continue
			}
			for _, kv := range res.Kvs {
				if string(kv.Key) == fmt.Sprintf("/store/host/%s/usedport", ReadHostUUID()) {
					var ports []int
					err = json.Unmarshal(kv.Value, &ports)
					if err != nil {
						glog.Errorf("get port map error unmarshal used port.%s", err.Error())
						time.Sleep(time.Second * 3)
						continue
					}
					sort.Ints(ports)
					for _, rep := range releasePort {
						for i := range ports {
							if ports[i] == rep {
								if i == 0 {
									if len(ports) > 1 {
										ports = ports[1 : len(ports)-1]
									} else {
										ports = []int{}
									}
								} else if i < len(ports)-1 {
									ports = append(ports[0:i], ports[i+1:len(ports)-1]...)
								} else {
									ports = ports[0 : len(ports)-1]
								}
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
					_, err = cli.Put(ctx, fmt.Sprintf("/store/host/%s/usedport", ReadHostUUID()), string(su))
					if err != nil {
						logrus.Error("release port put used port info error.", err.Error())
						continue
					}
				}
			}
		}
		if _, err := cli.Delete(ctx, fmt.Sprintf("/store/pod/%s/outerport", podName), clientv3.WithRange("usedport")); err != nil {
			glog.Error("delete pod port map info error.", err.Error())
		}
		break
	}
}

func selectPort(ctx context.Context, cli *clientv3.Client, selectPort, podName, containerPort string, ports []int) error {
	su, err := json.Marshal(ports)
	if err != nil {
		return err
	}
	_, err = cli.Put(ctx, fmt.Sprintf("/store/host/%s/usedport", ReadHostUUID()), string(su))
	if err != nil {
		return err
	}
	_, err = cli.Put(ctx, fmt.Sprintf("/store/pod/%s/outerport/%s/mapport", podName, containerPort), selectPort)
	if err != nil {
		return err
	}
	return nil
}

//HostPortInfo 主机端口
type HostPortInfo struct {
	CtnID         string `json:"ctn_id"`     //container id
	ReplicaID     string `json:"replica_id"` //rc id
	DeployVersion string `json:"deploy_version"`
	PodName       string `json:"pod_name"`
}

// PostContainerIDNew 推送容器端口
//register container info
func PostContainerIDNew(hostID, portNumber, containerID, replicaID, deployVersion, podName string) string {
	var result = ""
	var url = configMap["Region_net_api"] + NetType + "/" + hostID + "/ports/" + portNumber
	s := HostPortInfo{
		CtnID:         containerID,
		ReplicaID:     replicaID,
		DeployVersion: deployVersion,
		PodName:       podName,
	}
	var jsonStr, _ = json.Marshal(s)
	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if err != nil {
		glog.Errorf("PostContainerIdNew Error %s,URL:%s", err.Error(), url)
	} else {
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			glog.Errorf("PostContainerIdNew Read body error.%s", string(body))
		}
		result = "ok"
	}
	return result
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
			glog.Error("new send event message request error.", err.Error())
			continue
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			glog.Error("Send event message to server error.", err.Error())
			continue
		}
		if res != nil && res.StatusCode != 200 {
			rb, _ := ioutil.ReadAll(res.Body)
			glog.Error("Post EventMessage Error:" + string(rb))
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
	res, err := http.DefaultClient.Get(fmt.Sprintf("%s/event/instance", etcdEndpoints[0]))
	if err != nil {
		glog.Errorf("Error get docker log instance from etcd: %v", err)
		return nil
	}
	var instances = struct {
		Data struct {
			Instance []struct {
				HostIP  string
				WebPort int
			} `json:"instance"`
		} `json:"data"`
		OK bool `json:"ok"`
	}{}
	if res != nil && res.Body != nil {
		defer res.Body.Close()
		err = json.NewDecoder(res.Body).Decode(&instances)
		if err != nil {
			glog.Errorf("Error Decode instance info: %v", err)
			return nil
		}
		if len(instances.Data.Instance) > 0 {
			for _, ins := range instances.Data.Instance {
				if ins.HostIP != "" && ins.WebPort != 0 {
					clusterAddress = append(clusterAddress, fmt.Sprintf("http://%s:%d", ins.HostIP, ins.WebPort))
				}
			}
		}
	}
	return clusterAddress
}
