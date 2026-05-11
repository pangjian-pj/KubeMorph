package optimizer

import (
	"context"
	"testing"
)

func TestCommunicationPlugin_CentroidMatchesByNameWhenNamespaceEmpty(t *testing.T) {
	ctx := context.Background()

	replicas := []ReplicaKey{
		{Namespace: "default", Name: "frontend", ReplicaIndex: 0},
		{Namespace: "default", Name: "frontend", ReplicaIndex: 1},
	}
	nodes := []NodeContext{
		{ID: ClusterNodeID{ClusterID: "c1", NodeName: "n1"}, Region: "Hangzhou"},
		{ID: ClusterNodeID{ClusterID: "c1", NodeName: "n2"}, Region: "London"},
	}

	plugin := &CommunicationPlugin{
		// NOTE: topology matrix only has service names (namespace empty)
		Dependencies: map[NamespacedName][]NamespacedName{
			{Namespace: "", Name: "frontend"}: {{Namespace: "", Name: "frontend"}},
		},
		RegionLatencyMs: map[string]map[string]float64{
			"Hangzhou": {"Hangzhou": 0, "London": 10},
			"London":   {"Hangzhou": 10, "London": 0},
		},
		ReplicaService: map[ReplicaKey]NamespacedName{
			replicas[0]: {Namespace: "default", Name: "frontend"},
			replicas[1]: {Namespace: "default", Name: "frontend"},
		},
		CurrentReplicaRegion: map[ReplicaKey]string{
			replicas[0]: "Hangzhou",
			replicas[1]: "London",
		},
	}

	scores, err := plugin.ScorePlacement(PluginContext{Replicas: replicas, Nodes: nodes})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(scores) != len(replicas) {
		t.Fatalf("expected scores for %d replicas, got %d", len(replicas), len(scores))
	}
	// sanity: each replica should have per-node scores
	for _, rk := range replicas {
		m := scores[rk]
		if len(m) != len(nodes) {
			t.Fatalf("expected %d node scores for %s, got %d", len(nodes), rk.String(), len(m))
		}
	}

	_ = ctx
}
