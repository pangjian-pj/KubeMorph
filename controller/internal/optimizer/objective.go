package optimizer

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// diagLogger is a minimal logger adapter used inside optimizer package.
// It matches controller-runtime's logr.Logger Info signature without taking a hard dependency.
type diagLogger interface {
	Info(msg string, keysAndValues ...any)
}

// diagFromContext tries to fetch a controller-runtime logr.Logger from context.
// If unavailable, it returns nil.
func diagFromContext(ctx context.Context) diagLogger {
	// We avoid importing controller-runtime/log to keep optimizer decoupled.
	// But when optimizer is invoked from controller, a logr.Logger is stored in ctx
	// and can be extracted via interface assertion.
	if ctx == nil {
		return nil
	}
	if l, ok := ctx.Value(struct{ name string }{name: "logr"}).(diagLogger); ok {
		return l
	}
	// controller-runtime uses log.FromContext(ctx) which stores a logr.Logger internally;
	// we can't access it here without importing. So we rely on callers optionally
	// providing a logger through context key below.
	if l, ok := ctx.Value(OptimizerDiagLoggerKey{}).(diagLogger); ok {
		return l
	}
	return nil
}

// OptimizerDiagLoggerKey can be used by callers to inject a logger into ctx
// for optimizer diagnostics.
type OptimizerDiagLoggerKey struct{}

type matrixDiag struct {
	Rows         int
	Cols         int
	Min          float64
	Max          float64
	UniqueValues int
	AllZero      bool
	HasNaN       bool
	HasInf       bool
}

type commInputDiag struct {
	DistinctNodeRegions    int
	TopNodeRegions         []any
	DistinctServiceNames   int
	TopServiceNames        []any
	RegionLatencyRows      int
	RegionLatencyCells     int
	RegionLatencyMin       float64
	RegionLatencyMax       float64
	RegionLatencyAllZero   bool
	RegionLatencyDiagonal0 int
}

func diagCommInputs(ctx PluginContext, deps map[NamespacedName][]NamespacedName, regionLat map[string]map[string]float64, replicaService map[ReplicaKey]NamespacedName, nodeRegion map[ClusterNodeID]string) commInputDiag {
	// node region distribution
	rc := map[string]int{}
	for _, n := range ctx.Nodes {
		reg := n.Region
		if nodeRegion != nil {
			if r, ok := nodeRegion[n.ID]; ok {
				reg = r
			}
		}
		if reg != "" {
			rc[reg]++
		}
	}

	// service name distribution (helps detect “everything mapped to same service”)
	svcCnt := map[string]int{}
	for _, s := range replicaService {
		name := s.Name
		if name != "" {
			svcCnt[name]++
		}
	}

	// region latency sample
	m := matrixDiag{AllZero: true}
	init := false
	for a, row := range regionLat {
		if a == "" {
			continue
		}
		m.Rows++
		if row == nil {
			continue
		}
		for _, v := range row {
			m.Cols++
			if v != 0 {
				m.AllZero = false
			}
			if !init {
				m.Min, m.Max = v, v
				init = true
				continue
			}
			if v < m.Min {
				m.Min = v
			}
			if v > m.Max {
				m.Max = v
			}
		}
	}

	return commInputDiag{
		DistinctNodeRegions:    len(rc),
		TopNodeRegions:         topCounts(rc, 6),
		DistinctServiceNames:   len(svcCnt),
		TopServiceNames:        topCounts(svcCnt, 6),
		RegionLatencyRows:      m.Rows,
		RegionLatencyCells:     m.Cols,
		RegionLatencyMin:       m.Min,
		RegionLatencyMax:       m.Max,
		RegionLatencyAllZero:   m.AllZero,
		RegionLatencyDiagonal0: 0,
	}
}

func diagPlacementScores(ps map[ReplicaKey]map[ClusterNodeID]float64) matrixDiag {
	d := matrixDiag{Min: 0, Max: 0, AllZero: true}
	if len(ps) == 0 {
		return d
	}
	seen := map[float64]struct{}{}
	init := false
	for _, row := range ps {
		d.Rows++
		if d.Cols == 0 {
			d.Cols = len(row)
		}
		for _, v := range row {
			if math.IsNaN(v) {
				d.HasNaN = true
				continue
			}
			if math.IsInf(v, 0) {
				d.HasInf = true
				continue
			}
			if v != 0 {
				d.AllZero = false
			}
			seen[v] = struct{}{}
			if !init {
				d.Min, d.Max = v, v
				init = true
				continue
			}
			if v < d.Min {
				d.Min = v
			}
			if v > d.Max {
				d.Max = v
			}
		}
	}
	d.UniqueValues = len(seen)
	return d
}

func diagEnabledGoals(goals []WeightedGoal) (types []string, totalWeight float64, enabled int) {
	for _, g := range goals {
		if g.Weight == 0 {
			continue
		}
		enabled++
		totalWeight += g.Weight
		types = append(types, g.Type)
	}
	sort.Strings(types)
	return types, totalWeight, enabled
}

// WeightedGoal describes one optimization goal with a weight.
// Weight should be in [0,1]. A weight of 0 disables the goal.
//
// Type should match the plugin Name(): Cost/Latency/Communication/Energy/Migration.
type WeightedGoal struct {
	Type   string
	Weight float64

	// Optional parameters for certain plugins.
	SourceCity  string
	TopologyRef string
}

// ObjectiveInputs are data sources for building goal plugins.
//
// This is a controller-facing adapter struct. In early milestones we keep it simple:
// - controller can pass empty maps and enable fewer goals.
// - unit tests can construct it directly.
type ObjectiveInputs struct {
	Goals []WeightedGoal

	// Plugin contexts.
	Ctx PluginContext

	// Cost data.
	InstancePrice map[string]float64

	// Latency data.
	// sourceCity -> region -> latencyMs
	CityRegionLatencyMs map[string]map[string]float64

	// Communication data.
	Dependencies    map[NamespacedName][]NamespacedName
	RegionLatencyMs map[string]map[string]float64
	ReplicaService  map[ReplicaKey]NamespacedName
	NodeRegion      map[ClusterNodeID]string

	// Energy data.
	InstancePower map[string]PowerCurve
}

// ObjectiveOutput is the aggregated scoring result used by plan summary.
//
// Scores follow the convention: lower is better and expected to be in [0,100]
// (except migration penalties which are additive to expectedScore when moved).
type ObjectiveOutput struct {
	PlacementScores  map[ReplicaKey]map[ClusterNodeID]float64
	MigrationPenalty map[ReplicaKey]float64
}

// BuildObjective composes enabled goal plugins, runs their scoring, and aggregates
// them into a single placement score matrix.
//
// Aggregation rule:
// - For each enabled linear goal g: totalScore[i][j] += weight_g * score_g[i][j]
// - For migration goal: migrationPenalty[i] = weight_mig * penalty_i
//
// Notes:
// - It's safe for callers to pass extra data even if a goal is disabled.
// - Missing required plugin data returns a descriptive error.
func BuildObjective(ctx context.Context, in ObjectiveInputs) (ObjectiveOutput, error) {
	if lg := diagFromContext(ctx); lg != nil {
		types, tw, enabled := diagEnabledGoals(in.Goals)
		lg.Info("objective build: inputs", "goals", types, "enabledGoals", enabled, "totalWeight", tw, "replicas", len(in.Ctx.Replicas), "nodes", len(in.Ctx.Nodes))
	}

	// Initialize placement score matrix with zeros.
	ps := make(map[ReplicaKey]map[ClusterNodeID]float64, len(in.Ctx.Replicas))
	for _, r := range in.Ctx.Replicas {
		ps[r] = make(map[ClusterNodeID]float64, len(in.Ctx.Nodes))
		for _, n := range in.Ctx.Nodes {
			ps[r][n.ID] = 0
		}
	}

	var migPenalty map[ReplicaKey]float64

	for _, g := range in.Goals {
		if g.Weight == 0 {
			continue
		}
		if g.Weight < 0 {
			return ObjectiveOutput{}, fmt.Errorf("invalid weight for goal %q: %v", g.Type, g.Weight)
		}

		switch g.Type {
		case "Cost":
			p := &CostPlugin{InstancePrice: in.InstancePrice}
			sc, err := p.ScorePlacement(in.Ctx)
			if err != nil {
				return ObjectiveOutput{}, fmt.Errorf("Cost plugin: %w", err)
			}
			if lg := diagFromContext(ctx); lg != nil {
				d := diagPlacementScores(sc)
				lg.Info("objective build: goal scored", "goal", g.Type, "weight", g.Weight, "min", d.Min, "max", d.Max, "unique", d.UniqueValues, "allZero", d.AllZero)
			}
			accumulatePlacementScores(ps, sc, g.Weight)
		case "Latency":
			p := &LatencyPlugin{SourceCity: g.SourceCity, LatencyMs: in.CityRegionLatencyMs}
			sc, err := p.ScorePlacement(in.Ctx)
			if err != nil {
				return ObjectiveOutput{}, fmt.Errorf("Latency plugin: %w", err)
			}
			if lg := diagFromContext(ctx); lg != nil {
				d := diagPlacementScores(sc)
				lg.Info("objective build: goal scored", "goal", g.Type, "weight", g.Weight, "sourceCity", g.SourceCity, "min", d.Min, "max", d.Max, "unique", d.UniqueValues, "allZero", d.AllZero)
			}
			accumulatePlacementScores(ps, sc, g.Weight)
		case "Communication":
			if lg := diagFromContext(ctx); lg != nil {
				cd := diagCommInputs(in.Ctx, in.Dependencies, in.RegionLatencyMs, in.ReplicaService, in.NodeRegion)
				lg.Info(
					"objective build: comm inputs",
					"deps", len(in.Dependencies),
					"nodeRegions", cd.DistinctNodeRegions,
					"nodeRegionsTop", cd.TopNodeRegions,
					"services", cd.DistinctServiceNames,
					"servicesTop", cd.TopServiceNames,
					"regionLatencyRows", cd.RegionLatencyRows,
					"regionLatencyCells", cd.RegionLatencyCells,
					"regionLatencyMin", cd.RegionLatencyMin,
					"regionLatencyMax", cd.RegionLatencyMax,
					"regionLatencyAllZero", cd.RegionLatencyAllZero,
					"regionLatencyDiagonalZero", cd.RegionLatencyDiagonal0,
				)
			}
			p := &CommunicationPlugin{
				Dependencies:    in.Dependencies,
				RegionLatencyMs: in.RegionLatencyMs,
				ReplicaService:  in.ReplicaService,
				NodeRegion:      in.NodeRegion,
			}
			sc, err := p.scorePlacementWithLogger(ctx, in.Ctx)
			if err != nil {
				return ObjectiveOutput{}, fmt.Errorf("Communication plugin: %w", err)
			}
			if lg := diagFromContext(ctx); lg != nil {
				d := diagPlacementScores(sc)
				lg.Info("objective build: goal scored", "goal", g.Type, "weight", g.Weight, "deps", len(in.Dependencies), "regionRows", len(in.RegionLatencyMs), "replicaService", len(in.ReplicaService), "nodeRegion", len(in.NodeRegion), "min", d.Min, "max", d.Max, "unique", d.UniqueValues, "allZero", d.AllZero)
			}
			accumulatePlacementScores(ps, sc, g.Weight)
		case "Energy":
			p := &EnergyPlugin{InstancePower: in.InstancePower}
			sc, err := p.ScorePlacement(in.Ctx)
			if err != nil {
				return ObjectiveOutput{}, fmt.Errorf("Energy plugin: %w", err)
			}
			if lg := diagFromContext(ctx); lg != nil {
				d := diagPlacementScores(sc)
				lg.Info("objective build: goal scored", "goal", g.Type, "weight", g.Weight, "min", d.Min, "max", d.Max, "unique", d.UniqueValues, "allZero", d.AllZero)
			}
			accumulatePlacementScores(ps, sc, g.Weight)
		case "Migration":
			p := &MigrationPlugin{}
			pen, err := p.ScoreMigration(in.Ctx)
			if err != nil {
				return ObjectiveOutput{}, fmt.Errorf("Migration plugin: %w", err)
			}
			if lg := diagFromContext(ctx); lg != nil {
				lg.Info("objective build: migration penalty scored", "goal", g.Type, "weight", g.Weight, "penalties", len(pen))
			}
			if migPenalty == nil {
				migPenalty = make(map[ReplicaKey]float64, len(pen))
			}
			for r, v := range pen {
				migPenalty[r] += g.Weight * v
			}
		default:
			return ObjectiveOutput{}, fmt.Errorf("unknown goal type %q", g.Type)
		}
	}

	if lg := diagFromContext(ctx); lg != nil {
		d := diagPlacementScores(ps)
		lg.Info("objective build: aggregated placement scores", "rows", d.Rows, "cols", d.Cols, "min", d.Min, "max", d.Max, "unique", d.UniqueValues, "allZero", d.AllZero)
	}

	return ObjectiveOutput{PlacementScores: ps, MigrationPenalty: migPenalty}, nil
}

func accumulatePlacementScores(dst, add map[ReplicaKey]map[ClusterNodeID]float64, weight float64) {
	for r, m := range add {
		dm := dst[r]
		if dm == nil {
			dm = map[ClusterNodeID]float64{}
			dst[r] = dm
		}
		for n, v := range m {
			dm[n] += weight * v
		}
	}
}
