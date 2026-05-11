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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// GlobalDeploymentPhase represents a coarse-grained phase for global deployment.
// +kubebuilder:validation:Enum=Pending;Progressing;Running;Degraded;Scaling;Deleting;Failed
type GlobalDeploymentPhase string

const (
	GlobalDeploymentPhasePending     GlobalDeploymentPhase = "Pending"
	GlobalDeploymentPhaseProgressing GlobalDeploymentPhase = "Progressing"
	GlobalDeploymentPhaseRunning     GlobalDeploymentPhase = "Running"
	GlobalDeploymentPhaseDegraded    GlobalDeploymentPhase = "Degraded"
	GlobalDeploymentPhaseScaling     GlobalDeploymentPhase = "Scaling"
	GlobalDeploymentPhaseDeleting    GlobalDeploymentPhase = "Deleting"
	GlobalDeploymentPhaseFailed      GlobalDeploymentPhase = "Failed"
)

// GlobalDeploymentSpec defines the desired state of GlobalDeployment.
type GlobalDeploymentSpec struct {
	// Replicas is the total replicas desired globally.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Template is the user-provided native Kubernetes Deployment template.
	// Convention: we treat this as a template and will create per-replica (replicas=1)
	// member deployments named: <globaldeploy-name>-r<replicaIndex>.
	//
	// Why RawExtension?
	// - Encoding appsv1.DeploymentSpec directly makes controller-gen inline a huge OpenAPI schema,
	//   which can exceed apiserver's metadata.annotations size limit when installing the CRD.
	// - We only need a subset of fields at runtime (pod template requests, etc.).
	// +kubebuilder:validation:Required
	// +kubebuilder:pruning:PreserveUnknownFields
	Template runtime.RawExtension `json:"template"`
}

// GlobalDeploymentStatus defines the observed state of GlobalDeployment.
type GlobalDeploymentStatus struct {
	// Phase is the aggregated phase computed from ReplicaBindings.
	// +optional
	Phase GlobalDeploymentPhase `json:"phase,omitempty"`

	// Running is the number of replicas in Running phase.
	// +optional
	Running int32 `json:"running,omitempty"`

	// Failed is the number of replicas in Failed phase.
	// +optional
	Failed int32 `json:"failed,omitempty"`

	// Pending is the number of replicas still pending.
	// +optional
	Pending int32 `json:"pending,omitempty"`

	// UpdatedAt is the last time the controller updated status.
	// +optional
	UpdatedAt metav1.Time `json:"updatedAt,omitzero"`

	// ObservedRevision is the global rolling revision maintained by GlobalDeployment controller.
	// It is used to differentiate old/new replicas during rolling migration.
	// +optional
	ObservedRevision string `json:"observedRevision,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:Optional

// GlobalDeployment is the Schema for the globaldeployments API.
type GlobalDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`

	Spec   GlobalDeploymentSpec   `json:"spec"`
	Status GlobalDeploymentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GlobalDeploymentList contains a list of GlobalDeployment.
type GlobalDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GlobalDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GlobalDeployment{}, &GlobalDeploymentList{})
}
