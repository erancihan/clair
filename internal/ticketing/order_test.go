package ticketing_test

import (
	"errors"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/ticketing"
	"gorm.io/gorm"
)

func paymentsFor(t *testing.T, db *gorm.DB, orderID uint) (charged, refunded int64) {
	t.Helper()
	var pays []models.BookingPayment
	db.Where("order_id = ? AND status = 'succeeded'", orderID).Find(&pays)
	for _, p := range pays {
		if p.Kind == "refund" {
			refunded += p.AmountCents
		} else {
			charged += p.AmountCents
		}
	}
	return charged, refunded
}

func orderStatus(t *testing.T, db *gorm.DB, id uint) string {
	t.Helper()
	var o models.BookingOrder
	db.First(&o, id)
	return o.Status
}

// TestCaptureAfterHoldLapsedKeepsThePayment is K6: a payment that arrives after
// the hold ran out is recorded, refunded, and issues nothing. Before, the whole
// transaction rolled back and the payment row went with it.
func TestCaptureAfterHoldLapsedKeepsThePayment(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 10)
	now := time.Now()

	order, _, err := ticketing.HoldGA(db, tier, 2, ticketing.Buyer{OwnerRef: "guest:late"}, time.Minute, now, 0)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}

	late := now.Add(5 * time.Minute)
	pay := models.BookingPayment{Provider: "mock", Kind: "capture", Status: "succeeded",
		AmountCents: order.TotalCents, Currency: order.Currency, IdempotencyKey: "late-1"}
	if err := booking.CaptureOrder(db, order.ID, pay, late); !errors.Is(err, booking.ErrCaptureRefunded) {
		t.Fatalf("capture after expiry: got %v, want ErrCaptureRefunded", err)
	}

	charged, refunded := paymentsFor(t, db, order.ID)
	if charged != order.TotalCents || refunded != order.TotalCents {
		t.Errorf("charged %d refunded %d, want both %d", charged, refunded, order.TotalCents)
	}
	if s := orderStatus(t, db, order.ID); s != "refunded" {
		t.Errorf("order status %q, want refunded", s)
	}
	var tickets int64
	db.Model(&models.BookingTicket{}).Where("tier_id = ?", tier.ID).Count(&tickets)
	if tickets != 0 {
		t.Errorf("%d tickets issued for a lapsed hold", tickets)
	}
	inv, _ := booking.InventoryByOwner(db, booking.TierOwner(tier.ID))
	if inv.Held != 0 || inv.Booked != 0 {
		t.Errorf("inventory held=%d booked=%d, want 0/0", inv.Held, inv.Booked)
	}

	// The provider retries the same delivery: still refunded, nothing doubled.
	if err := booking.CaptureOrder(db, order.ID, pay, late); !errors.Is(err, booking.ErrCaptureRefunded) {
		t.Fatalf("retried capture: got %v, want ErrCaptureRefunded", err)
	}
	if c, r := paymentsFor(t, db, order.ID); c != charged || r != refunded {
		t.Errorf("retry changed payments: charged %d refunded %d", c, r)
	}
}

// TestCancelOrderReleasesAndRefunds is K7: a paid order can be undone. The
// booked units come back, the tickets are refunded, and so is the money.
func TestCancelOrderReleasesAndRefunds(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 3)
	now := time.Now()

	order, _, err := ticketing.HoldGA(db, tier, 2, ticketing.Buyer{OwnerRef: "guest:a", Name: "Ada"}, time.Hour, now, 0)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	paid, err := ticketing.Checkout(db, tier.EventID, ticketing.Buyer{OwnerRef: "guest:a", Name: "Ada"}, now)
	if err != nil || len(paid) != 1 || paid[0].Status != "paid" {
		t.Fatalf("checkout: %v %+v", err, paid)
	}

	// Somebody waits for the last units.
	if _, _, err := ticketing.HoldGA(db, tier, 1, ticketing.Buyer{OwnerRef: "guest:b"}, time.Hour, now, 0); err != nil {
		t.Fatalf("hold the remaining unit: %v", err)
	}
	if _, _, err := ticketing.JoinWaitlistTier(db, tier, "guest:c"); err != nil {
		t.Fatalf("join waitlist: %v", err)
	}

	if err := ticketing.CancelOrder(db, order.ID, "guest:other", now); !errors.Is(err, ticketing.ErrOrderNotFound) {
		t.Errorf("cancel by someone else: got %v, want ErrOrderNotFound", err)
	}
	if err := ticketing.CancelOrder(db, order.ID, "guest:a", now); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	if s := orderStatus(t, db, order.ID); s != "refunded" {
		t.Errorf("order status %q, want refunded", s)
	}
	charged, refunded := paymentsFor(t, db, order.ID)
	if charged == 0 || refunded != charged {
		t.Errorf("charged %d refunded %d", charged, refunded)
	}
	var valid int64
	db.Model(&models.BookingTicket{}).Where("tier_id = ? AND status = 'valid'", tier.ID).Count(&valid)
	if valid != 0 {
		t.Errorf("%d tickets still valid after cancel", valid)
	}

	// Two units freed: one goes to the waiter as an offer, one back on sale.
	inv, _ := booking.InventoryByOwner(db, booking.TierOwner(tier.ID))
	if inv.Booked != 0 || inv.Held != 2 {
		t.Errorf("after cancel booked=%d held=%d, want booked=0 held=2 (b's hold + c's offer)", inv.Booked, inv.Held)
	}
	var entry models.BookingWaitlistEntry
	db.Where("owner_ref = 'guest:c'").First(&entry)
	if entry.Status != "offered" {
		t.Errorf("waiter status %q, want offered", entry.Status)
	}

	// Cancelling again is a no-op.
	if err := ticketing.CancelOrder(db, order.ID, "guest:a", now); err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if _, r := paymentsFor(t, db, order.ID); r != refunded {
		t.Errorf("second cancel refunded again: %d", r)
	}
}

func TestCancelOrderRefusesAfterTheShowingStarts(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 3)
	now := time.Now()

	order, _, err := ticketing.HoldGA(db, tier, 1, ticketing.Buyer{OwnerRef: "guest:a"}, time.Hour, now, 0)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := ticketing.Checkout(db, tier.EventID, ticketing.Buyer{OwnerRef: "guest:a", Name: "Ada"}, now); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if err := ticketing.CancelOrder(db, order.ID, "guest:a", now.Add(48*time.Hour)); !errors.Is(err, ticketing.ErrTooLate) {
		t.Fatalf("cancel after start: got %v, want ErrTooLate", err)
	}
}

// TestCheckoutCoversEveryHoldAndSkipsReleased: a cart built over three
// requests is paid in one checkout, and a seat given back on the way is not
// charged for and does not sink the rest.
func TestCheckoutCoversEveryHoldAndSkipsReleased(t *testing.T) {
	db := ticketDB(t)
	seats := seatedEvent(t, db, "A-1", "A-2", "A-3", "A-4")
	eventID := seatOwnerEventID(t, db, seats[0])
	now := time.Now()
	b := ticketing.Buyer{OwnerRef: "guest:cart"}

	_, first, err := ticketing.HoldSeatGroup(db, eventID, []uint{seats[0].ID, seats[1].ID}, b, time.Hour, now, 0)
	if err != nil {
		t.Fatalf("hold 1: %v", err)
	}
	if _, _, err := ticketing.HoldSeatGroup(db, eventID, []uint{seats[2].ID}, b, time.Hour, now, 0); err != nil {
		t.Fatalf("hold 2: %v", err)
	}
	if n, err := ticketing.ReleaseHolds(db, eventID, b.OwnerRef, []string{first[1].Token}, now); err != nil || n != 1 {
		t.Fatalf("release one seat: %d %v", n, err)
	}

	if _, err := ticketing.Checkout(db, eventID, b, now); !errors.Is(err, ticketing.ErrBuyerName) {
		t.Errorf("checkout with no name: got %v, want ErrBuyerName", err)
	}
	b.Name = "Ada"
	paid, err := ticketing.Checkout(db, eventID, b, now)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if len(paid) != 2 {
		t.Fatalf("paid %d orders, want 2", len(paid))
	}
	var total int64
	for _, o := range paid {
		total += o.TotalCents
		if o.BuyerName != "Ada" {
			t.Errorf("order %d buyer %q", o.ID, o.BuyerName)
		}
	}
	if total != 2*12000 {
		t.Errorf("charged %d, want %d (two seats)", total, 2*12000)
	}

	views, err := ticketing.OrdersFor(db, b.OwnerRef)
	if err != nil {
		t.Fatalf("orders for: %v", err)
	}
	tickets := 0
	for _, v := range views {
		tickets += len(v.Tickets)
		for _, tk := range v.Tickets {
			if tk.Seat == "" || tk.Status != "valid" {
				t.Errorf("ticket %+v", tk)
			}
		}
	}
	if tickets != 2 {
		t.Errorf("%d tickets listed, want 2", tickets)
	}

	// Nothing left to pay for.
	if _, err := ticketing.Checkout(db, eventID, b, now); !errors.Is(err, ticketing.ErrNothingToPay) {
		t.Errorf("second checkout: got %v, want ErrNothingToPay", err)
	}
}
