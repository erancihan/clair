package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/server/routes"
	"github.com/erancihan/clair/internal/utils/middleware"
	"github.com/erancihan/clair/internal/utils/router"
	"github.com/erancihan/clair/internal/web"
	"github.com/valkey-io/valkey-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type backend struct {
	context server_context.BackEndContext

	closing   chan struct{}
	closeOnce sync.Once
	closers   []func()
}

func NewBackEnd(ctx context.Context, logger *zap.Logger, valkey valkey.Client, pool *gorm.DB) *backend {
	closing := make(chan struct{})
	return &backend{
		context: server_context.BackEndContext{
			DBConn:  pool,
			Logger:  logger,
			ValKey:  valkey,
			Closing: closing,
		},
		closing: closing,
	}
}

// Server wraps the handler in an http.Server with the timeouts a public
// listener needs. WriteTimeout is deliberately unset: an event stream writes
// for as long as the tab is open.
func (s *backend) Server(port int) *http.Server {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           s.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	srv.RegisterOnShutdown(s.Close)
	return srv
}

// Close tells every long-lived part of the server to stop: open streams end,
// the notification listener disconnects. Safe to call more than once.
func (s *backend) Close() {
	s.closeOnce.Do(func() {
		close(s.closing)
		for _, c := range s.closers {
			c()
		}
	})
}

// Routes builds the application's HTTP handler.
//
// Static assets are served here, off the bare mux. Every other path the server
// answers is registered in internal/server/routes, one Mount per area: the site
// itself, then one line per domain. routes/games.go is the reference to copy
// when adding a domain. Adding, renaming or removing a route is then a change in
// that package, and this function stays a file nobody has to contend over.
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

	// ---- the route table ---------------------------------------------------
	// Every path the server answers is registered in internal/server/routes.
	routes.MountSite(app, s.context)
	routes.MountGames(app, s.context)
	s.closers = append(s.closers, routes.MountBooking(app, s.context))

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
