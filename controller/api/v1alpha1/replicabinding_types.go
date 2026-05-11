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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ReplicaBindingPhase represents the state machine of a single replica binding decision/execution.
// +kubebuilder:validation:Enum=Pending;Assigned;Applying;Running;Failed;Rescheduling;Deleting
type ReplicaBindingPhase string

const (
	ReplicaBindingPhasePending      ReplicaBindingPhase = "Pending"
	ReplicaBindingPhaseAssigned     ReplicaBindingPhase = "Assigned"
	ReplicaBindingPhaseApplying     ReplicaBindingPhase = "Applying"
	ReplicaBindingPhaseRunning      ReplicaBindingPhase = "Running"
	ReplicaBindingPhaseFailed       ReplicaBindingPhase = "Failed"
	ReplicaBindingPhaseRescheduling ReplicaBindingPhase = "Rescheduling"
	ReplicaBindingPhaseDeleting     ReplicaBindingPhase = "Deleting"
)

// NamespacedObjectRef identifies an object by name/namespace.
type NamespacedObjectRef struct {
	// Name is the name of the referenced object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace is the namespace of the referenced object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`
}

// ReplicaBindingSpec defines the desired state of ReplicaBinding.
type ReplicaBindingSpec struct {
	// GlobalDeploymentRef references the GlobalDeployment this binding belongs to.
	// +kubebuilder:validation:Required
	GlobalDeploymentRef NamespacedObjectRef `json:"globalDeploymentRef"`

	// ReplicaIndex is 0..replicas-1.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Required
	ReplicaIndex int32 `json:"replicaIndex"`

	// TargetCluster is the target member clusterId.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +optional
	TargetCluster string `json:"targetCluster,omitempty"`

	// TargetNodeName is the Kubernetes node name in the target member cluster.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	TargetNodeName string `json:"targetNodeName,omitempty"`

	// Reschedule indicates this replica should be migrated (rolling) to the target cluster/node.
	// When set to true, GlobalDeployment controller will perform rolling migration, then set it back to false.
	// +optional
	Reschedule bool `json:"reschedule,omitempty"`

	// RescheduleRequest is a traceable execution request token for rescheduling.
	// It MUST be unique for each reschedule attempt of the same ReplicaBinding (e.g. include planName/uuid).
	// Controllers MUST NOT reset/clear this field as a completion signal.
	// +optional
	RescheduleRequest string `json:"rescheduleRequest,omitempty"`
}

// RescheduleResult is the terminal result of handling a reschedule request.
// +kubebuilder:validation:Enum=Succeeded;Failed
type RescheduleResult string

const (
	RescheduleResultSucceeded RescheduleResult = "Succeeded"
	RescheduleResultFailed    RescheduleResult = "Failed"
)

// RescheduleObservedLocation records the ground-truth location observed from member cluster.
// This is for auditing/debugging and for PlanExecutor to decide completion.
type RescheduleObservedLocation struct {
	// ClusterId is the member cluster identifier.
	// +optional
	ClusterId string `json:"clusterId,omitempty"`

	// NodeName is the node name inside the member cluster.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// PodName is a representative member pod name for this replica (optional).
	// +optional
	PodName string `json:"podName,omitempty"`
}

// ReplicaBindingRescheduleStatus defines reschedule request watermarks and observed outcomes.
// It is NOT a separate phase state machine.
type ReplicaBindingRescheduleStatus struct {
	// LastHandledRequest is the last rescheduleRequest token that the executors have handled/confirmed.
	// +optional
	LastHandledRequest string `json:"lastHandledRequest,omitempty"`

	// LastHandledTime is the last time the request was handled.
	// +optional
	LastHandledTime metav1.Time `json:"lastHandledTime,omitzero"`

	// ObservedLocation is the ground-truth location after handling the request.
	// +optional
	ObservedLocation RescheduleObservedLocation `json:"observedLocation,omitzero"`

	// LastResult is the terminal result for LastHandledRequest.
	// +optional
	LastResult RescheduleResult `json:"lastResult,omitempty"`

	// Message is a human-readable message for the last handled request.
	// +optional
	Message string `json:"message,omitempty"`

	// LastError carries error details if LastResult=Failed.
	// +optional
	LastError string `json:"lastError,omitempty"`
}

// ReplicaBindingStatus defines the observed state of ReplicaBinding.
type ReplicaBindingStatus struct {
	// Phase indicates current phase.
	// +optional
	Phase ReplicaBindingPhase `json:"phase,omitempty"`

	// LastError records the last error message.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// LastTransitionTime is set when phase changes.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitzero"`

	// NodeName is the actual node where the member pod is running (ground truth).
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// ClusterName is the display name of the member cluster, used by UI.
	// +optional
	ClusterName string `json:"clusterName,omitempty"`

	// Reschedule records reschedule execution watermark + observed outcomes.
	// +optional
	Reschedule ReplicaBindingRescheduleStatus `json:"reschedule,omitzero"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ReplicaBinding is the Schema for the replicabindings API.
type ReplicaBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`

	Spec   ReplicaBindingSpec   `json:"spec"`
	Status ReplicaBindingStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ReplicaBindingList contains a list of ReplicaBinding.
type ReplicaBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ReplicaBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ReplicaBinding{}, &ReplicaBindingList{})
}
