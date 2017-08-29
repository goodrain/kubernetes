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
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/kubernetes/pkg/api/v1"

	"sync"

	"github.com/golang/glog"
)

var (
	//NetType 网络类型
	NetType = "midolnet"
	//CustomFile 配置文件地址
	CustomFile = "/etc/goodrain/grkubelet.conf"
)

//HTTPTimeOut 超时
var HTTPTimeOut = time.Duration(25 * time.Second)

var configMap map[string]string

var eventLogServers = []string{"http://127.0.0.1:6363"}

func init() {
	configMap = make(map[string]string)
	servers := GetEventLogInstance()
	if servers != nil && len(servers) > 0 {
		eventLogServers = servers
	}
}

//ParseConfig 解析配置文件
func ParseConfig(customFile string) {
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
}

var once sync.Once

//SetNetType 设置网络类型
func SetNetType(netType string) {
	once.Do(func() {
		NetType = netType
	})
}

//GetDockerPort 获取容器映射端口
//request container port
func GetDockerPort(hostID string, containerPort string, podName string) string {
	var port = ""
	if NetType == "midolnet" {
		var para = "{\"container_port\":\"" + containerPort + "\", \"pod_name\":\"" + podName + "\"}"
		var jsonStr = []byte(para)
		var url = configMap["Region_net_api"] + NetType + "/" + hostID + "/ports"
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
		client := &http.Client{
			Timeout: HTTPTimeOut,
		}
		resp, err := client.Do(req)
		if err != nil {
			glog.Errorf("GetDockerPort from(" + url + ")error." + err.Error())
		} else {
			defer resp.Body.Close()
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				glog.Errorf("GetDockerPort read response body error. Body:%s,Error:%s", string(body), err.Error())
			}
			dataMap := parseJSON(body)
			port = dataMap["port"]
		}
	}
	return port
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

//ReadMidomanUUID get local midolman uuid
func ReadMidomanUUID() string {
	var result = "0000-0000-0000"
	if NetType == "midolnet" {
		f, err := os.Open(configMap["UUID_file"])
		if err != nil {
			return ""
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

//DrainPod drain pod
func DrainPod(podName string) (int, error) {
	var url = configMap["Region_service_api"] + "lifecycle/pods/" + podName
	var para = "{\"action\": \"drain\"}"
	var jsonStr = []byte(para)
	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if err != nil {
		glog.Errorf("DrainPod ERROR,%s", err.Error())
		return 0, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		glog.Errorf("DrainPod read response body error.")
		return 0, err
	}
	dataMap := parseJSON(body)
	return strconv.Atoi(dataMap["sleep"])
}

//NotifyService 通知服务
func NotifyService(pod *v1.Pod) error {
	var version = ""
	if v, ok := pod.Labels["version"]; ok {
		version = v
	}
	netIP := pod.Status.PodIP
	eventID := GetEventID(pod)
	var para = fmt.Sprintf(`{"net_ip":"%s","deploy_version":"%s","event_id":"%s"}`, netIP, version, eventID)
	var jsonStr = []byte(para)
	var url = configMap["Region_net_api"] + NetType + "/" + pod.Name + "/starting"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		EventLog(pod, "POD启动后回调REGION错误，负载均衡将不会写入。"+err.Error(), "error")
		return err
	}
	return nil
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
	res, err := http.DefaultClient.Get("http://127.0.0.1:8888/v1/etcd/event-log/instances")
	if err != nil {
		glog.Errorf("Error get docker log instance from region api: %v", err)
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
