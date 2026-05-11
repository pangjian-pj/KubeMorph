package storage

import (
	"context"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type KVStore struct {
	client *clientv3.Client
	prefix string
}

func NewKVStore(client *clientv3.Client, prefix string) *KVStore {
	return &KVStore{client: client, prefix: prefix}
}

func (s *KVStore) key(k string) string {
	return fmt.Sprintf("%s/kv/%s", s.prefix, k)
}

func (s *KVStore) Put(ctx context.Context, key, value string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.client.Put(ctx, s.key(key), value)
	return err
}

func (s *KVStore) Get(ctx context.Context, key string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := s.client.Get(ctx, s.key(key))
	if err != nil {
		return "", false, err
	}
	if len(resp.Kvs) == 0 {
		return "", false, nil
	}
	return string(resp.Kvs[0].Value), true, nil
}

func (s *KVStore) Delete(ctx context.Context, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := s.client.Delete(ctx, s.key(key))
	if err != nil {
		return false, err
	}
	return resp.Deleted > 0, nil
}
