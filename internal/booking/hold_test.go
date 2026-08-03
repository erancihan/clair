package booking_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/testsupport"
	"gorm.io/gorm"
)

// holdDB builds the application's full schema, so the partial unique index that
// backs hold idempotency is present and these tests exercise the real one.
func holdDB(t *testing.T) *gorm.DB {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	return db
}

func seedInv(t *testing.T, db *gorm.DB, capacity int) *models.BookingInventory {
	t.Helper()
	inv := models.BookingInventory{Capacity: capacity, TierID: uptr(1)}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	return &inv
}

func TestHoldUnits_NoOversell(t *testing.T) {
	db := holdDB(t)
	const cap = 3
	inv := seedInv(t, db, cap)
	now := time.Now()

	const N = 30
	var wg sync.WaitGroup
	var success int64
	for i := 0; i < N; i++ {
		owner := fmt.Sprintf("owner-%d", i) // distinct owners → no idempotent merge
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := booking.HoldUnits(db, inv.ID, 1, owner, nil, time.Hour, now); err == nil {
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	wg.Wait()

	if success != cap {
		t.Fatalf("capacity=%d but %d holds succeeded", cap, success)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Held != cap || got.Booked != 0 {
		t.Fatalf("held=%d booked=%d, want held=%d booked=0", got.Held, got.Booked, cap)
	}
}

func TestHoldUnits_IdempotentSameOwner(t *testing.T) {
	db := holdDB(t)
	inv := seedInv(t, db, 5)
	now := time.Now()

	a, err := booking.HoldUnits(db, inv.ID, 1, "guest:x", nil, time.Hour, now)
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}
	b, err := booking.HoldUnits(db, inv.ID, 1, "guest:x", nil, time.Hour, now)
	if err != nil {
		t.Fatalf("second hold: %v", err)
	}
	if a.Token != b.Token {
		t.Fatalf("same owner got two holds: %s vs %s", a.Token, b.Token)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Held != 1 {
		t.Fatalf("held=%d, want 1 (idempotent)", got.Held)
	}
}

func TestHold_ReclaimExpired(t *testing.T) {
	db := holdDB(t)
	inv := seedInv(t, db, 1) // capacity 1
	base := time.Now()

	a, err := booking.HoldUnits(db, inv.ID, 1, "owner-A", nil, 1*time.Minute, base)
	if err != nil {
		t.Fatalf("hold A: %v", err)
	}

	// Two minutes later A has expired; B's hold should reclaim it and succeed.
	later := base.Add(2 * time.Minute)
	b, err := booking.HoldUnits(db, inv.ID, 1, "owner-B", nil, 1*time.Minute, later)
	if err != nil {
		t.Fatalf("hold B should reclaim expired A, got: %v", err)
	}

	var reloadedA models.BookingHold
	db.Where("token = ?", a.Token).First(&reloadedA)
	if reloadedA.Status != "released" {
		t.Fatalf("expired hold A status=%q, want released", reloadedA.Status)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Held != 1 || got.Booked != 0 {
		t.Fatalf("held=%d booked=%d, want held=1 (B) booked=0", got.Held, got.Booked)
	}
	if b.Status != "active" {
		t.Fatalf("hold B status=%q, want active", b.Status)
	}
}

func TestCommit_HeldToBooked_Idempotent(t *testing.T) {
	db := holdDB(t)
	inv := seedInv(t, db, 1)
	now := time.Now()

	h, err := booking.HoldUnits(db, inv.ID, 1, "owner", nil, time.Hour, now)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := booking.Commit(db, h.Token, now); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Held != 0 || got.Booked != 1 {
		t.Fatalf("after commit held=%d booked=%d, want held=0 booked=1", got.Held, got.Booked)
	}
	// idempotent replay
	if err := booking.Commit(db, h.Token, now); err != nil {
		t.Fatalf("double commit should be a no-op, got: %v", err)
	}
	db.First(&got, inv.ID)
	if got.Booked != 1 {
		t.Fatalf("double commit changed booked to %d", got.Booked)
	}
}

func TestCommit_Expired(t *testing.T) {
	db := holdDB(t)
	inv := seedInv(t, db, 1)
	base := time.Now()

	h, err := booking.HoldUnits(db, inv.ID, 1, "owner", nil, 1*time.Minute, base)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := booking.Commit(db, h.Token, base.Add(2*time.Minute)); err != booking.ErrHoldExpired {
		t.Fatalf("commit expired: got %v, want ErrHoldExpired", err)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Booked != 0 {
		t.Fatalf("expired commit booked a unit: booked=%d", got.Booked)
	}
}

func TestRelease_FreesHold(t *testing.T) {
	db := holdDB(t)
	inv := seedInv(t, db, 1)
	now := time.Now()

	h, err := booking.HoldUnits(db, inv.ID, 1, "owner", nil, time.Hour, now)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := booking.Release(db, h.Token, now); err != nil {
		t.Fatalf("release: %v", err)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Held != 0 {
		t.Fatalf("after release held=%d, want 0", got.Held)
	}
	if err := booking.Release(db, h.Token, now); err != nil {
		t.Fatalf("second release should be a no-op, got: %v", err)
	}
}
