package models

import "time"

// BookingTicketTier is one price tier of an event.
//
// The two kinds differ in where the count lives. A "ga" tier owns one pooled
// inventory row, so selling is a counter increment. A "seated" tier owns no
// inventory at all: each of its seats owns a capacity-1 row, because a seat is
// either taken or it is not, and pooling them would lose which one.
type BookingTicketTier struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	EventID    uint      `json:"event_id" gorm:"index"`
	Name       string    `json:"name" gorm:"not null"`
	Kind       string    `json:"kind" gorm:"not null;check:kind IN ('ga','seated')"`
	PriceCents int64     `json:"price_cents" gorm:"not null"`
	Currency   string    `json:"currency" gorm:"not null;default:USD"`
}
