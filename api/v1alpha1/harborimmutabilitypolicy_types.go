package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ImmutabilityRule is one tag immutability rule.
type ImmutabilityRule struct {
	// +optional
	Disabled     bool                    `json:"disabled,omitempty"`
	Repositories RepositorySelector      `json:"repositories"`
	Tags         ImmutabilityTagSelector `json:"tags"`
}

// HarborImmutabilityPolicySpec is the desired tag immutability rule set of one project.
// +kubebuilder:validation:XValidation:rule="self.rules.all(r, self.rules.exists_one(o, o.repositories == r.repositories && o.tags == r.tags))",message="each rule must have a different repositories and tags selector pair"
type HarborImmutabilityPolicySpec struct {
	// ProjectRef names the HarborProject. Its allowedNamespaces must admit this namespace. Immutable.
	ProjectRef ObjectReference `json:"projectRef"`
	// +kubebuilder:validation:MaxItems=15
	// +listType=atomic
	Rules []ImmutabilityRule `json:"rules"`
	// DeletionPolicy empty means the connection's default.
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
	// ResyncInterval empty means the connection's default.
	// +optional
	ResyncInterval *metav1.Duration `json:"resyncInterval,omitempty"`
}

// HarborImmutabilityPolicyStatus is the observed state of a HarborImmutabilityPolicy.
type HarborImmutabilityPolicyStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ProjectID in Harbor. Set only while this resource owns the project's immutability rules.
	// +optional
	ProjectID int32 `json:"projectID,omitempty"`
	// HarborKey is the Harbor identity, object type and project name this resource claims.
	// +optional
	HarborKey string `json:"harborKey,omitempty"`
	// ObservedGeneration is the last generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.spec.projectRef.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.spec.projectRef == oldSelf.spec.projectRef",message="spec.projectRef is immutable"

// HarborImmutabilityPolicy is the tag immutability rule set of a project.
type HarborImmutabilityPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HarborImmutabilityPolicySpec   `json:"spec"`
	Status HarborImmutabilityPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HarborImmutabilityPolicyList is a list of HarborImmutabilityPolicy.
type HarborImmutabilityPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HarborImmutabilityPolicy `json:"items"`
}
