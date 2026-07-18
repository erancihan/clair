package test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/erancihan/clair/internal/utils"
)

// TestValKeyStatusDisabled verifies the fail-open contract: a nil client with
// no Valkey configured is reported as "disabled" rather than an error.
func TestValKeyStatusDisabled(t *testing.T) {
	t.Setenv("VALKEY_HOST", "")
	t.Setenv("VALKEY_PORT", "")

	if got := utils.ValKeyStatus(context.Background(), nil); got != utils.ValKeyStatusDisabled {
		t.Errorf("expected %q for an unconfigured nil client, got %q", utils.ValKeyStatusDisabled, got)
	}
}

// TestValKeyStatusDegradedWhenConfigured verifies that a nil client while
// Valkey is configured (host set) is reported as "degraded" — the dependency
// is expected but unavailable, distinct from "disabled".
func TestValKeyStatusDegradedWhenConfigured(t *testing.T) {
	t.Setenv("VALKEY_HOST", "127.0.0.1")

	if got := utils.ValKeyStatus(context.Background(), nil); got != utils.ValKeyStatusDegraded {
		t.Errorf("expected %q for a configured nil client, got %q", utils.ValKeyStatusDegraded, got)
	}
}

// TestNewValKeyClientUnconfigured verifies that an unset host yields a nil
// client so callers keep the "no Valkey, use the fallback path" behavior.
func TestNewValKeyClientUnconfigured(t *testing.T) {
	t.Setenv("VALKEY_HOST", "")
	t.Setenv("VALKEY_PORT", "")
	os.Unsetenv("VALKEY_HOST")
	os.Unsetenv("VALKEY_PORT")

	if client := utils.NewValKeyClient(context.Background()); client != nil {
		client.Close()
		t.Error("expected nil client when VALKEY_HOST and VALKEY_PORT are unset")
	}

	// A port without a host must not produce a ":6379" address; it stays nil.
	t.Setenv("VALKEY_PORT", "6379")
	os.Unsetenv("VALKEY_HOST")
	if client := utils.NewValKeyClient(context.Background()); client != nil {
		client.Close()
		t.Error("expected nil client when only VALKEY_PORT is set (empty host)")
	}
}

// TestHealthReportsValkeyStatus verifies the health endpoint surfaces the
// Valkey status and stays 200 even when Valkey is disabled.
func TestHealthReportsValkeyStatus(t *testing.T) {
	os.Unsetenv("VALKEY_HOST")
	os.Unsetenv("VALKEY_PORT")

	baseURL, teardown := setupTestServer(t, "5053")
	defer teardown()

	resp, err := http.Get(baseURL + "/api/health")
	if err != nil {
		t.Fatalf("Failed to make health request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("Failed to decode health response: %v", err)
	}

	if body["valkey"] != utils.ValKeyStatusDisabled {
		t.Errorf("expected valkey %q, got %q", utils.ValKeyStatusDisabled, body["valkey"])
	}
}
