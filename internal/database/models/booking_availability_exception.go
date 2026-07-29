package models

import "time"

// BookingAvailabilityException subtracts from a schedule on one specific local
// date: a holiday, or an afternoon the host is away. It overrides the weekly
// rules rather than replacing them.
//
// A nil StartMin and EndMin blocks the whole date; a pair of values blocks only
// that part of it.
type BookingAvailabilityException struct {
	ID         uint `gorm:"primaryKey"`
	CreatedAt  time.Time
	ScheduleID uint   `gorm:"index"`
	Date       string `gorm:"index"` // "2006-01-02" in the schedule's timezone
	StartMin   *int   // nil, nil => the whole day is blocked
	EndMin     *int
}
