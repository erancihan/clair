package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/a-h/templ"
	"github.com/erancihan/clair/internal/database/models"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	"github.com/erancihan/clair/internal/server/booking"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/server/games"
	"github.com/erancihan/clair/internal/utils"
	"github.com/erancihan/clair/internal/utils/middleware"
	"github.com/erancihan/clair/internal/utils/router"
	"github.com/erancihan/clair/internal/web"
	"github.com/erancihan/clair/internal/web/pages"
	"github.com/valkey-io/valkey-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type backend struct {
	context server_context.BackEndContext
}

func NewBackEnd(ctx context.Context, logger *zap.Logger, valkey valkey.Client, pool *gorm.DB) *backend {
	// NOTE: chess SQLite persistence (games.InitChessPersistence) is intentionally
	// NOT activated — chess games are in-memory only, so no DB writes occur. The
	// store code is left dormant pending an owner decision on removal vs redesign.
	// Do not re-enable without sign-off.
	games.StartChessCleanup(ctx)

	return &backend{
		context: server_context.BackEndContext{
			DBConn: pool,
			Logger: logger,
			ValKey: valkey,
		},
	}
}

func (s *backend) Server(port int) *http.Server {
	return &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: s.Routes(),
	}
}

// Routes builds the application's HTTP handler.
//
// Everything below the domain block is shared plumbing: static assets, the
// health probe, the authentication endpoints and the handful of pages the site
// itself owns. Domains are mounted one line each.
//
// That single line is the point. A domain package exposes
//
//	func Mount(r *router.Router, ctx server_context.BackEndContext)
//
// and owns its whole path space behind it - see internal/server/games/mount.go,
// which is the reference implementation to copy. Adding, renaming or removing a
// route is then a change inside a domain package, and this function stays a file
// nobody has to contend over.
func (s *backend) Routes() http.Handler {
	mux := router.NewRouter()
	mux.Use(middleware.Logger())

	// Static assets are served straight off the bare mux: they need none of the
	// per-request work the application routes rely on.
	s.mountStatic(mux)

	// Every application route resolves the caller's identity when there is one.
	// This rejects nothing - AuthMiddleware and AdminMiddleware still do the
	// enforcing - it just means CurrentUser, and therefore OwnerRef and the
	// header's user menu, also work on routes open to anonymous visitors.
	app := mux.Middleware(api_auth.OptionalAuthMiddleware(s.context))

	s.mountAPI(app)
	s.mountPages(app)

	// ---- domains ----------------------------------------------------------
	// One line per domain, and nothing else. Anything a domain needs beyond this
	// line belongs in its own package.
	games.Mount(app, s.context)
	booking.Mount(app, s.context)

	return mux
}

// mountStatic serves the embedded static assets and the public file directory.
func (s *backend) mountStatic(mux *router.Router) {
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		// Serve static files from the embedded filesystem
		http.FileServer(http.FS(web.Static)).ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /public/", func(w http.ResponseWriter, r *http.Request) {
		// Serve public files from the embedded filesystem
		http.StripPrefix("/public/", http.FileServer(http.Dir(web.Public()))).ServeHTTP(w, r)
	})
}

// mountAPI registers the shared JSON API: the health probe, the authentication
// endpoints and the administrative user listing. Domain APIs do not belong here;
// a domain mounts its own under whatever prefix it owns.
func (s *backend) mountAPI(app *router.Router) {
	app.Group("api", func(api *router.Router) {
		api.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
			// Valkey is optional; a degraded Valkey is not an outage, so the
			// endpoint still reports 200 and surfaces the dependency status.
			valkeyStatus := utils.ValKeyStatus(r.Context(), s.context.ValKey)

			// return json response
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			json.NewEncoder(w).Encode(map[string]string{
				"status":  "ok",
				"version": "1.0.0", // todo: get version from build variable
				"valkey":  valkeyStatus,
			})
		})

		// authentication middleware test route
		api.Middleware(api_auth.AuthMiddleware(s.context)).Group("", func(route *router.Router) {
			route.HandleFunc("GET /protected", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{
					"message": "You have accessed a protected route",
				})
			})
		})

		api.Group("v1", func(v1 *router.Router) {
			v1.Group("auth", func(auth *router.Router) {
				auth.HandleFunc("POST /login", api_auth.AuthLogin(s.context))

				auth.HandleFunc("POST /register", api_auth.AuthRegister(s.context))

				auth.HandleFunc("GET /logout", api_auth.AuthLogout(s.context))
			})

			// Listing every account is an administrative capability, so this group
			// runs behind AdminMiddleware rather than being publicly readable.
			v1.Group("users", func(users *router.Router) {
				users.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
					// return JSON response with all users

					// get all users from the database. Credentials are never needed
					// here, so they are left in the database rather than loaded and
					// relied upon not to serialize.
					var users []models.User

					tx := s.context.DBConn.Session(&gorm.Session{Context: r.Context()})
					tx.Omit("password").Find(&users)

					// return JSON response
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)

					json.NewEncoder(w).Encode(users)
				})
			}, api_auth.AdminMiddleware(s.context))
		})
	})
}

// mountPages registers the pages the site itself owns, as opposed to the pages a
// domain owns. Anything belonging to a domain goes in that domain's Mount.
func (s *backend) mountPages(app *router.Router) {
	app.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			// return 404 page with 404 HTTP response
			templ.Handler(web.Base(api_auth.PageShell(w, r, "Cihan Eran"), pages.NotFound())).ServeHTTP(w, r)
			return
		}

		templ.Handler(web.Base(api_auth.PageShell(w, r, "Cihan Eran"), pages.Home())).ServeHTTP(w, r)
	})

	app.HandleFunc("GET /login", api_auth.LoginPage(s.context))

	app.HandleFunc("GET /logout", func(w http.ResponseWriter, r *http.Request) {
		api_auth.AuthLogout(s.context).ServeHTTP(w, r)
		http.Redirect(w, r, "/login", http.StatusFound)
	})

	app.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {})

	app.HandleFunc("GET /requester", func(w http.ResponseWriter, r *http.Request) {
		templ.Handler(pages.RequesterPage()).ServeHTTP(w, r)
	})
}
