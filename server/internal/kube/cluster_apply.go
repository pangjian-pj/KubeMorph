package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	corev1alpha1 "github.com/pangjian-pj/kubeX/kubeX-controller/api/v1alpha1"
)

type ClusterApplier struct {
	Clientset kubernetes.Interface
	Dynamic   dynamic.Interface
	REST      rest.Interface
}

func (a ClusterApplier) GetTypedClusterCR(c *gin.Context, ns string, clusterID string) (*corev1alpha1.Cluster, error) {
	ctx := c.Request.Context()
	var out corev1alpha1.Cluster
	err := a.REST.Get().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "clusters", clusterID).
		Do(ctx).
		Into(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (a ClusterApplier) GetSecret(c *gin.Context, ns string, name string) (*corev1.Secret, error) {
	ctx := c.Request.Context()
	return a.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
}

func (a ClusterApplier) GetNodesFromKubeconfig(ctx context.Context, kubeconfigYAML string) (*corev1.NodeList, error) {
	if kubeconfigYAML == "" {
		return nil, fmt.Errorf("kubeconfig is empty")
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil {
		return nil, fmt.Errorf("build rest config from kubeconfig: %w", err)
	}

	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("new clientset for member cluster: %w", err)
	}

	list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	return list, nil
}

func (a ClusterApplier) ListTypedClusterCRs(c *gin.Context, ns string) (*corev1alpha1.ClusterList, error) {
	ctx := c.Request.Context()
	var list corev1alpha1.ClusterList
	err := a.REST.Get().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "clusters").
		Do(ctx).
		Into(&list)
	if err != nil {
		return nil, err
	}
	return &list, nil
}

func (a ClusterApplier) DeleteTypedClusterCR(c *gin.Context, ns string, clusterID string) error {
	ctx := c.Request.Context()
	return a.REST.Delete().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "clusters", clusterID).
		Do(ctx).
		Error()
}

func (a ClusterApplier) CreateSecret(c *gin.Context, ns string, s *corev1.Secret) (*corev1.Secret, error) {
	ctx := c.Request.Context()
	created, err := a.Clientset.CoreV1().Secrets(ns).Create(ctx, s, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, err
	}

	// already exists -> update
	existing, gerr := a.Clientset.CoreV1().Secrets(ns).Get(ctx, s.Name, metav1.GetOptions{})
	if gerr != nil {
		return nil, gerr
	}
	s.ResourceVersion = existing.ResourceVersion
	return a.Clientset.CoreV1().Secrets(ns).Update(ctx, s, metav1.UpdateOptions{})
}

func (a ClusterApplier) ApplyClusterCR(c *gin.Context, ns string, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	ctx := c.Request.Context()
	res := a.Dynamic.Resource(gvr).Namespace(ns)

	existing, err := res.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, cerr := res.Create(ctx, obj, metav1.CreateOptions{})
			return cerr
		}
		return err
	}

	// update with resourceVersion
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, uerr := res.Update(ctx, obj, metav1.UpdateOptions{})
	if uerr != nil {
		return fmt.Errorf("update cluster %s: %w", obj.GetName(), uerr)
	}
	return nil
}

// ApplyTypedClusterCR creates or updates a typed Cluster CR.
// 用 typed struct 的好处：字段由编译期约束，避免手动拼 JSON key。
// 这里用 RESTClient 直连 apiserver，绕过生成的 typed client（我们没有在 server 侧生成 clientset）。
func (a ClusterApplier) ApplyTypedClusterCR(c *gin.Context, ns string, cluster *corev1alpha1.Cluster) error {
	ctx := c.Request.Context()

	// 强制 namespace（避免调用方漏填）
	cluster.Namespace = ns

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// 先 GET，拿到 resourceVersion
		var existing corev1alpha1.Cluster
		err := a.REST.Get().
			AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "clusters", cluster.Name).
			Do(ctx).
			Into(&existing)
		if err != nil {
			if apierrors.IsNotFound(err) {
				body, merr := json.Marshal(cluster)
				if merr != nil {
					return merr
				}
				return a.REST.Post().
					AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "clusters").
					Body(bytes.NewReader(body)).
					SetHeader("Content-Type", "application/json").
					Do(ctx).
					Error()
			}
			return err
		}

		cluster.ResourceVersion = existing.ResourceVersion
		body, merr := json.Marshal(cluster)
		if merr != nil {
			return merr
		}
		return a.REST.Put().
			AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "clusters", cluster.Name).
			Body(bytes.NewReader(body)).
			SetHeader("Content-Type", "application/json").
			Do(ctx).
			Error()
	})
}

// ApplyTypedGlobalDeploymentCR creates or updates a typed GlobalDeployment CR.
// We use RESTClient to avoid generating typed clients in kubeX-server.
func (a ClusterApplier) ApplyTypedGlobalDeploymentCR(c *gin.Context, ns string, gd *corev1alpha1.GlobalDeployment) error {
	ctx := c.Request.Context()
	gd.Namespace = ns

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var existing corev1alpha1.GlobalDeployment
		err := a.REST.Get().
			AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "globaldeployments", gd.Name).
			Do(ctx).
			Into(&existing)
		if err != nil {
			if apierrors.IsNotFound(err) {
				body, merr := json.Marshal(gd)
				if merr != nil {
					return merr
				}
				return a.REST.Post().
					AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "globaldeployments").
					Body(bytes.NewReader(body)).
					SetHeader("Content-Type", "application/json").
					Do(ctx).
					Error()
			}
			return err
		}

		gd.ResourceVersion = existing.ResourceVersion
		body, merr := json.Marshal(gd)
		if merr != nil {
			return merr
		}
		return a.REST.Put().
			AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "globaldeployments", gd.Name).
			Body(bytes.NewReader(body)).
			SetHeader("Content-Type", "application/json").
			Do(ctx).
			Error()
	})
}

func (a ClusterApplier) ListTypedGlobalDeploymentCRs(c *gin.Context, ns string) (*corev1alpha1.GlobalDeploymentList, error) {
	ctx := c.Request.Context()
	var list corev1alpha1.GlobalDeploymentList
	err := a.REST.Get().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "globaldeployments").
		Do(ctx).
		Into(&list)
	if err != nil {
		return nil, err
	}
	return &list, nil
}

func (a ClusterApplier) GetTypedGlobalDeploymentCR(c *gin.Context, ns string, name string) (*corev1alpha1.GlobalDeployment, error) {
	ctx := c.Request.Context()
	var out corev1alpha1.GlobalDeployment
	err := a.REST.Get().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "globaldeployments", name).
		Do(ctx).
		Into(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (a ClusterApplier) ListTypedReplicaBindingCRs(c *gin.Context, ns string) (*corev1alpha1.ReplicaBindingList, error) {
	ctx := c.Request.Context()
	var list corev1alpha1.ReplicaBindingList
	err := a.REST.Get().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "replicabindings").
		Do(ctx).
		Into(&list)
	if err != nil {
		return nil, err
	}
	return &list, nil
}

func (a ClusterApplier) DeleteTypedReplicaBindingCR(c *gin.Context, ns string, name string) error {
	ctx := c.Request.Context()
	return a.REST.Delete().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "replicabindings", name).
		Do(ctx).
		Error()
}

func (a ClusterApplier) DeleteTypedGlobalDeploymentCR(c *gin.Context, ns string, name string) error {
	ctx := c.Request.Context()
	return a.REST.Delete().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "globaldeployments", name).
		Do(ctx).
		Error()
}

// ---- OptimizationPolicy ----

func (a ClusterApplier) ApplyTypedOptimizationPolicyCR(c *gin.Context, ns string, pol *corev1alpha1.OptimizationPolicy) error {
	ctx := c.Request.Context()
	pol.Namespace = ns

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var existing corev1alpha1.OptimizationPolicy
		err := a.REST.Get().
			AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "optimizationpolicies", pol.Name).
			Do(ctx).
			Into(&existing)
		if err != nil {
			if apierrors.IsNotFound(err) {
				body, merr := json.Marshal(pol)
				if merr != nil {
					return merr
				}
				return a.REST.Post().
					AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "optimizationpolicies").
					Body(bytes.NewReader(body)).
					SetHeader("Content-Type", "application/json").
					Do(ctx).
					Error()
			}
			return err
		}

		pol.ResourceVersion = existing.ResourceVersion
		body, merr := json.Marshal(pol)
		if merr != nil {
			return merr
		}
		return a.REST.Put().
			AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "optimizationpolicies", pol.Name).
			Body(bytes.NewReader(body)).
			SetHeader("Content-Type", "application/json").
			Do(ctx).
			Error()
	})
}

func (a ClusterApplier) GetTypedOptimizationPolicyCR(c *gin.Context, ns, name string) (*corev1alpha1.OptimizationPolicy, error) {
	ctx := c.Request.Context()
	var out corev1alpha1.OptimizationPolicy
	err := a.REST.Get().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "optimizationpolicies", name).
		Do(ctx).
		Into(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (a ClusterApplier) ListTypedOptimizationPolicyCRs(c *gin.Context, ns string) (*corev1alpha1.OptimizationPolicyList, error) {
	ctx := c.Request.Context()
	var list corev1alpha1.OptimizationPolicyList
	err := a.REST.Get().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "optimizationpolicies").
		Do(ctx).
		Into(&list)
	if err != nil {
		return nil, err
	}
	return &list, nil
}

func (a ClusterApplier) DeleteTypedOptimizationPolicyCR(c *gin.Context, ns, name string) error {
	ctx := c.Request.Context()
	return a.REST.Delete().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "optimizationpolicies", name).
		Do(ctx).
		Error()
}

// ---- ReOrchestrationPlan ----

func (a ClusterApplier) GetTypedReOrchestrationPlanCR(c *gin.Context, ns, name string) (*corev1alpha1.ReOrchestrationPlan, error) {
	ctx := c.Request.Context()
	var out corev1alpha1.ReOrchestrationPlan
	err := a.REST.Get().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "reorchestrationplans", name).
		Do(ctx).
		Into(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (a ClusterApplier) ListTypedReOrchestrationPlanCRs(c *gin.Context, ns string) (*corev1alpha1.ReOrchestrationPlanList, error) {
	ctx := c.Request.Context()
	var list corev1alpha1.ReOrchestrationPlanList
	err := a.REST.Get().
		AbsPath("/apis", "core.kubex.io", "v1alpha1", "namespaces", ns, "reorchestrationplans").
		Do(ctx).
		Into(&list)
	if err != nil {
		return nil, err
	}
	return &list, nil
}
