package booking

import (
	"fmt"
	"net/http"
	"time"

	appt "github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/booking/live"
	server_context "github.com/erancihan/clair/internal/server/context"
	"go.uber.org/zap"
)

// scheduleVersion names which snapshot of a schedule a reader wants, on the same
// terms as seats: the change count while a listener is up, a coarse clock when
// there is none.
func scheduleVersion(hub *live.Hub, scheduleID uint) uint64 {
	if version, listening := hub.Version(scheduleKey(scheduleID)); listening {
		return version
	}
	return seatClockVersionBase + uint64(time.Now().UnixMilli()/seatPollCoalesce.Milliseconds())
}

// availabilityStream pushes a schedule's open times as they change.
//
// A booking made in another browser removes a time this one is about to offer.
// Polling would surface that whenever the next tick happened to land, which is
// exactly the moment somebody is deciding. Changes arrive from Postgres, so a
// cancellation swept by the scheduler in another process reaches this stream the
// same way a booking from this one does.
func availabilityStream(ctx server_context.BackEndContext, hub *live.Hub, cache *snapshotCache[[]appt.OpenSlot]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadSchedule(ctx, r, r.PathValue("slug"))
		if !ok || !sched.Active {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		key := scheduleKey(sched.ID)
		signals, release := hub.Subscribe(key)
		defer release()

		// Say so rather than holding open a stream that will never carry
		// anything: the client falls back to polling on this status.
		if !hub.Ready(r.Context(), 2*time.Second) {
			http.Error(w, "live updates unavailable", http.StatusServiceUnavailable)
			return
		}

		// The first frame is the current truth, so a subscriber never has to fetch
		// the times separately to know where it stands. It is read before the
		// headers go out, so a read that fails can still be answered honestly.
		first, err := cache.Snapshot(key, scheduleVersion(hub, sched.ID))
		if err != nil {
			ctx.Logger.Error("availability stream first frame", zap.Error(err))
			http.Error(w, "live updates unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		write := func(snap snapshot) bool {
			if _, err := fmt.Fprintf(w, "data: %s\n\n", snap.JSON); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}

		// A read that fails mid-stream skips a frame; the next change tries again.
		send := func() bool {
			snap, err := cache.Snapshot(key, scheduleVersion(hub, sched.ID))
			if err != nil {
				return true
			}
			return write(snap)
		}

		if !write(first) {
			return
		}

		heartbeat := time.NewTicker(seatStreamHeartbeat)
		defer heartbeat.Stop()

		for {
			select {
			case <-ctx.Closing:
				return

			case <-r.Context().Done():
				return

			case <-signals:
				// Let the rest of the burst land before recomputing.
				timer := time.NewTimer(seatStreamDebounce)
				select {
				case <-r.Context().Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				drain(signals)
				if !send() {
					return
				}

			case <-heartbeat.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}
