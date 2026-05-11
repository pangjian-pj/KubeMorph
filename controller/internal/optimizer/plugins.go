package optimizer

import (
	"fmt"
	"math"
)

// ScoringPlugin defines a scoring plugin that can contribute objective terms.
//
// M3 4.2 scope (no concrete solver yet):
//   - Plugins provide per-(replica,node) coefficients (linear terms) and/or
//     per-replica migration penalty coefficients.
//   - The actual model wiring into a solver will be done in later milestones.
//
// All returned scores are expected to be in [0,100] unless noted otherwise.
// Lower score means better (objective is minimize).
type ScoringPlugin interface {
	Name() string
}

// LinearScorePlugin scores each (replica,node) placement with a linear coefficient.
//
// The returned map key is replica->node; missing entries mean “no term / 0”.
//
// Note:
// - In a real solver model, these coefficients will be multiplied by x_ij.
type LinearScorePlugin interface {
	ScoringPlugin
	ScorePlacement(ctx PluginContext) (map[ReplicaKey]map[ClusterNodeID]float64, error)
}

// MigrationScorePlugin scores migration cost m_i (binary per replica) with a coefficient.
//
// Note:
//   - In later milestones, optimizer will introduce m_i variables and constraints,
//     then this coefficient becomes the objective weight for m_i.
type MigrationScorePlugin interface {
	ScoringPlugin
	ScoreMigration(ctx PluginContext) (map[ReplicaKey]float64, error)
}

// PluginContext is a dependency-light snapshot for scoring.
// Controller-side adapters will convert from CR status/ConfigMaps into this struct.
type PluginContext struct {
	Replicas []ReplicaKey
	Nodes    []NodeContext

	// ReplicaRequests is resource requests per replica.
	ReplicaRequests map[ReplicaKey]ResourceQuantity

	// CurrentPlacement reports current (cluster,node) per replica when known.
	CurrentPlacement map[ReplicaKey]ClusterNodeID
}

// NodeContext contains attributes a plugin may need.
type NodeContext struct {
	ID ClusterNodeID

	// Labels is a subset of node labels.
	Labels map[string]string

	// Region is an optional normalized concept for Latency/Communication.
	Region string

	// CPUFreeMilli is optional and may be used by Energy (baseline util) in later milestones.
	CPUFreeMilli int64
	// CPUAllocatableMilli is optional and may be used by Energy.
	CPUAllocatableMilli int64
	// MemoryAllocatableMi is optional and used for Memory capacity constraints.
	MemoryAllocatableMi int64
}

// normalizeTo0_100 linearly maps v from [min,max] => [0,100].
//
// Behavior:
// - if max==min: returns 0 for all values (no discrimination)
// - clamps out-of-range values.
func normalizeTo0_100(v, min, max float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 100
	}
	if max <= min {
		return 0
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return (v - min) * 100 / (max - min)
}

func requireNonEmpty(name string, v string) error {
	if v == "" {
		return fmt.Errorf("%s must be non-empty", name)
	}
	return nil
}
