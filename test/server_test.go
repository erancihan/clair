package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestHealthCheck(t *testing.T) {
	// Use a specific port for this test to avoid conflicts if run in parallel
	baseURL, teardown := setupTestServer(t, "5051")
	defer teardown()

	t.Run("Health Endpoint Returns 200", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/api/health")
		if err != nil {
			t.Fatalf("Failed to make health request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}
	})
}

func TestAuthentication(t *testing.T) {
	// Use a different port for isolation
	baseURL, teardown := setupTestServer(t, "5052")
	defer teardown()

	client := &http.Client{Timeout: 5 * time.Second}
	email := "testuser@example.com"
	password := "securepassword123"

	// Variable to store the cookies across sub-tests
	var sessionCookies []*http.Cookie

	// 1. Unauthenticated Access Step
	t.Run("Fail to access protected route without token", func(t *testing.T) {
		protectedReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/protected", nil)
		resp, err := client.Do(protectedReq)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected 401 Unauthorized, got %d", resp.StatusCode)
		}
	})

	// 2. Registration Step
	t.Run("Register new user", func(t *testing.T) {
		registerPayload := map[string]string{
			"email":    email,
			"password": password,
		}
		if err := postJSON(client, baseURL+"/api/v1/auth/register", registerPayload); err != nil {
			t.Fatalf("Registration failed: %v", err)
		}
	})

	// 3. Login Step
	t.Run("Login and receive token", func(t *testing.T) {
		loginPayload := map[string]string{
			"email":    email,
			"password": password,
		}
		loginBody, err := json.Marshal(loginPayload)
		if err != nil {
			t.Fatal(err)
		}

		resp, err := client.Post(baseURL+"/api/v1/auth/login", "application/json", bytes.NewBuffer(loginBody))
		if err != nil {
			t.Fatalf("Login request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Login failed with status %d: %s", resp.StatusCode, string(body))
		}

		// authentication is done via session cookie, so, get the cookie from response
		cookies := resp.Cookies()
		if len(cookies) == 0 {
			t.Fatal("Login response did not contain any cookies")
		}

		// Store the cookies found as the session cookies
		sessionCookies = cookies
	})

	// 4. Authenticated Access Step
	t.Run("Access protected route with cookie", func(t *testing.T) {
		if len(sessionCookies) == 0 {
			t.Skip("Skipping because no cookie was received in previous step")
		}

		authReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/protected", nil)
		for _, cookie := range sessionCookies {
			authReq.AddCookie(cookie)
		}

		resp, err := client.Do(authReq)
		if err != nil {
			t.Fatalf("Failed authenticated request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200 OK, got %d", resp.StatusCode)
		}
	})
}

// Helper to send POST JSON requests easily
