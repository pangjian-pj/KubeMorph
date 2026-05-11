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
// We will iterate fields/validation markers as later milestones (M3+ / M4+) are implemented.

// PlanPhase defines lifecycle phase of a ReOrchestrationPlan.
// +kubebuilder:validation:Enum=Pending;Executing;Succeeded;Failed;PartiallyFailed;Terminating
type PlanPhase string

const (
	PlanPhasePending         PlanPhase = "Pending"
	PlanPhaseExecuting       PlanPhase = "Executing"
	PlanPhaseSucceeded       PlanPhase = "Succeeded"
	PlanPhaseFailed          PlanPhase = "Failed"
	PlanPhasePartiallyFailed PlanPhase = "PartiallyFailed"
	PlanPhaseTerminating     PlanPhase = "Terminating"
)

// MoveLocation identifies a (cluster, node) location.
type MoveLocation struct {
	// ClusterID is the member cluster identifier.
	// +kubebuilder:validation:MinLength=1
	ClusterID string `json:"clusterId"`

	// ClusterName is the display name of the member cluster, used by UI.
	// +optional
	ClusterName string `json:"clusterName,omitempty"`

	// NodeName is the Kubernetes node name inside that member cluster.
	// +kubebuilder:validation:MinLength=1
	NodeName string `json:"nodeName"`
}

// PlanSummary is a minimal summary of a plan.
type PlanSummary struct {
	// PolicyName is the OptimizationPolicy name that generated this plan.
	// +kubebuilder:validation:MinLength=1
	PolicyName string `json:"policyName"`

	// GoalScores provides per-goal score breakdown for better explainability.
	//
	// Key is goal type, e.g. Cost/Latency/Communication/Energy/Migration.
	// Values follow the same convention as total scores: lower is better.
	// +optional
	GoalScores map[string]GoalScoreSummary `json:"goalScores,omitempty"`

	// EstimatedImprovementScore is a high-level improvement score.
	// +optional
	EstimatedImprovementScore *float64 `json:"estimatedImprovementScore,omitempty"`

	// PodsToMove is the number of replicas that need to move.
	// +optional
	PodsToMove *int32 `json:"podsToMove,omitempty"`

	// CurrentScore is the total score of the current placement.
	// +optional
	CurrentScore *float64 `json:"currentScore,omitempty"`

	// ExpectedScore is the total score of the expected placement.
	// +optional
	ExpectedScore *float64 `json:"expectedScore,omitempty"`
}

// GoalScoreSummary describes current/expected score for one goal.
type GoalScoreSummary struct {
	// Weight is the goal weight used in OptimizationPolicy.
	// +optional
	Weight *float64 `json:"weight,omitempty"`

	// CurrentScore is the score of current placement for this goal.
	// +optional
	CurrentScore *float64 `json:"currentScore,omitempty"`

	// ExpectedScore is the score of expected placement for this goal.
	// +optional
	ExpectedScore *float64 `json:"expectedScore,omitempty"`

	// EstimatedImprovementScore is (currentScore - expectedScore), clamped to >= 0.
	// +optional
	EstimatedImprovementScore *float64 `json:"estimatedImprovementScore,omitempty"`

	// HumanReadable provides optional, goal-specific readable metrics in original units.
	// For example:
	// - Cost: totalCostUsdFrom/To
	// - Latency/Communication: avgLatencyMsFrom/To
	// - Energy: totalPowerWFrom/To
	// - Migration: movesFrom/To
	//
	// +optional
	HumanReadable *GoalHumanReadableSummary `json:"humanReadable,omitempty"`
}

// GoalHumanReadableSummary provides goal-specific metrics in original units.
//
// Notes:
// - Only a subset of fields will be filled depending on goal type.
// - For all goals, lower is better.
type GoalHumanReadableSummary struct {
	// Kind is the goal type, e.g. Cost/Latency/Communication/Energy/Migration.
	// +optional
	Kind string `json:"kind,omitempty"`

	// Unit is a human-readable unit string, e.g. "USD", "ms", "W", "moves".
	// +optional
	Unit string `json:"unit,omitempty"`

	// From is the current placement metric value.
	// +optional
	From *float64 `json:"from,omitempty"`

	// To is the expected placement metric value.
	// +optional
	To *float64 `json:"to,omitempty"`

	// Delta is (From - To). Positive means improvement.
	// +optional
	Delta *float64 `json:"delta,omitempty"`

	// Detail contains optional extra info for UI/debug.
	// +optional
	Detail map[string]string `json:"detail,omitempty"`
}

// PlanMove defines a single replica move from source to destination.
type PlanMove struct {
	// GlobalDeploymentRef identifies the target GlobalDeployment.
	// +kubebuilder:validation:Required
	GlobalDeploymentRef NamespacedObjectRef `json:"globalDeploymentRef"`

	// ReplicaIndex identifies a replica within the GlobalDeployment.
	// +kubebuilder:validation:Minimum=0
	ReplicaIndex int32 `json:"replicaIndex"`

	// Source location.
	// +kubebuilder:validation:Required
	Source MoveLocation `json:"source"`

	// Destination location.
	// +kubebuilder:validation:Required
	Destination MoveLocation `json:"destination"`
}

// ReOrchestrationPlanSpec defines the desired state of ReOrchestrationPlan.
type ReOrchestrationPlanSpec struct {
	// Summary is plan summary.
	// +kubebuilder:validation:Required
	Summary PlanSummary `json:"summary"`

	// Moves is the list of replica moves.
	// +kubebuilder:validation:Optional
	Moves []PlanMove `json:"moves,omitempty"`
}

// MoveExecutionStatus is per-move execution status.
// +kubebuilder:validation:Enum=Pending;InProgress;Succeeded;Failed
type MoveExecutionStatus string

const (
	MoveExecutionStatusPending    MoveExecutionStatus = "Pending"
	MoveExecutionStatusInProgress MoveExecutionStatus = "InProgress"
	MoveExecutionStatusSucceeded  MoveExecutionStatus = "Succeeded"
	MoveExecutionStatusFailed     MoveExecutionStatus = "Failed"
)

// MoveStatus records execution progress for a move.
type MoveStatus struct {
	// GlobalDeploymentRef identifies the target GlobalDeployment.
	// +kubebuilder:validation:Required
	GlobalDeploymentRef NamespacedObjectRef `json:"globalDeploymentRef"`

	// ReplicaIndex identifies a replica within the GlobalDeployment.
	// +kubebuilder:validation:Minimum=0
	ReplicaIndex int32 `json:"replicaIndex"`

	// Status is the move status.
	// +optional
	Status MoveExecutionStatus `json:"status,omitempty"`

	// Message contains human-readable error / progress message.
	// +optional
	Message string `json:"message,omitempty"`
}

// PlanExecutionSummary is aggregated execution status.
type PlanExecutionSummary struct {
	// TotalMoves is the total number of moves.
	// +optional
	TotalMoves int32 `json:"totalMoves,omitempty"`
	// SucceededMoves is the number of succeeded moves.
	// +optional
	SucceededMoves int32 `json:"succeededMoves,omitempty"`
	// FailedMoves is the number of failed moves.
	// +optional
	FailedMoves int32 `json:"failedMoves,omitempty"`
	// PendingMoves is the number of pending moves.
	// +optional
	PendingMoves int32 `json:"pendingMoves,omitempty"`
}

// ReOrchestrationPlanStatus defines the observed state of ReOrchestrationPlan.
type ReOrchestrationPlanStatus struct {
	// Phase is the main lifecycle status.
	// +optional
	Phase PlanPhase `json:"phase,omitempty"`

	// Summary is execution summary.
	// +optional
	Summary PlanExecutionSummary `json:"summary,omitempty"`

	// StartTime records when execution starts.
	// +optional
	StartTime metav1.Time `json:"startTime,omitzero"`

	// CompletionTime records when execution completes.
	// +optional
	CompletionTime metav1.Time `json:"completionTime,omitzero"`

	// MoveStatuses records per-move execution state.
	// +optional
	MoveStatuses []MoveStatus `json:"moveStatuses,omitempty"`

	// Conditions provide detailed diagnostics.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=rop

// ReOrchestrationPlan is the Schema for the reorchestrationplans API.
type ReOrchestrationPlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`

	Spec   ReOrchestrationPlanSpec   `json:"spec,omitzero"`
	Status ReOrchestrationPlanStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ReOrchestrationPlanList contains a list of ReOrchestrationPlan.
type ReOrchestrationPlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ReOrchestrationPlan `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ReOrchestrationPlan{}, &ReOrchestrationPlanList{})
}
