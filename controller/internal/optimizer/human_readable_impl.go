package optimizer

import (
	"fmt"
)

func computeCostTotal(in ObjectiveInputs, expected ExpectedPlacement) (from, to float64, _ error) {
	if len(in.Ctx.Replicas) == 0 {
		return 0, 0, nil
	}
	if len(in.InstancePrice) == 0 {
		return 0, 0, fmt.Errorf("Cost readable metric: InstancePrice is empty")
	}
	if len(in.Ctx.Nodes) == 0 {
		return 0, 0, fmt.Errorf("Cost readable metric: Nodes is empty")
	}

	priceByNode, err := buildNodeInstancePrice(in.Ctx.Nodes, in.InstancePrice)
	if err != nil {
		return 0, 0, err
	}

	// Interpret as total hourly cost: sum price(node_of_replica).
	for _, r := range in.Ctx.Replicas {
		curNode, ok := in.Ctx.CurrentPlacement[r]
		if ok {
			from += priceByNode[curNode]
		}
		expNode, ok := expected[r]
		if ok {
			to += priceByNode[expNode]
		}
	}
	return from, to, nil
}

func buildNodeInstancePrice(nodes []NodeContext, instPrice map[string]float64) (map[ClusterNodeID]float64, error) {
	out := make(map[ClusterNodeID]float64, len(nodes))
	for _, n := range nodes {
		it := ""
		if n.Labels != nil {
			it = n.Labels[LabelInstanceType]
		}
		if it == "" {
			return nil, fmt.Errorf("Cost readable metric: missing %s label on node %s", LabelInstanceType, n.ID.String())
		}
		p, ok := instPrice[it]
		if !ok {
			return nil, fmt.Errorf("Cost readable metric: missing price for instanceType %q (node %s)", it, n.ID.String())
		}
		out[n.ID] = p
	}
	return out, nil
}

func computeLatencyAvgMs(in ObjectiveInputs, goal WeightedGoal, expected ExpectedPlacement) (from, to float64, _ error) {
	if len(in.Ctx.Replicas) == 0 {
		return 0, 0, nil
	}
	if goal.SourceCity == "" {
		return 0, 0, fmt.Errorf("Latency readable metric: SourceCity is empty")
	}
	row, ok := in.CityRegionLatencyMs[goal.SourceCity]
	if !ok || len(row) == 0 {
		return 0, 0, fmt.Errorf("Latency readable metric: missing latency row for sourceCity %q", goal.SourceCity)
	}

	regionByNode, err := buildNodeRegion(in.Ctx.Nodes, in.NodeRegion)
	if err != nil {
		return 0, 0, err
	}

	var nFrom, nTo int
	for _, r := range in.Ctx.Replicas {
		if curNode, ok := in.Ctx.CurrentPlacement[r]; ok {
			if reg, ok := regionByNode[curNode]; ok {
				if lat, ok := row[reg]; ok {
					from += lat
					nFrom++
				}
			}
		}
		if expNode, ok := expected[r]; ok {
			if reg, ok := regionByNode[expNode]; ok {
				if lat, ok := row[reg]; ok {
					to += lat
					nTo++
				}
			}
		}
	}
	if nFrom > 0 {
		from = from / float64(nFrom)
	}
	if nTo > 0 {
		to = to / float64(nTo)
	}
	return from, to, nil
}

func computeCommunicationAvgMs(in ObjectiveInputs, expected ExpectedPlacement) (from, to float64, _ error) {
	if len(in.Ctx.Replicas) == 0 {
		return 0, 0, nil
	}
	p := &CommunicationPlugin{
		Dependencies:    in.Dependencies,
		RegionLatencyMs: in.RegionLatencyMs,
		ReplicaService:  in.ReplicaService,
		NodeRegion:      in.NodeRegion,
	}

	// Compute average on current placement.
	avgCur, err := p.AverageCommunicationLatencyMs(in.Ctx)
	if err != nil {
		return 0, 0, err
	}
	from = avgCur

	// Compute average on expected placement.
	ctx2 := in.Ctx
	ctx2.CurrentPlacement = map[ReplicaKey]ClusterNodeID{}
	for k, v := range expected {
		ctx2.CurrentPlacement[k] = v
	}
	avgExp, err := p.AverageCommunicationLatencyMs(ctx2)
	if err != nil {
		return 0, 0, err
	}
	to = avgExp

	return from, to, nil
}

func computeEnergyTotalW(in ObjectiveInputs, expected ExpectedPlacement) (from, to float64, _ error) {
	if len(in.Ctx.Replicas) == 0 {
		return 0, 0, nil
	}
	if len(in.InstancePower) == 0 {
		return 0, 0, fmt.Errorf("Energy readable metric: InstancePower is empty")
	}
	if in.Ctx.ReplicaRequests == nil {
		return 0, 0, fmt.Errorf("Energy readable metric: ReplicaRequests is nil")
	}

	wPerCore, err := buildNodeWattsPerCore(in.Ctx.Nodes, in.InstancePower)
	if err != nil {
		return 0, 0, err
	}

	// Interpret as total marginal watts: sum wPerCore(node) * cores(request).
	for _, r := range in.Ctx.Replicas {
		req := in.Ctx.ReplicaRequests[r]
		cores := float64(req.MilliCPU) / 1000.0
		if curNode, ok := in.Ctx.CurrentPlacement[r]; ok {
			from += wPerCore[curNode] * cores
		}
		if expNode, ok := expected[r]; ok {
			to += wPerCore[expNode] * cores
		}
	}
	return from, to, nil
}

func buildNodeWattsPerCore(nodes []NodeContext, instPower map[string]PowerCurve) (map[ClusterNodeID]float64, error) {
	out := make(map[ClusterNodeID]float64, len(nodes))
	for _, n := range nodes {
		it := ""
		if n.Labels != nil {
			it = n.Labels[LabelInstanceType]
		}
		if it == "" {
			return nil, fmt.Errorf("Energy readable metric: missing %s label on node %s", LabelInstanceType, n.ID.String())
		}
		curve, ok := instPower[it]
		if !ok {
			return nil, fmt.Errorf("Energy readable metric: missing power curve for instanceType %q (node %s)", it, n.ID.String())
		}
		if n.CPUAllocatableMilli <= 0 {
			return nil, fmt.Errorf("Energy readable metric: missing/invalid CPUAllocatableMilli for node %s", n.ID.String())
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
		val, err := marginalWattsPerCore(curve, util, float64(n.CPUAllocatableMilli)/1000.0)
		if err != nil {
			return nil, err
		}
		out[n.ID] = val
	}
	return out, nil
}

func computeMigrationMoves(in ObjectiveInputs, expected ExpectedPlacement) (from, to float64, _ error) {
	// Current placement has 0 moves by definition; expected has number of replicas that change location.
	// We surface it as a metric so UI can show "预计迁移 X 个副本" as Migration goal output.
	var moves int
	for _, r := range in.Ctx.Replicas {
		cur, ok1 := in.Ctx.CurrentPlacement[r]
		exp, ok2 := expected[r]
		if ok1 && ok2 && cur != exp {
			moves++
		}
	}
	from = 0
	to = float64(moves)
	return from, to, nil
}

func buildNodeRegion(nodes []NodeContext, override map[ClusterNodeID]string) (map[ClusterNodeID]string, error) {
	out := make(map[ClusterNodeID]string, len(nodes))
	for _, n := range nodes {
		reg := n.Region
		if override != nil {
			if v, ok := override[n.ID]; ok {
				reg = v
			}
		}
		if reg == "" {
			return nil, fmt.Errorf("readable metric: missing region for node %s", n.ID.String())
		}
		out[n.ID] = reg
	}
	return out, nil
}
