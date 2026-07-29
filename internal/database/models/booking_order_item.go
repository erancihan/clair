package models

import "time"

// BookingOrderItem is domain-agnostic: it points at a kernel hold and the
// inventory row that hold covered. The inventory row's typed FK is what tells
// the fulfiller what was actually reserved, which is why an item carries no
// slot, tier or seat column of its own.
type BookingOrderItem struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	OrderID     uint   `json:"order_id" gorm:"index;not null"`
	HoldToken   string `json:"hold_token" gorm:"index"`
	InventoryID uint   `json:"inventory_id" gorm:"index;not null"`
	Qty         int    `json:"qty" gorm:"not null;default:1"`
	PriceCents  int64  `json:"price_cents"`
	Status      string `json:"status" gorm:"index;not null;default:reserved"` // reserved|fulfilled|cancelled|refunded
	FulfilledID *uint  `json:"fulfilled_id"`                                  // id of the created domain object
}
