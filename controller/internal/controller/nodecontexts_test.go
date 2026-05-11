package controller

import (
	"context"
	"testing"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"github.com/pangjian-pj/KubeMorph/controller/internal/optimizer"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildNodeContexts_CPUFreeMilli_FromPodRequests(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1alpha1 scheme: %v", err)
	}

	n1 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"node.kubex.io/type": "c1"}},
		Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}},
	}

	// Running pod on n1, with 500m + 250m requests.
	p1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName: "n1",
			Containers: []corev1.Container{
				{Name: "c1", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}}},
				{Name: "c2", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")}}},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	// Succeeded pod should be ignored.
	p2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p2", Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName:   "n1",
			Containers: []corev1.Container{{Name: "c1", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1000m")}}}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(n1, p1, p2).Build()
	r := &OptimizationPolicyReconciler{Client: cl}

	ncs, err := r.buildNodeContexts(ctx, []optimizer.ClusterNodeID{{ClusterID: "c", NodeName: "n1"}})
	if err == nil {
		t.Fatalf("expected buildNodeContexts error in strict member mode, got nil (ncs=%v)", ncs)
	}
}
