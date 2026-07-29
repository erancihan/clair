// Package appointments is the appointments domain: DST-correct availability
// computation and the (free) office-hours booking flow, on top of the booking
// reservation kernel. It never imports net/http.
package appointments

import (
	"time"

	"github.com/erancihan/clair/internal/database/models"
)

// OpenSlot is a bookable time offered by a schedule, in UTC.
type OpenSlot struct {
	StartAt   time.Time `json:"start_at"`
	EndAt     time.Time `json:"end_at"`
	Remaining int       `json:"remaining"` // capacity - already booked
}

// ComputeAvailability returns bookable slots whose start is in [from, to).
//
// All wall-clock math happens in the schedule's timezone, then converts to UTC
// — this is what makes it DST-correct: a "09:00 Monday" rule stays 09:00 local
// across a DST boundary. Non-existent local times (spring-forward gap) are
// skipped. `booked` maps a slot's UTC start to its current booked count.
func ComputeAvailability(
	sched *models.BookingSchedule,
	rules []models.BookingAvailabilityRule,
	exceptions []models.BookingAvailabilityException,
	booked map[time.Time]int,
	from, to, now time.Time,
) ([]OpenSlot, error) {
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		return nil, err
	}

	exByDate := map[string][]models.BookingAvailabilityException{}
	for _, e := range exceptions {
		exByDate[e.Date] = append(exByDate[e.Date], e)
	}

	earliest := now.Add(time.Duration(sched.LeadTimeMin) * time.Minute)
	latest := now.AddDate(0, 0, sched.WindowDays)
	dur := time.Duration(sched.SlotDurationMin) * time.Minute

	var out []OpenSlot

	// Walk day by day in local time so weekday and DST are correct.
	start := from.In(loc)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	for day.Before(to) {
		dateKey := day.Format("2006-01-02")
		wd := int(day.Weekday())

		for _, rule := range rules {
			if rule.Weekday != wd {
				continue
			}
			for m := rule.StartMin; m+sched.SlotDurationMin <= rule.EndMin; m += sched.SlotDurationMin {
				local := time.Date(day.Year(), day.Month(), day.Day(), 0, m, 0, 0, loc)
				// Skip a wall time that does not exist on this date (DST gap):
				// time.Date normalizes it forward, changing the minute-of-day.
				if local.Hour()*60+local.Minute() != m {
					continue
				}

				startUTC := local.UTC()
				endUTC := startUTC.Add(dur)

				if startUTC.Before(from) || !startUTC.Before(to) {
					continue
				}
				if startUTC.Before(earliest) || startUTC.After(latest) {
					continue
				}
				if blockedByException(exByDate[dateKey], m, m+sched.SlotDurationMin) {
					continue
				}

				remaining := sched.Capacity - booked[startUTC]
				if remaining > 0 {
					out = append(out, OpenSlot{StartAt: startUTC, EndAt: endUTC, Remaining: remaining})
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return out, nil
}

// blockedByException reports whether [slotStart, slotEnd) (minutes-of-day) is
// blocked by any exception for that date.
func blockedByException(exs []models.BookingAvailabilityException, slotStart, slotEnd int) bool {
	for _, e := range exs {
		if e.StartMin == nil || e.EndMin == nil { // whole day
			return true
		}
		if slotStart < *e.EndMin && slotEnd > *e.StartMin { // overlap
			return true
		}
	}
	return false
}
