package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SystemRobotAccess is one system-level permission. Harbor checks the resource and action.
// +kubebuilder:validation:XValidation:rule="['audit-log:list','catalog:read','configuration:read','configuration:update','garbage-collection:create','garbage-collection:list','garbage-collection:read','garbage-collection:stop','garbage-collection:update','jobservice-monitor:list','jobservice-monitor:stop','label:create','label:delete','label:read','label:update','ldap-user:create','ldap-user:list','log-rotation:create','log-rotation:list','log-rotation:read','log-rotation:stop','log-rotation:update','preheat-instance:create','preheat-instance:delete','preheat-instance:list','preheat-instance:read','preheat-instance:update','project:create','project:list','purge-audit:create','purge-audit:list','purge-audit:read','purge-audit:stop','purge-audit:update','quota:list','quota:read','quota:update','registry:create','registry:delete','registry:list','registry:read','registry:update','replication:create','replication:list','replication:read','replication-adapter:list','replication-policy:create','replication-policy:delete','replication-policy:list','replication-policy:read','replication-policy:update','robot:create','robot:delete','robot:list','robot:read','robot:update','scan-all:create','scan-all:read','scan-all:stop','scan-all:update','scanner:create','scanner:delete','scanner:list','scanner:read','scanner:update','security-hub:create','security-hub:list','system-volumes:read','user:create','user:delete','user:list','user:read','user:update','user-group:create','user-group:delete','user-group:list','user-group:read','user-group:update'].exists(p, p == self.resource + ':' + self.action)",message="resource and action must be a pair from the system list in docs/user/robot-permissions.md"
type SystemRobotAccess struct {
	// Resource is a Harbor system resource, for example project.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	Resource string `json:"resource"`
	// Action is a Harbor action, for example create.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	Action string `json:"action"`
}

// SystemRobotProjectAccess is one permission a system robot holds in projects. It allows everything a project robot
// may hold, plus project read, update and delete, which only a system robot may be granted.
// +kubebuilder:validation:XValidation:rule="['accessory:list','artifact-label:create','artifact-label:delete','artifact:delete','artifact:list','artifact:read','configuration:read','configuration:update','export-cve:create','export-cve:read','immutable-tag:create','immutable-tag:delete','immutable-tag:list','immutable-tag:update','label:create','label:delete','label:list','label:read','label:update','log:list','member:create','member:delete','member:list','member:read','member:update','metadata:create','metadata:delete','metadata:list','metadata:read','metadata:update','notification-policy:create','notification-policy:delete','notification-policy:list','notification-policy:read','notification-policy:update','preheat-policy:create','preheat-policy:delete','preheat-policy:list','preheat-policy:read','preheat-policy:update','project:delete','project:read','project:update','quota:read','repository:create','repository:delete','repository:list','repository:pull','repository:push','repository:read','repository:update','robot:create','robot:delete','robot:list','robot:read','robot:update','sbom:create','sbom:read','sbom:stop','scan:create','scan:read','scan:stop','scanner-pull:read','tag-retention:create','tag-retention:delete','tag-retention:list','tag-retention:operate','tag-retention:read','tag-retention:update','tag:create','tag:delete','tag:list'].exists(p, p == self.resource + ':' + self.action)",message="resource and action must be a pair from the list in docs/user/robot-permissions.md"
type SystemRobotProjectAccess struct {
	// Resource is a Harbor resource, for example repository.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	Resource string `json:"resource"`
	// Action is a Harbor action, for example push.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	Action string `json:"action"`
}

// ProjectPermission grants project-level access in one or more projects.
type ProjectPermission struct {
	// Projects are Harbor project names. "*" means every project.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=255
	// +listType=set
	Projects []string `json:"projects"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	// +listType=atomic
	Access []SystemRobotProjectAccess `json:"access"`
}

// HarborSystemRobotSpec is the desired state of a system robot account.
// +kubebuilder:validation:XValidation:rule="(has(self.systemPermissions) ? size(self.systemPermissions) : 0) + (has(self.projectPermissions) ? size(self.projectPermissions) : 0) > 0",message="systemPermissions and projectPermissions need at least one entry together"
type HarborSystemRobotSpec struct {
	// ConnectionRef names the HarborConnection. Its systemAllowedNamespaces must admit this namespace. Immutable.
	ConnectionRef ObjectReference `json:"connectionRef"`
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
	// SystemPermissions are permissions on system resources.
	// +kubebuilder:validation:MaxItems=100
	// +listType=atomic
	// +optional
	SystemPermissions []SystemRobotAccess `json:"systemPermissions,omitempty"`
	// ProjectPermissions are permissions in projects. At most 10 entries keep the access rule inside the API server's cost budget.
	// +kubebuilder:validation:MaxItems=10
	// +listType=atomic
	// +optional
	ProjectPermissions []ProjectPermission `json:"projectPermissions,omitempty"`
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

// HarborSystemRobotStatus is the observed state of a HarborSystemRobot. It has the same fields as a project robot.
type HarborSystemRobotStatus = HarborProjectRobotStatus

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=harbor,shortName=hsrobot
// +kubebuilder:printcolumn:name="Harbor Name",type=string,JSONPath=`.status.harborName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="(has(self.spec.name) ? self.spec.name : self.metadata.name) == (has(oldSelf.spec.name) ? oldSelf.spec.name : oldSelf.metadata.name)",message="spec.name is immutable"
// +kubebuilder:validation:XValidation:rule="self.spec.connectionRef == oldSelf.spec.connectionRef",message="spec.connectionRef is immutable"
// +kubebuilder:validation:XValidation:rule="!(has(self.spec.secret.opaque) && has(self.spec.secret.opaque.enabled) && self.spec.secret.opaque.enabled) || (has(self.spec.secret.dockerConfigJSON) && has(self.spec.secret.dockerConfigJSON.enabled) && !self.spec.secret.dockerConfigJSON.enabled) || (has(self.spec.secret.dockerConfigJSON) && has(self.spec.secret.dockerConfigJSON.name) && self.spec.secret.dockerConfigJSON.name != '' ? self.spec.secret.dockerConfigJSON.name : self.metadata.name + '-dockerconfig') != (has(self.spec.secret.opaque.name) && self.spec.secret.opaque.name != '' ? self.spec.secret.opaque.name : self.metadata.name + '-credentials')",message="the dockerConfigJSON and opaque Secrets must have different names when both are enabled"

// HarborSystemRobot is a system-level robot account in Harbor.
type HarborSystemRobot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HarborSystemRobotSpec   `json:"spec"`
	Status HarborSystemRobotStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HarborSystemRobotList is a list of HarborSystemRobot.
type HarborSystemRobotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HarborSystemRobot `json:"items"`
}
