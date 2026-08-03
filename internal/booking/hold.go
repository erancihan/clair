package booking

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrHoldExpired means the hold is no longer active (expired, released, or gone).
var ErrHoldExpired = errors.New("booking: hold expired or not active")

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WithWriteRetry retries fn on a Postgres deadlock (40P01) or serialization
// failure (40001). Business errors (ErrSoldOut, ErrHoldExpired) are not retried.
func WithWriteRetry(fn func() error) error {
	const maxAttempts = 5
	backoff := 20 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !isRetryable(err) || attempt == maxAttempts {
			return err
		}
		time.Sleep(backoff)
		backoff *= 2
	}
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "40001") || strings.Contains(s, "40P01") ||
		strings.Contains(s, "deadlock detected") || strings.Contains(s, "could not serialize")
}

// HoldUnitsTx soft-reserves qty units inside a caller-provided transaction.
//
// Steps: (1) release this inventory's expired active holds; (2) recompute `held`
// authoritatively from surviving active holds (self-correcting, not a racy
// decrement); (3) guarded increment of `held`; (4) idempotent hold insert — a
// second active hold for the same (inventory, owner) reuses the first (backed by
// the partial unique index), undoing our increment.
//
// `now` is injected so tests can drive expiry deterministically.
func HoldUnitsTx(tx *gorm.DB, invID uint, qty int, owner string, orderID *uint, purpose string, ttl time.Duration, now time.Time) (*models.BookingHold, error) {
	// Reclaim this inventory's expired active holds and decrement `held` by exactly
	// the reclaimed quantity, in ONE data-modifying CTE. Using RETURNING (not a
	// snapshot-dependent SUM subquery) keeps this race-safe under READ COMMITTED:
	// `held` is maintained incrementally (+qty hold, -qty commit/release/reclaim),
	// and the row lock the outer UPDATE takes serialises concurrent callers.
	if err := tx.Exec(`
		WITH reclaimed AS (
			UPDATE booking_holds SET status = 'released', updated_at = now()
			WHERE inventory_id = ? AND status = 'active' AND expires_at <= ?
			RETURNING qty
		)
		UPDATE booking_inventories
		SET held = held - COALESCE((SELECT SUM(qty) FROM reclaimed), 0)
		WHERE id = ?`, invID, now, invID).Error; err != nil {
		return nil, err
	}

	res := tx.Model(&models.BookingInventory{}).
		Where("id = ? AND booked + held + blocked + ? <= capacity", invID, qty).
		UpdateColumn("held", gorm.Expr("held + ?", qty))
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrSoldOut
	}

	h := &models.BookingHold{
		Token: newToken(), InventoryID: invID, Qty: qty, OwnerRef: owner,
		OrderID: orderID, Purpose: purpose, ExpiresAt: now.Add(ttl), Status: "active",
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(h).Error; err != nil {
		return nil, err
	}
	if h.ID == 0 { // conflict → an active hold already exists for (inv, owner)
		if err := tx.Model(&models.BookingInventory{}).
			Where("id = ? AND held >= ?", invID, qty).
			UpdateColumn("held", gorm.Expr("held - ?", qty)).Error; err != nil {
			return nil, err
		}
		var existing models.BookingHold
		if err := tx.Where("inventory_id = ? AND owner_ref = ? AND status = 'active'", invID, owner).
			First(&existing).Error; err != nil {
			return nil, err
		}
		return &existing, nil
	}
	return h, nil
}

// HoldUnits opens its own transaction (with write-retry) around HoldUnitsTx.
func HoldUnits(db *gorm.DB, invID uint, qty int, owner string, orderID *uint, ttl time.Duration, now time.Time) (*models.BookingHold, error) {
	var h *models.BookingHold
	err := WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			var e error
			h, e = HoldUnitsTx(tx, invID, qty, owner, orderID, "cart", ttl, now)
			return e
		})
	})
	return h, err
}

// CommitTx converts a hold to booked (held--→booked++) inside a caller txn.
// Idempotent: an already-committed token returns success. Row-locks the hold on
// Postgres so Commit and the reaper never disagree at the expiry boundary.
func CommitTx(tx *gorm.DB, token string, now time.Time) (*models.BookingHold, error) {
	q := tx
	if tx.Dialector != nil && tx.Dialector.Name() == "postgres" {
		q = tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var h models.BookingHold
	err := q.Where("token = ?", token).First(&h).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrHoldExpired
	}
	if err != nil {
		return nil, err
	}
	if h.Status == "committed" {
		return &h, nil // idempotent replay
	}
	if h.Status != "active" || !now.Before(h.ExpiresAt) {
		return nil, ErrHoldExpired
	}
	res := tx.Model(&models.BookingInventory{}).
		Where("id = ? AND held >= ?", h.InventoryID, h.Qty).
		UpdateColumns(map[string]any{
			"held":   gorm.Expr("held - ?", h.Qty),
			"booked": gorm.Expr("booked + ?", h.Qty),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrHoldExpired
	}
	if err := tx.Model(&h).Update("status", "committed").Error; err != nil {
		return nil, err
	}
	return &h, nil
}

// Commit runs CommitTx in its own transaction.
func Commit(db *gorm.DB, token string, now time.Time) error {
	return WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			_, e := CommitTx(tx, token, now)
			return e
		})
	})
}

// defaultOfferTTL is how long a freed unit is reserved for a waitlisted owner.
const defaultOfferTTL = 10 * time.Minute

// ReleaseTx frees an active hold's units, then offers the freed unit(s) to the
// waitlist head (else they return to open inventory). Idempotent no-op if the
// hold is already gone. If the released hold was itself a waitlist offer, its
// waitlist entry is marked expired so it is not reused.
func ReleaseTx(tx *gorm.DB, token string, now time.Time) error {
	var h models.BookingHold
	err := tx.Where("token = ? AND status = 'active'", token).First(&h).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.Model(&models.BookingInventory{}).
		Where("id = ? AND held >= ?", h.InventoryID, h.Qty).
		UpdateColumn("held", gorm.Expr("held - ?", h.Qty)).Error; err != nil {
		return err
	}
	if err := tx.Model(&h).Update("status", "released").Error; err != nil {
		return err
	}
	if h.Purpose == "waitlist_offer" {
		if err := tx.Model(&models.BookingWaitlistEntry{}).
			Where("offer_token = ?", token).Update("status", "expired").Error; err != nil {
			return err
		}
	}
	return offerToWaitlist(tx, h.InventoryID, h.Qty, defaultOfferTTL, now)
}

// Release runs ReleaseTx in its own transaction.
func Release(db *gorm.DB, token string, now time.Time) error {
	return WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return ReleaseTx(tx, token, now)
		})
	})
}

// offerToWaitlist converts `freed` newly-available units into time-boxed offers
// for the oldest waiting entries (FIFO via FOR UPDATE SKIP LOCKED, so concurrent
// freers never offer the same unit twice). Units with no waiter stay open.
func offerToWaitlist(tx *gorm.DB, invID uint, freed int, offerTTL time.Duration, now time.Time) error {
	for i := 0; i < freed; i++ {
		var entry models.BookingWaitlistEntry
		res := tx.Raw(`SELECT * FROM booking_waitlist_entries
			WHERE inventory_id = ? AND status = 'waiting'
			ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, invID).Scan(&entry)
		if res.Error != nil {
			return res.Error
		}
		if entry.ID == 0 {
			return nil // no waiters → unit returns to open inventory
		}
		h, err := HoldUnitsTx(tx, invID, 1, entry.OwnerRef, nil, "waitlist_offer", offerTTL, now)
		if err != nil {
			return nil // couldn't re-hold (shouldn't happen); leave unit open
		}
		tok := h.Token
		exp := h.ExpiresAt
		if err := tx.Model(&models.BookingWaitlistEntry{}).Where("id = ?", entry.ID).
			Updates(map[string]any{"status": "offered", "offer_token": tok, "offer_expiry": exp}).Error; err != nil {
			return err
		}
	}
	return nil
}

// JoinWaitlist adds (idempotently) a waiting entry for an owner on an inventory
// row and returns the entry plus its 1-based position.
func JoinWaitlist(db *gorm.DB, invID uint, ownerRef string) (*models.BookingWaitlistEntry, int, error) {
	var entry models.BookingWaitlistEntry
	err := WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			e := models.BookingWaitlistEntry{InventoryID: invID, OwnerRef: ownerRef, Status: "waiting"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&e).Error; err != nil {
				return err
			}
			if e.ID == 0 { // already waiting → reuse
				if err := tx.Where("inventory_id = ? AND owner_ref = ? AND status = 'waiting'", invID, ownerRef).First(&e).Error; err != nil {
					return err
				}
			}
			entry = e
			return nil
		})
	})
	if err != nil {
		return nil, 0, err
	}
	var ahead int64
	db.Model(&models.BookingWaitlistEntry{}).
		Where("inventory_id = ? AND status = 'waiting' AND created_at < ?", invID, entry.CreatedAt).Count(&ahead)
	return &entry, int(ahead) + 1, nil
}

// CaptureOrder commits every hold on an order and fulfils each item in ONE
// transaction (so the money path is atomic), then marks the order paid.
// Idempotent via the payment IdempotencyKey. `pay` supplies the payment record.
func CaptureOrder(db *gorm.DB, orderID uint, pay models.BookingPayment, now time.Time) error {
	return WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			pay.OrderID = orderID
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&pay)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return nil // duplicate webhook delivery
			}

			var order models.BookingOrder
			if err := tx.First(&order, orderID).Error; err != nil {
				return err
			}
			if order.Status == "paid" {
				return nil
			}

			var items []models.BookingOrderItem
			if err := tx.Where("order_id = ?", orderID).Find(&items).Error; err != nil {
				return err
			}
			for i := range items {
				it := items[i]
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
				Where("id = ? AND status = 'pending'", orderID).
				Update("status", "paid").Error
		})
	})
}

// ReapExpiredHolds releases expired active holds and expires stale pending
// orders whose holds are all gone. Returns the number of holds released.
func ReapExpiredHolds(db *gorm.DB, now time.Time) (int, error) {
	var holds []models.BookingHold
	if err := db.Where("status = 'active' AND expires_at <= ?", now).Limit(500).Find(&holds).Error; err != nil {
		return 0, err
	}
	released := 0
	for _, h := range holds {
		if err := Release(db, h.Token, now); err == nil {
			released++
		}
	}
	db.Model(&models.BookingOrder{}).
		Where("status = 'pending' AND updated_at <= ?", now.Add(-30*time.Minute)).
		Where("NOT EXISTS (SELECT 1 FROM booking_holds WHERE booking_holds.order_id = booking_orders.id AND booking_holds.status = 'active')").
		Update("status", "expired")
	return released, nil
}
