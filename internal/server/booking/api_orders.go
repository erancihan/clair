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

type checkoutReq struct {
	GuestName  string `json:"guest_name"`
	GuestEmail string `json:"guest_email"`
}

// checkout pays for every hold the caller has on a showing with the mock
// provider and issues the tickets.
//
// There is no real payment here, so there is nothing for a provider to call
// back about: the charge is written in-process and goes through the same
// capture the webhook uses.
func checkout(ctx server_context.BackEndContext, cache *snapshotCache[[]seatView]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, ok := loadEvent(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}

		var body checkoutReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request payload", http.StatusBadRequest)
			return
		}

		owner := buyer(w, r, body.GuestName, body.GuestEmail)
		paid, err := ticketing.Checkout(db(ctx, r), event.ID, owner, time.Now())

		// Inventory moved in every outcome but the refusals up front.
		defer cache.Bump(eventKey(event.ID))

		switch {
		case errors.Is(err, ticketing.ErrBuyerName):
			http.Error(w, "a name is required for the tickets", http.StatusBadRequest)
			return
		case errors.Is(err, ticketing.ErrNothingToPay):
			http.Error(w, "you hold nothing on this showing", http.StatusConflict)
			return
		case errors.Is(err, ticketing.ErrEventClosed):
			http.Error(w, "this showing is no longer on sale", http.StatusConflict)
			return
		case errors.Is(err, ticketing.ErrHoldLapsed) && len(paid) == 0:
			http.Error(w, "your hold ran out before payment; you were not charged", http.StatusConflict)
			return
		case err != nil && !errors.Is(err, ticketing.ErrHoldLapsed):
			ctx.Logger.Error("checkout", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		views, err := ticketing.OrdersFor(db(ctx, r), owner.OwnerRef)
		if err != nil {
			ctx.Logger.Error("list orders after checkout", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		ids := make(map[uint]bool, len(paid))
		for _, o := range paid {
			ids[o.ID] = true
		}
		orders := make([]ticketing.OrderView, 0, len(paid))
		for _, v := range views {
			if ids[v.ID] {
				orders = append(orders, v)
			}
		}

		// Part of the cart lapsed while the rest was paid for. Say so, with what
		// was issued, rather than reporting either a success or a failure.
		writeJSON(w, http.StatusCreated, map[string]any{
			"orders":  orders,
			"partial": errors.Is(err, ticketing.ErrHoldLapsed),
		})
	}
}

// myOrders lists the caller's paid and refunded ticket orders.
//
// It is a guest read: a guest's tickets belong to their browser's owner
// reference, the same one their holds were placed under.
func myOrders(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		views, err := ticketing.OrdersFor(db(ctx, r), api_auth.OwnerRef(w, r))
		if err != nil {
			ctx.Logger.Error("list orders", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, views)
	}
}

// cancelOrder cancels one of the caller's ticket orders and refunds it.
//
// The order id is caller-supplied, so ownership is part of the lookup: an id
// that is not the caller's answers the same 404 as one that does not exist.
func cancelOrder(ctx server_context.BackEndContext, cache *snapshotCache[[]seatView]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(r, "id")
		if !ok {
			http.Error(w, "invalid order id", http.StatusBadRequest)
			return
		}

		conn := db(ctx, r)
		err := ticketing.CancelOrder(conn, id, api_auth.OwnerRef(w, r), time.Now())
		switch {
		case errors.Is(err, ticketing.ErrOrderNotFound):
			http.Error(w, "order not found", http.StatusNotFound)
			return
		case errors.Is(err, ticketing.ErrTooLate):
			http.Error(w, "the showing has started; tickets can no longer be cancelled", http.StatusConflict)
			return
		case errors.Is(err, ticketing.ErrTicketUsed):
			http.Error(w, "a ticket on this order has been used", http.StatusConflict)
			return
		case err != nil:
			ctx.Logger.Error("cancel order", zap.Uint("order_id", id), zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		for _, eventID := range eventIDsForOrder(conn, id) {
			cache.Bump(eventKey(eventID))
		}
		var order models.BookingOrder
		if err := conn.Select("status").First(&order, id).Error; err != nil {
			ctx.Logger.Error("read cancelled order", zap.Uint("order_id", id), zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"order_id": id, "status": order.Status})
	}
}

// isRefundedCapture reports the capture outcome the webhook answers 200 for
// without having issued anything.
func isRefundedCapture(err error) bool { return errors.Is(err, booking.ErrCaptureRefunded) }
