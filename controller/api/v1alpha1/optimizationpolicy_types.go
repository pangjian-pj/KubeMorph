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

// +groupName=core.kubex.io

// NOTE:
// This API is in M0 stage: it is intentionally minimal but fully functional for `kubectl apply` and schema validation.
// We will iterate fields/validation markers as later milestones (M1+) are implemented.

// OptimizationRunMode defines when the optimizer runs.
// +kubebuilder:validation:Enum=Once;Periodic
type OptimizationRunMode string

const (
	OptimizationRunModeOnce     OptimizationRunMode = "Once"
	OptimizationRunModePeriodic OptimizationRunMode = "Periodic"
)

// OptimizationStrategy defines whether a generated plan should be executed.
// +kubebuilder:validation:Enum=Preview;Conservative;Aggressive
type OptimizationStrategy string

const (
	OptimizationStrategyPreview      OptimizationStrategy = "Preview"
	OptimizationStrategyConservative OptimizationStrategy = "Conservative"
	OptimizationStrategyAggressive   OptimizationStrategy = "Aggressive"
)

// OptimizationGoal defines one weighted optimization objective.
// This largely follows `design_doc/optimization.md`.
type OptimizationGoal struct {
	// Type is the plugin name, e.g. Cost/Latency/Communication/Energy/Migration.
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`

	// Weight is in [0,1]. A weight of 0 means disabled.
	// (sum-to-1 validation is controller responsibility in M1)
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	Weight float64 `json:"weight"`

	// SourceCity is used by Latency goal.
	// +optional
	SourceCity string `json:"sourceCity,omitempty"`

	// TopologyRef references a ConfigMap name (typically in kubex-system) for Communication goal.
	// +optional
	TopologyRef string `json:"topologyRef,omitempty"`
}

// OptimizationPolicySpec defines the desired state of OptimizationPolicy.
type OptimizationPolicySpec struct {
	// Enabled indicates whether this policy is active. Only one policy may be active at a time (enforced by Lease in controller).
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// TargetSelector selects GlobalDeployments this policy applies to.
	// Empty selector means all.
	// +optional
	TargetSelector *metav1.LabelSelector `json:"targetSelector,omitempty"`

	// OptimizationGoals is a list of weighted objectives.
	// +optional
	OptimizationGoals []OptimizationGoal `json:"optimizationGoals,omitempty"`

	// RunMode determines whether optimizer runs once or periodically.
	// +kubebuilder:default:=Once
	RunMode OptimizationRunMode `json:"runMode,omitempty"`

	// RebalancePoint is required when RunMode is Periodic, e.g. "24h".
	// +optional
	RebalancePoint string `json:"rebalancePoint,omitempty"`

	// Strategy defines execution mode: Preview/Conservative/Aggressive.
	// +kubebuilder:default:=Preview
	Strategy OptimizationStrategy `json:"strategy,omitempty"`

	// ImprovementThresholdPercent is used when Strategy=Conservative.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	ImprovementThresholdPercent int32 `json:"improvementThresholdPercent,omitempty"`
}

// OptimizationPolicyPhase defines policy lifecycle phase.
// +kubebuilder:validation:Enum=Pending;Active;Failed;Disabled
type OptimizationPolicyPhase string

const (
	OptimizationPolicyPhasePending  OptimizationPolicyPhase = "Pending"
	OptimizationPolicyPhaseActive   OptimizationPolicyPhase = "Active"
	OptimizationPolicyPhaseFailed   OptimizationPolicyPhase = "Failed"
	OptimizationPolicyPhaseDisabled OptimizationPolicyPhase = "Disabled"
)

// LocalObjectRef identifies an object by name/namespace.
type LocalObjectRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
}

// CurrentReplicaLocation is the ground-truth (or best-effort) current placement for a replica.
// It is used to construct x_ij_current in later optimization modeling.
type CurrentReplicaLocation struct {
	// GlobalDeploymentRef identifies the GlobalDeployment this replica belongs to.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// ReplicaIndex is 0..replicas-1.
	// +kubebuilder:validation:Minimum=0
	ReplicaIndex int32 `json:"replicaIndex"`

	// ClusterId is the member cluster identifier.
	// +optional
	ClusterId string `json:"clusterId,omitempty"`

	// NodeName is the Kubernetes node name inside the member cluster.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// Source indicates where the location comes from: RBStatus or RBSpecFallback.
	// +optional
	Source string `json:"source,omitempty"`

	// Stable indicates whether this replica is considered stable and eligible for optimization.
	// +optional
	Stable bool `json:"stable,omitempty"`

	// UnstableReason is set when stable=false.
	// +optional
	UnstableReason string `json:"unstableReason,omitempty"`
}

// OptimizationPolicyStatus defines the observed state of OptimizationPolicy.
type OptimizationPolicyStatus struct {
	// Phase is the main lifecycle status.
	// +optional
	Phase OptimizationPolicyPhase `json:"phase,omitempty"`

	// ObservedDeployments is number of GlobalDeployments matched by targetSelector.
	// +optional
	ObservedDeployments int32 `json:"observedDeployments,omitempty"`

	// ObservedReplicas is number of ReplicaBindings (replicas) discovered under selected GlobalDeployments.
	// +optional
	ObservedReplicas int32 `json:"observedReplicas,omitempty"`

	// CurrentLayout is the collected current placement (x_ij_current) for later modeling.
	// It is a best-effort list; items with empty clusterId/nodeName indicate unknown placement.
	// +optional
	CurrentLayout []CurrentReplicaLocation `json:"currentLayout,omitempty"`

	// StableReplicas is number of replicas considered stable.
	// +optional
	StableReplicas int32 `json:"stableReplicas,omitempty"`

	// UnstableReplicas is number of replicas excluded from optimization.
	// +optional
	UnstableReplicas int32 `json:"unstableReplicas,omitempty"`

	// UnstableReasonsCount aggregates exclusion reasons to help debugging.
	// key is reason string, value is count.
	// +optional
	UnstableReasonsCount map[string]int32 `json:"unstableReasonsCount,omitempty"`

	// LastEvaluationTime records last optimizer run time.
	// +optional
	LastEvaluationTime metav1.Time `json:"lastEvaluationTime,omitzero"`

	// LatestPlanRef points to the most recent plan created.
	// +optional
	LatestPlanRef *LocalObjectRef `json:"latestPlanRef,omitempty"`

	// Conditions provide detailed diagnostics.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=op

// OptimizationPolicy is the Schema for the optimizationpolicies API.
type OptimizationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`

	Spec   OptimizationPolicySpec   `json:"spec,omitzero"`
	Status OptimizationPolicyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// OptimizationPolicyList contains a list of OptimizationPolicy.
type OptimizationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []OptimizationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OptimizationPolicy{}, &OptimizationPolicyList{})
}
