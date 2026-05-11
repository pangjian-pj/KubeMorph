package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	corev1alpha1 "github.com/pangjian-pj/kubeX/kubeX-controller/api/v1alpha1"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type fakeUpdateKube struct {
	getFn   func(ns, id string) (*corev1alpha1.Cluster, error)
	applyFn func(ns string, c *corev1alpha1.Cluster) error
}

func (f fakeUpdateKube) CreateSecret(ctx *gin.Context, ns string, s *corev1.Secret) (*corev1.Secret, error) {
	return nil, nil
}
func (f fakeUpdateKube) DeleteTypedClusterCR(ctx *gin.Context, ns string, clusterID string) error {
	return nil
}
func (f fakeUpdateKube) ListTypedClusterCRs(ctx *gin.Context, ns string) (*corev1alpha1.ClusterList, error) {
	return &corev1alpha1.ClusterList{}, nil
}
func (f fakeUpdateKube) GetSecret(ctx *gin.Context, ns string, name string) (*corev1.Secret, error) {
	return nil, nil
}
func (f fakeUpdateKube) GetNodesFromKubeconfig(ctx context.Context, kubeconfigYAML string) (*corev1.NodeList, error) {
	return nil, nil
}

func (f fakeUpdateKube) GetTypedClusterCR(ctx *gin.Context, ns string, clusterID string) (*corev1alpha1.Cluster, error) {
	if f.getFn != nil {
		return f.getFn(ns, clusterID)
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: "core.kubex.io", Resource: "clusters"}, clusterID)
}

func (f fakeUpdateKube) ApplyTypedClusterCR(ctx *gin.Context, ns string, cluster *corev1alpha1.Cluster) error {
	if f.applyFn != nil {
		return f.applyFn(ns, cluster)
	}
	return nil
}

func TestUpdateCluster_UpdatesFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// baseline cluster
	base := &corev1alpha1.Cluster{
		TypeMeta: metav1.TypeMeta{APIVersion: "core.kubex.io/v1alpha1", Kind: "Cluster"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cid",
			Namespace: "default",
			Labels: map[string]string{
				"provider": "Onprem",
				"keep":     "1",
			},
			Annotations: map[string]string{
				AnnotationName:        "old",
				AnnotationDescription: "olddesc",
			},
		},
	}

	var applied *corev1alpha1.Cluster

	g := gin.New()
	h := NewClusterHandler(zap.NewNop(), nil, ClusterHandlerConfig{Namespace: "default"}, fakeUpdateKube{
		getFn: func(ns, id string) (*corev1alpha1.Cluster, error) {
			return base.DeepCopy(), nil
		},
		applyFn: func(ns string, c *corev1alpha1.Cluster) error {
			applied = c.DeepCopy()
			return nil
		},
	})
	v1 := g.Group("/api/v1")
	h.Register(v1)

	body, _ := json.Marshal(UpdateClusterRequest{
		ClusterID:   "cid",
		Name:        "newname",
		Description: "", // clear
		Provider:    "AWS",
		Labels: map[string]string{
			"env": "prod",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/UpdateCluster", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	if applied == nil {
		t.Fatalf("expected apply called")
	}
	if applied.Annotations[AnnotationName] != "newname" {
		t.Fatalf("name not updated: %+v", applied.Annotations)
	}
	if _, ok := applied.Annotations[AnnotationDescription]; ok {
		t.Fatalf("description should be cleared")
	}
	if applied.Labels["provider"] != "AWS" {
		t.Fatalf("provider label not updated: %+v", applied.Labels)
	}
	if applied.Labels["env"] != "prod" {
		t.Fatalf("label not merged: %+v", applied.Labels)
	}
	if applied.Labels["keep"] != "1" {
		t.Fatalf("existing label should be kept: %+v", applied.Labels)
	}
}

func TestUpdateCluster_NotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := gin.New()
	h := NewClusterHandler(zap.NewNop(), nil, ClusterHandlerConfig{Namespace: "default"}, fakeUpdateKube{})
	v1 := g.Group("/api/v1")
	h.Register(v1)

	body, _ := json.Marshal(UpdateClusterRequest{ClusterID: "missing", Name: "x"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/UpdateCluster", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
}
