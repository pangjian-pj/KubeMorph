package optimizer

import "fmt"

const (
	// LabelInstanceType is the label key used to identify node instance type.
	// This follows design_doc/optimization.md convention.
	LabelInstanceType = "node.kubex.io/type"
)

// CostPlugin scores placement by node instance price.
// Lower is better.
type CostPlugin struct {
	// InstancePrice maps instanceType => price (any positive unit).
	InstancePrice map[string]float64
}

func (p *CostPlugin) Name() string { return "Cost" }

func (p *CostPlugin) ScorePlacement(ctx PluginContext) (map[ReplicaKey]map[ClusterNodeID]float64, error) {
	if len(ctx.Replicas) == 0 || len(ctx.Nodes) == 0 {
		return map[ReplicaKey]map[ClusterNodeID]float64{}, nil
	}
	if p == nil {
		return nil, fmt.Errorf("CostPlugin is nil")
	}
	if len(p.InstancePrice) == 0 {
		return nil, fmt.Errorf("InstancePrice is empty")
	}

	// Determine min/max price among candidate nodes.
	pricesByNode := make(map[ClusterNodeID]float64, len(ctx.Nodes))
	minP := 0.0
	maxP := 0.0
	init := false
	for _, n := range ctx.Nodes {
		it := ""
		if n.Labels != nil {
			it = n.Labels[LabelInstanceType]
		}
		if it == "" {
			return nil, fmt.Errorf("missing %s label on node %s", LabelInstanceType, n.ID.String())
		}
		price, ok := p.InstancePrice[it]
		if !ok {
			return nil, fmt.Errorf("missing price for instanceType %q (node %s)", it, n.ID.String())
		}
		if price < 0 {
			return nil, fmt.Errorf("invalid price for instanceType %q: %v", it, price)
		}
		pricesByNode[n.ID] = price
		if !init {
			minP, maxP = price, price
			init = true
			continue
		}
		if price < minP {
			minP = price
		}
		if price > maxP {
			maxP = price
		}
	}

	// For cost, score only depends on node; replicate across replicas.
	out := make(map[ReplicaKey]map[ClusterNodeID]float64, len(ctx.Replicas))
	for _, r := range ctx.Replicas {
		m := make(map[ClusterNodeID]float64, len(ctx.Nodes))
		for _, n := range ctx.Nodes {
			price := pricesByNode[n.ID]
			m[n.ID] = normalizeTo0_100(price, minP, maxP)
		}
		out[r] = m
	}
	return out, nil
}
