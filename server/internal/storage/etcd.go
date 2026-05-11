package storage

import (
	"context"
	"time"

	"github.com/pangjian-pj/KubeMorph/server/internal/config"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func NewEtcdClient(cfg config.EtcdConfig) (*clientv3.Client, error) {
	return clientv3.New(clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: time.Duration(cfg.DialTimeoutSeconds) * time.Second,
		Context:     context.Background(),
	})
}
