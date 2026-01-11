package test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/cmd"
)

func TestServerInit(t *testing.T) {
	// Setup: Initialize an in-memory SQLite database specifically for testing.
	// "file::memory:?cache=shared" ensures it lives in RAM.
	os.Setenv("DB_PATH", "file::memory:?cache=shared")
	os.Setenv("SERVER_PORT", "5050") // Use a test-specific port

	// init context to cancel server after test
	context, cancel := context.WithCancel(context.Background())
	defer cancel()

	// initialize server command, which is a cobra command, and have it run on a goroutine
	go func() {
		err := cmd.ServerCmd(context).Execute()
		if err != nil {
			t.Errorf("Failed to start server command: %v", err)
		}
	}()

	t.Run("Check Server starts healthy", func(t *testing.T) {
		// wait for server to start and respond to /health endpoint
		req, err := http.NewRequest(http.MethodGet, "http://localhost:5050/health", nil)
		if err != nil {
			t.Fatalf("Failed to create health check request: %v", err)
		}

		client := &http.Client{}
		maxAttempts := 10

		for range maxAttempts {
			resp, err := client.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				// server is up, and healthy
				// no need to check response body for this test, as long as we get 200 OK
				return
			} else {
				// wait before retrying

				sleepDuration := 500 // milliseconds
				time.Sleep(time.Duration(sleepDuration) * time.Millisecond)
			}
		}

		t.Fatalf("Server did not start within expected time")
	})

	// cleanup: cancel context to stop server
	cancel()
}
