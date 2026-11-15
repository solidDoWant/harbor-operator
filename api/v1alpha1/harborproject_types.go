package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CVEAllowlist is a project's list of CVEs that do not block pulls.
type CVEAllowlist struct {
	// Items are CVE IDs. The list replaces Harbor's whole list.
	// +listType=set
	// +optional
	Items []string `json:"items,omitempty"`
	// ExpiresAt is when the allowlist stops applying. Unset means never.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
}

// VulnerabilitySpec holds the project's vulnerability settings. Unset fields are not touched.
// +kubebuilder:validation:XValidation:rule="!(has(self.useSystemAllowlist) && self.useSystemAllowlist && has(self.cveAllowlist))",message="cveAllowlist cannot be set together with useSystemAllowlist: true"
type VulnerabilitySpec struct {
	// +optional
	PreventVulnerableImages *bool `json:"preventVulnerableImages,omitempty"`
	// Severity is the level at and above which images are blocked.
	// +kubebuilder:validation:Enum=none;low;medium;high;critical
	// +optional
	Severity string `json:"severity,omitempty"`
	// +optional
	ScanOnPush *bool `json:"scanOnPush,omitempty"`
	// +optional
	UseSystemAllowlist *bool `json:"useSystemAllowlist,omitempty"`
	// +optional
	CVEAllowlist *CVEAllowlist `json:"cveAllowlist,omitempty"`
}

// HarborProjectSpec is the desired state of a Harbor project. Unset fields are not touched in Harbor.
// +kubebuilder:validation:XValidation:rule="!(has(self.storageQuota) && has(self.unlimitedStorage) && self.unlimitedStorage)",message="storageQuota and unlimitedStorage cannot both be set"
type HarborProjectSpec struct {
	ConnectionRef ObjectReference `json:"connectionRef"`
	// Name of the Harbor project. Defaults to metadata.name. Immutable.
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Public *bool `json:"public,omitempty"`
	// StorageQuota must be greater than zero. Use unlimitedStorage for no limit.
	// +kubebuilder:validation:XValidation:rule="type(self) == int ? self > 0 : quantity(self).isGreaterThan(quantity('0'))",message="storageQuota must be greater than 0; use unlimitedStorage for no limit"
	// +optional
	StorageQuota *resource.Quantity `json:"storageQuota,omitempty"`
	// UnlimitedStorage writes -1 as the quota.
	// +optional
	UnlimitedStorage bool `json:"unlimitedStorage,omitempty"`
	// +optional
	Vulnerability *VulnerabilitySpec `json:"vulnerability,omitempty"`
	// AllowedNamespaces admits robots and policies that point at this project.
	// +optional
	AllowedNamespaces AllowedNamespaces `json:"allowedNamespaces,omitempty"`
	// DeletionPolicy empty means the connection's default.
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
	// Adopt lets this resource take over an existing project of the same name.
	// +optional
	Adopt bool `json:"adopt,omitempty"`
	// ResyncInterval empty means the connection's default.
	// +optional
	ResyncInterval *metav1.Duration `json:"resyncInterval,omitempty"`
}

// HarborProjectStatus is the observed state of a HarborProject.
type HarborProjectStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ProjectID in Harbor.
	// +optional
	ProjectID int32 `json:"projectID,omitempty"`
	// HarborName is the project's name in Harbor.
	// +optional
	HarborName string `json:"harborName,omitempty"`
	// HarborKey is the Harbor identity, object type and Harbor name this resource claims.
	// +optional
	HarborKey string `json:"harborKey,omitempty"`
	// CreatePending is set just before the operator asks Harbor to create the project, and cleared once the ID is
	// recorded or Harbor refuses the create. A project found in Harbor counts as this resource's own interrupted create only when it was created no
	// earlier than this time, less a clock-skew allowance.
	// +optional
	CreatePending *metav1.Time `json:"createPending,omitempty"`
	// ObservedGeneration is the last generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Harbor Name",type=string,JSONPath=`.status.harborName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="(has(self.spec.name) ? self.spec.name : self.metadata.name) == (has(oldSelf.spec.name) ? oldSelf.spec.name : oldSelf.metadata.name)",message="spec.name is immutable"
// +kubebuilder:validation:XValidation:rule="self.spec.connectionRef == oldSelf.spec.connectionRef",message="spec.connectionRef is immutable"

// HarborProject is a project in Harbor.
type HarborProject struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HarborProjectSpec   `json:"spec"`
	Status HarborProjectStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HarborProjectList is a list of HarborProject.
type HarborProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HarborProject `json:"items"`
}
