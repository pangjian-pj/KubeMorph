package optimizer

import (
	"context"
	"fmt"
)

// GoalHumanReadableMetric represents one goal's metric in original units.
//
// Contract:
// - Lower is better.
// - From/To may be nil if cannot be computed with current inputs.
// - Delta is (From - To). Positive means improvement.
type GoalHumanReadableMetric struct {
	Kind   string
	Unit   string
	From   *float64
	To     *float64
	Delta  *float64
	Detail map[string]string
}

// ComputeHumanReadableMetric computes goal-specific readable metric from ObjectiveInputs.
// It intentionally does not depend on normalized scores.
func ComputeHumanReadableMetric(ctx context.Context, in ObjectiveInputs, goal WeightedGoal, expected ExpectedPlacement) (GoalHumanReadableMetric, error) {
	switch goal.Type {
	case "Cost":
		vFrom, vTo, err := computeCostTotal(in, expected)
		if err != nil {
			return GoalHumanReadableMetric{}, err
		}
		return buildMetric("Cost", "USD", vFrom, vTo, nil), nil
	case "Latency":
		vFrom, vTo, err := computeLatencyAvgMs(in, goal, expected)
		if err != nil {
			return GoalHumanReadableMetric{}, err
		}
		detail := map[string]string{}
		if goal.SourceCity != "" {
			detail["sourceCity"] = goal.SourceCity
		}
		return buildMetric("Latency", "ms", vFrom, vTo, detail), nil
	case "Communication":
		vFrom, vTo, err := computeCommunicationAvgMs(in, expected)
		if err != nil {
			return GoalHumanReadableMetric{}, err
		}
		return buildMetric("Communication", "ms", vFrom, vTo, nil), nil
	case "Energy":
		vFrom, vTo, err := computeEnergyTotalW(in, expected)
		if err != nil {
			return GoalHumanReadableMetric{}, err
		}
		return buildMetric("Energy", "W", vFrom, vTo, nil), nil
	case "Migration":
		vFrom, vTo, err := computeMigrationMoves(in, expected)
		if err != nil {
			return GoalHumanReadableMetric{}, err
		}
		return buildMetric("Migration", "moves", vFrom, vTo, nil), nil
	default:
		return GoalHumanReadableMetric{}, fmt.Errorf("unknown goal type %q", goal.Type)
	}
}

func buildMetric(kind, unit string, from, to float64, detail map[string]string) GoalHumanReadableMetric {
	f := from
	t := to
	d := from - to
	return GoalHumanReadableMetric{Kind: kind, Unit: unit, From: &f, To: &t, Delta: &d, Detail: detail}
}
