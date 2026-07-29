package models

import "time"

// BookingPayment records one authorization, capture or refund event. An order
// has several rows over its life rather than a single mutable status.
//
// IdempotencyKey holds the payment provider's delivery id and is uniquely
// indexed, so a re-delivered webhook collides on insert instead of capturing an
// order twice. That unique index is the whole of the webhook's replay defence.
type BookingPayment struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	OrderID        uint   `json:"order_id" gorm:"index;not null"`
	Provider       string `json:"provider"`
	ProviderRef    string `json:"provider_ref" gorm:"index"`
	Kind           string `json:"kind" gorm:"not null;default:charge"`          // authorization|capture|charge|refund
	Status         string `json:"status" gorm:"index;not null;default:pending"` // pending|succeeded|failed
	AmountCents    int64  `json:"amount_cents"`
	Currency       string `json:"currency"`
	IdempotencyKey string `json:"-" gorm:"uniqueIndex"`
}
