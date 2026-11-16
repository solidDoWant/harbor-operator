package v1alpha1

// RobotPermissions lists the resource and action pairs a project robot may be granted, as Harbor 2.15 documents
// them for project robots. The CEL rule on RobotAccess carries the same list; a test keeps the two equal.
var RobotPermissions = map[string][]string{
	"accessory":           {"list"},
	"artifact":            {"delete", "list", "read"},
	"artifact-label":      {"create", "delete"},
	"configuration":       {"read", "update"},
	"export-cve":          {"create", "read"},
	"immutable-tag":       {"create", "delete", "list", "update"},
	"label":               {"create", "delete", "list", "read", "update"},
	"log":                 {"list"},
	"member":              {"create", "delete", "list", "read", "update"},
	"metadata":            {"create", "delete", "list", "read", "update"},
	"notification-policy": {"create", "delete", "list", "read", "update"},
	"preheat-policy":      {"create", "delete", "list", "read", "update"},
	"quota":               {"read"},
	"repository":          {"create", "delete", "list", "pull", "push", "read", "update"},
	"robot":               {"create", "delete", "list", "read", "update"},
	"sbom":                {"create", "read", "stop"},
	"scan":                {"create", "read", "stop"},
	"scanner-pull":        {"read"},
	"tag":                 {"create", "delete", "list"},
	"tag-retention":       {"create", "delete", "list", "operate", "read", "update"},
}

// SystemRobotPermissions lists the resource and action pairs a system robot may be granted for its system-level
// access, as Harbor 2.15 documents them. The CEL rule on SystemRobotAccess carries the same list; a test keeps the two equal.
var SystemRobotPermissions = map[string][]string{
	"audit-log":           {"list"},
	"catalog":             {"read"},
	"configuration":       {"read", "update"},
	"garbage-collection":  {"create", "list", "read", "stop", "update"},
	"jobservice-monitor":  {"list", "stop"},
	"label":               {"create", "delete", "read", "update"},
	"ldap-user":           {"create", "list"},
	"log-rotation":        {"create", "list", "read", "stop", "update"},
	"preheat-instance":    {"create", "delete", "list", "read", "update"},
	"project":             {"create", "list"},
	"purge-audit":         {"create", "list", "read", "stop", "update"},
	"quota":               {"list", "read", "update"},
	"registry":            {"create", "delete", "list", "read", "update"},
	"replication":         {"create", "list", "read"},
	"replication-adapter": {"list"},
	"replication-policy":  {"create", "delete", "list", "read", "update"},
	"robot":               {"create", "delete", "list", "read", "update"},
	"scan-all":            {"create", "read", "stop", "update"},
	"scanner":             {"create", "delete", "list", "read", "update"},
	"security-hub":        {"create", "list"},
	"system-volumes":      {"read"},
	"user":                {"create", "delete", "list", "read", "update"},
	"user-group":          {"create", "delete", "list", "read", "update"},
}

// SystemRobotProjectPermissions lists the pairs a system robot may be granted in projects: everything a project
// robot may hold, plus project read, update and delete. The CEL rule on SystemRobotProjectAccess carries the same list.
var SystemRobotProjectPermissions = func() map[string][]string {
	permissions := make(map[string][]string, len(RobotPermissions)+1)
	for resource, actions := range RobotPermissions {
		permissions[resource] = actions
	}

	permissions["project"] = []string{"delete", "read", "update"}

	return permissions
}()
