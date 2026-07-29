package components

// SessionUser is the signed-in visitor the header renders a user menu for.
//
// It is a view-layer projection of the authentication layer's Identity rather
// than the Identity itself. Keeping it a separate type is what lets internal/web
// stay free of any dependency on internal/server/authentication — and that in
// turn is what lets the authentication layer render pages (the login form) with
// no import cycle.
type SessionUser struct {
	// ID is the account's primary key.
	ID uint
	// Role is the account's coarse authorization level, as resolved by the
	// authentication layer.
	Role string
}
