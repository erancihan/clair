package ticketing

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
)

func newCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "TKT-" + hex.EncodeToString(b)
}

// ticketFulfiller creates BookingTickets when a ticketing order is captured.
// One ticket per seat; item.Qty tickets for a GA item. Idempotent: if tickets
// already exist for the item, it returns without creating more.
type ticketFulfiller struct{}

func (ticketFulfiller) Fulfill(tx *gorm.DB, order models.BookingOrder, item models.BookingOrderItem, inv models.BookingInventory) (uint, error) {
	var existing models.BookingTicket
	if res := tx.Where("order_item_id = ?", item.ID).Limit(1).Find(&existing); res.RowsAffected > 0 {
		return existing.ID, nil // idempotent
	}

	switch {
	case inv.SeatID != nil:
		var seat models.BookingSeat
		if err := tx.First(&seat, *inv.SeatID).Error; err != nil {
			return 0, err
		}
		var tier models.BookingTicketTier
		if err := tx.First(&tier, seat.TierID).Error; err != nil {
			return 0, err
		}
		seatID := seat.ID
		ticket := models.BookingTicket{
			OrderItemID: item.ID, EventID: tier.EventID, TierID: tier.ID, SeatID: &seatID,
			Code: newCode(), HolderName: order.BuyerName, HolderEmail: order.BuyerEmail, Status: "valid",
		}
		if err := tx.Create(&ticket).Error; err != nil {
			return 0, err
		}
		return ticket.ID, nil

	case inv.TierID != nil:
		var tier models.BookingTicketTier
		if err := tx.First(&tier, *inv.TierID).Error; err != nil {
			return 0, err
		}
		firstID := uint(0)
		for i := 0; i < item.Qty; i++ {
			ticket := models.BookingTicket{
				OrderItemID: item.ID, EventID: tier.EventID, TierID: tier.ID,
				Code: newCode(), HolderName: order.BuyerName, HolderEmail: order.BuyerEmail, Status: "valid",
			}
			if err := tx.Create(&ticket).Error; err != nil {
				return 0, err
			}
			if firstID == 0 {
				firstID = ticket.ID
			}
		}
		return firstID, nil
	}
	return 0, fmt.Errorf("ticketing: inventory %d has no seat/tier owner", inv.ID)
}

func init() {
	booking.RegisterFulfiller("seat", ticketFulfiller{})
	booking.RegisterFulfiller("tier", ticketFulfiller{})
}
