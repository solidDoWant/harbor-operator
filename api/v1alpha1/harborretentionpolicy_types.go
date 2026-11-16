package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RetentionRule is one tag retention rule.
// +kubebuilder:validation:XValidation:rule="self.template == 'always' ? !has(self.count) : (has(self.count) && self.count > 0)",message="count is required and must be greater than 0, except for the always template, which takes no count"
type RetentionRule struct {
	// +optional
	Disabled bool `json:"disabled,omitempty"`
	// Template is the retention template.
	// +kubebuilder:validation:Enum=latestPushedK;latestPulledN;nDaysSinceLastPush;nDaysSinceLastPull;always
	Template string `json:"template"`
	// Count is K, N or the number of days, depending on the template.
	// +optional
	Count        *int32             `json:"count,omitempty"`
	Repositories RepositorySelector `json:"repositories"`
	Tags         TagSelector        `json:"tags"`
}

// HarborRetentionPolicySpec is the desired tag retention policy of one project.
type HarborRetentionPolicySpec struct {
	// ProjectRef names the HarborProject. Its allowedNamespaces must admit this namespace. Immutable.
	ProjectRef ObjectReference `json:"projectRef"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=15
	// +listType=atomic
	Rules []RetentionRule `json:"rules"`
	// Schedule is a Harbor cron expression with six fields (seconds first). Empty means manual runs only.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	Schedule string `json:"schedule,omitempty"`
	// DeletionPolicy empty means the connection's default.
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
	// ResyncInterval empty means the connection's default.
	// +optional
	ResyncInterval *metav1.Duration `json:"resyncInterval,omitempty"`
}

// HarborRetentionPolicyStatus is the observed state of a HarborRetentionPolicy.
type HarborRetentionPolicyStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ProjectID in Harbor of the project this policy belongs to.
	// +optional
	ProjectID int32 `json:"projectID,omitempty"`
	// RetentionID in Harbor.
	// +optional
	RetentionID int64 `json:"retentionID,omitempty"`
	// HarborKey is the Harbor identity, object type and project name this resource claims.
	// +optional
	HarborKey string `json:"harborKey,omitempty"`
	// ObservedGeneration is the last generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=harbor,shortName=hret
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.spec.projectRef.name`
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.spec.projectRef == oldSelf.spec.projectRef",message="spec.projectRef is immutable"

// HarborRetentionPolicy is the tag retention policy of a project.
type HarborRetentionPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HarborRetentionPolicySpec   `json:"spec"`
	Status HarborRetentionPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HarborRetentionPolicyList is a list of HarborRetentionPolicy.
type HarborRetentionPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HarborRetentionPolicy `json:"items"`
}
