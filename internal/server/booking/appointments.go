package booking

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	appt "github.com/erancihan/clair/internal/appointments"
	kernel "github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"go.uber.org/zap"
)

type bookReq struct {
	StartAt    time.Time `json:"start_at"`
	GuestName  string    `json:"guest_name"`
	GuestEmail string    `json:"guest_email"`
}

// book reserves one place in a slot of a free schedule.
//
// A paid schedule is refused here rather than quietly booked for nothing: the
// two flows differ in whether a place is committed or only held, and answering
// the wrong one would hand out free appointments.
func book(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadSchedule(ctx, r, r.PathValue("slug"))
		if !ok || !sched.Active {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}
		if sched.RequiresPayment {
			http.Error(w, "this schedule requires payment; use /hold then checkout", http.StatusBadRequest)
			return
		}

		var body bookReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request payload", http.StatusBadRequest)
			return
		}

		in := appt.BookInput{
			Schedule: &sched, StartAt: body.StartAt,
			GuestName: body.GuestName, GuestEmail: body.GuestEmail,
		}
		// A signed-in booker gets the appointment linked to their account, so it
		// shows up under /appointments/me. A guest gets only the cancel token.
		if identity, ok := api_auth.CurrentUser(r.Context()); ok {
			uid := identity.UserID
			in.UserID = &uid
		}

		appointment, err := appt.Book(db(ctx, r), in, time.Now())
		switch {
		case errors.Is(err, appt.ErrPast):
			http.Error(w, "cannot book a slot in the past", http.StatusBadRequest)
		case errors.Is(err, appt.ErrNotOpen):
			http.Error(w, "requested time is not an open slot", http.StatusConflict)
		case errors.Is(err, kernel.ErrSoldOut):
			http.Error(w, "slot is full", http.StatusConflict)
		case err != nil:
			ctx.Logger.Error("book appointment", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
		default:
			writeJSON(w, http.StatusCreated, appointment)
		}
	}
}

// holdSlot reserves a slot of a paid schedule for the length of checkout and
// opens a pending order against it.
//
// Nothing is confirmed here. The place is held, not booked, and becomes an
// appointment only when the payment webhook captures the order — which is what
// stops an abandoned checkout from occupying a slot forever.
func holdSlot(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadSchedule(ctx, r, r.PathValue("slug"))
		if !ok || !sched.Active {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}
		if !sched.RequiresPayment {
			http.Error(w, "this schedule is free; use /book", http.StatusBadRequest)
			return
		}

		var body bookReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request payload", http.StatusBadRequest)
			return
		}

		in := appt.HoldInput{
			Schedule: &sched, StartAt: body.StartAt,
			OwnerRef:  api_auth.OwnerRef(w, r),
			GuestName: body.GuestName, GuestEmail: body.GuestEmail,
		}
		if identity, ok := api_auth.CurrentUser(r.Context()); ok {
			uid := identity.UserID
			in.UserID = &uid
		}

		order, hold, err := appt.HoldSlot(db(ctx, r), in, time.Now())
		switch {
		case errors.Is(err, appt.ErrPast):
			http.Error(w, "cannot hold a slot in the past", http.StatusBadRequest)
		case errors.Is(err, appt.ErrNotOpen):
			http.Error(w, "requested time is not an open slot", http.StatusConflict)
		case errors.Is(err, kernel.ErrSoldOut):
			http.Error(w, "slot is no longer available", http.StatusConflict)
		case err != nil:
			ctx.Logger.Error("hold slot", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
		default:
			writeJSON(w, http.StatusCreated, map[string]any{
				"order":       order,
				"hold_token":  hold.Token,
				"expires_at":  hold.ExpiresAt,
				"total_cents": order.TotalCents,
				"currency":    order.Currency,
			})
		}
	}
}

// cancelByToken cancels an appointment using the opaque token issued when it was
// booked.
//
// The token is the authorization: a guest booking has no account behind it, and
// the token is the only thing that proves the caller is the person who booked.
// It is deliberately idempotent, because the link lands in an email and gets
// clicked more than once.
func cancelByToken(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "token is required", http.StatusBadRequest)
			return
		}

		if err := appt.CancelByToken(db(ctx, r), token); err != nil {
			ctx.Logger.Error("cancel appointment", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

// myAppointments returns the appointments the authenticated caller booked.
func myAppointments(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := api_auth.CurrentUser(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var appointments []models.BookingAppointment
		db(ctx, r).Where("user_id = ?", identity.UserID).Order("start_at DESC").Find(&appointments)

		writeJSON(w, http.StatusOK, appointments)
	}
}
