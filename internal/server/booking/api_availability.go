package booking

import (
	"net/http"
	"time"

	appt "github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/booking/live"
	"github.com/erancihan/clair/internal/database/models"
	server_context "github.com/erancihan/clair/internal/server/context"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// availability returns the open slots of a schedule.
//
// With no range asked for, it serves the snapshot every reader of the schedule
// shares — the same bytes the stream's first frame carries — with an ETag, so a
// poller that already has them gets a 304. A caller-supplied range is computed
// directly, floored at now and capped at the schedule's own window.
//
// Slots are computed from the schedule's rules rather than stored: a schedule
// that runs weekly for a year is a handful of rules, and writing a row per slot
// up front would make editing those rules a migration. Only slots somebody has
// actually booked exist as rows.
func availability(ctx server_context.BackEndContext, hub *live.Hub, cache *snapshotCache[[]appt.OpenSlot]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadSchedule(ctx, r, r.PathValue("slug"))
		if !ok || !sched.Active {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}

		q := r.URL.Query()
		if q.Get("from") == "" && q.Get("to") == "" {
			snap, err := cache.Snapshot(scheduleKey(sched.ID), scheduleVersion(hub, sched.ID))
			if err != nil {
				ctx.Logger.Error("availability snapshot", zap.Error(err))
				http.Error(w, "could not compute availability", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("ETag", snap.ETag)
			w.Header().Set("Cache-Control", "no-cache")
			if r.Header.Get("If-None-Match") == snap.ETag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(snap.JSON)
			return
		}

		now := time.Now()

		// `from` is caller-supplied and the generator walks day by day from it;
		// nothing before now is bookable, so nothing before now is computed.
		from := now
		if ts, err := time.Parse(time.RFC3339, q.Get("from")); err == nil && ts.After(now) {
			from = ts
		}

		// The schedule's own booking window is the ceiling. A caller asking for a
		// range beyond it is clamped rather than refused, but it can never widen
		// how far ahead the schedule is bookable.
		to := now.AddDate(0, 0, sched.WindowDays)
		if ts, err := time.Parse(time.RFC3339, q.Get("to")); err == nil && ts.Before(to) {
			to = ts
		}

		slots, err := appt.OpenSlotsFor(db(ctx, r), &sched, from, to, now)
		if err != nil {
			ctx.Logger.Error("compute availability", zap.Error(err))
			http.Error(w, "could not compute availability", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, slots)
	}
}

// availabilityViews computes a schedule's open slots from now to the end of its
// window. Availability is derived from rules rather than stored, which is why
// it is produced once per change and shared, rather than repeated per reader —
// and why the window is the schedule's whole window: the cost is per change,
// and a screen must never be shown less than the schedule offers.
func availabilityViews(conn *gorm.DB, sched models.BookingSchedule, now time.Time) ([]appt.OpenSlot, error) {
	return appt.OpenSlotsFor(conn, &sched, now, now.AddDate(0, 0, sched.WindowDays), now)
}
