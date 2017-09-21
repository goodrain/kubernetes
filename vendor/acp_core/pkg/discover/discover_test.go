package discover

import (
	"acp_core/pkg/discover/config"
	"testing"

	"github.com/Sirupsen/logrus"
)

func TestAddUpdateProject(t *testing.T) {
	discover, err := GetDiscover(config.DiscoverConfig{
		EtcdClusterEndpoints: []string{"127.0.0.1:2379"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer discover.Stop()
	discover.AddUpdateProject("test", callbackupdate{})
}
func TestAddProject(t *testing.T) {
	discover, err := GetDiscover(config.DiscoverConfig{
		EtcdClusterEndpoints: []string{"127.0.0.1:2379"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer discover.Stop()
	discover.AddProject("test", callback{})
}

type callbackupdate struct {
	callback
}

func (c callbackupdate) UpdateEndpoints(operation config.Operation, endpoints ...*config.Endpoint) {
	logrus.Info(operation, "////", endpoints)
}

type callback struct {
}

func (c callback) UpdateEndpoints(endpoints ...*config.Endpoint) {
	for _, en := range endpoints {
		logrus.Infof("%+v", en)
	}
}

//when watch occurred error,will exec this method
func (c callback) Error(err error) {
	logrus.Error(err.Error())
}
