package models

// 这里先作为 kubeX 自定义资源的“服务端表示”占位。
// 后续可以按需要对接 kube-apiserver 的 CRD，或者像当前架构描述一样做“以 etcd 为主存储”的资源模型。

type Cluster struct {
	Name       string `json:"name"`
	Kubeconfig string `json:"kubeconfig"`
	// TODO: region/provider/labels ...
}

type FederatedObject struct {
	Name string `json:"name"`
	// TODO: extracted podTemplate + replicas
}

type OptimizationPolicy struct {
	Name string `json:"name"`
	// TODO: strategy + target objects
}
