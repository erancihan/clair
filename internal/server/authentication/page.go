package authentication

import (
	"net/http"

	"github.com/erancihan/clair/internal/web"
)

// PageShell assembles the per-request state that web.Base renders around a page
// body: the CSRF token browser JS needs, and the signed-in visitor the header
// renders a user menu for.
//
// Every HTML page in the application should build its shell through this helper,
// so the two are always populated the same way:
//
//	templ.Handler(web.Base(api_auth.PageShell(w, r, "Games"), pages.Games())).ServeHTTP(w, r)
//
// It lives in this package, not in internal/web, because it is the only side of
// the seam that knows about sessions and identities. internal/web stays free of
// any dependency on the authentication layer, which is what lets this layer
// render the login page without an import cycle.
//
// PageShell writes to w (it may persist a freshly minted CSRF token as a cookie),
// so it must be called before any part of the response body is written. Passing
// it as an argument to web.Base, as above, gets that ordering for free.
//
// The user menu is driven by CurrentUser, which is only populated on routes that
// ran AuthMiddleware or OptionalAuthMiddleware. On a route with neither, a
// signed-in visitor renders as anonymous — the same caveat that applies to
// OwnerRef, and the reason server.Routes puts OptionalAuthMiddleware in front of
// every application route.
func PageShell(w http.ResponseWriter, r *http.Request, title string) web.Page {
	page := web.Page{
		Title:     title,
		CSRFToken: ensureCSRFToken(w, r),
	}

	if identity, ok := CurrentUser(r.Context()); ok {
		page.User = &web.PageUser{ID: identity.UserID, Role: identity.Role}
	}

	return page
}
