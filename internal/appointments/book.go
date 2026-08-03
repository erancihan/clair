package appointments

import (
	"crypto/rand"
	"encoding/hex"
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
	if err := validateOpen(db, in.Schedule, startUTC, now); err != nil {
		return nil, err
	}

	var appt *models.BookingAppointment
	err := db.Transaction(func(tx *gorm.DB) error {
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
			CancelToken: newToken(),
		}
		if err := tx.Create(a).Error; err != nil {
			return err
		}
		appt = a
		return nil
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
		var item models.BookingOrderItem
		if err := tx.Where("fulfilled_id = ? AND status = ?", a.ID, "fulfilled").Limit(1).Find(&item).Error; err != nil {
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

// validateOpen checks the requested UTC instant aligns to a rule-derived slot,
// is inside lead-time/window, and is not blocked by an exception. Capacity is
// NOT checked here — the atomic guard in Book handles that race-free.
func validateOpen(db *gorm.DB, sched *models.BookingSchedule, startUTC, now time.Time) error {
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		return err
	}
	if startUTC.Before(now.Add(time.Duration(sched.LeadTimeMin) * time.Minute)) {
		return ErrNotOpen
	}
	if startUTC.After(now.AddDate(0, 0, sched.WindowDays)) {
		return ErrNotOpen
	}

	local := startUTC.In(loc)
	minute := local.Hour()*60 + local.Minute()
	wd := int(local.Weekday())

	var rules []models.BookingAvailabilityRule
	if err := db.Where("schedule_id = ? AND weekday = ?", sched.ID, wd).Find(&rules).Error; err != nil {
		return err
	}
	aligned := false
	for _, r := range rules {
		if minute < r.StartMin || minute+sched.SlotDurationMin > r.EndMin {
			continue
		}
		if (minute-r.StartMin)%sched.SlotDurationMin == 0 {
			aligned = true
			break
		}
	}
	if !aligned {
		return ErrNotOpen
	}

	var exs []models.BookingAvailabilityException
	if err := db.Where("schedule_id = ? AND date = ?", sched.ID, local.Format("2006-01-02")).Find(&exs).Error; err != nil {
		return err
	}
	if blockedByException(exs, minute, minute+sched.SlotDurationMin) {
		return ErrNotOpen
	}
	return nil
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
