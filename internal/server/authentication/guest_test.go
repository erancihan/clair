package authentication

import (
	"context"
	"testing"

	"gorm.io/gorm"
)

func TestRegisterGuestMigratorReplacesByName(t *testing.T) {
	guestMigratorsMu.Lock()
	guestMigrators = nil
	guestMigratorsMu.Unlock()

	noop := func(context.Context, *gorm.DB, string, string) error { return nil }
	RegisterGuestMigrator("booking", noop)
	RegisterGuestMigrator("booking", noop)
	RegisterGuestMigrator("games", noop)

	guestMigratorsMu.RLock()
	defer guestMigratorsMu.RUnlock()
	if len(guestMigrators) != 2 {
		t.Fatalf("registering the same name twice left %d migrators, want 2", len(guestMigrators))
	}
}
