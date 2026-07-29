package booking

import (
	"encoding/json"
	"net/http"
	"time"

	kernel "github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/ticketing"
	"go.uber.org/zap"
)

// loadEvent fetches an event by its public slug.
func loadEvent(ctx server_context.BackEndContext, r *http.Request, slug string) (models.BookingEvent, bool) {
	var event models.BookingEvent
	res := db(ctx, r).Where("slug = ?", slug).Limit(1).Find(&event)
	return event, res.RowsAffected == 1
}

type seatReq struct {
	Label   string `json:"label"`
	Section string `json:"section"`
	RowName string `json:"row_name"`
	Number  int    `json:"number"`
}

type tierReq struct {
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	PriceCents int64     `json:"price_cents"`
	Currency   string    `json:"currency"`
	Capacity   int       `json:"capacity"`
	Seats      []seatReq `json:"seats"`
}

type createEventReq struct {
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	StartAt     time.Time `json:"start_at"`
	Timezone    string    `json:"timezone"`
	MaxPerBuyer int       `json:"max_per_buyer"`
	Tiers       []tierReq `json:"tiers"`
}

// createEvent creates an event with its tiers and seats, owned by the caller.
//
// The two tier kinds carry different requirements, and both are checked here
// rather than left to the domain: a GA tier is a pool and needs a capacity, a
// seated tier is a set of distinct positions and needs the seats themselves.
// Either one missing produces a tier that exists but can never sell.
func createEvent(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := api_auth.CurrentUser(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var req createEventReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request payload", http.StatusBadRequest)
			return
		}
		if req.Slug == "" || req.Name == "" || len(req.Tiers) == 0 {
			http.Error(w, "slug, name and at least one tier are required", http.StatusBadRequest)
			return
		}

		in := ticketing.EventInput{
			OwnerID: identity.UserID, Slug: req.Slug, Name: req.Name, StartAt: req.StartAt,
			Timezone: req.Timezone, MaxPerBuyer: req.MaxPerBuyer,
		}
		for _, t := range req.Tiers {
			switch {
			case t.Kind != "ga" && t.Kind != "seated":
				http.Error(w, "tier kind must be ga or seated", http.StatusBadRequest)
				return
			case t.Kind == "ga" && t.Capacity < 1:
				http.Error(w, "ga tier requires capacity >= 1", http.StatusBadRequest)
				return
			case t.Kind == "seated" && len(t.Seats) == 0:
				http.Error(w, "seated tier requires seats", http.StatusBadRequest)
				return
			}

			tier := ticketing.TierInput{
				Name: t.Name, Kind: t.Kind, PriceCents: t.PriceCents,
				Currency: t.Currency, Capacity: t.Capacity,
			}
			for _, st := range t.Seats {
				tier.Seats = append(tier.Seats, ticketing.SeatInput{
					Label: st.Label, Section: st.Section, RowName: st.RowName, Number: st.Number,
				})
			}
			in.Tiers = append(in.Tiers, tier)
		}

		event, err := ticketing.CreateEvent(db(ctx, r), in)
		if err != nil {
			ctx.Logger.Error("create event", zap.Error(err))
			http.Error(w, "slug likely taken or database error", http.StatusConflict)
			return
		}

		writeJSON(w, http.StatusCreated, event)
	}
}

// tierView is a tier plus what is left of it, which is the number a buyer
// actually cares about and the one a tier row does not carry.
type tierView struct {
	models.BookingTicketTier
	Remaining int `json:"remaining"`
}

// getEvent returns an event with its tiers and their remaining availability.
//
// The two tier kinds are counted differently because they are stored
// differently: a GA tier has one pooled inventory row to subtract from, while a
// seated tier's availability is how many of its seats are still free.
func getEvent(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, ok := loadEvent(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}

		conn := db(ctx, r)
		var tiers []models.BookingTicketTier
		conn.Where("event_id = ?", event.ID).Order("id").Find(&tiers)

		views := make([]tierView, 0, len(tiers))
		for _, t := range tiers {
			remaining := 0
			if t.Kind == "ga" {
				if inv, err := kernel.InventoryByOwner(conn, kernel.TierOwner(t.ID)); err == nil {
					remaining = inv.Capacity - inv.Booked - inv.Held - inv.Blocked
				}
			} else {
				var n int64
				conn.Raw(`SELECT count(*) FROM booking_seats s
					JOIN booking_inventories i ON i.seat_id = s.id
					WHERE s.tier_id = ? AND s.kind = 'sellable'
					  AND i.booked + i.held + i.blocked < i.capacity`, t.ID).Scan(&n)
				remaining = int(n)
			}
			views = append(views, tierView{BookingTicketTier: t, Remaining: remaining})
		}

		writeJSON(w, http.StatusOK, map[string]any{"event": event, "tiers": views})
	}
}

// seatView is one seat as the seat map renders it: its position, and whether it
// can be picked.
type seatView struct {
	ID      uint   `json:"id"`
	Label   string `json:"label"`
	Section string `json:"section"`
	RowName string `json:"row_name"`
	Number  int    `json:"number"`
	State   string `json:"state"` // free|held|booked|blocked
}

// seatMap returns an event's seats, optionally narrowed to one section, each
// with the state the map should draw it in.
//
// Held seats are reported as taken. The hold may well expire, but a map that
// showed them free would invite a buyer to pick a seat that is going to be
// refused at checkout.
func seatMap(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, ok := loadEvent(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}

		type row struct {
			ID                              uint
			Label, Section, RowName, Kind   string
			Number                          int
			Booked, Held, Blocked, Capacity int
		}

		query := `SELECT s.id, s.label, s.section, s.row_name, s.number, s.kind,
				i.booked, i.held, i.blocked, i.capacity
			FROM booking_seats s
			JOIN booking_ticket_tiers t ON t.id = s.tier_id
			JOIN booking_inventories i ON i.seat_id = s.id
			WHERE t.event_id = ?`
		args := []any{event.ID}
		if section := r.URL.Query().Get("section"); section != "" {
			query += " AND s.section = ?"
			args = append(args, section)
		}
		query += " ORDER BY s.section, s.row_name, s.number"

		var rows []row
		db(ctx, r).Raw(query, args...).Scan(&rows)

		out := make([]seatView, 0, len(rows))
		for _, rr := range rows {
			state := "free"
			switch {
			// A seat that is not sellable at all (house, ada, comp, kill) reads
			// the same as one held back by an operator: present on the map, not
			// available to this buyer.
			case rr.Kind != "sellable" || rr.Blocked > 0:
				state = "blocked"
			case rr.Booked > 0:
				state = "booked"
			case rr.Held > 0:
				state = "held"
			}
			out = append(out, seatView{
				ID: rr.ID, Label: rr.Label, Section: rr.Section,
				RowName: rr.RowName, Number: rr.Number, State: state,
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}
