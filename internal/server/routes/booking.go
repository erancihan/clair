// Package routes owns the server's route table: which paths exist, and what
// guards each one.
//
// Domains supply handlers; this package decides where they are reachable and
// what has to be true of a caller to reach them. Keeping that decision in one
// place is the point — a security question answered inside a domain package is a
// security question nobody reviews.
package routes

import (
	"net/http"
	"os"
	"strconv"

	"github.com/a-h/templ"
	"github.com/erancihan/clair/internal/aurenor"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	"github.com/erancihan/clair/internal/server/booking"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/utils/middleware"
	"github.com/erancihan/clair/internal/utils/router"
	"github.com/erancihan/clair/internal/web"
	pages "github.com/erancihan/clair/internal/web/pages"
)

// MountBooking registers every route the booking domain owns.
//
// The domain owns two path spaces because it serves two vocabularies over one
// booking service: /appointments for schedules and office hours, /events for
// tickets and seats. They share the booking service, the orders and the payment webhook,
// and differ only in what a reserved unit is called.
//
// Routes fall into four classes, and which class a route is in is the security
// decision this file exists to make explicit:
//
//   - Public reads. Availability, an event, a seat map. No identity needed, and
//     none is required, because these are the pages a link leads to.
//
//   - Guest-allowed writes. Booking, holding, releasing, cancelling with a
//     token, joining a waitlist. These deliberately do NOT sit behind
//     AuthMiddleware: making an appointment is exactly the kind of thing a
//     first-time visitor does, and forcing an account first is the friction this
//     system exists to remove. Ownership follows api_auth.OwnerRef, which yields
//     the caller's account when they have one and a stable per-browser guest
//     reference when they do not.
//
//   - Authenticated. Managing a schedule you own, listing what you booked. These
//     are about a specific account's rows, so there has to be an account.
//
//   - Machine. The payment webhook, authenticated by a signature over its body
//     rather than by a session. It is outside every group below, since a session
//     and a CSRF token are precisely what a provider does not have.
//
// Every mutating browser route is behind api_auth.CSRF(), mounted on the
// domain's own groups rather than globally — login and registration precede the
// session a token is bound to, so a global mount would break them. The token is
// published to the browser by the page shell as <meta name="csrf-token">.
//
// It returns the closer for the domain's long-lived state (the notification
// listener behind the streams); the server calls it on shutdown.
func MountBooking(r *router.Router, ctx server_context.BackEndContext) func() {
	api := booking.NewAPI(ctx)

	// Guest writes have no account to cap and streams hold a connection open, so
	// both are limited per client address. Read once, at mount.
	limits := bookingLimits{
		guest:   middleware.PerIP(envFloat("RATE_LIMIT_GUEST_RPS", 5), envInt("RATE_LIMIT_GUEST_BURST", 20)),
		streams: middleware.MaxConcurrentPerIP(envInt("RATE_LIMIT_STREAMS_PER_IP", 12)),
	}

	mountAppointments(r, api, ctx, limits)
	mountEvents(r, api, ctx, limits)
	mountAurenor(r)

	// The payment webhook is mounted at the top level, outside both path spaces:
	// one order can hold appointment slots or event seats, and the provider
	// signing the delivery has no idea which.
	r.Group("webhooks/payments", func(hooks *router.Router) {
		hooks.HandleFunc("POST /{provider}", api.PaymentWebhook())
	})

	return api.Close
}

// bookingLimits are the per-address limits the booking routes share. One
// instance per mount, so the guest budget covers every guest write together:
// a caller cannot spread a loop across holds and bookings to get more of it.
type bookingLimits struct {
	guest   router.Middleware
	streams router.Middleware
}

// mountAppointments registers the /appointments path space: the office-hours
// vocabulary over the booking service.
func mountAppointments(r *router.Router, api *booking.API, ctx server_context.BackEndContext, limits bookingLimits) {
	r.Group("appointments", func(appointments *router.Router) {
		// ---- public reads ----
		appointments.HandleFunc("GET /schedules/{slug}/availability", api.Availability())

		// The same availability, pushed as it changes.
		appointments.Middleware(limits.streams).HandleFunc("GET /schedules/{slug}/availability/stream", api.AvailabilityStream())

		// ---- guest-allowed writes ----
		appointments.Group("", func(guest *router.Router) {
			guest.HandleFunc("POST /schedules/{slug}/book", api.Book())
			guest.HandleFunc("POST /schedules/{slug}/hold", api.HoldSlot())

			// The cancel token is the authorization here: a guest booking has no
			// account behind it, and the token is the only thing proving the
			// caller is who booked.
			guest.HandleFunc("POST /cancel", api.CancelByToken())
		}, limits.guest, api_auth.CSRF())

		// ---- authenticated: the booker's own appointments ----
		appointments.Group("", func(mine *router.Router) {
			mine.HandleFunc("GET /me", api.MyAppointments())
		}, api_auth.AuthMiddleware(ctx))

		// ---- authenticated: managing schedules you host ----
		//
		// CSRF sits alongside AuthMiddleware rather than instead of it. A session
		// cookie is sent on cross-site requests too, so authentication alone does
		// not stop another origin from posting here as the signed-in host.
		//
		// The group takes no prefix of its own and every route spells out
		// "/schedules/...". Nesting a "schedules" group and registering "/" in it
		// would produce the pattern "/appointments/schedules/", and a pattern
		// ending in a slash matches a whole subtree: the bare collection path
		// would answer with a 301, and any unmatched POST below it — a typo, a
		// probe — would reach the create handler instead of a 404.
		appointments.Group("", func(host *router.Router) {
			host.HandleFunc("POST /schedules", api.CreateSchedule())
			host.HandleFunc("GET /schedules", api.ListSchedules())

			host.HandleFunc("GET /schedules/{slug}", api.GetSchedule())
			host.HandleFunc("GET /schedules/{slug}/bookings", api.ListBookings())

			host.HandleFunc("POST /schedules/{slug}/rules", api.AddRule())
			host.HandleFunc("DELETE /schedules/{slug}/rules/{id}", api.DeleteRule())

			host.HandleFunc("POST /schedules/{slug}/exceptions", api.AddException())
			host.HandleFunc("DELETE /schedules/{slug}/exceptions/{id}", api.DeleteException())
		}, api_auth.AuthMiddleware(ctx), api_auth.CSRF())
	})
}

// mountEvents registers the /events path space: the ticketing vocabulary over
// the same booking service.
func mountEvents(r *router.Router, api *booking.API, ctx server_context.BackEndContext, limits bookingLimits) {
	// ---- authenticated: running an event ----
	//
	// Creating an event is the collection endpoint, so it is registered from the
	// top-level router to land on "/events" exactly. Registering "/" inside the
	// "events" group below would instead produce the subtree pattern "/events/",
	// which answers every unmatched POST under it.
	r.Group("", func(owner *router.Router) {
		owner.HandleFunc("POST /events", api.CreateEvent())
	}, api_auth.AuthMiddleware(ctx), api_auth.CSRF())

	r.Group("events", func(events *router.Router) {
		// ---- public reads ----
		//
		// The literal path is registered before the wildcard so a board asking for
		// "summary" is not read as a showing called "summary".
		//
		// The whole board in one request: a page that lists thirteen showings
		// should ask once, not thirteen times.
		events.HandleFunc("GET /summary", api.EventSummary())

		// The same board, pushed as its showings change.
		events.Middleware(limits.streams).HandleFunc("GET /summary/stream", api.EventSummaryStream())

		events.HandleFunc("GET /{slug}", api.GetEvent())
		events.HandleFunc("GET /{slug}/seats", api.SeatMap())

		// The same seat map, pushed as it changes.
		events.Middleware(limits.streams).HandleFunc("GET /{slug}/seats/stream", api.SeatStream())

		// ---- guest-allowed writes ----
		events.Group("", func(guest *router.Router) {
			guest.HandleFunc("POST /{slug}/tiers/{tierID}/hold", api.HoldGA())
			guest.HandleFunc("POST /{slug}/seats/hold", api.HoldSeats())

			// Giving a hold back is the counterpart of cancelling an appointment,
			// and belongs to the same guest-allowed class: the person who placed it
			// usually has no account.
			guest.HandleFunc("POST /{slug}/holds/release", api.ReleaseHolds())

			guest.HandleFunc("POST /{slug}/tiers/{tierID}/waitlist", api.JoinWaitlistGA())
			guest.HandleFunc("POST /{slug}/seats/{seatID}/waitlist", api.JoinWaitlistSeat())
			guest.HandleFunc("POST /{slug}/waitlist/claim", api.ClaimOffer())
		}, limits.guest, api_auth.CSRF())
	})
}

// mountAurenor registers the booking simulations: static portfolio pages under
// /aurenor, the made-up country the demos are themed around, reached from the
// Projects page.
//
// Institutions are collections — /aurenor/theaters and /aurenor/hospitals list
// instances and each {slug} is one instance — while the single-body consulate
// and civic bureau are flat pages. /aurenor itself is reserved as the country
// home for a future map. These are HTML pages, distinct from the JSON API above.
func mountAurenor(r *router.Router) {
	r.Group("aurenor", func(nation *router.Router) {
		nation.HandleFunc("GET /theaters", page("Aurenor · theaters", pages.AurenorTheaters))
		nation.HandleFunc("GET /theaters/{slug}", venuePage(pages.FindTheater, pages.AurenorTheater))

		// Seat selection is a screen of its own, so it gets a URL of its own: the
		// back button returns to the showtimes, and a chosen showing can be linked.
		nation.HandleFunc("GET /theaters/{slug}/showings/{event}", showingPage())

		nation.HandleFunc("GET /hospitals", page("Aurenor · hospitals", pages.AurenorHospitals))
		nation.HandleFunc("GET /hospitals/{slug}", venuePage(pages.FindHospital, pages.AurenorHospital))

		// A screen per bookable service, for the same reason seat selection has
		// one: it is the only way a screen can subscribe to exactly what it shows.
		nation.HandleFunc("GET /hospitals/{slug}/{service}", servicePage("hospitals"))

		nation.HandleFunc("GET /consulate", page("Aurenor · consulate", pages.AurenorConsulate))
		nation.HandleFunc("GET /consulate/{service}", servicePage("consulate"))

		nation.HandleFunc("GET /bureau", page("Aurenor · civic bureau", pages.AurenorBureau))
		nation.HandleFunc("GET /bureau/{service}", servicePage("bureau"))
	})
}

func page(title string, body func() templ.Component) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		templ.Handler(web.Base(api_auth.PageShell(w, req, title), body())).ServeHTTP(w, req)
	}
}

// servicePage renders one bookable service's screen, or 404s when the
// institution does not offer it.
func servicePage(institution string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		venue := ""
		if institution == "hospitals" {
			venue = req.PathValue("slug")
		}

		card, ok := pages.FindService(institution, venue, req.PathValue("service"))
		if !ok {
			http.NotFound(w, req)
			return
		}

		templ.Handler(web.Base(api_auth.PageShell(w, req, "Aurenor · "+card.Title), pages.AurenorService(card))).ServeHTTP(w, req)
	}
}

// venuePage resolves a {slug} to a Venue and renders its detail page, or 404s
// when the slug names no known instance of that collection.
func venuePage(find func(string) (pages.Venue, bool), body func(pages.Venue) templ.Component) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, ok := find(req.PathValue("slug"))
		if !ok {
			http.NotFound(w, req)
			return
		}
		templ.Handler(web.Base(api_auth.PageShell(w, req, "Aurenor · "+v.Name), body(v))).ServeHTTP(w, req)
	}
}

// showingPage renders the seat-selection screen for one showing of one theater,
// or 404s when either the theater or the showing is not in the catalog.
func showingPage() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		venue, ok := pages.FindTheater(req.PathValue("slug"))
		if !ok {
			http.NotFound(w, req)
			return
		}
		showing, ok := aurenor.FindShowing(venue.Slug, req.PathValue("event"))
		if !ok {
			http.NotFound(w, req)
			return
		}

		title := "Aurenor · " + showing.Film
		templ.Handler(web.Base(api_auth.PageShell(w, req, title), pages.AurenorShowing(venue, showing))).ServeHTTP(w, req)
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func envFloat(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil && v > 0 {
		return v
	}
	return def
}
