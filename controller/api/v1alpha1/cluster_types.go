/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ClusterPhase represents a coarse-grained state machine phase for cluster importing/health.
// +kubebuilder:validation:Enum=Importing;Ready;NotReady;Failed
type ClusterPhase string

const (
	ClusterPhaseImporting ClusterPhase = "Importing"
	ClusterPhaseReady     ClusterPhase = "Ready"
	ClusterPhaseNotReady  ClusterPhase = "NotReady"
	ClusterPhaseFailed    ClusterPhase = "Failed"
)

const (
	// ConditionTypeReady indicates whether the cluster is reachable and usable.
	ConditionTypeReady = "Ready"
	// ConditionTypeImported indicates import process completion.
	ConditionTypeImported = "Imported"
)

// ClusterSpec defines the desired state of Cluster.
type ClusterSpec struct {
	// APIEndpoint is the API endpoint of the member cluster.
	// This can be a hostname, hostname:port, IP or IP:port.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Required
	APIEndpoint string `json:"apiEndpoint"`

	// SecretRef is the name of the Secret containing credentials required to access the member cluster.
	// The Secret needs to exist in the fed system namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Required
	SecretRef string `json:"secretRef"`
}

type ResourceList struct {
	// CPU is the total CPU cores of the cluster.
	// Serialized as a string, e.g. "32".
	// +optional
	CPU resource.Quantity `json:"cpu,omitempty"`

	// Memory is the total memory of the cluster.
	// Serialized as a string, e.g. "128Gi".
	// +optional
	Memory resource.Quantity `json:"memory,omitempty"`
}

type Resources struct {
	// Resources describes the cluster's resources.
	// +optional
	Capacity ResourceList `json:"capacity,omitempty"`
	// +optional
	Allocatable ResourceList `json:"allocatable,omitempty"`

	// NodeCount is the number of Nodes in the member cluster.
	// +optional
	NodeCount int64 `json:"nodeCount,omitempty"`

	// PodCount is the number of Pods (all namespaces) in the member cluster.
	// +optional
	PodCount int64 `json:"podCount,omitempty"`
}

// NodeResources describes allocatable/requested/free resources on a node.
type NodeResources struct {
	// CPU is CPU cores (or millicores) as a k8s quantity.
	// +optional
	CPU resource.Quantity `json:"cpu,omitempty"`

	// Memory is memory as a k8s quantity.
	// +optional
	Memory resource.Quantity `json:"memory,omitempty"`

	// Pods is the number of pods allocatable/free on this node.
	// +optional
	Pods resource.Quantity `json:"pods,omitempty"`
}

// NodeSummary is a lightweight snapshot of a member cluster node.
type NodeSummary struct {
	// Name is the node name.
	// +optional
	Name string `json:"name,omitempty"`

	// UID is the Kubernetes UID of the node.
	// +optional
	UID string `json:"uid,omitempty"`

	// Ready indicates whether the node is Ready.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// Allocatable is node allocatable resources.
	// +optional
	Allocatable NodeResources `json:"allocatable,omitempty"`

	// Requested is requested resources summed from pod spec requests on this node.
	// +optional
	Requested NodeResources `json:"requested,omitempty"`

	// Free = allocatable - requested (for cpu/memory/pods).
	// +optional
	Free NodeResources `json:"free,omitempty"`

	// Labels is a subset/all node labels.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// ClusterCondition is a wrapper around metav1.Condition for future extension.
// We keep it as a dedicated type so the API can evolve without changing the schema style.
type ClusterCondition metav1.Condition

// ClusterStatus defines the observed state of Cluster.
type ClusterStatus struct {
	// Phase is a coarse-grained state machine phase: Importing/Ready/NotReady/Failed.
	// +optional
	Phase ClusterPhase `json:"phase,omitempty"`

	// UpdatedAt is the last time the controller updated the status.
	// +optional
	UpdatedAt metav1.Time `json:"updatedAt,omitzero"`

	// LastProbeTime is the timestamp of the last successful probe/sync.
	// +optional
	LastProbeTime metav1.Time `json:"lastProbeTime,omitzero"`

	// Conditions is an array of current cluster conditions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Resources describes the cluster's resources.
	// +optional
	Resources Resources `json:"resources,omitempty"`

	// Nodes is a summary list for each node in the member cluster.
	// +optional
	Nodes []NodeSummary `json:"nodes,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Cluster is the Schema for the clusters API
type Cluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Cluster
	// +required
	Spec ClusterSpec `json:"spec"`

	// status defines the observed state of Cluster
	// +optional
	Status ClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ClusterList contains a list of Cluster
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Cluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Cluster{}, &ClusterList{})
}
