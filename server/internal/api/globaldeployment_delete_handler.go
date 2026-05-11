package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type DeleteGlobalDeploymentRequest struct {
	Namespace string `json:"namespace" binding:"required"`
	Name      string `json:"name" binding:"required"`
}

type DeleteGlobalDeploymentResponse struct {
	Message string `json:"message,omitempty"`
}

// GlobalDeploymentDeleter provides all ops needed by DeleteGlobalDeployment.
// We re-use existing typed CR getters in kube layer, and do member deletion here.
// This keeps server-side delete flow independent from controller logic.
//
// NOTE: member kubeconfig is stored in Cluster.spec.secretRef (same as controller uses).
// We read Cluster CR + Secret from control namespace.
//
// All methods should be implemented by kube.ClusterApplier.
// (We keep this interface in api package to avoid circular deps.)
//
//go:generate false

type GlobalDeploymentDeleter interface {
	GetTypedGlobalDeploymentCR(ctx *gin.Context, ns string, name string) (*corev1alpha1.GlobalDeployment, error)
	ListTypedReplicaBindingCRs(ctx *gin.Context, ns string) (*corev1alpha1.ReplicaBindingList, error)
	DeleteTypedReplicaBindingCR(ctx *gin.Context, ns string, name string) error
	DeleteTypedGlobalDeploymentCR(ctx *gin.Context, ns string, name string) error
	GetTypedClusterCR(ctx *gin.Context, ns string, clusterID string) (*corev1alpha1.Cluster, error)
	GetSecret(ctx *gin.Context, ns string, name string) (*corev1.Secret, error)
}

func (h *GlobalDeploymentHandler) RegisterDelete(rg *gin.RouterGroup) {
	rg.POST("/DeleteGlobalDeployment", h.DeleteGlobalDeployment)
}

func (h *GlobalDeploymentHandler) DeleteGlobalDeployment(c *gin.Context) {
	var req DeleteGlobalDeploymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	name := strings.TrimSpace(req.Name)
	ns := strings.TrimSpace(req.Namespace)
	if name == "" || ns == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and namespace are required"})
		return
	}

	k, ok := h.kube.(GlobalDeploymentDeleter)
	if !ok {
		h.logger.Error("kube does not implement GlobalDeploymentDeleter")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server misconfigured"})
		return
	}

	// 1) get GlobalDeployment (for existence)
	gd, err := k.GetTypedGlobalDeploymentCR(c, ns, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "globaldeployment not found"})
			return
		}
		h.logger.Error("get globaldeployment failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get globaldeployment failed"})
		return
	}

	// 2) list bindings, filter owned
	list, err := k.ListTypedReplicaBindingCRs(c, ns)
	if err != nil {
		h.logger.Error("list replicabindings failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list replicabindings failed"})
		return
	}

	for _, rb := range list.Items {
		if rb.Spec.GlobalDeploymentRef.Name != gd.Name || rb.Spec.GlobalDeploymentRef.Namespace != gd.Namespace {
			continue
		}

		// 2.1) delete member deployment if we have scheduling info
		if rb.Spec.TargetCluster != "" {
			if derr := h.deleteMemberDeployment(c, k, rb.Spec.TargetCluster, gd.Namespace, fmt.Sprintf("%s-r%d", gd.Name, rb.Spec.ReplicaIndex)); derr != nil {
				// best-effort: log and continue; still delete RB/GD to let controller clean up if needed
				h.logger.Warn("delete member deployment failed", zap.String("clusterId", rb.Spec.TargetCluster), zap.String("deployment", fmt.Sprintf("%s-r%d", gd.Name, rb.Spec.ReplicaIndex)), zap.Error(derr))
			}
		}

		// 2.2) delete replicaBinding
		if derr := k.DeleteTypedReplicaBindingCR(c, ns, rb.Name); derr != nil {
			if !apierrors.IsNotFound(derr) {
				h.logger.Warn("delete replicabinding failed", zap.String("replicaBinding", rb.Name), zap.Error(derr))
			}
		}
	}

	// 3) delete GlobalDeployment
	if err := k.DeleteTypedGlobalDeploymentCR(c, ns, gd.Name); err != nil {
		if apierrors.IsNotFound(err) {
			c.JSON(http.StatusOK, DeleteGlobalDeploymentResponse{Message: "GlobalDeployment deletion started"})
			return
		}
		h.logger.Error("delete globaldeployment failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete globaldeployment failed"})
		return
	}

	c.JSON(http.StatusOK, DeleteGlobalDeploymentResponse{Message: "GlobalDeployment deletion started"})
}

func (h *GlobalDeploymentHandler) deleteMemberDeployment(c *gin.Context, k GlobalDeploymentDeleter, clusterID string, workloadNS string, depName string) error {
	controlNS := h.cfg.Namespace
	if controlNS == "" {
		controlNS = workloadNS
	}

	clusterCR, err := k.GetTypedClusterCR(c, controlNS, clusterID)
	if err != nil {
		return fmt.Errorf("get cluster CR: %w", err)
	}
	sec, err := k.GetSecret(c, controlNS, clusterCR.Spec.SecretRef)
	if err != nil {
		return fmt.Errorf("get cluster secret: %w", err)
	}
	kubeconfig := string(sec.Data["kubeconfig"])
	if kubeconfig == "" {
		// fallback common keys
		kubeconfig = string(sec.Data["value"])
	}
	if kubeconfig == "" {
		return fmt.Errorf("kubeconfig is empty in secret %s/%s", controlNS, sec.Name)
	}

	cs, err := buildMemberClientset(kubeconfig)
	if err != nil {
		return err
	}

	// best-effort delete
	err = cs.AppsV1().Deployments(workloadNS).Delete(c.Request.Context(), depName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete deployment %s/%s: %w", workloadNS, depName, err)
	}
	return nil
}

// buildMemberClientset creates a Kubernetes clientset from kubeconfig YAML.
func buildMemberClientset(kubeconfigYAML string) (*kubernetes.Clientset, error) {
	restCfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil {
		return nil, fmt.Errorf("build rest config from kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("new clientset: %w", err)
	}
	return cs, nil
}
