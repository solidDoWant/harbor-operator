package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AllowedNamespaces lists the namespaces that may use an object. The object's own namespace is always admitted.
type AllowedNamespaces struct {
	// Names is an explicit list of namespace names.
	// +optional
	// +listType=set
	Names []string `json:"names,omitempty"`
	// Selector admits every namespace whose labels match.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// ObjectReference points at an object of another kind.
type ObjectReference struct {
	// Namespace of the object. Empty means the namespace of the referring resource.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Name of the object.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// LocalSecretReference points at a Secret in the namespace of the referring resource.
type LocalSecretReference struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// CredentialsSecretReference points at a Secret in the namespace of the referring resource and says which keys hold
// the username and the password.
// +kubebuilder:validation:XValidation:rule="!(has(self.username) && has(self.usernameKey))",message="username and usernameKey are mutually exclusive"
type CredentialsSecretReference struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Username is the literal user name. Use it when the Secret has no user name key. Excludes usernameKey.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Username string `json:"username,omitempty"`
	// UsernameKey is the key in the Secret that holds the user name. Defaults to username.
	// +kubebuilder:validation:MinLength=1
	// +optional
	UsernameKey string `json:"usernameKey,omitempty"`
	// PasswordKey is the key in the Secret that holds the password. Defaults to password.
	// +kubebuilder:validation:MinLength=1
	// +optional
	PasswordKey string `json:"passwordKey,omitempty"`
}

// DeletionPolicy says what happens to the Harbor object when its custom resource is deleted.
// +kubebuilder:validation:Enum=Delete;DeleteWithContents;Orphan
type DeletionPolicy string

const (
	// DeletionPolicyDelete deletes the Harbor object.
	DeletionPolicyDelete DeletionPolicy = "Delete"
	// DeletionPolicyDeleteWithContents deletes every repository of a project, then the project, when the connection
	// allows content deletion. On every other kind it means the same as Delete.
	DeletionPolicyDeleteWithContents DeletionPolicy = "DeleteWithContents"
	// DeletionPolicyOrphan leaves the Harbor object in place.
	DeletionPolicyOrphan DeletionPolicy = "Orphan"
)

// Annotations the operator sets on resources while a multi-step Harbor change is in flight.
const (
	// AnnotationCreatePending was the create marker of an earlier design. Users can write it, so it proves nothing: the
	// operator ignores it and removes it. The marker is now status.createPending.
	AnnotationCreatePending = "harbor.soliddowant.dev/create-pending"
	// AnnotationSecretRefreshPending is set before a robot secret refresh and cleared once every enabled Secret is written.
	AnnotationSecretRefreshPending = "harbor.soliddowant.dev/secret-refresh-pending"
)

// Condition types.
const (
	ConditionReady             = "Ready"
	ConditionReferenceResolved = "ReferenceResolved"
)

// Condition reasons. Every reason the controllers set is listed here.
const (
	ReasonReady                 = "Ready"
	ReasonCredentialsInvalid    = "CredentialsInvalid"
	ReasonUnauthorized          = "Unauthorized"
	ReasonForbidden             = "Forbidden"
	ReasonNotFound              = "NotFound"
	ReasonHarborUnavailable     = "HarborUnavailable"
	ReasonDependentsExist       = "DependentsExist"
	ReasonNotAllowed            = "NotAllowed"
	ReasonConflict              = "Conflict"
	ReasonAlreadyExists         = "AlreadyExists"
	ReasonAdoptionNotAllowed    = "AdoptionNotAllowed"
	ReasonInvalidSpec           = "InvalidSpec"
	ReasonDeleteBlocked         = "DeleteBlocked"
	ReasonProjectNotReady       = "ProjectNotReady"
	ReasonSecretConflict        = "SecretConflict"
	ReasonExpired               = "Expired"
	ReasonReferenceResolved     = "ReferenceResolved"
	ReasonRegistryNotReady      = "RegistryNotReady"
	ReasonRegistryOnOtherHarbor = "RegistryOnOtherHarbor"
	ReasonProjectNotFound       = "ProjectNotFound"
	ReasonOperatorCredential    = "OperatorCredential"
)

// Event reasons.
const (
	EventReasonCreated       = "Created"
	EventReasonUpdated       = "Updated"
	EventReasonDeleted       = "Deleted"
	EventReasonOrphaned      = "Orphaned"
	EventReasonAdopted       = "Adopted"
	EventReasonRecreated     = "Recreated"
	EventReasonReplaced      = "Replaced"
	EventReasonHealthChanged = "HealthChanged"
	// ReasonContentDeletionNotAllowed is emitted when a project's policy asks to delete its contents but the
	// connection does not allow it.
	ReasonContentDeletionNotAllowed = "ContentDeletionNotAllowed"
)
