package authentication

import (
	"net/http"
	"os"

	"github.com/gorilla/sessions"
)

const SESSION_NAME = "session-name"

// sessionMaxAge is the lifetime of the authenticated session cookie.
const sessionMaxAge = 3600

// devSessionKey is a non-secret, well-known signing key used ONLY outside of
// production so local development and tests work without extra configuration.
// Production must supply its own key via SESSION_KEY.
const devSessionKey = "super-secret-32-byte-key-auth-v1"

// store signs and encrypts the session cookies. Its key is resolved once at
// package initialization: in production a missing SESSION_KEY is fatal (fail
// closed), everywhere else a development fallback is used.
var store = newSessionStore()

// newSessionStore builds the cookie store with hardened defaults. gorilla's
// out-of-the-box options are wrong for this app: HttpOnly is unset (leaving the
// session cookie readable from JavaScript), SameSite is None, and Secure is
// hard-coded true (so browsers drop the cookie over plain HTTP in development).
// Setting them here means every save is safe by default — including the cookie
// deletion performed by logout, which does not override the options.
func newSessionStore() *sessions.CookieStore {
	s := sessions.NewCookieStore([]byte(sessionKey()))
	s.Options = sessionCookieOptions()
	// Keep the signature lifetime in step with the cookie lifetime so a captured
	// cookie cannot be replayed after it should have expired.
	s.MaxAge(sessionMaxAge)
	return s
}

// sessionCookieOptions returns the hardened cookie options used for every session
// cookie this layer writes.
func sessionCookieOptions() *sessions.Options {
	return &sessions.Options{
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		Secure:   SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	}
}

// sessionKey resolves the cookie signing key. It sources SESSION_KEY from the
// environment and fails closed in production when it is unset; outside production
// it falls back to a fixed development key so the app still boots.
func sessionKey() string {
	if key := os.Getenv("SESSION_KEY"); key != "" {
		return key
	}

	if isProduction() {
		panic("authentication: SESSION_KEY must be set when APP_ENV=production")
	}

	return devSessionKey
}

// isProduction reports whether the process is running in the production
// environment, as declared by APP_ENV.
func isProduction() bool {
	return os.Getenv("APP_ENV") == "production"
}

// SecureCookies reports whether cookies set by this layer should carry the
// Secure attribute (HTTPS-only). It is enabled in production and disabled
// elsewhere so cookies still work over plain HTTP during local development.
func SecureCookies() bool {
	return isProduction()
}
