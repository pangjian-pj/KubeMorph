package optimizer

import "testing"

func TestClusterNodeIDString(t *testing.T) {
	got := (ClusterNodeID{ClusterID: "c1", NodeName: "n1"}).String()
	if got != "c1/n1" {
		t.Fatalf("expected c1/n1, got %q", got)
	}
}

func TestReplicaKeyString(t *testing.T) {
	got := (ReplicaKey{Namespace: "ns", Name: "gd", ReplicaIndex: 3}).String()
	if got != "ns/gd/3" {
		t.Fatalf("expected ns/gd/3, got %q", got)
	}
}
