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
