package test

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	authentication "github.com/erancihan/clair/internal/server/authentication"
)

// sidCookie returns the "sid" cookie from a response, or nil if absent.
func sidCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "sid" {
			return c
		}
	}
	return nil
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	return string(b)
}

// TestSessionIDMintAndReuse verifies that the first guest request mints a
// long-lived, HttpOnly, site-wide "sid" cookie and that a subsequent request
// carrying that cookie reuses it (no new mint), with OwnerRef returning
// "guest:<sid>".
func TestSessionIDMintAndReuse(t *testing.T) {
	srv := newAuthTestServer(t)

	// First request: no sid cookie -> a fresh one is minted.
	resp1, err := noRedirectClient().Get(srv.URL + "/owner")
	if err != nil {
		t.Fatalf("owner request failed: %v", err)
	}
	body1 := readBody(t, resp1)
	resp1.Body.Close()

	sid := sidCookie(resp1)
	if sid == nil {
		t.Fatal("expected first response to set a sid cookie")
	}
	if !sid.HttpOnly {
		t.Error("sid cookie should be HttpOnly")
	}
	if sid.Path != "/" {
		t.Errorf("sid cookie Path should be \"/\", got %q", sid.Path)
	}
	if sid.MaxAge <= 0 {
		t.Errorf("sid cookie should be long-lived (positive MaxAge), got %d", sid.MaxAge)
	}
	if want := "guest:" + sid.Value; body1 != want {
		t.Errorf("expected OwnerRef %q, got %q", want, body1)
	}

	// Second request: carry the sid cookie -> it is reused, not re-minted.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/owner", nil)
	req2.AddCookie(&http.Cookie{Name: "sid", Value: sid.Value})
	resp2, err := noRedirectClient().Do(req2)
	if err != nil {
		t.Fatalf("second owner request failed: %v", err)
	}
	body2 := readBody(t, resp2)
	resp2.Body.Close()

	if got := sidCookie(resp2); got != nil {
		t.Errorf("expected no new sid cookie on reuse, got %q", got.Value)
	}
	if want := "guest:" + sid.Value; body2 != want {
		t.Errorf("expected reused OwnerRef %q, got %q", want, body2)
	}
}

// TestOwnerRefAuthenticated verifies that an authenticated caller's OwnerRef is
// "user:<id>" rather than a guest reference.
func TestOwnerRefAuthenticated(t *testing.T) {
	srv := newAuthTestServer(t)
	user := createUser(t, srv.db, "owner@example.com", "owner-pass-123", authentication.RoleUser)

	cookies := loginJSON(t, srv, "owner@example.com", "owner-pass-123")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/owner-auth", nil)
	req.Header.Set("Accept", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("owner-auth request failed: %v", err)
	}
	body := readBody(t, resp)
	resp.Body.Close()

	if !strings.HasPrefix(body, "user:") {
		t.Fatalf("expected a user owner reference, got %q", body)
	}
	if want := "user:"; !strings.HasPrefix(body, want) || body == want {
		t.Fatalf("expected user:<id>, got %q", body)
	}
	_ = user
}

// TestSecureToken sanity-checks SecureToken: URL-safe, non-empty, and unique
// across calls.
func TestSecureToken(t *testing.T) {
	urlSafe := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		tok := authentication.SecureToken()
		if tok == "" {
			t.Fatal("SecureToken returned empty string")
		}
		if !urlSafe.MatchString(tok) {
			t.Fatalf("SecureToken produced a non-URL-safe token: %q", tok)
		}
		if seen[tok] {
			t.Fatalf("SecureToken produced a duplicate token: %q", tok)
		}
		seen[tok] = true
	}
}
