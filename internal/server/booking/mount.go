package booking

import (
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/utils/router"
)

// Mount registers every route the booking domain owns onto r, following the
// convention internal/server/games/mount.go documents.
//
// The domain owns two path spaces because it serves two vocabularies over one
// reservation kernel: /appointments for schedules and office hours, /events for
// tickets and seats. They share the kernel, the orders and the payment webhook,
// and differ only in what a reserved unit is called.
//
// Routes fall into four classes, and which class a route is in is the security
// decision this file exists to make explicit:
//
//   - Public reads. Availability, an event, a seat map. No identity needed, and
//     none is required, because these are the pages a link leads to.
//
//   - Guest-allowed writes. Booking, holding, cancelling with a token, joining a
//     waitlist. These deliberately do NOT sit behind AuthMiddleware: making an
//     appointment is exactly the kind of thing a first-time visitor does, and
//     forcing an account first is the friction this system exists to remove.
//     Ownership follows api_auth.OwnerRef, which yields the caller's account when
//     they have one and a stable per-browser guest reference when they do not.
//
//   - Authenticated. Managing a schedule you own, listing what you booked. These
//     are about a specific account's rows, so there has to be an account.
//
//   - Machine. The payment webhook, authenticated by a signature over its body
//     rather than by a session. It is outside every group below, since a session
//     and a CSRF token are precisely what a provider does not have.
//
// Every mutating browser route is behind api_auth.CSRF(), mounted here on the
// domain's own groups rather than globally — login and registration precede the
// session a token is bound to, so a global mount would break them. The token is
// published to the browser by the page shell as <meta name="csrf-token">.
func Mount(r *router.Router, ctx server_context.BackEndContext) {
	// A visitor who holds a seat as a guest and then signs in to pay keeps that
	// hold. Registering here, from the domain, is what lets the authentication
	// layer run this without importing the booking domain.
	api_auth.RegisterGuestMigrator("booking", migrateGuest)

	mountAppointments(r, ctx)
	mountEvents(r, ctx)

	// The payment webhook is mounted at the top level, outside both domains: one
	// order can hold appointment slots or event seats, and the provider signing
	// the delivery has no idea which.
	r.Group("webhooks/payments", func(hooks *router.Router) {
		hooks.HandleFunc("POST /{provider}", paymentWebhook(ctx))
	})
}

// mountAppointments registers the /appointments path space: the office-hours
// vocabulary over the reservation kernel.
func mountAppointments(r *router.Router, ctx server_context.BackEndContext) {
	r.Group("appointments", func(appointments *router.Router) {
		// ---- public reads ----
		appointments.HandleFunc("GET /schedules/{slug}/availability", availability(ctx))

		// ---- guest-allowed writes ----
		appointments.Group("", func(guest *router.Router) {
			guest.HandleFunc("POST /schedules/{slug}/book", book(ctx))
			guest.HandleFunc("POST /schedules/{slug}/hold", holdSlot(ctx))

			// The cancel token is the authorization here: a guest booking has no
			// account behind it, and the token is the only thing proving the
			// caller is who booked.
			guest.HandleFunc("POST /cancel", cancelByToken(ctx))
		}, api_auth.CSRF())

		// ---- authenticated: the booker's own appointments ----
		appointments.Group("", func(mine *router.Router) {
			mine.HandleFunc("GET /me", myAppointments(ctx))
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
			host.HandleFunc("POST /schedules", createSchedule(ctx))
			host.HandleFunc("GET /schedules", listSchedules(ctx))

			host.HandleFunc("GET /schedules/{slug}", getSchedule(ctx))
			host.HandleFunc("GET /schedules/{slug}/bookings", listBookings(ctx))

			host.HandleFunc("POST /schedules/{slug}/rules", addRule(ctx))
			host.HandleFunc("DELETE /schedules/{slug}/rules/{id}", deleteRule(ctx))

			host.HandleFunc("POST /schedules/{slug}/exceptions", addException(ctx))
			host.HandleFunc("DELETE /schedules/{slug}/exceptions/{id}", deleteException(ctx))
		}, api_auth.AuthMiddleware(ctx), api_auth.CSRF())
	})
}

// mountEvents registers the /events path space: the ticketing vocabulary over the
// same kernel.
func mountEvents(r *router.Router, ctx server_context.BackEndContext) {
	// ---- authenticated: running an event ----
	//
	// Creating an event is the collection endpoint, so it is registered from the
	// top-level router to land on "/events" exactly. Registering "/" inside the
	// "events" group below would instead produce the subtree pattern "/events/",
	// which answers every unmatched POST under it.
	r.Group("", func(owner *router.Router) {
		owner.HandleFunc("POST /events", createEvent(ctx))
	}, api_auth.AuthMiddleware(ctx), api_auth.CSRF())

	r.Group("events", func(events *router.Router) {
		// ---- public reads ----
		events.HandleFunc("GET /{slug}", getEvent(ctx))
		events.HandleFunc("GET /{slug}/seats", seatMap(ctx))

		// ---- guest-allowed writes ----
		events.Group("", func(guest *router.Router) {
			guest.HandleFunc("POST /{slug}/tiers/{tierID}/hold", holdGA(ctx))
			guest.HandleFunc("POST /{slug}/seats/hold", holdSeats(ctx))

			guest.HandleFunc("POST /{slug}/tiers/{tierID}/waitlist", joinWaitlistGA(ctx))
			guest.HandleFunc("POST /{slug}/seats/{seatID}/waitlist", joinWaitlistSeat(ctx))
			guest.HandleFunc("POST /{slug}/waitlist/claim", claimOffer(ctx))
		}, api_auth.CSRF())
	})
}
