package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConnectionDefaults are the defaults for resources that use the connection and leave a field unset.
type ConnectionDefaults struct {
	// ResyncInterval is how often resources are compared with Harbor again.
	// +kubebuilder:default="10m"
	// +optional
	ResyncInterval *metav1.Duration `json:"resyncInterval,omitempty"`
	// DeletionPolicy applies to resources with an empty deletionPolicy.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// HarborConnectionSpec describes how to reach one Harbor.
type HarborConnectionSpec struct {
	// URL of Harbor, for example https://harbor.example.com. No path.
	// +kubebuilder:validation:Pattern=`^https?://[^/?#]+/?$`
	URL string `json:"url"`
	// CredentialsSecretRef names a Secret in this namespace and the keys in it that hold the username and the password.
	// Without key settings the keys are username and password.
	CredentialsSecretRef CredentialsSecretReference `json:"credentialsSecretRef"`
	// CABundle is a PEM bundle that signs Harbor's certificate.
	// +optional
	CABundle string `json:"caBundle,omitempty"`
	// InsecureSkipVerify turns off certificate checks.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
	// RegistryHost is host[:port] written into docker configs. Defaults to the host of Harbor's external_url.
	// +optional
	RegistryHost string `json:"registryHost,omitempty"`
	// AllowedNamespaces admits HarborProject resources.
	// +optional
	AllowedNamespaces AllowedNamespaces `json:"allowedNamespaces,omitempty"`
	// SystemAllowedNamespaces admits Harbor-wide kinds.
	// +optional
	SystemAllowedNamespaces AllowedNamespaces `json:"systemAllowedNamespaces,omitempty"`
	// AllowAdoption lets resources that use this connection adopt existing Harbor objects.
	// +optional
	AllowAdoption bool `json:"allowAdoption,omitempty"`
	// AllowContentDeletion lets projects that use this connection with deletionPolicy DeleteWithContents delete every
	// repository in the project, and so every image, before the project is deleted.
	// +optional
	AllowContentDeletion bool `json:"allowContentDeletion,omitempty"`
	// Defaults for resources that use this connection.
	// +kubebuilder:default={}
	// +optional
	Defaults ConnectionDefaults `json:"defaults,omitempty"`
}

// HarborConnectionStatus is the observed state of a HarborConnection.
type HarborConnectionStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// HarborVersion as reported by Harbor.
	// +optional
	HarborVersion string `json:"harborVersion,omitempty"`
	// HarborIdentity is Harbor's normalized external_url.
	// +optional
	HarborIdentity string `json:"harborIdentity,omitempty"`
	// RegistryHost used in docker configs.
	// +optional
	RegistryHost string `json:"registryHost,omitempty"`
	// ObservedGeneration is the last generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=harbor,shortName=hconn
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.url`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.harborVersion`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HarborConnection is one Harbor instance and the credentials to reach it.
type HarborConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HarborConnectionSpec   `json:"spec"`
	Status HarborConnectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HarborConnectionList is a list of HarborConnection.
type HarborConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HarborConnection `json:"items"`
}
