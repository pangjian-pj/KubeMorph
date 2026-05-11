package optimizer

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBuildILPModel_CountsAndResourceConstraints(t *testing.T) {
	ctx := context.Background()
	stable := []ReplicaKey{
		{Namespace: "ns", Name: "gd", ReplicaIndex: 0},
		{Namespace: "ns", Name: "gd", ReplicaIndex: 1},
		{Namespace: "ns", Name: "gd", ReplicaIndex: 2},
	}
	nodes := []ClusterNodeID{{ClusterID: "c1", NodeName: "n1"}, {ClusterID: "c1", NodeName: "n2"}}

	req := map[ReplicaKey]ResourceQuantity{}
	for _, r := range stable {
		req[r] = ResourceQuantity{MilliCPU: 500, MemoryMi: 256}
	}
	cap := map[ClusterNodeID]ResourceQuantity{}
	for _, n := range nodes {
		cap[n] = ResourceQuantity{MilliCPU: 2000, MemoryMi: 1024}
	}

	res, err := BuildILPModel(ctx, Problem{
		StableReplicas:   stable,
		CandidateNodes:   nodes,
		ReplicaRequests:  req,
		NodeCapacities:   cap,
		RequireCPU:       true,
		RequireMemory:    true,
		MaximumVariables: 10_000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// vars = |I|*|J| = 3*2
	if res.NumDecisionVars != 6 {
		t.Fatalf("expected 6 vars, got %d", res.NumDecisionVars)
	}
	// constraints = assignment(|I|) + cpu(|J|) + memory(|J|) = 3 + 2 + 2
	if res.NumConstraints != 7 {
		t.Fatalf("expected 7 constraints, got %d", res.NumConstraints)
	}
}

func TestBuildILPModel_VariableLimit(t *testing.T) {
	ctx := context.Background()
	stable := make([]ReplicaKey, 101) // 101 replicas
	for i := range stable {
		stable[i] = ReplicaKey{Namespace: "ns", Name: "gd", ReplicaIndex: int32(i)}
	}
	nodes := make([]ClusterNodeID, 100) // 100 nodes => 10100 vars
	for i := range nodes {
		nodes[i] = ClusterNodeID{ClusterID: "c", NodeName: "n"}
	}

	_, err := BuildILPModel(ctx, Problem{StableReplicas: stable, CandidateNodes: nodes, MaximumVariables: 10_000})
	if err == nil {
		t.Fatalf("expected ErrTooManyVariables")
	}
	if _, ok := err.(*ErrTooManyVariables); !ok {
		t.Fatalf("expected ErrTooManyVariables, got %T: %v", err, err)
	}
}

type fakeSolver struct {
	called bool

	// blockUntilDone makes Solve wait for ctx.Done() and then return ctx.Err().
	blockUntilDone bool
}

func (s *fakeSolver) Solve(ctx context.Context, _ Problem) (*SolveResult, error) {
	s.called = true
	if s.blockUntilDone {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &SolveResult{Status: "OK", Assignment: map[ReplicaKey]ClusterNodeID{}}, nil
}

func TestSolveProblem_NoSolverConfigured(t *testing.T) {
	ctx := context.Background()
	_, err := SolveProblem(ctx, nil, Problem{StableReplicas: []ReplicaKey{{Namespace: "ns", Name: "gd", ReplicaIndex: 0}}, CandidateNodes: []ClusterNodeID{{ClusterID: "c", NodeName: "n"}}}, SolveOptions{})
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := err.Error(); got != "no solver configured" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSolveProblem_DefaultTimeout(t *testing.T) {
	ctx := context.Background()
	s := &fakeSolver{blockUntilDone: true}

	start := time.Now()
	_, err := SolveProblem(ctx, s, Problem{StableReplicas: []ReplicaKey{{Namespace: "ns", Name: "gd", ReplicaIndex: 0}}, CandidateNodes: []ClusterNodeID{{ClusterID: "c", NodeName: "n"}}}, SolveOptions{Timeout: 10 * time.Millisecond})
	if !s.called {
		t.Fatalf("expected solver to be called")
	}
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %T: %v", err, err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("timeout took too long")
	}
}
