package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

type Dependencies struct {
	Logger           *zap.Logger
	KV               KVHandlerDepKV
	Etcd             *clientv3.Client
	Cluster          ClusterDeps
	GlobalDeployment GlobalDeploymentDeps
	Optimization     OptimizationDeps
}

type ClusterDeps struct {
	EtcdPrefix string

	Namespace string
	CRDGroup  string
	CRDVer    string
	CRDPlural string
	Kube      ClusterHandlerKube
}

type GlobalDeploymentDeps struct {
	Namespace string
	Kube      GlobalDeploymentHandlerKube
}

type OptimizationDeps struct {
	Namespace string
	Kube      OptimizationHandlerKube
	// For topology list. Prefer this namespace when reading topology configmaps.
	TopologyNamespace string
}

// 为了避免 handler 直接依赖 storage 包，这里用一个最小接口封装。
// 当前只用于示例 KV CRUD。

// 这样 internal/api 不需要 import internal/storage（保持分层清晰）。

func NewRouter(dep Dependencies) *gin.Engine {
	g := gin.New()
	g.Use(gin.Recovery())
	g.Use(requestLogger(dep.Logger))

	g.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	g.GET("/readyz", func(c *gin.Context) {
		ctx := c.Request.Context()
		endpoints := dep.Etcd.Endpoints()
		if len(endpoints) == 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{"ready": false, "error": "no etcd endpoints"})
			return
		}
		_, err := dep.Etcd.Status(ctx, endpoints[0])
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"ready": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ready": true})
	})

	v1 := g.Group("/api/v1")
	{
		kv := NewKVHandler(dep.Logger, dep.KV)
		kv.Register(v1)

		clusters := NewClusterHandler(dep.Logger, dep.Etcd, ClusterHandlerConfig{
			EtcdPrefix: dep.Cluster.EtcdPrefix,
			Namespace:  dep.Cluster.Namespace,
			CRDGroup:   dep.Cluster.CRDGroup,
			CRDVer:     dep.Cluster.CRDVer,
			CRDPlural:  dep.Cluster.CRDPlural,
		}, dep.Cluster.Kube)
		clusters.Register(v1)

		gds := NewGlobalDeploymentHandler(dep.Logger, GlobalDeploymentHandlerConfig{
			Namespace: dep.GlobalDeployment.Namespace,
		}, dep.GlobalDeployment.Kube)
		gds.Register(v1)
		gds.RegisterGet(v1)
		gds.RegisterList(v1)
		gds.RegisterDelete(v1)

		opt := NewOptimizationHandler(dep.Logger, OptimizationHandlerConfig{
			Namespace:         dep.Optimization.Namespace,
			TopologyNamespace: dep.Optimization.TopologyNamespace,
		}, dep.Optimization.Kube)
		opt.Register(v1)
	}

	return g
}
