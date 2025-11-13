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
	"github.com/erancihan/clair/internal/utils"
	"github.com/erancihan/clair/internal/web"
	"github.com/valkey-io/valkey-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type backend struct {
	context server_context.BackEndContext
}

func NewBackEnd(ctx context.Context, logger *zap.Logger, valkey valkey.Client, pool *gorm.DB) *backend {
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
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			// 404 Not Found
			templ.Handler(web.Base("Clair", web.NotFound())).ServeHTTP(w, r)
			return
		}

		templ.Handler(web.Base("Clair", web.Home())).ServeHTTP(w, r)
	})

	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		// Serve static files from the embedded filesystem
		http.FileServer(http.FS(web.Static)).ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /public/", func(w http.ResponseWriter, r *http.Request) {
		// Serve public files from the embedded filesystem
		http.StripPrefix("/public/", http.FileServer(http.Dir(web.Public()))).ServeHTTP(w, r)
	})

	/**
	 * Authentication related routes
	 */
	mux.HandleFunc("GET /login", api_auth.LoginPage(s.context))
	mux.HandleFunc("POST /api/v1/auth/login", api_auth.AuthLogin(s.context))
	mux.HandleFunc("POST /api/v1/auth/register", api_auth.AuthRegister(s.context))

	mux.HandleFunc("GET /logout", api_auth.AuthLogout(s.context))

	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {})

	// users list
	mux.HandleFunc("GET /api/v1/users", func(w http.ResponseWriter, r *http.Request) {
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

	handler := utils.RegisterLoggerMiddleware(mux)

	return handler
}
