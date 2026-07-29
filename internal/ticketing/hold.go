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
// order. Idempotent per (tier, owner): a repeat call reuses the existing hold
// and its order.
func HoldGA(db *gorm.DB, tier *models.BookingTicketTier, qty int, buyer Buyer, ttl time.Duration, now time.Time, maxPerBuyer int) (*models.BookingOrder, *models.BookingHold, error) {
	if qty < 1 {
		return nil, nil, errors.New("ticketing: qty must be >= 1")
	}
	var order *models.BookingOrder
	var hold *models.BookingHold
	err := booking.WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := assertBuyerLimit(tx, tier.EventID, buyer.OwnerRef, qty, maxPerBuyer); err != nil {
				return err
			}
			inv, err := booking.InventoryByOwner(tx, booking.TierOwner(tier.ID))
			if err != nil {
				return err
			}
			h, err := booking.HoldUnitsTx(tx, inv.ID, qty, buyer.OwnerRef, nil, "cart", ttl, now)
			if err != nil {
				return err
			}
			if h.OrderID != nil { // idempotent reuse of an existing active hold
				var o models.BookingOrder
				if err := tx.First(&o, *h.OrderID).Error; err != nil {
					return err
				}
				order, hold = &o, h
				return nil
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

// HoldSeatGroup holds a set of assigned seats all-or-nothing in one transaction:
// if any seat is unavailable the whole group rolls back. Seats are acquired in
// ascending inventory-id order to avoid deadlocks.
func HoldSeatGroup(db *gorm.DB, eventID uint, seatIDs []uint, buyer Buyer, ttl time.Duration, now time.Time, maxPerBuyer int) (*models.BookingOrder, []string, error) {
	if len(seatIDs) == 0 {
		return nil, nil, errors.New("ticketing: no seats requested")
	}
	var order *models.BookingOrder
	var tokens []string
	err := booking.WithWriteRetry(func() error {
		tokens = tokens[:0]
		return db.Transaction(func(tx *gorm.DB) error {
			if err := assertBuyerLimit(tx, eventID, buyer.OwnerRef, len(seatIDs), maxPerBuyer); err != nil {
				return err
			}
			type entry struct {
				invID uint
				price int64
			}
			var entries []entry
			var total int64
			currency := "USD"
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
				inv, err := booking.InventoryByOwner(tx, booking.SeatOwner(sid))
				if err != nil {
					return err
				}
				entries = append(entries, entry{invID: inv.ID, price: tier.PriceCents})
				total += tier.PriceCents
				currency = tier.Currency
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].invID < entries[j].invID })

			o := newOrder(buyer, total, currency)
			if err := tx.Create(o).Error; err != nil {
				return err
			}
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
				tokens = append(tokens, h.Token)
			}
			order = o
			return nil
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return order, tokens, nil
}
