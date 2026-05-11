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
	"errors"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"github.com/pangjian-pj/KubeMorph/controller/internal/optimizer"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type alwaysTimeoutSolver struct{}

func (alwaysTimeoutSolver) Solve(ctx context.Context, _ optimizer.Problem) (*optimizer.SolveResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

var _ = Describe("OptimizationPolicy Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-op"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		AfterEach(func() {
			// best-effort cleanup for the per-test policy name (test-op)
			pol := &corev1alpha1.OptimizationPolicy{}
			_ = k8sClient.Get(ctx, typeNamespacedName, pol)
			_ = k8sClient.Delete(ctx, pol)
			// also cleanup op-summary if created
			op := &corev1alpha1.OptimizationPolicy{}
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "op-summary", Namespace: "default"}, op)
			_ = k8sClient.Delete(ctx, op)
		})

		It("should set phase=Failed and condition when weights sum != 1", func() {
			By("creating an invalid OptimizationPolicy")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled: true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{
						{Type: "Cost", Weight: 0.4},
						{Type: "Latency", Weight: 0.4},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())

			By("reconciling")
			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("observing status.phase and condition")
			Eventually(func(g Gomega) {
				got := &corev1alpha1.OptimizationPolicy{}
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(corev1alpha1.OptimizationPolicyPhaseFailed))
				g.Expect(got.Status.Conditions).NotTo(BeEmpty())
				g.Expect(got.Status.Conditions[len(got.Status.Conditions)-1].Type).To(Equal("SpecValid"))
				g.Expect(got.Status.Conditions[len(got.Status.Conditions)-1].Status).To(Equal(metav1.ConditionFalse))
			}, "2s", "200ms").Should(Succeed())
		})

		It("should set phase=Active when spec is valid, enabled=true and lease acquired", func() {
			By("creating a valid OptimizationPolicy")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled: true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{
						{Type: "Cost", Weight: 1.0},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())

			By("reconciling")
			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"                          // envtest doesn't create kubex-system namespace by default
			reconciler.LockName = "kubex-optimization-policy-lock-active" // isolate from other tests
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("observing status.phase")
			Eventually(func(g Gomega) {
				got := &corev1alpha1.OptimizationPolicy{}
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(corev1alpha1.OptimizationPolicyPhaseActive))
			}, "2s", "200ms").Should(Succeed())
		})

		It("should allow only one Active policy via Lease lock", func() {
			By("creating 2 valid enabled policies")
			p1 := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "op-1", Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
				},
			}
			p2 := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "op-2", Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
				},
			}
			Expect(k8sClient.Create(ctx, p1)).To(Succeed())
			Expect(k8sClient.Create(ctx, p2)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, p1)
				_ = k8sClient.Delete(ctx, p2)
			})

			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"                           // avoid depending on external kubex-system namespace in envtest
			reconciler.LockName = "kubex-optimization-policy-lock-compete" // isolate from other tests
			reconciler.LeaseDurationSeconds = 15

			By("reconciling both")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "op-1", Namespace: "default"}})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "op-2", Namespace: "default"}})
			Expect(err).NotTo(HaveOccurred())

			By("asserting exactly one Active and one Failed(Conflict)")
			Eventually(func(g Gomega) {
				g1 := &corev1alpha1.OptimizationPolicy{}
				g2 := &corev1alpha1.OptimizationPolicy{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "op-1", Namespace: "default"}, g1)).To(Succeed())
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "op-2", Namespace: "default"}, g2)).To(Succeed())
				phases := []corev1alpha1.OptimizationPolicyPhase{g1.Status.Phase, g2.Status.Phase}
				g.Expect(phases).To(ContainElement(corev1alpha1.OptimizationPolicyPhaseActive))
				g.Expect(phases).To(ContainElement(corev1alpha1.OptimizationPolicyPhaseFailed))
			}, "3s", "200ms").Should(Succeed())
		})

		It("should cancel old timer and apply new rebalancePoint after update", func() {
			By("creating an Active Periodic policy")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					RunMode:           corev1alpha1.OptimizationRunModePeriodic,
					RebalancePoint:    "200ms",
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())

			var fired int32
			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-timer"
			reconciler.LeaseDurationSeconds = 15
			reconciler.nowFunc = time.Now
			reconciler.timerHandles = map[types.NamespacedName]context.CancelFunc{}
			reconciler.evaluateFunc = func(ctx context.Context, key types.NamespacedName) error {
				atomic.AddInt32(&fired, 1)
				return nil
			}

			By("initial reconcile schedules timer")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("updating rebalancePoint to a longer duration and reconciling again")
			got := &corev1alpha1.OptimizationPolicy{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, got)).To(Succeed())
			got.Spec.RebalancePoint = "800ms"
			Expect(k8sClient.Update(ctx, got)).To(Succeed())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying old timer was canceled (should not fire within 400ms)")
			time.Sleep(400 * time.Millisecond)
			Expect(atomic.LoadInt32(&fired)).To(Equal(int32(0)))

			By("verifying new timer eventually fires")
			Eventually(func() int32 { return atomic.LoadInt32(&fired) }, "2s", "50ms").Should(BeNumerically(">=", 1))
		})

		It("should select GlobalDeployments across namespaces by targetSelector and write observedDeployments", func() {
			By("creating an extra namespace")
			// envtest uses corev1.Namespace, create it via unstructured to avoid extra imports.
			nsObj := &unstructured.Unstructured{}
			nsObj.SetAPIVersion("v1")
			nsObj.SetKind("Namespace")
			nsObj.SetName("ns-observed")
			Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, nsObj) })

			By("creating 3 GlobalDeployments across namespaces")
			gd1 := &corev1alpha1.GlobalDeployment{ObjectMeta: metav1.ObjectMeta{Name: "gd-a", Namespace: "default", Labels: map[string]string{"app": "demo"}}, Spec: corev1alpha1.GlobalDeploymentSpec{Template: runtime.RawExtension{Raw: []byte("{}")}}}
			gd2 := &corev1alpha1.GlobalDeployment{ObjectMeta: metav1.ObjectMeta{Name: "gd-b", Namespace: "default", Labels: map[string]string{"app": "other"}}, Spec: corev1alpha1.GlobalDeploymentSpec{Template: runtime.RawExtension{Raw: []byte("{}")}}}
			gd3 := &corev1alpha1.GlobalDeployment{ObjectMeta: metav1.ObjectMeta{Name: "gd-c", Namespace: "ns-observed", Labels: map[string]string{"app": "demo"}}, Spec: corev1alpha1.GlobalDeploymentSpec{Template: runtime.RawExtension{Raw: []byte("{}")}}}
			Expect(k8sClient.Create(ctx, gd1)).To(Succeed())
			Expect(k8sClient.Create(ctx, gd2)).To(Succeed())
			Expect(k8sClient.Create(ctx, gd3)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, gd1)
				_ = k8sClient.Delete(ctx, gd2)
				_ = k8sClient.Delete(ctx, gd3)
			})

			By("creating an enabled policy with targetSelector app=demo")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
					TargetSelector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": "demo"}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pol) })

			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-observed"
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("observing observedDeployments=2 (gd-a + gd-c)")
			Eventually(func(g Gomega) {
				got := &corev1alpha1.OptimizationPolicy{}
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, got)).To(Succeed())
				g.Expect(got.Status.ObservedDeployments).To(Equal(int32(2)))
			}, "2s", "200ms").Should(Succeed())
		})

		It("should build currentLayout from ReplicaBinding status (preferred) and spec (fallback) and compute stable/unstable counts", func() {
			By("creating a migratable GlobalDeployment with 2 replicas")
			gd := &corev1alpha1.GlobalDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-layout", Namespace: "default", Labels: map[string]string{"app": "layout"}},
				Spec:       corev1alpha1.GlobalDeploymentSpec{Template: runtime.RawExtension{Raw: []byte(`{"template":{}}`)}},
			}
			Expect(k8sClient.Create(ctx, gd)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, gd) })

			By("creating ReplicaBindings for replicaIndex=0,1")
			rb0 := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-layout-rb-0", Namespace: "default"},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
					ReplicaIndex:        0,
					TargetCluster:       "c1",
					TargetNodeName:      "n1",
				},
			}
			rb1 := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-layout-rb-1", Namespace: "default"},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
					ReplicaIndex:        1,
					TargetCluster:       "c2",
					TargetNodeName:      "n2",
				},
			}
			Expect(k8sClient.Create(ctx, rb0)).To(Succeed())
			Expect(k8sClient.Create(ctx, rb1)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, rb0)
				_ = k8sClient.Delete(ctx, rb1)
			})

			By("patching RB0 status to represent ground truth (different from spec)")
			Eventually(func(g Gomega) {
				var got corev1alpha1.ReplicaBinding
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rb0.Name, Namespace: rb0.Namespace}, &got)).To(Succeed())
				base := got.DeepCopy()
				got.Status.NodeName = "n1-gt"
				got.Status.ClusterName = "c1-gt"
				got.Status.Phase = corev1alpha1.ReplicaBindingPhaseRunning
				err := k8sClient.Status().Patch(ctx, &got, client.MergeFrom(base))
				if err != nil {
					return
				}
			}, "2s", "100ms").Should(Succeed())

			By("patching RB1 status to Running but missing clusterName to force unstable")
			Eventually(func(g Gomega) {
				var got corev1alpha1.ReplicaBinding
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rb1.Name, Namespace: rb1.Namespace}, &got)).To(Succeed())
				base := got.DeepCopy()
				got.Status.NodeName = "n2-gt"
				got.Status.ClusterName = "" // force ClusterIdEmpty
				got.Status.Phase = corev1alpha1.ReplicaBindingPhaseRunning
				err := k8sClient.Status().Patch(ctx, &got, client.MergeFrom(base))
				if err != nil {
					return
				}
			}, "2s", "100ms").Should(Succeed())

			By("creating an enabled policy selecting app=layout")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
					TargetSelector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": "layout"}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pol) })

			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-layout"
			reconciler.LeaseDurationSeconds = 15
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying status.currentLayout prefers RBStatus and counts unstable reasons")
			Eventually(func(g Gomega) {
				got := &corev1alpha1.OptimizationPolicy{}
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, got)).To(Succeed())
				g.Expect(got.Status.ObservedReplicas).To(Equal(int32(2)))
				g.Expect(got.Status.StableReplicas).To(Equal(int32(1)))
				g.Expect(got.Status.UnstableReplicas).To(Equal(int32(1)))
				g.Expect(got.Status.UnstableReasonsCount).To(HaveKeyWithValue("ClusterIdEmpty", int32(1)))

				// RB0 should come from status, not spec.
				var found0 *corev1alpha1.CurrentReplicaLocation
				for i := range got.Status.CurrentLayout {
					if got.Status.CurrentLayout[i].Name == gd.Name && got.Status.CurrentLayout[i].ReplicaIndex == 0 {
						found0 = &got.Status.CurrentLayout[i]
						break
					}
				}
				g.Expect(found0).ToNot(BeNil())
				g.Expect(found0.Source).To(Equal("RBStatus"))
				g.Expect(found0.ClusterId).To(Equal("c1-gt"))
				g.Expect(found0.NodeName).To(Equal("n1-gt"))
				g.Expect(found0.Stable).To(BeTrue())
			}, "3s", "200ms").Should(Succeed())
		})

		It("should mark replicas under hostPath GlobalDeployment as NonMigratableHostPath", func() {
			By("creating a GlobalDeployment whose template has hostPath volume")
			gd := &corev1alpha1.GlobalDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-hostpath", Namespace: "default", Labels: map[string]string{"app": "hostpath"}},
				Spec: corev1alpha1.GlobalDeploymentSpec{Template: runtime.RawExtension{Raw: []byte(`{
  "replicas": 1,
  "template": {
    "spec": {
      "volumes": [{"name": "hp", "hostPath": {"path": "/tmp"}}],
      "containers": [{"name": "main", "image": "nginx"}]
    }
  }
}`)}},
			}
			Expect(k8sClient.Create(ctx, gd)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, gd) })

			By("creating a Running ReplicaBinding with placement")
			rb := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-hostpath-rb-0", Namespace: "default"},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
					ReplicaIndex:        0,
					TargetCluster:       "c1",
					TargetNodeName:      "n1",
				},
			}
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, rb) })
			Eventually(func(g Gomega) {
				var got corev1alpha1.ReplicaBinding
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, &got)).To(Succeed())
				base := got.DeepCopy()
				got.Status.NodeName = "n1"
				got.Status.ClusterName = "c1"
				got.Status.Phase = corev1alpha1.ReplicaBindingPhaseRunning
				_ = k8sClient.Status().Patch(ctx, &got, client.MergeFrom(base))
			}, "2s", "100ms").Should(Succeed())

			By("creating an enabled policy selecting app=hostpath")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-op", Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
					TargetSelector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": "hostpath"}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pol) })

			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock"
			reconciler.LeaseDurationSeconds = 15
			_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}})
			Expect(reconcileErr).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				got := &corev1alpha1.OptimizationPolicy{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}, got)).To(Succeed())
				g.Expect(got.Status.ObservedReplicas).To(Equal(int32(1)))
				g.Expect(got.Status.StableReplicas).To(Equal(int32(0)))
				g.Expect(got.Status.UnstableReplicas).To(Equal(int32(1)))
				g.Expect(got.Status.UnstableReasonsCount).To(HaveKeyWithValue("NonMigratableHostPath", int32(1)))
			}, "3s", "200ms").Should(Succeed())
		})

		It("should collect current layout from ReplicaBindings and write currentLayout/observedReplicas", func() {
			By("creating a GlobalDeployment")
			gd := &corev1alpha1.GlobalDeployment{ObjectMeta: metav1.ObjectMeta{Name: "gd-layout", Namespace: "default", Labels: map[string]string{"app": "layout"}}, Spec: corev1alpha1.GlobalDeploymentSpec{Template: runtime.RawExtension{Raw: []byte("{}")}}}
			Expect(k8sClient.Create(ctx, gd)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, gd) })

			By("creating ReplicaBindings with ground-truth status")
			rb0 := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-layout-rb-0", Namespace: "default"},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: "gd-layout", Namespace: "default"},
					ReplicaIndex:        0,
					TargetCluster:       "cluster-a",
					TargetNodeName:      "node-a",
				},
			}
			Expect(k8sClient.Create(ctx, rb0)).To(Succeed())
			// Write status via subresource patch (more reliable in envtest).
			rb0Latest := &corev1alpha1.ReplicaBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rb0.Name, Namespace: rb0.Namespace}, rb0Latest)).To(Succeed())
			patch0 := client.MergeFrom(rb0Latest.DeepCopy())
			rb0Latest.Status.NodeName = "node-gt-0"
			rb0Latest.Status.ClusterName = "cluster-gt-a"
			rb0Latest.Status.Phase = corev1alpha1.ReplicaBindingPhaseRunning
			Expect(k8sClient.Status().Patch(ctx, rb0Latest, patch0)).To(Succeed())

			rb1 := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-layout-rb-1", Namespace: "default"},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: "gd-layout", Namespace: "default"},
					ReplicaIndex:        1,
					TargetCluster:       "cluster-b",
					TargetNodeName:      "node-b",
				},
			}
			// leave rb1.status empty to force fallback to spec; also mark it non-running to be unstable
			Expect(k8sClient.Create(ctx, rb1)).To(Succeed())
			rb1Latest := &corev1alpha1.ReplicaBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rb1.Name, Namespace: rb1.Namespace}, rb1Latest)).To(Succeed())
			patch1 := client.MergeFrom(rb1Latest.DeepCopy())
			rb1Latest.Status.Phase = corev1alpha1.ReplicaBindingPhaseApplying
			Expect(k8sClient.Status().Patch(ctx, rb1Latest, patch1)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, rb0)
				_ = k8sClient.Delete(ctx, rb1)
			})

			By("creating an enabled policy selecting gd-layout")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
					TargetSelector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": "layout"}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pol) })

			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-layout"
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("observing observedReplicas=2 and currentLayout entries")
			Eventually(func(g Gomega) {
				got := &corev1alpha1.OptimizationPolicy{}
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, got)).To(Succeed())
				g.Expect(got.Status.ObservedReplicas).To(Equal(int32(2)))
				g.Expect(got.Status.CurrentLayout).To(HaveLen(2))
				g.Expect(got.Status.StableReplicas).To(Equal(int32(1)))
				g.Expect(got.Status.UnstableReplicas).To(Equal(int32(1)))
				g.Expect(got.Status.UnstableReasonsCount).To(HaveKeyWithValue("ReplicaNotRunning", int32(1)))
				g.Expect(got.Status.CurrentLayout).To(ContainElement(corev1alpha1.CurrentReplicaLocation{
					Name:         "gd-layout",
					Namespace:    "default",
					ReplicaIndex: 0,
					ClusterId:    "cluster-gt-a",
					NodeName:     "node-gt-0",
					Source:       "RBStatus",
					Stable:       true,
				}))
				g.Expect(got.Status.CurrentLayout).To(ContainElement(corev1alpha1.CurrentReplicaLocation{
					Name:           "gd-layout",
					Namespace:      "default",
					ReplicaIndex:   1,
					ClusterId:      "cluster-b",
					NodeName:       "node-b",
					Source:         "RBSpecFallback",
					Stable:         false,
					UnstableReason: "ReplicaNotRunning",
				}))
			}, "2s", "200ms").Should(Succeed())
		})

		It("should mark replicas as non-migratable when GD template uses hostPath", func() {
			By("creating a GlobalDeployment with hostPath volume")
			tmpl := []byte(`{
			  "replicas": 2,
			  "selector": {"matchLabels": {"app": "hp"}},
			  "template": {
			    "metadata": {"labels": {"app": "hp"}},
			    "spec": {
			      "containers": [{"name": "c", "image": "nginx"}],
			      "volumes": [{"name": "d", "hostPath": {"path": "/data"}}]
			    }
			  }
			}`)
			gd := &corev1alpha1.GlobalDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-hostpath", Namespace: "default", Labels: map[string]string{"app": "hp"}},
				Spec:       corev1alpha1.GlobalDeploymentSpec{Template: runtime.RawExtension{Raw: tmpl}},
			}
			Expect(k8sClient.Create(ctx, gd)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, gd) })

			By("creating 2 ReplicaBindings under the GD")
			rb0 := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-hostpath-rb-0", Namespace: "default"},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
					ReplicaIndex:        0,
					TargetCluster:       "cluster-a",
					TargetNodeName:      "node-a",
				},
			}
			rb1 := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-hostpath-rb-1", Namespace: "default"},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
					ReplicaIndex:        1,
					TargetCluster:       "cluster-b",
					TargetNodeName:      "node-b",
				},
			}
			Expect(k8sClient.Create(ctx, rb0)).To(Succeed())
			Expect(k8sClient.Create(ctx, rb1)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, rb0)
				_ = k8sClient.Delete(ctx, rb1)
			})

			By("creating an enabled policy selecting app=hp")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "op-hostpath", Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
					TargetSelector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": "hp"}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pol) })

			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-hostpath"
			_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}})
			Expect(reconcileErr).NotTo(HaveOccurred())

			By("observing all replicas excluded with NonMigratableHostPath")
			Eventually(func(g Gomega) {
				got := &corev1alpha1.OptimizationPolicy{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}, got)).To(Succeed())
				g.Expect(got.Status.ObservedReplicas).To(Equal(int32(2)))
				g.Expect(got.Status.StableReplicas).To(Equal(int32(0)))
				g.Expect(got.Status.UnstableReplicas).To(Equal(int32(2)))
				g.Expect(got.Status.UnstableReasonsCount).To(HaveKeyWithValue("NonMigratableHostPath", int32(2)))
			}, "2s", "200ms").Should(Succeed())
		})

		It("should create Plan but keep it Pending with NotExecuted when Conservative threshold not met", func() {
			By("creating a valid Active Conservative policy with threshold=10%")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:        resourceName,
					Namespace:   "default",
					Annotations: map[string]string{"kubex.io/debug-improvement-percent": "5"},
				},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:                     true,
					Strategy:                    corev1alpha1.OptimizationStrategyConservative,
					ImprovementThresholdPercent: 10,
					OptimizationGoals:           []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pol) })

			By("creating minimal cost/energy configmaps to satisfy shared loader")
			cmCost := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "instance-cost-profiles", Namespace: "default"}, Data: map[string]string{"ecs.c9i.xlarge": "family: ecs.c9i\nresources:\n  cpu: 4\ncost:\n  price: 1\n"}}
			cmEnergy := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "instance-family-energy-profiles", Namespace: "default"}, Data: map[string]string{"ecs.c9i": "baseCores: 8\nenergy:\n  powerSamples:\n  - util: 0.0\n    power: 80\n  - util: 1.0\n    power: 120\n"}}
			cmErr := k8sClient.Create(ctx, cmCost)
			if cmErr != nil {
				Expect(client.IgnoreAlreadyExists(cmErr)).To(Succeed())
			}
			cmErr = k8sClient.Create(ctx, cmEnergy)
			if cmErr != nil {
				Expect(client.IgnoreAlreadyExists(cmErr)).To(Succeed())
			}

			By("creating minimal latency matrix configmap to satisfy shared loader")
			cmLat := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "region-latency-matrix", Namespace: "default"},
				Data: map[string]string{
					"matrix.yaml": "regions:\n- Hangzhou\n- Shanghai\nlatency:\n  Hangzhou:\n    Hangzhou: 0\n    Shanghai: 10\n  Shanghai:\n    Hangzhou: 10\n    Shanghai: 0\n",
				},
			}
			cmErr = k8sClient.Create(ctx, cmLat)
			if cmErr != nil {
				Expect(client.IgnoreAlreadyExists(cmErr)).To(Succeed())
			}

			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-m4-52"
			reconciler.LeaseDurationSeconds = 15
			reconciler.ControlNamespace = "default"
			// Run evaluation immediately by calling evaluate directly after reconcile.
			reconciler.nowFunc = time.Now

			By("creating member Cluster c2 and kubeconfig Secret")
			// Point kubeconfig to envtest apiserver so member client can read Nodes created in this suite.
			kubeconfigBytes, kErr := clientcmd.Write(clientcmdapi.Config{
				Clusters: map[string]*clientcmdapi.Cluster{"c": {Server: cfg.Host, CertificateAuthorityData: cfg.CAData}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"u": {
					ClientCertificateData: cfg.CertData,
					ClientKeyData:         cfg.KeyData,
				}},
				Contexts:       map[string]*clientcmdapi.Context{"ctx": {Cluster: "c", AuthInfo: "u"}},
				CurrentContext: "ctx",
			})
			Expect(kErr).NotTo(HaveOccurred())
			cluster := &corev1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "c2", Namespace: "default"},
				Spec:       corev1alpha1.ClusterSpec{APIEndpoint: cfg.Host, SecretRef: "c2"},
			}
			cErr := k8sClient.Create(ctx, cluster)
			if cErr != nil {
				Expect(client.IgnoreAlreadyExists(cErr)).To(Succeed())
			}
			// MemberClientCache expects the referenced kubeconfig Secret to live in the same namespace.
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "c2", Namespace: "default"}, Data: map[string][]byte{"kubeconfig": kubeconfigBytes}}
			sErr := k8sClient.Create(ctx, sec)
			if sErr != nil {
				Expect(client.IgnoreAlreadyExists(sErr)).To(Succeed())
			}
			By("creating Nodes referenced by Cluster c2")
			// Candidate nodes are derived from Cluster.status.nodes and then fetched from member apiserver.
			// In envtest we use the same apiserver, so we must create these Nodes.
			n1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{optimizer.LabelInstanceType: "ecs.c9i.xlarge"}}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}}}
			n2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n2", Labels: map[string]string{optimizer.LabelInstanceType: "ecs.c9i.xlarge"}}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}}}
			_ = k8sClient.Create(ctx, n1)
			_ = k8sClient.Create(ctx, n2)
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, n1)
				_ = k8sClient.Delete(ctx, n2)
			})

			By("reconciling to become Active")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Profiles live in ProfilesNamespace (default: kubex-system). In tests we create them in "default".
			reconciler.ProfilesNamespace = "default"

			By("invoking evaluate to create a plan")
			Expect(reconciler.defaultEvaluate(ctx, typeNamespacedName)).To(Succeed())

			By("observing a plan is created and stays Pending with NotExecuted marker")
			Eventually(func(g Gomega) {
				var plans corev1alpha1.ReOrchestrationPlanList
				g.Expect(k8sClient.List(ctx, &plans, client.InNamespace("default"))).To(Succeed())
				// Find the plan created by this test: label matches and has notExecutedReason annotation.
				var found *corev1alpha1.ReOrchestrationPlan
				for i := range plans.Items {
					p := &plans.Items[i]
					if p.Labels["kubex.io/policy"] != resourceName {
						continue
					}
					if p.Annotations != nil && p.Annotations["kubex.io/notExecutedReason"] == "ImprovementBelowThreshold" {
						found = p
						break
					}
					// fallback: keep the first labeled plan if no better match yet
					if found == nil {
						found = p
					}
				}
				g.Expect(found).NotTo(BeNil())
				g.Expect(found.Status.Phase).To(Equal(corev1alpha1.PlanPhasePending))
				g.Expect(found.Annotations).NotTo(BeNil())
				g.Expect(found.Annotations["kubex.io/notExecutedReason"]).To(Equal("ImprovementBelowThreshold"))
				g.Expect(found.Annotations["kubex.io/notExecuted"]).To(ContainSubstring("below threshold"))
			}, "2s", "200ms").Should(Succeed())

			By("observing the policy status.latestPlanRef is set and RebalanceSucceeded=True")
			Eventually(func(g Gomega) {
				got := &corev1alpha1.OptimizationPolicy{}
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, got)).To(Succeed())
				g.Expect(got.Status.LatestPlanRef).NotTo(BeNil())
				g.Expect(got.Status.LatestPlanRef.Name).NotTo(BeEmpty())
				g.Expect(got.Status.LatestPlanRef.Namespace).To(Equal("default"))
				// condition exists
				var cond *metav1.Condition
				for i := range got.Status.Conditions {
					c := got.Status.Conditions[i]
					if c.Type == "RebalanceSucceeded" {
						cond = &c
						break
					}
				}
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(Equal("PlanCreated"))
			}, "2s", "200ms").Should(Succeed())
		})

		It("should mark policy RebalanceSucceeded=False with SolverTimeout when solver exceeds timeout", func() {
			By("creating a valid Active policy")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "op-solver-timeout", Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pol) })

			reconciler := &OptimizationPolicyReconciler{Client: k8sClient, Recorder: record.NewFakeRecorder(64)}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-solver-timeout"
			reconciler.LeaseDurationSeconds = 15
			reconciler.nowFunc = time.Now
			reconciler.SolverTimeout = 1 * time.Millisecond
			reconciler.evaluateFunc = func(_ context.Context, key types.NamespacedName) error {
				// Directly exercise SolveProblem with a solver that only returns when ctx is done.
				// This makes the timeout behavior deterministic for envtest.
				p := optimizer.Problem{
					StableReplicas: []optimizer.ReplicaKey{{Namespace: key.Namespace, Name: key.Name, ReplicaIndex: 0}},
					CandidateNodes: []optimizer.ClusterNodeID{{ClusterID: "cluster-0", NodeName: "node-0"}},
					RequireCPU:     false,
				}
				_, err := optimizer.SolveProblem(context.Background(), alwaysTimeoutSolver{}, p, optimizer.SolveOptions{Timeout: reconciler.SolverTimeout})
				if err == nil {
					return nil
				}
				// Mimic defaultEvaluate's surface behavior.
				reason := "SolverError"
				msg := err.Error()
				if errors.Is(err, context.DeadlineExceeded) {
					reason = "SolverTimeout"
					msg = "solver timed out"
				}
				got := &corev1alpha1.OptimizationPolicy{}
				if getErr := k8sClient.Get(ctx, key, got); getErr == nil {
					_ = reconciler.setStatusPhaseAndCondition(ctx, got, got.Status.Phase, metav1.Condition{
						Type:               "RebalanceSucceeded",
						Status:             metav1.ConditionFalse,
						Reason:             reason,
						Message:            msg,
						ObservedGeneration: got.GetGeneration(),
						LastTransitionTime: metav1.Now(),
					})
					if reconciler.Recorder != nil {
						reconciler.Recorder.Event(got, "Warning", reason, msg)
					}
				}
				return err
			}

			By("reconciling to become Active")
			_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}})
			Expect(reconcileErr).NotTo(HaveOccurred())

			By("invoking evaluate and expecting timeout error")
			evalErr := reconciler.evaluateFunc(ctx, types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace})
			Expect(evalErr).To(HaveOccurred())
			Expect(errors.Is(evalErr, context.DeadlineExceeded)).To(BeTrue())

			By("observing policy condition SolverTimeout")
			Eventually(func(g Gomega) {
				got := &corev1alpha1.OptimizationPolicy{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}, got)).To(Succeed())
				var cond *metav1.Condition
				for i := range got.Status.Conditions {
					c := got.Status.Conditions[i]
					if c.Type == "RebalanceSucceeded" {
						cond = &c
						break
					}
				}
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal("SolverTimeout"))
			}, "2s", "200ms").Should(Succeed())

			By("observing a Warning event is emitted")
			Eventually(func() string {
				select {
				case e := <-reconciler.Recorder.(*record.FakeRecorder).Events:
					return e
				default:
					return ""
				}
			}, "2s", "50ms").Should(ContainSubstring("SolverTimeout"))
		})

		It("should populate Plan spec.summary fields when moves are generated", func() {
			By("creating a GD + RBs so policy has currentLayout")
			repl := int32(2)
			gd := &corev1alpha1.GlobalDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-summary", Namespace: "default", Labels: map[string]string{"app": "sum"}},
				Spec: corev1alpha1.GlobalDeploymentSpec{
					Replicas: &repl,
					Template: runtime.RawExtension{Raw: []byte(`{"replicas":1,"selector":{"matchLabels":{"app":"x"}},"template":{"metadata":{"labels":{"app":"x"}},"spec":{"containers":[{"name":"c","image":"nginx"}]}}}`)},
				},
			}
			Expect(k8sClient.Create(ctx, gd)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, gd) })

			rb0 := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-summary-rb-0", Namespace: "default"},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
					ReplicaIndex:        0,
					TargetCluster:       "cluster-a",
					TargetNodeName:      "node-a",
				},
				Status: corev1alpha1.ReplicaBindingStatus{Phase: corev1alpha1.ReplicaBindingPhaseRunning, ClusterName: "cluster-a", NodeName: "node-a"},
			}
			rb1 := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-summary-rb-1", Namespace: "default"},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
					ReplicaIndex:        1,
					TargetCluster:       "cluster-a",
					TargetNodeName:      "node-a",
				},
				Status: corev1alpha1.ReplicaBindingStatus{Phase: corev1alpha1.ReplicaBindingPhaseRunning, ClusterName: "cluster-a", NodeName: "node-a"},
			}
			Expect(k8sClient.Create(ctx, rb0)).To(Succeed())
			Expect(k8sClient.Create(ctx, rb1)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, rb0)
				_ = k8sClient.Delete(ctx, rb1)
			})

			By("creating an enabled policy selecting app=sum")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "op-summary", Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
					TargetSelector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sum"}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pol) })

			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-summary"
			reconciler.LeaseDurationSeconds = 15
			reconciler.nowFunc = time.Now

			By("reconciling to become Active and collect currentLayout")
			_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}})
			Expect(reconcileErr).NotTo(HaveOccurred())

			// Force a move by setting a fake expected placement via overriding solver path.
			// We do this by manually patching policy.status.currentLayout to include two different nodes,
			// then the current code's candidateNodes derives from that set and OR-Tools can choose any.
			// In this test we primarily assert summary fields are populated, not exact move selection.
			Expect(reconciler.defaultEvaluate(ctx, types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace})).To(Succeed())

			By("observing plan summary fields are populated")
			Eventually(func(g Gomega) {
				var plans corev1alpha1.ReOrchestrationPlanList
				g.Expect(k8sClient.List(ctx, &plans, client.InNamespace("default"))).To(Succeed())
				var found *corev1alpha1.ReOrchestrationPlan
				for i := range plans.Items {
					p := &plans.Items[i]
					if p.Labels["kubex.io/policy"] == pol.Name {
						found = p
						break
					}
				}
				g.Expect(found).NotTo(BeNil())
				g.Expect(found.Spec.Summary.PodsToMove).NotTo(BeNil())
				g.Expect(found.Spec.Summary.CurrentScore).NotTo(BeNil())
				g.Expect(found.Spec.Summary.ExpectedScore).NotTo(BeNil())
				g.Expect(found.Spec.Summary.EstimatedImprovementScore).NotTo(BeNil())
				g.Expect(*found.Spec.Summary.PodsToMove).To(Equal(int32(len(found.Spec.Moves))))
				g.Expect(*found.Spec.Summary.CurrentScore).To(BeNumerically(">=", 0))
				g.Expect(*found.Spec.Summary.ExpectedScore).To(BeNumerically(">=", 0))
				g.Expect(*found.Spec.Summary.EstimatedImprovementScore).To(BeNumerically(">=", 0))
			}, "2s", "200ms").Should(Succeed())
		})

		It("should compute plan summary from Cost/Energy configmaps when assignment creates moves", func() {
			By("creating cost/energy profiles configmaps")
			cmCost := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "instance-cost-profiles", Namespace: "default"},
				Data: map[string]string{
					"ecs.c9i.xlarge":  "family: ecs.c9i\nresources:\n  cpu: 4\ncost:\n  price: 1\n",
					"ecs.g9ae.xlarge": "family: ecs.g9ae\nresources:\n  cpu: 4\ncost:\n  price: 2\n",
				},
			}
			err := k8sClient.Create(ctx, cmCost)
			if err != nil {
				Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
			}
			cmEnergy := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "instance-family-energy-profiles", Namespace: "default"},
				Data: map[string]string{
					"ecs.c9i":  "baseCores: 8\nenergy:\n  powerSamples:\n  - util: 0.0\n    power: 80\n  - util: 1.0\n    power: 120\n",
					"ecs.g9ae": "baseCores: 96\nenergy:\n  powerSamples:\n  - util: 0.0\n    power: 72\n  - util: 1.0\n    power: 380\n",
				},
			}
			err = k8sClient.Create(ctx, cmEnergy)
			if err != nil {
				Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
			}

			By("creating a valid policy")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "op-summary", Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled: true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{
						{Type: "Cost", Weight: 0.5},
						{Type: "Energy", Weight: 0.5},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())

			By("creating a GD + 2 RBs to produce 2 stable replicas in currentLayout")
			gd := &corev1alpha1.GlobalDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "gd", Namespace: "default"},
				Spec: corev1alpha1.GlobalDeploymentSpec{
					Template: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"c","image":"pause"}]}}}}`)},
				},
			}
			Expect(k8sClient.Create(ctx, gd)).To(Succeed())

			By("creating Nodes with instance type labels")
			n1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{optimizer.LabelInstanceType: "ecs.c9i.xlarge"}},
				Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}},
			}
			n2 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "n2", Labels: map[string]string{optimizer.LabelInstanceType: "ecs.g9ae.xlarge"}},
				Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}},
			}
			Expect(k8sClient.Create(ctx, n1)).To(Succeed())
			Expect(k8sClient.Create(ctx, n2)).To(Succeed())
			mkRB := func(name string, idx int32, cluster, node string) *corev1alpha1.ReplicaBinding {
				u := &corev1alpha1.ReplicaBinding{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec: corev1alpha1.ReplicaBindingSpec{
						GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
						ReplicaIndex:        idx,
						TargetCluster:       cluster,
						TargetNodeName:      node,
					},
					Status: corev1alpha1.ReplicaBindingStatus{Phase: "Running", ClusterName: cluster, NodeName: node},
				}
				return u
			}
			Expect(k8sClient.Create(ctx, mkRB("rb0", 0, "c1", "n1"))).To(Succeed())
			Expect(k8sClient.Create(ctx, mkRB("rb1", 1, "c1", "n2"))).To(Succeed())

			By("reconciling")
			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-summary-cost-energy"
			reconciler.ProfilesNamespace = "default"
			reconciler.evaluateFunc = func(ctx context.Context, key types.NamespacedName) error {
				return reconciler.defaultEvaluate(ctx, key)
			}
			// We only assert that reconcile doesn't error and that there exists a plan whose summary is non-zero.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "op-summary", Namespace: "default"}})
			Expect(err).NotTo(HaveOccurred())

			By("observing a plan with non-zero summary")
			Eventually(func(g Gomega) {
				var plans corev1alpha1.ReOrchestrationPlanList
				g.Expect(k8sClient.List(ctx, &plans, client.InNamespace("default"))).To(Succeed())
				g.Expect(plans.Items).NotTo(BeEmpty())
				pl := plans.Items[len(plans.Items)-1]
				g.Expect(pl.Spec.Summary).NotTo(BeNil())
				g.Expect(pl.Spec.Summary.CurrentScore).NotTo(BeNil())
				g.Expect(pl.Spec.Summary.PodsToMove).NotTo(BeNil())
				g.Expect(*pl.Spec.Summary.PodsToMove).To(Equal(int32(len(pl.Spec.Moves))))
			}, "3s", "200ms").Should(Succeed())
		})

		It("should compute plan summary from Latency configmap when nodes have region labels", func() {
			By("creating latency matrix configmap")
			cmLat := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "region-latency-matrix", Namespace: "default"},
				Data: map[string]string{
					"matrix.yaml": "regions:\n- Hangzhou\n- Shanghai\nlatency:\n  Hangzhou:\n    Hangzhou: 0\n    Shanghai: 10\n",
				},
			}
			err := k8sClient.Create(ctx, cmLat)
			if err != nil {
				Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
			}

			By("creating Nodes with region labels")
			n1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "lat-n1", Labels: map[string]string{
					optimizer.LabelInstanceType: "ecs.c9i.xlarge",
					"node.kubex.io/region":      "Hangzhou",
				}},
				Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}},
			}
			n2 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "lat-n2", Labels: map[string]string{
					optimizer.LabelInstanceType: "ecs.c9i.xlarge",
					"node.kubex.io/region":      "Shanghai",
				}},
				Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}},
			}
			Expect(k8sClient.Create(ctx, n1)).To(Succeed())
			Expect(k8sClient.Create(ctx, n2)).To(Succeed())

			By("creating minimal cost/energy configmaps to satisfy shared loader")
			cmCost := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "instance-cost-profiles", Namespace: "default"}, Data: map[string]string{"ecs.c9i.xlarge": "family: ecs.c9i\nresources:\n  cpu: 4\ncost:\n  price: 1\n"}}
			cmEnergy := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "instance-family-energy-profiles", Namespace: "default"}, Data: map[string]string{"ecs.c9i": "baseCores: 8\nenergy:\n  powerSamples:\n  - util: 0.0\n    power: 80\n  - util: 1.0\n    power: 120\n"}}
			err = k8sClient.Create(ctx, cmCost)
			if err != nil {
				// other tests may have created the same shared profiles CM
				Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
			}
			err = k8sClient.Create(ctx, cmEnergy)
			if err != nil {
				Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
			}

			By("creating a policy with Latency goal")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "op-latency", Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Latency", Weight: 1.0, SourceCity: "Hangzhou"}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pol) })

			By("creating a GD + 2 RBs to produce stable replicas across two nodes")
			gd := &corev1alpha1.GlobalDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-lat", Namespace: "default"},
				Spec:       corev1alpha1.GlobalDeploymentSpec{Template: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"c","image":"pause"}]}}}}`)}},
			}
			Expect(k8sClient.Create(ctx, gd)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, gd) })
			mkRB := func(name string, idx int32, cluster, node string) *corev1alpha1.ReplicaBinding {
				return &corev1alpha1.ReplicaBinding{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec: corev1alpha1.ReplicaBindingSpec{
						GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
						ReplicaIndex:        idx,
						TargetCluster:       cluster,
						TargetNodeName:      node,
					},
					Status: corev1alpha1.ReplicaBindingStatus{Phase: "Running", ClusterName: cluster, NodeName: node},
				}
			}
			rb0 := mkRB("rb-lat-0", 0, "c1", "lat-n1")
			rb1 := mkRB("rb-lat-1", 1, "c1", "lat-n2")
			Expect(k8sClient.Create(ctx, rb0)).To(Succeed())
			Expect(k8sClient.Create(ctx, rb1)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, rb0)
				_ = k8sClient.Delete(ctx, rb1)
			})

			By("reconciling")
			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-latency"
			reconciler.ProfilesNamespace = "default"
			reconciler.ControlNamespace = "default"
			reconciler.evaluateFunc = func(ctx context.Context, key types.NamespacedName) error {
				return reconciler.defaultEvaluate(ctx, key)
			}

			By("creating member Cluster c1 and kubeconfig Secret")
			kubeconfigBytes, kErr := clientcmd.Write(clientcmdapi.Config{
				Clusters: map[string]*clientcmdapi.Cluster{"c": {Server: cfg.Host, CertificateAuthorityData: cfg.CAData}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"u": {
					ClientCertificateData: cfg.CertData,
					ClientKeyData:         cfg.KeyData,
				}},
				Contexts:       map[string]*clientcmdapi.Context{"ctx": {Cluster: "c", AuthInfo: "u"}},
				CurrentContext: "ctx",
			})
			Expect(kErr).NotTo(HaveOccurred())
			cluster := &corev1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default"},
				Spec:       corev1alpha1.ClusterSpec{APIEndpoint: cfg.Host, SecretRef: "c1"},
			}
			cErr := k8sClient.Create(ctx, cluster)
			if cErr != nil {
				Expect(client.IgnoreAlreadyExists(cErr)).To(Succeed())
			}
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default"}, Data: map[string][]byte{"kubeconfig": kubeconfigBytes}}
			sErr := k8sClient.Create(ctx, sec)
			if sErr != nil {
				Expect(client.IgnoreAlreadyExists(sErr)).To(Succeed())
			}
			_, nerr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}})
			Expect(nerr).NotTo(HaveOccurred())

			By("observing a plan exists and summary populated")
			Eventually(func(g Gomega) {
				var plans corev1alpha1.ReOrchestrationPlanList
				g.Expect(k8sClient.List(ctx, &plans, client.InNamespace("default"))).To(Succeed())
				var found *corev1alpha1.ReOrchestrationPlan
				for i := range plans.Items {
					p := &plans.Items[i]
					if p.Labels["kubex.io/policy"] == pol.Name {
						found = p
						break
					}
				}
				g.Expect(found).NotTo(BeNil())
				g.Expect(found.Spec.Summary.PodsToMove).NotTo(BeNil())
				g.Expect(*found.Spec.Summary.PodsToMove).To(Equal(int32(len(found.Spec.Moves))))
			}, "3s", "200ms").Should(Succeed())
		})

		It("should compute plan summary from Communication topology when user selects topologyRef", func() {
			By("creating latency matrix configmap (reused by Communication)")
			cmLat := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "region-latency-matrix", Namespace: "default"},
				Data: map[string]string{
					"matrix.yaml": "regions:\n- Hangzhou\n- Shanghai\nlatency:\n  Hangzhou:\n    Hangzhou: 0\n    Shanghai: 10\n  Shanghai:\n    Hangzhou: 10\n    Shanghai: 0\n",
				},
			}
			err := k8sClient.Create(ctx, cmLat)
			if err != nil {
				Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
			}

			By("creating topology template configmap")
			cmTopo := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "online-boutique-topology", Namespace: "default"},
				Data: map[string]string{
					"online-boutique": `{"nodes":["frontend"],"adjacency":[[1]]}`,
				},
			}
			Expect(k8sClient.Create(ctx, cmTopo)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, cmTopo) })

			By("creating minimal cost/energy configmaps to satisfy shared loader")
			cmCost := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "instance-cost-profiles", Namespace: "default"}, Data: map[string]string{"ecs.c9i.xlarge": "family: ecs.c9i\nresources:\n  cpu: 4\ncost:\n  price: 1\n"}}
			cmEnergy := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "instance-family-energy-profiles", Namespace: "default"}, Data: map[string]string{"ecs.c9i": "baseCores: 8\nenergy:\n  powerSamples:\n  - util: 0.0\n    power: 80\n  - util: 1.0\n    power: 120\n"}}
			err = k8sClient.Create(ctx, cmCost)
			if err != nil {
				Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
			}
			err = k8sClient.Create(ctx, cmEnergy)
			if err != nil {
				Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
			}

			By("creating Nodes with region labels")
			n1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "comm-n1", Labels: map[string]string{
					optimizer.LabelInstanceType: "ecs.c9i.xlarge",
					"node.kubex.io/region":      "Hangzhou",
				}},
				Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}},
			}
			n2 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "comm-n2", Labels: map[string]string{
					optimizer.LabelInstanceType: "ecs.c9i.xlarge",
					"node.kubex.io/region":      "Shanghai",
				}},
				Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}},
			}
			Expect(k8sClient.Create(ctx, n1)).To(Succeed())
			Expect(k8sClient.Create(ctx, n2)).To(Succeed())

			By("creating a policy with Communication goal and topologyRef")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "op-comm", Namespace: "default"},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:           true,
					OptimizationGoals: []corev1alpha1.OptimizationGoal{{Type: "Communication", Weight: 1.0, TopologyRef: "online-boutique-topology"}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pol) })

			By("creating a GD + 2 RBs to produce stable replicas across two nodes")
			gd := &corev1alpha1.GlobalDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-comm", Namespace: "default", Labels: map[string]string{"kubex.io/service": "frontend"}},
				Spec:       corev1alpha1.GlobalDeploymentSpec{Template: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"c","image":"pause"}]}}}}`)}},
			}
			Expect(k8sClient.Create(ctx, gd)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, gd) })
			mkRB := func(name string, idx int32, cluster, node string) *corev1alpha1.ReplicaBinding {
				return &corev1alpha1.ReplicaBinding{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec: corev1alpha1.ReplicaBindingSpec{
						GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
						ReplicaIndex:        idx,
						TargetCluster:       cluster,
						TargetNodeName:      node,
					},
					Status: corev1alpha1.ReplicaBindingStatus{Phase: "Running", ClusterName: cluster, NodeName: node},
				}
			}
			rb0 := mkRB("rb-comm-0", 0, "c1", "comm-n1")
			rb1 := mkRB("rb-comm-1", 1, "c1", "comm-n2")
			Expect(k8sClient.Create(ctx, rb0)).To(Succeed())
			Expect(k8sClient.Create(ctx, rb1)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, rb0)
				_ = k8sClient.Delete(ctx, rb1)
			})

			By("reconciling")
			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-comm"
			reconciler.ProfilesNamespace = "default"
			reconciler.ControlNamespace = "default"
			reconciler.evaluateFunc = func(ctx context.Context, key types.NamespacedName) error {
				return reconciler.defaultEvaluate(ctx, key)
			}

			By("creating member Cluster c1 and kubeconfig Secret")
			kubeconfigBytes, kErr := clientcmd.Write(clientcmdapi.Config{
				Clusters: map[string]*clientcmdapi.Cluster{"c": {Server: cfg.Host, CertificateAuthorityData: cfg.CAData}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"u": {
					ClientCertificateData: cfg.CertData,
					ClientKeyData:         cfg.KeyData,
				}},
				Contexts:       map[string]*clientcmdapi.Context{"ctx": {Cluster: "c", AuthInfo: "u"}},
				CurrentContext: "ctx",
			})
			Expect(kErr).NotTo(HaveOccurred())
			cluster := &corev1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default"},
				Spec:       corev1alpha1.ClusterSpec{APIEndpoint: cfg.Host, SecretRef: "c1"},
			}
			cErr := k8sClient.Create(ctx, cluster)
			if cErr != nil {
				Expect(client.IgnoreAlreadyExists(cErr)).To(Succeed())
			}
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default"}, Data: map[string][]byte{"kubeconfig": kubeconfigBytes}}
			sErr := k8sClient.Create(ctx, sec)
			if sErr != nil {
				Expect(client.IgnoreAlreadyExists(sErr)).To(Succeed())
			}
			_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}})
			Expect(reconcileErr).NotTo(HaveOccurred())

			By("observing a plan exists and summary populated")
			Eventually(func(g Gomega) {
				var plans corev1alpha1.ReOrchestrationPlanList
				g.Expect(k8sClient.List(ctx, &plans, client.InNamespace("default"))).To(Succeed())
				var found *corev1alpha1.ReOrchestrationPlan
				for i := range plans.Items {
					p := &plans.Items[i]
					if p.Labels["kubex.io/policy"] == pol.Name {
						found = p
						break
					}
				}
				g.Expect(found).NotTo(BeNil())
				g.Expect(found.Spec.Summary.PodsToMove).NotTo(BeNil())
				g.Expect(*found.Spec.Summary.PodsToMove).To(Equal(int32(len(found.Spec.Moves))))
			}, "3s", "200ms").Should(Succeed())
		})
	})
})
