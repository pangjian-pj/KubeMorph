package optimizer

import (
	"context"
)

// ComputePlanFromGoals wires everything together:
// goals -> placement scores -> plan metrics/moves.
//
// This does not run the ILP solver yet; it only computes summary based on
// a given expected placement (solver assignment).
func ComputePlanFromGoals(ctx context.Context, in ObjectiveInputs, expected ExpectedPlacement) (PlanMetrics, []PlanMoveLite, error) {
	out, err := BuildObjective(ctx, in)
	if err != nil {
		return PlanMetrics{}, nil, err
	}
	return BuildPlanMetricsAndMoves(PlanInputs{
		Replicas:          in.Ctx.Replicas,
		PlacementScores:   out.PlacementScores,
		CurrentPlacement:  in.Ctx.CurrentPlacement,
		ExpectedPlacement: expected,
		MigrationPenalty:  out.MigrationPenalty,
	})
}

// ComputePlanForSingleGoal computes plan metrics/moves for a single goal type.
//
// It is used to provide per-goal breakdown in plan summary. It shares the same
// scoring convention as ComputePlanFromGoals: lower score is better.
func ComputePlanForSingleGoal(ctx context.Context, in ObjectiveInputs, goal WeightedGoal, expected ExpectedPlacement) (PlanMetrics, []PlanMoveLite, error) {
	in2 := in
	in2.Goals = []WeightedGoal{goal}
	return ComputePlanFromGoals(ctx, in2, expected)
}
