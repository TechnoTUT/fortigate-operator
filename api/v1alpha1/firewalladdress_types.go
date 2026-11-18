package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FirewallAddressSpec is an IPv4 address object (config firewall address).
// +kubebuilder:validation:XValidation:rule="self.type != 'ipmask' || (has(self.subnet) != has(self.serviceRef))",message="set exactly one of subnet or serviceRef for type ipmask"
// +kubebuilder:validation:XValidation:rule="!has(self.serviceRef) || self.type == 'ipmask'",message="serviceRef can only be used with type ipmask"
// +kubebuilder:validation:XValidation:rule="self.type != 'iprange' || (has(self.startIP) && has(self.endIP))",message="startIP and endIP are required for type iprange"
// +kubebuilder:validation:XValidation:rule="self.type != 'fqdn' || has(self.fqdn)",message="fqdn is required for type fqdn"
type FirewallAddressSpec struct {
	CommonSpec `json:",inline"`

	// +optional
	// +kubebuilder:validation:Enum=ipmask;iprange;fqdn
	// +kubebuilder:default=ipmask
	Type string `json:"type,omitempty"`

	// Subnet in CIDR notation, such as 10.12.0.0/16 or 10.12.1.5/32. Used with type ipmask.
	// +optional
	Subnet string `json:"subnet,omitempty"`

	// ServiceRef takes the address from a Service of type LoadBalancer in the same namespace.
	// The address object holds the Service's IPv4 load balancer address as a /32 and follows it
	// when it changes. Used with type ipmask, instead of subnet.
	// +optional
	ServiceRef *ServiceReference `json:"serviceRef,omitempty"`

	// First address of the range. Used with type iprange.
	// +optional
	StartIP string `json:"startIP,omitempty"`

	// Last address of the range. Used with type iprange.
	// +optional
	EndIP string `json:"endIP,omitempty"`

	// Fully qualified domain name, such as example.com or *.example.com. Used with type fqdn.
	// +optional
	FQDN string `json:"fqdn,omitempty"`

	// Interface the address is bound to. Empty means any interface.
	// +optional
	AssociatedInterface string `json:"associatedInterface,omitempty"`

	// Free-form comment. The controller prefixes it with an ownership marker.
	// +optional
	// +kubebuilder:validation:MaxLength=200
	Comment string `json:"comment,omitempty"`
}

// ServiceReference names a Service in the same namespace as the referring resource.
type ServiceReference struct {
	// Name of the Service.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// FirewallAddressStatus is the observed state of a FirewallAddress.
type FirewallAddressStatus struct {
	ObjectStatus `json:",inline"`

	// Address is the subnet last resolved from spec.subnet or spec.serviceRef, in CIDR notation.
	// +optional
	Address string `json:"address,omitempty"`
}

// FirewallAddress is a FortiGate IPv4 address object.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fgaddr,categories=fortigate
// +kubebuilder:printcolumn:name="FortiGate name",type=string,JSONPath=`.status.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Service",type=string,JSONPath=`.spec.serviceRef.name`,priority=1
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.address`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type FirewallAddress struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirewallAddressSpec   `json:"spec"`
	Status FirewallAddressStatus `json:"status,omitempty"`
}

// FirewallAddressList is a list of FirewallAddress.
// +kubebuilder:object:root=true
type FirewallAddressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirewallAddress `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirewallAddress{}, &FirewallAddressList{})
}
