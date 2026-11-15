// Package v1alpha1 contains the API schema definitions for the harbor v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=harbor.soliddowant.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "harbor.soliddowant.dev", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme. Each type registers itself in an init
	// function.
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		metav1.AddToGroupVersion(s, GroupVersion)
		s.AddKnownTypes(GroupVersion,
			&HarborConnection{}, &HarborConnectionList{},
			&HarborProject{}, &HarborProjectList{},
			&HarborProjectRobot{}, &HarborProjectRobotList{},
			&HarborRegistry{}, &HarborRegistryList{},
			&HarborRetentionPolicy{}, &HarborRetentionPolicyList{},
			&HarborImmutabilityPolicy{}, &HarborImmutabilityPolicyList{},
			&HarborSystemRobot{}, &HarborSystemRobotList{},
		)

		return nil
	})

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
