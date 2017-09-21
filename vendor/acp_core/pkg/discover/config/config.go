package config

//Operation 实例操作类型
type Operation int

const (
	ADD Operation = iota
	DELETE
	UPDATE
	SYNC
)

type DiscoverConfig struct {
	EtcdClusterEndpoints []string
}

type Endpoint struct {
	Name   string
	URL    string
	Weight int
	Mode   int //0 表示URL变化，1表示Weight变化 ,2表示全变化
}
