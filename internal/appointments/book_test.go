package appointments_test

import (
	"errors"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/testsupport"
	"gorm.io/gorm"
)

// bookDB builds the application's full schema, so both the free flow and the
// cancel-refund path (which reads booking_order_items) have their tables.
func bookDB(t *testing.T) *gorm.DB {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	return db
}

// seedSchedule creates a Mon 09:00–12:00, 30-min, capacity-1 UTC schedule.
func seedSchedule(t *testing.T, db *gorm.DB) *models.BookingSchedule {
	t.Helper()
	sched := &models.BookingSchedule{
		OwnerID: 1, Slug: "office-hours", Name: "Office Hours",
		Timezone: "UTC", SlotDurationMin: 30, Capacity: 1, WindowDays: 365,
	}
	if err := db.Create(sched).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	if err := db.Create(&models.BookingAvailabilityRule{
		ScheduleID: sched.ID, Weekday: int(time.Monday), StartMin: 9 * 60, EndMin: 12 * 60,
	}).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	return sched
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestBook_FreeFlow_HappyThenSoldOutThenCancel(t *testing.T) {
	db := bookDB(t)
	sched := seedSchedule(t, db)
	now := at(t, "2026-03-01T00:00:00Z")
	start := at(t, "2026-03-02T09:00:00Z") // Monday 09:00

	appt, err := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: start, GuestName: "Ada"}, now)
	if err != nil {
		t.Fatalf("first book: %v", err)
	}
	if appt.Status != "confirmed" || appt.CancelToken == "" {
		t.Fatalf("unexpected appt: %+v", appt)
	}

	// capacity 1 → the same slot is now sold out
	if _, err := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: start, GuestName: "Bob"}, now); !errors.Is(err, booking.ErrSoldOut) {
		t.Fatalf("second book: got %v, want ErrSoldOut", err)
	}

	// cancelling frees the slot
	if err := appointments.CancelByToken(db, appt.CancelToken); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: start, GuestName: "Cara"}, now); err != nil {
		t.Fatalf("re-book after cancel: %v", err)
	}
}

func TestBook_RejectsUnofferedTimes(t *testing.T) {
	db := bookDB(t)
	sched := seedSchedule(t, db)
	now := at(t, "2026-03-01T00:00:00Z")

	cases := []struct {
		name  string
		start time.Time
		want  error
	}{
		{"misaligned to grid", at(t, "2026-03-02T09:15:00Z"), appointments.ErrNotOpen},
		{"wrong weekday (Sunday)", at(t, "2026-03-01T09:00:00Z"), appointments.ErrNotOpen},
		{"outside rule window", at(t, "2026-03-02T13:00:00Z"), appointments.ErrNotOpen},
		{"in the past", at(t, "2026-02-01T09:00:00Z"), appointments.ErrPast},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: tc.start}, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBook_CancelByToken_Idempotent(t *testing.T) {
	db := bookDB(t)
	sched := seedSchedule(t, db)
	now := at(t, "2026-03-01T00:00:00Z")
	start := at(t, "2026-03-02T10:00:00Z")

	appt, err := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: start}, now)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if err := appointments.CancelByToken(db, appt.CancelToken); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	if err := appointments.CancelByToken(db, appt.CancelToken); err != nil {
		t.Fatalf("second cancel should be a no-op, got: %v", err)
	}
	if err := appointments.CancelByToken(db, "nonexistent-token"); err != nil {
		t.Fatalf("unknown token should be a no-op, got: %v", err)
	}
}
