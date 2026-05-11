//go:build kubex_ortools

package optimizer

import (
	"context"
	"testing"
)

func TestORToolsSolver_ObjectiveInfluencesAssignment_Cost(t *testing.T) {
	ctx := context.Background()

	s := NewORToolsSolver()

	r := ReplicaKey{Namespace: "ns", Name: "gd", ReplicaIndex: 0}
	nCheap := ClusterNodeID{ClusterID: "c1", NodeName: "cheap"}
	nExp := ClusterNodeID{ClusterID: "c1", NodeName: "exp"}

	nodes := []NodeContext{
		{ID: nCheap, Labels: map[string]string{"node.kubex.io/type": "t1"}},
		{ID: nExp, Labels: map[string]string{"node.kubex.io/type": "t2"}},
	}

	res, err := s.Solve(ctx, Problem{
		StableReplicas: []ReplicaKey{r},
		CandidateNodes: []ClusterNodeID{nCheap, nExp},

		NodeContexts: nodes,
		ReplicaRequests: map[ReplicaKey]ResourceQuantity{
			r: {MilliCPU: 1000},
		},
		NodeCapacities: map[ClusterNodeID]ResourceQuantity{
			nCheap: {MilliCPU: 2000},
			nExp:   {MilliCPU: 2000},
		},
		RequireCPU: true,

		Objective: &ProblemObjective{
			Goals: []WeightedGoal{{Type: "Cost", Weight: 1}},
			InstancePrice: map[string]float64{
				"t1": 1, // cheaper
				"t2": 10,
			},
		},
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	got := res.Assignment[r]
	if got != nCheap {
		t.Fatalf("expected cheaper node chosen, got %s", got.String())
	}
}

func TestORToolsSolver_ObjectiveInfluencesAssignment_MigrationPenalty(t *testing.T) {
	ctx := context.Background()

	s := NewORToolsSolver()

	r := ReplicaKey{Namespace: "ns", Name: "gd", ReplicaIndex: 0}
	nCur := ClusterNodeID{ClusterID: "c1", NodeName: "cur"}
	nAlt := ClusterNodeID{ClusterID: "c1", NodeName: "alt"}

	// Make alt cheaper, but migration penalty should keep it on current.
	nodes := []NodeContext{
		{ID: nCur, Labels: map[string]string{"node.kubex.io/type": "tExp"}},
		{ID: nAlt, Labels: map[string]string{"node.kubex.io/type": "tCheap"}},
	}

	res, err := s.Solve(ctx, Problem{
		StableReplicas:   []ReplicaKey{r},
		CandidateNodes:   []ClusterNodeID{nCur, nAlt},
		CurrentPlacement: map[ReplicaKey]ClusterNodeID{r: nCur},

		NodeContexts: nodes,
		ReplicaRequests: map[ReplicaKey]ResourceQuantity{
			r: {MilliCPU: 1000},
		},
		NodeCapacities: map[ClusterNodeID]ResourceQuantity{
			nCur: {MilliCPU: 2000},
			nAlt: {MilliCPU: 2000},
		},
		RequireCPU: true,

		Objective: &ProblemObjective{
			Goals: []WeightedGoal{
				{Type: "Cost", Weight: 1},
				{Type: "Migration", Weight: 5000}, // overpower cost
			},
			InstancePrice: map[string]float64{
				"tCheap": 1,
				"tExp":   10,
			},
		},
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	got := res.Assignment[r]
	if got != nCur {
		t.Fatalf("expected stay on current due to migration penalty, got %s", got.String())
	}
}
