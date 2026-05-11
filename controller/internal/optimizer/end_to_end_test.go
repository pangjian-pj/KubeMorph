package optimizer

import (
	"context"
	"testing"
)

func TestComputePlanFromGoals_CostPlusMigration(t *testing.T) {
	r0 := ReplicaKey{Namespace: "default", Name: "a", ReplicaIndex: 0}
	n1 := ClusterNodeID{ClusterID: "c1", NodeName: "n1"}
	n2 := ClusterNodeID{ClusterID: "c1", NodeName: "n2"}

	ctx := PluginContext{
		Replicas: []ReplicaKey{r0},
		Nodes: []NodeContext{
			{ID: n1, Labels: map[string]string{LabelInstanceType: "t1"}},
			{ID: n2, Labels: map[string]string{LabelInstanceType: "t2"}},
		},
		CurrentPlacement: map[ReplicaKey]ClusterNodeID{r0: n2}, // currently on expensive node
	}

	goals := []WeightedGoal{
		{Type: "Cost", Weight: 1},
		{Type: "Migration", Weight: 1},
	}

	metrics, moves, err := ComputePlanFromGoals(
		context.Background(),
		ObjectiveInputs{
			Goals:         goals,
			Ctx:           ctx,
			InstancePrice: map[string]float64{"t1": 1, "t2": 2},
		},
		ExpectedPlacement{r0: n1},
	)
	if err != nil {
		t.Fatalf("ComputePlanFromGoals error: %v", err)
	}
	if metrics.PodsToMove != 1 {
		t.Fatalf("expected PodsToMove=1, got %d", metrics.PodsToMove)
	}
	if len(moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(moves))
	}
	if moves[0].Source != n2 || moves[0].Destination != n1 {
		t.Fatalf("unexpected move: %+v", moves[0])
	}

	// Cost scores are normalized: n1=0, n2=100.
	// currentScore = 100
	// expectedScore = 0 + migrationPenalty(=1)
	if metrics.CurrentScore != 100 {
		t.Fatalf("expected CurrentScore=100, got %v", metrics.CurrentScore)
	}
	if metrics.ExpectedScore != 1 {
		t.Fatalf("expected ExpectedScore=1, got %v", metrics.ExpectedScore)
	}
	if metrics.EstimatedImprovement != 99 {
		t.Fatalf("expected EstimatedImprovement=99, got %v", metrics.EstimatedImprovement)
	}
}
