package appointments_test

import (
	"testing"

	"github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/database/models"
)

// TestOpenSlotsFor_UsesSlotCapacity: a slot that has been materialised carries
// its own capacity, and that is the number the booking guard enforces. Reporting
// the schedule's current capacity instead would advertise seats the guard will
// refuse (or hide ones it would accept).
func TestOpenSlotsFor_UsesSlotCapacity(t *testing.T) {
	db := bookDB(t)
	sched := seedSchedule(t, db) // Mon 09:00–12:00, 30 min, capacity 1
	now := at(t, "2026-03-01T00:00:00Z")
	nine := at(t, "2026-03-02T09:00:00Z")

	if _, err := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: nine, GuestName: "Ada"}, now); err != nil {
		t.Fatalf("book: %v", err)
	}
	// The host later raises the schedule's capacity; the 09:00 slot was
	// materialised at 1 and stays 1.
	if err := db.Model(&models.BookingSchedule{}).Where("id = ?", sched.ID).Update("capacity", 3).Error; err != nil {
		t.Fatal(err)
	}
	sched.Capacity = 3

	slots, err := appointments.OpenSlotsFor(db, sched, at(t, "2026-03-02T00:00:00Z"), at(t, "2026-03-03T00:00:00Z"), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range slots {
		if s.StartAt.Equal(nine) {
			t.Fatalf("09:00 is full at its materialised capacity of 1, but was reported with remaining=%d", s.Remaining)
		}
	}
	if len(slots) != 5 || slots[0].Remaining != 3 {
		t.Fatalf("want 5 open slots at the new capacity 3, got %d (first remaining=%d)", len(slots), slots[0].Remaining)
	}
}
