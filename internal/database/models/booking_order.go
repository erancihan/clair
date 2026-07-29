package models

import "time"

// BookingOrder is the money aggregate shared by every booking flow that sells:
// the appointments paid flow and ticketing both use it unchanged. Buyer contact
// fields are deliberately generic so a domain fulfiller can copy them onto
// whatever it creates (an appointment, a ticket).
//
// OwnerRef carries the reference the authentication layer produces, so an order
// placed by a guest can be re-pointed at an account when that visitor signs in.
type BookingOrder struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Domain   string `json:"domain" gorm:"index;not null"` // "appointment" | "event"
	OwnerRef string `json:"-" gorm:"index"`               // "user:<id>" | "guest:<sid>"
	UserID   *uint  `json:"user_id" gorm:"index"`         // nil for guest

	BuyerName  string `json:"buyer_name"`
	BuyerEmail string `json:"buyer_email" gorm:"index"`

	Status        string `json:"status" gorm:"index;not null;default:pending"` // pending|paid|cancelled|expired|refunded
	SubtotalCents int64  `json:"subtotal_cents"`
	FeeCents      int64  `json:"fee_cents"`
	TaxCents      int64  `json:"tax_cents"`
	DiscountCents int64  `json:"discount_cents"`
	TotalCents    int64  `json:"total_cents"`
	Currency      string `json:"currency"`
}
