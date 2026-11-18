package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FirewallPolicySpec is an IPv4 firewall policy (config firewall policy).
// Field names and values follow the FortiOS CLI.
type FirewallPolicySpec struct {
	CommonSpec `json:",inline"`

	// Incoming interfaces or zones.
	// +kubebuilder:validation:MinItems=1
	SrcIntf []string `json:"srcintf"`

	// Outgoing interfaces or zones.
	// +kubebuilder:validation:MinItems=1
	DstIntf []string `json:"dstintf"`

	// Source address objects or groups.
	// +kubebuilder:validation:MinItems=1
	SrcAddr []string `json:"srcaddr"`

	// Destination address objects or groups.
	// +kubebuilder:validation:MinItems=1
	DstAddr []string `json:"dstaddr"`

	// Service objects or groups.
	// +kubebuilder:validation:MinItems=1
	Service []string `json:"service"`

	// +optional
	// +kubebuilder:validation:Enum=accept;deny
	// +kubebuilder:default=accept
	Action string `json:"action,omitempty"`

	// +optional
	// +kubebuilder:default=always
	Schedule string `json:"schedule,omitempty"`

	// Source NAT with the outgoing interface address.
	// +optional
	// +kubebuilder:validation:Enum=enable;disable
	// +kubebuilder:default=disable
	NAT string `json:"nat,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=enable;disable
	// +kubebuilder:default=enable
	Status string `json:"status,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=all;utm;disable
	// +kubebuilder:default=utm
	LogTraffic string `json:"logtraffic,omitempty"`

	// Free-form comment. The controller prefixes it with an ownership marker.
	// +optional
	// +kubebuilder:validation:MaxLength=900
	Comments string `json:"comments,omitempty"`

	// Position keeps the policy above or below another policy. Without it, a new policy
	// is added at the end of the list and is not moved afterwards.
	// +optional
	Position *PolicyPosition `json:"position,omitempty"`
}

// PolicyPosition places a policy relative to another policy, given by its FortiGate name.
// The policy is moved only when it is on the wrong side of that policy.
// +kubebuilder:validation:XValidation:rule="has(self.before) != has(self.after)",message="set exactly one of before or after"
type PolicyPosition struct {
	// +optional
	// +kubebuilder:validation:MinLength=1
	Before string `json:"before,omitempty"`
	// +optional
	// +kubebuilder:validation:MinLength=1
	After string `json:"after,omitempty"`
}

// FirewallPolicyStatus is the observed state of a FirewallPolicy.
type FirewallPolicyStatus struct {
	ObjectStatus `json:",inline"`

	// PolicyID on the FortiGate.
	// +optional
	PolicyID int `json:"policyID,omitempty"`
}

// FirewallPolicy is a FortiGate IPv4 firewall policy.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fgpol,categories=fortigate
// +kubebuilder:printcolumn:name="FortiGate name",type=string,JSONPath=`.status.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.policyID`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type FirewallPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirewallPolicySpec   `json:"spec"`
	Status FirewallPolicyStatus `json:"status,omitempty"`
}

// FirewallPolicyList is a list of FirewallPolicy.
// +kubebuilder:object:root=true
type FirewallPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirewallPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirewallPolicy{}, &FirewallPolicyList{})
}
