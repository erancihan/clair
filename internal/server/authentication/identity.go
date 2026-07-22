package authentication

import "context"

// contextKey is an unexported type for context keys defined in this package. Using
// a private type prevents collisions with keys defined in other packages.
type contextKey string

// identityContextKey is the context key under which the authenticated Identity is
// stored by AuthMiddleware.
const identityContextKey contextKey = "authentication.identity"

// Identity is the authenticated principal for a request. It is derived from the
// session (and re-validated against the database) by AuthMiddleware and injected
// into the request context so downstream handlers can authorize without touching
// the session store directly.
type Identity struct {
	UserID uint
	Role   string
}

// CurrentUser returns the Identity that AuthMiddleware injected into ctx. The
// boolean is false when the request is not authenticated (no middleware ran, or
// authentication failed), in which case the zero Identity is returned.
func CurrentUser(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityContextKey).(Identity)
	return identity, ok
}
