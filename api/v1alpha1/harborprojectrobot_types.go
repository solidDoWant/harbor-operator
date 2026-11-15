package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RobotAccess is one permission: an action on a resource of the robot's project.
// +kubebuilder:validation:XValidation:rule="['accessory:list','artifact:delete','artifact:list','artifact:read','artifact-label:create','artifact-label:delete','configuration:read','configuration:update','export-cve:create','export-cve:read','immutable-tag:create','immutable-tag:delete','immutable-tag:list','immutable-tag:update','label:create','label:delete','label:list','label:read','label:update','log:list','member:create','member:delete','member:list','member:read','member:update','metadata:create','metadata:delete','metadata:list','metadata:read','metadata:update','notification-policy:create','notification-policy:delete','notification-policy:list','notification-policy:read','notification-policy:update','preheat-policy:create','preheat-policy:delete','preheat-policy:list','preheat-policy:read','preheat-policy:update','quota:read','repository:create','repository:delete','repository:list','repository:pull','repository:push','repository:read','repository:update','robot:create','robot:delete','robot:list','robot:read','robot:update','sbom:create','sbom:read','sbom:stop','scan:create','scan:read','scan:stop','scanner-pull:read','tag:create','tag:delete','tag:list','tag-retention:create','tag-retention:delete','tag-retention:list','tag-retention:operate','tag-retention:read','tag-retention:update'].exists(p, p == self.resource + ':' + self.action)",message="resource and action must be a pair from the list in docs/user/robot-permissions.md"
type RobotAccess struct {
	// Resource is a Harbor resource, for example repository.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	Resource string `json:"resource"`
	// Action is a Harbor action, for example push.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	Action string `json:"action"`
}

// SecretOutput configures one Secret the operator writes for the robot.
type SecretOutput struct {
	// Enabled turns the Secret on.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Name of the Secret. Defaults to <resource name>-dockerconfig or <resource name>-credentials.
	// +optional
	Name string `json:"name,omitempty"`
}

// RobotSecretSpec says which Secrets hold the robot's credentials. They are written to the robot's own namespace.
type RobotSecretSpec struct {
	// DockerConfigJSON is a kubernetes.io/dockerconfigjson Secret. Enabled by default.
	// +optional
	DockerConfigJSON SecretOutput `json:"dockerConfigJSON,omitempty"`
	// Opaque is a Secret with the keys username, password and registry. Disabled by default.
	// +optional
	Opaque SecretOutput `json:"opaque,omitempty"`
	// Rotate is any string. Changing it refreshes the robot's secret.
	// +optional
	Rotate string `json:"rotate,omitempty"`
}

// HarborProjectRobotSpec is the desired state of a project robot account.
type HarborProjectRobotSpec struct {
	ProjectRef ObjectReference `json:"projectRef"`
	// Name of the robot. Defaults to metadata.name. Immutable.
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Description string `json:"description,omitempty"`
	// Duration in days. -1 means the robot never expires.
	// +kubebuilder:default=-1
	// +kubebuilder:validation:XValidation:rule="self == -1 || (self >= 1 && self <= 2147483646)",message="duration must be -1 or between 1 and 2147483646"
	// +optional
	Duration int64 `json:"duration,omitempty"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	// +listType=atomic
	Permissions []RobotAccess `json:"permissions"`
	// +optional
	Disabled bool `json:"disabled,omitempty"`
	// +kubebuilder:default={}
	// +optional
	Secret RobotSecretSpec `json:"secret,omitempty"`
	// DeletionPolicy empty means the connection's default.
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
	// +optional
	Adopt bool `json:"adopt,omitempty"`
	// ResyncInterval empty means the connection's default.
	// +optional
	ResyncInterval *metav1.Duration `json:"resyncInterval,omitempty"`
}

// HarborProjectRobotStatus is the observed state of a HarborProjectRobot. The secret hash is kept in an annotation on
// each Secret, never here.
type HarborProjectRobotStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// RobotID in Harbor.
	// +optional
	RobotID int64 `json:"robotID,omitempty"`
	// HarborKey is the Harbor identity, project name and robot name this resource claims.
	// +optional
	HarborKey string `json:"harborKey,omitempty"`
	// HarborName is the robot's name as Harbor returned it, including Harbor's prefix.
	// +optional
	HarborName string `json:"harborName,omitempty"`
	// CreatePending is set just before the operator asks Harbor to create the robot, and cleared once its Secrets hold
	// the new secret or Harbor refuses the create. A robot found in Harbor counts as this resource's own interrupted create only when it was created
	// no earlier than this time, less a clock-skew allowance.
	// +optional
	CreatePending *metav1.Time `json:"createPending,omitempty"`
	// ObservedRotate is the last spec.secret.rotate value acted on.
	// +optional
	ObservedRotate string `json:"observedRotate,omitempty"`
	// LastRotated is when the secret was last refreshed.
	// +optional
	LastRotated *metav1.Time `json:"lastRotated,omitempty"`
	// ExpiresAt is when the robot expires. Unset means never.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
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
// +kubebuilder:validation:XValidation:rule="!(has(self.spec.secret.opaque) && has(self.spec.secret.opaque.enabled) && self.spec.secret.opaque.enabled) || (has(self.spec.secret.dockerConfigJSON) && has(self.spec.secret.dockerConfigJSON.enabled) && !self.spec.secret.dockerConfigJSON.enabled) || (has(self.spec.secret.dockerConfigJSON) && has(self.spec.secret.dockerConfigJSON.name) && self.spec.secret.dockerConfigJSON.name != '' ? self.spec.secret.dockerConfigJSON.name : self.metadata.name + '-dockerconfig') != (has(self.spec.secret.opaque.name) && self.spec.secret.opaque.name != '' ? self.spec.secret.opaque.name : self.metadata.name + '-credentials')",message="the dockerConfigJSON and opaque Secrets must have different names when both are enabled"
// +kubebuilder:validation:XValidation:rule="self.spec.projectRef == oldSelf.spec.projectRef",message="spec.projectRef is immutable"

// HarborProjectRobot is a project-level robot account in Harbor.
type HarborProjectRobot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HarborProjectRobotSpec   `json:"spec"`
	Status HarborProjectRobotStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HarborProjectRobotList is a list of HarborProjectRobot.
type HarborProjectRobotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HarborProjectRobot `json:"items"`
}
