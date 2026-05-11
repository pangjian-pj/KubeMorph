/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
)

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	mu          sync.RWMutex
	kubeClients map[string]*kubernetes.Clientset // clusterID -> client
}

// +kubebuilder:rbac:groups=core.kubex.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.kubex.io,resources=clusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.kubex.io,resources=clusters/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Cluster object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if r.kubeClients == nil {
		r.kubeClients = map[string]*kubernetes.Clientset{}
	}

	var cluster corev1alpha1.Cluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Default phase
	if cluster.Status.Phase == "" {
		cluster.Status.Phase = corev1alpha1.ClusterPhaseImporting
	}

	// Validate required fields
	if cluster.Spec.APIEndpoint == "" || cluster.Spec.SecretRef == "" {
		setCondition(&cluster, corev1alpha1.ConditionTypeImported, metav1.ConditionFalse, "InvalidSpec", "apiEndpoint/secretRef is required")
		setCondition(&cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "InvalidSpec", "apiEndpoint/secretRef is required")
		cluster.Status.Phase = corev1alpha1.ClusterPhaseFailed
		touchStatus(&cluster)
		r.clearClient(cluster.Name)
		if err := r.Status().Update(ctx, &cluster); err != nil {
			return ctrl.Result{}, err
		}
		// Failed: 不再调谐
		return ctrl.Result{}, nil
	}

	switch cluster.Status.Phase {
	case corev1alpha1.ClusterPhaseFailed:
		// Failed: 不再调谐
		r.clearClient(cluster.Name)
		return ctrl.Result{}, nil
	case corev1alpha1.ClusterPhaseImporting:
		res, err := r.reconcileImporting(ctx, &cluster)
		if err != nil {
			log.Error(err, "reconcile importing failed")
		}
		return res, err
	case corev1alpha1.ClusterPhaseReady:
		res, err := r.reconcileReady(ctx, &cluster)
		if err != nil {
			log.Error(err, "reconcile ready failed")
		}
		return res, err
	case corev1alpha1.ClusterPhaseNotReady:
		res, err := r.reconcileNotReady(ctx, &cluster)
		if err != nil {
			log.Error(err, "reconcile notready failed")
		}
		return res, err
	default:
		cluster.Status.Phase = corev1alpha1.ClusterPhaseImporting
		touchStatus(&cluster)
		if err := r.Status().Update(ctx, &cluster); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
}

func (r *ClusterReconciler) reconcileImporting(ctx context.Context, cluster *corev1alpha1.Cluster) (ctrl.Result, error) {
	// Importing: 获取 secret -> 构建 kubeclient(缓存) -> 测试连通性 -> Ready/Failed
	sec, err := r.getKubeconfigSecret(ctx, cluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			msg := "referenced secret not found"
			setCondition(cluster, corev1alpha1.ConditionTypeImported, metav1.ConditionFalse, "SecretNotFound", msg)
			setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "SecretNotFound", msg)
			cluster.Status.Phase = corev1alpha1.ClusterPhaseNotReady
			touchStatus(cluster)
			_ = r.Status().Update(ctx, cluster)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	cs, err := r.getOrBuildClient(cluster.Name, sec)
	if err != nil {
		setCondition(cluster, corev1alpha1.ConditionTypeImported, metav1.ConditionFalse, "InvalidKubeconfig", err.Error())
		setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "InvalidKubeconfig", err.Error())
		cluster.Status.Phase = corev1alpha1.ClusterPhaseFailed
		touchStatus(cluster)
		r.clearClient(cluster.Name)
		_ = r.Status().Update(ctx, cluster)
		return ctrl.Result{}, nil
	}

	// 连通性测试：GET /version
	_, err = cs.Discovery().ServerVersion()
	if err != nil {
		setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "Unreachable", err.Error())
		cluster.Status.Phase = corev1alpha1.ClusterPhaseFailed
		touchStatus(cluster)
		r.clearClient(cluster.Name)
		_ = r.Status().Update(ctx, cluster)
		// Failed: 不再调谐
		return ctrl.Result{}, nil
	}

	setCondition(cluster, corev1alpha1.ConditionTypeImported, metav1.ConditionTrue, "Imported", "cluster credentials secret found")
	setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionTrue, "Reachable", "api server reachable")
	cluster.Status.Phase = corev1alpha1.ClusterPhaseReady
	touchStatus(cluster)
	if err := r.Status().Update(ctx, cluster); err != nil {
		return ctrl.Result{}, err
	}
	// Importing -> 立即
	return ctrl.Result{Requeue: true}, nil
}

func (r *ClusterReconciler) reconcileReady(ctx context.Context, cluster *corev1alpha1.Cluster) (ctrl.Result, error) {
	// Ready: 监控健康 + 同步资源
	sec, err := r.getKubeconfigSecret(ctx, cluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "SecretNotFound", "referenced secret not found")
			cluster.Status.Phase = corev1alpha1.ClusterPhaseNotReady
			r.clearClient(cluster.Name)
			touchStatus(cluster)
			_ = r.Status().Update(ctx, cluster)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	cs, err := r.getOrBuildClient(cluster.Name, sec)
	if err != nil {
		setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "InvalidKubeconfig", err.Error())
		cluster.Status.Phase = corev1alpha1.ClusterPhaseNotReady
		r.clearClient(cluster.Name)
		touchStatus(cluster)
		_ = r.Status().Update(ctx, cluster)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 健康检测
	if _, err := cs.Discovery().ServerVersion(); err != nil {
		setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "Unreachable", err.Error())
		cluster.Status.Phase = corev1alpha1.ClusterPhaseNotReady
		touchStatus(cluster)
		_ = r.Status().Update(ctx, cluster)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionTrue, "Reachable", "api server reachable")

	// 资源统计：节点容量/可分配 + Pod 数
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		// 资源同步失败也视为 NotReady（与设计一致：API 不可达/不可用）
		setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "ListNodesFailed", err.Error())
		cluster.Status.Phase = corev1alpha1.ClusterPhaseNotReady
		touchStatus(cluster)
		_ = r.Status().Update(ctx, cluster)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "ListPodsFailed", err.Error())
		cluster.Status.Phase = corev1alpha1.ClusterPhaseNotReady
		touchStatus(cluster)
		_ = r.Status().Update(ctx, cluster)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	capacityCPU, capacityMem, allocCPU, allocMem := sumNodeResources(nodes)
	cluster.Status.Resources.Capacity.CPU = capacityCPU
	cluster.Status.Resources.Capacity.Memory = capacityMem
	cluster.Status.Resources.Allocatable.CPU = allocCPU
	cluster.Status.Resources.Allocatable.Memory = allocMem
	cluster.Status.Resources.NodeCount = int64(len(nodes.Items))
	cluster.Status.Resources.PodCount = int64(len(pods.Items))

	// Nodes 摘要 + requested/free 计算
	nodeNameToRequested := sumPodRequestsByNode(pods)
	cluster.Status.Nodes = buildNodeSummaries(nodes, nodeNameToRequested)
	cluster.Status.LastProbeTime = metav1.Now()

	setCondition(cluster, corev1alpha1.ConditionTypeImported, metav1.ConditionTrue, "Imported", "cluster imported")
	touchStatus(cluster)

	if err := r.Status().Update(ctx, cluster); err != nil {
		return ctrl.Result{}, err
	}
	// Ready -> 60s
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

func (r *ClusterReconciler) reconcileNotReady(ctx context.Context, cluster *corev1alpha1.Cluster) (ctrl.Result, error) {
	// NotReady: retry 连接，成功则 Ready
	sec, err := r.getKubeconfigSecret(ctx, cluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "SecretNotFound", "referenced secret not found")
			touchStatus(cluster)
			_ = r.Status().Update(ctx, cluster)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	cs, err := r.getOrBuildClient(cluster.Name, sec)
	if err != nil {
		setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "InvalidKubeconfig", err.Error())
		r.clearClient(cluster.Name)
		touchStatus(cluster)
		_ = r.Status().Update(ctx, cluster)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if _, err := cs.Discovery().ServerVersion(); err != nil {
		setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionFalse, "Unreachable", err.Error())
		touchStatus(cluster)
		_ = r.Status().Update(ctx, cluster)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	setCondition(cluster, corev1alpha1.ConditionTypeReady, metav1.ConditionTrue, "Reachable", "api server reachable")
	cluster.Status.Phase = corev1alpha1.ClusterPhaseReady
	touchStatus(cluster)
	if err := r.Status().Update(ctx, cluster); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

func touchStatus(cluster *corev1alpha1.Cluster) {
	cluster.Status.UpdatedAt = metav1.Now()
}

func (r *ClusterReconciler) getKubeconfigSecret(ctx context.Context, cluster *corev1alpha1.Cluster) (*corev1.Secret, error) {
	var sec corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Spec.SecretRef}, &sec); err != nil {
		return nil, err
	}
	return &sec, nil
}

func (r *ClusterReconciler) getOrBuildClient(clusterID string, sec *corev1.Secret) (*kubernetes.Clientset, error) {
	r.mu.RLock()
	cs := r.kubeClients[clusterID]
	r.mu.RUnlock()
	if cs != nil {
		return cs, nil
	}

	kcfg, ok := sec.Data["kubeconfig"]
	if !ok || len(kcfg) == 0 {
		return nil, fmt.Errorf("secret %s missing data.kubeconfig", sec.Name)
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kcfg)
	if err != nil {
		return nil, err
	}

	newCS, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.kubeClients == nil {
		r.kubeClients = map[string]*kubernetes.Clientset{}
	}
	// double-check
	if existing := r.kubeClients[clusterID]; existing != nil {
		return existing, nil
	}
	r.kubeClients[clusterID] = newCS
	return newCS, nil
}

func (r *ClusterReconciler) clearClient(clusterID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.kubeClients == nil {
		return
	}
	delete(r.kubeClients, clusterID)
}

func sumNodeResources(nodes *corev1.NodeList) (capacityCPU, capacityMem, allocCPU, allocMem resource.Quantity) {
	capacityCPU = resource.NewQuantity(0, resource.DecimalSI).DeepCopy()
	capacityMem = resource.NewQuantity(0, resource.BinarySI).DeepCopy()
	allocCPU = resource.NewQuantity(0, resource.DecimalSI).DeepCopy()
	allocMem = resource.NewQuantity(0, resource.BinarySI).DeepCopy()
	for i := range nodes.Items {
		n := &nodes.Items[i]
		capacityCPU.Add(*n.Status.Capacity.Cpu())
		capacityMem.Add(*n.Status.Capacity.Memory())
		allocCPU.Add(*n.Status.Allocatable.Cpu())
		allocMem.Add(*n.Status.Allocatable.Memory())
	}
	return capacityCPU, capacityMem, allocCPU, allocMem
}

func sumPodRequestsByNode(pods *corev1.PodList) map[string]corev1alpha1.NodeResources {
	res := make(map[string]corev1alpha1.NodeResources, 16)
	for i := range pods.Items {
		p := &pods.Items[i]
		node := p.Spec.NodeName
		if node == "" {
			continue
		}

		// 只统计正在运行/已调度的 Pod；忽略 Succeeded/Failed
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}

		agg := res[node]

		// 每个 Pod 计 1 个 pods
		agg.Pods.Add(*resource.NewQuantity(1, resource.DecimalSI))

		for _, c := range p.Spec.Containers {
			req := c.Resources.Requests
			if cpu, ok := req[corev1.ResourceCPU]; ok {
				agg.CPU.Add(cpu)
			}
			if mem, ok := req[corev1.ResourceMemory]; ok {
				agg.Memory.Add(mem)
			}
		}
		for _, c := range p.Spec.InitContainers {
			// initContainers 的 requests 不是累加，而是取最大；这里做一个近似：按最大值合并
			req := c.Resources.Requests
			if cpu, ok := req[corev1.ResourceCPU]; ok {
				if agg.CPU.Cmp(cpu) < 0 {
					agg.CPU = cpu.DeepCopy()
				}
			}
			if mem, ok := req[corev1.ResourceMemory]; ok {
				if agg.Memory.Cmp(mem) < 0 {
					agg.Memory = mem.DeepCopy()
				}
			}
		}

		res[node] = agg
	}
	return res
}

func buildNodeSummaries(nodes *corev1.NodeList, requestedByNode map[string]corev1alpha1.NodeResources) []corev1alpha1.NodeSummary {
	out := make([]corev1alpha1.NodeSummary, 0, len(nodes.Items))
	for i := range nodes.Items {
		n := &nodes.Items[i]
		req := requestedByNode[n.Name]

		alloc := corev1alpha1.NodeResources{}
		if q := n.Status.Allocatable.Cpu(); q != nil {
			alloc.CPU = q.DeepCopy()
		}
		if q := n.Status.Allocatable.Memory(); q != nil {
			alloc.Memory = q.DeepCopy()
		}
		if q := n.Status.Allocatable.Pods(); q != nil {
			alloc.Pods = q.DeepCopy()
		}

		free := corev1alpha1.NodeResources{}
		free.CPU = alloc.CPU.DeepCopy()
		free.CPU.Sub(req.CPU)
		free.Memory = alloc.Memory.DeepCopy()
		free.Memory.Sub(req.Memory)
		free.Pods = alloc.Pods.DeepCopy()
		free.Pods.Sub(req.Pods)

		out = append(out, corev1alpha1.NodeSummary{
			Name: n.Name,
			UID:  string(n.UID),
			Ready: func() bool {
				for _, c := range n.Status.Conditions {
					if c.Type == corev1.NodeReady {
						return c.Status == corev1.ConditionTrue
					}
				}
				return false
			}(),
			Allocatable: alloc,
			Requested:   req,
			Free:        free,
			Labels:      pickNodeLabels(n.Labels),
		})
	}
	return out
}

func pickNodeLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	// 设计约束：节点摘要 labels 只存 kubernetes.io/hostname
	const key = "kubernetes.io/hostname"
	v, ok := in[key]
	if !ok || v == "" {
		return nil
	}
	return map[string]string{key: v}
}

func setCondition(cluster *corev1alpha1.Cluster, condType string, status metav1.ConditionStatus, reason, message string) {
	cond := metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: cluster.GetGeneration(),
		LastTransitionTime: metav1.Now(),
	}
	cluster.Status.Conditions = mergeCondition(cluster.Status.Conditions, cond)
}

func mergeCondition(conds []metav1.Condition, newCond metav1.Condition) []metav1.Condition {
	for i := range conds {
		if conds[i].Type == newCond.Type {
			// Only bump transition time when status changes
			if conds[i].Status == newCond.Status {
				newCond.LastTransitionTime = conds[i].LastTransitionTime
			}
			conds[i] = newCond
			return conds
		}
	}
	return append(conds, newCond)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// watch Secret 变化：当 kubeconfig 被更新时，触发对应 Cluster 重新调谐
	mapSecretToClusters := handler.TypedEnqueueRequestsFromMapFunc[*corev1.Secret, reconcile.Request](func(ctx context.Context, sec *corev1.Secret) []reconcile.Request {
		// 查同 namespace 下所有 cluster，命中 spec.secretRef == secret.name
		var list corev1alpha1.ClusterList
		if err := r.List(ctx, &list, client.InNamespace(sec.Namespace)); err != nil {
			return nil
		}
		res := make([]reconcile.Request, 0, 4)
		for i := range list.Items {
			c := &list.Items[i]
			if c.Spec.SecretRef == sec.Name {
				res = append(res, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(c)})
			}
		}
		return res
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Cluster{}).
		WatchesRawSource(source.Kind(mgr.GetCache(), &corev1.Secret{}, mapSecretToClusters)).
		Named("cluster").
		Complete(r)
}
