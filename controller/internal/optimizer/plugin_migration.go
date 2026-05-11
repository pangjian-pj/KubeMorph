package optimizer

import "fmt"

// MigrationPlugin assigns a penalty to moving a replica.
//
// M3 4.2: only produces coefficients for m_i (per replica).
// The actual m_i variable and constraints are introduced in a later milestone.
type MigrationPlugin struct {
	// Penalty is the objective coefficient for each migrated replica.
	// If 0, defaults to 100.
	Penalty float64
}

func (p *MigrationPlugin) Name() string { return "Migration" }

func (p *MigrationPlugin) ScoreMigration(ctx PluginContext) (map[ReplicaKey]float64, error) {
	if len(ctx.Replicas) == 0 {
		return map[ReplicaKey]float64{}, nil
	}
	if p == nil {
		return nil, fmt.Errorf("MigrationPlugin is nil")
	}
	pen := p.Penalty
	if pen == 0 {
		pen = 100
	}
	if pen < 0 {
		return nil, fmt.Errorf("invalid Penalty: %v", pen)
	}
	out := make(map[ReplicaKey]float64, len(ctx.Replicas))
	for _, r := range ctx.Replicas {
		out[r] = pen
	}
	return out, nil
}
