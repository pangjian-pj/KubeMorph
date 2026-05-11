package multicluster

import (
	"context"
	"fmt"
	"sync"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MemberClientCache caches Kubernetes clients for member clusters.
//
// Contract:
// - clusterID is the Cluster CR name.
// - controlNamespace is where Cluster CRs and kubeconfig Secrets live.
// - cache is best-effort; callers should handle transient errors and retry.
type MemberClientCache struct {
	mu      sync.RWMutex
	clients map[string]*kubernetes.Clientset
}

func NewMemberClientCache() *MemberClientCache {
	return &MemberClientCache{clients: map[string]*kubernetes.Clientset{}}
}

func (c *MemberClientCache) Get(clusterID string) *kubernetes.Clientset {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.clients == nil {
		return nil
	}
	return c.clients[clusterID]
}

func (c *MemberClientCache) Clear(clusterID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clients == nil {
		return
	}
	delete(c.clients, clusterID)
}

// GetOrBuild returns a cached client for clusterID, or builds it from Cluster CR + kubeconfig Secret.
func (c *MemberClientCache) GetOrBuild(ctx context.Context, k8sClient client.Reader, controlNamespace, clusterID string) (*kubernetes.Clientset, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("clusterID is empty")
	}
	if controlNamespace == "" {
		return nil, fmt.Errorf("controlNamespace is empty")
	}

	if cs := c.Get(clusterID); cs != nil {
		return cs, nil
	}

	var cl corev1alpha1.Cluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterID, Namespace: controlNamespace}, &cl); err != nil {
		return nil, err
	}
	if cl.Spec.SecretRef == "" {
		return nil, fmt.Errorf("cluster %s missing secretRef", clusterID)
	}

	var sec corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: cl.Spec.SecretRef, Namespace: controlNamespace}, &sec); err != nil {
		return nil, err
	}

	kubeconfig := sec.Data["kubeconfig"]
	if len(kubeconfig) == 0 {
		return nil, fmt.Errorf("secret %s missing kubeconfig", cl.Spec.SecretRef)
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}

	newCS, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clients == nil {
		c.clients = map[string]*kubernetes.Clientset{}
	}
	if existing := c.clients[clusterID]; existing != nil {
		return existing, nil
	}
	c.clients[clusterID] = newCS
	return newCS, nil
}
