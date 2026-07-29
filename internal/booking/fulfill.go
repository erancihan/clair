package booking

import (
	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
)

// Fulfiller creates the domain object for a captured order item. It runs inside
// the capture transaction, after the hold is committed, and must be idempotent
// (return the existing id if item.FulfilledID is already set).
type Fulfiller interface {
	Fulfill(tx *gorm.DB, order models.BookingOrder, item models.BookingOrderItem, inv models.BookingInventory) (uint, error)
}

var fulfillers = map[string]Fulfiller{} // key: owner kind "slot"|"seat"|"tier"

// RegisterFulfiller registers the fulfiller for an inventory owner kind. Domains
// call this at init; registration is explicit, not auto-discovered.
func RegisterFulfiller(kind string, f Fulfiller) { fulfillers[kind] = f }

// FulfillerFor returns the fulfiller for an owner kind.
func FulfillerFor(kind string) (Fulfiller, bool) { f, ok := fulfillers[kind]; return f, ok }

// OwnerKind reports which typed owner an inventory row belongs to.
func OwnerKind(inv models.BookingInventory) string {
	switch {
	case inv.SlotID != nil:
		return "slot"
	case inv.SeatID != nil:
		return "seat"
	case inv.TierID != nil:
		return "tier"
	}
	return ""
}
