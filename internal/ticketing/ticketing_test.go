package ticketing_test

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
	"github.com/erancihan/clair/internal/ticketing"
	"gorm.io/gorm"
)

func ticketDB(t *testing.T) *gorm.DB {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	return db
}

func gaTier(t *testing.T, db *gorm.DB, capacity int) *models.BookingTicketTier {
	t.Helper()
	ev, err := ticketing.CreateEvent(db, ticketing.EventInput{
		OwnerID: 1, Slug: "ev-ga", Name: "Concert", StartAt: time.Now().Add(24 * time.Hour),
		Tiers: []ticketing.TierInput{{Name: "GA", Kind: "ga", PriceCents: 8000, Capacity: capacity}},
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	var tier models.BookingTicketTier
	db.Where("event_id = ?", ev.ID).First(&tier)
	return &tier
}

func seatedEvent(t *testing.T, db *gorm.DB, labels ...string) []models.BookingSeat {
	t.Helper()
	seats := make([]ticketing.SeatInput, len(labels))
	for i, l := range labels {
		seats[i] = ticketing.SeatInput{Label: l, Section: "Orchestra", RowName: "A", Number: i + 1}
	}
	ev, err := ticketing.CreateEvent(db, ticketing.EventInput{
		OwnerID: 1, Slug: "ev-seated", Name: "Cinema", StartAt: time.Now().Add(24 * time.Hour),
		Tiers: []ticketing.TierInput{{Name: "Orchestra", Kind: "seated", PriceCents: 12000, Seats: seats}},
	})
	if err != nil {
		t.Fatalf("create seated event: %v", err)
	}
	var out []models.BookingSeat
	db.Joins("JOIN booking_ticket_tiers t ON t.id = booking_seats.tier_id").
		Where("t.event_id = ?", ev.ID).Order("number").Find(&out)
	return out
}

func TestHoldGA_NoOversell(t *testing.T) {
	db := ticketDB(t)
	const cap = 5
	tier := gaTier(t, db, cap)
	now := time.Now()

	const N = 40
	var wg sync.WaitGroup
	var success int64
	for i := 0; i < N; i++ {
		buyer := ticketing.Buyer{OwnerRef: fmt.Sprintf("guest:%d", i)}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := ticketing.HoldGA(db, tier, 1, buyer, time.Hour, now, 0); err == nil {
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	wg.Wait()

	if success != cap {
		t.Fatalf("GA pool=%d but %d holds succeeded", cap, success)
	}
	inv, _ := booking.InventoryByOwner(db, booking.TierOwner(tier.ID))
	if inv.Held != cap {
		t.Fatalf("held=%d, want %d", inv.Held, cap)
	}
}

func TestHoldSeatGroup_AllOrNothing(t *testing.T) {
	db := ticketDB(t)
	seats := seatedEvent(t, db, "A-1", "A-2", "A-3")
	now := time.Now()

	// Buyer 1 takes A-1 + A-2.
	if _, _, err := ticketing.HoldSeatGroup(db, 0, []uint{seats[0].ID, seats[1].ID},
		ticketing.Buyer{OwnerRef: "guest:b1"}, time.Hour, now, 0); err != nil {
		t.Fatalf("buyer1 hold: %v", err)
	}

	// Buyer 2 wants A-2 + A-3; A-2 is taken → the WHOLE group must fail and A-3
	// must stay free (no lone seat).
	if _, _, err := ticketing.HoldSeatGroup(db, 0, []uint{seats[1].ID, seats[2].ID},
		ticketing.Buyer{OwnerRef: "guest:b2"}, time.Hour, now, 0); err != booking.ErrSoldOut {
		t.Fatalf("buyer2 hold: got %v, want ErrSoldOut", err)
	}

	inv3, _ := booking.InventoryByOwner(db, booking.SeatOwner(seats[2].ID))
	if inv3.Held != 0 || inv3.Booked != 0 {
		t.Fatalf("A-3 should be untouched, got held=%d booked=%d", inv3.Held, inv3.Booked)
	}
	inv1, _ := booking.InventoryByOwner(db, booking.SeatOwner(seats[0].ID))
	inv2, _ := booking.InventoryByOwner(db, booking.SeatOwner(seats[1].ID))
	if inv1.Held != 1 || inv2.Held != 1 {
		t.Fatalf("A-1/A-2 should be held by buyer1, got %d/%d", inv1.Held, inv2.Held)
	}
}

func TestCapture_GA_CreatesTickets(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 10)
	now := time.Now()

	order, _, err := ticketing.HoldGA(db, tier, 3, ticketing.Buyer{OwnerRef: "guest:x", Name: "Ada", Email: "ada@x.com"}, time.Hour, now, 0)
	if err != nil {
		t.Fatalf("hold GA: %v", err)
	}
	pay := models.BookingPayment{Provider: "mock", Kind: "capture", Status: "succeeded", AmountCents: 24000, IdempotencyKey: "evt-ga"}
	if err := booking.CaptureOrder(db, order.ID, pay, now); err != nil {
		t.Fatalf("capture: %v", err)
	}

	var tickets int64
	db.Model(&models.BookingTicket{}).Where("tier_id = ?", tier.ID).Count(&tickets)
	if tickets != 3 {
		t.Fatalf("want 3 GA tickets, got %d", tickets)
	}
	inv, _ := booking.InventoryByOwner(db, booking.TierOwner(tier.ID))
	if inv.Held != 0 || inv.Booked != 3 {
		t.Fatalf("after capture held=%d booked=%d, want held=0 booked=3", inv.Held, inv.Booked)
	}

	// idempotent capture: still 3 tickets.
	if err := booking.CaptureOrder(db, order.ID, pay, now); err != nil {
		t.Fatalf("dup capture: %v", err)
	}
	db.Model(&models.BookingTicket{}).Where("tier_id = ?", tier.ID).Count(&tickets)
	if tickets != 3 {
		t.Fatalf("duplicate capture changed ticket count to %d", tickets)
	}
}

func gaTierMax(t *testing.T, db *gorm.DB, capacity, maxPerBuyer int) *models.BookingTicketTier {
	t.Helper()
	ev, err := ticketing.CreateEvent(db, ticketing.EventInput{
		OwnerID: 1, Slug: "ev-max", Name: "Concert", StartAt: time.Now().Add(24 * time.Hour),
		MaxPerBuyer: maxPerBuyer,
		Tiers:       []ticketing.TierInput{{Name: "GA", Kind: "ga", PriceCents: 8000, Capacity: capacity}},
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	var tier models.BookingTicketTier
	db.Where("event_id = ?", ev.ID).First(&tier)
	return &tier
}

func TestWaitlist_OfferClaimCapture(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 1) // capacity 1
	inv, _ := booking.InventoryByOwner(db, booking.TierOwner(tier.ID))
	base := time.Now()

	// Buyer A holds the only unit (short TTL so it expires soon).
	if _, _, err := ticketing.HoldGA(db, tier, 1, ticketing.Buyer{OwnerRef: "guest:a"}, time.Minute, base, 0); err != nil {
		t.Fatalf("hold A: %v", err)
	}
	// Buyer B joins the waitlist.
	_, pos, err := booking.JoinWaitlist(db, inv.ID, "guest:b")
	if err != nil || pos != 1 {
		t.Fatalf("join waitlist: pos=%d err=%v", pos, err)
	}

	// After A's hold expires, the reaper frees the unit → offered to B.
	later := base.Add(2 * time.Minute)
	if _, err := booking.ReapExpiredHolds(db, later); err != nil {
		t.Fatalf("reap: %v", err)
	}
	var entry models.BookingWaitlistEntry
	db.Where("owner_ref = ? AND status = 'offered'", "guest:b").First(&entry)
	if entry.OfferToken == nil {
		t.Fatal("buyer B was not offered the freed unit")
	}
	// The unit is reserved for B (held), not open.
	db.First(inv, inv.ID)
	if inv.Held != 1 || inv.Booked != 0 {
		t.Fatalf("after offer held=%d booked=%d, want held=1 booked=0", inv.Held, inv.Booked)
	}

	// B claims the offer and captures → B gets the ticket.
	order, err := ticketing.ClaimOffer(db, *entry.OfferToken, ticketing.Buyer{OwnerRef: "guest:b", Name: "Bob"}, later)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	pay := models.BookingPayment{Provider: "mock", Kind: "capture", Status: "succeeded", IdempotencyKey: "wl-1"}
	if err := booking.CaptureOrder(db, order.ID, pay, later); err != nil {
		t.Fatalf("capture: %v", err)
	}
	var ticket models.BookingTicket
	if res := db.Where("tier_id = ?", tier.ID).Limit(1).Find(&ticket); res.RowsAffected != 1 || ticket.HolderName != "Bob" {
		t.Fatalf("expected Bob's ticket, got %+v (rows=%d)", ticket, res.RowsAffected)
	}
	db.First(inv, inv.ID)
	if inv.Booked != 1 {
		t.Fatalf("after claim+capture booked=%d, want 1", inv.Booked)
	}
}

func TestPerBuyerLimit(t *testing.T) {
	db := ticketDB(t)
	tier := gaTierMax(t, db, 10, 4) // pool 10, max 4 per buyer
	now := time.Now()
	buyer := ticketing.Buyer{OwnerRef: "guest:greedy"}

	// qty 5 in one shot exceeds the cap.
	if _, _, err := ticketing.HoldGA(db, tier, 5, buyer, time.Hour, now, 4); err != ticketing.ErrPurchaseLimit {
		t.Fatalf("hold 5: got %v, want ErrPurchaseLimit", err)
	}
	// qty 3 is fine; capture it.
	order, _, err := ticketing.HoldGA(db, tier, 3, buyer, time.Hour, now, 4)
	if err != nil {
		t.Fatalf("hold 3: %v", err)
	}
	pay := models.BookingPayment{Provider: "mock", Kind: "capture", Status: "succeeded", IdempotencyKey: "lim-1"}
	if err := booking.CaptureOrder(db, order.ID, pay, now); err != nil {
		t.Fatalf("capture: %v", err)
	}
	// Now 3 are fulfilled; a further 2 would be 5 > 4 → rejected.
	if _, _, err := ticketing.HoldGA(db, tier, 2, buyer, time.Hour, now, 4); err != ticketing.ErrPurchaseLimit {
		t.Fatalf("hold 2 more: got %v, want ErrPurchaseLimit", err)
	}
	// But 1 more (total 4) is allowed.
	if _, _, err := ticketing.HoldGA(db, tier, 1, buyer, time.Hour, now, 4); err != nil {
		t.Fatalf("hold 1 more (total 4): %v", err)
	}
}

func TestCapture_Seated_CreatesTicketPerSeat(t *testing.T) {
	db := ticketDB(t)
	seats := seatedEvent(t, db, "A-1", "A-2")
	now := time.Now()

	order, _, err := ticketing.HoldSeatGroup(db, 0, []uint{seats[0].ID, seats[1].ID},
		ticketing.Buyer{OwnerRef: "guest:x", Name: "Ada"}, time.Hour, now, 0)
	if err != nil {
		t.Fatalf("hold seats: %v", err)
	}
	pay := models.BookingPayment{Provider: "mock", Kind: "capture", Status: "succeeded", AmountCents: 24000, IdempotencyKey: "evt-seat"}
	if err := booking.CaptureOrder(db, order.ID, pay, now); err != nil {
		t.Fatalf("capture: %v", err)
	}

	var tickets []models.BookingTicket
	db.Where("seat_id IS NOT NULL").Find(&tickets)
	if len(tickets) != 2 {
		t.Fatalf("want 2 seated tickets, got %d", len(tickets))
	}
	for _, seat := range seats {
		inv, _ := booking.InventoryByOwner(db, booking.SeatOwner(seat.ID))
		if inv.Booked != 1 || inv.Held != 0 {
			t.Fatalf("seat %s after capture held=%d booked=%d, want held=0 booked=1", seat.Label, inv.Held, inv.Booked)
		}
	}
}
