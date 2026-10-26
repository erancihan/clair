package test

import (
	"testing"
	"time"

	"github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/aurenor"
	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/testsupport"
	"github.com/erancihan/clair/internal/ticketing"
	"gorm.io/gorm"
)

func count(t *testing.T, db *gorm.DB, model any, where string, args ...any) int64 {
	t.Helper()
	var n int64
	q := db.Model(model)
	if where != "" {
		q = q.Where(where, args...)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// TestPurgeDeletesHistoryByBusinessTime is B3: once a showing has started and
// a slot has ended past the retention window, their orders, payments, tickets,
// appointments and holds go. The catalog stays, and so does anything with a
// payment that may still be in flight.
func TestPurgeDeletesHistoryByBusinessTime(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	now := time.Now()
	if _, err := aurenor.Seed(db, now, "seed@aurenor.test"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	catalog := countSeed(db)

	// Two seated showings: one is bought and paid, one is left pending.
	var events []models.BookingEvent
	db.Joins("JOIN booking_ticket_tiers t ON t.event_id = booking_events.id AND t.kind = 'seated'").
		Order("booking_events.start_at").Limit(2).Find(&events)
	if len(events) != 2 {
		t.Fatalf("want two seated showings, got %d", len(events))
	}
	seatOf := func(eventID uint) uint {
		var seat models.BookingSeat
		db.Joins("JOIN booking_ticket_tiers t ON t.id = booking_seats.tier_id").
			Where("t.event_id = ?", eventID).Order("booking_seats.id").First(&seat)
		return seat.ID
	}
	paidBuyer := ticketing.Buyer{OwnerRef: "guest:paid", Name: "Paid"}
	if _, _, err := ticketing.HoldSeatGroup(db, events[0].ID, []uint{seatOf(events[0].ID)}, paidBuyer, time.Hour, now, 0); err != nil {
		t.Fatalf("hold paid: %v", err)
	}
	if _, err := ticketing.Checkout(db, events[0].ID, paidBuyer, now); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	pendingOrder, _, err := ticketing.HoldSeatGroup(db, events[1].ID, []uint{seatOf(events[1].ID)},
		ticketing.Buyer{OwnerRef: "guest:pending"}, time.Hour, now, 0)
	if err != nil {
		t.Fatalf("hold pending: %v", err)
	}

	// One free appointment.
	var sched models.BookingSchedule
	db.Where("slug = ?", "bureau-id-card-renewal").First(&sched)
	open, err := appointments.OpenSlotsFor(db, &sched, now, now.AddDate(0, 0, 7), now)
	if err != nil || len(open) == 0 {
		t.Fatalf("open slots: %d %v", len(open), err)
	}
	if _, err := appointments.Book(db, appointments.BookInput{Schedule: &sched, StartAt: open[0].StartAt, GuestName: "Guest"}, now); err != nil {
		t.Fatalf("book: %v", err)
	}

	// Nothing is old yet: a purge as of now deletes nothing.
	rep, err := booking.PurgeOldBookingData(db, now)
	if err != nil || rep.Total() != 0 {
		t.Fatalf("purge now: %+v %v", rep, err)
	}

	// Everything has happened, and the pending hold has long lapsed but the
	// order has not been settled by the reaper.
	later := events[1].StartAt.Add(booking.DefaultRetention + 24*time.Hour)
	if s := open[0].StartAt.Add(booking.DefaultRetention + 24*time.Hour); s.After(later) {
		later = s
	}
	rep, err = booking.PurgeOldBookingData(db, later)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if rep.Events != 1 || rep.Slots != 1 || rep.Appointments != 1 || rep.Tickets != 1 {
		t.Errorf("purge report %+v, want one event, one slot, one appointment, one ticket", rep)
	}

	if n := count(t, db, &models.BookingTicket{}, ""); n != 0 {
		t.Errorf("%d tickets left", n)
	}
	if n := count(t, db, &models.BookingAppointment{}, ""); n != 0 {
		t.Errorf("%d appointments left", n)
	}
	if n := count(t, db, &models.BookingSlot{}, ""); n != 0 {
		t.Errorf("%d slots left", n)
	}
	if n := count(t, db, &models.BookingPayment{}, ""); n != 0 {
		t.Errorf("%d payments left", n)
	}
	// The pending order is a money row that may still be paid for; it stays,
	// with its hold, until it settles.
	if n := count(t, db, &models.BookingOrder{}, "id = ?", pendingOrder.ID); n != 1 {
		t.Errorf("pending order deleted")
	}
	if n := count(t, db, &models.BookingOrder{}, "status <> 'pending'"); n != 0 {
		t.Errorf("%d settled orders left", n)
	}

	// The catalog is untouched.
	after := countSeed(db)
	if after.schedules != catalog.schedules || after.rules != catalog.rules || after.events != catalog.events ||
		after.tiers != catalog.tiers || after.seats != catalog.seats {
		t.Errorf("catalog changed: %+v then %+v", catalog, after)
	}

	// The reaper expires the pending order; the next purge takes it.
	if _, err := booking.ReapExpiredHolds(db, later); err != nil {
		t.Fatalf("reap: %v", err)
	}
	rep, err = booking.PurgeOldBookingData(db, later)
	if err != nil || rep.Events != 1 || rep.Orders != 1 {
		t.Fatalf("second purge %+v %v, want the settled order's event", rep, err)
	}
	if n := count(t, db, &models.BookingOrder{}, ""); n != 0 {
		t.Errorf("%d orders left after the second purge", n)
	}
	if n := count(t, db, &models.BookingHold{}, ""); n != 0 {
		t.Errorf("%d holds left", n)
	}

	// A third run finds nothing.
	if rep, err := booking.PurgeOldBookingData(db, later); err != nil || rep.Total() != 0 || rep.Events != 0 {
		t.Errorf("third purge %+v %v", rep, err)
	}
}
