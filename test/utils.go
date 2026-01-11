package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/cmd"
)

func setupTestServer(t *testing.T, port string) (string, func()) {
	// Setup: Initialize an in-memory SQLite database specifically for testing.
	os.Setenv("DB_PATH", "file::memory:?cache=shared")
	os.Setenv("SERVER_PORT", port)

	// Context to cancel server after test
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize server command in a goroutine
	go func() {
		// We ignore the error here because cancelling the context will cause Execute to return an error,
		// which is expected behavior during teardown.
		_ = cmd.ServerCmd(ctx).Execute()
	}()

	baseURL := "http://localhost:" + port
	client := &http.Client{Timeout: 5 * time.Second}

	// Wait loop: Block until server is ready
	serverReady := false
	maxAttempts := 20

	for i := 0; i < maxAttempts; i++ {
		// We try to hit the health endpoint to see if the server is up
		resp, err := client.Get(baseURL + "/api/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			serverReady = true
			resp.Body.Close()
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !serverReady {
		cancel() // Clean up if we failed
		t.Fatalf("Server failed to start on port %s within expected time", port)
	}

	// Return the cleanup function
	return baseURL, func() {
		cancel()
		// Optional: Add a small sleep to let the OS release the port if running tests rapidly
		time.Sleep(100 * time.Millisecond)
	}
}

func postJSON(client *http.Client, url string, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return http.ErrBodyNotAllowed
	}

	return nil
}
