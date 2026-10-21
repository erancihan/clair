package booking

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/ticketing"
	"go.uber.org/zap"
)

// cartTTL is how long a ticketing hold survives without being paid for. It is
// the trade-off between giving a buyer time to finish checkout and letting an
// abandoned cart keep a seat out of circulation.
const cartTTL = 10 * time.Minute

// maxSeatsPerRequest and maxGAPerRequest bound one hold request independently
// of the event's own per-buyer cap, which is zero — unlimited — by default.
const (
	maxSeatsPerRequest = 10
	maxGAPerRequest    = 10
)

// uniqueIDs collapses repeated ids, keeping first-seen order.
func uniqueIDs(ids []uint) []uint {
	seen := make(map[uint]bool, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// buyer builds the purchaser for a ticketing request.
//
// OwnerRef is what the per-buyer limit and the waitlist are counted against, and
// it comes from the shared authentication layer so a guest's holds carry the
// same reference their account inherits when they sign in.
func buyer(w http.ResponseWriter, r *http.Request, name, email string) ticketing.Buyer {
	b := ticketing.Buyer{OwnerRef: api_auth.OwnerRef(w, r), Name: name, Email: email}
	if identity, ok := api_auth.CurrentUser(r.Context()); ok {
		uid := identity.UserID
		b.UserID = &uid
	}
	return b
}

// loadGATier fetches a GA tier belonging to an event, writing the error response
// itself when there is none. The kind is part of the query: a seated tier sells
// through the seat routes, and treating one as a pool would hand out tickets
// with no seat behind them.
func loadGATier(ctx server_context.BackEndContext, w http.ResponseWriter, r *http.Request, eventID uint) (models.BookingTicketTier, bool) {
	tierID, ok := parseID(r, "tierID")
	if !ok {
		http.Error(w, "invalid tier id", http.StatusBadRequest)
		return models.BookingTicketTier{}, false
	}

	var tier models.BookingTicketTier
	res := db(ctx, r).Where("id = ? AND event_id = ? AND kind = 'ga'", tierID, eventID).Limit(1).Find(&tier)
	if res.RowsAffected == 0 {
		http.Error(w, "GA tier not found", http.StatusNotFound)
		return models.BookingTicketTier{}, false
	}

	return tier, true
}

type holdGAReq struct {
	Qty        int    `json:"qty"`
	GuestName  string `json:"guest_name"`
	GuestEmail string `json:"guest_email"`
}

// holdGA holds units of a general admission tier and opens a pending order.
func holdGA(ctx server_context.BackEndContext, cache *snapshotCache[[]seatView]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, ok := loadEvent(ctx, r, r.PathValue("slug"))
		if !ok || event.Status != "scheduled" {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		tier, ok := loadGATier(ctx, w, r, event.ID)
		if !ok {
			return
		}

		var body holdGAReq
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Qty <= 0 {
			body.Qty = 1
		}
		if body.Qty > maxGAPerRequest {
			http.Error(w, "too many tickets in one request", http.StatusBadRequest)
			return
		}

		order, hold, err := ticketing.HoldGA(
			db(ctx, r), &tier, body.Qty,
			buyer(w, r, body.GuestName, body.GuestEmail),
			cartTTL, time.Now(), event.MaxPerBuyer,
		)
		switch {
		case errors.Is(err, booking.ErrSoldOut):
			http.Error(w, "not enough tickets available", http.StatusConflict)
		case errors.Is(err, ticketing.ErrPurchaseLimit):
			http.Error(w, "per-buyer purchase limit exceeded", http.StatusConflict)
		case errors.Is(err, ticketing.ErrEventClosed):
			http.Error(w, "this showing is no longer on sale", http.StatusConflict)
		case err != nil:
			ctx.Logger.Error("hold GA tickets", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
		default:
			// This process just moved inventory; say so, so the next read of the
			// map cannot be answered from before it.
			cache.Bump(eventKey(event.ID))

			// qty is what is actually held, which is not always what was asked
			// for: a buyer who already holds more keeps it. The page renders these
			// numbers back to them, so they have to be the real ones.
			writeJSON(w, http.StatusCreated, map[string]any{
				"order":       order,
				"hold_token":  hold.Token,
				"qty":         hold.Qty,
				"expires_at":  hold.ExpiresAt,
				"total_cents": order.TotalCents,
			})
		}
	}
}

type holdSeatsReq struct {
	SeatIDs    []uint `json:"seat_ids"`
	GuestName  string `json:"guest_name"`
	GuestEmail string `json:"guest_email"`
}

// holdSeats holds a set of assigned seats, all or nothing.
//
// Partial success is not offered on purpose: somebody buying four seats together
// does not want whichever two were still free.
func holdSeats(ctx server_context.BackEndContext, cache *snapshotCache[[]seatView]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, ok := loadEvent(ctx, r, r.PathValue("slug"))
		if !ok || event.Status != "scheduled" {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}

		var body holdSeatsReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.SeatIDs) == 0 {
			http.Error(w, "seat_ids required", http.StatusBadRequest)
			return
		}

		// A seat named twice is one seat. Comparing the request length against a
		// count of matching rows would otherwise read a repeat as a seat that does
		// not belong to the event, and say so.
		seatIDs := uniqueIDs(body.SeatIDs)

		// An event with no per-buyer cap still cannot be drained in one request:
		// without this, MaxPerBuyer=0 means one call can take the whole salon.
		if len(seatIDs) > maxSeatsPerRequest {
			http.Error(w, "too many seats in one request", http.StatusBadRequest)
			return
		}

		// Every requested seat must belong to this event. Without this the seat
		// ids are caller-supplied primary keys, and one event's checkout could
		// reach into another's seat map.
		var n int64
		db(ctx, r).Raw(`SELECT count(*) FROM booking_seats s
			JOIN booking_ticket_tiers t ON t.id = s.tier_id
			WHERE t.event_id = ? AND s.id IN ?`, event.ID, seatIDs).Scan(&n)
		if int(n) != len(seatIDs) {
			http.Error(w, "one or more seats do not belong to this event", http.StatusBadRequest)
			return
		}

		order, holds, err := ticketing.HoldSeatGroup(
			db(ctx, r), event.ID, seatIDs,
			buyer(w, r, body.GuestName, body.GuestEmail),
			cartTTL, time.Now(), event.MaxPerBuyer,
		)
		switch {
		case errors.Is(err, booking.ErrSoldOut):
			http.Error(w, "one or more seats are no longer available", http.StatusConflict)
		case errors.Is(err, ticketing.ErrPurchaseLimit):
			http.Error(w, "per-buyer purchase limit exceeded", http.StatusConflict)
		case errors.Is(err, ticketing.ErrInvalidSeat):
			http.Error(w, "invalid or non-sellable seat", http.StatusBadRequest)
		case errors.Is(err, ticketing.ErrEventClosed):
			http.Error(w, "this showing is no longer on sale", http.StatusConflict)
		case errors.Is(err, ticketing.ErrMixedCurrency):
			http.Error(w, "seats must be priced in one currency", http.StatusBadRequest)
		case err != nil:
			ctx.Logger.Error("hold seats", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
		default:
			cache.Bump(eventKey(event.ID))

			tokens := make([]string, len(holds))
			for i, h := range holds {
				tokens[i] = h.Token
			}
			// The expiry is the server's, not a duration the page re-derives: a
			// countdown that runs out early tells the buyer their seats are back
			// on sale while this process still holds them.
			body := map[string]any{
				"order":       order,
				"hold_tokens": tokens,
				"seat_count":  len(holds),
			}
			if order != nil {
				body["total_cents"] = order.TotalCents
			}
			if len(holds) > 0 {
				body["expires_at"] = holds[0].ExpiresAt
			}
			writeJSON(w, http.StatusCreated, body)
		}
	}
}

type releaseHoldsReq struct {
	HoldTokens []string `json:"hold_tokens"`
}

// releaseHolds gives seats or tickets back before the hold lapses.
//
// Without it the only way out of a hold is waiting ten minutes for the reaper,
// which means a buyer who changes their mind keeps inventory out of circulation
// and burns their own per-buyer allowance in the meantime. The token alone is
// not authorization: it is checked against the caller's owner reference, so a
// leaked token cannot free somebody else's seat.
func releaseHolds(ctx server_context.BackEndContext, cache *snapshotCache[[]seatView]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, ok := loadEvent(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}

		var body releaseHoldsReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.HoldTokens) == 0 {
			http.Error(w, "hold_tokens required", http.StatusBadRequest)
			return
		}

		released, err := ticketing.ReleaseHolds(
			db(ctx, r), event.ID, api_auth.OwnerRef(w, r), body.HoldTokens, time.Now(),
		)
		if err != nil {
			ctx.Logger.Error("release holds", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		if released > 0 {
			cache.Bump(eventKey(event.ID))
		}

		writeJSON(w, http.StatusOK, map[string]any{"released": released})
	}
}

type waitlistReq struct {
	GuestName  string `json:"guest_name"`
	GuestEmail string `json:"guest_email"`
}

// joinWaitlistGA puts the caller in the queue for a sold-out GA tier.
func joinWaitlistGA(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, ok := loadEvent(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		tier, ok := loadGATier(ctx, w, r, event.ID)
		if !ok {
			return
		}

		var body waitlistReq
		_ = json.NewDecoder(r.Body).Decode(&body)

		entry, position, err := ticketing.JoinWaitlistTier(
			db(ctx, r), &tier, buyer(w, r, body.GuestName, body.GuestEmail).OwnerRef,
		)
		if err != nil {
			ctx.Logger.Error("join tier waitlist", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{"entry": entry, "position": position})
	}
}

// joinWaitlistSeat puts the caller in the queue for one specific seat.
func joinWaitlistSeat(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, ok := loadEvent(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		seatID, ok := parseID(r, "seatID")
		if !ok {
			http.Error(w, "invalid seat id", http.StatusBadRequest)
			return
		}

		var n int64
		db(ctx, r).Raw(`SELECT count(*) FROM booking_seats s
			JOIN booking_ticket_tiers t ON t.id = s.tier_id
			WHERE t.event_id = ? AND s.id = ?`, event.ID, seatID).Scan(&n)
		if n != 1 {
			http.Error(w, "seat not found for this event", http.StatusNotFound)
			return
		}

		var body waitlistReq
		_ = json.NewDecoder(r.Body).Decode(&body)

		entry, position, err := ticketing.JoinWaitlistSeat(
			db(ctx, r), seatID, buyer(w, r, body.GuestName, body.GuestEmail).OwnerRef,
		)
		if err != nil {
			ctx.Logger.Error("join seat waitlist", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{"entry": entry, "position": position})
	}
}

type claimReq struct {
	Token      string `json:"token"`
	GuestName  string `json:"guest_name"`
	GuestEmail string `json:"guest_email"`
}

// claimOffer turns a waitlist offer into a pending order, which then captures
// like any other.
//
// The offer is time-boxed. Letting an expired one through would mean the unit it
// was reserving had already been passed to the next person in the queue.
func claimOffer(ctx server_context.BackEndContext, cache *snapshotCache[[]seatView]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, ok := loadEvent(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}

		var body claimReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
			http.Error(w, "token is required", http.StatusBadRequest)
			return
		}

		order, err := ticketing.ClaimOffer(
			db(ctx, r), body.Token,
			buyer(w, r, body.GuestName, body.GuestEmail), time.Now(),
		)
		switch {
		case errors.Is(err, ticketing.ErrOfferExpired):
			http.Error(w, "waitlist offer expired or not found", http.StatusGone)
		case err != nil:
			ctx.Logger.Error("claim waitlist offer", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
		default:
			cache.Bump(eventKey(event.ID))

			writeJSON(w, http.StatusCreated, map[string]any{
				"order": order, "total_cents": order.TotalCents,
			})
		}
	}
}
