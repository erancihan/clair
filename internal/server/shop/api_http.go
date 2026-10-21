package shop

import (
	"net/http"

	"github.com/a-h/templ"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	"github.com/erancihan/clair/internal/web"
	"github.com/erancihan/clair/internal/web/pages"
)

// page renders body inside the site shell with a 200.
func page(w http.ResponseWriter, r *http.Request, title string, body templ.Component) {
	render(w, r, http.StatusOK, title, body)
}

// notFound answers a real 404 with the site's not-found page.
func notFound(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusNotFound, "Not found", pages.NotFound())
}

// render renders body inside the site shell with an explicit status.
//
// PageShell mints and persists the CSRF token and resolves the user menu. It
// writes to w (it may set a cookie), so it runs before anything else touches
// the response. The status goes through templ.WithStatus rather than a bare
// w.WriteHeader: templ.Handler sets Content-Type only as it starts writing,
// and a header written earlier would be discarded.
func render(w http.ResponseWriter, r *http.Request, status int, title string, body templ.Component) {
	shell := api_auth.PageShell(w, r, title)
	templ.Handler(web.Base(shell, body), templ.WithStatus(status)).ServeHTTP(w, r)
}
