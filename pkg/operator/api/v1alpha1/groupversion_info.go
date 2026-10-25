// Package v1alpha1 contains the PredictiveScaler API of kubeforecast-operator.
// +kubebuilder:object:generate=true
// +groupName=autoscaling.kubeforecast.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API group and version. The group is provisional until the project domain is decided.
	GroupVersion = schema.GroupVersion{Group: "autoscaling.kubeforecast.io", Version: "v1alpha1"}

	// SchemeBuilder registers the types with a runtime scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
