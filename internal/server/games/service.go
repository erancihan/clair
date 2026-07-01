package games

import (
	"net/http"

	server_context "github.com/erancihan/clair/internal/server/context"
)

type createGameRequest struct {
	GameMode string `json:"game_mode"`     // "pvp" or "agent"
	FEN      string `json:"fen,omitempty"` // optional starting position (chess)
}

type GameService interface {
	CreateGame(context server_context.BackEndContext) http.HandlerFunc
	StreamGame(context server_context.BackEndContext) http.HandlerFunc
	TakeAction(context server_context.BackEndContext) http.HandlerFunc
}
