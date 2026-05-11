package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

type fakeKV struct{}

func (f fakeKV) Put(ctx *gin.Context, key, value string) error          { return nil }
func (f fakeKV) Get(ctx *gin.Context, key string) (string, bool, error) { return "v", true, nil }
func (f fakeKV) Delete(ctx *gin.Context, key string) (bool, error)      { return true, nil }

type fakeEtcd struct{ clientv3.Client }

func TestHealthz(t *testing.T) {
	r := NewRouter(Dependencies{
		Logger:           zap.NewNop(),
		KV:               fakeKV{},
		Etcd:             &clientv3.Client{},
		GlobalDeployment: GlobalDeploymentDeps{Namespace: "default", Kube: nil},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
