package test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/erancihan/clair/internal/database/models"
	authentication "github.com/erancihan/clair/internal/server/authentication"
)

// TestUserJSONNeverExposesPassword guards the credential-leak fix. models.User is
// encoded straight to JSON by user-facing handlers (e.g. GET /api/v1/users), so
// the bcrypt hash must never be marshalable — regardless of which handler does
// the encoding.
func TestUserJSONNeverExposesPassword(t *testing.T) {
	user := models.User{
		ID:       7,
		Username: "leak-check",
		Email:    "leak@example.com",
		Password: "$2a$10$bcrypthashthatmustnotescape",
		Role:     authentication.RoleAdmin,
	}

	encoded, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("failed to marshal user: %v", err)
	}
	payload := string(encoded)

	if strings.Contains(payload, user.Password) {
		t.Errorf("serialized user leaks the password hash: %s", payload)
	}
	if strings.Contains(strings.ToLower(payload), "\"password\"") {
		t.Errorf("serialized user still carries a password field: %s", payload)
	}

	// The rest of the record must still round-trip for legitimate consumers.
	for _, want := range []string{"leak-check", "leak@example.com", authentication.RoleAdmin} {
		if !strings.Contains(payload, want) {
			t.Errorf("serialized user is missing %q: %s", want, payload)
		}
	}
}

// TestCSRFTokenRotatesOnLogin verifies that a CSRF token obtained before signing
// in does not survive into the authenticated session. Without rotation, anyone
// who had planted or observed the visitor's pre-login session cookie would hold a
// valid token for their authenticated session, defeating CSRF protection exactly
// where it matters.
func TestCSRFTokenRotatesOnLogin(t *testing.T) {
	srv := newAuthTestServer(t)
	createUser(t, srv.db, "rotate@example.com", "rotate-pass-123", authentication.RoleUser)

	client := noRedirectClient()

	// 1. Before logging in, obtain a CSRF token bound to the current session.
	resp, err := client.Get(srv.URL + "/csrf")
	if err != nil {
		t.Fatalf("pre-login csrf request failed: %v", err)
	}
	before := readBody(t, resp)
	resp.Body.Close()
	preLoginCookies := resp.Cookies()

	if before == "" {
		t.Fatal("expected a pre-login CSRF token")
	}

	// 2. Log in while carrying that same session cookie.
	body, _ := json.Marshal(map[string]string{
		"email":    "rotate@example.com",
		"password": "rotate-pass-123",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range preLoginCookies {
		req.AddCookie(c)
	}

	loginResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected login 200, got %d", loginResp.StatusCode)
	}

	sessionCookies := loginResp.Cookies()
	if len(sessionCookies) == 0 {
		sessionCookies = preLoginCookies
	}

	// 3. Read the token again on the now-authenticated session.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/csrf", nil)
	for _, c := range sessionCookies {
		req2.AddCookie(c)
	}
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("post-login csrf request failed: %v", err)
	}
	after := readBody(t, resp2)
	resp2.Body.Close()

	if after == "" {
		t.Fatal("expected a post-login CSRF token")
	}
	if before == after {
		t.Error("CSRF token survived the login boundary; it must rotate on privilege change")
	}
}
