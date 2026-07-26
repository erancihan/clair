package test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/database/models"
	authentication "github.com/erancihan/clair/internal/server/authentication"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// bcryptHashPattern matches a bcrypt hash, so a test can assert that no response
// body carries one regardless of the field name it might be nested under.
var bcryptHashPattern = regexp.MustCompile(`\$2[aby]?\$\d{2}\$`)

// openTestDB connects to the test database without resetting it, for tests that
// need to inspect or adjust rows while a server is running.
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = defaultTestDatabaseURL
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to connect to test database: %v", err)
	}
	return db
}

// TestUsersEndpointRequiresAdminAndHidesPasswords exercises the real wired route
// through the full server. Listing accounts is an administrative capability, and
// no response may ever carry a password hash.
func TestUsersEndpointRequiresAdminAndHidesPasswords(t *testing.T) {
	baseURL, teardown := setupTestServer(t, "5054")
	defer teardown()

	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	const email = "listing@example.com"
	const password = "listing-pass-123"

	if err := postJSON(client, baseURL+"/api/v1/auth/register", map[string]string{
		"username": "listing",
		"email":    email,
		"password": password,
	}); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	get := func(t *testing.T, cookies []*http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/users/", nil)
		req.Header.Set("Accept", "application/json")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("users request failed: %v", err)
		}
		return resp
	}

	t.Run("unauthenticated JSON caller is rejected", func(t *testing.T) {
		resp := get(t, nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 for an unauthenticated caller, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if bcryptHashPattern.Match(body) {
			t.Errorf("rejection response leaked a password hash: %s", body)
		}
	})

	// Log in as the freshly registered account, which defaults to the user role.
	loginBody, _ := json.Marshal(map[string]string{"email": email, "password": password})
	loginResp, err := client.Post(baseURL+"/api/v1/auth/login", "application/json", strings.NewReader(string(loginBody)))
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected login 200, got %d", loginResp.StatusCode)
	}
	cookies := loginResp.Cookies()

	t.Run("regular user is forbidden", func(t *testing.T) {
		resp := get(t, cookies)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 for a non-admin, got %d", resp.StatusCode)
		}
	})

	// Promote the account. AuthMiddleware re-reads the row on every request, so the
	// existing session immediately reflects the new role.
	db := openTestDB(t)
	if err := db.Model(&models.User{}).Where("email = ?", email).
		Update("role", authentication.RoleAdmin).Error; err != nil {
		t.Fatalf("failed to promote user to admin: %v", err)
	}

	t.Run("admin gets the listing without any password", func(t *testing.T) {
		resp := get(t, cookies)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for an admin, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		payload := string(body)

		if bcryptHashPattern.MatchString(payload) {
			t.Errorf("users listing leaked a password hash: %s", payload)
		}
		if strings.Contains(strings.ToLower(payload), "password") {
			t.Errorf("users listing still carries a password field: %s", payload)
		}

		// The listing must still be useful.
		if !strings.Contains(payload, email) {
			t.Errorf("expected the listing to include %q, got: %s", email, payload)
		}
		if !strings.Contains(payload, authentication.RoleAdmin) {
			t.Errorf("expected the listing to include the role, got: %s", payload)
		}
	})
}
