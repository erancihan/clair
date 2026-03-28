package games_chess

import (
	"sync"

	"github.com/erancihan/clair/internal/utils"
)

type GameType int

const (
	TypePvP GameType = iota
	TypeAgent
)

type Player string

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
	Turn     Player   `json:"turn"`
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
			Turn:     "white",
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
	if player != string(g.State.Turn) {
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

	err = g.State.Board.MovePiece(fromPos, toPos)
	if err != nil {
		return err
	}

	// Switch turn
	if g.State.Turn == "white" {
		g.State.Turn = "black"
	} else {
		g.State.Turn = "white"
	}

	g.broadcastLocked()
	return nil
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
	return Position{
		Row: int(pos[1] - '1'), // '1' -> 0, '2' -> 1, ..., '8' -> 7
		Col: int(pos[0] - 'a'), // 'a' -> 0, 'b' -> 1, ..., 'h' -> 7
	}, nil
}
