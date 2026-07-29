// Package testsupport provides shared helpers for integration tests that need a
// real PostgreSQL database (the app is PostgreSQL-only; the booking kernel relies
// on partial indexes, FOR UPDATE [SKIP LOCKED], and RETURNING).
//
// It reads DATABASE_URL (the repo/CI convention; CI provides a postgres:16
// service), falling back to TEST_DATABASE_URL for local runs. When neither is
// set, tests that call PostgresDB are skipped (not failed).
package testsupport

import (
	"os"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// dsn resolves the Postgres connection string: DATABASE_URL first (matches CI and
// the app), then TEST_DATABASE_URL.
func dsn() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return os.Getenv("TEST_DATABASE_URL")
}

// PostgresDB returns a *gorm.DB bound to a fresh, isolated schema in the Postgres
// pointed to by DATABASE_URL (or TEST_DATABASE_URL), migrated for the given
// models. The schema is dropped on test cleanup, so tests are independent and can
// run concurrently.
func PostgresDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()

	base := dsn()
	if base == "" {
		t.Skip("DATABASE_URL/TEST_DATABASE_URL not set; skipping Postgres integration test")
	}

	schema := schemaName(t.Name())

	// Bootstrap connection (default search_path) to (re)create the schema.
	boot, err := gorm.Open(postgres.Open(base), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connect (bootstrap): %v", err)
	}
	if err := boot.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := boot.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}

	// Working connection pinned to the schema via search_path (applies to every
	// pooled connection, so concurrent goroutines all target the same schema).
	db, err := gorm.Open(postgres.Open(withSearchPath(base, schema)),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connect (schema): %v", err)
	}
	if len(models) > 0 {
		if err := db.AutoMigrate(models...); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}

	t.Cleanup(func() {
		_ = boot.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		if sqlDB, err := boot.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// schemaName derives a safe, unique-per-test Postgres schema identifier.
func schemaName(testName string) string {
	var b strings.Builder
	b.WriteString("t_")
	for _, r := range strings.ToLower(testName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 60 { // Postgres identifiers max 63 bytes
		s = s[:60]
	}
	return s
}

func withSearchPath(base, schema string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "search_path=" + schema
}
