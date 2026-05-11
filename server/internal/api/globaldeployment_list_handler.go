package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	corev1alpha1 "github.com/pangjian-pj/kubeX/kubeX-controller/api/v1alpha1"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ListGlobalDeploymentsRequest struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Page      int    `json:"page,omitempty"`
	PageSize  int    `json:"pageSize,omitempty"`
}

type GlobalDeploymentItem struct {
	Name              string      `json:"name"`
	Namespace         string      `json:"namespace"`
	Replicas          int32       `json:"replicas"`
	RunningReplicas   int32       `json:"runningReplicas"`
	FailedReplicas    int32       `json:"failedReplicas"`
	Phase             string      `json:"phase"`
	CreationTimestamp metav1.Time `json:"creationTimestamp"`
}

type ListGlobalDeploymentsResponse struct {
	Total int                    `json:"total"`
	Items []GlobalDeploymentItem `json:"items"`
}

type GlobalDeploymentLister interface {
	ListTypedGlobalDeploymentCRs(ctx *gin.Context, ns string) (*corev1alpha1.GlobalDeploymentList, error)
}

// RegisterList registers list endpoint on the same /api/v1 group.
func (h *GlobalDeploymentHandler) RegisterList(rg *gin.RouterGroup) {
	rg.POST("/ListGlobalDeployments", h.ListGlobalDeployments)
}

func (h *GlobalDeploymentHandler) ListGlobalDeployments(c *gin.Context) {
	var req ListGlobalDeploymentsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ns := strings.TrimSpace(req.Namespace)
	if ns == "" {
		ns = h.cfg.Namespace
	}
	nameFilter := strings.TrimSpace(req.Name)
	page := req.Page
	pageSize := req.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}

	list, err := h.kube.(interface {
		ListTypedGlobalDeploymentCRs(ctx *gin.Context, ns string) (*corev1alpha1.GlobalDeploymentList, error)
	}).ListTypedGlobalDeploymentCRs(c, ns)
	if err != nil {
		h.logger.Error("list globaldeployments failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list globaldeployments failed"})
		return
	}

	items := make([]GlobalDeploymentItem, 0, len(list.Items))
	for _, gd := range list.Items {
		if nameFilter != "" && !strings.Contains(gd.Name, nameFilter) {
			continue
		}
		replicas := int32(1)
		if gd.Spec.Replicas != nil {
			replicas = *gd.Spec.Replicas
		}
		items = append(items, GlobalDeploymentItem{
			Name:              gd.Name,
			Namespace:         gd.Namespace,
			Replicas:          replicas,
			RunningReplicas:   gd.Status.Running,
			FailedReplicas:    gd.Status.Failed,
			Phase:             string(gd.Status.Phase),
			CreationTimestamp: gd.CreationTimestamp,
		})
	}

	// Stable ordering: newest first
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreationTimestamp.After(items[j].CreationTimestamp.Time)
	})

	total := len(items)
	start := (page - 1) * pageSize
	if start >= total {
		c.JSON(http.StatusOK, ListGlobalDeploymentsResponse{Total: total, Items: []GlobalDeploymentItem{}})
		return
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	c.JSON(http.StatusOK, ListGlobalDeploymentsResponse{Total: total, Items: items[start:end]})
}
