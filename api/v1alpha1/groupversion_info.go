// Package v1alpha1 contains the API types of the fortigate.technotut.net group.
// +kubebuilder:object:generate=true
// +groupName=fortigate.technotut.net
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group and version of the API.
	GroupVersion = schema.GroupVersion{Group: "fortigate.technotut.net", Version: "v1alpha1"}

	// SchemeBuilder registers the types with a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types of this group to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
