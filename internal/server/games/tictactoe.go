package games

import (
	"encoding/json"
	"fmt"
	"net/http"

	game "github.com/erancihan/clair/internal/games/tictactoe"
	server_context "github.com/erancihan/clair/internal/server/context"
)

type tictactoeService struct{}

var TicTacToe GameService = &tictactoeService{}

func (s *tictactoeService) CreateGame(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createGameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		gType := game.TypeAgent
		if req.GameMode == "pvp" {
			gType = game.TypePvP
		}

		instance, id := game.NewGame(gType)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"game_id": id,
			"player":  "X", // Creator is always X in this simple implementation
			"type":    instance.State.GameType,
		})
	}
}

func (s *tictactoeService) StreamGame(ctx server_context.BackEndContext) http.HandlerFunc {
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
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		clientChan := instance.AddClient()
		defer instance.RemoveClient(clientChan)

		// Keep connection alive or send events
		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-clientChan:
				data, _ := json.Marshal(ev)
				fmt.Fprintf(w, "data: %s\n\n", string(data))
				flusher.Flush()
			}
		}
	}
}

type makeMoveRequest struct {
	GameID string `json:"game_id"`
	Player string `json:"player"`
	Index  int    `json:"index"`
}

func (s *tictactoeService) TakeAction(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req makeMoveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		instance := game.GetGame(req.GameID)
		if instance == nil {
			http.Error(w, "game not found", http.StatusNotFound)
			return
		}

		player := game.Player(req.Player)
		if player != game.PlayerX && player != game.PlayerO {
			http.Error(w, "invalid player", http.StatusBadRequest)
			return
		}

		instance.MakeMove(player, req.Index)

		w.WriteHeader(http.StatusOK)
	}
}
