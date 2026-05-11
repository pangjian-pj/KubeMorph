package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
)

var _ = Describe("ReplicaBinding Controller", func() {
	Context("Finalizer-driven member cleanup", func() {
		ctx := context.Background()

		It("should keep RB until member deployment is deleted, then remove finalizer", Serial, func() {
			// Start a dedicated manager with ONLY ReplicaBinding controller registered.
			mgr, err := ctrl.NewManager(cfg, manager.Options{Scheme: k8sClient.Scheme(), Metrics: server.Options{BindAddress: "0"}})
			Expect(err).NotTo(HaveOccurred())
			rbOnlyReconciler := &ReplicaBindingReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ControlNamespace: "default"}
			Expect(rbOnlyReconciler.SetupWithManager(mgr)).To(Succeed())

			mgrCtx, mgrCancel := context.WithCancel(context.Background())
			DeferCleanup(mgrCancel)
			go func() { _ = mgr.Start(mgrCtx) }()
			Expect(mgr.GetCache().WaitForCacheSync(mgrCtx)).To(BeTrue())
			time.Sleep(100 * time.Millisecond)

			// Create a GD (only used to carry namespace/name for member deployment naming).
			gd := &corev1alpha1.GlobalDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-rb-fin", Namespace: "default"},
				Spec:       corev1alpha1.GlobalDeploymentSpec{Template: runtime.RawExtension{Raw: []byte("{}")}},
			}
			Expect(k8sClient.Create(ctx, gd)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, gd) })

			// Create member Cluster CR + kubeconfig Secret for c2 (pointing to envtest apiserver).
			kubeCfg := clientcmdapi.NewConfig()
			kubeCfg.Clusters["env"] = &clientcmdapi.Cluster{
				Server:                   cfg.Host,
				CertificateAuthorityData: cfg.CAData,
				InsecureSkipTLSVerify:    cfg.Insecure,
			}
			kubeCfg.Contexts["env"] = &clientcmdapi.Context{Cluster: "env", AuthInfo: "env"}
			kubeCfg.AuthInfos["env"] = &clientcmdapi.AuthInfo{
				ClientCertificateData: cfg.CertData,
				ClientKeyData:         cfg.KeyData,
				Token:                 cfg.BearerToken,
			}
			kubeCfg.CurrentContext = "env"
			kcfgBytes, err := clientcmd.Write(*kubeCfg)
			Expect(err).NotTo(HaveOccurred())

			sec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "c2", Namespace: "default"},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"kubeconfig": kcfgBytes},
			}
			err = k8sClient.Create(ctx, sec)
			if err != nil {
				Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
			}
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, sec) })

			cl := &corev1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "c2", Namespace: "default"},
				Spec: corev1alpha1.ClusterSpec{
					APIEndpoint: cfg.Host,
					SecretRef:   "c2",
				},
			}
			err = k8sClient.Create(ctx, cl)
			if err != nil {
				Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
			}
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, cl) })

			rb := &corev1alpha1.ReplicaBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "rb-fin-0", Namespace: "default"},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
					ReplicaIndex:        0,
					TargetCluster:       "c2",
					TargetNodeName:      "n2",
				},
				// Start from Assigned so RB controller can move to Applying and execute.
				Status: corev1alpha1.ReplicaBindingStatus{Phase: corev1alpha1.ReplicaBindingPhaseAssigned},
			}
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())

			// Create a fake member deployment in the member cluster API (same envtest).
			// Note: RB controller now lists pods by Deployment.spec.selector.matchLabels, so keep this consistent.
			// Important: RB controller names member deployment as <gd.Name>-r<replicaIndex>.
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "gd-rb-fin-r0", Namespace: "default"},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "gd-rb-fin-r0"}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "gd-rb-fin-r0"}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "nginx"}}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dep)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, dep) })

			// Controller should add finalizer.
			Eventually(func(g Gomega) {
				var got corev1alpha1.ReplicaBinding
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, &got)).To(Succeed())
				g.Expect(got.Finalizers).To(ContainElement(replicaBindingFinalizer))
			}, "5s", "100ms").Should(Succeed())

			// Delete RB and ensure it doesn't disappear until member deployment is gone.
			Expect(k8sClient.Delete(ctx, rb)).To(Succeed())
			Eventually(func(g Gomega) {
				var got corev1alpha1.ReplicaBinding
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, &got)).To(Succeed())
				g.Expect(got.DeletionTimestamp.IsZero()).To(BeFalse())
			}, "2s", "100ms").Should(Succeed())

			// Member deployment should be deleted by controller.
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, &appsv1.Deployment{})
				return apierrors.IsNotFound(err)
			}, "5s", "100ms").Should(BeTrue())

			// Eventually RB should be fully deleted.
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, &corev1alpha1.ReplicaBinding{})
				return apierrors.IsNotFound(err)
			}, "5s", "100ms").Should(BeTrue())
		})
	})
})
