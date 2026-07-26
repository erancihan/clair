package authentication

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/erancihan/clair/internal/database/models"
	server_context "github.com/erancihan/clair/internal/server/context"
	"gorm.io/gorm"
)

// AuthMiddleware validates the session cookie, re-checks that the user still
// exists in the database, and injects the resulting Identity into the request
// context. On failure it delegates to unauthorized, which content-negotiates
// between a 401 (JSON callers) and a redirect to the login page (browsers).
func AuthMiddleware(ctx server_context.BackEndContext) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := authenticate(ctx, r)
			if !ok {
				unauthorized(w, r)
				return
			}

			r = r.WithContext(context.WithValue(r.Context(), identityContextKey, identity))
			next.ServeHTTP(w, r)
		})
	}
}

// AdminMiddleware wraps AuthMiddleware and additionally requires that the
// authenticated user hold the admin role. Non-admin users receive a 403; the
// authentication failure path (unauthenticated) is handled by AuthMiddleware and
// stays content-negotiated.
func AdminMiddleware(ctx server_context.BackEndContext) func(http.Handler) http.Handler {
	requireAuth := AuthMiddleware(ctx)
	return func(next http.Handler) http.Handler {
		return requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := CurrentUser(r.Context())
			if !ok {
				// Should not happen: AuthMiddleware injects the identity before
				// this handler runs. Treat a missing identity as unauthorized.
				unauthorized(w, r)
				return
			}

			if identity.Role != RoleAdmin {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		}))
	}
}

// authenticate resolves the Identity for a request from the session cookie,
// re-validating the user against the database. It returns false when the caller
// is unauthenticated or the backing user row no longer exists.
func authenticate(ctx server_context.BackEndContext, r *http.Request) (Identity, bool) {
	session, err := store.Get(r, SESSION_NAME)
	if err != nil {
		return Identity{}, false
	}

	if auth, ok := session.Values["authenticated"].(bool); !ok || !auth {
		return Identity{}, false
	}

	userID, ok := session.Values["id"].(uint)
	if !ok {
		return Identity{}, false
	}

	tx := ctx.DBConn.Session(&gorm.Session{Context: r.Context()})

	var user models.User
	result := tx.Limit(1).Where("id = ?", userID).Find(&user)
	if result.RowsAffected == 0 {
		return Identity{}, false
	}

	role := user.Role
	if role == "" {
		role = RoleUser
	}

	return Identity{UserID: user.ID, Role: role}, true
}

// unauthorized responds to an authentication failure using content negotiation.
// API/JSON callers receive a 401 so they can react programmatically; browsers are
// redirected to the login page with a validated return path so they can sign in
// and come back to where they were.
func unauthorized(w http.ResponseWriter, r *http.Request) {
	if wantsJSON(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	next := url.QueryEscape(r.URL.RequestURI())
	http.Redirect(w, r, "/login?next="+next, http.StatusFound)
}

// wantsJSON reports whether the caller is a JSON/API client, as signalled by an
// Accept or Content-Type header mentioning application/json.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json") ||
		strings.Contains(r.Header.Get("Content-Type"), "application/json")
}
