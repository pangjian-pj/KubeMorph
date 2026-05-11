package optimizer

// ClusterNodeID uniquely identifies a node across member clusters.
//
// It follows the design doc idea: GlobalNodeId = {clusterId}/{nodeName}.
type ClusterNodeID struct {
	ClusterID string
	NodeName  string
}

func (id ClusterNodeID) String() string {
	if id.ClusterID == "" {
		return id.NodeName
	}
	if id.NodeName == "" {
		return id.ClusterID
	}
	return id.ClusterID + "/" + id.NodeName
}

// ResourceQuantity is an intentionally minimal resource model for M0.
// M2 will likely expand to use k8s resource.Quantity; until then we keep this package dependency-light.
type ResourceQuantity struct {
	MilliCPU int64
	MemoryMi int64
}

// ReplicaKey uniquely identifies a replica (GlobalDeployment + replicaIndex).
// This becomes the i dimension in x_ij.
type ReplicaKey struct {
	Namespace    string
	Name         string
	ReplicaIndex int32
}

func (k ReplicaKey) String() string {
	return k.Namespace + "/" + k.Name + "/" + itoa32(k.ReplicaIndex)
}

func itoa32(v int32) string {
	// avoid pulling strconv in hot paths; M0 simplicity.
	// (still readable/cheap here)
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := [12]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
