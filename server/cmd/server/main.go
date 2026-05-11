package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pangjian-pj/KubeMorph/server/internal/api"
	"github.com/pangjian-pj/KubeMorph/server/internal/config"
	"github.com/pangjian-pj/KubeMorph/server/internal/kube"
	"github.com/pangjian-pj/KubeMorph/server/internal/logging"
	"github.com/pangjian-pj/KubeMorph/server/internal/storage"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		panic(err)
	}
	defer func() { _ = logger.Sync() }()

	etcdClient, err := storage.NewEtcdClient(cfg.Etcd)
	if err != nil {
		logger.Fatal("failed to init etcd client", zap.Error(err))
	}
	defer func() { _ = etcdClient.Close() }()

	kvStore := storage.NewKVStore(etcdClient, cfg.Etcd.Prefix)

	kubeClients, err := kube.NewClients(cfg.Kubernetes)
	if err != nil {
		logger.Fatal("failed to init kubernetes clients", zap.Error(err))
	}

	clusterApplier := kube.ClusterApplier{Clientset: kubeClients.Clientset, Dynamic: kubeClients.Dynamic, REST: kubeClients.RESTClient}

	router := api.NewRouter(api.Dependencies{
		Logger: logger,
		KV:     storage.KVStoreAdapter{Store: kvStore},
		Etcd:   etcdClient,
		Cluster: api.ClusterDeps{
			EtcdPrefix: cfg.Etcd.Prefix,
			Namespace:  cfg.Kubernetes.Namespace,
			CRDGroup:   cfg.Kubernetes.ClusterCRD.Group,
			CRDVer:     cfg.Kubernetes.ClusterCRD.Version,
			CRDPlural:  cfg.Kubernetes.ClusterCRD.Plural,
			Kube:       clusterApplier,
		},
		GlobalDeployment: api.GlobalDeploymentDeps{
			Namespace: cfg.Kubernetes.Namespace,
			Kube:      clusterApplier,
		},
		Optimization: api.OptimizationDeps{
			Namespace:         cfg.Kubernetes.Namespace,
			TopologyNamespace: cfg.Kubernetes.Namespace,
			Kube:              clusterApplier,
		},
	})

	srv := &http.Server{
		Addr:              cfg.ServerAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("server listening", zap.String("addr", cfg.ServerAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("server crashed", zap.Error(err))
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger.Info("shutting down")
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", zap.Error(err))
	}
}
