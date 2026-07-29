package booking_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/testsupport"
	"gorm.io/gorm"
)

func uptr(v uint) *uint { return &v }

// memDB returns a Postgres-backed DB in an isolated schema. Concurrency here is
// genuine: goroutines get separate pooled connections and contend on the row
// lock the guard UPDATE takes — the real production behaviour.
func memDB(t *testing.T) *gorm.DB {
	return testsupport.PostgresDB(t, &models.BookingInventory{})
}

func TestBookDirect_NoOversell_Capacity1(t *testing.T) {
	db := memDB(t)
	inv := models.BookingInventory{Capacity: 1, SlotID: uptr(1)}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	const N = 30
	var wg sync.WaitGroup
	var success int64
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := booking.BookDirect(db, inv.ID, 1); err == nil {
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	wg.Wait()

	if success != 1 {
		t.Fatalf("capacity=1 but %d bookings succeeded", success)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Booked != 1 {
		t.Fatalf("booked=%d, want 1", got.Booked)
	}
}

func TestBookDirect_NoOversell_CapacityN(t *testing.T) {
	db := memDB(t)
	const cap = 5
	inv := models.BookingInventory{Capacity: cap, TierID: uptr(1)}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	const N = 40
	var wg sync.WaitGroup
	var success int64
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := booking.BookDirect(db, inv.ID, 1); err == nil {
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	wg.Wait()

	if success != cap {
		t.Fatalf("capacity=%d but %d bookings succeeded", cap, success)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Booked != cap {
		t.Fatalf("booked=%d, want %d", got.Booked, cap)
	}
}

func TestEnsureInventory_Idempotent(t *testing.T) {
	db := memDB(t)

	a, err := booking.EnsureInventory(db, booking.SlotOwner(7), 3)
	if err != nil {
		t.Fatalf("first EnsureInventory: %v", err)
	}
	b, err := booking.EnsureInventory(db, booking.SlotOwner(7), 3)
	if err != nil {
		t.Fatalf("second EnsureInventory: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("EnsureInventory returned different rows: %d vs %d", a.ID, b.ID)
	}
	var count int64
	db.Model(&models.BookingInventory{}).Where("slot_id = ?", 7).Count(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 inventory row, got %d", count)
	}
}

func TestSetCapacity_RefusesBelowBooked(t *testing.T) {
	db := memDB(t)
	inv := models.BookingInventory{Capacity: 5, Booked: 3, SeatID: uptr(1)}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := booking.SetCapacity(db, inv.ID, 2); err != booking.ErrCapacityBelow {
		t.Fatalf("shrink below booked: got %v, want ErrCapacityBelow", err)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Capacity != 5 {
		t.Fatalf("capacity changed to %d despite rejection", got.Capacity)
	}

	if err := booking.SetCapacity(db, inv.ID, 3); err != nil {
		t.Fatalf("shrink to exactly booked: %v", err)
	}
}
