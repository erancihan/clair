package routes

import (
	"net/http"

	"github.com/a-h/templ"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	"github.com/erancihan/clair/internal/server/booking"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/utils/router"
	"github.com/erancihan/clair/internal/web"
	pages "github.com/erancihan/clair/internal/web/pages"
)

// mountHost registers the host pages: the HTML that drives the authenticated
// host routes in booking.go. A host creates schedules and events here, shapes a
// schedule's availability with rules and exceptions, and reads its bookings.
//
// The pages sit behind AuthMiddleware, the same guard as the JSON they call. An
// anonymous browser is redirected to /login with a next parameter, and nothing
// links to that: the only way in from a page is the signed-in account menu.
//
// The pages carry no CSRF guard of their own because they are reads. The
// mutating requests they make go to the host JSON routes, which have one, and
// the token comes from the <meta name="csrf-token"> every page shell publishes.
//
// Like the host JSON group, this group takes no prefix and spells out each
// path, so "/host" is an exact pattern rather than the "/host/" subtree.
func mountHost(r *router.Router, api *booking.API, ctx server_context.BackEndContext) {
	r.Group("", func(host *router.Router) {
		host.HandleFunc("GET /host", page("Host", pages.HostHome))
		host.HandleFunc("GET /host/schedules/{slug}", hostSchedulePage(api))
	}, api_auth.AuthMiddleware(ctx))
}

// hostSchedulePage renders the editor for one schedule the caller owns, or
// 404s. A schedule somebody else owns is "not found" here as it is on the JSON
// route, so the page cannot be used to probe which slugs exist.
func hostSchedulePage(api *booking.API) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		slug := req.PathValue("slug")
		if !api.HostsSchedule(req, slug) {
			http.NotFound(w, req)
			return
		}
		templ.Handler(web.Base(api_auth.PageShell(w, req, "Host · "+slug), pages.HostSchedule(slug))).ServeHTTP(w, req)
	}
}
