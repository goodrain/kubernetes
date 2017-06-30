package license

import "testing"

func TestReadLicenseFromFile(t *testing.T) {
	info, err := ReadLicenseFromFile("/Users/qingguo/gowork/src/license/北京好雨科技有限公司-goodrain-LICENSE.yb")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(info)
}

func TestReadLicenseFromConsole(t *testing.T) {
	info, err := ReadLicenseFromConsole("", "/Users/qingguo/gowork/src/license/北京好雨科技有限公司-goodrain-LICENSE.yb")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(info)
}
