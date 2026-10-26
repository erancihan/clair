package booking

import (
	"fmt"
	"net/http"
	"time"

	"github.com/erancihan/clair/internal/booking/live"
	server_context "github.com/erancihan/clair/internal/server/context"
	"go.uber.org/zap"
)

// seatStreamDebounce collapses the burst a single action produces. Holding six
// seats writes six inventory rows and so raises six notifications; a buyer needs
// one redraw, not six.
const seatStreamDebounce = 150 * time.Millisecond

// seatStreamHeartbeat keeps the connection from being reaped by an idle proxy.
const seatStreamHeartbeat = 25 * time.Second

// seatPollCoalesce is how coarsely the snapshot version advances when there is
// no listener to count changes. Without it every poller on the fallback path
// would read the database for itself; with it they share a read.
const seatPollCoalesce = 500 * time.Millisecond

// seatClockVersionBase keeps clock-derived versions out of the counter's range,
// so a fallback version can never be mistaken for a change count.
const seatClockVersionBase = uint64(1) << 62

// seatVersion names which snapshot a reader wants.
//
// While the listener is up that is the change count, so everyone who learns
// about the same change asks for the same snapshot and exactly one of them pays
// to build it. With no listener there is nothing to count, so a coarse clock
// stands in and the herd costs a couple of reads a second instead of one per
// reader.
func seatVersion(hub *live.Hub, eventID uint) uint64 {
	if version, listening := hub.Version(eventKey(eventID)); listening {
		return version
	}
	return seatClockVersionBase + uint64(time.Now().UnixMilli()/seatPollCoalesce.Milliseconds())
}

// seatStream pushes an event's seat map as it changes.
//
// The alternative was polling, and polling is what this replaces: a seat taken
// by somebody else showed up whenever the next tick happened to land, which is
// exactly the moment a buyer is deciding. Changes arrive from Postgres, so a
// release performed by the scheduler in another process reaches this stream the
// same way a hold from this one does.
//
// It is a public read, like the seat map it streams.
func seatStream(ctx server_context.BackEndContext, hub *live.Hub, cache *snapshotCache[[]seatView]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, ok := loadEvent(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		signals, release := hub.Subscribe(eventKey(event.ID))
		defer release()

		// Say so rather than holding open a stream that will never carry
		// anything: the client falls back to polling on this status.
		if !hub.Ready(r.Context(), 2*time.Second) {
			http.Error(w, "live updates unavailable", http.StatusServiceUnavailable)
			return
		}

		// The first frame is the current truth, so a subscriber never has to fetch
		// the map separately to know where it stands. It is read before the
		// headers go out, so a read that fails can still be answered honestly.
		first, err := cache.Snapshot(eventKey(event.ID), seatVersion(hub, event.ID))
		if err != nil {
			ctx.Logger.Error("seat stream first frame", zap.Error(err))
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

		// Every watcher of this showing shares one rendered snapshot per change,
		// so a hold on a busy showing is one query and one marshal in total
		// rather than one of each per connection. A read that fails mid-stream
		// skips a frame; the next change tries again.
		send := func() bool {
			snap, err := cache.Snapshot(eventKey(event.ID), seatVersion(hub, event.ID))
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
				// Let the rest of the burst land before redrawing.
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

func drain(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
