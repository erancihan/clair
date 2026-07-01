package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/a-h/templ"
	"github.com/erancihan/clair/internal/database/models"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/server/games"
	"github.com/erancihan/clair/internal/utils/middleware"
	"github.com/erancihan/clair/internal/utils/router"
	"github.com/erancihan/clair/internal/web"
	"github.com/valkey-io/valkey-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type backend struct {
	context server_context.BackEndContext
}

func NewBackEnd(ctx context.Context, logger *zap.Logger, valkey valkey.Client, pool *gorm.DB) *backend {
	// Start the background janitor that evicts finished/abandoned in-memory games.
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

func (s *backend) Routes() http.Handler {
	mux := router.NewRouter()
	mux.Use(middleware.Logger())

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			// return 404 page with 404 HTTP response
			templ.Handler(web.Base("Cihan Eran", web.NotFound())).ServeHTTP(w, r)
			return
		}

		templ.Handler(web.Base("Cihan Eran", web.Home())).ServeHTTP(w, r)
	})

	mux.Group("api", func(api *router.Router) {
		api.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)

			// return json response
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "ok",
				"version": "1.0.0", // todo: get version from build variable
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

			v1.Group("users", func(users *router.Router) {
				users.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
					// return JSON response with all users

					// get all users from the database
					var users []models.User

					tx := s.context.DBConn.Session(&gorm.Session{Context: r.Context()})
					tx.Find(&users)

					// return JSON response
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)

					json.NewEncoder(w).Encode(users)
				})
			})
		})
	})

	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		// Serve static files from the embedded filesystem
		http.FileServer(http.FS(web.Static)).ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /public/", func(w http.ResponseWriter, r *http.Request) {
		// Serve public files from the embedded filesystem
		http.StripPrefix("/public/", http.FileServer(http.Dir(web.Public()))).ServeHTTP(w, r)
	})

	mux.HandleFunc("GET /login", api_auth.LoginPage(s.context))

	mux.HandleFunc("GET /logout", func(w http.ResponseWriter, r *http.Request) {
		api_auth.AuthLogout(s.context).ServeHTTP(w, r)
		http.Redirect(w, r, "/login", http.StatusFound)
	})

	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {})

	mux.Group("games", func(gamesRoute *router.Router) {
		gamesRoute.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			templ.Handler(web.Base("Games", web.Games())).ServeHTTP(w, r)
		})

		gamesRoute.Group("tic-tac-toe", func(route *router.Router) {
			route.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
				templ.Handler(web.Base("Tic-Tac-Toe", web.TicTacToe())).ServeHTTP(w, r)
			})
			route.HandleFunc("POST /create", games.TicTacToe.CreateGame(s.context))
			route.HandleFunc("GET /stream", games.TicTacToe.StreamGame(s.context))
			route.HandleFunc("POST /move", games.TicTacToe.TakeAction(s.context))
		})

		gamesRoute.Group("chess", func(route *router.Router) {
			route.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
				templ.Handler(web.Base("Chess", web.Chess())).ServeHTTP(w, r)
			})
			route.HandleFunc("POST /create", games.Chess.CreateGame(s.context))
			route.HandleFunc("POST /join", games.Chess.JoinGame(s.context))
			route.HandleFunc("GET /stream", games.Chess.StreamGame(s.context))
			route.HandleFunc("POST /move", games.Chess.TakeAction(s.context))
			route.HandleFunc("GET /pgn", games.Chess.PGN(s.context))
		})
	})

	mux.HandleFunc("GET /requester", func(w http.ResponseWriter, r *http.Request) {
		templ.Handler(web.Requester()).ServeHTTP(w, r)
	})

	return mux
}
