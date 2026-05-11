package optimizer

import "fmt"

// LatencyPlugin scores placement by estimated latency from a user source city to node region.
// Lower is better.
type LatencyPlugin struct {
	// SourceCity is the logical origin (e.g. Shanghai).
	SourceCity string

	// LatencyMs is a matrix: sourceCity -> region -> latencyMs.
	LatencyMs map[string]map[string]float64
}

func (p *LatencyPlugin) Name() string { return "Latency" }

func (p *LatencyPlugin) ScorePlacement(ctx PluginContext) (map[ReplicaKey]map[ClusterNodeID]float64, error) {
	if len(ctx.Replicas) == 0 || len(ctx.Nodes) == 0 {
		return map[ReplicaKey]map[ClusterNodeID]float64{}, nil
	}
	if p == nil {
		return nil, fmt.Errorf("LatencyPlugin is nil")
	}
	if err := requireNonEmpty("SourceCity", p.SourceCity); err != nil {
		return nil, err
	}
	row, ok := p.LatencyMs[p.SourceCity]
	if !ok || len(row) == 0 {
		return nil, fmt.Errorf("missing latency row for sourceCity %q", p.SourceCity)
	}

	// Determine min/max latency among candidate nodes.
	latByNode := make(map[ClusterNodeID]float64, len(ctx.Nodes))
	minL := 0.0
	maxL := 0.0
	init := false
	for _, n := range ctx.Nodes {
		if n.Region == "" {
			return nil, fmt.Errorf("missing region on node %s", n.ID.String())
		}
		lat, ok := row[n.Region]
		if !ok {
			return nil, fmt.Errorf("missing latency for sourceCity=%q region=%q (node %s)", p.SourceCity, n.Region, n.ID.String())
		}
		if lat < 0 {
			return nil, fmt.Errorf("invalid latency for sourceCity=%q region=%q: %v", p.SourceCity, n.Region, lat)
		}
		latByNode[n.ID] = lat
		if !init {
			minL, maxL = lat, lat
			init = true
			continue
		}
		if lat < minL {
			minL = lat
		}
		if lat > maxL {
			maxL = lat
		}
	}

	out := make(map[ReplicaKey]map[ClusterNodeID]float64, len(ctx.Replicas))
	for _, r := range ctx.Replicas {
		m := make(map[ClusterNodeID]float64, len(ctx.Nodes))
		for _, n := range ctx.Nodes {
			m[n.ID] = normalizeTo0_100(latByNode[n.ID], minL, maxL)
		}
		out[r] = m
	}
	return out, nil
}
