package server

import (
	"clair/internal/utils"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
)

type Server struct {
	Templates *template.Template
}

func NewServer() *Server {
	return &Server{
		Templates: nil,
	}
}

func (s *Server) ListenAndServe() {
	// start the server
	port := utils.GetEnv("PORT", "8080")

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]string{
			"Region": os.Getenv("FLY_REGION"),
		}

		s.Templates.ExecuteTemplate(w, "index.html.tmpl", data)
	})

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	go func() {
		log.Println("Server listening on", port)
		log.Fatal(server.ListenAndServe())
	}()

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt, os.Kill)

	<-sc
	log.Println("Shutting down...")

	// TODO: gracefully shutdown properly
	if err := server.Shutdown(nil); err != nil {
		log.Fatal(err)
		defer os.Exit(1)
	}

	log.Println("Graceful shutdown")

}
