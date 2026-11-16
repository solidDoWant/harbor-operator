package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Registry health values.
const (
	RegistryHealthHealthy   = "healthy"
	RegistryHealthUnhealthy = "unhealthy"
	RegistryHealthUnknown   = "unknown"
)

// HarborRegistrySpec is the desired state of a Harbor registry endpoint, the upstream of a proxy cache.
type HarborRegistrySpec struct {
	// ConnectionRef names the HarborConnection. Its systemAllowedNamespaces must admit this namespace.
	ConnectionRef ObjectReference `json:"connectionRef"`
	// Name of the registry in Harbor. Defaults to metadata.name. Immutable.
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`
	// +optional
	Name string `json:"name,omitempty"`
	// Type is the Harbor adapter type, for example docker-hub or github-ghcr. Immutable. Harbor checks the value.
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`
	Type string `json:"type"`
	// URL of the upstream registry. No user information and no trailing slash.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:XValidation:rule="!self.endsWith('/')",message="url must not end with a slash"
	// +kubebuilder:validation:XValidation:rule="!self.matches('^[a-zA-Z][a-zA-Z0-9+.-]*://[^/?#]*@')",message="url must not contain user information"
	URL string `json:"url"`
	// CredentialsSecretRef names a Secret in this namespace with the keys accessKey and accessSecret.
	// +optional
	CredentialsSecretRef *LocalSecretReference `json:"credentialsSecretRef,omitempty"`
	// Insecure skips certificate checks on the upstream.
	// +optional
	Insecure bool `json:"insecure,omitempty"`
	// +optional
	Description string `json:"description,omitempty"`
	// AllowedNamespaces admits the HarborProjects that may use this registry as a proxy-cache upstream.
	// +optional
	AllowedNamespaces AllowedNamespaces `json:"allowedNamespaces,omitempty"`
	// DeletionPolicy empty means the connection's default.
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
	// Adopt lets this resource take over an existing registry of the same name.
	// +optional
	Adopt bool `json:"adopt,omitempty"`
	// ResyncInterval empty means the connection's default.
	// +optional
	ResyncInterval *metav1.Duration `json:"resyncInterval,omitempty"`
}

// HarborRegistryStatus is the observed state of a HarborRegistry.
type HarborRegistryStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// RegistryID in Harbor.
	// +optional
	RegistryID int64 `json:"registryID,omitempty"`
	// HarborKey is the Harbor identity, object type and registry name this resource claims.
	// +optional
	HarborKey string `json:"harborKey,omitempty"`
	// CreatePending is set just before the operator asks Harbor to create the registry, and cleared once the ID is
	// recorded or Harbor refuses the create.
	// +optional
	CreatePending *metav1.Time `json:"createPending,omitempty"`
	// Health is the result of the last ping of the upstream. It never changes Ready.
	// +kubebuilder:validation:Enum=healthy;unhealthy;unknown
	// +optional
	Health string `json:"health,omitempty"`
	// ObservedGeneration is the last generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=harbor,shortName=hreg
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.url`
// +kubebuilder:printcolumn:name="Health",type=string,JSONPath=`.status.health`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="(has(self.spec.name) ? self.spec.name : self.metadata.name) == (has(oldSelf.spec.name) ? oldSelf.spec.name : oldSelf.metadata.name)",message="spec.name is immutable"
// +kubebuilder:validation:XValidation:rule="self.spec.type == oldSelf.spec.type",message="spec.type is immutable"
// +kubebuilder:validation:XValidation:rule="self.spec.connectionRef == oldSelf.spec.connectionRef",message="spec.connectionRef is immutable"

// HarborRegistry is a registry endpoint in Harbor.
type HarborRegistry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HarborRegistrySpec   `json:"spec"`
	Status HarborRegistryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HarborRegistryList is a list of HarborRegistry.
type HarborRegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HarborRegistry `json:"items"`
}
