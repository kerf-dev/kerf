// Package v1alpha1 contains the API types of the resource-optimizer.oz.io group.
//
// This package is a contract (WO-00). Changing it requires a decision in DECISIONS.md.
// It depends only on k8s.io/api and k8s.io/apimachinery so that every component can import it.
//
// +kubebuilder:object:generate=true
// +groupName=resource-optimizer.oz.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "resource-optimizer.oz.io", Version: "v1alpha1"}

	// SchemeBuilder registers the types of this group-version.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&RightsizingPolicy{}, &RightsizingPolicyList{},
		&RightsizingRecommendation{}, &RightsizingRecommendationList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
