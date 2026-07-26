package authentication

import (
	"net/http"

	server_context "github.com/erancihan/clair/internal/server/context"
)

// AuthLogout ends the authenticated session by expiring the session cookie. The
// long-lived guest "sid" cookie is deliberately left intact so the visitor keeps
// a stable guest identity (OwnerRef) after signing out — only the authentication
// session ends here.
func AuthLogout(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, _ := store.Get(r, SESSION_NAME)

		// Write the deletion cookie with the same hardened attributes the session
		// was created with, resolved now rather than at package initialization.
		// Attributes must line up for the browser to replace the cookie, and a
		// Secure cookie would be dropped over plain HTTP outside production.
		session.Options = sessionCookieOptions()
		session.Options.MaxAge = -1

		session.Save(r, w)
	}
}
