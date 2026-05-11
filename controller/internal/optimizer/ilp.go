package optimizer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// ErrSolverNotImplemented is returned when a solver backend is wired but its
// solving logic hasn't been implemented yet.
//
// This is a temporary sentinel used during incremental OR-Tools/SCIP integration.
var ErrSolverNotImplemented = errors.New("solver not implemented")

// Problem is the minimal M3 4.1 ILP-model input.
//
// Contract:
// - StableReplicas (I): replicas eligible for optimization.
// - CandidateNodes (J): candidate nodes across clusters.
// - CurrentPlacement: optional seed for x_ij_current (used later for migration goal / plan moves).
// - ReplicaRequests: per-replica resource demand (at least CPU must be provided in M3 4.1).
// - NodeCapacities: per-node capacity.
//
// Notes:
// - This is currently controller-runtime agnostic and has no k8s imports.
// - M3 4.1 focuses on model/constraints; objective plugins are in later milestones.
type Problem struct {
	StableReplicas   []ReplicaKey
	CandidateNodes   []ClusterNodeID
	CurrentPlacement map[ReplicaKey]ClusterNodeID

	// NodeContexts are optional but required when objective plugins are enabled.
	// NodeContexts[i].ID must refer to nodes present in CandidateNodes.
	NodeContexts []NodeContext

	ReplicaRequests map[ReplicaKey]ResourceQuantity
	NodeCapacities  map[ClusterNodeID]ResourceQuantity

	// Objective holds data sources to build and weight goal plugins.
	// If nil, solver runs feasibility-first.
	Objective *ProblemObjective

	// MaximumVariables limits |I|*|J| to avoid exploding problem size.
	// If 0, DefaultMaximumVariables is used.
	MaximumVariables int

	// RequireCPU enforces that CPU request and node CPU capacity are provided.
	RequireCPU bool

	// RequireMemory enforces that Memory request and node Memory capacity are provided.
	// Unit: Mi (mebibytes).
	RequireMemory bool
}

// ProblemObjective is a lightweight carrier of controller-provided objective inputs.
// It mirrors ObjectiveInputs but avoids duplicating the large PluginContext.
type ProblemObjective struct {
	Goals []WeightedGoal

	// Cost
	InstancePrice map[string]float64

	// Latency
	CityRegionLatencyMs map[string]map[string]float64

	// Communication
	Dependencies    map[NamespacedName][]NamespacedName
	RegionLatencyMs map[string]map[string]float64
	ReplicaService  map[ReplicaKey]NamespacedName
	NodeRegion      map[ClusterNodeID]string

	// Energy
	InstancePower map[string]PowerCurve
}

const DefaultMaximumVariables = 10_000

// DefaultSolveTimeout is the default time budget for a solver run.
// It matches design_doc/optimization.md and 开发计划.md (M3 4.1): 5 minutes.
const DefaultSolveTimeout = 5 * time.Minute

// Solver is a minimal solver interface used by optimizer.
//
// Note: A concrete implementation (OR-Tools/SCIP) will be wired in later.
// For now this allows controller code to pass a mock/placeholder solver
// while we validate constraints and timeouts.
type Solver interface {
	Solve(ctx context.Context, p Problem) (*SolveResult, error)
}

// SolveResult is a minimal solver output.
//
// Assignment maps each replica i to exactly one chosen node j.
// When no replicas exist, Assignment can be empty.
type SolveResult struct {
	Status     string
	Assignment map[ReplicaKey]ClusterNodeID
}

type SolveOptions struct {
	Timeout time.Duration
}

func (o SolveOptions) withDefaults() SolveOptions {
	if o.Timeout <= 0 {
		o.Timeout = DefaultSolveTimeout
	}
	return o
}

// BuildResult is a lightweight output of model building.
// It intentionally doesn't carry a concrete solver model yet.
type BuildResult struct {
	NumDecisionVars int
	NumConstraints  int
}

// ErrTooManyVariables indicates variable count exceeds the configured limit.
type ErrTooManyVariables struct {
	Variables int
	Limit     int
}

func (e *ErrTooManyVariables) Error() string {
	return fmt.Sprintf("too many decision variables: %d > %d", e.Variables, e.Limit)
}

// BuildILPModel validates the input and builds a minimal constraint model.
//
// M3 4.1 scope:
// - decision vars: x_ij (binary, for each stable replica i and candidate node j)
// - constraints:
//  1. for each i: sum_j x_ij = 1
//  2. for each node j: sum_i cpu_i * x_ij <= cpuCap_j
//     (memory/pods can be added later)
//
// - max variable size protection
//
// This function doesn't invoke an external solver yet; it only checks that the
// model is well-formed and counts vars/constraints so we can test correctness.
func BuildILPModel(_ context.Context, p Problem) (*BuildResult, error) {
	if len(p.StableReplicas) == 0 {
		return &BuildResult{}, nil
	}
	if len(p.CandidateNodes) == 0 {
		return nil, fmt.Errorf("no candidate nodes")
	}
	limit := p.MaximumVariables
	if limit <= 0 {
		limit = DefaultMaximumVariables
	}
	vars := len(p.StableReplicas) * len(p.CandidateNodes)
	if vars > limit {
		return nil, &ErrTooManyVariables{Variables: vars, Limit: limit}
	}

	// Resource validation.
	if p.RequireCPU {
		for _, i := range p.StableReplicas {
			req, ok := p.ReplicaRequests[i]
			if !ok {
				return nil, fmt.Errorf("missing replica request for %s", i.String())
			}
			if req.MilliCPU <= 0 {
				return nil, fmt.Errorf("invalid replica cpu request for %s: %d", i.String(), req.MilliCPU)
			}
		}
		for _, j := range p.CandidateNodes {
			cap, ok := p.NodeCapacities[j]
			if !ok {
				return nil, fmt.Errorf("missing node capacity for %s", j.String())
			}
			if cap.MilliCPU <= 0 {
				return nil, fmt.Errorf("invalid node cpu capacity for %s: %d", j.String(), cap.MilliCPU)
			}
		}
	}
	if p.RequireMemory {
		for _, i := range p.StableReplicas {
			req, ok := p.ReplicaRequests[i]
			if !ok {
				return nil, fmt.Errorf("missing replica request for %s", i.String())
			}
			if req.MemoryMi <= 0 {
				return nil, fmt.Errorf("invalid replica memory request for %s: %d", i.String(), req.MemoryMi)
			}
		}
		for _, j := range p.CandidateNodes {
			cap, ok := p.NodeCapacities[j]
			if !ok {
				return nil, fmt.Errorf("missing node capacity for %s", j.String())
			}
			if cap.MemoryMi <= 0 {
				return nil, fmt.Errorf("invalid node memory capacity for %s: %d", j.String(), cap.MemoryMi)
			}
		}
	}

	// Validate objective inputs if provided.
	if p.Objective != nil {
		// NodeContexts should be consistent with CandidateNodes.
		if len(p.NodeContexts) == 0 {
			return nil, fmt.Errorf("objective enabled but NodeContexts is empty")
		}
		seen := map[ClusterNodeID]struct{}{}
		for _, n := range p.CandidateNodes {
			seen[n] = struct{}{}
		}
		for _, nc := range p.NodeContexts {
			if _, ok := seen[nc.ID]; !ok {
				return nil, fmt.Errorf("NodeContexts contains node %s not present in CandidateNodes", nc.ID.String())
			}
		}
		// When Migration goal is used, current placement must be present.
		for _, g := range p.Objective.Goals {
			if g.Weight == 0 {
				continue
			}
			if g.Type == "Migration" && len(p.CurrentPlacement) == 0 {
				return nil, fmt.Errorf("Migration goal enabled but CurrentPlacement is empty")
			}
		}
	}

	// Constraint counts (for testability):
	// - one assignment constraint per replica
	// - one CPU capacity constraint per node (when RequireCPU)
	// - one Memory capacity constraint per node (when RequireMemory)
	constraints := len(p.StableReplicas)
	if p.RequireCPU {
		constraints += len(p.CandidateNodes)
	}
	if p.RequireMemory {
		constraints += len(p.CandidateNodes)
	}

	return &BuildResult{NumDecisionVars: vars, NumConstraints: constraints}, nil
}

// SolveProblem builds and solves the M3 4.1 ILP problem.
//
// Contract:
// - Always enforces maximum variable size protection.
// - When opts.Timeout is set (or defaulted), it applies a context deadline.
// - If s is nil, returns an error (we haven't wired OR-Tools/SCIP yet).
func SolveProblem(ctx context.Context, s Solver, p Problem, opts SolveOptions) (*SolveResult, error) {
	start := time.Now()
	buildRes, err := BuildILPModel(ctx, p)
	if err != nil {
		return nil, err
	}
	// Log key ILP scale info for visibility.
	// Note: when OR-Tools backend is disabled, NewORToolsSolver returns a stub that will
	// error; this log helps operators tell whether we're even attempting ILP.
	backend := "unknown"
	if s == nil {
		backend = "none"
	} else {
		backend = fmt.Sprintf("%T", s)
	}
	log.Printf("[optimizer.ilp] build ok backend=%s replicas=%d nodes=%d vars=%d cons=%d objective=%t timeout=%s",
		backend,
		len(p.StableReplicas),
		len(p.CandidateNodes),
		buildRes.NumDecisionVars,
		buildRes.NumConstraints,
		p.Objective != nil,
		opts.withDefaults().Timeout.String(),
	)
	if len(p.StableReplicas) == 0 {
		log.Printf("[optimizer.ilp] skip solve (no replicas) elapsed=%s", time.Since(start))
		return &SolveResult{Status: "NoReplicas", Assignment: map[ReplicaKey]ClusterNodeID{}}, nil
	}
	if s == nil {
		return nil, fmt.Errorf("no solver configured")
	}
	o := opts.withDefaults()
	solveCtx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	res, err := s.Solve(solveCtx, p)
	if err != nil {
		// When ORTools backend isn't enabled, the stub returns a plain error.
		// Map it to a stable sentinel so callers can keep working without build tags.
		if strings.Contains(err.Error(), "ORTools backend is disabled") {
			log.Printf("[optimizer.ilp] solve skipped backend=%s reason=ortools_disabled elapsed=%s", backend, time.Since(start))
			return nil, ErrSolverNotImplemented
		}
		log.Printf("[optimizer.ilp] solve failed backend=%s err=%v elapsed=%s", backend, err, time.Since(start))
		return nil, err
	}
	assigned := 0
	if res != nil && res.Assignment != nil {
		assigned = len(res.Assignment)
	}
	st := "<nil>"
	if res != nil {
		st = res.Status
	}
	log.Printf("[optimizer.ilp] solve ok backend=%s status=%s assigned=%d elapsed=%s", backend, st, assigned, time.Since(start))
	return res, nil
}
