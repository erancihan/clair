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
	if err := appointments.CancelByToken(db, appt.CancelToken, time.Now()); err != nil {
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
	if err := appointments.CancelByToken(db, appt.CancelToken, time.Now()); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	if err := appointments.CancelByToken(db, appt.CancelToken, time.Now()); err != nil {
		t.Fatalf("second cancel should be a no-op, got: %v", err)
	}
	if err := appointments.CancelByToken(db, "nonexistent-token", time.Now()); err != nil {
		t.Fatalf("unknown token should be a no-op, got: %v", err)
	}
}

// TestBook_DSTFallBack_OneInstantPerWallTime: on the night clocks go back, the
// local hour 01:00–02:00 happens twice. The generator offers it once; the
// booking check must agree with the generator, or the second instant books a
// second, invisible, full-capacity slot for the same wall time.
//
// Europe/Lisbon leaves summer time on Sun 25 Oct 2026 at 02:00 WEST → 01:00 WET,
// so local 01:00 is both 00:00Z (WEST, first pass) and 01:00Z (WET, second).
func TestBook_DSTFallBack_OneInstantPerWallTime(t *testing.T) {
	db := bookDB(t)
	sched := &models.BookingSchedule{
		OwnerID: 1, Slug: "night-desk", Name: "Night Desk",
		Timezone: "Europe/Lisbon", SlotDurationMin: 60, Capacity: 1, WindowDays: 365,
	}
	if err := db.Create(sched).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.BookingAvailabilityRule{
		ScheduleID: sched.ID, Weekday: int(time.Sunday), StartMin: 0, EndMin: 3 * 60,
	}).Error; err != nil {
		t.Fatal(err)
	}
	now := at(t, "2026-10-01T00:00:00Z")
	first := at(t, "2026-10-25T00:00:00Z")  // 01:00 WEST
	second := at(t, "2026-10-25T01:00:00Z") // 01:00 WET

	_, errFirst := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: first, GuestName: "A"}, now)
	_, errSecond := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: second, GuestName: "B"}, now)

	okFirst, okSecond := errFirst == nil, errSecond == nil
	if okFirst == okSecond {
		t.Fatalf("exactly one instant of local 01:00 must be bookable; first=%v second=%v", errFirst, errSecond)
	}
	rejected := errSecond
	if okSecond {
		rejected = errFirst
	}
	if !errors.Is(rejected, appointments.ErrNotOpen) {
		t.Fatalf("the other instant must be ErrNotOpen, got %v", rejected)
	}
	var slots int64
	db.Model(&models.BookingSlot{}).Where("schedule_id = ?", sched.ID).Count(&slots)
	if slots != 1 {
		t.Fatalf("materialised %d slots for one wall time, want 1", slots)
	}
}

// TestCancelByToken_DoesNotRefundATicketOrder: fulfilled_id is the id of
// whatever a fulfiller created, in its own table. Appointment 7 and ticket 7 are
// different rows that share a number, so the refund lookup must be scoped to
// the order's domain.
func TestCancelByToken_DoesNotRefundATicketOrder(t *testing.T) {
	db := bookDB(t)
	sched := seedSchedule(t, db)
	now := at(t, "2026-03-01T00:00:00Z")

	appt, err := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: at(t, "2026-03-02T09:00:00Z"), GuestName: "Ada"}, now)
	if err != nil {
		t.Fatalf("book: %v", err)
	}

	// A paid ticket order whose fulfilled id happens to equal the appointment's.
	inv, err := booking.InventoryForSlot(db, appt.SlotID)
	if err != nil {
		t.Fatal(err)
	}
	order := models.BookingOrder{Domain: "event", OwnerRef: "guest:z", Status: "paid", TotalCents: 5000, Currency: "USD"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	item := models.BookingOrderItem{OrderID: order.ID, InventoryID: inv.ID, Qty: 1, PriceCents: 5000, Status: "fulfilled", FulfilledID: &appt.ID}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if err := appointments.CancelByToken(db, appt.CancelToken, time.Now()); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	var got models.BookingOrder
	db.First(&got, order.ID)
	if got.Status != "paid" {
		t.Fatalf("ticket order status = %q after cancelling an appointment, want paid", got.Status)
	}
	var refunds int64
	db.Model(&models.BookingPayment{}).Where("order_id = ? AND kind = 'refund'", order.ID).Count(&refunds)
	if refunds != 0 {
		t.Fatalf("%d refund rows written against a ticket order", refunds)
	}
}
