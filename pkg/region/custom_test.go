package region

import "testing"
import "fmt"

func TestGetPortMap(t *testing.T) {
	port := GetHostPortMap("80", "xxx-46")
	t.Log(port)
}

func BenchmarkGetPortMap(b *testing.B) {
	var port string
	for i := 0; i < b.N; i++ {
		port = GetHostPortMap("80", fmt.Sprintf("xxx-%d", i))
		if port == "0" {
			b.Fail()
		}
	}
	b.Log(port)
}

func TestReleaseHostPort(t *testing.T) {
	ReleaseHostPort("xxx-46")
}
