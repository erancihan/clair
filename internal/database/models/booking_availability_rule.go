package models

import "time"

// BookingAvailabilityRule is one recurring weekly window in its schedule's
// timezone. Start and end are minutes from local midnight, so the rule is
// independent of any particular date and survives daylight-saving changes.
type BookingAvailabilityRule struct {
	ID         uint `gorm:"primaryKey"`
	CreatedAt  time.Time
	ScheduleID uint `gorm:"index"`
	Weekday    int  `gorm:"check:weekday >= 0 AND weekday <= 6"` // 0=Sunday .. 6=Saturday
	StartMin   int  // minutes from local midnight, inclusive
	EndMin     int  // minutes from local midnight, exclusive
}
