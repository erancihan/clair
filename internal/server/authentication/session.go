package authentication

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
)

const (
	// sessionIDCookieName is the name of the long-lived guest session cookie.
	sessionIDCookieName = "sid"
	// sessionIDMaxAge is the lifetime of the guest session cookie (~1 year). The
	// guest identity is meant to outlive individual auth sessions so a visitor
	// keeps the same owner reference across visits.
	sessionIDMaxAge = 60 * 60 * 24 * 365
	// tokenBytes is the entropy, in bytes, of tokens minted by SecureToken.
	tokenBytes = 32 // 256 bits
)

// SecureToken returns a cryptographically-random, URL-safe token carrying 256
// bits of entropy. It is used for session ids and CSRF tokens. It panics only if
// the system CSPRNG fails, which is not a recoverable condition.
func SecureToken() string {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("authentication: failed to read from CSPRNG: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// SessionID returns the caller's long-lived guest session id, reading it from the
// "sid" cookie. When the cookie is absent (or empty) a new id is minted and set
// on the response as an HttpOnly, site-wide, Lax cookie. This id is stable for a
// browser regardless of whether the user is authenticated, which is what lets the
// booking domain attribute anonymous activity to a consistent owner.
//
// It is idempotent within a request: a freshly minted id is mirrored onto the
// request so repeat calls return the same value and only one "sid" cookie is ever
// set per response. Handlers that resolve an owner more than once (for example
// reading a hold and then writing an order) therefore see one consistent id.
func SessionID(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(sessionIDCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}

	sid := SecureToken()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionIDCookieName,
		Value:    sid,
		Path:     "/",
		MaxAge:   sessionIDMaxAge,
		HttpOnly: true,
		Secure:   SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})

	// Mirror the new id onto the request so later calls in this same request read
	// it back instead of minting (and setting) a second, conflicting id.
	r.AddCookie(&http.Cookie{Name: sessionIDCookieName, Value: sid})

	return sid
}

// OwnerRef returns a stable owner reference for the current caller. Authenticated
// callers get "user:<id>"; everyone else gets "guest:<sid>", minting the guest
// session cookie if needed. This exact on-disk format is persisted by downstream
// domains (e.g. booking holds and orders), so it must remain stable.
//
// "Authenticated" here means an Identity is present in the request context, which
// AuthMiddleware injects. On a route that is NOT behind AuthMiddleware a signed-in
// visitor is indistinguishable from a guest and will get "guest:<sid>", so any
// route whose ownership should follow the account must run behind it.
func OwnerRef(w http.ResponseWriter, r *http.Request) string {
	if identity, ok := CurrentUser(r.Context()); ok {
		return fmt.Sprintf("user:%d", identity.UserID)
	}
	return "guest:" + SessionID(w, r)
}
