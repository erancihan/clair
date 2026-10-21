package routes

import (
	"encoding/json"
	"net/http"

	"github.com/a-h/templ"
	"github.com/erancihan/clair/internal/clair"
	"github.com/erancihan/clair/internal/database/models"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/utils"
	"github.com/erancihan/clair/internal/utils/router"
	"github.com/erancihan/clair/internal/web"
	"github.com/erancihan/clair/internal/web/pages"
	"gorm.io/gorm"
)

// MountSite registers what the site itself owns, as opposed to what a domain
// owns: the health probe, the authentication endpoints, the administrative user
// listing and the site's own pages.
func MountSite(r *router.Router, ctx server_context.BackEndContext) {
	mountSiteAPI(r, ctx)
	mountSitePages(r, ctx)
}

// mountSiteAPI registers the shared JSON API. Domain APIs do not belong here; a
// domain's routes go in a file of their own in this package.
func mountSiteAPI(r *router.Router, ctx server_context.BackEndContext) {
	r.Group("api", func(api *router.Router) {
		api.HandleFunc("GET /health", health(ctx))

		api.Group("v1", func(v1 *router.Router) {
			v1.Group("auth", func(auth *router.Router) {
				auth.HandleFunc("POST /login", api_auth.AuthLogin(ctx))

				auth.HandleFunc("POST /register", api_auth.AuthRegister(ctx))

				auth.HandleFunc("GET /logout", api_auth.AuthLogout(ctx))
			})

			// Listing every account is an administrative capability, so the route
			// runs behind AdminMiddleware rather than being publicly readable.
			//
			// Registered from the v1 router with its full path so the pattern is
			// "/api/v1/users" exactly. A "GET /" inside a "users" group would be the
			// subtree pattern "/api/v1/users/", which answers everything beneath it.
			v1.Middleware(api_auth.AdminMiddleware(ctx)).HandleFunc("GET /users", listUsers(ctx))
		})
	})
}

// health reports whether the process can do its job. The database decides
// that; Valkey is reported but optional.
func health(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := "ok"
		code := http.StatusOK

		// The database is the one dependency without which nothing works.
		dbStatus := "ok"
		if sqlDB, err := ctx.DBConn.DB(); err != nil || sqlDB.PingContext(r.Context()) != nil {
			dbStatus, status, code = "error", "degraded", http.StatusServiceUnavailable
		}

		// Valkey is optional; a degraded Valkey is not an outage.
		valkeyStatus := utils.ValKeyStatus(r.Context(), ctx.ValKey)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":   status,
			"version":  clair.Version,
			"database": dbStatus,
			"valkey":   valkeyStatus,
		})
	}
}

// listUsers returns every account. Credentials are never needed here, so they
// are left in the database rather than loaded and relied upon not to serialize.
func listUsers(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var users []models.User

		tx := ctx.DBConn.Session(&gorm.Session{Context: r.Context()})
		tx.Omit("password").Find(&users)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		_ = json.NewEncoder(w).Encode(users)
	}
}

// mountSitePages registers the pages the site itself owns. Anything belonging
// to a domain goes in that domain's file.
func mountSitePages(r *router.Router, ctx server_context.BackEndContext) {
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			// The 404 page, with the 404 status: a crawler or a monitor reads the
			// status, not the words on the page.
			templ.Handler(web.Base(api_auth.PageShell(w, r, "Cihan Eran"), pages.NotFound()), templ.WithStatus(http.StatusNotFound)).ServeHTTP(w, r)
			return
		}

		templ.Handler(web.Base(api_auth.PageShell(w, r, "Cihan Eran"), pages.Home())).ServeHTTP(w, r)
	})

	r.HandleFunc("GET /login", api_auth.LoginPage(ctx))

	r.HandleFunc("GET /projects", page("Projects", pages.Projects))

	r.HandleFunc("GET /logout", func(w http.ResponseWriter, r *http.Request) {
		api_auth.AuthLogout(ctx).ServeHTTP(w, r)
		http.Redirect(w, r, "/login", http.StatusFound)
	})

	r.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {})

	r.HandleFunc("GET /requester", func(w http.ResponseWriter, r *http.Request) {
		templ.Handler(pages.RequesterPage()).ServeHTTP(w, r)
	})
}
