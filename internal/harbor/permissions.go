package harbor

// RequiredPermission is one permission a kind's reconciler needs on the connection's credential. Scope is "system" or
// "project". The table, RequiredPermissions, is generated (make generate) from the //harbor:permission annotations on
// the client methods, so it cannot drift from the calls.
type RequiredPermission struct {
	Kind     string
	Scope    string
	Resource string
	Action   string
	// UserCredentialsOnly is true when robot credentials do not need it: they replace robots instead of updating them.
	UserCredentialsOnly bool
}
