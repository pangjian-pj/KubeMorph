package optimizer

import "testing"

func TestCostPlugin_Normalize(t *testing.T) {
	replicas := []ReplicaKey{{Namespace: "default", Name: "a", ReplicaIndex: 0}}
	n1 := ClusterNodeID{ClusterID: "c1", NodeName: "n1"}
	n2 := ClusterNodeID{ClusterID: "c1", NodeName: "n2"}
	ctx := PluginContext{
		Replicas: replicas,
		Nodes: []NodeContext{
			{ID: n1, Labels: map[string]string{LabelInstanceType: "t1"}},
			{ID: n2, Labels: map[string]string{LabelInstanceType: "t2"}},
		},
	}
	p := &CostPlugin{InstancePrice: map[string]float64{"t1": 1, "t2": 2}}
	out, err := p.ScorePlacement(ctx)
	if err != nil {
		t.Fatalf("ScorePlacement error: %v", err)
	}
	if out[replicas[0]][n1] != 0 {
		t.Fatalf("expected cheapest node score 0, got %v", out[replicas[0]][n1])
	}
	if out[replicas[0]][n2] != 100 {
		t.Fatalf("expected expensive node score 100, got %v", out[replicas[0]][n2])
	}
}

func TestLatencyPlugin_Normalize(t *testing.T) {
	replicas := []ReplicaKey{{Namespace: "default", Name: "a", ReplicaIndex: 0}}
	n1 := ClusterNodeID{ClusterID: "c1", NodeName: "n1"}
	n2 := ClusterNodeID{ClusterID: "c1", NodeName: "n2"}
	ctx := PluginContext{
		Replicas: replicas,
		Nodes: []NodeContext{
			{ID: n1, Region: "hz"},
			{ID: n2, Region: "bj"},
		},
	}
	p := &LatencyPlugin{
		SourceCity: "sh",
		LatencyMs:  map[string]map[string]float64{"sh": {"hz": 10, "bj": 30}},
	}
	out, err := p.ScorePlacement(ctx)
	if err != nil {
		t.Fatalf("ScorePlacement error: %v", err)
	}
	if out[replicas[0]][n1] != 0 {
		t.Fatalf("expected lowest latency score 0, got %v", out[replicas[0]][n1])
	}
	if out[replicas[0]][n2] != 100 {
		t.Fatalf("expected highest latency score 100, got %v", out[replicas[0]][n2])
	}
}

func TestCommunicationPlugin_CentroidAndNormalize(t *testing.T) {
	// services: frontend depends on cart
	frontend := NamespacedName{Namespace: "default", Name: "frontend"}
	cart := NamespacedName{Namespace: "default", Name: "cart"}

	// replicas: one frontend, two cart
	rFront := ReplicaKey{Namespace: "default", Name: "frontend", ReplicaIndex: 0}
	rCart0 := ReplicaKey{Namespace: "default", Name: "cart", ReplicaIndex: 0}
	rCart1 := ReplicaKey{Namespace: "default", Name: "cart", ReplicaIndex: 1}
	replicas := []ReplicaKey{rFront, rCart0, rCart1}

	nHz := ClusterNodeID{ClusterID: "c1", NodeName: "hz-1"}
	nBj := ClusterNodeID{ClusterID: "c1", NodeName: "bj-1"}
	ctx := PluginContext{
		Replicas: replicas,
		Nodes: []NodeContext{
			{ID: nHz, Region: "hz"},
			{ID: nBj, Region: "bj"},
		},
		CurrentPlacement: map[ReplicaKey]ClusterNodeID{
			rCart0: nHz,
			rCart1: nHz,
		},
	}

	mat := map[string]map[string]float64{
		"hz": {"hz": 1, "bj": 50},
		"bj": {"hz": 45, "bj": 1},
	}

	p := &CommunicationPlugin{
		Dependencies:    map[NamespacedName][]NamespacedName{frontend: {cart}},
		RegionLatencyMs: mat,
		ReplicaService: map[ReplicaKey]NamespacedName{
			rFront: frontend,
			rCart0: cart,
			rCart1: cart,
		},
	}
	out, err := p.ScorePlacement(ctx)
	if err != nil {
		t.Fatalf("ScorePlacement error: %v", err)
	}
	// cart replicas centroid should be hz because both cart replicas are in hz.
	// thus placing frontend to hz yields lower comm cost than to bj.
	if !(out[rFront][nHz] < out[rFront][nBj]) {
		t.Fatalf("expected hz score < bj score, got hz=%v bj=%v", out[rFront][nHz], out[rFront][nBj])
	}
}

func TestEnergyPlugin_MarginalAndNormalize(t *testing.T) {
	r := ReplicaKey{Namespace: "default", Name: "a", ReplicaIndex: 0}
	n1 := ClusterNodeID{ClusterID: "c1", NodeName: "n1"}
	n2 := ClusterNodeID{ClusterID: "c1", NodeName: "n2"}
	ctx := PluginContext{
		Replicas: []ReplicaKey{r},
		Nodes: []NodeContext{
			{
				ID:                  n1,
				Labels:              map[string]string{LabelInstanceType: "t"},
				CPUAllocatableMilli: 2000,
				CPUFreeMilli:        1000, // util=0.5
			},
			{
				ID:                  n2,
				Labels:              map[string]string{LabelInstanceType: "t"},
				CPUAllocatableMilli: 2000,
				CPUFreeMilli:        0, // util=1.0 (higher slope in our sample)
			},
		},
		ReplicaRequests: map[ReplicaKey]ResourceQuantity{r: {MilliCPU: 1000}},
	}
	p := &EnergyPlugin{
		InstancePower: map[string]PowerCurve{
			// piecewise: low util slope 100W, high util slope 300W
			"t": {Samples: []PowerSample{{Util: 0.0, Power: 100}, {Util: 0.5, Power: 150}, {Util: 1.0, Power: 300}}},
		},
	}
	// Now shift n1 to util=0.25 to hit low-slope segment.
	ctx.Nodes[0].CPUFreeMilli = 1500 // used=500 => util=0.25
	out2, err := p.ScorePlacement(ctx)
	if err != nil {
		t.Fatalf("ScorePlacement error: %v", err)
	}
	if out2[r][n1] != 0 {
		t.Fatalf("expected n1 score 0 (lower marginal), got %v", out2[r][n1])
	}
	if out2[r][n2] != 100 {
		t.Fatalf("expected n2 score 100 (higher marginal), got %v", out2[r][n2])
	}
}

func TestMigrationPlugin_DefaultPenalty(t *testing.T) {
	r := ReplicaKey{Namespace: "default", Name: "a", ReplicaIndex: 0}
	p := &MigrationPlugin{}
	out, err := p.ScoreMigration(PluginContext{Replicas: []ReplicaKey{r}})
	if err != nil {
		t.Fatalf("ScoreMigration error: %v", err)
	}
	if out[r] != 1 {
		t.Fatalf("expected default penalty 1, got %v", out[r])
	}
}
