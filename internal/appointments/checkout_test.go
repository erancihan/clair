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

func paidDB(t *testing.T) *gorm.DB {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	return db
}

func seedPaidSchedule(t *testing.T, db *gorm.DB) *models.BookingSchedule {
	t.Helper()
	sched := &models.BookingSchedule{
		OwnerID: 1, Slug: "visa", Name: "Visa Interview", Timezone: "UTC",
		SlotDurationMin: 30, Capacity: 1, WindowDays: 365,
		RequiresPayment: true, PriceCents: 5000, Currency: "USD", HoldTTLMin: 10,
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

func TestPaidAppointment_HoldCaptureFulfill(t *testing.T) {
	db := paidDB(t)
	sched := seedPaidSchedule(t, db)
	now := at(t, "2026-03-01T00:00:00Z")
	start := at(t, "2026-03-02T09:00:00Z")

	order, hold, err := appointments.HoldSlot(db, appointments.HoldInput{
		Schedule: sched, StartAt: start, OwnerRef: "guest:abc", GuestName: "Ada", GuestEmail: "ada@example.com",
	}, now)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if order.Status != "pending" || hold.Status != "active" {
		t.Fatalf("unexpected order/hold: %+v %+v", order, hold)
	}
	var inv models.BookingInventory
	db.First(&inv, hold.InventoryID)
	if inv.Held != 1 || inv.Booked != 0 {
		t.Fatalf("after hold held=%d booked=%d, want held=1 booked=0", inv.Held, inv.Booked)
	}

	// The slot is reserved during checkout: nobody else can take it.
	if _, _, err := appointments.HoldSlot(db, appointments.HoldInput{
		Schedule: sched, StartAt: start, OwnerRef: "guest:def",
	}, now); !errors.Is(err, booking.ErrSoldOut) {
		t.Fatalf("concurrent hold: got %v, want ErrSoldOut", err)
	}

	// Capture payment → appointment is created, order paid, slot committed.
	pay := models.BookingPayment{Provider: "mock", Kind: "capture", Status: "succeeded", AmountCents: 5000, Currency: "USD", IdempotencyKey: "evt-1"}
	if err := booking.CaptureOrder(db, order.ID, pay, now); err != nil {
		t.Fatalf("capture: %v", err)
	}
	var appts []models.BookingAppointment
	db.Where("schedule_id = ?", sched.ID).Find(&appts)
	if len(appts) != 1 || appts[0].GuestName != "Ada" || appts[0].Status != "confirmed" {
		t.Fatalf("expected 1 confirmed appointment for Ada, got %+v", appts)
	}
	var o models.BookingOrder
	db.First(&o, order.ID)
	if o.Status != "paid" {
		t.Fatalf("order status=%q, want paid", o.Status)
	}
	var item models.BookingOrderItem
	db.Where("order_id = ?", order.ID).First(&item)
	if item.Status != "fulfilled" || item.FulfilledID == nil {
		t.Fatalf("item not fulfilled: %+v", item)
	}
	db.First(&inv, hold.InventoryID)
	if inv.Held != 0 || inv.Booked != 1 {
		t.Fatalf("after capture held=%d booked=%d, want held=0 booked=1", inv.Held, inv.Booked)
	}

	// Duplicate webhook delivery is idempotent — no second appointment.
	if err := booking.CaptureOrder(db, order.ID, pay, now); err != nil {
		t.Fatalf("duplicate capture: %v", err)
	}
	db.Where("schedule_id = ?", sched.ID).Find(&appts)
	if len(appts) != 1 {
		t.Fatalf("duplicate capture created %d appointments, want 1", len(appts))
	}
}

func TestPaidAppointment_ReaperFreesAbandonedHold(t *testing.T) {
	db := paidDB(t)
	sched := seedPaidSchedule(t, db)
	now := at(t, "2026-03-01T00:00:00Z")
	start := at(t, "2026-03-02T09:00:00Z")

	_, hold, err := appointments.HoldSlot(db, appointments.HoldInput{
		Schedule: sched, StartAt: start, OwnerRef: "guest:a",
	}, now)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}

	// After the TTL, the reaper releases the abandoned hold and frees the slot.
	later := now.Add(11 * time.Minute)
	n, err := booking.ReapExpiredHolds(db, later)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if n < 1 {
		t.Fatalf("reaper released %d holds, want >= 1", n)
	}
	var inv models.BookingInventory
	db.First(&inv, hold.InventoryID)
	if inv.Held != 0 {
		t.Fatalf("after reap held=%d, want 0", inv.Held)
	}

	// The freed slot can now be held by someone else.
	if _, _, err := appointments.HoldSlot(db, appointments.HoldInput{
		Schedule: sched, StartAt: start, OwnerRef: "guest:c",
	}, later); err != nil {
		t.Fatalf("hold after reap: %v", err)
	}
}
