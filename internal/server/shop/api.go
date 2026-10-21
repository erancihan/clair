// Package shop is the HTTP layer for the shop domain: the storefront, the cart,
// checkout and the admin CMS, over the domain logic in internal/shop.
//
// Handlers here stay thin. They decode a request, resolve who is asking, call
// into the domain package and render the answer; every rule about money, stock
// and order state lives below them, where it can be tested without a server.
package shop

import (
	"net/http"

	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/web/pages"
)

// API is the shop domain's HTTP surface: one handler per thing the domain can
// be asked to do, and the shared state those handlers need.
//
// It exists so the route table can live outside this package
// (internal/server/routes/shop.go). Where a route is mounted, and what
// middleware guards it, is a decision about the server; what a handler does is
// a decision about the domain.
type API struct {
	ctx server_context.BackEndContext
}

// NewAPI builds the domain's handlers.
func NewAPI(ctx server_context.BackEndContext) *API {
	return &API{ctx: ctx}
}

// ---- storefront ----

// Landing renders the storefront's front page.
func (a *API) Landing() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page(w, r, "Shop", pages.ShopLanding())
	}
}

// NotFound is the subtree fallback the route table mounts under /shop/ (and
// later /shop/admin/): a real 404 for any path or verb nothing else claimed.
//
// Without it those requests fall through to the site's method-less "/"
// catch-all, which renders the not-found page with a 200.
func (a *API) NotFound() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		notFound(w, r)
	}
}
