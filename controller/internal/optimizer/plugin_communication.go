package optimizer

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// NOTE: rich diagnostics are logged in BuildObjective where a logger can be injected via context.

// diagLoggerFromContext fetches an injected diag logger from context.
// It mirrors optimizer.diagFromContext but is duplicated here to keep files independent.
func diagLoggerFromContext(ctx context.Context) diagLogger {
	if ctx == nil {
		return nil
	}
	if l, ok := ctx.Value(OptimizerDiagLoggerKey{}).(diagLogger); ok {
		return l
	}
	return nil
}

func topCounts(m map[string]int, topN int) []any {
	if topN <= 0 {
		topN = 5
	}
	type kv struct {
		k string
		v int
	}
	arr := make([]kv, 0, len(m))
	for k, v := range m {
		arr = append(arr, kv{k: k, v: v})
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].v != arr[j].v {
			return arr[i].v > arr[j].v
		}
		return arr[i].k < arr[j].k
	})
	if len(arr) > topN {
		arr = arr[:topN]
	}
	out := make([]any, 0, len(arr))
	for _, it := range arr {
		out = append(out, fmt.Sprintf("%s=%d", it.k, it.v))
	}
	return out
}

type matSample struct {
	Rows      int
	Cells     int
	Min       float64
	Max       float64
	HasNaN    bool
	HasInf    bool
	AllZero   bool
	Diagonal0 int
}

func diagRegionLatency(mat map[string]map[string]float64) matSample {
	d := matSample{AllZero: true}
	init := false
	for a, row := range mat {
		if a == "" {
			continue
		}
		d.Rows++
		if row == nil {
			continue
		}
		if v, ok := row[a]; ok {
			if v == 0 {
				d.Diagonal0++
			}
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
			d.Cells++
			if v != 0 {
				d.AllZero = false
			}
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
	return d
}

// CommunicationPlugin scores placement by cross-service communication latency.
//
// M3 4.2 simplified linearization (per design_doc/optimization.md):
// - For each service K that current replica depends on, compute a centroid region for K.
// - Then score placement of replica i on node j as CommCost(region(j), centroid(K)).
//
// This plugin operates on GlobalDeployment-level dependency graph, but scoring is per replica.
type CommunicationPlugin struct {
	// Dependencies maps a service (namespace/name) to the services it depends on.
	// Key and values use ReplicaKey-like identity but without replicaIndex.
	Dependencies map[NamespacedName][]NamespacedName

	// RegionLatencyMs is a matrix: regionA -> regionB -> latencyMs.
	RegionLatencyMs map[string]map[string]float64

	// ReplicaService maps a replica to its service identity (ns/name).
	ReplicaService map[ReplicaKey]NamespacedName

	// NodeRegion maps NodeID to region.
	// If empty, ctx.Nodes[*].Region is used.
	NodeRegion map[ClusterNodeID]string

	// CurrentReplicaRegion provides best-effort current region per replica.
	// If empty, it will try to map from ctx.CurrentPlacement + NodeRegion/ctx.Nodes.
	CurrentReplicaRegion map[ReplicaKey]string
}

func (p *CommunicationPlugin) Name() string { return "Communication" }

// NamespacedName is a minimal helper for service identity.
type NamespacedName struct {
	Namespace string
	Name      string
}

func (n NamespacedName) String() string {
	return n.Namespace + "/" + n.Name
}

func (p *CommunicationPlugin) ScorePlacement(ctx PluginContext) (map[ReplicaKey]map[ClusterNodeID]float64, error) {
	return p.scorePlacementWithLogger(context.Background(), ctx)
}

// AverageCommunicationLatencyMs computes a readable, placement-level metric:
// the average communication latency (ms) across all replicas for the given placement.
//
// Definition (current simplified model):
// - For each replica i, find its service svc(i).
// - For each dependency dep of svc(i), compute centroid region of dep.
// - Latency for i is avg over deps: regionLatency(region(node(i)) -> centroid(dep)).
// - Result is avg over replicas that have at least one dependency.
//
// This is used by controller/UI to display human-readable Communication improvements.
func (p *CommunicationPlugin) AverageCommunicationLatencyMs(ctx PluginContext) (float64, error) {
	// Reuse the same validation and centroid computation path as scoring.
	// We intentionally don't normalize; we return original ms.
	if len(ctx.Replicas) == 0 {
		return 0, nil
	}
	if p == nil {
		return 0, fmt.Errorf("CommunicationPlugin is nil")
	}
	if len(p.Dependencies) == 0 {
		return 0, fmt.Errorf("Dependencies is empty")
	}
	if len(p.RegionLatencyMs) == 0 {
		return 0, fmt.Errorf("RegionLatencyMs is empty")
	}
	if len(p.ReplicaService) == 0 {
		return 0, fmt.Errorf("ReplicaService is empty")
	}

	// Node region lookup.
	nodeRegion := map[ClusterNodeID]string{}
	for _, n := range ctx.Nodes {
		reg := n.Region
		if p.NodeRegion != nil {
			if r, ok := p.NodeRegion[n.ID]; ok {
				reg = r
			}
		}
		if reg == "" {
			return 0, fmt.Errorf("missing region for node %s", n.ID.String())
		}
		nodeRegion[n.ID] = reg
	}

	// Resolve current replica regions.
	currentRegion := map[ReplicaKey]string{}
	if p.CurrentReplicaRegion != nil {
		for k, v := range p.CurrentReplicaRegion {
			currentRegion[k] = v
		}
	}
	if len(currentRegion) == 0 {
		for rk, nid := range ctx.CurrentPlacement {
			if reg, ok := nodeRegion[nid]; ok {
				currentRegion[rk] = reg
			}
		}
	}

	// Pre-compute centroid region for each dependency service.
	centroid := map[NamespacedName]string{}
	for svc := range p.Dependencies {
		deps := p.Dependencies[svc]
		for _, dep := range deps {
			if _, ok := centroid[dep]; ok {
				continue
			}
			cr, err := computeServiceCentroidRegion(dep, currentRegion, p.ReplicaService, p.RegionLatencyMs)
			if err != nil {
				return 0, err
			}
			centroid[dep] = cr
		}
	}

	var sum float64
	var cnt int
	for _, r := range ctx.Replicas {
		svc, ok := p.ReplicaService[r]
		if !ok {
			continue
		}
		deps := p.Dependencies[svc]
		if len(deps) == 0 {
			continue
		}
		nid, ok := ctx.CurrentPlacement[r]
		if !ok {
			continue
		}
		regJ, ok := nodeRegion[nid]
		if !ok {
			continue
		}
		// per-replica avg over deps
		var rs float64
		var rc int
		for _, dep := range deps {
			cr, ok := centroid[dep]
			if !ok {
				continue
			}
			lat, err := lookupRegionLatency(p.RegionLatencyMs, regJ, cr)
			if err != nil {
				return 0, err
			}
			rs += lat
			rc++
		}
		if rc == 0 {
			continue
		}
		sum += rs / float64(rc)
		cnt++
	}
	if cnt == 0 {
		return 0, nil
	}
	return sum / float64(cnt), nil
}

// scorePlacementWithLogger is the same as ScorePlacement, but allows callers (BuildObjective)
// to pass a context that contains an optional diagnostic logger.
func (p *CommunicationPlugin) scorePlacementWithLogger(diagCtx context.Context, ctx PluginContext) (map[ReplicaKey]map[ClusterNodeID]float64, error) {
	if len(ctx.Replicas) == 0 || len(ctx.Nodes) == 0 {
		return map[ReplicaKey]map[ClusterNodeID]float64{}, nil
	}
	if p == nil {
		return nil, fmt.Errorf("CommunicationPlugin is nil")
	}
	if len(p.Dependencies) == 0 {
		return nil, fmt.Errorf("Dependencies is empty")
	}
	if len(p.RegionLatencyMs) == 0 {
		return nil, fmt.Errorf("RegionLatencyMs is empty")
	}
	if len(p.ReplicaService) == 0 {
		return nil, fmt.Errorf("ReplicaService is empty")
	}

	// Build node region lookup.
	nodeRegion := map[ClusterNodeID]string{}
	regionCount := map[string]int{}
	for _, n := range ctx.Nodes {
		reg := n.Region
		if p.NodeRegion != nil {
			if r, ok := p.NodeRegion[n.ID]; ok {
				reg = r
			}
		}
		if reg == "" {
			return nil, fmt.Errorf("missing region for node %s", n.ID.String())
		}
		nodeRegion[n.ID] = reg
		regionCount[reg]++
	}
	_ = regionCount
	lg := diagLoggerFromContext(diagCtx)

	// Resolve current replica regions.
	currentRegion := map[ReplicaKey]string{}
	if p.CurrentReplicaRegion != nil {
		for k, v := range p.CurrentReplicaRegion {
			currentRegion[k] = v
		}
	}
	if len(currentRegion) == 0 {
		for rk, nid := range ctx.CurrentPlacement {
			if reg, ok := nodeRegion[nid]; ok {
				currentRegion[rk] = reg
			}
		}
	}

	// Pre-compute centroid region for each service that appears as a dependency target.
	centroid := map[NamespacedName]string{}
	centroidCount := map[string]int{}
	for svc := range p.Dependencies {
		deps := p.Dependencies[svc]
		for _, dep := range deps {
			if _, ok := centroid[dep]; ok {
				continue
			}
			cr, err := computeServiceCentroidRegion(dep, currentRegion, p.ReplicaService, p.RegionLatencyMs)
			if err != nil {
				return nil, err
			}
			centroid[dep] = cr
			centroidCount[cr]++
		}
	}
	_ = centroidCount
	if lg != nil {
		matD := diagRegionLatency(p.RegionLatencyMs)
		lg.Info(
			"comm plugin: centroid inputs",
			"deps", len(p.Dependencies),
			"nodeRegionsDistinct", len(regionCount),
			"nodeRegionsTop", topCounts(regionCount, 6),
			"centroidRegionsDistinct", len(centroidCount),
			"centroidRegionsTop", topCounts(centroidCount, 6),
			"regionLatencyRows", matD.Rows,
			"regionLatencyCells", matD.Cells,
			"regionLatencyMin", matD.Min,
			"regionLatencyMax", matD.Max,
			"regionLatencyAllZero", matD.AllZero,
			"regionLatencyDiagonalZero", matD.Diagonal0,
		)
	}

	// Compute raw comm cost per (replica,node).
	// Since multiple dependencies may exist, we sum their costs.
	raw := make(map[ReplicaKey]map[ClusterNodeID]float64, len(ctx.Replicas))
	minC := 0.0
	maxC := 0.0
	init := false
	rawUnique := map[float64]struct{}{}
	// sampling for diagnostics: compare one replica across distinct regions
	var sampleReplica *ReplicaKey
	sampleByRegion := map[string]float64{}
	// sampling for diagnostics: also retain one node id per distinct region
	sampleNodeByRegion := map[string]ClusterNodeID{}
	for _, r := range ctx.Replicas {
		svc, ok := p.ReplicaService[r]
		if !ok {
			return nil, fmt.Errorf("missing ReplicaService for replica %s", r.String())
		}
		deps := p.Dependencies[svc]
		// pick sample replica once
		if lg != nil && sampleReplica == nil {
			tmp := r
			sampleReplica = &tmp
		}
		m := make(map[ClusterNodeID]float64, len(ctx.Nodes))
		for _, n := range ctx.Nodes {
			regJ := nodeRegion[n.ID]
			sum := 0.0
			for _, dep := range deps {
				cr := centroid[dep]
				lat, err := lookupRegionLatency(p.RegionLatencyMs, regJ, cr)
				if err != nil {
					return nil, fmt.Errorf("replica %s commcost error: %w", r.String(), err)
				}
				sum += lat
			}
			m[n.ID] = sum
			rawUnique[sum] = struct{}{}
			// diag sampling: for sample replica capture one value + node per region
			if lg != nil && sampleReplica != nil && r == *sampleReplica {
				if _, ok := sampleByRegion[regJ]; !ok {
					sampleByRegion[regJ] = sum
					sampleNodeByRegion[regJ] = n.ID
				}
			}
			if !init {
				minC, maxC = sum, sum
				init = true
			} else {
				if sum < minC {
					minC = sum
				}
				if sum > maxC {
					maxC = sum
				}
			}
		}
		raw[r] = m
	}
	_ = rawUnique
	if lg != nil {
		// summarize raw costs
		lg.Info(
			"comm plugin: raw scored",
			"replicas", len(ctx.Replicas),
			"nodes", len(ctx.Nodes),
			"rawMin", minC,
			"rawMax", maxC,
			"rawUnique", len(rawUnique),
			"rawAllSame", maxC <= minC,
		)
		if sampleReplica != nil {
			lg.Info(
				"comm plugin: raw sample by region",
				"replica", sampleReplica.String(),
				"regions", len(sampleByRegion),
				"values", topCounts(floatBucket(sampleByRegion), 10),
			)

			// Pair-level breakdown: for one sample node per region, print each dep centroid and the per-dep latency.
			svc := p.ReplicaService[*sampleReplica]
			deps := p.Dependencies[svc]
			// put a hard cap to keep logs bounded
			if len(deps) > 10 {
				deps = deps[:10]
			}
			for regJ, nid := range sampleNodeByRegion {
				// compute per-dep pairs
				pairs := make([]any, 0, len(deps))
				for _, dep := range deps {
					cr := centroid[dep]
					lat, err := lookupRegionLatency(p.RegionLatencyMs, regJ, cr)
					if err != nil {
						pairs = append(pairs, fmt.Sprintf("%s->%s=ERR:%v", regJ, cr, err))
						continue
					}
					pairs = append(pairs, fmt.Sprintf("%s->%s=%.3f(dep=%s)", regJ, cr, lat, dep.String()))
				}
				lg.Info(
					"comm plugin: pair sample",
					"replica", sampleReplica.String(),
					"service", svc.String(),
					"nodeID", nid.String(),
					"nodeRegion", regJ,
					"deps", len(deps),
					"pairs", pairs,
				)
			}
		}
	}

	// Normalize to [0,100] globally.
	out := make(map[ReplicaKey]map[ClusterNodeID]float64, len(raw))
	postUnique := map[float64]struct{}{}
	postMin := 0.0
	postMax := 0.0
	postInit := false
	for r, m := range raw {
		nm := make(map[ClusterNodeID]float64, len(m))
		for nid, v := range m {
			nv := normalizeTo0_100(v, minC, maxC)
			nm[nid] = nv
			if lg != nil {
				postUnique[nv] = struct{}{}
				if !postInit {
					postMin, postMax = nv, nv
					postInit = true
				} else {
					if nv < postMin {
						postMin = nv
					}
					if nv > postMax {
						postMax = nv
					}
				}
			}
		}
		out[r] = nm
	}
	if lg != nil {
		lg.Info(
			"comm plugin: normalized scored",
			"min", postMin,
			"max", postMax,
			"unique", len(postUnique),
			"allZero", len(postUnique) == 1 && postMin == 0 && postMax == 0,
			"rawMin", minC,
			"rawMax", maxC,
		)
	}
	return out, nil
}

// floatBucket converts map[region]value to map["region:value"]count for reuse with topCounts.
func floatBucket(m map[string]float64) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[fmt.Sprintf("%s:%.3f", k, v)] = 1
	}
	return out
}

func lookupRegionLatency(mat map[string]map[string]float64, a, b string) (float64, error) {
	row, ok := mat[a]
	if !ok {
		return 0, fmt.Errorf("missing region latency row: %q", a)
	}
	v, ok := row[b]
	if !ok {
		return 0, fmt.Errorf("missing region latency value: %q->%q", a, b)
	}
	if v < 0 {
		return 0, fmt.Errorf("invalid region latency value: %q->%q=%v", a, b, v)
	}
	return v, nil
}

// computeServiceCentroidRegion returns argmin_{region_c} avgLatency(region_c -> regions(serviceReplicas)).
func computeServiceCentroidRegion(
	svc NamespacedName,
	currentReplicaRegion map[ReplicaKey]string,
	replicaService map[ReplicaKey]NamespacedName,
	regionLatencyMs map[string]map[string]float64,
) (string, error) {
	// collect regions where replicas of svc are currently located
	regions := make([]string, 0)
	for rk, s := range replicaService {
		// Topology dependencies may come from ConfigMap matrices that only contain service name
		// (without namespace). In that case, treat namespace as wildcard and match by name.
		if svc.Namespace == "" {
			if s.Name != svc.Name {
				continue
			}
		} else {
			if s != svc {
				continue
			}
		}
		reg := currentReplicaRegion[rk]
		if reg == "" {
			continue
		}
		regions = append(regions, reg)
	}
	if len(regions) == 0 {
		return "", fmt.Errorf("cannot compute centroid: no current replica regions for service %s", svc.String())
	}

	// candidate centroid regions are all regions present in regionLatencyMs
	best := ""
	bestAvg := 0.0
	for cand := range regionLatencyMs {
		sum := 0.0
		for _, r := range regions {
			v, err := lookupRegionLatency(regionLatencyMs, cand, r)
			if err != nil {
				return "", err
			}
			sum += v
		}
		avg := sum / float64(len(regions))
		if best == "" || avg < bestAvg {
			best = cand
			bestAvg = avg
		}
	}
	if best == "" {
		return "", fmt.Errorf("cannot compute centroid: no candidate regions")
	}
	return best, nil
}
