package authentication

// Roles are intentionally modeled as plain strings rather than a closed enum:
// this is a central authentication layer shared by multiple domains (a shop CMS,
// a booking domain, future admin surfaces), so keeping the type open leaves
// headroom to introduce finer-grained roles later without a breaking change.
const (
	// RoleUser is the default role granted to every registered account.
	RoleUser = "user"
	// RoleAdmin grants access to administrative surfaces.
	RoleAdmin = "admin"
)
