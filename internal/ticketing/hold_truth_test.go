package ticketing_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/ticketing"
	"gorm.io/gorm"
)

// heldUnits totals what an owner currently holds across an event, the way a
// buyer would count their own tickets.
func heldUnits(t *testing.T, db *gorm.DB, owner string) int {
	t.Helper()

	var total int64
	db.Raw(`SELECT COALESCE(SUM(h.qty), 0) FROM booking_holds h
		WHERE h.owner_ref = ? AND h.status = 'active'`, owner).Scan(&total)
	return int(total)
}

// TestBuyerLimitHoldsUnderConcurrency is the cap's real test. Checking it with
// sequential calls only proves the SELECT reads what the previous call wrote;
// the question is whether two racing requests can both pass a check neither has
// committed yet.
func TestBuyerLimitHoldsUnderConcurrency(t *testing.T) {
	db := ticketDB(t)
	seats := seatedEvent(t, db, "A-1", "A-2", "A-3", "A-4", "A-5", "A-6", "A-7", "A-8")
	eventID := seatOwnerEventID(t, db, seats[0])

	const cap = 2
	buyer := ticketing.Buyer{OwnerRef: "guest:racer", Name: "Racer"}
	now := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		pair := []uint{seats[i*2].ID, seats[i*2+1].ID}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Errors are the expected outcome for all but one caller.
			_, _, _ = ticketing.HoldSeatGroup(db, eventID, pair, buyer, time.Hour, now, cap)
		}()
	}
	wg.Wait()

	if got := heldUnits(t, db, buyer.OwnerRef); got > cap {
		t.Errorf("buyer holds %d units against a cap of %d", got, cap)
	}
}

// TestExpiredHoldStopsCountingTowardLimit pins the behaviour the cap's own
// comment already claims: a hold that has lapsed is not a ticket the buyer has.
func TestExpiredHoldStopsCountingTowardLimit(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 20)

	buyer := ticketing.Buyer{OwnerRef: "guest:lapsed", Name: "Lapsed"}
	base := time.Now()

	if _, _, err := ticketing.HoldGA(db, tier, 2, buyer, time.Minute, base, 4); err != nil {
		t.Fatalf("first hold: %v", err)
	}

	// The hold has lapsed but nothing has reaped it yet — the state the site is
	// in for up to a minute at a time.
	later := base.Add(2 * time.Minute)
	if _, _, err := ticketing.HoldGA(db, tier, 3, buyer, time.Minute, later, 4); err != nil {
		t.Fatalf("hold after the first lapsed: %v", err)
	}
}

// TestHoldGATopsUpToRequestedQty covers the case where a buyer asks for more
// than they already hold. Answering 201 while quietly keeping the old, smaller
// hold tells the page it got tickets that do not exist.
func TestHoldGATopsUpToRequestedQty(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 20)

	buyer := ticketing.Buyer{OwnerRef: "guest:topup", Name: "Top Up"}
	now := time.Now()

	if _, _, err := ticketing.HoldGA(db, tier, 2, buyer, time.Hour, now, 6); err != nil {
		t.Fatalf("first hold: %v", err)
	}

	order, hold, err := ticketing.HoldGA(db, tier, 3, buyer, time.Hour, now, 6)
	if err != nil {
		t.Fatalf("second hold: %v", err)
	}
	if hold.Qty != 3 {
		t.Errorf("hold covers %d units, want the 3 that were asked for", hold.Qty)
	}
	if want := tier.PriceCents * 3; order.TotalCents != want {
		t.Errorf("order total %d, want %d", order.TotalCents, want)
	}

	inv, err := booking.InventoryByOwner(db, booking.TierOwner(tier.ID))
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if inv.Held != 3 {
		t.Errorf("inventory holds %d units, want 3", inv.Held)
	}
}

// TestHoldGABelowExistingReportsWhatIsHeld covers the other direction: asking
// for fewer than you hold must not silently report the smaller number.
func TestHoldGABelowExistingReportsWhatIsHeld(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 20)

	buyer := ticketing.Buyer{OwnerRef: "guest:down", Name: "Down"}
	now := time.Now()

	if _, _, err := ticketing.HoldGA(db, tier, 3, buyer, time.Hour, now, 6); err != nil {
		t.Fatalf("first hold: %v", err)
	}

	_, hold, err := ticketing.HoldGA(db, tier, 1, buyer, time.Hour, now, 6)
	if err != nil {
		t.Fatalf("second hold: %v", err)
	}
	if hold.Qty != 3 {
		t.Errorf("hold reports %d units, but 3 are held", hold.Qty)
	}
}

// TestReHoldingYourOwnSeatIsNotSoldOut: a buyer clicking Hold twice owns the
// seat both times. Telling them it is gone is a lie about their own seat.
func TestReHoldingYourOwnSeatIsNotSoldOut(t *testing.T) {
	db := ticketDB(t)
	seats := seatedEvent(t, db, "B-1", "B-2")
	eventID := seatOwnerEventID(t, db, seats[0])

	buyer := ticketing.Buyer{OwnerRef: "guest:again", Name: "Again"}
	now := time.Now()

	_, first, err := ticketing.HoldSeatGroup(db, eventID, []uint{seats[0].ID}, buyer, time.Hour, now, 6)
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}

	_, second, err := ticketing.HoldSeatGroup(db, eventID, []uint{seats[0].ID}, buyer, time.Hour, now, 6)
	if err != nil {
		t.Fatalf("re-holding a seat this buyer already holds: %v", err)
	}
	if len(second) != 1 || second[0].Token != first[0].Token {
		t.Errorf("re-hold issued a different hold instead of returning the buyer's own")
	}

	if got := heldUnits(t, db, buyer.OwnerRef); got != 1 {
		t.Errorf("buyer holds %d units after re-holding one seat, want 1", got)
	}
}

// TestHoldSeatGroupReportsExpiry: the page counts down to this. Leaving it out
// of the answer means the countdown is invented client-side and lapses while the
// server still holds the seats.
func TestHoldSeatGroupReportsExpiry(t *testing.T) {
	db := ticketDB(t)
	seats := seatedEvent(t, db, "C-1", "C-2")
	eventID := seatOwnerEventID(t, db, seats[0])

	now := time.Now()
	_, holds, err := ticketing.HoldSeatGroup(db, eventID, []uint{seats[0].ID, seats[1].ID},
		ticketing.Buyer{OwnerRef: "guest:clock"}, 10*time.Minute, now, 6)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if len(holds) != 2 {
		t.Fatalf("got %d holds for 2 seats", len(holds))
	}
	for _, h := range holds {
		if !h.ExpiresAt.After(now) {
			t.Errorf("hold %s expires at %s, which is not after %s", h.Token, h.ExpiresAt, now)
		}
	}
}

// TestReleaseHoldsFreesSeats covers giving a hold back — the counterpart of
// cancelling an appointment, which the ticketing side never had.
func TestReleaseHoldsFreesSeats(t *testing.T) {
	db := ticketDB(t)
	seats := seatedEvent(t, db, "D-1", "D-2")
	eventID := seatOwnerEventID(t, db, seats[0])

	buyer := ticketing.Buyer{OwnerRef: "guest:letgo"}
	now := time.Now()

	_, holds, err := ticketing.HoldSeatGroup(db, eventID, []uint{seats[0].ID, seats[1].ID}, buyer, time.Hour, now, 6)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	tokens := []string{holds[0].Token, holds[1].Token}

	released, err := ticketing.ReleaseHolds(db, eventID, buyer.OwnerRef, tokens, now)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released != 2 {
		t.Errorf("released %d holds, want 2", released)
	}
	if got := heldUnits(t, db, buyer.OwnerRef); got != 0 {
		t.Errorf("buyer still holds %d units after releasing everything", got)
	}

	// The link gets clicked twice; the second one is not an error.
	if _, err := ticketing.ReleaseHolds(db, eventID, buyer.OwnerRef, tokens, now); err != nil {
		t.Errorf("second release: %v", err)
	}
}

// TestReleaseHoldsRefusesAnotherBuyersToken: the token is the authorization, so
// holding a token you were not issued must not free somebody else's seat.
func TestReleaseHoldsRefusesAnotherBuyersToken(t *testing.T) {
	db := ticketDB(t)
	seats := seatedEvent(t, db, "E-1")
	eventID := seatOwnerEventID(t, db, seats[0])

	owner := ticketing.Buyer{OwnerRef: "guest:owner"}
	now := time.Now()

	_, holds, err := ticketing.HoldSeatGroup(db, eventID, []uint{seats[0].ID}, owner, time.Hour, now, 6)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}

	released, err := ticketing.ReleaseHolds(db, eventID, "guest:thief", []string{holds[0].Token}, now)
	if err != nil {
		t.Fatalf("release by a stranger: %v", err)
	}
	if released != 0 {
		t.Errorf("a stranger released %d of somebody else's holds", released)
	}
	if got := heldUnits(t, db, owner.OwnerRef); got != 1 {
		t.Errorf("owner holds %d units, want their seat still held", got)
	}
}

// seatOwnerEventID resolves the event a seat belongs to.
func seatOwnerEventID(t *testing.T, db *gorm.DB, seat models.BookingSeat) uint {
	t.Helper()

	var tier models.BookingTicketTier
	if err := db.First(&tier, seat.TierID).Error; err != nil {
		t.Fatalf("load tier: %v", err)
	}
	return tier.EventID
}

// closeEvent moves an event's showtime into the past, the state a stale tab
// or a simulator running late would try to sell into.
func closeEvent(t *testing.T, db *gorm.DB, eventID uint) {
	t.Helper()
	if err := db.Model(&models.BookingEvent{}).Where("id = ?", eventID).
		Update("start_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("close event: %v", err)
	}
}

func TestHoldGARefusesStartedEvent(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 10)
	closeEvent(t, db, tier.EventID)

	_, _, err := ticketing.HoldGA(db, tier, 1, ticketing.Buyer{OwnerRef: "guest:late"}, time.Hour, time.Now(), 0)
	if !errors.Is(err, ticketing.ErrEventClosed) {
		t.Fatalf("hold on a started event: got %v, want ErrEventClosed", err)
	}
}

func TestHoldSeatGroupRefusesStartedEvent(t *testing.T) {
	db := ticketDB(t)
	seats := seatedEvent(t, db, "A-1", "A-2")
	eventID := seatOwnerEventID(t, db, seats[0])
	closeEvent(t, db, eventID)

	_, _, err := ticketing.HoldSeatGroup(db, eventID, []uint{seats[0].ID}, ticketing.Buyer{OwnerRef: "guest:late"}, time.Hour, time.Now(), 0)
	if !errors.Is(err, ticketing.ErrEventClosed) {
		t.Fatalf("seat hold on a started event: got %v, want ErrEventClosed", err)
	}
}

// TestHoldSeatGroupRefusesForeignSeat: the seat ids are caller-supplied primary
// keys. The HTTP handler checks membership, but a caller inside the process (the
// simulator, a test) gets no handler, so the domain has to check too.
func TestHoldSeatGroupRefusesForeignSeat(t *testing.T) {
	db := ticketDB(t)
	mine := seatedEvent(t, db, "A-1")
	myEvent := seatOwnerEventID(t, db, mine[0])

	other, err := ticketing.CreateEvent(db, ticketing.EventInput{
		OwnerID: 1, Slug: "ev-other", Name: "Other", StartAt: time.Now().Add(24 * time.Hour),
		Tiers: []ticketing.TierInput{{Name: "Stalls", Kind: "seated", PriceCents: 1000,
			Seats: []ticketing.SeatInput{{Label: "B-1", Section: "Stalls", RowName: "B", Number: 1}}}},
	})
	if err != nil {
		t.Fatalf("create other event: %v", err)
	}
	var foreign models.BookingSeat
	db.Joins("JOIN booking_ticket_tiers t ON t.id = booking_seats.tier_id").
		Where("t.event_id = ?", other.ID).First(&foreign)

	_, _, err = ticketing.HoldSeatGroup(db, myEvent, []uint{mine[0].ID, foreign.ID},
		ticketing.Buyer{OwnerRef: "guest:reach"}, time.Hour, time.Now(), 0)
	if !errors.Is(err, ticketing.ErrInvalidSeat) {
		t.Fatalf("hold with a foreign seat: got %v, want ErrInvalidSeat", err)
	}
	inv, _ := booking.InventoryByOwner(db, booking.SeatOwner(mine[0].ID))
	if inv.Held != 0 {
		t.Fatalf("own seat held=%d after a refused group, want 0", inv.Held)
	}
}

func TestHoldSeatGroupRefusesMixedCurrency(t *testing.T) {
	db := ticketDB(t)
	ev, err := ticketing.CreateEvent(db, ticketing.EventInput{
		OwnerID: 1, Slug: "ev-mixed", Name: "Mixed", StartAt: time.Now().Add(24 * time.Hour),
		Tiers: []ticketing.TierInput{
			{Name: "Stalls", Kind: "seated", PriceCents: 1000, Currency: "USD",
				Seats: []ticketing.SeatInput{{Label: "A-1", Section: "Stalls", RowName: "A", Number: 1}}},
			{Name: "Circle", Kind: "seated", PriceCents: 900, Currency: "EUR",
				Seats: []ticketing.SeatInput{{Label: "C-1", Section: "Circle", RowName: "C", Number: 1}}},
		},
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	var seats []models.BookingSeat
	db.Joins("JOIN booking_ticket_tiers t ON t.id = booking_seats.tier_id").
		Where("t.event_id = ?", ev.ID).Order("booking_seats.label").Find(&seats)

	_, _, err = ticketing.HoldSeatGroup(db, ev.ID, []uint{seats[0].ID, seats[1].ID},
		ticketing.Buyer{OwnerRef: "guest:fx"}, time.Hour, time.Now(), 0)
	if !errors.Is(err, ticketing.ErrMixedCurrency) {
		t.Fatalf("mixed-currency group: got %v, want ErrMixedCurrency", err)
	}
}

// offerFor drives one unit through hold → expiry → reap so that `waiter` holds
// a live offer, and returns its token.
func offerFor(t *testing.T, db *gorm.DB, tier *models.BookingTicketTier, waiter string, base time.Time) (string, time.Time) {
	t.Helper()
	inv, err := booking.InventoryByOwner(db, booking.TierOwner(tier.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ticketing.HoldGA(db, tier, 1, ticketing.Buyer{OwnerRef: "guest:first"}, time.Minute, base, 0); err != nil {
		t.Fatalf("first hold: %v", err)
	}
	if _, _, err := booking.JoinWaitlist(db, inv.ID, waiter); err != nil {
		t.Fatalf("join: %v", err)
	}
	later := base.Add(2 * time.Minute)
	if _, err := booking.ReapExpiredHolds(db, later); err != nil {
		t.Fatalf("reap: %v", err)
	}
	var entry models.BookingWaitlistEntry
	db.Where("owner_ref = ? AND status = 'offered'", waiter).First(&entry)
	if entry.OfferToken == nil {
		t.Fatal("no offer was made")
	}
	return *entry.OfferToken, later
}

func TestClaimOfferRefusesAnotherBuyer(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 1)
	token, later := offerFor(t, db, tier, "guest:owner", time.Now())

	_, err := ticketing.ClaimOffer(db, token, ticketing.Buyer{OwnerRef: "guest:thief"}, later)
	if !errors.Is(err, ticketing.ErrOfferExpired) {
		t.Fatalf("claim by another buyer: got %v, want ErrOfferExpired", err)
	}
	if _, err := ticketing.ClaimOffer(db, token, ticketing.Buyer{OwnerRef: "guest:owner"}, later); err != nil {
		t.Fatalf("claim by the owner after a refused attempt: %v", err)
	}
}

func TestClaimOfferRespectsBuyerCap(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 2)
	if err := db.Model(&models.BookingEvent{}).Where("id = ?", tier.EventID).Update("max_per_buyer", 1).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	// The waiter already owns one ticket, which is the whole cap. It has to be
	// a captured ticket, not a live hold: a live hold on the same tier would be
	// reused as the "offer" by the engine's one-active-hold-per-owner rule, and
	// the claim would fail for the wrong reason.
	order, _, err := ticketing.HoldGA(db, tier, 1, ticketing.Buyer{OwnerRef: "guest:greedy"}, time.Hour, base, 1)
	if err != nil {
		t.Fatalf("waiter's own hold: %v", err)
	}
	pay := models.BookingPayment{Provider: "mock", Kind: "capture", Status: "succeeded", IdempotencyKey: "greedy-1"}
	if err := booking.CaptureOrder(db, order.ID, pay, base); err != nil {
		t.Fatalf("capture waiter's ticket: %v", err)
	}
	token, later := offerFor(t, db, tier, "guest:greedy", base)

	_, err = ticketing.ClaimOffer(db, token, ticketing.Buyer{OwnerRef: "guest:greedy"}, later)
	if !errors.Is(err, ticketing.ErrPurchaseLimit) {
		t.Fatalf("claim over cap: got %v, want ErrPurchaseLimit", err)
	}
}
