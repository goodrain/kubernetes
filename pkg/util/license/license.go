package license

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"github.com/golang/glog"
)

//Info license 信息
type Info struct {
	Code       string   `json:"code"`
	Company    string   `json:"company"`
	Node       int64    `json:"node"`
	CPU        int64    `json:"cpu"`
	Memory     int64    `json:"memory"`
	Tenant     int64    `json:"tenant"`
	EndTime    string   `json:"end_time"`
	StartTime  string   `json:"start_time"`
	DataCenter int64    `json:"data_center"`
	ModuleList []string `json:"module_list"`
}

var key = []byte("qa123zxswe3532crfvtg123bnhymjuki")

//decrypt 解密算法
func decrypt(key []byte, encrypted string) ([]byte, error) {
	ciphertext, err := base64.RawURLEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < aes.BlockSize {
		return nil, errors.New("ciphertext too short")
	}
	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]
	cfb := cipher.NewCFBDecrypter(block, iv)
	cfb.XORKeyStream(ciphertext, ciphertext)
	return ciphertext, nil
}

//ReadLicenseFromFile 从文件获取license
func ReadLicenseFromFile(licenseFile string) (Info, error) {

	info := Info{}
	//step1 read license file
	_, err := os.Stat(licenseFile)
	if err != nil {
		return info, err
	}
	infoBody, err := ioutil.ReadFile(licenseFile)
	if err != nil {
		return info, errors.New("LICENSE文件不可读")
	}

	//step2 decryption info
	infoData, err := decrypt(key, string(infoBody))
	if err != nil {
		return info, errors.New("LICENSE解密发生错误。")
	}
	err = json.Unmarshal(infoData, &info)
	if err != nil {
		return info, errors.New("解码LICENSE文件发生错误")
	}
	return info, nil
}

//ReadLicenseFromConsole 从控制台api获取license
func ReadLicenseFromConsole(token string, defaultLicense string) (Info, error) {
	var info Info
	req, err := http.NewRequest("GET", "http://console.goodrain.me/api/license", nil)
	if err != nil {
		return info, err
	}
	req.Header.Add("Content-Type", "application/json")
	if token != "" {
		req.Header.Add("Authorization", "Token "+token)
	}
	http.DefaultClient.Timeout = time.Second * 5
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		glog.Info("控制台读取授权失败，使用默认授权。")
		return ReadLicenseFromFile(defaultLicense)
	}
	if res != nil {
		defer res.Body.Close()
		body, err := ioutil.ReadAll(res.Body)
		if err != nil {
			return info, err
		}
		apiInfo := struct {
			Bean string `json:"bean"`
		}{}

		err = json.Unmarshal(body, &apiInfo)
		if err != nil {
			return info, err
		}
		//step2 decryption info
		infoData, err := decrypt(key, apiInfo.Bean)
		if err != nil {
			return info, errors.New("LICENSE解密发生错误。")
		}
		err = json.Unmarshal(infoData, &info)
		if err != nil {
			return info, errors.New("解码LICENSE文件发生错误")
		}
		return info, nil
	}
	return info, errors.New("res body is nil")
}
