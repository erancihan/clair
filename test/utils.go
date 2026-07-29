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
	"github.com/erancihan/clair/internal/database"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// defaultTestDatabaseURL points at the Postgres service defined in
// docker-compose.dev.yaml. Override it with the DATABASE_URL environment
// variable to run the tests against a different Postgres instance.
const defaultTestDatabaseURL = "postgres://clair:clair@localhost:5432/clair?sslmode=disable"

func setupTestServer(t *testing.T, port string) (string, func()) {
	// Setup: point the server at a real Postgres database for testing.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = defaultTestDatabaseURL
	}
	os.Setenv("DATABASE_URL", dsn)
	os.Setenv("SERVER_PORT", port)

	// Reset the schema so each run starts from a clean slate; the server
	// re-creates the tables via AutoMigrate on startup.
	resetTestDatabase(t, dsn)

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

// resetTestDatabase drops the tables managed by the app so that tests are
// idempotent even when run repeatedly against a persistent Postgres instance.
func resetTestDatabase(t *testing.T, dsn string) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to connect to test database at %s: %v", dsn, err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("failed to access test database handle: %v", err)
	}
	defer sqlDB.Close()

	dropAppTables(t, db)
}

// dropAppTables drops every table under migration, newest domain first.
//
// It reads the migration set from internal/database rather than naming models,
// so a domain that adds a table gets it reset here for free - no edit to this
// fixture, which every domain would otherwise have to make.
func dropAppTables(t *testing.T, db *gorm.DB) {
	t.Helper()

	// Drop in reverse: domain tables may carry foreign keys onto the shared
	// identity table, and a referenced table cannot be dropped first.
	appModels := database.MigrationModels()
	for i := len(appModels) - 1; i >= 0; i-- {
		if err := db.Migrator().DropTable(appModels[i]); err != nil {
			t.Fatalf("failed to reset test database: %v", err)
		}
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
