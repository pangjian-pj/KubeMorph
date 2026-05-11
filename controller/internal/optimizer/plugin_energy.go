package optimizer

import (
	"fmt"
	"sort"
)

// EnergyPlugin scores placement by marginal power cost.
//
// M3 4.2 simplified model:
//   - A power curve is given per instanceType: util->power(W)
//   - For each node j, compute baseline util from CPU usage (requested) / allocatable.
//   - Compute marginal coefficient approx dP/du at baseline util, and convert to Watts/core:
//     coeff_j ~= slope(util) / totalCores
//   - Score_{ij} = coeff_j * cpuRequest_i
//
// Returned scores are normalized to [0,100] across all (i,j).
type EnergyPlugin struct {
	// InstancePower maps instanceType => util->power samples.
	InstancePower map[string]PowerCurve

	// InstanceTypeLabelKey defaults to LabelInstanceType.
	InstanceTypeLabelKey string
}

func (p *EnergyPlugin) Name() string { return "Energy" }

// PowerCurve describes power samples at different utilizations.
// Util should be within [0,1].
type PowerCurve struct {
	Samples []PowerSample
}

type PowerSample struct {
	Util  float64
	Power float64
}

func (p *EnergyPlugin) ScorePlacement(ctx PluginContext) (map[ReplicaKey]map[ClusterNodeID]float64, error) {
	if len(ctx.Replicas) == 0 || len(ctx.Nodes) == 0 {
		return map[ReplicaKey]map[ClusterNodeID]float64{}, nil
	}
	if p == nil {
		return nil, fmt.Errorf("EnergyPlugin is nil")
	}
	if len(p.InstancePower) == 0 {
		return nil, fmt.Errorf("InstancePower is empty")
	}
	key := p.InstanceTypeLabelKey
	if key == "" {
		key = LabelInstanceType
	}
	if ctx.ReplicaRequests == nil {
		return nil, fmt.Errorf("ReplicaRequests is nil")
	}

	// Precompute coeff_j for each node.
	coeff := make(map[ClusterNodeID]float64, len(ctx.Nodes))
	for _, n := range ctx.Nodes {
		it := ""
		if n.Labels != nil {
			it = n.Labels[key]
		}
		if it == "" {
			return nil, fmt.Errorf("missing %s label on node %s", key, n.ID.String())
		}
		curve, ok := p.InstancePower[it]
		if !ok {
			return nil, fmt.Errorf("missing power curve for instanceType %q (node %s)", it, n.ID.String())
		}
		if n.CPUAllocatableMilli <= 0 {
			return nil, fmt.Errorf("missing/invalid CPUAllocatableMilli for node %s", n.ID.String())
		}
		baselineUsed := n.CPUAllocatableMilli - n.CPUFreeMilli
		if baselineUsed < 0 {
			baselineUsed = 0
		}
		util := float64(baselineUsed) / float64(n.CPUAllocatableMilli)
		if util < 0 {
			util = 0
		}
		if util > 1 {
			util = 1
		}
		wPerCore, err := marginalWattsPerCore(curve, util, float64(n.CPUAllocatableMilli)/1000.0)
		if err != nil {
			return nil, err
		}
		coeff[n.ID] = wPerCore
	}

	// Compute raw score per (replica,node).
	raw := make(map[ReplicaKey]map[ClusterNodeID]float64, len(ctx.Replicas))
	minV := 0.0
	maxV := 0.0
	init := false
	for _, r := range ctx.Replicas {
		req, ok := ctx.ReplicaRequests[r]
		if !ok {
			return nil, fmt.Errorf("missing replica request for %s", r.String())
		}
		if req.MilliCPU <= 0 {
			return nil, fmt.Errorf("invalid replica cpu request for %s: %d", r.String(), req.MilliCPU)
		}
		m := make(map[ClusterNodeID]float64, len(ctx.Nodes))
		for _, n := range ctx.Nodes {
			v := coeff[n.ID] * (float64(req.MilliCPU) / 1000.0)
			m[n.ID] = v
			if !init {
				minV, maxV = v, v
				init = true
			} else {
				if v < minV {
					minV = v
				}
				if v > maxV {
					maxV = v
				}
			}
		}
		raw[r] = m
	}

	out := make(map[ReplicaKey]map[ClusterNodeID]float64, len(raw))
	for r, m := range raw {
		nm := make(map[ClusterNodeID]float64, len(m))
		for nid, v := range m {
			nm[nid] = normalizeTo0_100(v, minV, maxV)
		}
		out[r] = nm
	}
	return out, nil
}

func marginalWattsPerCore(curve PowerCurve, util float64, totalCores float64) (float64, error) {
	if totalCores <= 0 {
		return 0, fmt.Errorf("invalid totalCores: %v", totalCores)
	}
	if len(curve.Samples) < 2 {
		return 0, fmt.Errorf("power curve must have at least 2 samples")
	}
	// copy & sort
	s := append([]PowerSample(nil), curve.Samples...)
	sort.Slice(s, func(i, j int) bool { return s[i].Util < s[j].Util })
	for i := 0; i < len(s); i++ {
		if s[i].Util < 0 || s[i].Util > 1 {
			return 0, fmt.Errorf("invalid util in power curve: %v", s[i].Util)
		}
	}
	if util <= s[0].Util {
		// forward diff
		du := s[1].Util - s[0].Util
		if du <= 0 {
			return 0, fmt.Errorf("invalid power curve (duplicate util)")
		}
		slope := (s[1].Power - s[0].Power) / du
		return slope / totalCores, nil
	}
	if util >= s[len(s)-1].Util {
		// backward diff
		du := s[len(s)-1].Util - s[len(s)-2].Util
		if du <= 0 {
			return 0, fmt.Errorf("invalid power curve (duplicate util)")
		}
		slope := (s[len(s)-1].Power - s[len(s)-2].Power) / du
		return slope / totalCores, nil
	}
	// find segment containing util
	for i := 0; i < len(s)-1; i++ {
		if util >= s[i].Util && util <= s[i+1].Util {
			du := s[i+1].Util - s[i].Util
			if du <= 0 {
				return 0, fmt.Errorf("invalid power curve (duplicate util)")
			}
			slope := (s[i+1].Power - s[i].Power) / du
			return slope / totalCores, nil
		}
	}
	return 0, fmt.Errorf("cannot find util segment")
}
