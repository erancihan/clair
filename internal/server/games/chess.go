package games

import (
	"encoding/json"
	"net/http"

	game "github.com/erancihan/clair/internal/games/chess"
	server_context "github.com/erancihan/clair/internal/server/context"
)

type chessService struct{}

var Chess GameService = &chessService{}

func (s *chessService) CreateGame(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request createGameRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// For now, we only support PvP mode for chess
		if request.GameMode != "pvp" {
			http.Error(w, "unsupported game mode", http.StatusBadRequest)
			return
		}

		instance, id := game.NewGame(game.TypePvP)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"game_id": id,
			"player":  "white", // Creator is always white in this simple implementation
			"type":    instance.State.GameType,
		})
	}
}

func (s *chessService) StreamGame(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing ID", http.StatusBadRequest)
			return
		}

		instance := game.GetGame(id)
		if instance == nil {
			http.Error(w, "game not found", http.StatusNotFound)
			return
		}

		// Upgrade to SSE
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		clientChan := instance.AddClient()
		defer instance.RemoveClient(clientChan)

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-clientChan:
				if event != nil {
					data, err := json.Marshal(event)
					if err != nil {
						continue
					}
					w.Write([]byte("data: " + string(data) + "\n\n"))
					flusher.Flush()
				}
			}
		}
	}
}

type actionRequest struct {
	GameID string `json:"game_id"`
	Player string `json:"player"` // "white" or "black"
	From   string `json:"from"`   // e.g. "e2"
	To     string `json:"to"`     // e.g. "e4"
}

func (s *chessService) TakeAction(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req actionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		instance := game.GetGame(req.GameID)
		if instance == nil {
			http.Error(w, "game not found", http.StatusNotFound)
			return
		}

		err := instance.MakeMove(req.Player, req.From, req.To)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}
