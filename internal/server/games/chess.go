package games

import (
	"context"
	"encoding/json"
	"net/http"

	game "github.com/erancihan/clair/internal/games/chess"
	server_context "github.com/erancihan/clair/internal/server/context"
)

// StartChessCleanup launches the background janitor that evicts finished and
// abandoned chess games. It is wired from server startup with the server's
// context.
func StartChessCleanup(ctx context.Context) {
	game.StartCleanup(ctx)
}

type chessService struct{}

// Chess is stored as the concrete type (not the GameService interface) so the
// chess-only JoinGame handler is reachable from the router.
var Chess = &chessService{}

var _ GameService = Chess

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
		seat, token := instance.Join() // the creator takes the white seat

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"game_id": id,
			"player":  seat,
			"token":   token,
			"type":    instance.State.GameType,
		})
	}
}

type joinGameRequest struct {
	GameID string `json:"game_id"`
}

// JoinGame assigns the caller to an open seat (or spectator) and returns the
// seat color plus its secret token. It is chess-specific and not part of the
// shared GameService interface.
func (s *chessService) JoinGame(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req joinGameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		instance := game.GetGame(req.GameID)
		if instance == nil {
			http.Error(w, "game not found", http.StatusNotFound)
			return
		}

		seat, token := instance.Join()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"player": seat,
			"token":  token,
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
	GameID    string `json:"game_id"`
	Token     string `json:"token"`               // seat token authorizing the action
	Action    string `json:"action,omitempty"`    // "move" (default), "resign", "offer_draw", "accept_draw", "decline_draw"
	From      string `json:"from"`                // e.g. "e2"
	To        string `json:"to"`                  // e.g. "e4"
	Promotion string `json:"promotion,omitempty"` // "queen"/"rook"/"bishop"/"knight" for a promoting pawn
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

		// The seat token is authoritative for identity; the client cannot pick
		// which color it plays.
		color, ok := instance.SeatColor(req.Token)
		if !ok {
			http.Error(w, "not a player in this game", http.StatusForbidden)
			return
		}

		var err error
		switch req.Action {
		case "resign":
			err = instance.Resign(color)
		case "offer_draw":
			err = instance.OfferDraw(color)
		case "accept_draw":
			err = instance.AcceptDraw(color)
		case "decline_draw":
			err = instance.DeclineDraw(color)
		default: // "move" or empty
			err = instance.MakeMove(color, req.From, req.To, req.Promotion)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}
