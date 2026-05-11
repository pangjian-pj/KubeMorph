package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
)

var _ = Describe("ReOrchestrationPlan Controller", func() {
	Context("Execution decision", func() {
		ctx := context.Background()

		It("should keep Pending for Preview strategy", func() {
			plan := &corev1alpha1.ReOrchestrationPlan{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "plan-preview",
					Namespace:   "default",
					Annotations: map[string]string{"kubex.io/strategy": string(corev1alpha1.OptimizationStrategyPreview)},
				},
				Spec: corev1alpha1.ReOrchestrationPlanSpec{
					Summary: corev1alpha1.PlanSummary{PolicyName: "p"},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, plan) })

			reconciler := &ReOrchestrationPlanReconciler{Client: k8sClient}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				got := &corev1alpha1.ReOrchestrationPlan{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(corev1alpha1.PlanPhasePending))
			}, "2s", "100ms").Should(Succeed())
		})

		It("should enter Executing for Aggressive strategy", func() {
			plan := &corev1alpha1.ReOrchestrationPlan{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "plan-aggressive",
					Namespace:   "default",
					Annotations: map[string]string{"kubex.io/strategy": string(corev1alpha1.OptimizationStrategyAggressive)},
				},
				Spec: corev1alpha1.ReOrchestrationPlanSpec{
					Summary: corev1alpha1.PlanSummary{PolicyName: "p"},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, plan) })

			reconciler := &ReOrchestrationPlanReconciler{Client: k8sClient}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				got := &corev1alpha1.ReOrchestrationPlan{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, got)).To(Succeed())
				// No moves => execution completes immediately.
				g.Expect(got.Status.Phase).To(Equal(corev1alpha1.PlanPhaseSucceeded))
			}, "2s", "100ms").Should(Succeed())
		})
	})

	Context("Move execution & completion", func() {
		ctx := context.Background()

		It("should patch ReplicaBinding intent and converge plan to Succeeded when RB reaches destination", func() {
			// Arrange a ReplicaBinding that matches naming convention <gd>-rb-<idx>
			rb := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gd1-rb-0",
					Namespace: "default",
				},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: "gd1", Namespace: "default"},
					ReplicaIndex:        0,
				},
			}
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, rb) })

			plan := &corev1alpha1.ReOrchestrationPlan{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "plan-moves-succ",
					Namespace:   "default",
					Annotations: map[string]string{"kubex.io/strategy": string(corev1alpha1.OptimizationStrategyAggressive)},
				},
				Spec: corev1alpha1.ReOrchestrationPlanSpec{
					Summary: corev1alpha1.PlanSummary{PolicyName: "p"},
					Moves: []corev1alpha1.PlanMove{
						{
							GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: "gd1", Namespace: "default"},
							ReplicaIndex:        0,
							Source:              corev1alpha1.MoveLocation{ClusterID: "c1", ClusterName: "c1", NodeName: "n1"},
							Destination:         corev1alpha1.MoveLocation{ClusterID: "c2", ClusterName: "c2", NodeName: "n2"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, plan) })

			reconciler := &ReOrchestrationPlanReconciler{Client: k8sClient}
			// First reconcile: should set phase Executing, apply RB intent.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				gotRB := &corev1alpha1.ReplicaBinding{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, gotRB)).To(Succeed())
				g.Expect(gotRB.Spec.Reschedule).To(BeTrue())
				g.Expect(gotRB.Spec.TargetCluster).To(Equal("c2"))
				g.Expect(gotRB.Spec.TargetNodeName).To(Equal("n2"))
			}, "2s", "100ms").Should(Succeed())

			// Simulate RB completed: Running and status.reschedule watermark/outcome written by GD controller.
			Eventually(func(g Gomega) {
				gotRB := &corev1alpha1.ReplicaBinding{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, gotRB)).To(Succeed())
				// status: running
				stBase := gotRB.DeepCopy()
				gotRB.Status.Phase = corev1alpha1.ReplicaBindingPhaseRunning
				gotRB.Status.Reschedule.LastHandledRequest = gotRB.Spec.RescheduleRequest
				gotRB.Status.Reschedule.LastResult = corev1alpha1.RescheduleResultSucceeded
				gotRB.Status.Reschedule.ObservedLocation.ClusterId = gotRB.Spec.TargetCluster
				gotRB.Status.Reschedule.ObservedLocation.NodeName = gotRB.Spec.TargetNodeName
				gotRB.Status.Reschedule.Message = "reschedule completed"
				g.Expect(k8sClient.Status().Patch(ctx, gotRB, client.MergeFrom(stBase))).To(Succeed())
			}, "2s", "100ms").Should(Succeed())

			Eventually(func(g Gomega) {
				_, recErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}})
				g.Expect(recErr).NotTo(HaveOccurred())
				got := &corev1alpha1.ReOrchestrationPlan{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, got)).To(Succeed())
				g.Expect(got.Status.MoveStatuses).NotTo(BeEmpty())
				g.Expect(got.Status.MoveStatuses[0].Status).To(Equal(corev1alpha1.MoveExecutionStatusSucceeded))
				g.Expect(got.Status.Phase).To(Equal(corev1alpha1.PlanPhaseSucceeded))
				g.Expect(got.Status.CompletionTime.IsZero()).To(BeFalse())
				g.Expect(got.Status.Summary.TotalMoves).To(Equal(int32(1)))
				g.Expect(got.Status.Summary.SucceededMoves).To(Equal(int32(1)))
			}, "4s", "100ms").Should(Succeed())
		})

		It("should converge plan to Failed when RB enters Failed", func() {
			rb := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gd2-rb-0",
					Namespace: "default",
				},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: "gd2", Namespace: "default"},
					ReplicaIndex:        0,
				},
			}
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, rb) })

			plan := &corev1alpha1.ReOrchestrationPlan{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "plan-moves-fail",
					Namespace:   "default",
					Annotations: map[string]string{"kubex.io/strategy": string(corev1alpha1.OptimizationStrategyAggressive)},
				},
				Spec: corev1alpha1.ReOrchestrationPlanSpec{
					Summary: corev1alpha1.PlanSummary{PolicyName: "p"},
					Moves: []corev1alpha1.PlanMove{
						{
							GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: "gd2", Namespace: "default"},
							ReplicaIndex:        0,
							Source:              corev1alpha1.MoveLocation{ClusterID: "c1", ClusterName: "c1", NodeName: "n1"},
							Destination:         corev1alpha1.MoveLocation{ClusterID: "c2", ClusterName: "c2", NodeName: "n2"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, plan) })

			reconciler := &ReOrchestrationPlanReconciler{Client: k8sClient}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}})
			Expect(err).NotTo(HaveOccurred())

			// Simulate RB failed.
			Eventually(func(g Gomega) {
				gotRB := &corev1alpha1.ReplicaBinding{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, gotRB)).To(Succeed())
				base := gotRB.DeepCopy()
				gotRB.Status.Phase = corev1alpha1.ReplicaBindingPhaseFailed
				gotRB.Status.LastError = "boom"
				gotRB.Status.Reschedule.LastHandledRequest = gotRB.Spec.RescheduleRequest
				gotRB.Status.Reschedule.LastResult = corev1alpha1.RescheduleResultFailed
				gotRB.Status.Reschedule.LastError = "boom"
				g.Expect(k8sClient.Status().Patch(ctx, gotRB, client.MergeFrom(base))).To(Succeed())
			}, "2s", "100ms").Should(Succeed())

			Eventually(func(g Gomega) {
				_, recErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}})
				g.Expect(recErr).NotTo(HaveOccurred())
				got := &corev1alpha1.ReOrchestrationPlan{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, got)).To(Succeed())
				g.Expect(got.Status.MoveStatuses).NotTo(BeEmpty())
				g.Expect(got.Status.MoveStatuses[0].Status).To(Equal(corev1alpha1.MoveExecutionStatusFailed))
				g.Expect(got.Status.Phase).To(Equal(corev1alpha1.PlanPhaseFailed))
			}, "4s", "100ms").Should(Succeed())
		})
	})
})
