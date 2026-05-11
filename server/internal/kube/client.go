package kube

import (
	"fmt"

	"github.com/pangjian-pj/kubeX/kubeX-server/internal/config"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Clients struct {
	RestConfig *rest.Config
	Clientset  kubernetes.Interface
	Dynamic    dynamic.Interface
	RESTClient rest.Interface
}

func NewClients(cfg config.KubernetesConfig) (Clients, error) {
	var (
		restCfg *rest.Config
		err     error
	)

	if cfg.KubeconfigPath != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", cfg.KubeconfigPath)
		if err != nil {
			return Clients{}, fmt.Errorf("build kubeconfig from %q: %w", cfg.KubeconfigPath, err)
		}
	} else {
		restCfg, err = rest.InClusterConfig()
		if err != nil {
			return Clients{}, fmt.Errorf("build in-cluster config: %w", err)
		}
	}

	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return Clients{}, fmt.Errorf("new clientset: %w", err)
	}

	dc, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return Clients{}, fmt.Errorf("new dynamic client: %w", err)
	}

	// RESTClientFor 需要在 config 上显式提供 GroupVersion + serializer。
	// 引入这个RESTClient的作用是：不生成 typed clientset 的情况下，用“HTTP 风格”的方式直接调用 APIServer 的 CRD REST 路径。简单来说，就是用来操作CRD的。
	crdGV := schema.GroupVersion{Group: "core.kubex.io", Version: "v1alpha1"}
	crdRestCfg := rest.CopyConfig(restCfg)
	crdRestCfg.GroupVersion = &crdGV
	crdRestCfg.APIPath = "/apis"
	crdRestCfg.NegotiatedSerializer = serializer.NewCodecFactory(runtime.NewScheme())
	crdRestCfg.UserAgent = rest.DefaultKubernetesUserAgent()

	rc, err := rest.RESTClientFor(crdRestCfg)
	if err != nil {
		return Clients{}, fmt.Errorf("new rest client: %w", err)
	}

	return Clients{RestConfig: restCfg, Clientset: cs, Dynamic: dc, RESTClient: rc}, nil
}
