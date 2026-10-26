package booking

import (
	"errors"
	"fmt"
	"time"

	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrCaptureRefunded means a payment arrived for an order that could not be
// fulfilled (its holds lapsed, or it was already cancelled or expired). The
// payment is kept on record and a refund for it has been written; nothing was
// issued.
var ErrCaptureRefunded = errors.New("booking: order could not be fulfilled; payment refunded")

// CaptureOrder records a payment for an order, then commits every hold on it
// and fulfils each item.
//
// The payment is written in its own transaction, before fulfilment. Money that
// reached the provider is a fact whatever happens to the seats, so a hold that
// lapsed between checkout and this call must not roll the payment row back with
// it. When fulfilment fails for that reason, the order is cancelled and the
// payment refunded, and ErrCaptureRefunded is returned.
//
// Idempotent: a repeated payment (same IdempotencyKey) is not recorded twice,
// and an order that is already paid is left alone.
func CaptureOrder(db *gorm.DB, orderID uint, pay models.BookingPayment, now time.Time) error {
	pay.OrderID = orderID
	if err := WithWriteRetry(func() error {
		return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&pay).Error
	}); err != nil {
		return fmt.Errorf("record payment: %w", err)
	}

	// A duplicate delivery still runs the fulfilment step: if the first
	// delivery's payment was recorded but its fulfilment never ran (a crash
	// between the two), the retry is what finishes it. Fulfilment checks the
	// order status under a lock, so running it twice is harmless.
	var unfulfillable bool
	err := WithWriteRetry(func() error {
		unfulfillable = false
		return db.Transaction(func(tx *gorm.DB) error {
			order, err := lockOrder(tx, orderID)
			if err != nil {
				return err
			}
			switch order.Status {
			case "paid":
				return nil
			case "pending":
			default:
				unfulfillable = true
				return nil
			}

			err = fulfilTx(tx, order, now)
			if errors.Is(err, ErrHoldExpired) || errors.Is(err, ErrSoldOut) {
				unfulfillable = true
				return errRollback
			}
			return err
		})
	})
	if errors.Is(err, errRollback) {
		err = nil
	}
	if err != nil {
		return err
	}
	if !unfulfillable {
		return nil
	}

	if err := CancelOrder(db, orderID, now); err != nil {
		return fmt.Errorf("refund unfulfillable order %d: %w", orderID, err)
	}
	return ErrCaptureRefunded
}

// errRollback rolls a transaction back without being an error to the caller.
var errRollback = errors.New("booking: rollback")

// lockOrder loads an order and holds its row lock for the transaction, so two
// deliveries, or a delivery and a cancel, are serialised per order.
func lockOrder(tx *gorm.DB, orderID uint) (models.BookingOrder, error) {
	var order models.BookingOrder
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, orderID).Error
	return order, err
}

// fulfilTx commits each item's hold, runs its domain fulfiller and marks the
// order paid. The caller holds the order lock.
func fulfilTx(tx *gorm.DB, order models.BookingOrder, now time.Time) error {
	// Only reserved items are paid for. An item dropped before checkout (its
	// hold given back) is cancelled and is not part of the total.
	var items []models.BookingOrderItem
	if err := tx.Where("order_id = ? AND status = 'reserved'", order.ID).Find(&items).Error; err != nil {
		return err
	}
	for _, it := range items {
		h, err := CommitTx(tx, it.HoldToken, now)
		if err != nil {
			return err
		}
		var inv models.BookingInventory
		if err := tx.First(&inv, h.InventoryID).Error; err != nil {
			return err
		}
		f, ok := FulfillerFor(OwnerKind(inv))
		if !ok {
			return fmt.Errorf("booking: no fulfiller for inventory %d", inv.ID)
		}
		fid, err := f.Fulfill(tx, order, it, inv)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.BookingOrderItem{}).Where("id = ?", it.ID).
			Updates(map[string]any{"status": "fulfilled", "fulfilled_id": fid}).Error; err != nil {
			return err
		}
	}
	return tx.Model(&models.BookingOrder{}).
		Where("id = ? AND status = 'pending'", order.ID).
		Update("status", "paid").Error
}

// CancelOrder undoes an order, whatever stage it reached.
//
//   - Reserved items: the hold is released and its units offered to the
//     waitlist.
//   - Fulfilled items (a paid order): the domain object is revoked (a ticket
//     refunded, an appointment cancelled), the booked units are released and
//     offered to the waitlist.
//   - Money: whatever was charged and not yet refunded is refunded.
//
// The order ends "refunded" when money went back, "cancelled" when it was
// pending and nothing had been charged. An order already cancelled, expired or
// refunded with nothing left to return is a no-op, so a retry is safe.
func CancelOrder(db *gorm.DB, orderID uint, now time.Time) error {
	return WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return cancelOrderTx(tx, orderID, now)
		})
	})
}

func cancelOrderTx(tx *gorm.DB, orderID uint, now time.Time) error {
	order, err := lockOrder(tx, orderID)
	if err != nil {
		return err
	}

	var items []models.BookingOrderItem
	if err := tx.Where("order_id = ?", order.ID).Order("inventory_id").Find(&items).Error; err != nil {
		return err
	}
	for _, it := range items {
		switch it.Status {
		case "reserved":
			if it.HoldToken != "" {
				if err := ReleaseTx(tx, it.HoldToken, now); err != nil {
					return err
				}
			}
			if err := setItemStatus(tx, it.ID, "cancelled"); err != nil {
				return err
			}
		case "fulfilled":
			if err := revokeItemTx(tx, it, now); err != nil {
				return err
			}
			if err := setItemStatus(tx, it.ID, "refunded"); err != nil {
				return err
			}
		}
	}

	refunded, err := refundDueTx(tx, order)
	if err != nil {
		return err
	}

	next := order.Status
	switch {
	case refunded || order.Status == "paid":
		next = "refunded"
	case order.Status == "pending":
		next = "cancelled"
	}
	if next == order.Status {
		return nil
	}
	return tx.Model(&models.BookingOrder{}).Where("id = ?", order.ID).Update("status", next).Error
}

func setItemStatus(tx *gorm.DB, itemID uint, status string) error {
	return tx.Model(&models.BookingOrderItem{}).Where("id = ?", itemID).Update("status", status).Error
}

// revokeItemTx undoes one fulfilled item: the domain revokes what it issued,
// then the booked units go back to the inventory and on to the waitlist.
func revokeItemTx(tx *gorm.DB, it models.BookingOrderItem, now time.Time) error {
	var inv models.BookingInventory
	if err := tx.First(&inv, it.InventoryID).Error; err != nil {
		return err
	}
	f, ok := FulfillerFor(OwnerKind(inv))
	if !ok {
		return fmt.Errorf("booking: no fulfiller for inventory %d", inv.ID)
	}
	r, ok := f.(Revoker)
	if !ok {
		return fmt.Errorf("booking: %s fulfiller cannot revoke", OwnerKind(inv))
	}
	if err := r.Revoke(tx, it, inv); err != nil {
		return err
	}
	res := tx.Model(&models.BookingInventory{}).
		Where("id = ? AND booked >= ?", inv.ID, it.Qty).
		UpdateColumn("booked", gorm.Expr("booked - ?", it.Qty))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("booking: revoke item %d: booked below item qty on inventory %d", it.ID, inv.ID)
	}
	return offerToWaitlist(tx, inv.ID, it.Qty, defaultOfferTTL, now)
}

// RefundOrder returns whatever an order was charged and has not yet had back,
// as one mock refund payment. It does not touch seats, slots or tickets:
// cancelling what was bought is CancelOrder, which refunds through this.
// Returns whether a refund was written.
func RefundOrder(db *gorm.DB, orderID uint) (bool, error) {
	var refunded bool
	err := WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			order, err := lockOrder(tx, orderID)
			if err != nil {
				return err
			}
			refunded, err = refundDueTx(tx, order)
			return err
		})
	})
	return refunded, err
}

// refundDueTx writes a refund for the charged-but-not-refunded amount. The
// caller holds the order lock, which is what makes the amount and the
// idempotency key stable: the key names how much had been refunded before
// this one, so a replay under the same state collides instead of refunding
// twice.
func refundDueTx(tx *gorm.DB, order models.BookingOrder) (bool, error) {
	var sums struct{ Charged, Refunded int64 }
	if err := tx.Model(&models.BookingPayment{}).
		Select(`COALESCE(SUM(CASE WHEN kind <> 'refund' THEN amount_cents END), 0) AS charged,
			COALESCE(SUM(CASE WHEN kind = 'refund' THEN amount_cents END), 0) AS refunded`).
		Where("order_id = ? AND status = 'succeeded'", order.ID).
		Scan(&sums).Error; err != nil {
		return false, err
	}
	due := sums.Charged - sums.Refunded
	if due <= 0 {
		return false, nil
	}
	refund := models.BookingPayment{
		OrderID: order.ID, Provider: "mock", Kind: "refund", Status: "succeeded",
		AmountCents: due, Currency: order.Currency,
		IdempotencyKey: fmt.Sprintf("refund:order:%d:%d", order.ID, sums.Refunded),
	}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&refund)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
