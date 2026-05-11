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
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type fakeDeleteKube struct {
	deleteFn func(ns, clusterID string) error
}

func (f fakeDeleteKube) CreateSecret(ctx *gin.Context, ns string, s *corev1.Secret) (*corev1.Secret, error) {
	return nil, nil
}
func (f fakeDeleteKube) ApplyTypedClusterCR(ctx *gin.Context, ns string, cluster *corev1alpha1.Cluster) error {
	return nil
}
func (f fakeDeleteKube) ListTypedClusterCRs(ctx *gin.Context, ns string) (*corev1alpha1.ClusterList, error) {
	return &corev1alpha1.ClusterList{}, nil
}
func (f fakeDeleteKube) GetTypedClusterCR(ctx *gin.Context, ns string, clusterID string) (*corev1alpha1.Cluster, error) {
	return &corev1alpha1.Cluster{}, nil
}
func (f fakeDeleteKube) DeleteTypedClusterCR(ctx *gin.Context, ns string, clusterID string) error {
	if f.deleteFn == nil {
		return nil
	}
	return f.deleteFn(ns, clusterID)
}
func (f fakeDeleteKube) GetSecret(ctx *gin.Context, ns string, name string) (*corev1.Secret, error) {
	return nil, nil
}
func (f fakeDeleteKube) GetNodesFromKubeconfig(ctx context.Context, kubeconfigYAML string) (*corev1.NodeList, error) {
	return nil, nil
}

func TestDeleteClusters_AllSuccess(t *testing.T) {
	g := gin.New()
	h := NewClusterHandler(zap.NewNop(), nil, ClusterHandlerConfig{Namespace: "default"}, fakeDeleteKube{
		deleteFn: func(ns, id string) error {
			if ns != "default" {
				t.Fatalf("unexpected ns: %s", ns)
			}
			return nil
		},
	})
	v1 := g.Group("/api/v1")
	h.Register(v1)

	body, _ := json.Marshal(DeleteClustersRequest{ClusterIDs: []string{"c1", "c2"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/DeleteClusters", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	var resp DeleteClustersResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Deleted) != 2 || len(resp.Failed) != 0 {
		t.Fatalf("unexpected resp: %+v", resp)
	}
}

func TestDeleteClusters_NotFoundRecorded(t *testing.T) {
	g := gin.New()
	h := NewClusterHandler(zap.NewNop(), nil, ClusterHandlerConfig{Namespace: "default"}, fakeDeleteKube{
		deleteFn: func(ns, id string) error {
			return apierrors.NewNotFound(schema.GroupResource{Group: "core.kubex.io", Resource: "clusters"}, id)
		},
	})
	v1 := g.Group("/api/v1")
	h.Register(v1)

	body, _ := json.Marshal(DeleteClustersRequest{ClusterIDs: []string{"missing"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/DeleteClusters", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	var resp DeleteClustersResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Deleted) != 0 || len(resp.Failed) != 1 {
		t.Fatalf("unexpected resp: %+v", resp)
	}
	if resp.Failed[0].ClusterID != "missing" {
		t.Fatalf("unexpected failed id: %+v", resp.Failed[0])
	}
}

func TestDeleteClusters_ValidateRequest(t *testing.T) {
	g := gin.New()
	h := NewClusterHandler(zap.NewNop(), nil, ClusterHandlerConfig{Namespace: "default"}, fakeDeleteKube{})
	v1 := g.Group("/api/v1")
	h.Register(v1)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/DeleteClusters", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
}
