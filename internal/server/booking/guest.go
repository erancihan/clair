package booking

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
)

// migrateGuest hands a visitor's in-progress booking work over to the account
// they just signed into.
//
// Somebody who picks a seat, then signs in to pay, would otherwise lose the hold
// they were paying for: the hold is stamped with the guest reference they had
// when they took it, and after login every subsequent request carries their user
// reference instead. Re-pointing the rows is what makes signing in mid-checkout
// a continuation rather than a restart.
//
// Orders additionally get user_id set, so the appointment or ticket that comes
// out of a capture is linked to the account and not only to the guest session
// that started it.
//
// The whole thing is a set of ref-scoped updates and so is safe to repeat, which
// it has to be: the guest cookie outlives the login, and the same visitor can
// sign in from the same browser any number of times. A second run simply matches
// no rows.
func migrateGuest(ctx context.Context, db *gorm.DB, guestRef, userRef string) error {
	userID, err := userIDFromRef(userRef)
	if err != nil {
		return err
	}

	tx := db.WithContext(ctx)

	return tx.Transaction(func(tx *gorm.DB) error {
		// Active holds first: they are the time-critical ones, since an
		// unclaimed hold expires while the visitor is signing in.
		if err := tx.Model(&models.BookingHold{}).
			Where("owner_ref = ? AND status = 'active'", guestRef).
			Update("owner_ref", userRef).Error; err != nil {
			return fmt.Errorf("re-own holds: %w", err)
		}

		// Only orders still in flight are moved. A cancelled or expired order is
		// history, and a paid one has already been fulfilled to whoever bought
		// it — reassigning either would rewrite a completed record.
		if err := tx.Model(&models.BookingOrder{}).
			Where("owner_ref = ? AND status = 'pending'", guestRef).
			Updates(map[string]any{"owner_ref": userRef, "user_id": userID}).Error; err != nil {
			return fmt.Errorf("re-own orders: %w", err)
		}

		// Waiting and offered positions both move, so the queue place survives
		// the login and an offer already extended stays claimable.
		if err := tx.Model(&models.BookingWaitlistEntry{}).
			Where("owner_ref = ? AND status IN ('waiting','offered')", guestRef).
			Update("owner_ref", userRef).Error; err != nil {
			return fmt.Errorf("re-own waitlist entries: %w", err)
		}

		return nil
	})
}

// userIDFromRef extracts the numeric id from a "user:<id>" owner reference.
//
// The format is the authentication layer's, and it is parsed rather than assumed
// because BookingOrder stores the id as its own column: a silently wrong parse
// would link an order to the wrong account.
func userIDFromRef(userRef string) (uint, error) {
	raw, ok := strings.CutPrefix(userRef, "user:")
	if !ok {
		return 0, fmt.Errorf("booking: unexpected user ref %q", userRef)
	}

	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("booking: unparseable user ref %q", userRef)
	}

	return uint(id), nil
}
