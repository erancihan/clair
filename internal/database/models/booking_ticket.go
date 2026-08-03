package models

import "time"

// BookingTicket is the artifact a captured order item is fulfilled into: the
// thing the buyer actually holds. It is created at capture time, never at hold
// time, so an abandoned checkout leaves no ticket behind.
//
// SeatID is null for general admission, where the tier is the whole of what was
// bought.
type BookingTicket struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	CreatedAt   time.Time `json:"created_at"`
	OrderItemID uint      `json:"order_item_id" gorm:"index;not null"`
	EventID     uint      `json:"event_id" gorm:"index"`
	TierID      uint      `json:"tier_id" gorm:"index"`
	SeatID      *uint     `json:"seat_id" gorm:"index"` // null for GA
	Code        string    `json:"code" gorm:"uniqueIndex;not null"`
	HolderName  string    `json:"holder_name"`
	HolderEmail string    `json:"holder_email" gorm:"index"`
	Status      string    `json:"status" gorm:"index;not null;default:valid"` // valid|refunded|checked_in
}
