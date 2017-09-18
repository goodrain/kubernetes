package region

import "testing"
import "fmt"

func TestGetPortMap(t *testing.T) {
	store, err := GetHostPortStore()
	if err != nil {
		t.Fatal(err)
	}
	port := store.GetHostPort("80", "xxx-46")
	t.Log(port)
	port = store.GetHostPort("81", "xxx-46")
	t.Log(port)
	port = store.GetHostPort("80", "xxx-47")
	t.Log(port)
	port = store.GetHostPort("81", "xxx-47")
	t.Log(port)
	port = store.GetHostPort("80", "xxx-48")
	t.Log(port)
	port = store.GetHostPort("81", "xxx-48")
	t.Log(port)
}

func BenchmarkGetPortMap(b *testing.B) {
	store, err := GetHostPortStore()
	if err != nil {
		b.Fatal(err)
	}
	var port string
	for i := 0; i < b.N; i++ {
		port = store.GetHostPort("80", fmt.Sprintf("xxx-%d", i))
		if port == "0" {
			b.Fail()
		}
	}
	b.Log(port)
}

func TestReleaseHostPort(t *testing.T) {
	store, err := GetHostPortStore()
	if err != nil {
		t.Fatal(err)
	}
	store.ReleaseHostPortByPod("xxx-46")
}
