package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"github.com/pangjian-pj/KubeMorph/server/internal/kube"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const AnnotationDescription = "kubex.io/description"
const AnnotationName = "kubex.io/name"

type ListClustersRequest struct {
	Page          int    `form:"page"`
	PageSize      int    `form:"pageSize"`
	Name          string `form:"name"`
	Provider      string `form:"provider"`
	Status        string `form:"status"`
	LabelSelector string `form:"labelSelector"`
}

type ListClustersResponse struct {
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
	Items    []ClusterSummary `json:"items"`
}

type ClusterSummary struct {
	ClusterID   string            `json:"clusterId,omitempty"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Status      string            `json:"status,omitempty"`
	APIEndpoint string            `json:"apiEndpoint,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	CreatedAt   string            `json:"createdAt,omitempty"`
	UpdatedAt   string            `json:"updatedAt,omitempty"`
	Message     string            `json:"message,omitempty"`
}

type ImportClusterRequest struct {
	Name        string            `json:"name" binding:"required"`
	Description string            `json:"description,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Kubeconfig  string            `json:"kubeconfig" binding:"required"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type ImportClusterResponse struct {
	ClusterID string `json:"clusterId,omitempty"`
	Name      string `json:"name,omitempty"`
	Status    string `json:"status,omitempty"`
	Message   string `json:"message,omitempty"`
}

type GetClusterRequest struct {
	ClusterID string `form:"clusterId" binding:"required"`
}

type GetClusterResponse struct {
	Cluster     ClusterSummary   `json:"cluster"`
	Kubeconfig  string           `json:"kubeconfig,omitempty"`
	Nodes       []NodeItem       `json:"nodes,omitempty"`
	Resources   ClusterResources `json:"resources,omitzero"`
	SecretName  string           `json:"secretName,omitempty"`
	APIEndpoint string           `json:"apiEndpoint,omitempty"`
}

type ClusterResources struct {
	Capacity    ResourceSummary `json:"capacity,omitzero"`
	Allocatable ResourceSummary `json:"allocatable,omitzero"`
	NodeCount   int64           `json:"nodeCount,omitempty"`
	PodCount    int64           `json:"podCount,omitempty"`
}

type ResourceSummary struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type NodeItem struct {
	Name   string            `json:"name,omitempty"`
	UID    string            `json:"uid,omitempty"`
	Ready  bool              `json:"ready,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`

	Allocatable ResourceSummary `json:"allocatable,omitzero"`
	Requested   ResourceSummary `json:"requested,omitzero"`
	Free        ResourceSummary `json:"free,omitzero"`
}

type ClusterHandler struct {
	logger *zap.Logger
	etcd   *clientv3.Client
	cfg    ClusterHandlerConfig
	kube   ClusterHandlerKube
}

type ClusterHandlerConfig struct {
	EtcdPrefix string

	Namespace string
	CRDGroup  string
	CRDVer    string
	CRDPlural string
}

type ClusterHandlerKube interface {
	CreateSecret(ctx *gin.Context, ns string, s *corev1.Secret) (*corev1.Secret, error)
	ApplyTypedClusterCR(ctx *gin.Context, ns string, cluster *corev1alpha1.Cluster) error
	ListTypedClusterCRs(ctx *gin.Context, ns string) (*corev1alpha1.ClusterList, error)
	GetTypedClusterCR(ctx *gin.Context, ns string, clusterID string) (*corev1alpha1.Cluster, error)
	DeleteTypedClusterCR(ctx *gin.Context, ns string, clusterID string) error
	GetSecret(ctx *gin.Context, ns string, name string) (*corev1.Secret, error)
	GetNodesFromKubeconfig(ctx context.Context, kubeconfigYAML string) (*corev1.NodeList, error)
}

func NewClusterHandler(logger *zap.Logger, etcd *clientv3.Client, cfg ClusterHandlerConfig, kube ClusterHandlerKube) *ClusterHandler {
	return &ClusterHandler{logger: logger, etcd: etcd, cfg: cfg, kube: kube}
}

func (h *ClusterHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/ImportCluster", h.ImportCluster)
	rg.GET("/ListClusters", h.ListClusters)
	rg.GET("/GetCluster", h.GetCluster)
	rg.POST("/DeleteClusters", h.DeleteClusters)
	rg.POST("/UpdateCluster", h.UpdateCluster)
}

type UpdateClusterRequest struct {
	ClusterID   string            `json:"clusterId" binding:"required"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type UpdateClusterResponse struct {
	ClusterID   string            `json:"clusterId,omitempty"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	UpdatedAt   string            `json:"updatedAt,omitempty"`
	Message     string            `json:"message,omitempty"`
}

func (h *ClusterHandler) UpdateCluster(c *gin.Context) {
	var req UpdateClusterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 1) GET cluster
	cr, err := h.kube.GetTypedClusterCR(c, h.cfg.Namespace, req.ClusterID)
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
			return
		}
		h.logger.Error("get cluster cr failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get cluster failed"})
		return
	}

	// 2) 更新 annotations/name/description
	if cr.Annotations == nil {
		cr.Annotations = map[string]string{}
	}
	if req.Name != "" {
		cr.Annotations[AnnotationName] = req.Name
	}
	// description：前端会始终传入（允许清空）
	if req.Description == "" {
		delete(cr.Annotations, AnnotationDescription)
	} else {
		cr.Annotations[AnnotationDescription] = req.Description
	}

	// 3) 更新 labels（保留 system labels），provider 单独处理
	newLabels := map[string]string{}
	for k, v := range cr.Labels {
		// 保留 kubernetes 内置/系统标签，避免传 nil 时被清掉
		newLabels[k] = v
	}

	// 将用户 labels 合并进去（允许覆盖同名 key）
	if req.Labels != nil {
		for k, v := range req.Labels {
			newLabels[k] = v
		}
	}
	if req.Provider != "" {
		newLabels["provider"] = req.Provider
	}
	cr.Labels = newLabels

	// 4) Apply 更新
	if err := h.kube.ApplyTypedClusterCR(c, h.cfg.Namespace, cr); err != nil {
		h.logger.Error("apply cluster cr failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update cluster failed"})
		return
	}

	// 5) 取回最新对象，updatedAt 使用 controller 写入的 status.updatedAt
	updatedAt := ""
	latest, gerr := h.kube.GetTypedClusterCR(c, h.cfg.Namespace, req.ClusterID)
	if gerr != nil {
		h.logger.Warn("get latest cluster cr failed", zap.Error(gerr))
	} else {
		cr = latest
		if !cr.Status.UpdatedAt.IsZero() {
			updatedAt = cr.Status.UpdatedAt.Time.UTC().Format(time.RFC3339)
		}
	}

	name := cr.Annotations[AnnotationName]
	desc := cr.Annotations[AnnotationDescription]
	provider := cr.Labels["provider"]
	labels := cr.Labels

	c.JSON(http.StatusOK, UpdateClusterResponse{
		ClusterID:   req.ClusterID,
		Name:        name,
		Description: desc,
		Provider:    provider,
		Labels:      labels,
		UpdatedAt:   updatedAt,
		Message:     "Cluster updated successfully",
	})
}

type DeleteClustersRequest struct {
	ClusterIDs []string `json:"clusterIds" binding:"required"`
}

type DeleteClustersResponse struct {
	Deleted []string            `json:"deleted"`
	Failed  []DeleteClusterFail `json:"failed"`
	Message string              `json:"message,omitempty"`
}

type DeleteClusterFail struct {
	ClusterID string `json:"clusterId"`
	Message   string `json:"message"`
}

func (h *ClusterHandler) DeleteClusters(c *gin.Context) {
	var req DeleteClustersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 去重 + 清洗
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(req.ClusterIDs))
	for _, id := range req.ClusterIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clusterIds is required"})
		return
	}

	resp := DeleteClustersResponse{Deleted: []string{}, Failed: []DeleteClusterFail{}}
	for _, id := range ids {
		err := h.kube.DeleteTypedClusterCR(c, h.cfg.Namespace, id)
		if err == nil {
			resp.Deleted = append(resp.Deleted, id)
			continue
		}
		if apierrors.IsNotFound(err) {
			resp.Failed = append(resp.Failed, DeleteClusterFail{ClusterID: id, Message: "cluster not found"})
			continue
		}
		h.logger.Error("delete cluster cr failed", zap.String("clusterId", id), zap.Error(err))
		resp.Failed = append(resp.Failed, DeleteClusterFail{ClusterID: id, Message: err.Error()})
	}

	resp.Message = "Cluster deletion completed"
	// 语义上这里始终 200，让前端按 deleted/failed 做分段提示
	c.JSON(http.StatusOK, resp)
}

func (h *ClusterHandler) GetCluster(c *gin.Context) {
	var req GetClusterRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 查 Cluster CR
	cr, err := h.kube.GetTypedClusterCR(c, h.cfg.Namespace, req.ClusterID)
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
			return
		}
		h.logger.Error("get cluster cr failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get cluster failed"})
		return
	}

	name := cr.Annotations[AnnotationName]
	desc := cr.Annotations[AnnotationDescription]
	provider := cr.Labels["provider"]
	status := string(cr.Status.Phase)
	if status == "" {
		status = string(corev1alpha1.ClusterPhaseImporting)
	}

	clusterSummary := ClusterSummary{
		ClusterID:   cr.Name,
		Name:        name,
		Description: desc,
		Provider:    provider,
		Status:      status,
		APIEndpoint: cr.Spec.APIEndpoint,
		Labels:      cr.Labels,
		CreatedAt:   cr.CreationTimestamp.Time.UTC().Format(time.RFC3339),
		UpdatedAt:   "",
		Message:     "",
	}
	if !cr.Status.UpdatedAt.IsZero() {
		clusterSummary.UpdatedAt = cr.Status.UpdatedAt.Time.UTC().Format(time.RFC3339)
	}

	// 查 Secret 拿 kubeconfig
	secretName := cr.Spec.SecretRef
	sec, err := h.kube.GetSecret(c, h.cfg.Namespace, secretName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "cluster secret not found"})
			return
		}
		h.logger.Error("get secret failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get cluster secret failed"})
		return
	}

	kubeconfigBytes := sec.Data["kubeconfig"]
	kubeconfig := string(kubeconfigBytes)

	resp := GetClusterResponse{
		Cluster:     clusterSummary,
		Kubeconfig:  kubeconfig,
		SecretName:  secretName,
		APIEndpoint: cr.Spec.APIEndpoint,
		Resources: ClusterResources{
			Capacity: ResourceSummary{
				CPU:    cr.Status.Resources.Capacity.CPU.String(),
				Memory: cr.Status.Resources.Capacity.Memory.String(),
			},
			Allocatable: ResourceSummary{
				CPU:    cr.Status.Resources.Allocatable.CPU.String(),
				Memory: cr.Status.Resources.Allocatable.Memory.String(),
			},
			NodeCount: cr.Status.Resources.NodeCount,
			PodCount:  cr.Status.Resources.PodCount,
		},
	}

	// 节点信息已聚合到 Cluster CR status.nodes，直接读取返回
	if len(cr.Status.Nodes) > 0 {
		nodes := make([]NodeItem, 0, len(cr.Status.Nodes))
		for _, n := range cr.Status.Nodes {
			nodes = append(nodes, NodeItem{
				Name:  n.Name,
				UID:   n.UID,
				Ready: n.Ready,
				Labels: func() map[string]string {
					if len(n.Labels) == 0 {
						return nil
					}
					m := make(map[string]string, len(n.Labels))
					for k, v := range n.Labels {
						m[k] = v
					}
					return m
				}(),
				Allocatable: ResourceSummary{
					CPU:    n.Allocatable.CPU.String(),
					Memory: n.Allocatable.Memory.String(),
				},
				Requested: ResourceSummary{
					CPU:    n.Requested.CPU.String(),
					Memory: n.Requested.Memory.String(),
				},
				Free: ResourceSummary{
					CPU:    n.Free.CPU.String(),
					Memory: n.Free.Memory.String(),
				},
			})
		}
		resp.Nodes = nodes
	}

	c.JSON(http.StatusOK, resp)
}

func (h *ClusterHandler) ImportCluster(c *gin.Context) {
	var req ImportClusterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	clusterID := uuid.NewString()

	apiEndpoint, err := kube.ExtractServerFromKubeconfig(req.Kubeconfig)
	if err != nil {
		h.logger.Warn("parse kubeconfig server failed", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid kubeconfig: cannot extract cluster server"})
		return
	}

	ns := h.cfg.Namespace

	// 创建/更新 Cluster CR（直接复用 kubeX-controller 的 API 类型）
	crLabels := map[string]string{}
	for k, v := range req.Labels {
		crLabels[k] = v
	}
	if req.Provider != "" {
		crLabels["provider"] = req.Provider
	}

	crAnnotations := map[string]string{}
	if req.Description != "" {
		crAnnotations[AnnotationDescription] = req.Description
	}
	crAnnotations[AnnotationName] = req.Name

	cluster := &corev1alpha1.Cluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "core.kubex.io/v1alpha1",
			Kind:       "Cluster",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        clusterID,
			Namespace:   ns,
			Labels:      crLabels,
			Annotations: crAnnotations,
		},
		Spec: corev1alpha1.ClusterSpec{
			APIEndpoint: apiEndpoint,
			SecretRef:   clusterID,
		},
	}

	if err := h.kube.ApplyTypedClusterCR(c, ns, cluster); err != nil {
		h.logger.Error("apply cluster cr failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "apply cluster failed"})
		return
	}

	// 取回 Cluster CR（拿 UID 绑定 OwnerReference）
	applied, err := h.kube.GetTypedClusterCR(c, ns, clusterID)
	if err != nil {
		h.logger.Error("get applied cluster cr failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get cluster failed"})
		return
	}

	// 在集群创建/更新 Secret（kubeconfig 基于 data 字段保存），并设置 ownerReferences 级联删除
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterID,
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         applied.APIVersion,
					Kind:               applied.Kind,
					Name:               applied.Name,
					UID:                applied.UID,
					Controller:         ptrBool(true),
					BlockOwnerDeletion: ptrBool(true),
				},
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"kubeconfig": []byte(req.Kubeconfig),
		},
	}
	_, err = h.kube.CreateSecret(c, ns, sec)
	if err != nil {
		h.logger.Error("create secret failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create secret failed"})
		return
	}

	c.JSON(http.StatusOK, ImportClusterResponse{
		ClusterID: clusterID,
		Name:      req.Name,
		Status:    "Importing",
		Message:   "Cluster import started successfully",
	})
}

func ptrBool(v bool) *bool { return &v }

func (h *ClusterHandler) ListClusters(c *gin.Context) {
	var req ListClustersRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

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

	list, err := h.kube.ListTypedClusterCRs(c, h.cfg.Namespace)
	if err != nil {
		h.logger.Error("list cluster crs failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list clusters failed"})
		return
	}

	selector := parseLabelSelector(req.LabelSelector)
	items := make([]ClusterSummary, 0, len(list.Items))
	for _, it := range list.Items {
		clusterId := it.Name
		name := it.Annotations[AnnotationName]
		if req.Name != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(req.Name)) {
			continue
		}

		provider := it.Labels["provider"]
		if req.Provider != "" && provider != req.Provider {
			continue
		}

		status := string(it.Status.Phase)
		if status == "" {
			status = string(corev1alpha1.ClusterPhaseImporting)
		}
		if req.Status != "" && status != req.Status {
			continue
		}

		desc := it.Annotations[AnnotationDescription]

		labels := it.Labels
		if len(selector) > 0 && !matchLabels(labels, selector) {
			continue
		}

		items = append(items, ClusterSummary{
			ClusterID:   clusterId,
			Name:        name,
			Description: desc,
			Provider:    provider,
			Status:      status,
			APIEndpoint: it.Spec.APIEndpoint,
			Labels:      labels,
			CreatedAt:   it.CreationTimestamp.Time.UTC().Format(time.RFC3339),
			UpdatedAt: func() string {
				if it.Status.UpdatedAt.IsZero() {
					return ""
				}
				return it.Status.UpdatedAt.Time.UTC().Format(time.RFC3339)
			}(),
			Message: "",
		})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	total := len(items)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	c.JSON(http.StatusOK, ListClustersResponse{
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Items:    items[start:end],
	})
}

func parseLabelSelector(s string) map[string]string {
	res := map[string]string{}
	s = strings.TrimSpace(s)
	if s == "" {
		return res
	}
	parts := strings.Split(s, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		if k == "" {
			continue
		}
		res[k] = v
	}
	return res
}

func matchLabels(labels map[string]string, selector map[string]string) bool {
	if len(selector) == 0 {
		return true
	}
	if labels == nil {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
