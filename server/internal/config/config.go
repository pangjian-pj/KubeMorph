package config

import (
	"strings"

	"github.com/spf13/viper"
)

type EtcdConfig struct {
	Endpoints          []string `mapstructure:"endpoints"`
	Prefix             string   `mapstructure:"prefix"`
	DialTimeoutSeconds int      `mapstructure:"dial_timeout_seconds"`
}

type Config struct {
	ServerAddr string           `mapstructure:"server_addr"`
	LogLevel   string           `mapstructure:"log_level"`
	Etcd       EtcdConfig       `mapstructure:"etcd"`
	Kubernetes KubernetesConfig `mapstructure:"kubernetes"`
}

// KubernetesConfig 用于配置 kubeX-server 连接“控制平面集群”（安装 kubeX-controller 的集群）。
// 按设计要求：kubeconfig 通过环境变量注入。
// 默认走 in-cluster config；如果设置了 KUBEX_KUBERNETES_KUBECONFIG 从该 path 读取。
type KubernetesConfig struct {
	KubeconfigPath string           `mapstructure:"kubeconfig"`
	Namespace      string           `mapstructure:"namespace"`
	ClusterCRD     ClusterCRDConfig `mapstructure:"cluster_crd"`
}

type ClusterCRDConfig struct {
	Group   string `mapstructure:"group"`
	Version string `mapstructure:"version"`
	Plural  string `mapstructure:"plural"`
}

func Load() (Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.SetEnvPrefix("KUBEX")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("server_addr", ":8080")
	v.SetDefault("log_level", "info")
	v.SetDefault("etcd.endpoints", []string{"http://localhost:2379"})
	v.SetDefault("etcd.prefix", "/kubex")
	v.SetDefault("etcd.dial_timeout_seconds", 5)

	// Kubernetes (control-plane cluster)
	// 通过环境变量 KUBEX_KUBERNETES_KUBECONFIG 注入（可选）；默认使用 in-cluster config。
	v.SetDefault("kubernetes.kubeconfig", "")
	v.SetDefault("kubernetes.namespace", "kubex-system")
	v.SetDefault("kubernetes.cluster_crd.group", "core.kubex.io")
	v.SetDefault("kubernetes.cluster_crd.version", "v1alpha1")
	v.SetDefault("kubernetes.cluster_crd.plural", "clusters")

	_ = v.ReadInConfig() // optional

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
