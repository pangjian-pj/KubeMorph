package storage

import "github.com/gin-gonic/gin"

// KVStoreAdapter 把 KVStore 适配成 internal/api 期望的接口。

type KVStoreAdapter struct{ Store *KVStore }

func (a KVStoreAdapter) Put(ctx *gin.Context, key, value string) error {
	return a.Store.Put(ctx.Request.Context(), key, value)
}

func (a KVStoreAdapter) Get(ctx *gin.Context, key string) (string, bool, error) {
	return a.Store.Get(ctx.Request.Context(), key)
}

func (a KVStoreAdapter) Delete(ctx *gin.Context, key string) (bool, error) {
	return a.Store.Delete(ctx.Request.Context(), key)
}
