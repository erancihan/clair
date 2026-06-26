package games_chess

import (
	"fmt"
	"sync"

	"github.com/erancihan/clair/internal/utils"
)

type GameType int

const (
	TypePvP GameType = iota
	TypeAgent
)

type Status string

const (
	StatusWaiting   Status = "Waiting" // Waiting for player 2 in PvP
	StatusOngoing   Status = "Ongoing"
	StatusWhiteWins Status = "White Wins"
	StatusBlackWins Status = "Black Wins"
	StatusDraw      Status = "Draw"
)

type Move struct {
	From string `json:"from"` // e.g. "e2"
	To   string `json:"to"`   // e.g. "e4"
}

type GameState struct {
	Board    Board    `json:"board"`
	Turn     Color    `json:"turn"`
	Status   Status   `json:"status"`
	GameType GameType `json:"game_type"`
}

type Event struct {
	Type   string     `json:"type"`
	GameID string     `json:"game_id"`
	Data   *GameState `json:"data"`
	Error  string     `json:"error,omitempty"`
}

type Game struct {
	mu      sync.Mutex
	ID      string
	State   GameState
	clients map[chan *Event]bool
}

var (
	games sync.Map // map[string]*Game
)

func NewGame(gType GameType) (*Game, string) {
	gameID := utils.GenerateGameID()
	for {
		if _, exists := games.Load(gameID); !exists {
			break
		}
		gameID = utils.GenerateGameID()
	}

	g := &Game{
		ID: gameID,
		State: GameState{
			Board:    NewBoard(),
			Turn:     White,
			Status:   StatusWaiting, // Waiting for player 2 in PvP
			GameType: gType,         // Set to the provided game type
		},
		clients: make(map[chan *Event]bool),
	}

	if gType == TypeAgent {
		g.State.Status = StatusWaiting
	}

	games.Store(gameID, g)
	return g, gameID
}

func GetGame(id string) *Game {
	if val, ok := games.Load(id); ok {
		return val.(*Game)
	}
	return nil
}

func (g *Game) AddClient() chan *Event {
	g.mu.Lock()
	defer g.mu.Unlock()

	ch := make(chan *Event, 10) // Buffered channel to prevent blocking
	g.clients[ch] = true

	ch <- &Event{
		Type:   "update",
		GameID: g.ID,
		Data:   &g.State,
	}

	// if PvP and we have 2 clients attached, start game
	if g.State.GameType == TypePvP && g.State.Status == StatusWaiting {
		if len(g.clients) >= 2 {
			g.State.Status = StatusOngoing
			g.broadcastLocked()
		}
	}

	return ch
}

func (g *Game) RemoveClient(ch chan *Event) {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.clients, ch)
	close(ch)
}

func (g *Game) MakeMove(player string, from, to string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.State.Status != StatusOngoing {
		return nil // Game not active, ignore moves
	}

	playerColor, ok := ColorFromString(player)
	if !ok {
		return fmt.Errorf("invalid player %q", player)
	}
	if playerColor != g.State.Turn {
		return nil // Not this player's turn, ignore move
	}

	// convert from/to like "e2" to board coordinates
	fromPos, err := g.parsePosition(from)
	if err != nil {
		return err
	}
	toPos, err := g.parsePosition(to)
	if err != nil {
		return err
	}

	// Ensure the player is moving one of their own pieces. The frontend already
	// enforces this, but the server is authoritative.
	piece := g.State.Board.Grid[fromPos.Row][fromPos.Col]
	if piece == nil || piece.Color() != playerColor {
		return fmt.Errorf("no %s piece at %s", playerColor, from)
	}

	if err := g.State.Board.MovePiece(fromPos, toPos); err != nil {
		return err
	}

	// Hand the turn to the opponent, then evaluate the resulting position.
	g.State.Turn = g.State.Turn.Opponent()
	g.updateStatusLocked()

	g.broadcastLocked()
	return nil
}

// updateStatusLocked transitions the game to a terminal status when the side to
// move has no legal reply: checkmate (the player who just moved wins) or
// stalemate (draw). Caller must hold g.mu.
func (g *Game) updateStatusLocked() {
	side := g.State.Turn
	if g.State.Board.HasAnyLegalMove(side) {
		return // game continues
	}

	if g.State.Board.InCheck(side) {
		if side == White {
			g.State.Status = StatusBlackWins
		} else {
			g.State.Status = StatusWhiteWins
		}
	} else {
		g.State.Status = StatusDraw // stalemate
	}
}

func (g *Game) broadcastLocked() {
	state := g.State
	event := &Event{
		Type:   "update",
		GameID: g.ID,
		Data:   &state,
	}

	for ch := range g.clients {
		select {
		case ch <- event:
		default:
			// If the channel is full, skip sending to this client to avoid blocking
		}
	}
}

func (g *Game) parsePosition(pos string) (Position, error) {
	if len(pos) != 2 {
		return Position{}, fmt.Errorf("invalid position %q", pos)
	}

	col := int(pos[0] - 'a') // 'a' -> 0, 'b' -> 1, ..., 'h' -> 7
	row := int(pos[1] - '1') // '1' -> 0, '2' -> 1, ..., '8' -> 7
	if col < 0 || col > 7 || row < 0 || row > 7 {
		return Position{}, fmt.Errorf("position out of range %q", pos)
	}

	return Position{Row: row, Col: col}, nil
}
