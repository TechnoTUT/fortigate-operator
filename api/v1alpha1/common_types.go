package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeletionPolicy says what happens to the FortiGate object when the Kubernetes resource is deleted.
// +kubebuilder:validation:Enum=Delete;Orphan
type DeletionPolicy string

const (
	// DeletionPolicyDelete deletes the FortiGate object.
	DeletionPolicyDelete DeletionPolicy = "Delete"
	// DeletionPolicyOrphan leaves the FortiGate object in place.
	DeletionPolicyOrphan DeletionPolicy = "Orphan"
)

// Condition types and reasons.
const (
	// ConditionReady is true when the FortiGate object matches the spec.
	ConditionReady = "Ready"

	ReasonSynced     = "Synced"
	ReasonDryRun     = "DryRun"
	ReasonConflict   = "Conflict"
	ReasonAPIError   = "FortiGateError"
	ReasonInvalid    = "InvalidSpec"
	ReasonNotFound   = "ReferenceNotFound"
	ReasonDeleteFail = "DeleteFailed"
	// ReasonNotConverged means the FortiGate stored values different from those written.
	ReasonNotConverged = "NotConverged"
)

// AdoptAnnotation, when set to "true", lets the controller take over an existing FortiGate
// object with the same name that it did not create.
const AdoptAnnotation = "fortigate.technotut.net/adopt"

// CommonSpec holds the fields shared by every kind.
type CommonSpec struct {
	// Name of the object on the FortiGate. Defaults to metadata.name.
	// It cannot be changed after creation.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="name is immutable"
	Name string `json:"name,omitempty"`

	// DeletionPolicy says what happens to the FortiGate object when this resource is deleted.
	// +optional
	// +kubebuilder:default=Delete
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// ObjectStatus is the status shared by every kind.
type ObjectStatus struct {
	// Name of the object on the FortiGate.
	// +optional
	Name string `json:"name,omitempty"`

	// ObservedGeneration is the generation that was last applied.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncTime is when the FortiGate object was last checked against the spec.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// Conditions. The Ready condition is true when the FortiGate object matches the spec.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
