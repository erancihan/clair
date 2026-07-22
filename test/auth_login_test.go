package test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	authentication "github.com/erancihan/clair/internal/server/authentication"
)

// TestLoginJSONContractPreserved verifies the existing JSON login contract: a
// JSON request receives a bare 200 with a session cookie and no redirect.
func TestLoginJSONContractPreserved(t *testing.T) {
	srv := newAuthTestServer(t)
	createUser(t, srv.db, "json@example.com", "json-pass-123", authentication.RoleUser)

	cookies := loginJSON(t, srv, "json@example.com", "json-pass-123")
	if len(cookies) == 0 {
		t.Fatal("expected JSON login to set a session cookie")
	}
}

// TestLoginFormRedirect exercises the browser (form) login path and the safeNext
// return-path validation.
func TestLoginFormRedirect(t *testing.T) {
	srv := newAuthTestServer(t)
	createUser(t, srv.db, "form@example.com", "form-pass-123", authentication.RoleUser)

	tests := []struct {
		name    string
		next    string
		wantLoc string
	}{
		{name: "no next falls back to dashboard", next: "", wantLoc: "/dashboard"},
		{name: "valid same-origin path is honored", next: "/account/orders", wantLoc: "/account/orders"},
		{name: "protocol-relative url is rejected", next: "//evil.example.com", wantLoc: "/dashboard"},
		{name: "absolute url is rejected", next: "http://evil.example.com/x", wantLoc: "/dashboard"},
		{name: "backslash trick is rejected", next: "/\\evil.example.com", wantLoc: "/dashboard"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{
				"email":    {"form@example.com"},
				"password": {"form-pass-123"},
			}
			if tc.next != "" {
				form.Set("next", tc.next)
			}

			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			resp, err := noRedirectClient().Do(req)
			if err != nil {
				t.Fatalf("form login failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusFound {
				t.Fatalf("expected 302 redirect, got %d", resp.StatusCode)
			}
			if loc := resp.Header.Get("Location"); loc != tc.wantLoc {
				t.Errorf("expected redirect to %q, got %q", tc.wantLoc, loc)
			}
		})
	}
}

// TestLoginPageRendersNext verifies the login page renders a validated `next`
// value into a hidden field, and drops it when the value is unsafe.
func TestLoginPageRendersNext(t *testing.T) {
	srv := newAuthTestServer(t)

	t.Run("safe next is rendered", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/login-page?next=" + url.QueryEscape("/account"))
		if err != nil {
			t.Fatalf("login page request failed: %v", err)
		}
		defer resp.Body.Close()
		body := readBody(t, resp)

		if !strings.Contains(body, `name="next"`) {
			t.Errorf("expected a hidden next field in the login page, got:\n%s", body)
		}
		if !strings.Contains(body, `value="/account"`) {
			t.Errorf("expected the next value to be rendered, got:\n%s", body)
		}
	})

	t.Run("unsafe next is dropped", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/login-page?next=" + url.QueryEscape("//evil.example.com"))
		if err != nil {
			t.Fatalf("login page request failed: %v", err)
		}
		defer resp.Body.Close()
		body := readBody(t, resp)

		if strings.Contains(body, `name="next"`) {
			t.Errorf("expected no next field for an unsafe value, got:\n%s", body)
		}
	})
}
