package kube

import (
	"fmt"

	"k8s.io/client-go/tools/clientcmd"
)

// ExtractServerFromKubeconfig 解析 kubeconfig 内容，取 current-context 关联 cluster 的 server 字段。
// 返回值示例：https://1.2.3.4:6443
func ExtractServerFromKubeconfig(kubeconfigYAML string) (string, error) {
	if kubeconfigYAML == "" {
		return "", fmt.Errorf("kubeconfig is empty")
	}

	cfg, err := clientcmd.Load([]byte(kubeconfigYAML))
	if err != nil {
		return "", fmt.Errorf("load kubeconfig: %w", err)
	}

	currentCtx := cfg.CurrentContext
	if currentCtx == "" {
		return "", fmt.Errorf("kubeconfig.current-context is empty")
	}

	ctx, ok := cfg.Contexts[currentCtx]
	if !ok || ctx == nil {
		return "", fmt.Errorf("kubeconfig context %q not found", currentCtx)
	}
	if ctx.Cluster == "" {
		return "", fmt.Errorf("kubeconfig context %q has empty cluster", currentCtx)
	}

	cl, ok := cfg.Clusters[ctx.Cluster]
	if !ok || cl == nil {
		return "", fmt.Errorf("kubeconfig cluster %q not found", ctx.Cluster)
	}
	if cl.Server == "" {
		return "", fmt.Errorf("kubeconfig cluster %q has empty server", ctx.Cluster)
	}

	return cl.Server, nil
}
