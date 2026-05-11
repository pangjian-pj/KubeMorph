package api

import "github.com/gin-gonic/gin"

// KVHandlerDepKV 是 KV handler 所需的最小存储接口。
// 想扩展为资源 CRUD 时，可以针对不同资源定义更细粒度的接口。

type KVHandlerDepKV interface {
	Put(ctx *gin.Context, key, value string) error
	Get(ctx *gin.Context, key string) (string, bool, error)
	Delete(ctx *gin.Context, key string) (bool, error)
}
