// Package appointments is the appointments domain: DST-correct availability
// computation and the (free) office-hours booking flow, on top of the booking
// booking service. It never imports net/http.
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

// SlotCount is what a materialised slot knows about itself: how many are
// booked, and the capacity it was materialised with. A slot that has never been
// booked has no row and no entry; its capacity is the schedule's.
type SlotCount struct {
	Booked   int
	Capacity int
}

// ComputeAvailability returns bookable slots whose start is in [from, to).
//
// The instants come from offeredStarts; this only subtracts what is booked.
// A slot's remaining count is measured against the capacity it was materialised
// with, because that is the number the booking guard enforces.
func ComputeAvailability(
	sched *models.BookingSchedule,
	rules []models.BookingAvailabilityRule,
	exceptions []models.BookingAvailabilityException,
	counts map[time.Time]SlotCount,
	from, to, now time.Time,
) ([]OpenSlot, error) {
	starts, err := offeredStarts(sched, rules, exceptions, from, to, now)
	if err != nil {
		return nil, err
	}
	dur := time.Duration(sched.SlotDurationMin) * time.Minute
	out := make([]OpenSlot, 0, len(starts))
	for _, startUTC := range starts {
		capacity := sched.Capacity
		c, ok := counts[startUTC]
		if ok && c.Capacity > 0 {
			capacity = c.Capacity
		}
		if remaining := capacity - c.Booked; remaining > 0 {
			out = append(out, OpenSlot{StartAt: startUTC, EndAt: startUTC.Add(dur), Remaining: remaining})
		}
	}
	return out, nil
}

// offeredStarts returns, in order, every UTC instant the rules offer in
// [from, to), honouring lead time, the booking window, exceptions and DST.
// Capacity is not its concern.
//
// All wall-clock math happens in the schedule's timezone, then converts to UTC
// — this is what makes it DST-correct: a "09:00 Monday" rule stays 09:00 local
// across a DST boundary. A local time that does not exist (spring-forward gap)
// is skipped; a local time that exists twice (fall-back) is offered once, at
// the instant time.Date resolves it to, so a booking can never materialise two
// slots for one wall-clock time.
func offeredStarts(
	sched *models.BookingSchedule,
	rules []models.BookingAvailabilityRule,
	exceptions []models.BookingAvailabilityException,
	from, to, now time.Time,
) ([]time.Time, error) {
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

	var out []time.Time

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
				if startUTC.Before(from) || !startUTC.Before(to) {
					continue
				}
				if startUTC.Before(earliest) || startUTC.After(latest) {
					continue
				}
				if blockedByException(exByDate[dateKey], m, m+sched.SlotDurationMin) {
					continue
				}
				out = append(out, startUTC)
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
