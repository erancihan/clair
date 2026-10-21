package appointments

import (
	"errors"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrNotOpen means the requested time is not a slot this schedule offers.
	ErrNotOpen = errors.New("appointments: requested time is not an open slot")
	// ErrPast means the requested time is in the past (or within lead time).
	ErrPast = errors.New("appointments: cannot book a slot in the past")
)

// BookInput is a request to reserve a single seat in a schedule's slot.
type BookInput struct {
	Schedule   *models.BookingSchedule
	StartAt    time.Time
	UserID     *uint
	GuestName  string
	GuestEmail string
}

// Book reserves one seat in the slot at StartAt, using the free (no-hold,
// no-payment) flow: validate the time is offered, lazily materialize the slot +
// its inventory, then atomically increment the counter. Returns ErrNotOpen /
// ErrPast / booking.ErrSoldOut on the respective failures.
func Book(db *gorm.DB, in BookInput, now time.Time) (*models.BookingAppointment, error) {
	startUTC := in.StartAt.UTC()
	if !startUTC.After(now) {
		return nil, ErrPast
	}

	var appt *models.BookingAppointment
	err := booking.WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			// Inside the transaction, so an exception inserted between the check
			// and the write is seen by the write.
			if err := validateOpen(tx, in.Schedule, startUTC, now); err != nil {
				return err
			}
			slot, err := ensureSlot(tx, in.Schedule, startUTC)
			if err != nil {
				return err
			}
			inv, err := booking.EnsureInventory(tx, booking.SlotOwner(slot.ID), slot.Capacity)
			if err != nil {
				return err
			}
			if err := booking.BookDirect(tx, inv.ID, 1); err != nil {
				return err
			}
			a := &models.BookingAppointment{
				ScheduleID:  in.Schedule.ID,
				SlotID:      slot.ID,
				UserID:      in.UserID,
				GuestName:   in.GuestName,
				GuestEmail:  in.GuestEmail,
				StartAt:     startUTC,
				EndAt:       slot.EndAt,
				Status:      "confirmed",
				CancelToken: booking.NewToken(),
			}
			if err := tx.Create(a).Error; err != nil {
				return err
			}
			appt = a
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return appt, nil
}

// CancelByToken cancels a confirmed appointment and frees its slot. Idempotent:
// an unknown or already-cancelled token is a no-op.
func CancelByToken(db *gorm.DB, cancelToken string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var a models.BookingAppointment
		err := tx.Where("cancel_token = ? AND status = ?", cancelToken, "confirmed").First(&a).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		inv, err := booking.InventoryForSlot(tx, a.SlotID)
		if err != nil {
			return err
		}
		if err := booking.Cancel(tx, inv.ID, 1, time.Now()); err != nil {
			return err
		}

		// If this appointment came from a paid order, refund it (mock refund) and
		// mark the order/item refunded. Free appointments have no order item.
		// fulfilled_id is only unique within a domain, so the order's domain is
		// part of the match.
		var item models.BookingOrderItem
		if err := tx.Joins("JOIN booking_orders o ON o.id = booking_order_items.order_id").
			Where("booking_order_items.fulfilled_id = ? AND booking_order_items.status = ? AND o.domain = ?",
				a.ID, "fulfilled", "appointment").
			Limit(1).Find(&item).Error; err != nil {
			return err
		}
		if item.ID != 0 {
			if err := tx.Model(&models.BookingOrderItem{}).Where("id = ?", item.ID).Update("status", "refunded").Error; err != nil {
				return err
			}
			if err := tx.Model(&models.BookingOrder{}).Where("id = ?", item.OrderID).Update("status", "refunded").Error; err != nil {
				return err
			}
			if err := tx.Create(&models.BookingPayment{
				OrderID: item.OrderID, Provider: "mock", Kind: "refund", Status: "succeeded",
				AmountCents: item.PriceCents, IdempotencyKey: "refund-" + a.CancelToken,
			}).Error; err != nil {
				return err
			}
		}

		return tx.Model(&a).Update("status", "cancelled").Error
	})
}

// ensureSlot upserts the concrete slot row for (schedule, startAt) and returns
// the canonical row.
func ensureSlot(tx *gorm.DB, sched *models.BookingSchedule, startUTC time.Time) (*models.BookingSlot, error) {
	slot := models.BookingSlot{
		ScheduleID: sched.ID,
		StartAt:    startUTC,
		EndAt:      startUTC.Add(time.Duration(sched.SlotDurationMin) * time.Minute),
		Capacity:   sched.Capacity,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&slot).Error; err != nil {
		return nil, err
	}
	var out models.BookingSlot
	if err := tx.Where("schedule_id = ? AND start_at = ?", sched.ID, startUTC).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// validateOpen checks that startUTC is one of the instants the schedule
// offers on that local day — the same generator the availability endpoint
// uses, so the two can never disagree about a DST edge, lead time, the window
// or an exception. Capacity is NOT checked here — the atomic guard in the
// caller handles that race-free.
func validateOpen(tx *gorm.DB, sched *models.BookingSchedule, startUTC, now time.Time) error {
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		return err
	}
	local := startUTC.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)

	var rules []models.BookingAvailabilityRule
	if err := tx.Where("schedule_id = ? AND weekday = ?", sched.ID, int(day.Weekday())).Find(&rules).Error; err != nil {
		return err
	}
	var exs []models.BookingAvailabilityException
	if err := tx.Where("schedule_id = ? AND date = ?", sched.ID, day.Format("2006-01-02")).Find(&exs).Error; err != nil {
		return err
	}

	starts, err := offeredStarts(sched, rules, exs, startUTC, startUTC.Add(time.Nanosecond), now)
	if err != nil {
		return err
	}
	for _, s := range starts {
		if s.Equal(startUTC) {
			return nil
		}
	}
	return ErrNotOpen
}
