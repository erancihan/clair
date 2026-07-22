package authentication

import (
	"os"

	"github.com/gorilla/sessions"
)

const SESSION_NAME = "session-name"

// devSessionKey is a non-secret, well-known signing key used ONLY outside of
// production so local development and tests work without extra configuration.
// Production must supply its own key via SESSION_KEY.
const devSessionKey = "super-secret-32-byte-key-auth-v1"

// store signs and encrypts the session cookies. Its key is resolved once at
// package initialization: in production a missing SESSION_KEY is fatal (fail
// closed), everywhere else a development fallback is used.
var store = sessions.NewCookieStore([]byte(sessionKey()))

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
