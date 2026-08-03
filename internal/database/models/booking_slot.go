package models

import "time"

// BookingSlot is a concrete bookable time, materialized from a schedule's rules
// the first time somebody books it. Availability itself is computed, so a
// schedule does not write a row per slot up front.
//
// A slot owns a kernel BookingInventory row and keeps no counter of its own:
// there is exactly one authoritative count per reservable thing, and it lives in
// the inventory row the capacity guard writes.
type BookingSlot struct {
	ID         uint `gorm:"primaryKey"`
	CreatedAt  time.Time
	ScheduleID uint      `gorm:"index;uniqueIndex:idx_bslot_key"`
	StartAt    time.Time `gorm:"uniqueIndex:idx_bslot_key;not null"` // UTC
	EndAt      time.Time `gorm:"not null"`
	Capacity   int       `gorm:"not null"` // snapshot of the schedule's capacity at materialization
}
