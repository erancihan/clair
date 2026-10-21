package ticketing

import (
	"errors"
	"fmt"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
)

var (
	// ErrPurchaseLimit means the buyer would exceed the event's per-buyer cap.
	ErrPurchaseLimit = errors.New("ticketing: per-buyer purchase limit exceeded")
	// ErrOfferExpired means a waitlist offer is no longer claimable.
	ErrOfferExpired = errors.New("ticketing: waitlist offer expired or not found")
)

// lockBuyer serialises one buyer's holds for one event, for the rest of the
// transaction.
//
// Without it the limit below is a check on a snapshot: two requests can both
// read "you have none" and both commit, because a seated group touches disjoint
// per-seat rows and nothing else makes them conflict. The lock is keyed on
// (event, owner) so it never blocks a different buyer, and Postgres drops it at
// commit.
func lockBuyer(tx *gorm.DB, eventID uint, ownerRef string) error {
	key := fmt.Sprintf("%d:%s", eventID, ownerRef)
	return tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, key).Error
}

// assertEventOpen refuses to sell into an event that is cancelled or has
// already started. The page hides such showtimes; the domain has to refuse
// them too, because a stale tab or an in-process caller sees no page.
func assertEventOpen(tx *gorm.DB, eventID uint, now time.Time) error {
	var ev models.BookingEvent
	if err := tx.First(&ev, eventID).Error; err != nil {
		return err
	}
	if ev.Status != "scheduled" || !now.Before(ev.StartAt) {
		return ErrEventClosed
	}
	return nil
}

// assertBuyerLimit enforces an event's MaxPerBuyer inside the hold transaction.
// It counts the buyer's live units for the event — currently-held plus
// already-fulfilled.
//
// A hold that has lapsed is not a ticket the buyer has, so it must not count
// against them. Nothing reaps at the instant of expiry, so the predicate tests
// `expires_at` rather than trusting `status` to have been swept.
//
// Callers must hold lockBuyer for the same (event, owner) first.
func assertBuyerLimit(tx *gorm.DB, eventID uint, ownerRef string, addQty, max int, now time.Time) error {
	if max <= 0 {
		return nil
	}
	var have int64
	if err := tx.Raw(`
		SELECT COALESCE(SUM(oi.qty), 0)
		FROM booking_order_items oi
		JOIN booking_orders o ON o.id = oi.order_id
		JOIN booking_inventories i ON i.id = oi.inventory_id
		LEFT JOIN booking_seats s ON s.id = i.seat_id
		LEFT JOIN booking_holds h ON h.token = oi.hold_token
		WHERE o.owner_ref = ?
		  AND (oi.status = 'fulfilled' OR (h.status = 'active' AND h.expires_at > ?))
		  AND COALESCE(i.tier_id, s.tier_id) IN (SELECT id FROM booking_ticket_tiers WHERE event_id = ?)`,
		ownerRef, now, eventID).Scan(&have).Error; err != nil {
		return err
	}
	if int(have)+addQty > max {
		return ErrPurchaseLimit
	}
	return nil
}

// activeHold returns the buyer's own live hold on an inventory row, if any.
// Expired-but-unswept holds are deliberately not returned: they are about to be
// reclaimed, and reusing one would hand back a hold that is already gone.
func activeHold(tx *gorm.DB, invID uint, ownerRef string, now time.Time) (*models.BookingHold, error) {
	var h models.BookingHold
	res := tx.Where("inventory_id = ? AND owner_ref = ? AND status = 'active' AND expires_at > ?",
		invID, ownerRef, now).Limit(1).Find(&h)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &h, nil
}

// JoinWaitlistTier adds the buyer to a GA tier's waitlist.
func JoinWaitlistTier(db *gorm.DB, tier *models.BookingTicketTier, ownerRef string) (*models.BookingWaitlistEntry, int, error) {
	inv, err := booking.InventoryByOwner(db, booking.TierOwner(tier.ID))
	if err != nil {
		return nil, 0, err
	}
	return booking.JoinWaitlist(db, inv.ID, ownerRef)
}

// JoinWaitlistSeat adds the buyer to a specific seat's waitlist.
func JoinWaitlistSeat(db *gorm.DB, seatID uint, ownerRef string) (*models.BookingWaitlistEntry, int, error) {
	inv, err := booking.InventoryByOwner(db, booking.SeatOwner(seatID))
	if err != nil {
		return nil, 0, err
	}
	return booking.JoinWaitlist(db, inv.ID, ownerRef)
}

// ClaimOffer converts an active waitlist offer into a pending order the buyer
// can capture. The offer hold already reserves the unit; this attaches an order
// so the normal capture path issues the ticket.
//
// The token alone proves nothing: it is matched against the caller's owner
// reference, so a leaked token cannot be claimed by somebody else, and the
// answer for another buyer's token is the same as for an unknown one. The claim
// counts against the event's per-buyer cap like any other hold.
func ClaimOffer(db *gorm.DB, token string, buyer Buyer, now time.Time) (*models.BookingOrder, error) {
	var order *models.BookingOrder
	err := booking.WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			var h models.BookingHold
			res := tx.Where("token = ? AND owner_ref = ? AND status = 'active' AND purpose = 'waitlist_offer'",
				token, buyer.OwnerRef).Limit(1).Find(&h)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 || !now.Before(h.ExpiresAt) {
				return ErrOfferExpired
			}
			var inv models.BookingInventory
			if err := tx.First(&inv, h.InventoryID).Error; err != nil {
				return err
			}
			tier, err := tierForInventory(tx, inv)
			if err != nil {
				return err
			}
			var ev models.BookingEvent
			if err := tx.First(&ev, tier.EventID).Error; err != nil {
				return err
			}
			if err := lockBuyer(tx, ev.ID, buyer.OwnerRef); err != nil {
				return err
			}
			// The offer hold has no order item yet, so it is not in the count;
			// its qty is what this claim adds.
			if err := assertBuyerLimit(tx, ev.ID, buyer.OwnerRef, h.Qty, ev.MaxPerBuyer, now); err != nil {
				return err
			}

			o := newOrder(buyer, tier.PriceCents*int64(h.Qty), tier.Currency)
			if err := tx.Create(o).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.BookingHold{}).Where("id = ?", h.ID).Update("order_id", o.ID).Error; err != nil {
				return err
			}
			item := &models.BookingOrderItem{
				OrderID: o.ID, HoldToken: h.Token, InventoryID: inv.ID, Qty: h.Qty,
				PriceCents: tier.PriceCents, Status: "reserved",
			}
			if err := tx.Create(item).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.BookingWaitlistEntry{}).Where("offer_token = ?", token).Update("status", "converted").Error; err != nil {
				return err
			}
			order = o
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// tierForInventory resolves the ticket tier behind a tier- or seat-owned
// inventory row; price, currency and event all hang off it.
func tierForInventory(tx *gorm.DB, inv models.BookingInventory) (*models.BookingTicketTier, error) {
	var tier models.BookingTicketTier
	switch {
	case inv.TierID != nil:
		if err := tx.First(&tier, *inv.TierID).Error; err != nil {
			return nil, err
		}
	case inv.SeatID != nil:
		var seat models.BookingSeat
		if err := tx.First(&seat, *inv.SeatID).Error; err != nil {
			return nil, err
		}
		if err := tx.First(&tier, seat.TierID).Error; err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("ticketing: inventory %d is not a tier or seat", inv.ID)
	}
	return &tier, nil
}
