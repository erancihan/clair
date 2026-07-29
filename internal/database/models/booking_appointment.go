package models

import "time"

// BookingAppointment is a single confirmed booking against a slot.
//
// UserID is nil for a guest booking, which is why CancelToken exists: a visitor
// with no account still needs one unguessable handle that lets them cancel
// without proving who they are.
type BookingAppointment struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ScheduleID  uint      `json:"schedule_id" gorm:"index"`
	SlotID      uint      `json:"slot_id" gorm:"index"`
	UserID      *uint     `json:"user_id" gorm:"index"` // nil for guest bookings
	GuestName   string    `json:"guest_name"`
	GuestEmail  string    `json:"guest_email" gorm:"index"`
	StartAt     time.Time `json:"start_at" gorm:"not null"` // UTC
	EndAt       time.Time `json:"end_at" gorm:"not null"`
	Status      string    `json:"status" gorm:"index;not null;default:confirmed"` // confirmed|cancelled
	CancelToken string    `json:"cancel_token" gorm:"uniqueIndex;not null"`
}
