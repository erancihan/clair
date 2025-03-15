package server

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"os"
	"text/template"

	"github.com/go-redis/redis/v8"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type backend struct {
	Templates *template.Template
}

//go:generate cp -r ../../templates ./
//go:embed templates/*
var resources embed.FS

var templates = template.Must(template.ParseFS(resources, "templates/*"))

func NewBackEnd(ctx context.Context, logger *zap.Logger, redis *redis.Client, pool *pgxpool.Pool) *backend {
	return &backend{
		Templates: templates,
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

	return mux
}
