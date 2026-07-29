package booking

import (
	"net/http"
	"time"

	appt "github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/database/models"
	server_context "github.com/erancihan/clair/internal/server/context"
	"go.uber.org/zap"
)

// availability returns the open slots of a schedule over a time range.
//
// Slots are computed from the schedule's rules on every request rather than
// stored: a schedule that runs weekly for a year is a handful of rules, and
// writing a row per slot up front would make editing those rules a migration.
// Only slots somebody has actually booked exist as rows, which is what the
// booked counts below are read from.
func availability(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadSchedule(ctx, r, r.PathValue("slug"))
		if !ok || !sched.Active {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}

		now := time.Now()

		from := now
		if q := r.URL.Query().Get("from"); q != "" {
			if ts, err := time.Parse(time.RFC3339, q); err == nil {
				from = ts
			}
		}

		// The schedule's own booking window is the ceiling. A caller asking for a
		// range beyond it is clamped rather than refused, but it can never widen
		// how far ahead the schedule is bookable.
		to := now.AddDate(0, 0, sched.WindowDays)
		if q := r.URL.Query().Get("to"); q != "" {
			if ts, err := time.Parse(time.RFC3339, q); err == nil && ts.Before(to) {
				to = ts
			}
		}

		conn := db(ctx, r)
		var rules []models.BookingAvailabilityRule
		var exceptions []models.BookingAvailabilityException
		conn.Where("schedule_id = ?", sched.ID).Find(&rules)
		conn.Where("schedule_id = ?", sched.ID).Find(&exceptions)

		// Booked counts for the slots that have been materialized in this range.
		// The join is left, not inner: a slot row exists from the moment it is
		// first booked, but its inventory row is what carries the count.
		type slotCount struct {
			StartAt time.Time
			Booked  int
		}
		var counts []slotCount
		conn.Raw(`SELECT bs.start_at AS start_at, COALESCE(bi.booked, 0) AS booked
			FROM booking_slots bs
			LEFT JOIN booking_inventories bi ON bi.slot_id = bs.id
			WHERE bs.schedule_id = ? AND bs.start_at >= ? AND bs.start_at < ?`,
			sched.ID, from.UTC(), to.UTC()).Scan(&counts)

		booked := make(map[time.Time]int, len(counts))
		for _, c := range counts {
			booked[c.StartAt.UTC()] = c.Booked
		}

		slots, err := appt.ComputeAvailability(&sched, rules, exceptions, booked, from, to, now)
		if err != nil {
			ctx.Logger.Error("compute availability", zap.Error(err))
			http.Error(w, "invalid schedule timezone", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, slots)
	}
}
