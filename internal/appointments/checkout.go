package appointments

import (
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
)

// HoldInput is a request to reserve (hold) a slot during paid checkout.
type HoldInput struct {
	Schedule   *models.BookingSchedule
	StartAt    time.Time
	OwnerRef   string // "user:<id>" | "guest:<sid>" — identifies the holder
	UserID     *uint
	GuestName  string
	GuestEmail string
}

// HoldSlot reserves a slot for the schedule's HoldTTLMin and opens a pending
// order for it (the paid visa flow). The slot is counted in `held`, so it cannot
// be taken by anyone else while the applicant pays; if they abandon it, the
// reaper releases it. Confirmation happens later at payment capture.
func HoldSlot(db *gorm.DB, in HoldInput, now time.Time) (*models.BookingOrder, *models.BookingHold, error) {
	startUTC := in.StartAt.UTC()
	if !startUTC.After(now) {
		return nil, nil, ErrPast
	}
	if err := validateOpen(db, in.Schedule, startUTC, now); err != nil {
		return nil, nil, err
	}

	ttl := time.Duration(in.Schedule.HoldTTLMin) * time.Minute
	var order *models.BookingOrder
	var hold *models.BookingHold

	err := booking.WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			slot, err := ensureSlot(tx, in.Schedule, startUTC)
			if err != nil {
				return err
			}
			inv, err := booking.EnsureInventory(tx, booking.SlotOwner(slot.ID), slot.Capacity)
			if err != nil {
				return err
			}
			h, err := booking.HoldUnitsTx(tx, inv.ID, 1, in.OwnerRef, nil, "cart", ttl, now)
			if err != nil {
				return err
			}
			o := &models.BookingOrder{
				Domain: "appointment", OwnerRef: in.OwnerRef, UserID: in.UserID,
				BuyerName: in.GuestName, BuyerEmail: in.GuestEmail, Status: "pending",
				SubtotalCents: in.Schedule.PriceCents, TotalCents: in.Schedule.PriceCents,
				Currency: in.Schedule.Currency,
			}
			if err := tx.Create(o).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.BookingHold{}).Where("id = ?", h.ID).Update("order_id", o.ID).Error; err != nil {
				return err
			}
			item := &models.BookingOrderItem{
				OrderID: o.ID, HoldToken: h.Token, InventoryID: inv.ID, Qty: 1,
				PriceCents: in.Schedule.PriceCents, Status: "reserved",
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

// slotFulfiller creates the confirmed appointment when a paid order is captured.
type slotFulfiller struct{}

func (slotFulfiller) Fulfill(tx *gorm.DB, order models.BookingOrder, item models.BookingOrderItem, inv models.BookingInventory) (uint, error) {
	if item.FulfilledID != nil {
		return *item.FulfilledID, nil // idempotent
	}
	var slot models.BookingSlot
	if err := tx.First(&slot, *inv.SlotID).Error; err != nil {
		return 0, err
	}
	appt := models.BookingAppointment{
		ScheduleID: slot.ScheduleID, SlotID: slot.ID, UserID: order.UserID,
		GuestName: order.BuyerName, GuestEmail: order.BuyerEmail,
		StartAt: slot.StartAt, EndAt: slot.EndAt, Status: "confirmed", CancelToken: newToken(),
	}
	if err := tx.Create(&appt).Error; err != nil {
		return 0, err
	}
	return appt.ID, nil
}

func init() {
	// Register the appointment fulfiller so payment capture can create the
	// confirmed appointment for a slot-owned inventory.
	booking.RegisterFulfiller("slot", slotFulfiller{})
}
