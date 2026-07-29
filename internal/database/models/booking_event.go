package models

import "time"

// BookingEvent is a single fixed-datetime event: a concert, a showtime. It is
// the ticketing counterpart of a schedule, and the difference is the whole of
// why both exist: a schedule generates times, an event has exactly one.
//
// MaxPerBuyer caps how many tickets one buyer may hold across the event, which
// is what stops a single client from draining a tier the moment it opens.
type BookingEvent struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	OwnerID     uint      `json:"owner_id" gorm:"index"`
	Slug        string    `json:"slug" gorm:"uniqueIndex;not null"`
	Name        string    `json:"name" gorm:"not null"`
	StartAt     time.Time `json:"start_at" gorm:"not null"` // fixed showtime, UTC
	Timezone    string    `json:"timezone" gorm:"not null;default:UTC"`
	Status      string    `json:"status" gorm:"index;not null;default:scheduled"` // scheduled|cancelled
	MaxPerBuyer int       `json:"max_per_buyer" gorm:"not null;default:0"`        // 0 = unlimited
}
