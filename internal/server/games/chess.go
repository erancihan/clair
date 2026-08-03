package games

import (
	"context"
	"encoding/json"
	"net/http"

	game "github.com/erancihan/clair/internal/games/chess"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
)

// StartChessCleanup launches the background janitor that evicts finished and
// abandoned chess games. It is wired from mountChess, and is safe to call more
// than once - only the first call starts the loop.
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

		var gType game.GameType
		switch request.GameMode {
		case "pvp":
			gType = game.TypePvP
		case "agent":
			gType = game.TypeAgent
		default:
			http.Error(w, "unsupported game mode", http.StatusBadRequest)
			return
		}

		var instance *game.Game
		var id string
		if request.FEN != "" {
			var err error
			if instance, id, err = game.NewGameFromFEN(gType, request.FEN); err != nil {
				http.Error(w, "invalid FEN: "+err.Error(), http.StatusBadRequest)
				return
			}
		} else {
			instance, id = game.NewGame(gType)
		}
		// The creator takes the white seat, attributed to whoever they are: a real
		// account when signed in, otherwise the stable guest reference behind the
		// sid cookie. Either way play needs no login.
		seat, token := instance.JoinAs(api_auth.OwnerRef(w, r))

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

		seat, token := instance.JoinAs(api_auth.OwnerRef(w, r))

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

// PGN returns the game's moves as a Portable Game Notation document.
func (s *chessService) PGN(ctx server_context.BackEndContext) http.HandlerFunc {
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

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(instance.PGN()))
	}
}

// OpenGames lists PvP games waiting for a second player (for matchmaking).
func (s *chessService) OpenGames(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"games": game.ListOpenGames(),
		})
	}
}

// MyGames lists the live games the caller holds a seat in, so a player who still
// has their cookie but has lost their local copy of a seat token can pick a game
// back up. Anonymous visitors are matched on their guest reference, signed-in
// ones on their account, so this works either way and needs no login.
//
// "ephemeral": true is part of the response on purpose. Chess keeps its games in
// memory, so this lists what this process is holding right now - not history. It
// comes back empty after a restart and does not see another instance's games.
// Making it durable means giving the games domain real tables (games.Models()),
// which is a separate piece of work.
func (s *chessService) MyGames(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The response carries seat tokens, so it must stay same-origin: no CORS
		// header here, unlike the SSE stream.
		owner := api_auth.OwnerRef(w, r)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"games":     game.GamesForOwner(owner),
			"ephemeral": true,
		})
	}
}
