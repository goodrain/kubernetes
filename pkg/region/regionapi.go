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
	"os/exec"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
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

func init() {
	configMap = make(map[string]string)
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

//FetchContainerIP 查找容器ip.midonet查找eth1(兼容旧版服务时使用，新版cni midonet 使用eth0)
func FetchContainerIP(pid string, processID int) string {
	var netip = ""
	if NetType == "midolnet" {
		if pid != "" {
			command := "ip netns exec " + pid + " ifconfig eth1 | head -n 2 | cut -d: -f2 | awk '{ print $1}' | tail -n 1"
			out, err := exec.Command("/bin/sh", "-c", command).Output()
			if err != nil {
				glog.Errorf("FetchContainerIP is failure")
				out, err = exec.Command("/bin/sh", "-c", command).Output()
				if err != nil {
					return netip
				}
			}
			str := fmt.Sprintf("%s", out)
			netip = strings.Replace(str, "\n", "", -1)
		}
	} else {
		if processID > 0 {
			extractIPCmd := fmt.Sprintf("ip -4 addr show %s | grep inet | awk -F\" \" '{print $2}' | cut -d '/' -f1", "eth0")
			args := []string{"-t", fmt.Sprintf("%d", processID), "-n", "--", "bash", "-c", extractIPCmd}
			command := exec.Command("nsenter", args...)
			out, err := command.CombinedOutput()
			if err == nil {
				str := fmt.Sprintf("%s", out)
				netip = strings.Replace(str, "\n", "", -1)
			}
		} else {
			netip = "1.1.1.1"
		}
	}
	return netip
}

//StopServicePod stop service
func StopServicePod(serviceID string) string {
	var result = ""
	var jsonStr = []byte("{}")
	var url = configMap["Region_service_api"] + "lifecycle/" + serviceID + "/stop-running/"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if err != nil {
		glog.Errorf("StopServicePod error serviceID:%s error:%s", serviceID, err.Error())
	} else {
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			glog.Errorf("StopServicePod Read response body error.%s,"+string(body), err.Error())
		}
		result = "ok"
	}
	return result
}

//StopServicePodByReplicaID stop service
func StopServicePodByReplicaID(replicaID, eventID string) string {
	var result = ""
	var jsonStr = []byte(`{"event_id":"` + eventID + `"}`)
	var url = configMap["Region_service_api"] + "lifecycle/" + replicaID + "/stop-rc-running/"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if err != nil {
		glog.Errorf("StopServicePodByReplicaID error replicaID:%s error:%s", replicaID, err.Error())
	} else {
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			glog.Errorf("StopServicePodByReplicaID Read response body error.%s,"+string(body), err.Error())
		}
		result = "ok"
	}
	return result
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

//Bindingips binding ip
func Bindingips(pod *v1.Pod, HostIP string) {
	eventID := GetEventID(pod)
	var para = fmt.Sprintf(`{"host_ip":"%s","event_id":"%s"}`, HostIP, eventID)
	var jsonStr = []byte(para)
	var url = configMap["Region_service_api"] + "lifecycle/bindings/" + pod.Name
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if err != nil {
		EventLog(pod, "POD绑定机器IP调用错误。", "error")
	} else {
		if resp.Body != nil {
			resp.Body.Close()
		}

	}
}

//UnCreatePod uncreate pod
func UnCreatePod(namespace, name string) {
	var para = "{\"tenant_id\":\"" + namespace + "\"}"
	var jsonStr = []byte(para)

	var url = configMap["Region_service_api"] + "lifecycle/uncreatepod/" + name
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if err != nil {
		glog.Errorf("UnCreatePod name:%s error %s", name, err.Error())
	} else {
		if resp.Body != nil {
			resp.Body.Close()
		}
	}
}

//NotFoundPod not found pod
func NotFoundPod(namespace, name string) {
	var para = "{\"tenant_id\":\"" + namespace + "\"}"
	var jsonStr = []byte(para)

	var url = configMap["Region_service_api"] + "lifecycle/notfoundpod/" + name
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if err != nil {
		glog.Errorf("NotFoundPod name:%s error %s", name, err.Error())
	} else {
		if resp.Body != nil {
			resp.Body.Close()
		}
	}
}

// CreateErrorRc error rc
func CreateErrorRc(tenantID, newRCID, oldRCID string) {
	var para = "{\"tenant_id\":\"" + tenantID + "\",\"new_rc_id\":\"" + newRCID + "\",\"old_rc_id\":\"" + oldRCID + "\"}"
	var jsonStr = []byte(para)
	url := configMap["Region_service_api"] + "lifecycle/error-rc"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if err != nil {
		glog.Errorf("CreateErrorRc newRCID:%s oldRCID:%s, error %s", newRCID, oldRCID, err.Error())
	} else {
		if resp.Body != nil {
			resp.Body.Close()
		}
	}

}

//DownK8sNode 下线节点
func DownK8sNode(hostIP string) {
	var jsonStr = []byte("{}")
	url := configMap["Region_service_api"] + "k8snode/" + hostIP
	req, err := http.NewRequest("DELETE", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if err != nil {
		glog.Errorf("DownK8sNode hostIP:%s, error %s", hostIP, err.Error())
	} else {
		if resp.Body != nil {
			resp.Body.Close()
		}
	}
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
	url := configMap["Region_service_api"] + "lifecycle/event_log"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token 5ca196801173be06c7e6ce41d5f7b3b8071e680a")
	client := &http.Client{
		Timeout: HTTPTimeOut,
	}
	resp, err := client.Do(req)
	if err != nil {
		glog.Error("Post event log to region error", err.Error())
	} else {
		if resp.Body != nil {
			resp.Body.Close()
		}
	}
}

var lock sync.Mutex

var cache = make(map[types.UID]string)

//SetDockerBridgeIP  暂存midonet container eth1 IP
func SetDockerBridgeIP(uid types.UID, ip string) {
	lock.Lock()
	defer lock.Unlock()
	glog.V(2).Infof("set docker bridge container ip %s to cache.", ip)
	cache[uid] = ip
}

//RemoveDockerBridgeIP 移除midonet container eth1 IP
func RemoveDockerBridgeIP(uid types.UID) {
	lock.Lock()
	defer lock.Unlock()
	if _, ok := cache[uid]; ok {
		delete(cache, uid)
		glog.V(2).Infof("remove pod(%s) docker bridge container ip from cache.", uid)
	}
}

//GetDockerBridgeIP 获取midonet container eth1 IP
func GetDockerBridgeIP(uid types.UID) string {
	lock.Lock()
	defer lock.Unlock()
	if _, ok := cache[uid]; ok {
		return cache[uid]
	}
	return ""
}
