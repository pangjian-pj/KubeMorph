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
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"github.com/pangjian-pj/KubeMorph/controller/internal/controller/multicluster"
)

const replicaBindingFinalizer = "core.kubex.io/finalizer-replicabinding"

// ReplicaBindingReconciler executes a binding by creating/updating resources in member cluster.
type ReplicaBindingReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ControlNamespace is where Cluster CRs and kubeconfig Secrets live.
	// In this project, it's typically "kubex-system".
	// If empty, defaults to rb.Namespace (legacy behavior).
	ControlNamespace string

	memberClients *multicluster.MemberClientCache
}

// +kubebuilder:rbac:groups=core.kubex.io,resources=replicabindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.kubex.io,resources=replicabindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.kubex.io,resources=globaldeployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=core.kubex.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

func (r *ReplicaBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if r.memberClients == nil {
		r.memberClients = multicluster.NewMemberClientCache()
	}

	var rb corev1alpha1.ReplicaBinding
	if err := r.Get(ctx, req.NamespacedName, &rb); err != nil {
		// During deletion storms, workqueue may still have stale keys after the RB is gone.
		// Treat NotFound as a normal terminal condition.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if rb.Status.Phase == corev1alpha1.ReplicaBindingPhaseDeleting {
		// 进入到deleting状态后，rb controller要去检查成员集群中的deployment是否被回收掉，如果没有则入队，如果有则移除掉finalizer
		if controllerutil.ContainsFinalizer(&rb, replicaBindingFinalizer) {
			controlNS := r.ControlNamespace
			if controlNS == "" {
				controlNS = rb.Namespace
			}
			cs, err := r.getMemberClient(ctx, controlNS, rb.Spec.TargetCluster)
			if err != nil {
				log.Info("deleting replicabinding: get member client failed", "binding", rb.Name, "cluster", rb.Spec.TargetCluster, "err", err.Error())
				// Retry.
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			gdName := rb.Spec.GlobalDeploymentRef.Name
			workloadNS := rb.Namespace
			depName := fmt.Sprintf("%s-r%d", gdName, rb.Spec.ReplicaIndex)
			deps := cs.AppsV1().Deployments(workloadNS)
			_, gErr := deps.Get(ctx, depName, metav1.GetOptions{})
			if gErr != nil && !apierrors.IsNotFound(gErr) {
				log.Info("deleting replicabinding: get member deployment failed", "binding", rb.Name, "cluster", rb.Spec.TargetCluster, "deployment", depName, "err", gErr.Error())
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			// Finalizer can be removed when member deployment is not found.
			if apierrors.IsNotFound(gErr) {
				base := rb.DeepCopy()
				controllerutil.RemoveFinalizer(&rb, replicaBindingFinalizer)
				if err := r.Patch(ctx, &rb, client.MergeFrom(base)); err != nil {
					return ctrl.Result{}, err
				}
				log.Info("deleting replicabinding: removed finalizer after member cleanup", "binding", rb.Name, "cluster", rb.Spec.TargetCluster, "deployment", depName)
				return ctrl.Result{}, nil
			}
			// Still exists; keep waiting.
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, nil
	}

	// 如果rb被打上了deletionTimestamp，说明已经被执行delete动作，要流转成deleting状态
	if !rb.DeletionTimestamp.IsZero() {
		// 这里先去成员集群中删除deployment，再流转成deleting
		var gd corev1alpha1.GlobalDeployment
		gdKey := types.NamespacedName{Name: rb.Spec.GlobalDeploymentRef.Name, Namespace: rb.Spec.GlobalDeploymentRef.Namespace}
		workloadNS := rb.Namespace
		gdName := rb.Spec.GlobalDeploymentRef.Name
		if err := r.Get(ctx, gdKey, &gd); err == nil {
			if gd.Namespace != "" {
				workloadNS = gd.Namespace
			}
			if gd.Name != "" {
				gdName = gd.Name
			}
		}

		controlNS := r.ControlNamespace
		if controlNS == "" {
			controlNS = rb.Namespace
		}
		cs, err := r.getMemberClient(ctx, controlNS, rb.Spec.TargetCluster)
		if err != nil {
			log.Info("deleting replicabinding: get member client failed", "binding", rb.Name, "cluster", rb.Spec.TargetCluster, "err", err.Error())
			// Retry.
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}

		depName := fmt.Sprintf("%s-r%d", gdName, rb.Spec.ReplicaIndex)
		deps := cs.AppsV1().Deployments(workloadNS)
		err = deps.Delete(ctx, depName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			log.Info("deleting replicabinding: delete member deployment failed", "binding", rb.Name, "cluster", rb.Spec.TargetCluster, "deployment", depName, "err", err.Error())
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}

		base := rb.DeepCopy()
		rb.Status.Phase = corev1alpha1.ReplicaBindingPhaseDeleting
		rb.Status.LastTransitionTime = metav1.Now()
		rb.Status.LastError = ""
		if err := r.Status().Patch(ctx, &rb, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 200 * time.Millisecond}, nil
	}

	// Ensure finalizer exists (only in non-deleting state).
	if !controllerutil.ContainsFinalizer(&rb, replicaBindingFinalizer) {
		base := rb.DeepCopy()
		controllerutil.AddFinalizer(&rb, replicaBindingFinalizer)
		if err := r.Patch(ctx, &rb, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		// Requeue quickly to continue normal execution on updated object.
		return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
	}

	if rb.Status.Phase == "" {
		// Design contract: RB controller initializes phase to Pending (observable) and then waits
		// for GD controller to assign targetCluster/targetNodeName and set phase=Assigned.
		base := rb.DeepCopy()
		rb.Status.Phase = corev1alpha1.ReplicaBindingPhasePending
		rb.Status.LastTransitionTime = metav1.Now()
		rb.Status.LastError = ""
		if err := r.Status().Patch(ctx, &rb, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 200 * time.Millisecond}, nil
	}

	if rb.Status.Phase == corev1alpha1.ReplicaBindingPhaseFailed {
		return ctrl.Result{}, nil
	}

	if rb.Status.Phase == corev1alpha1.ReplicaBindingPhaseRunning {
		return ctrl.Result{}, nil
	}

	// 如果rb status还是Pending，说明gd controller还没有去设置target cluster和node，这里先让rb过2s再入队检查
	if rb.Status.Phase == corev1alpha1.ReplicaBindingPhasePending {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 这里先去拿globaldeployment
	var gd corev1alpha1.GlobalDeployment
	gdKey := types.NamespacedName{Name: rb.Spec.GlobalDeploymentRef.Name, Namespace: rb.Spec.GlobalDeploymentRef.Namespace}
	if err := r.Get(ctx, gdKey, &gd); err != nil {
		log.Error(err, "get globaldeployment failed")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	controlNS := r.ControlNamespace
	if controlNS == "" {
		controlNS = rb.Namespace
	}

	// 这里构造出成员集群的client
	cs, err := r.getMemberClient(ctx, controlNS, rb.Spec.TargetCluster)
	if err != nil {
		log.Error(err, "get member client failed", "cluster", rb.Spec.TargetCluster)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	depName := fmt.Sprintf("%s-r%d", gd.Name, rb.Spec.ReplicaIndex)

	// 如果rb status是Assigned，说明gd controller已经设置好target cluster和node了
	// 那rb controller这个时候就要到对应的member cluster中去创建deployment了
	if rb.Status.Phase == corev1alpha1.ReplicaBindingPhaseAssigned {
		// Ensure the target namespace exists in member cluster.
		if err := ensureNamespace(ctx, cs, gd.Namespace); err != nil {
			return r.failBinding(ctx, &rb, fmt.Sprintf("ensure member namespace failed (cluster=%s, namespace=%s): %v", rb.Spec.TargetCluster, gd.Namespace, err))
		}

		// Ensure Deployment exists in member cluster (replicas=1) with nodeAffinity pinned.
		dep := buildMemberDeployment(&gd, depName, rb.Spec.TargetNodeName)
		depsClient := cs.AppsV1().Deployments(gd.Namespace)
		_, err := depsClient.Create(ctx, dep, metav1.CreateOptions{})
		if err != nil {
			log.Error(err, "failed to create deployment in member cluster")
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		log.Info("deployment created successfully in member cluster", "name", depName)
		// 在成员集群创建完deployment后，把rb status更新为Applying
		base := rb.DeepCopy()
		rb.Status.Phase = corev1alpha1.ReplicaBindingPhaseApplying
		rb.Status.LastTransitionTime = metav1.Now()
		rb.Status.LastError = ""
		if err := r.Status().Patch(ctx, &rb, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 如果rb status 为rescheduling，说明此时进入了重调度阶段，rb controller先根据status里边存的cluster和node信息去删deployment，然后再创建新的
	if rb.Status.Phase == corev1alpha1.ReplicaBindingPhaseRescheduling {
		// 这里用的是status里边的cluster
		csOld, err := r.getMemberClient(ctx, controlNS, rb.Status.ClusterName)
		if err != nil {
			log.Error(err, "get member client failed", "cluster", rb.Status.ClusterName)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		oldDepsClient := csOld.AppsV1().Deployments(gd.Namespace)

		// 1) Best-effort delete the old member Deployment.
		// Member Deployment name is stable (<gd>-r<replicaIndex>), so rescheduling is effectively:
		// delete then recreate with updated nodeAffinity.
		dErr := oldDepsClient.Delete(ctx, depName, metav1.DeleteOptions{})
		if dErr != nil && !apierrors.IsNotFound(dErr) {
			log.Error(dErr, "failed to delete deployment in member cluster for rescheduling", "deployment", depName)
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}

		// 2) Wait until the deployment is fully gone (avoid recreating with same name while it's
		// still terminating, which can cause AlreadyExists/conflicts).
		_, gErr := oldDepsClient.Get(ctx, depName, metav1.GetOptions{})
		if gErr == nil {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		if !apierrors.IsNotFound(gErr) {
			log.Error(gErr, "failed to get deployment in member cluster for rescheduling", "deployment", depName)
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}

		// 3) Recreate a fresh deployment pinned to the new target cluster and node.
		dep := buildMemberDeployment(&gd, depName, rb.Spec.TargetNodeName)
		depsClient := cs.AppsV1().Deployments(gd.Namespace)
		if _, cErr := depsClient.Create(ctx, dep, metav1.CreateOptions{}); cErr != nil {
			if !apierrors.IsAlreadyExists(cErr) {
				log.Error(cErr, "failed to recreate deployment in member cluster for rescheduling", "deployment", depName)
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
		}
		log.Info("deployment recreated successfully in member cluster for rescheduling", "name", depName)

		// 4) Move to Applying so we can reuse the existing readiness loop.
		base := rb.DeepCopy()
		rb.Status.Phase = corev1alpha1.ReplicaBindingPhaseApplying
		rb.Status.LastTransitionTime = metav1.Now()
		rb.Status.LastError = ""
		if err := r.Status().Patch(ctx, &rb, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 如果rb status是Applying，说明rb controller已经在member cluster 中创建deployment了，这个时候需要去循环检查deployment状态
	if rb.Status.Phase == corev1alpha1.ReplicaBindingPhaseApplying {
		depsClient := cs.AppsV1().Deployments(gd.Namespace)
		currentDep, getErr := depsClient.Get(ctx, depName, metav1.GetOptions{})
		if getErr != nil {
			if apierrors.IsNotFound(getErr) {
				// 说明deployment没有创建成功，再创建一遍，然后再入队
				dep := buildMemberDeployment(&gd, depName, rb.Spec.TargetNodeName)
				_, err := depsClient.Create(ctx, dep, metav1.CreateOptions{})
				if err != nil {
					log.Error(err, "failed to create deployment in member cluster")
					return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
				}
				log.Info("deployment created successfully in member cluster", "name", depName)
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			} else {
				return ctrl.Result{}, getErr
			}
		}
		// 这里要去检查deployment状态，如果deployment是ready，那pod一定是running，这个时候再去查实际的node是哪一个，然后更新rb status为Running
		if currentDep != nil && currentDep.Status.AvailableReplicas >= 1 {
			actualNodeName := ""
			pods, pErr := cs.CoreV1().Pods(gd.Namespace).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("core.kubex.io/deployName=%s", currentDep.Name)})
			if pErr != nil || len(pods.Items) == 0 {
				// Fallback: broad GD label selector and filter by pod name prefix.
				pods2, pErr2 := cs.CoreV1().Pods(gd.Namespace).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("core.kubex.io/globaldeploy=%s", gd.Name)})
				if pErr2 == nil {
					filtered := make([]corev1.Pod, 0, len(pods2.Items))
					prefix := depName + "-"
					for i := range pods2.Items {
						p := pods2.Items[i]
						if p.Name == depName || strings.HasPrefix(p.Name, prefix) {
							filtered = append(filtered, p)
						}
					}
					pods.Items = filtered
				}
			}
			if pErr == nil {
				for i := range pods.Items {
					p := &pods.Items[i]
					if p.Status.Phase != corev1.PodRunning {
						continue
					}
					if p.Spec.NodeName == "" {
						continue
					}
					// Prefer a truly Ready pod.
					ready := false
					for _, c := range p.Status.Conditions {
						if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
							ready = true
							break
						}
					}
					if ready {
						actualNodeName = p.Spec.NodeName
						break
					}
					// Fallback if no ready pod found yet.
					if actualNodeName == "" {
						actualNodeName = p.Spec.NodeName
					}
				}
			}

			if rb.Status.Phase != corev1alpha1.ReplicaBindingPhaseRunning {
				base := rb.DeepCopy()
				rb.Status.Phase = corev1alpha1.ReplicaBindingPhaseRunning
				rb.Status.LastTransitionTime = metav1.Now()
				rb.Status.LastError = ""
				if actualNodeName != "" {
					rb.Status.NodeName = actualNodeName
				}
				rb.Status.ClusterName = rb.Spec.TargetCluster
				if err := r.Status().Patch(ctx, &rb, client.MergeFrom(base)); err != nil {
					return ctrl.Result{}, err
				}
				log.Info("binding is running", "binding", rb.Name, "cluster", rb.Spec.TargetCluster, "deployment", depName)
			}
			// Running: stop periodic requeue; rely on events or owner reconcile.
			return ctrl.Result{}, nil
		} else if currentDep != nil && isDeploymentFailed(currentDep) {
			//  说明pod failed了，直接把status置为Failed，然后返回
			if rb.Status.Phase != corev1alpha1.ReplicaBindingPhaseFailed {
				base := rb.DeepCopy()
				rb.Status.Phase = corev1alpha1.ReplicaBindingPhaseFailed
				rb.Status.LastTransitionTime = metav1.Now()
				rb.Status.LastError = "pod failed"
				if err := r.Status().Patch(ctx, &rb, client.MergeFrom(base)); err != nil {
					return ctrl.Result{}, err
				}
			}
		} else {
			// 说明deployment还在处理中，让它重新入队再检查
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	// Not ready yet.
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

// isDeploymentFailed 判断 Deployment 是否真的失败
func isDeploymentFailed(dep *appsv1.Deployment) bool {
	// 没有副本在运行
	if dep.Status.AvailableReplicas != 0 {
		return false
	}
	// 没有创建任何副本，不算失败
	if dep.Status.Replicas == 0 && dep.Status.UpdatedReplicas == 0 {
		return false
	}

	// 检查 Deployment 自身条件是否失败
	for _, cond := range dep.Status.Conditions {
		if cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

func ensureNamespace(ctx context.Context, cs *kubernetes.Clientset, namespace string) error {
	if namespace == "" {
		return fmt.Errorf("namespace is empty")
	}
	_, err := cs.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	// Create namespace if not found.
	_, err = cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (r *ReplicaBindingReconciler) failBinding(ctx context.Context, rb *corev1alpha1.ReplicaBinding, msg string) (ctrl.Result, error) {
	base := rb.DeepCopy()
	rb.Status.Phase = corev1alpha1.ReplicaBindingPhaseFailed
	rb.Status.LastError = msg
	rb.Status.LastTransitionTime = metav1.Now()
	_ = r.Status().Patch(ctx, rb, client.MergeFrom(base))
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *ReplicaBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.ReplicaBinding{}).
		Named("replicabinding").
		Complete(r)
}

func (r *ReplicaBindingReconciler) getMemberClient(ctx context.Context, namespace string, clusterID string) (*kubernetes.Clientset, error) {
	if r.memberClients == nil {
		r.memberClients = multicluster.NewMemberClientCache()
	}
	return r.memberClients.GetOrBuild(ctx, r.Client, namespace, clusterID)
}

func buildMemberDeployment(gd *corev1alpha1.GlobalDeployment, name string, targetNodeName string) *appsv1.Deployment {
	// Decode + clone template
	var spec appsv1.DeploymentSpec
	_ = yaml.Unmarshal(gd.Spec.Template.Raw, &spec)
	spec = *spec.DeepCopy()

	// Force replicas=1 per replica binding
	one := int32(1)
	spec.Replicas = &one

	// Inject labels
	if spec.Template.Labels == nil {
		spec.Template.Labels = map[string]string{}
	}
	spec.Template.Labels["core.kubex.io/globaldeploy"] = gd.Name
	spec.Template.Labels["core.kubex.io/deployName"] = name
	// NOTE: replica-index label is added in RB controller reconcile where we have it.

	// Inject nodeAffinity to pin to specific node hostname.
	if spec.Template.Spec.Affinity == nil {
		spec.Template.Spec.Affinity = &corev1.Affinity{}
	}
	if spec.Template.Spec.Affinity.NodeAffinity == nil {
		spec.Template.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key:      "kubernetes.io/hostname",
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{targetNodeName},
			}},
		}},
	}

	dep := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: gd.Namespace,
			Labels: map[string]string{
				"core.kubex.io/globaldeploy": gd.Name,
				"app.kubernetes.io/name":     name,
			},
		},
		Spec: spec,
	}
	return dep
}
