package models

import "time"

// BookingInventory is the booking domain's single shared primitive: a countable
// pool guarded by an atomic capacity check. Every reservable thing (an
// appointment slot, a ticket tier, a seat) owns exactly one row, and exactly one
// owner FK is set.
//
// INVARIANT: booked + held + blocked <= capacity. It is enforced by the WHERE
// clause of every mutation in the kernel, not by a constraint, because the guard
// has to be the same statement that does the increment.
//
// All booking types are prefixed Booking* so GORM derives booking_* table names
// that stay disjoint from sibling domains (e.g. the shop's shop_* tables). See
// docs/booking-system-design.md.
type BookingInventory struct {
	ID        uint `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	// Exactly one of these is non-null. Nullable-unique indexes let many rows
	// share a NULL on the columns they don't use, because Postgres treats NULLs
	// as distinct in a unique index.
	SlotID *uint `gorm:"uniqueIndex"` // appointment slot
	TierID *uint `gorm:"uniqueIndex"` // GA ticket tier
	SeatID *uint `gorm:"uniqueIndex"` // one assigned seat

	Capacity int `gorm:"not null;check:capacity >= 0"`
	Booked   int `gorm:"not null;default:0;check:booked  >= 0"`
	Held     int `gorm:"not null;default:0;check:held    >= 0"`
	Blocked  int `gorm:"not null;default:0;check:blocked >= 0"`
}
