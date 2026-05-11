package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

type CreateGlobalDeploymentRequest struct {
	YAML string `json:"yaml" binding:"required"`
}

type CreateGlobalDeploymentResponse struct {
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Replicas  int32  `json:"replicas,omitempty"`
	Message   string `json:"message,omitempty"`
}

type GlobalDeploymentHandler struct {
	logger *zap.Logger
	cfg    GlobalDeploymentHandlerConfig
	kube   GlobalDeploymentHandlerKube
}

type GlobalDeploymentHandlerConfig struct {
	Namespace string
}

type GlobalDeploymentHandlerKube interface {
	ApplyTypedGlobalDeploymentCR(ctx *gin.Context, ns string, gd *corev1alpha1.GlobalDeployment) error
}

func NewGlobalDeploymentHandler(logger *zap.Logger, cfg GlobalDeploymentHandlerConfig, kube GlobalDeploymentHandlerKube) *GlobalDeploymentHandler {
	return &GlobalDeploymentHandler{logger: logger, cfg: cfg, kube: kube}
}

func (h *GlobalDeploymentHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/CreateGlobalDeployment", h.CreateGlobalDeployment)
}

func (h *GlobalDeploymentHandler) CreateGlobalDeployment(c *gin.Context) {
	var req CreateGlobalDeploymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	name, ns, replicas, depSpec, err := parseDeploymentYAML(req.YAML, h.cfg.Namespace)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	depSpecJSON, err := json.Marshal(depSpec)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid templateYAML: cannot encode as json"})
		return
	}

	gd := &corev1alpha1.GlobalDeployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.GroupVersion.String(),
			Kind:       "GlobalDeployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: corev1alpha1.GlobalDeploymentSpec{
			Replicas: replicas,
			Template: runtime.RawExtension{Raw: depSpecJSON},
		},
	}

	if err := h.kube.ApplyTypedGlobalDeploymentCR(c, ns, gd); err != nil {
		if apierrors.IsAlreadyExists(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "globalDeployment already exists"})
			return
		}
		h.logger.Error("apply globalDeployment failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create globalDeployment failed"})
		return
	}

	c.JSON(http.StatusOK, CreateGlobalDeploymentResponse{
		Name:      name,
		Namespace: ns,
		Replicas:  derefI32(replicas, 1),
		Message:   "GlobalDeployment created successfully",
	})
}

func derefI32(p *int32, def int32) int32 {
	if p == nil {
		return def
	}
	return *p
}

// parseDeploymentYAML accepts a full Kubernetes Deployment YAML.
// It extracts name/namespace/replicas and returns DeploymentSpec used for scheduling and member deployment creation.
func parseDeploymentYAML(y string, defaultNamespace string) (string, string, *int32, appsv1.DeploymentSpec, error) {
	s := strings.TrimSpace(y)
	if s == "" {
		return "", "", nil, appsv1.DeploymentSpec{}, &apiError{msg: "yaml is required"}
	}

	var dep appsv1.Deployment
	if err := yaml.Unmarshal([]byte(s), &dep); err != nil {
		return "", "", nil, appsv1.DeploymentSpec{}, &apiError{msg: "invalid yaml: must be a Kubernetes Deployment YAML"}
	}
	if !strings.EqualFold(dep.Kind, "Deployment") {
		return "", "", nil, appsv1.DeploymentSpec{}, &apiError{msg: "invalid yaml: kind must be Deployment"}
	}
	name := strings.TrimSpace(dep.ObjectMeta.Name)
	if name == "" {
		return "", "", nil, appsv1.DeploymentSpec{}, &apiError{msg: "invalid yaml: metadata.name is required"}
	}
	ns := strings.TrimSpace(dep.ObjectMeta.Namespace)
	if ns == "" {
		ns = defaultNamespace
	}
	if ns == "" {
		ns = "default"
	}

	depSpec := dep.Spec
	if err := validateDeploymentSpec(depSpec); err != nil {
		return "", "", nil, appsv1.DeploymentSpec{}, err
	}
	return name, ns, dep.Spec.Replicas, depSpec, nil
}

func validateDeploymentSpec(depSpec appsv1.DeploymentSpec) error {
	// We rely on this for resource calculation and for actually creating member deployments.
	if len(depSpec.Template.Spec.Containers) == 0 {
		return &apiError{msg: "invalid templateYAML: spec.template.spec.containers must not be empty"}
	}
	return nil
}

type apiError struct{ msg string }

func (e *apiError) Error() string { return e.msg }
