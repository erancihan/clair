package authentication

import (
	"crypto/subtle"
	"net/http"
)

const (
	// csrfSessionField is the key under which the CSRF token is stored in the
	// session values.
	csrfSessionField = "csrf_token"
	// CSRFHeaderName is the request header carrying the CSRF token for AJAX/API
	// style callers.
	CSRFHeaderName = "X-CSRF-Token"
	// CSRFFieldName is the form field carrying the CSRF token for HTML form
	// submissions.
	CSRFFieldName = "csrf_token"
)

// csrfSafeMethods are the HTTP methods considered safe (non-mutating); they are
// never blocked by the CSRF middleware.
var csrfSafeMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
	http.MethodTrace:   true,
}

// CSRFToken returns the CSRF token bound to the caller's session, reading the
// existing token or creating one when absent. The token is created in the
// session's (per-request cached) values; persistence to the cookie happens when
// the session is saved — the CSRF middleware does this on safe requests so a
// rendered form embeds a token that will later validate.
func CSRFToken(r *http.Request) string {
	session, _ := store.Get(r, SESSION_NAME)
	if token, ok := session.Values[csrfSessionField].(string); ok && token != "" {
		return token
	}

	token := SecureToken()
	session.Values[csrfSessionField] = token
	return token
}

// ensureCSRFToken returns the caller's session-bound CSRF token, persisting a
// freshly minted one to the session cookie before returning it.
//
// This is what CSRFToken cannot do on its own: CSRFToken only writes into the
// per-request session values, so a token it minted never reaches the browser
// unless something else saves the session. A token published in a page that was
// never persisted would fail the very check it exists to pass. Callers must
// therefore call this before writing any part of the response body.
func ensureCSRFToken(w http.ResponseWriter, r *http.Request) string {
	session, _ := store.Get(r, SESSION_NAME)

	if token, ok := session.Values[csrfSessionField].(string); ok && token != "" {
		return token
	}

	token := SecureToken()
	session.Values[csrfSessionField] = token
	session.Options = sessionCookieOptions()
	_ = session.Save(r, w)

	return token
}

// CSRF returns middleware that protects state-changing requests against
// cross-site request forgery. Safe methods (GET/HEAD/OPTIONS/TRACE) pass through,
// but first ensure a session-bound token exists and is persisted so forms and API
// clients can obtain one. Unsafe methods (POST/PUT/PATCH/DELETE) must present a
// token — via the X-CSRF-Token header or the csrf_token form field — that matches
// the session token under a constant-time comparison, else they receive a 403.
//
// Login and registration are intentionally NOT wrapped by this middleware: they
// precede the session that a CSRF token is bound to.
func CSRF() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, _ := store.Get(r, SESSION_NAME)

			expected, ok := session.Values[csrfSessionField].(string)
			if !ok || expected == "" {
				// Mint a token and persist it so the client can echo it back.
				expected = SecureToken()
				session.Values[csrfSessionField] = expected
				session.Options = sessionCookieOptions()
				_ = session.Save(r, w)
			}

			if csrfSafeMethods[r.Method] {
				next.ServeHTTP(w, r)
				return
			}

			presented := r.Header.Get(CSRFHeaderName)
			if presented == "" {
				presented = r.PostFormValue(CSRFFieldName)
			}

			if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
				http.Error(w, "Forbidden - invalid CSRF token", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
