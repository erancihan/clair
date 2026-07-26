package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	authentication "github.com/erancihan/clair/internal/server/authentication"
)

// noRedirectClient returns an http.Client that captures redirects instead of
// following them, so tests can assert on 302 responses.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// TestLoginInjectsIdentity verifies that after a successful login a handler
// behind AuthMiddleware reads the correct Identity via CurrentUser.
func TestLoginInjectsIdentity(t *testing.T) {
	srv := newAuthTestServer(t)
	user := createUser(t, srv.db, "identity@example.com", "s3cret-pass", authentication.RoleUser)

	cookies := loginJSON(t, srv, "identity@example.com", "s3cret-pass")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/whoami", nil)
	req.Header.Set("Accept", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("whoami request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		UserID uint   `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode whoami: %v", err)
	}

	if body.UserID != user.ID {
		t.Errorf("expected user id %d, got %d", user.ID, body.UserID)
	}
	if body.Role != authentication.RoleUser {
		t.Errorf("expected role %q, got %q", authentication.RoleUser, body.Role)
	}
}

// TestAdminMiddleware is a table-driven test of the AdminMiddleware's outcomes.
func TestAdminMiddleware(t *testing.T) {
	srv := newAuthTestServer(t)
	createUser(t, srv.db, "admin@example.com", "admin-pass-123", authentication.RoleAdmin)
	createUser(t, srv.db, "user@example.com", "user-pass-123", authentication.RoleUser)

	adminCookies := loginJSON(t, srv, "admin@example.com", "admin-pass-123")
	userCookies := loginJSON(t, srv, "user@example.com", "user-pass-123")

	tests := []struct {
		name       string
		cookies    []*http.Cookie
		accept     string
		wantStatus int
		wantLoc    string // substring expected in Location header (302 only)
	}{
		{
			name:       "admin passes",
			cookies:    adminCookies,
			wantStatus: http.StatusOK,
		},
		{
			name:       "regular user forbidden",
			cookies:    userCookies,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "unauthenticated browser redirects to login",
			cookies:    nil,
			accept:     "text/html",
			wantStatus: http.StatusFound,
			wantLoc:    "/login?next=",
		},
		{
			name:       "unauthenticated JSON caller gets 401",
			cookies:    nil,
			accept:     "application/json",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin", nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			for _, c := range tc.cookies {
				req.AddCookie(c)
			}

			resp, err := noRedirectClient().Do(req)
			if err != nil {
				t.Fatalf("admin request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("expected status %d, got %d", tc.wantStatus, resp.StatusCode)
			}

			if tc.wantLoc != "" {
				loc := resp.Header.Get("Location")
				if !strings.Contains(loc, tc.wantLoc) {
					t.Errorf("expected Location to contain %q, got %q", tc.wantLoc, loc)
				}
			}
		})
	}
}

// TestAuthMiddlewareDeletedUser verifies that a valid session cookie whose backing
// user row has been deleted is treated as unauthorized.
func TestAuthMiddlewareDeletedUser(t *testing.T) {
	srv := newAuthTestServer(t)
	user := createUser(t, srv.db, "ghost@example.com", "ghost-pass-123", authentication.RoleUser)

	cookies := loginJSON(t, srv, "ghost@example.com", "ghost-pass-123")

	// Hard-delete the user row so the session id no longer resolves.
	if err := srv.db.Unscoped().Delete(&user).Error; err != nil {
		t.Fatalf("failed to delete user: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/whoami", nil)
	req.Header.Set("Accept", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("whoami request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for deleted user, got %d", resp.StatusCode)
	}
}
