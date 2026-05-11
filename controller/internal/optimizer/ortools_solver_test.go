//go:build kubex_ortools

package optimizer

import (
	"context"
	"testing"
	"time"
)

func TestORToolsSolver_AssignmentFeasible(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	s := NewORToolsSolver()

	replicas := []ReplicaKey{{Namespace: "ns", Name: "gd", ReplicaIndex: 0}, {Namespace: "ns", Name: "gd", ReplicaIndex: 1}, {Namespace: "ns", Name: "gd", ReplicaIndex: 2}}
	nodes := []ClusterNodeID{{ClusterID: "c1", NodeName: "n1"}, {ClusterID: "c1", NodeName: "n2"}}

	res, err := s.Solve(ctx, Problem{StableReplicas: replicas, CandidateNodes: nodes, RequireCPU: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatalf("expected non-nil result")
	}
	if len(res.Assignment) != len(replicas) {
		t.Fatalf("expected assignment for %d replicas, got %d", len(replicas), len(res.Assignment))
	}
	for _, r := range replicas {
		if _, ok := res.Assignment[r]; !ok {
			t.Fatalf("missing assignment for %s", r.String())
		}
	}
}

func TestORToolsSolver_CPUCapacityRespected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	s := NewORToolsSolver()

	replicas := []ReplicaKey{{Namespace: "ns", Name: "gd", ReplicaIndex: 0}, {Namespace: "ns", Name: "gd", ReplicaIndex: 1}, {Namespace: "ns", Name: "gd", ReplicaIndex: 2}}
	n1 := ClusterNodeID{ClusterID: "c1", NodeName: "n1"}
	n2 := ClusterNodeID{ClusterID: "c1", NodeName: "n2"}
	nodes := []ClusterNodeID{n1, n2}

	req := map[ReplicaKey]ResourceQuantity{}
	for _, r := range replicas {
		req[r] = ResourceQuantity{MilliCPU: 500}
	}

	cap := map[ClusterNodeID]ResourceQuantity{
		n1: {MilliCPU: 1000}, // can host at most 2 replicas
		n2: {MilliCPU: 1000},
	}

	res, err := s.Solve(ctx, Problem{
		StableReplicas:  replicas,
		CandidateNodes:  nodes,
		RequireCPU:      true,
		ReplicaRequests: req,
		NodeCapacities:  cap,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	used := map[ClusterNodeID]int64{n1: 0, n2: 0}
	for _, r := range replicas {
		n := res.Assignment[r]
		used[n] += req[r].MilliCPU
	}
	if used[n1] > cap[n1].MilliCPU {
		t.Fatalf("node %s cpu exceeded: used=%d cap=%d", n1.String(), used[n1], cap[n1].MilliCPU)
	}
	if used[n2] > cap[n2].MilliCPU {
		t.Fatalf("node %s cpu exceeded: used=%d cap=%d", n2.String(), used[n2], cap[n2].MilliCPU)
	}
}

func TestORToolsSolver_MemoryCapacityRespected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	s := NewORToolsSolver()

	replicas := []ReplicaKey{{Namespace: "ns", Name: "gd", ReplicaIndex: 0}, {Namespace: "ns", Name: "gd", ReplicaIndex: 1}, {Namespace: "ns", Name: "gd", ReplicaIndex: 2}}
	n1 := ClusterNodeID{ClusterID: "c1", NodeName: "n1"}
	n2 := ClusterNodeID{ClusterID: "c1", NodeName: "n2"}
	nodes := []ClusterNodeID{n1, n2}

	req := map[ReplicaKey]ResourceQuantity{}
	for _, r := range replicas {
		req[r] = ResourceQuantity{MilliCPU: 10, MemoryMi: 700}
	}

	cap := map[ClusterNodeID]ResourceQuantity{
		n1: {MilliCPU: 10_000, MemoryMi: 1000}, // can host at most 1 replica by memory
		n2: {MilliCPU: 10_000, MemoryMi: 1000},
	}

	res, err := s.Solve(ctx, Problem{
		StableReplicas:  replicas,
		CandidateNodes:  nodes,
		RequireCPU:      false,
		RequireMemory:   true,
		ReplicaRequests: req,
		NodeCapacities:  cap,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	used := map[ClusterNodeID]int64{n1: 0, n2: 0}
	for _, r := range replicas {
		n := res.Assignment[r]
		used[n] += req[r].MemoryMi
	}
	if used[n1] > cap[n1].MemoryMi {
		t.Fatalf("node %s memory exceeded: used=%d cap=%d", n1.String(), used[n1], cap[n1].MemoryMi)
	}
	if used[n2] > cap[n2].MemoryMi {
		t.Fatalf("node %s memory exceeded: used=%d cap=%d", n2.String(), used[n2], cap[n2].MemoryMi)
	}
}
