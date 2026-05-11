package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
)

type GetGlobalDeploymentRequest struct {
	Name      string `json:"name" binding:"required"`
	Namespace string `json:"namespace" binding:"required"`
}

type GetGlobalDeploymentBinding struct {
	ReplicaIndex       int32  `json:"replicaIndex"`
	ClusterID          string `json:"clusterId"`
	ClusterName        string `json:"clusterName,omitempty"`
	NodeName           string `json:"nodeName"`
	Phase              string `json:"phase"`
	LastError          string `json:"lastError,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
	PodName            string `json:"podName,omitempty"`
	PodIP              string `json:"podIP,omitempty"`
	Attempt            int32  `json:"attempt,omitempty"`
}

type GetGlobalDeploymentResponse struct {
	Name            string                       `json:"name"`
	Namespace       string                       `json:"namespace"`
	Labels          map[string]string            `json:"labels,omitempty"`
	Annotations     map[string]string            `json:"annotations,omitempty"`
	Replicas        int32                        `json:"replicas"`
	RunningReplicas int32                        `json:"runningReplicas"`
	FailedReplicas  int32                        `json:"failedReplicas"`
	Phase           string                       `json:"phase"`
	Template        any                          `json:"template,omitempty"`
	Bindings        []GetGlobalDeploymentBinding `json:"bindings"`
}

// GlobalDeploymentGetter lists/gets GlobalDeployment and ReplicaBinding typed CRs.
type GlobalDeploymentGetter interface {
	GetTypedGlobalDeploymentCR(ctx *gin.Context, ns string, name string) (*corev1alpha1.GlobalDeployment, error)
	ListTypedReplicaBindingCRs(ctx *gin.Context, ns string) (*corev1alpha1.ReplicaBindingList, error)
	GetTypedClusterCR(ctx *gin.Context, ns string, clusterID string) (*corev1alpha1.Cluster, error)
}

func (h *GlobalDeploymentHandler) RegisterGet(rg *gin.RouterGroup) {
	rg.POST("/GetGlobalDeployment", h.GetGlobalDeployment)
}

func (h *GlobalDeploymentHandler) GetGlobalDeployment(c *gin.Context) {
	var req GetGlobalDeploymentRequest
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

	getter, ok := h.kube.(GlobalDeploymentGetter)
	if !ok {
		h.logger.Error("kube does not implement GlobalDeploymentGetter")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server misconfigured"})
		return
	}

	gd, err := getter.GetTypedGlobalDeploymentCR(c, ns, name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get globaldeployment failed"})
		return
	}

	bindings, err := getter.ListTypedReplicaBindingCRs(c, ns)
	if err != nil {
		h.logger.Error("list replicabindings failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list replicabindings failed"})
		return
	}

	// Decode template to a generic object for UI.
	var templateObj any
	if len(gd.Spec.Template.Raw) > 0 {
		_ = json.Unmarshal(gd.Spec.Template.Raw, &templateObj)
		// If raw isn't JSON (should be JSON bytes), fallback to returning it as string.
		if templateObj == nil {
			templateObj = string(gd.Spec.Template.Raw)
		}
	}

	replicas := int32(1)
	if gd.Spec.Replicas != nil {
		replicas = *gd.Spec.Replicas
	}

	out := GetGlobalDeploymentResponse{
		Name:            gd.Name,
		Namespace:       gd.Namespace,
		Labels:          gd.Labels,
		Annotations:     gd.Annotations,
		Replicas:        replicas,
		RunningReplicas: gd.Status.Running,
		FailedReplicas:  gd.Status.Failed,
		Phase:           string(gd.Status.Phase),
		Template:        templateObj,
		Bindings:        make([]GetGlobalDeploymentBinding, 0),
	}

	// Best-effort cache for id -> name to avoid repeated reads.
	clusterNameCache := map[string]string{}

	for _, rb := range bindings.Items {
		if rb.Spec.GlobalDeploymentRef.Name != gd.Name || rb.Spec.GlobalDeploymentRef.Namespace != gd.Namespace {
			continue
		}
		clusterID := rb.Spec.TargetCluster
		clusterName := ""
		if clusterID != "" {
			if v, ok := clusterNameCache[clusterID]; ok {
				clusterName = v
			} else {
				// Cluster CRs are stored in control namespace (same as server config namespace).
				// We use annotation kubex.io/name as the display name.
				cr, cerr := getter.GetTypedClusterCR(c, h.cfg.Namespace, clusterID)
				if cerr == nil && cr != nil {
					clusterName = strings.TrimSpace(cr.Annotations[AnnotationName])
					if clusterName == "" {
						// fallback to clusterID
						clusterName = clusterID
					}
				} else {
					// fallback to clusterID
					clusterName = clusterID
				}
				clusterNameCache[clusterID] = clusterName
			}
		}
		b := GetGlobalDeploymentBinding{
			ReplicaIndex: rb.Spec.ReplicaIndex,
			ClusterID:    clusterID,
			ClusterName:  clusterName,
			NodeName:     rb.Spec.TargetNodeName,
			Phase:        string(rb.Status.Phase),
			LastError:    rb.Status.LastError,
		}
		if !rb.Status.LastTransitionTime.IsZero() {
			b.LastTransitionTime = rb.Status.LastTransitionTime.Time.Format(timeRFC3339)
		}
		out.Bindings = append(out.Bindings, b)
	}

	c.JSON(http.StatusOK, out)
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

// keep runtime import used (for template any)
var _ = runtime.RawExtension{}
