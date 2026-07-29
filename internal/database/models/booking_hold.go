package models

import "time"

// BookingHold is an expiring soft-reservation over one inventory row, taken
// before payment or confirmation. The authoritative expiry lives here; Valkey,
// when it is added, is only an accelerator. A multi-item cart is several holds
// sharing one OrderID.
//
// The uq_active_bhold index below backs idempotency: an owner holds a given
// inventory row at most once. It has to be partial — restricted to active holds —
// because released and committed holds are kept as history, and a plain unique
// index would stop the same owner ever holding that row again.
//
// idx_bhold_reap serves the reaper, which sweeps for active holds that have
// expired and is the one query in the system that runs on a timer rather than in
// response to a request.
type BookingHold struct {
	ID        uint `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Token       string    `json:"token" gorm:"uniqueIndex;not null"` // opaque cart handle
	InventoryID uint      `json:"inventory_id" gorm:"index;not null;uniqueIndex:uq_active_bhold,priority:1,where:status = 'active'"`
	OrderID     *uint     `json:"order_id" gorm:"index"` // groups multi-item carts
	Qty         int       `json:"qty" gorm:"not null;default:1;check:qty >= 1"`
	OwnerRef    string    `json:"-" gorm:"not null;uniqueIndex:uq_active_bhold,priority:2"` // "user:<id>" | "guest:<sid>"
	ExpiresAt   time.Time `json:"expires_at" gorm:"index;not null;index:idx_bhold_reap,priority:2"`
	Status      string    `json:"status" gorm:"index;not null;default:active;index:idx_bhold_reap,priority:1"` // active|committed|released
	Purpose     string    `json:"purpose" gorm:"not null;default:cart"`                                        // cart|waitlist_offer
}
