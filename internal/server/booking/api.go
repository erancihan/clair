package booking

import (
	"context"
	"net/http"
	"time"

	appt "github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/database/models"

	"github.com/erancihan/clair/internal/booking/live"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/utils"
)

// API is the booking domain's HTTP surface: one handler per thing the domain
// can be asked to do, and the shared state those handlers need.
//
// It exists so the route table can live outside this package. Where a route is
// mounted, and what middleware guards it, is a decision about the server; what a
// handler does is a decision about the domain. Keeping the two apart means the
// registry reads as a list of paths and guards rather than as domain code.
type API struct {
	ctx          server_context.BackEndContext
	hub          *live.Hub
	seats        *snapshotCache[[]seatView]
	summary      *snapshotCache[[]eventSummary]
	availability *snapshotCache[[]appt.OpenSlot]
}

// NewAPI builds the domain's handlers along with the two pieces of shared state
// they need: the notification listener behind the seat stream, and the rendered
// seat map every reader of an event shares.
func NewAPI(bctx server_context.BackEndContext) *API {
	// A visitor who holds a seat as a guest and then signs in to pay keeps that
	// hold. Registering here, from the domain, is what lets the authentication
	// layer run this without importing the booking domain.
	api_auth.RegisterGuestMigrator("booking", migrateGuest)

	// One listener per process, shared by every seat stream. It connects only
	// while somebody is watching.
	hub := live.NewHub(utils.GetEnv("DATABASE_URL", ""), bctx.Logger)

	// One rendered seat map per event per change, shared by every reader of it.
	//
	// The loaders deliberately use the long-lived connection rather than a
	// request-scoped one: the snapshot belongs to everybody watching, and one
	// client hanging up must not cancel the read the others are waiting on. The
	// context they get is the cache's own deadline.
	seats := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		return seatViews(bctx.DBConn.WithContext(ctx), eventKeyID(key))
	})

	// One summary per showing, shared by every board that lists it.
	summary := newSnapshotCache(func(ctx context.Context, key string) ([]eventSummary, error) {
		return summaryFor(bctx.DBConn.WithContext(ctx), eventKeyID(key))
	})

	// One computed availability per schedule per change. Availability is derived
	// from rules rather than stored, so it is the most expensive of the three to
	// produce and the most worth sharing.
	availability := newSnapshotCache(func(ctx context.Context, key string) ([]appt.OpenSlot, error) {
		conn := bctx.DBConn.WithContext(ctx)
		var sched models.BookingSchedule
		res := conn.Limit(1).Find(&sched, scheduleKeyID(key))
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, nil
		}
		return availabilityViews(conn, sched, time.Now())
	})

	return &API{ctx: bctx, hub: hub, seats: seats, summary: summary, availability: availability}
}

// Close stops the notification listener behind the streams. The server calls it
// on shutdown; nothing will signal a stream after it returns.
func (a *API) Close() { a.hub.Close() }

// ---- appointments: the office-hours vocabulary over the booking service ----

func (a *API) Availability() http.HandlerFunc {
	return availability(a.ctx, a.hub, a.availability)
}
func (a *API) AvailabilityStream() http.HandlerFunc {
	return availabilityStream(a.ctx, a.hub, a.availability)
}
func (a *API) Book() http.HandlerFunc          { return book(a.ctx, a.availability) }
func (a *API) HoldSlot() http.HandlerFunc      { return holdSlot(a.ctx) }
func (a *API) CancelByToken() http.HandlerFunc { return cancelByToken(a.ctx, a.availability) }
func (a *API) MyAppointments() http.HandlerFunc {
	return myAppointments(a.ctx)
}

// ---- appointments: running a schedule you host ----

func (a *API) CreateSchedule() http.HandlerFunc  { return createSchedule(a.ctx) }
func (a *API) ListSchedules() http.HandlerFunc   { return listSchedules(a.ctx) }
func (a *API) GetSchedule() http.HandlerFunc     { return getSchedule(a.ctx) }
func (a *API) ListBookings() http.HandlerFunc    { return listBookings(a.ctx) }
func (a *API) AddRule() http.HandlerFunc         { return addRule(a.ctx) }
func (a *API) DeleteRule() http.HandlerFunc      { return deleteRule(a.ctx) }
func (a *API) AddException() http.HandlerFunc    { return addException(a.ctx) }
func (a *API) DeleteException() http.HandlerFunc { return deleteException(a.ctx) }

// HostsSchedule reports whether the authenticated caller owns the schedule
// with this slug. It is for the host pages, which answer 404 for a schedule the
// caller does not own exactly as the JSON routes do, so a guessed slug learns
// nothing from either. It is not a route.
func (a *API) HostsSchedule(r *http.Request, slug string) bool {
	_, ok := loadOwnedSchedule(a.ctx, r, slug)
	return ok
}

// ---- ticketing: the events vocabulary over the same service ----

func (a *API) CreateEvent() http.HandlerFunc { return createEvent(a.ctx) }
func (a *API) EventSummary() http.HandlerFunc {
	return eventSummaries(a.ctx, a.hub, a.summary)
}
func (a *API) EventSummaryStream() http.HandlerFunc {
	return eventSummaryStream(a.ctx, a.hub, a.summary)
}
func (a *API) GetEvent() http.HandlerFunc   { return getEvent(a.ctx) }
func (a *API) SeatMap() http.HandlerFunc    { return seatMap(a.ctx, a.hub, a.seats) }
func (a *API) SeatStream() http.HandlerFunc { return seatStream(a.ctx, a.hub, a.seats) }

func (a *API) HoldGA() http.HandlerFunc       { return holdGA(a.ctx, a.seats) }
func (a *API) HoldSeats() http.HandlerFunc    { return holdSeats(a.ctx, a.seats) }
func (a *API) ReleaseHolds() http.HandlerFunc { return releaseHolds(a.ctx, a.seats) }
func (a *API) JoinWaitlistGA() http.HandlerFunc {
	return joinWaitlistGA(a.ctx)
}
func (a *API) JoinWaitlistSeat() http.HandlerFunc { return joinWaitlistSeat(a.ctx) }
func (a *API) ClaimOffer() http.HandlerFunc       { return claimOffer(a.ctx, a.seats) }
func (a *API) Checkout() http.HandlerFunc         { return checkout(a.ctx, a.seats) }
func (a *API) MyOrders() http.HandlerFunc         { return myOrders(a.ctx) }
func (a *API) CancelOrder() http.HandlerFunc      { return cancelOrder(a.ctx, a.seats) }

// ---- machine: the payment provider ----

func (a *API) PaymentWebhook() http.HandlerFunc { return paymentWebhook(a.ctx, a.seats) }
