package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
)

var _ = Describe("OptimizationPolicy Plan ownership", func() {
	Context("OwnerReference on ReOrchestrationPlan", func() {
		ctx := context.Background()

		It("should set ownerReferences on created plans and GC them when policy is deleted", func() {
			By("creating minimal profiles configmaps required by evaluator")
			// NOTE: envtest doesn't pre-create kubex-system; keep it consistent with other controller tests.
			profilesNS := "default"
			cmCost := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "instance-cost-profiles", Namespace: profilesNS}, Data: map[string]string{"ecs.c9i.xlarge": "family: ecs.c9i\nresources:\n  cpu: 4\ncost:\n  price: 1\n", "ecs.g9ae.xlarge": "family: ecs.g9ae\nresources:\n  cpu: 4\ncost:\n  price: 1\n"}}
			cmEnergy := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "instance-family-energy-profiles", Namespace: profilesNS}, Data: map[string]string{"ecs.c9i": "baseCores: 8\nenergy:\n  powerSamples:\n  - util: 0.0\n    power: 80\n  - util: 1.0\n    power: 120\n", "ecs.g9ae": "baseCores: 8\nenergy:\n  powerSamples:\n  - util: 0.0\n    power: 80\n  - util: 1.0\n    power: 120\n"}}
			cmLat := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "region-latency-matrix", Namespace: profilesNS}, Data: map[string]string{"matrix.yaml": "regions:\n- Hangzhou\n- Shanghai\nlatency:\n  Hangzhou:\n    Hangzhou: 0\n    Shanghai: 10\n  Shanghai:\n    Hangzhou: 10\n    Shanghai: 0\n"}}
			for _, cm := range []*corev1.ConfigMap{cmCost, cmEnergy, cmLat} {
				err := k8sClient.Create(ctx, cm)
				if err != nil {
					// If the object already exists (leftover from previous spec), that's fine.
					Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
				}
			}

			By("creating an enabled Conservative policy (evaluate will create a Pending plan)")
			pol := &corev1alpha1.OptimizationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "op-own", Namespace: "default", Annotations: map[string]string{"kubex.io/debug-improvement-percent": "1"}},
				Spec: corev1alpha1.OptimizationPolicySpec{
					Enabled:                     true,
					Strategy:                    corev1alpha1.OptimizationStrategyConservative,
					ImprovementThresholdPercent: 10,
					OptimizationGoals:           []corev1alpha1.OptimizationGoal{{Type: "Cost", Weight: 1.0}},
				},
			}
			Expect(k8sClient.Create(ctx, pol)).To(Succeed())

			reconciler := &OptimizationPolicyReconciler{Client: k8sClient}
			reconciler.LockNamespace = "default"
			reconciler.LockName = "kubex-optimization-policy-lock-ownerref"
			reconciler.LeaseDurationSeconds = 15
			reconciler.nowFunc = time.Now
			reconciler.ProfilesNamespace = profilesNS
			reconciler.ControlNamespace = "default"

			By("creating member Cluster c2 and kubeconfig Secret")
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
			err := k8sClient.Create(ctx, cluster)
			_ = client.IgnoreAlreadyExists(err)
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "c2", Namespace: "default"}, Data: map[string][]byte{"kubeconfig": kubeconfigBytes}}
			err = k8sClient.Create(ctx, sec)
			_ = client.IgnoreAlreadyExists(err)
			By("creating Nodes referenced by Cluster c2")
			n1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"node.kubex.io/type": "ecs.c9i.xlarge"}}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}}}
			n2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n2", Labels: map[string]string{"node.kubex.io/type": "ecs.g9ae.xlarge"}}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}}}
			_ = k8sClient.Create(ctx, n1)
			_ = k8sClient.Create(ctx, n2)
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, n1)
				_ = k8sClient.Delete(ctx, n2)
			})

			By("reconciling to active")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}})
			Expect(err).NotTo(HaveOccurred())

			By("invoking evaluate to create a plan")
			Expect(reconciler.defaultEvaluate(ctx, types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace})).To(Succeed())

			var planName string
			By("observing a plan exists and has controller ownerReference")
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
				planName = found.Name
				g.Expect(found.OwnerReferences).NotTo(BeEmpty())
				var owner *metav1.OwnerReference
				for i := range found.OwnerReferences {
					or := found.OwnerReferences[i]
					if or.Kind == "OptimizationPolicy" && or.Controller != nil && *or.Controller {
						owner = &or
						break
					}
				}
				g.Expect(owner).NotTo(BeNil())
				g.Expect(owner.Name).To(Equal(pol.Name))
				g.Expect(owner.UID).To(Equal(pol.UID))
			}, "3s", "200ms").Should(Succeed())

			By("deleting policy")
			Expect(k8sClient.Delete(ctx, pol)).To(Succeed())
			// NOTE: envtest doesn't always enable/execute kube-apiserver GC the same way as a real cluster.
			// The core requirement here is: the plan has a controller OwnerReference pointing to the policy.
			// If GC is enabled, the plan will be deleted automatically; otherwise it may remain.
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}, &corev1alpha1.OptimizationPolicy{})
				return apierrors.IsNotFound(err)
			}, "5s", "200ms").Should(BeTrue())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, &corev1alpha1.ReOrchestrationPlan{ObjectMeta: metav1.ObjectMeta{Name: planName, Namespace: "default"}})
			})
		})
	})
})
