package optimizer

import "fmt"

// ExpectedPlacement is the solver output that maps each replica to its chosen node.
//
// M4 5.1 scope:
// - We only need a placement map to generate plan moves and to score the expected layout.
// - In later milestones, this will come from ILP solver result decoding.
type ExpectedPlacement map[ReplicaKey]ClusterNodeID

// PlanInputs is the minimal input needed for M4 5.1.
//
// Contract:
// - placementScore[r][n] is the normalized score in [0,100] of putting replica r on node n.
// - currentPlacement/expectedPlacement may be partial; missing entries are treated as unknown.
// - For score sums, replicas with unknown placement are skipped.
type PlanInputs struct {
	Replicas          []ReplicaKey
	PlacementScores   map[ReplicaKey]map[ClusterNodeID]float64
	CurrentPlacement  map[ReplicaKey]ClusterNodeID
	ExpectedPlacement map[ReplicaKey]ClusterNodeID

	// MigrationPenalty is per replica penalty added when the replica moves.
	// If nil, migration penalty is treated as 0.
	MigrationPenalty map[ReplicaKey]float64
}

// PlanMetrics is the computed output for plan summary.
type PlanMetrics struct {
	CurrentScore            float64
	ExpectedScore           float64
	EstimatedImprovement    float64
	EstimatedImprovementPct float64
	PodsToMove              int32
}

// BuildPlanMetricsAndMoves computes:
// - currentScore: sum score(r, currentNode)
// - expectedScore: sum score(r, expectedNode)
// - estimatedImprovementScore: (currentScore - expectedScore). If negative, clamped to 0.
// - moves: replicas whose expected placement differs from current.
//
// Notes:
// - This is a deterministic, solver-agnostic implementation for M4 5.1.
// - Conservative/Aggressive execution policy is handled in later milestones.
func BuildPlanMetricsAndMoves(in PlanInputs) (PlanMetrics, []PlanMoveLite, error) {
	if len(in.Replicas) == 0 {
		return PlanMetrics{}, nil, nil
	}
	if in.PlacementScores == nil {
		return PlanMetrics{}, nil, fmt.Errorf("PlacementScores is nil")
	}
	if in.CurrentPlacement == nil {
		in.CurrentPlacement = map[ReplicaKey]ClusterNodeID{}
	}
	if in.ExpectedPlacement == nil {
		in.ExpectedPlacement = map[ReplicaKey]ClusterNodeID{}
	}

	var curSum float64
	var expSum float64
	moves := make([]PlanMoveLite, 0)

	for _, r := range in.Replicas {
		curNode, curOk := in.CurrentPlacement[r]
		expNode, expOk := in.ExpectedPlacement[r]

		// Scores: skip replicas without known placement.
		if curOk {
			if s, ok := getPlacementScore(in.PlacementScores, r, curNode); ok {
				curSum += s
			}
		}
		if expOk {
			if s, ok := getPlacementScore(in.PlacementScores, r, expNode); ok {
				expSum += s
			}
		}

		if curOk && expOk && (curNode != expNode) {
			moves = append(moves, PlanMoveLite{Replica: r, Source: curNode, Destination: expNode})
		}
	}

	// Migration penalty (if provided): apply only to expectedScore when moved.
	if in.MigrationPenalty != nil {
		for _, mv := range moves {
			if pen, ok := in.MigrationPenalty[mv.Replica]; ok {
				expSum += pen
			}
		}
	}

	impr := curSum - expSum
	if impr < 0 {
		impr = 0
	}
	var imprPct float64
	if curSum > 0 {
		imprPct = impr * 100 / curSum
	}

	return PlanMetrics{
		CurrentScore:            curSum,
		ExpectedScore:           expSum,
		EstimatedImprovement:    impr,
		EstimatedImprovementPct: imprPct,
		PodsToMove:              int32(len(moves)),
	}, moves, nil
}

func getPlacementScore(scores map[ReplicaKey]map[ClusterNodeID]float64, r ReplicaKey, n ClusterNodeID) (float64, bool) {
	m, ok := scores[r]
	if !ok {
		return 0, false
	}
	v, ok := m[n]
	return v, ok
}

// PlanMoveLite is an internal representation of a move which can later be mapped
// into api/v1alpha1.PlanMove.
type PlanMoveLite struct {
	Replica     ReplicaKey
	Source      ClusterNodeID
	Destination ClusterNodeID
}
