package api

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func listConfigMapsByPrefix(c *gin.Context, logger *zap.Logger, namespace string, namePrefix string) ([]TopologyConfigItem, error) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	kubeconfig := strings.TrimSpace(os.Getenv("KUBEX_KUBERNETES_KUBECONFIG"))
	if kubeconfig == "" {
		return []TopologyConfigItem{}, nil
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		logger.Warn("build kubeconfig failed", zap.Error(err))
		return []TopologyConfigItem{}, nil
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("init kubernetes client failed: %w", err)
	}

	list, err := cs.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list configmaps failed: %w", err)
	}

	items := make([]TopologyConfigItem, 0)
	for _, cm := range list.Items {
		if namePrefix != "" && !strings.HasPrefix(cm.Name, namePrefix) {
			continue
		}
		items = append(items, TopologyConfigItem{Name: cm.Name, Namespace: cm.Namespace, CreationTimestamp: cm.CreationTimestamp})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].CreationTimestamp.After(items[j].CreationTimestamp.Time)
	})
	_ = corev1.ConfigMap{} // ensure corev1 is used (future: filter by label)
	return items, nil
}
