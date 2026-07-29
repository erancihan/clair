package models

import "time"

// BookingSchedule is an "office hours" definition owned by a host user: the
// recurring shape from which concrete slots are derived.
//
// Times are stored as a timezone plus minute offsets rather than as absolute
// instants, so a schedule keeps meaning across a daylight-saving transition:
// "every Tuesday at 09:00 local" stays 09:00 local on both sides of the change.
type BookingSchedule struct {
	ID              uint      `json:"id" gorm:"primaryKey"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	OwnerID         uint      `json:"owner_id" gorm:"index"`
	Slug            string    `json:"slug" gorm:"uniqueIndex;not null"` // public URL id
	Name            string    `json:"name" gorm:"not null"`
	Description     string    `json:"description"`
	Timezone        string    `json:"timezone" gorm:"not null"` // IANA, e.g. "Europe/Istanbul"
	SlotDurationMin int       `json:"slot_duration_min" gorm:"not null"`
	Capacity        int       `json:"capacity" gorm:"not null;check:capacity >= 1"` // applicants per slot
	LeadTimeMin     int       `json:"lead_time_min" gorm:"not null;default:0"`      // min notice before a slot is bookable
	WindowDays      int       `json:"window_days" gorm:"not null;default:30"`       // how far ahead bookings are allowed
	Active          bool      `json:"active" gorm:"not null;default:true"`

	// Payment and hold configuration. RequiresPayment=false is the free office
	// hours flow, which books directly. RequiresPayment=true is the paid flow:
	// the slot is held for HoldTTLMin during checkout and confirmed on capture.
	RequiresPayment bool   `json:"requires_payment" gorm:"not null;default:false"`
	PriceCents      int64  `json:"price_cents" gorm:"not null;default:0"`
	Currency        string `json:"currency" gorm:"not null;default:USD"`
	HoldTTLMin      int    `json:"hold_ttl_min" gorm:"not null;default:10"`
}
