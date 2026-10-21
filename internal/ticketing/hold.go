package ticketing

import (
	"errors"
	"sort"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
)

// Buyer identifies who is reserving and their contact details.
type Buyer struct {
	OwnerRef string // "user:<id>" | "guest:<sid>"
	UserID   *uint
	Name     string
	Email    string
}

func newOrder(buyer Buyer, total int64, currency string) *models.BookingOrder {
	return &models.BookingOrder{
		Domain: "event", OwnerRef: buyer.OwnerRef, UserID: buyer.UserID,
		BuyerName: buyer.Name, BuyerEmail: buyer.Email, Status: "pending",
		SubtotalCents: total, TotalCents: total, Currency: currency,
	}
}

// HoldGA holds qty units of a GA tier's pooled inventory and opens a pending
// order.
//
// A buyer who already holds units of this tier is topped up to qty rather than
// given a second hold — one pooled hold per (tier, buyer) is what the partial
// unique index allows. Asking for fewer than you already hold changes nothing
// and returns the hold as it stands: the returned hold's Qty is always what is
// actually reserved, never what was requested, because the page renders those
// numbers back to the buyer.
func HoldGA(db *gorm.DB, tier *models.BookingTicketTier, qty int, buyer Buyer, ttl time.Duration, now time.Time, maxPerBuyer int) (*models.BookingOrder, *models.BookingHold, error) {
	if qty < 1 {
		return nil, nil, errors.New("ticketing: qty must be >= 1")
	}
	var order *models.BookingOrder
	var hold *models.BookingHold
	err := booking.WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := lockBuyer(tx, tier.EventID, buyer.OwnerRef); err != nil {
				return err
			}
			if err := assertEventOpen(tx, tier.EventID, now); err != nil {
				return err
			}
			inv, err := booking.InventoryByOwner(tx, booking.TierOwner(tier.ID))
			if err != nil {
				return err
			}

			existing, err := activeHold(tx, inv.ID, buyer.OwnerRef, now)
			if err != nil {
				return err
			}
			if existing != nil {
				o, h, err := topUpGA(tx, tier, inv.ID, existing, qty, buyer, now, maxPerBuyer)
				if err != nil {
					return err
				}
				order, hold = o, h
				return nil
			}

			if err := assertBuyerLimit(tx, tier.EventID, buyer.OwnerRef, qty, maxPerBuyer, now); err != nil {
				return err
			}
			h, err := booking.HoldUnitsTx(tx, inv.ID, qty, buyer.OwnerRef, nil, "cart", ttl, now)
			if err != nil {
				return err
			}
			o := newOrder(buyer, tier.PriceCents*int64(qty), tier.Currency)
			if err := tx.Create(o).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.BookingHold{}).Where("id = ?", h.ID).Update("order_id", o.ID).Error; err != nil {
				return err
			}
			item := &models.BookingOrderItem{
				OrderID: o.ID, HoldToken: h.Token, InventoryID: inv.ID, Qty: qty,
				PriceCents: tier.PriceCents, Status: "reserved",
			}
			if err := tx.Create(item).Error; err != nil {
				return err
			}
			order, hold = o, h
			return nil
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return order, hold, nil
}

// topUpGA raises an existing pooled hold to qty units and keeps its order in
// step, so the order total always describes the hold it belongs to.
//
// Only the difference is charged against the per-buyer cap: the units already
// held are already counted there.
func topUpGA(tx *gorm.DB, tier *models.BookingTicketTier, invID uint, existing *models.BookingHold, qty int, buyer Buyer, now time.Time, maxPerBuyer int) (*models.BookingOrder, *models.BookingHold, error) {
	if delta := qty - existing.Qty; delta > 0 {
		if err := assertBuyerLimit(tx, tier.EventID, buyer.OwnerRef, delta, maxPerBuyer, now); err != nil {
			return nil, nil, err
		}
		res := tx.Model(&models.BookingInventory{}).
			Where("id = ? AND booked + held + blocked + ? <= capacity", invID, delta).
			UpdateColumn("held", gorm.Expr("held + ?", delta))
		if res.Error != nil {
			return nil, nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, nil, booking.ErrSoldOut
		}
		if err := tx.Model(&models.BookingHold{}).Where("id = ?", existing.ID).
			UpdateColumn("qty", gorm.Expr("qty + ?", delta)).Error; err != nil {
			return nil, nil, err
		}
		existing.Qty = qty
	}

	total := tier.PriceCents * int64(existing.Qty)

	// A hold with no order behind it is a waitlist offer being converted into a
	// cart; it needs an order opening for it.
	if existing.OrderID == nil {
		o := newOrder(buyer, total, tier.Currency)
		if err := tx.Create(o).Error; err != nil {
			return nil, nil, err
		}
		if err := tx.Model(&models.BookingHold{}).Where("id = ?", existing.ID).
			Update("order_id", o.ID).Error; err != nil {
			return nil, nil, err
		}
		item := &models.BookingOrderItem{
			OrderID: o.ID, HoldToken: existing.Token, InventoryID: invID, Qty: existing.Qty,
			PriceCents: tier.PriceCents, Status: "reserved",
		}
		if err := tx.Create(item).Error; err != nil {
			return nil, nil, err
		}
		existing.OrderID = &o.ID
		return o, existing, nil
	}

	if err := tx.Model(&models.BookingOrderItem{}).
		Where("order_id = ? AND hold_token = ?", *existing.OrderID, existing.Token).
		UpdateColumn("qty", existing.Qty).Error; err != nil {
		return nil, nil, err
	}
	if err := tx.Model(&models.BookingOrder{}).Where("id = ?", *existing.OrderID).
		Updates(map[string]any{"subtotal_cents": total, "total_cents": total}).Error; err != nil {
		return nil, nil, err
	}

	var o models.BookingOrder
	if err := tx.First(&o, *existing.OrderID).Error; err != nil {
		return nil, nil, err
	}
	return &o, existing, nil
}

// HoldSeatGroup holds a set of assigned seats all-or-nothing in one transaction:
// if any seat is unavailable the whole group rolls back. Seats are acquired in
// ascending inventory-id order to avoid deadlocks.
//
// Seats the buyer already holds are theirs, not obstacles: they are dropped from
// the group rather than refused as sold out, and the returned order covers only
// the seats this call actually reserved. Repeat ids are collapsed, so a
// double-tap or a double-submit is not a different request.
//
// The returned holds carry their own expiry, because that is what a countdown
// has to be driven from.
func HoldSeatGroup(db *gorm.DB, eventID uint, seatIDs []uint, buyer Buyer, ttl time.Duration, now time.Time, maxPerBuyer int) (*models.BookingOrder, []models.BookingHold, error) {
	seatIDs = dedupe(seatIDs)
	if len(seatIDs) == 0 {
		return nil, nil, errors.New("ticketing: no seats requested")
	}
	var order *models.BookingOrder
	var holds []models.BookingHold
	err := booking.WithWriteRetry(func() error {
		order, holds = nil, nil
		return db.Transaction(func(tx *gorm.DB) error {
			if err := lockBuyer(tx, eventID, buyer.OwnerRef); err != nil {
				return err
			}
			if err := assertEventOpen(tx, eventID, now); err != nil {
				return err
			}

			type entry struct {
				invID uint
				price int64
			}
			var entries []entry
			var mine []models.BookingHold
			var total int64
			currency := ""
			for _, sid := range seatIDs {
				var seat models.BookingSeat
				if err := tx.First(&seat, sid).Error; err != nil {
					return ErrInvalidSeat
				}
				if seat.Kind != "sellable" {
					return ErrInvalidSeat
				}
				var tier models.BookingTicketTier
				if err := tx.First(&tier, seat.TierID).Error; err != nil {
					return err
				}
				// A seat id is a caller-supplied key; it must belong to the event
				// this group is being sold for.
				if tier.EventID != eventID {
					return ErrInvalidSeat
				}
				inv, err := booking.InventoryByOwner(tx, booking.SeatOwner(sid))
				if err != nil {
					return err
				}
				// One order, one currency: cents from two currencies do not add.
				if currency == "" {
					currency = tier.Currency
				} else if tier.Currency != currency {
					return ErrMixedCurrency
				}

				held, err := activeHold(tx, inv.ID, buyer.OwnerRef, now)
				if err != nil {
					return err
				}
				if held != nil { // already this buyer's seat
					mine = append(mine, *held)
					continue
				}

				entries = append(entries, entry{invID: inv.ID, price: tier.PriceCents})
				total += tier.PriceCents
			}

			// Nothing new to reserve: the buyer resubmitted a group they already
			// hold, so answer with what they have instead of an error.
			if len(entries) == 0 {
				o, err := orderBehind(tx, mine)
				if err != nil {
					return err
				}
				order, holds = o, mine
				return nil
			}

			// Only the new seats count against the cap; the rest are already in it.
			if err := assertBuyerLimit(tx, eventID, buyer.OwnerRef, len(entries), maxPerBuyer, now); err != nil {
				return err
			}

			sort.Slice(entries, func(i, j int) bool { return entries[i].invID < entries[j].invID })

			if currency == "" {
				currency = "USD"
			}
			o := newOrder(buyer, total, currency)
			if err := tx.Create(o).Error; err != nil {
				return err
			}
			var fresh []models.BookingHold
			for _, e := range entries {
				h, err := booking.HoldUnitsTx(tx, e.invID, 1, buyer.OwnerRef, &o.ID, "cart", ttl, now)
				if err != nil {
					return err // any failure rolls back the whole group
				}
				item := &models.BookingOrderItem{
					OrderID: o.ID, HoldToken: h.Token, InventoryID: e.invID, Qty: 1,
					PriceCents: e.price, Status: "reserved",
				}
				if err := tx.Create(item).Error; err != nil {
					return err
				}
				fresh = append(fresh, *h)
			}
			order, holds = o, fresh
			return nil
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return order, holds, nil
}

// orderBehind returns the order a set of existing holds belongs to.
func orderBehind(tx *gorm.DB, holds []models.BookingHold) (*models.BookingOrder, error) {
	for _, h := range holds {
		if h.OrderID == nil {
			continue
		}
		var o models.BookingOrder
		if err := tx.First(&o, *h.OrderID).Error; err != nil {
			return nil, err
		}
		return &o, nil
	}
	return nil, nil
}

// dedupe collapses repeated ids, keeping first-seen order. A seat named twice in
// one request is one seat; without this the caller-facing membership check reads
// a repeat as a seat that does not belong to the event.
func dedupe(ids []uint) []uint {
	seen := make(map[uint]bool, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// ReleaseHolds gives back holds the buyer placed, and reports how many it freed.
//
// The token proves nothing on its own here — it is checked against the caller's
// owner reference and the event it was issued for, so a token that leaked cannot
// be used to free somebody else's seat. Releasing is idempotent: a token that is
// already gone, or was never this buyer's, is skipped rather than refused,
// because the button gets pressed twice.
func ReleaseHolds(db *gorm.DB, eventID uint, ownerRef string, tokens []string, now time.Time) (int, error) {
	released := 0
	err := booking.WithWriteRetry(func() error {
		released = 0
		return db.Transaction(func(tx *gorm.DB) error {
			for _, token := range tokens {
				var h models.BookingHold
				res := tx.Raw(`
					SELECT h.* FROM booking_holds h
					JOIN booking_inventories i ON i.id = h.inventory_id
					LEFT JOIN booking_seats s ON s.id = i.seat_id
					WHERE h.token = ? AND h.owner_ref = ? AND h.status = 'active'
					  AND COALESCE(i.tier_id, s.tier_id) IN (SELECT id FROM booking_ticket_tiers WHERE event_id = ?)`,
					token, ownerRef, eventID).Scan(&h)
				if res.Error != nil {
					return res.Error
				}
				if h.ID == 0 {
					continue
				}
				if err := booking.ReleaseTx(tx, token, now); err != nil {
					return err
				}
				released++
			}
			return nil
		})
	})
	if err != nil {
		return 0, err
	}
	return released, nil
}
