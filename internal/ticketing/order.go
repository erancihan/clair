package ticketing

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrNothingToPay means the buyer holds nothing on the event to check out.
	ErrNothingToPay = errors.New("ticketing: no live holds to pay for")
	// ErrHoldLapsed means a hold ran out while it was being paid for. The
	// payment was refunded and no ticket was issued.
	ErrHoldLapsed = errors.New("ticketing: a hold ran out during checkout; nothing was charged")
	// ErrOrderNotFound means no event order with that id belongs to the caller.
	ErrOrderNotFound = errors.New("ticketing: order not found")
	// ErrTooLate means the showing has started, so its tickets can no longer be
	// cancelled.
	ErrTooLate = errors.New("ticketing: the showing has started")
	// ErrTicketUsed means a ticket on the order was already checked in.
	ErrTicketUsed = errors.New("ticketing: a ticket on this order has been used")
	// ErrBuyerName means checkout was asked for without a name for the tickets.
	ErrBuyerName = errors.New("ticketing: a name is required for the tickets")
)

// mockProvider is the payment provider checkout charges through. There is no
// real money in this system: the charge is a payment row the booking service
// treats exactly like a provider's capture.
const mockProvider = "mock"

// Checkout pays for every live hold the buyer has on an event, with the mock
// provider, and issues the tickets.
//
// A buyer builds a cart over several requests (a seat, then two more), and
// each request opens its own order, so checkout covers all of the buyer's
// pending orders on the event. Items whose hold was given back or ran out are
// dropped from their order first, and the order total recomputed, so paying for
// the rest does not trip over a seat the buyer no longer holds.
//
// A hold that lapses between that step and the capture makes its order
// unfulfillable: the booking service refunds it and Checkout returns
// ErrHoldLapsed alongside whatever did get paid.
func Checkout(db *gorm.DB, eventID uint, buyer Buyer, now time.Time) ([]models.BookingOrder, error) {
	buyer.Name = strings.TrimSpace(buyer.Name)
	buyer.Email = strings.TrimSpace(buyer.Email)
	if buyer.Name == "" {
		return nil, ErrBuyerName
	}

	var orderIDs []uint
	err := booking.WithWriteRetry(func() error {
		orderIDs = nil
		return db.Transaction(func(tx *gorm.DB) error {
			if err := lockBuyer(tx, eventID, buyer.OwnerRef); err != nil {
				return err
			}
			if err := assertEventOpen(tx, eventID, now); err != nil {
				return err
			}
			ids, err := pendingOrdersOn(tx, eventID, buyer.OwnerRef)
			if err != nil {
				return err
			}
			for _, id := range ids {
				live, err := trimOrder(tx, id, buyer, now)
				if err != nil {
					return err
				}
				if live {
					orderIDs = append(orderIDs, id)
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if len(orderIDs) == 0 {
		return nil, ErrNothingToPay
	}

	var paid []models.BookingOrder
	var lapsed bool
	for _, id := range orderIDs {
		var order models.BookingOrder
		if err := db.First(&order, id).Error; err != nil {
			return paid, err
		}
		pay := models.BookingPayment{
			Provider: mockProvider, Kind: "charge", Status: "succeeded",
			AmountCents: order.TotalCents, Currency: order.Currency,
			IdempotencyKey: fmt.Sprintf("checkout:order:%d", id),
		}
		err := booking.CaptureOrder(db, id, pay, now)
		if errors.Is(err, booking.ErrCaptureRefunded) {
			lapsed = true
			continue
		}
		if err != nil {
			return paid, err
		}
		if err := db.First(&order, id).Error; err != nil {
			return paid, err
		}
		paid = append(paid, order)
	}
	if lapsed {
		return paid, ErrHoldLapsed
	}
	return paid, nil
}

// pendingOrdersOn lists the buyer's pending orders that have an item on the
// event, oldest first.
func pendingOrdersOn(tx *gorm.DB, eventID uint, ownerRef string) ([]uint, error) {
	var ids []uint
	err := tx.Raw(`
		SELECT DISTINCT o.id FROM booking_orders o
		JOIN booking_order_items oi ON oi.order_id = o.id
		JOIN booking_inventories i ON i.id = oi.inventory_id
		LEFT JOIN booking_seats s ON s.id = i.seat_id
		WHERE o.domain = 'event' AND o.status = 'pending' AND o.owner_ref = ?
		  AND COALESCE(i.tier_id, s.tier_id) IN (SELECT id FROM booking_ticket_tiers WHERE event_id = ?)
		ORDER BY o.id`, ownerRef, eventID).Scan(&ids).Error
	return ids, err
}

// trimOrder cancels the order's items whose hold is no longer live, recomputes
// its total from what is left and stamps the buyer's details on it. An order
// left with nothing is cancelled. Reports whether anything is left to pay for.
func trimOrder(tx *gorm.DB, orderID uint, buyer Buyer, now time.Time) (bool, error) {
	var order models.BookingOrder
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, orderID).Error; err != nil {
		return false, err
	}
	if order.Status != "pending" {
		return false, nil
	}

	var items []models.BookingOrderItem
	if err := tx.Where("order_id = ? AND status = 'reserved'", orderID).Find(&items).Error; err != nil {
		return false, err
	}
	var total int64
	live := 0
	for _, it := range items {
		var h models.BookingHold
		res := tx.Where("token = ? AND status = 'active' AND expires_at > ?", it.HoldToken, now).Limit(1).Find(&h)
		if res.Error != nil {
			return false, res.Error
		}
		if res.RowsAffected == 0 {
			if err := tx.Model(&models.BookingOrderItem{}).Where("id = ?", it.ID).
				Update("status", "cancelled").Error; err != nil {
				return false, err
			}
			continue
		}
		total += it.PriceCents * int64(it.Qty)
		live++
	}

	updates := map[string]any{
		"subtotal_cents": total, "total_cents": total,
		"buyer_name": buyer.Name, "buyer_email": buyer.Email,
	}
	if buyer.UserID != nil {
		updates["user_id"] = *buyer.UserID
	}
	if err := tx.Model(&models.BookingOrder{}).Where("id = ?", orderID).Updates(updates).Error; err != nil {
		return false, err
	}
	if live == 0 {
		// Nothing left to sell. The order is cancelled through the booking
		// service rather than by setting its status, because an earlier
		// checkout may have taken money for it that has to go back.
		return false, booking.CancelOrderTx(tx, orderID, now)
	}
	return true, nil
}

// CancelOrder cancels one of the caller's event orders: its tickets are
// refunded, its seats or units go back on sale (and to the waitlist), and the
// payment is refunded. It refuses once the showing has started.
func CancelOrder(db *gorm.DB, orderID uint, ownerRef string, now time.Time) error {
	var order models.BookingOrder
	res := db.Where("id = ? AND domain = 'event' AND owner_ref = ?", orderID, ownerRef).Limit(1).Find(&order)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrOrderNotFound
	}

	start, err := orderStart(db, orderID)
	if err != nil {
		return err
	}
	if !now.Before(start) {
		return ErrTooLate
	}
	return booking.CancelOrder(db, orderID, now)
}

// orderStart is the earliest start of any event an order has items on.
func orderStart(db *gorm.DB, orderID uint) (time.Time, error) {
	var start struct{ StartAt *time.Time }
	err := db.Raw(`
		SELECT MIN(e.start_at) AS start_at FROM booking_order_items oi
		JOIN booking_inventories i ON i.id = oi.inventory_id
		LEFT JOIN booking_seats s ON s.id = i.seat_id
		JOIN booking_ticket_tiers t ON t.id = COALESCE(i.tier_id, s.tier_id)
		JOIN booking_events e ON e.id = t.event_id
		WHERE oi.order_id = ?`, orderID).Scan(&start).Error
	if err != nil {
		return time.Time{}, err
	}
	if start.StartAt == nil {
		return time.Time{}, ErrOrderNotFound
	}
	return *start.StartAt, nil
}

// TicketView is one issued ticket as the buyer sees it.
type TicketView struct {
	Code   string `json:"code"`
	Seat   string `json:"seat,omitempty"` // empty for general admission
	Tier   string `json:"tier"`
	Status string `json:"status"` // valid | refunded | checked_in
}

// OrderView is one paid or refunded event order with what it bought.
type OrderView struct {
	ID         uint         `json:"id"`
	Status     string       `json:"status"`
	TotalCents int64        `json:"total_cents"`
	Currency   string       `json:"currency"`
	EventSlug  string       `json:"event_slug"`
	EventName  string       `json:"event_name"`
	StartAt    time.Time    `json:"start_at"`
	Tickets    []TicketView `json:"tickets"`
}

// maxOrderViews bounds the my-tickets list.
const maxOrderViews = 50

// OrdersFor lists the caller's paid and refunded event orders, newest first,
// with their tickets.
func OrdersFor(db *gorm.DB, ownerRef string) ([]OrderView, error) {
	var orders []models.BookingOrder
	if err := db.Where("domain = 'event' AND owner_ref = ? AND status IN ('paid', 'refunded')", ownerRef).
		Order("id DESC").Limit(maxOrderViews).Find(&orders).Error; err != nil {
		return nil, err
	}

	out := make([]OrderView, 0, len(orders))
	for _, o := range orders {
		type row struct {
			Code, Status, Tier, Seat, Slug, Name string
			StartAt                              time.Time
		}
		var rows []row
		if err := db.Raw(`
			SELECT tk.code, tk.status, t.name AS tier, COALESCE(s.label, '') AS seat,
			       e.slug, e.name, e.start_at
			FROM booking_tickets tk
			JOIN booking_order_items oi ON oi.id = tk.order_item_id
			JOIN booking_ticket_tiers t ON t.id = tk.tier_id
			JOIN booking_events e ON e.id = tk.event_id
			LEFT JOIN booking_seats s ON s.id = tk.seat_id
			WHERE oi.order_id = ?
			ORDER BY tk.id`, o.ID).Scan(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		v := OrderView{
			ID: o.ID, Status: o.Status, TotalCents: o.TotalCents, Currency: o.Currency,
			EventSlug: rows[0].Slug, EventName: rows[0].Name, StartAt: rows[0].StartAt,
		}
		for _, r := range rows {
			v.Tickets = append(v.Tickets, TicketView{Code: r.Code, Seat: r.Seat, Tier: r.Tier, Status: r.Status})
		}
		out = append(out, v)
	}
	return out, nil
}
