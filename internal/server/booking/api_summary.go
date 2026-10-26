package booking

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/erancihan/clair/internal/booking/live"
	"github.com/erancihan/clair/internal/database/models"
	server_context "github.com/erancihan/clair/internal/server/context"
	"gorm.io/gorm"
)

// maxSummarySlugs bounds one request. An uncapped list is an invitation to make
// a single call do arbitrary work.
const maxSummarySlugs = 50

// eventSummary is a showing as a board lists it: when it is, what it costs, and
// how much of it is left. It is deliberately not the whole event, because a
// board draws none of the rest.
type eventSummary struct {
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	StartAt     time.Time `json:"start_at"`
	Status      string    `json:"status"`
	MaxPerBuyer int       `json:"max_per_buyer"`
	TierID      uint      `json:"tier_id"`
	Kind        string    `json:"kind"`
	PriceCents  int64     `json:"price_cents"`
	Currency    string    `json:"currency"`
	Remaining   int       `json:"remaining"`
}

// summaryFor reads one showing's summary. Every seeded showing sells through a
// single tier, so the first tier is the showing.
func summaryFor(conn *gorm.DB, eventID uint) ([]eventSummary, error) {
	var event models.BookingEvent
	res := conn.Limit(1).Find(&event, eventID)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}

	var tier models.BookingTicketTier
	res = conn.Where("event_id = ?", event.ID).Order("id").Limit(1).Find(&tier)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}

	return []eventSummary{{
		Slug: event.Slug, Name: event.Name, StartAt: event.StartAt,
		Status: event.Status, MaxPerBuyer: event.MaxPerBuyer,
		TierID: tier.ID, Kind: tier.Kind, PriceCents: tier.PriceCents,
		Currency: tier.Currency, Remaining: tierRemaining(conn, tier),
	}}, nil
}

// requestedSlugs parses and bounds the slugs parameter.
func requestedSlugs(r *http.Request) ([]string, bool) {
	raw := r.URL.Query().Get("slugs")
	if raw == "" {
		return nil, false
	}
	slugs := strings.Split(raw, ",")
	if len(slugs) > maxSummarySlugs {
		return nil, false
	}
	return slugs, true
}

// resolveEventIDs turns a caller's slug list into event ids, in the order
// asked, skipping any that name nothing. Called once per request or stream:
// the ids do not change while a connection is open, so nothing re-resolves
// them per change.
//
// A stale entry in a page's catalog is not a reason to fail the whole board; the
// page reports the shortfall itself.
func resolveEventIDs(conn *gorm.DB, slugs []string) []uint {
	ids := make([]uint, 0, len(slugs))
	for _, slug := range slugs {
		var event models.BookingEvent
		if res := conn.Where("slug = ?", strings.TrimSpace(slug)).Limit(1).Find(&event); res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		ids = append(ids, event.ID)
	}
	return ids
}

// summariesFor reads the shared snapshot of each showing. It touches no table
// itself; a change to one showing costs one reload of that showing's summary.
func summariesFor(hub *live.Hub, cache *snapshotCache[[]eventSummary], ids []uint) []eventSummary {
	rows := make([]eventSummary, 0, len(ids))
	for _, id := range ids {
		snap, err := cache.Snapshot(eventKey(id), seatVersion(hub, id))
		if err != nil {
			continue
		}
		var summaries []eventSummary
		if err := json.Unmarshal(snap.JSON, &summaries); err == nil {
			rows = append(rows, summaries...)
		}
	}
	return rows
}

func topicsFor(ids []uint) []string {
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = eventKey(id)
	}
	return keys
}

// eventSummaries answers a whole board in one request.
//
// A page listing thirteen showings used to ask thirteen times for the same kind
// of answer. That is thirteen round trips and thirteen times the work to draw
// one screen.
func eventSummaries(ctx server_context.BackEndContext, hub *live.Hub, cache *snapshotCache[[]eventSummary]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slugs, ok := requestedSlugs(r)
		if !ok {
			http.Error(w, "slugs is required, and at most 50 of them", http.StatusBadRequest)
			return
		}
		ids := resolveEventIDs(db(ctx, r), slugs)
		writeJSON(w, http.StatusOK, summariesFor(hub, cache, ids))
	}
}

// eventSummaryStream pushes a whole board as its showings change.
//
// One connection covers every showing the page lists. A stream per showing would
// be simpler to write and unusable in practice: a browser allows about six
// connections per origin over HTTP/1.1, and each open stream holds one for its
// whole life — a thirteen-showing board would spend the tab's entire budget on
// itself.
func eventSummaryStream(ctx server_context.BackEndContext, hub *live.Hub, cache *snapshotCache[[]eventSummary]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slugs, ok := requestedSlugs(r)
		if !ok {
			http.Error(w, "slugs is required, and at most 50 of them", http.StatusBadRequest)
			return
		}
		// Resolved once for the life of the connection. Every later frame is
		// cache reads only, so a change costs the same however many are watching.
		ids := resolveEventIDs(db(ctx, r), slugs)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		signals, release := hub.SubscribeAll(topicsFor(ids))
		defer release()

		if !hub.Ready(r.Context(), 2*time.Second) {
			http.Error(w, "live updates unavailable", http.StatusServiceUnavailable)
			return
		}

		// Read only once the listener is up: a snapshot taken before that is
		// stamped with the clock stand-in, and the first change afterwards would
		// have every showing on the board reload at once.
		rows := summariesFor(hub, cache, ids)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		send := func(payload []eventSummary) bool {
			body, err := json.Marshal(payload)
			if err != nil {
				return false
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}

		if !send(rows) {
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
				timer := time.NewTimer(seatStreamDebounce)
				select {
				case <-r.Context().Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				drain(signals)

				if !send(summariesFor(hub, cache, ids)) {
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
