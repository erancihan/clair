package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/a-h/templ"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/utils"
	"github.com/erancihan/clair/web"
	"github.com/valkey-io/valkey-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type backend struct {
	conn   *gorm.DB
	logger *zap.Logger
	valkey valkey.Client
}

func NewBackEnd(ctx context.Context, logger *zap.Logger, valkey valkey.Client, pool *gorm.DB) *backend {
	return &backend{
		conn:   pool,
		logger: logger,
		valkey: valkey,
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
		templ.Handler(web.Base("Clair", web.Home())).ServeHTTP(w, r)
	})

	mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		// Serve static files from the embedded filesystem
		http.FileServer(http.FS(web.Static)).ServeHTTP(w, r)
	})

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		templ.Handler(web.Base("Clair", web.Login())).ServeHTTP(w, r)
	})

	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {})

	// users list
	mux.HandleFunc("/v1/users", func(w http.ResponseWriter, r *http.Request) {
		// return JSON response with all users

		// get all users from the database
		var users []models.User

		tx := s.conn.Session(&gorm.Session{Context: r.Context()})
		tx.Find(&users)

		// return JSON response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		json.NewEncoder(w).Encode(users)
	})

	handler := utils.RegisterLoggerMiddleware(mux)

	return handler
}
