package booking_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/testsupport"
	"github.com/jackc/pgx/v5"
)

// TestInventoryChangeNotifies is the contract the live seat map rests on: a
// change to what is booked, held or blocked announces itself, whichever process
// made it.
//
// It listens the way the hub does — a real connection on the real channel —
// rather than asserting the trigger exists, because a trigger that fires into
// the void would pass that weaker test.
func TestInventoryChangeNotifies(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install triggers: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("listener connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "LISTEN "+booking.NotifyChannel); err != nil {
		t.Fatalf("listen: %v", err)
	}

	// A pooled inventory row standing in for a GA tier.
	inv := models.BookingInventory{Capacity: 10}
	tierID := uint(4242)
	inv.TierID = &tierID
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	tier := models.BookingTicketTier{Name: "GA", Kind: "ga", PriceCents: 1000, EventID: 99}
	tier.ID = tierID
	if err := db.Create(&tier).Error; err != nil {
		t.Fatalf("create tier: %v", err)
	}

	if _, err := booking.HoldUnits(db, inv.ID, 2, "guest:listener", nil, time.Minute, time.Now()); err != nil {
		t.Fatalf("hold: %v", err)
	}

	// Other schemas share this channel, so read past anything that is not ours.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no notification for the held inventory")
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
		note, err := conn.WaitForNotification(waitCtx)
		waitCancel()
		if err != nil {
			t.Fatalf("wait for notification: %v", err)
		}
		if note.Payload == "event:99" {
			return
		}
	}
}

// TestInstallNotifyTriggersIsIdempotent: it runs on every boot.
func TestInstallNotifyTriggersIsIdempotent(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	for i := 0; i < 3; i++ {
		if err := booking.InstallNotifyTriggers(db); err != nil {
			t.Fatalf("install %d: %v", i+1, err)
		}
	}
}

// TestRuleChangeNotifies: availability is computed from rules, so editing them
// changes what is bookable even though no inventory moved. A screen showing that
// schedule has to hear about it.
func TestRuleChangeNotifies(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install triggers: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("listener connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "LISTEN "+booking.NotifyChannel); err != nil {
		t.Fatalf("listen: %v", err)
	}

	rule := models.BookingAvailabilityRule{ScheduleID: 4242, Weekday: 1, StartMin: 540, EndMin: 1020}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no notification for the new rule")
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
		note, err := conn.WaitForNotification(waitCtx)
		waitCancel()
		if err != nil {
			t.Fatalf("wait for notification: %v", err)
		}
		if note.Payload == "schedule:4242" {
			return
		}
	}
}

// listenFor opens a raw LISTEN connection on the notify channel and returns a
// function that waits, up to 10s, for a payload equal to want.
func listenFor(t *testing.T) (waitFor func(want string)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("listener connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, "LISTEN "+booking.NotifyChannel); err != nil {
		t.Fatalf("listen: %v", err)
	}
	return func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if time.Now().After(deadline) {
				t.Fatalf("no notification %q", want)
			}
			waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
			note, err := conn.WaitForNotification(waitCtx)
			waitCancel()
			if err != nil {
				t.Fatalf("wait for notification: %v", err)
			}
			if note.Payload == want {
				return
			}
		}
	}
}

// TestCapacityChangeNotifies: a capacity edit changes what is open without any
// unit moving; a screen showing that inventory has to hear about it.
func TestCapacityChangeNotifies(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install triggers: %v", err)
	}
	waitFor := listenFor(t)

	tier := models.BookingTicketTier{Name: "GA", Kind: "ga", PriceCents: 1000, EventID: 4343}
	if err := db.Create(&tier).Error; err != nil {
		t.Fatal(err)
	}
	inv := models.BookingInventory{Capacity: 10, TierID: &tier.ID}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	if err := booking.SetCapacity(db, inv.ID, 12); err != nil {
		t.Fatalf("set capacity: %v", err)
	}
	waitFor("event:4343")
}

// TestInventoryInsertNotifies: the first booking of a slot materialises its
// inventory row. That insert is the change the screen is waiting for.
func TestInventoryInsertNotifies(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install triggers: %v", err)
	}
	waitFor := listenFor(t)

	slot := models.BookingSlot{ScheduleID: 7171, StartAt: time.Now().Add(time.Hour), EndAt: time.Now().Add(2 * time.Hour), Capacity: 1}
	if err := db.Create(&slot).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := booking.EnsureInventory(db, booking.SlotOwner(slot.ID), 1); err != nil {
		t.Fatalf("ensure inventory: %v", err)
	}
	waitFor("schedule:7171")
}
