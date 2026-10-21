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

	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/server/shop"
	"github.com/erancihan/clair/internal/utils/router"
)

// MountShop registers every route the shop domain owns.
//
// Three net/http ServeMux facts shape this table (docs/shop-plan.md §4.3):
//
//   - The site has a method-less "/" catch-all that renders the not-found
//     page with a 200. Anything the shop does not register — an unknown
//     /shop/* path, a wrong verb on a registered path — would fall through to
//     it, and ServeMux never answers 405 while that pattern exists. So the
//     shop mounts its own method-less subtree fallback, "/shop/", answering a
//     real 404.
//
//   - A pattern ending in "/" matches a whole subtree. That is what the
//     fallback relies on, and why the landing must not be registered as
//     "GET /" inside the group: it would be "/shop/" and shadow the fallback
//     with a 200. The landing is registered exactly: "GET /shop" from the
//     top-level router, and "GET /shop/{$}" as a redirect. The canonical URL
//     has no trailing slash because that is the form that can be registered
//     exactly.
//
//   - router.HandleFunc splits on the first space, so patterns carry exactly
//     one space between method and path.
func MountShop(r *router.Router, ctx server_context.BackEndContext) {
	api := shop.NewAPI(ctx)

	// ---- landing: exact patterns ----
	r.HandleFunc("GET /shop", api.Landing())
	r.HandleFunc("GET /shop/{$}", redirectTo("/shop"))

	r.Group("shop", func(store *router.Router) {
		// Subtree fallback: a real 404 for any path or verb under /shop/ that
		// nothing below claims. Method-less on purpose.
		store.HandleFunc("/", api.NotFound())
	})
}

// redirectTo answers every request with a permanent redirect to path. It is
// how the trailing-slash form of an exactly-registered page finds its way to
// the canonical one.
func redirectTo(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, path, http.StatusMovedPermanently)
	}
}
