package test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	authentication "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
)

// findCookie returns the named cookie from a response, or nil.
func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestSessionCookieHardening verifies the attributes of the session cookie that
// logout writes. Logout does not override the store's options, so this is what
// catches an unsafe default leaking through: the cookie must always be HttpOnly
// and SameSite=Lax, and must only carry Secure in production (a Secure cookie is
// dropped by browsers over plain HTTP, which would silently break logout in
// development).
func TestSessionCookieHardening(t *testing.T) {
	tests := []struct {
		name       string
		appEnv     string
		wantSecure bool
	}{
		{name: "development", appEnv: "development", wantSecure: false},
		{name: "production", appEnv: "production", wantSecure: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.appEnv)

			rec := httptest.NewRecorder()
			authentication.AuthLogout(server_context.BackEndContext{}).ServeHTTP(
				rec, httptest.NewRequest(http.MethodGet, "/logout", nil))

			cookie := findCookie(rec.Result(), "session-name")
			if cookie == nil {
				t.Fatal("expected logout to write the session cookie")
			}

			if !cookie.HttpOnly {
				t.Error("session cookie must be HttpOnly")
			}
			if cookie.SameSite != http.SameSiteLaxMode {
				t.Errorf("session cookie SameSite = %v, want Lax", cookie.SameSite)
			}
			if cookie.Path != "/" {
				t.Errorf("session cookie Path = %q, want \"/\"", cookie.Path)
			}
			if cookie.Secure != tc.wantSecure {
				t.Errorf("session cookie Secure = %v, want %v (APP_ENV=%s)",
					cookie.Secure, tc.wantSecure, tc.appEnv)
			}
			// Logout must expire the cookie.
			if cookie.MaxAge >= 0 {
				t.Errorf("logout should expire the session cookie, got MaxAge=%d", cookie.MaxAge)
			}
		})
	}
}

// TestGuestCookieHardening verifies the guest "sid" cookie carries Secure only in
// production, mirroring the session cookie policy.
func TestGuestCookieHardening(t *testing.T) {
	tests := []struct {
		name       string
		appEnv     string
		wantSecure bool
	}{
		{name: "development", appEnv: "development", wantSecure: false},
		{name: "production", appEnv: "production", wantSecure: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.appEnv)

			rec := httptest.NewRecorder()
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authentication.SessionID(w, r)
			}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			cookie := findCookie(rec.Result(), "sid")
			if cookie == nil {
				t.Fatal("expected a sid cookie to be minted")
			}
			if !cookie.HttpOnly {
				t.Error("sid cookie must be HttpOnly")
			}
			if cookie.Secure != tc.wantSecure {
				t.Errorf("sid cookie Secure = %v, want %v (APP_ENV=%s)",
					cookie.Secure, tc.wantSecure, tc.appEnv)
			}
		})
	}
}
