package ticketing

import (
	"errors"
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

// assertBuyerLimit enforces an event's MaxPerBuyer inside the hold transaction.
// It counts the buyer's live units for the event — currently-held (active hold)
// plus already-fulfilled — so expired/released holds don't count.
func assertBuyerLimit(tx *gorm.DB, eventID uint, ownerRef string, addQty, max int) error {
	if max <= 0 {
		return nil
	}
	var have int64
	tx.Raw(`
		SELECT COALESCE(SUM(oi.qty), 0)
		FROM booking_order_items oi
		JOIN booking_orders o ON o.id = oi.order_id
		JOIN booking_inventories i ON i.id = oi.inventory_id
		LEFT JOIN booking_seats s ON s.id = i.seat_id
		LEFT JOIN booking_holds h ON h.token = oi.hold_token
		WHERE o.owner_ref = ?
		  AND (oi.status = 'fulfilled' OR h.status = 'active')
		  AND COALESCE(i.tier_id, s.tier_id) IN (SELECT id FROM booking_ticket_tiers WHERE event_id = ?)`,
		ownerRef, eventID).Scan(&have)
	if int(have)+addQty > max {
		return ErrPurchaseLimit
	}
	return nil
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
func ClaimOffer(db *gorm.DB, token string, buyer Buyer, now time.Time) (*models.BookingOrder, error) {
	var order *models.BookingOrder
	err := booking.WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			var h models.BookingHold
			if res := tx.Where("token = ? AND status = 'active' AND purpose = 'waitlist_offer'", token).Limit(1).Find(&h); res.RowsAffected == 0 {
				return ErrOfferExpired
			}
			if !now.Before(h.ExpiresAt) {
				return ErrOfferExpired
			}
			var inv models.BookingInventory
			if err := tx.First(&inv, h.InventoryID).Error; err != nil {
				return err
			}
			price, currency, err := priceForInventory(tx, inv)
			if err != nil {
				return err
			}
			o := newOrder(buyer, price*int64(h.Qty), currency)
			if err := tx.Create(o).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.BookingHold{}).Where("id = ?", h.ID).Update("order_id", o.ID).Error; err != nil {
				return err
			}
			item := &models.BookingOrderItem{
				OrderID: o.ID, HoldToken: h.Token, InventoryID: inv.ID, Qty: h.Qty,
				PriceCents: price, Status: "reserved",
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

func priceForInventory(tx *gorm.DB, inv models.BookingInventory) (int64, string, error) {
	switch {
	case inv.TierID != nil:
		var tier models.BookingTicketTier
		if err := tx.First(&tier, *inv.TierID).Error; err != nil {
			return 0, "", err
		}
		return tier.PriceCents, tier.Currency, nil
	case inv.SeatID != nil:
		var seat models.BookingSeat
		if err := tx.First(&seat, *inv.SeatID).Error; err != nil {
			return 0, "", err
		}
		var tier models.BookingTicketTier
		if err := tx.First(&tier, seat.TierID).Error; err != nil {
			return 0, "", err
		}
		return tier.PriceCents, tier.Currency, nil
	}
	return 0, "USD", nil
}
