package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: kubeX-web 的 http.ts 默认 baseURL=/api，因此这里统一挂在 /api/v1 下。

type OptimizationHandler struct {
	logger *zap.Logger
	cfg    OptimizationHandlerConfig
	kube   OptimizationHandlerKube
}

type OptimizationHandlerConfig struct {
	Namespace         string
	TopologyNamespace string
}

type OptimizationHandlerKube interface {
	// OptimizationPolicy
	ApplyTypedOptimizationPolicyCR(ctx *gin.Context, ns string, pol *corev1alpha1.OptimizationPolicy) error
	GetTypedOptimizationPolicyCR(ctx *gin.Context, ns, name string) (*corev1alpha1.OptimizationPolicy, error)
	ListTypedOptimizationPolicyCRs(ctx *gin.Context, ns string) (*corev1alpha1.OptimizationPolicyList, error)
	DeleteTypedOptimizationPolicyCR(ctx *gin.Context, ns, name string) error

	// Plan
	GetTypedReOrchestrationPlanCR(ctx *gin.Context, ns, name string) (*corev1alpha1.ReOrchestrationPlan, error)
	ListTypedReOrchestrationPlanCRs(ctx *gin.Context, ns string) (*corev1alpha1.ReOrchestrationPlanList, error)
}

func NewOptimizationHandler(logger *zap.Logger, cfg OptimizationHandlerConfig, kube OptimizationHandlerKube) *OptimizationHandler {
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.TopologyNamespace == "" {
		cfg.TopologyNamespace = "kubex-system"
	}
	return &OptimizationHandler{logger: logger, cfg: cfg, kube: kube}
}

func (h *OptimizationHandler) Register(rg *gin.RouterGroup) {
	// Policy
	rg.POST("/ListOptimizationPolicies", h.ListOptimizationPolicies)
	rg.POST("/GetOptimizationPolicy", h.GetOptimizationPolicy)
	rg.POST("/CreateOptimizationPolicy", h.CreateOptimizationPolicy)
	rg.POST("/UpdateOptimizationPolicy", h.UpdateOptimizationPolicy)
	rg.POST("/DeleteOptimizationPolicy", h.DeleteOptimizationPolicy)

	// Plans
	rg.POST("/ListReOrchestrationPlans", h.ListReOrchestrationPlans)
	rg.POST("/GetReOrchestrationPlan", h.GetReOrchestrationPlan)

	// Topology
	rg.POST("/ListTopologyConfigs", h.ListTopologyConfigs)
}

type ListOptimizationPoliciesRequest struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Page      int    `json:"page,omitempty"`
	PageSize  int    `json:"pageSize,omitempty"`
}

type OptimizationPolicyItem struct {
	Name               string      `json:"name"`
	Namespace          string      `json:"namespace"`
	Enabled            bool        `json:"enabled"`
	RunMode            string      `json:"runMode,omitempty"`
	RebalancePoint     string      `json:"rebalancePoint,omitempty"`
	Strategy           string      `json:"strategy,omitempty"`
	ThresholdPercent   int32       `json:"thresholdPercent,omitempty"`
	Phase              string      `json:"phase,omitempty"`
	LastEvaluationTime *time.Time  `json:"lastEvaluationTime,omitempty"`
	CreationTimestamp  metav1.Time `json:"creationTimestamp"`
}

type ListOptimizationPoliciesResponse struct {
	Total int                      `json:"total"`
	Items []OptimizationPolicyItem `json:"items"`
}

func (h *OptimizationHandler) ListOptimizationPolicies(c *gin.Context) {
	var req ListOptimizationPoliciesRequest
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

	list, err := h.kube.ListTypedOptimizationPolicyCRs(c, ns)
	if err != nil {
		h.logger.Error("list optimizationpolicies failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list optimizationpolicies failed"})
		return
	}

	items := make([]OptimizationPolicyItem, 0, len(list.Items))
	for _, pol := range list.Items {
		if nameFilter != "" && !strings.Contains(pol.Name, nameFilter) {
			continue
		}
		it := OptimizationPolicyItem{
			Name:              pol.Name,
			Namespace:         pol.Namespace,
			Enabled:           pol.Spec.Enabled,
			RunMode:           string(pol.Spec.RunMode),
			RebalancePoint:    pol.Spec.RebalancePoint,
			Strategy:          string(pol.Spec.Strategy),
			ThresholdPercent:  pol.Spec.ImprovementThresholdPercent,
			Phase:             string(pol.Status.Phase),
			CreationTimestamp: pol.CreationTimestamp,
		}
		if !pol.Status.LastEvaluationTime.IsZero() {
			t := pol.Status.LastEvaluationTime.Time
			it.LastEvaluationTime = &t
		}
		items = append(items, it)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].CreationTimestamp.After(items[j].CreationTimestamp.Time)
	})

	total := len(items)
	start := (page - 1) * pageSize
	if start >= total {
		c.JSON(http.StatusOK, ListOptimizationPoliciesResponse{Total: total, Items: []OptimizationPolicyItem{}})
		return
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	c.JSON(http.StatusOK, ListOptimizationPoliciesResponse{Total: total, Items: items[start:end]})
}

type GetOptimizationPolicyRequest struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name" binding:"required"`
}

type GetOptimizationPolicyResponse struct {
	Policy *corev1alpha1.OptimizationPolicy `json:"policy"`
}

func (h *OptimizationHandler) GetOptimizationPolicy(c *gin.Context) {
	var req GetOptimizationPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ns := strings.TrimSpace(req.Namespace)
	if ns == "" {
		ns = h.cfg.Namespace
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	pol, err := h.kube.GetTypedOptimizationPolicyCR(c, ns, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "optimizationPolicy not found"})
			return
		}
		h.logger.Error("get optimizationpolicy failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get optimizationPolicy failed"})
		return
	}

	c.JSON(http.StatusOK, GetOptimizationPolicyResponse{Policy: pol})
}

type ApplyOptimizationPolicyRequest struct {
	Policy *corev1alpha1.OptimizationPolicy `json:"policy" binding:"required"`
}

type ApplyOptimizationPolicyResponse struct {
	Message string `json:"message,omitempty"`
}

func (h *OptimizationHandler) CreateOptimizationPolicy(c *gin.Context) {
	h.applyOptimizationPolicy(c, true)
}

func (h *OptimizationHandler) UpdateOptimizationPolicy(c *gin.Context) {
	h.applyOptimizationPolicy(c, false)
}

func (h *OptimizationHandler) applyOptimizationPolicy(c *gin.Context, create bool) {
	var req ApplyOptimizationPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Policy == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "policy is required"})
		return
	}
	pol := req.Policy.DeepCopy()

	if strings.TrimSpace(pol.Namespace) == "" {
		pol.Namespace = h.cfg.Namespace
	}
	if strings.TrimSpace(pol.Namespace) == "" {
		pol.Namespace = "default"
	}
	if strings.TrimSpace(pol.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "policy.metadata.name is required"})
		return
	}

	if err := h.kube.ApplyTypedOptimizationPolicyCR(c, pol.Namespace, pol); err != nil {
		if create && apierrors.IsAlreadyExists(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "optimizationPolicy already exists"})
			return
		}
		h.logger.Error("apply optimizationpolicy failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "apply optimizationPolicy failed"})
		return
	}

	msg := "OptimizationPolicy updated successfully"
	if create {
		msg = "OptimizationPolicy created successfully"
	}
	c.JSON(http.StatusOK, ApplyOptimizationPolicyResponse{Message: msg})
}

type DeleteOptimizationPolicyRequest struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name" binding:"required"`
}

type DeleteOptimizationPolicyResponse struct {
	Message string `json:"message,omitempty"`
}

func (h *OptimizationHandler) DeleteOptimizationPolicy(c *gin.Context) {
	var req DeleteOptimizationPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ns := strings.TrimSpace(req.Namespace)
	if ns == "" {
		ns = h.cfg.Namespace
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	if err := h.kube.DeleteTypedOptimizationPolicyCR(c, ns, name); err != nil {
		if apierrors.IsNotFound(err) {
			c.JSON(http.StatusOK, DeleteOptimizationPolicyResponse{Message: "already deleted"})
			return
		}
		h.logger.Error("delete optimizationpolicy failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete optimizationPolicy failed"})
		return
	}

	c.JSON(http.StatusOK, DeleteOptimizationPolicyResponse{Message: "deleted"})
}

type ListReOrchestrationPlansRequest struct {
	Namespace  string `json:"namespace,omitempty"`
	PolicyName string `json:"policyName,omitempty"`
	Page       int    `json:"page,omitempty"`
	PageSize   int    `json:"pageSize,omitempty"`
}

type ReOrchestrationPlanItem struct {
	Name              string      `json:"name"`
	Namespace         string      `json:"namespace"`
	PolicyName        string      `json:"policyName,omitempty"`
	Phase             string      `json:"phase,omitempty"`
	Moves             int         `json:"moves"`
	CreationTimestamp metav1.Time `json:"creationTimestamp"`
}

type ListReOrchestrationPlansResponse struct {
	Total int                       `json:"total"`
	Items []ReOrchestrationPlanItem `json:"items"`
}

func (h *OptimizationHandler) ListReOrchestrationPlans(c *gin.Context) {
	var req ListReOrchestrationPlansRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ns := strings.TrimSpace(req.Namespace)
	if ns == "" {
		ns = h.cfg.Namespace
	}
	policyName := strings.TrimSpace(req.PolicyName)
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

	list, err := h.kube.ListTypedReOrchestrationPlanCRs(c, ns)
	if err != nil {
		h.logger.Error("list plans failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list plans failed"})
		return
	}

	items := make([]ReOrchestrationPlanItem, 0, len(list.Items))
	for _, p := range list.Items {
		pn := ""
		pn = strings.TrimSpace(p.Spec.Summary.PolicyName)
		if pn == "" {
			if v, ok := p.Labels["kubex.io/policy"]; ok {
				pn = strings.TrimSpace(v)
			}
		}
		if policyName != "" && pn != policyName {
			continue
		}
		moves := 0
		if p.Spec.Moves != nil {
			moves = len(p.Spec.Moves)
		}
		items = append(items, ReOrchestrationPlanItem{
			Name:              p.Name,
			Namespace:         p.Namespace,
			PolicyName:        pn,
			Phase:             string(p.Status.Phase),
			Moves:             moves,
			CreationTimestamp: p.CreationTimestamp,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].CreationTimestamp.After(items[j].CreationTimestamp.Time)
	})

	total := len(items)
	start := (page - 1) * pageSize
	if start >= total {
		c.JSON(http.StatusOK, ListReOrchestrationPlansResponse{Total: total, Items: []ReOrchestrationPlanItem{}})
		return
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	c.JSON(http.StatusOK, ListReOrchestrationPlansResponse{Total: total, Items: items[start:end]})
}

type GetReOrchestrationPlanRequest struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name" binding:"required"`
}

type GetReOrchestrationPlanResponse struct {
	Plan *corev1alpha1.ReOrchestrationPlan `json:"plan"`
}

func (h *OptimizationHandler) GetReOrchestrationPlan(c *gin.Context) {
	var req GetReOrchestrationPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ns := strings.TrimSpace(req.Namespace)
	if ns == "" {
		ns = h.cfg.Namespace
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	plan, err := h.kube.GetTypedReOrchestrationPlanCR(c, ns, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "plan not found"})
			return
		}
		h.logger.Error("get plan failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get plan failed"})
		return
	}

	c.JSON(http.StatusOK, GetReOrchestrationPlanResponse{Plan: plan})
}

type ListTopologyConfigsRequest struct {
	// optional. if empty, server uses configured TopologyNamespace.
	Namespace string `json:"namespace,omitempty"`
}

type TopologyConfigItem struct {
	Name              string      `json:"name"`
	Namespace         string      `json:"namespace"`
	CreationTimestamp metav1.Time `json:"creationTimestamp"`
}

type ListTopologyConfigsResponse struct {
	Items []TopologyConfigItem `json:"items"`
}

func (h *OptimizationHandler) ListTopologyConfigs(c *gin.Context) {
	var req ListTopologyConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ns := strings.TrimSpace(req.Namespace)
	if ns == "" {
		ns = h.cfg.TopologyNamespace
	}

	// We can't rely on typed imports for configmaps here without introducing clientset deps.
	// Reuse KV handler pattern? For now, keep it simple and return an empty list if kube layer doesn't support it.
	// The actual implementation lives in kube applier (cluster_apply.go) using client-go.
	items, err := listConfigMapsByPrefix(c, h.logger, ns, "topology-")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, ListTopologyConfigsResponse{Items: items})
}
