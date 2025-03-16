package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"text/template"

	"github.com/erancihan/clair/internal/database/models"
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type backend struct {
	Templates *template.Template

	conn *gorm.DB
}

//go:generate cp -r ../../templates ./
//go:embed templates/*
var resources embed.FS

var templates = template.Must(template.ParseFS(resources, "templates/*"))

func NewBackEnd(ctx context.Context, logger *zap.Logger, redis *redis.Client, pool *gorm.DB) *backend {
	return &backend{
		Templates: templates,
		conn:      pool,
	}
}

func (s *backend) Server(port int) *http.Server {
	return &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: s.Routes(),
	}
}

func (s *backend) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]string{
			"Region": os.Getenv("FLY_REGION"),
		}

		s.Templates.ExecuteTemplate(w, "index.html.tmpl", data)
	})

	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello, World!"))
	})

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

	return mux
}
