package optimizer

import "testing"

func TestBuildPlanMetricsAndMoves_Basic(t *testing.T) {
	r0 := ReplicaKey{Namespace: "default", Name: "a", ReplicaIndex: 0}
	r1 := ReplicaKey{Namespace: "default", Name: "a", ReplicaIndex: 1}

	n1 := ClusterNodeID{ClusterID: "c1", NodeName: "n1"}
	n2 := ClusterNodeID{ClusterID: "c1", NodeName: "n2"}

	in := PlanInputs{
		Replicas: []ReplicaKey{r0, r1},
		PlacementScores: map[ReplicaKey]map[ClusterNodeID]float64{
			r0: {n1: 10, n2: 30},
			r1: {n1: 5, n2: 50},
		},
		CurrentPlacement:  map[ReplicaKey]ClusterNodeID{r0: n1, r1: n2},
		ExpectedPlacement: map[ReplicaKey]ClusterNodeID{r0: n2, r1: n2},
		MigrationPenalty:  map[ReplicaKey]float64{r0: 7},
	}

	m, moves, err := BuildPlanMetricsAndMoves(in)
	if err != nil {
		t.Fatalf("BuildPlanMetricsAndMoves error: %v", err)
	}
	// current = r0@10 + r1@50
	if m.CurrentScore != 60 {
		t.Fatalf("expected currentScore=60, got %v", m.CurrentScore)
	}
	// expected = r0@30 + r1@50 + migration(r0)
	if m.ExpectedScore != 87 {
		t.Fatalf("expected expectedScore=87, got %v", m.ExpectedScore)
	}
	if m.PodsToMove != 1 {
		t.Fatalf("expected podsToMove=1, got %d", m.PodsToMove)
	}
	if len(moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(moves))
	}
	if moves[0].Replica != r0 || moves[0].Source != n1 || moves[0].Destination != n2 {
		t.Fatalf("unexpected move: %+v", moves[0])
	}
	// improvement is clamped to 0 because expected worse.
	if m.EstimatedImprovement != 0 {
		t.Fatalf("expected improvement=0, got %v", m.EstimatedImprovement)
	}
}
