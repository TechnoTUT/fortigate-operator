package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FirewallServiceSpec is a custom service (config firewall service custom) for TCP, UDP or SCTP.
// +kubebuilder:validation:XValidation:rule="has(self.tcpPortrange) || has(self.udpPortrange) || has(self.sctpPortrange)",message="set at least one of tcpPortrange, udpPortrange or sctpPortrange"
type FirewallServiceSpec struct {
	CommonSpec `json:",inline"`

	// Destination port ranges, each in the FortiOS form dst[-dst][:src[-src]], such as 1935, 8000-8100 or 53:1024-65535.
	// +optional
	TCPPortrange []string `json:"tcpPortrange,omitempty"`
	// +optional
	UDPPortrange []string `json:"udpPortrange,omitempty"`
	// +optional
	SCTPPortrange []string `json:"sctpPortrange,omitempty"`

	// Service category shown in the GUI. Empty leaves it uncategorized.
	// +optional
	Category string `json:"category,omitempty"`

	// Free-form comment. The controller prefixes it with an ownership marker.
	// +optional
	// +kubebuilder:validation:MaxLength=200
	Comment string `json:"comment,omitempty"`
}

// FirewallService is a FortiGate custom service.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fgsvc,categories=fortigate
// +kubebuilder:printcolumn:name="FortiGate name",type=string,JSONPath=`.status.name`
// +kubebuilder:printcolumn:name="TCP",type=string,JSONPath=`.spec.tcpPortrange`
// +kubebuilder:printcolumn:name="UDP",type=string,JSONPath=`.spec.udpPortrange`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type FirewallService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirewallServiceSpec `json:"spec"`
	Status ObjectStatus        `json:"status,omitempty"`
}

// FirewallServiceList is a list of FirewallService.
// +kubebuilder:object:root=true
type FirewallServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirewallService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirewallService{}, &FirewallServiceList{})
}
