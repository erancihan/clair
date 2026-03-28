package games_tictactoe

import (
	"crypto/rand"
	"math/big"
	"sync"
	"time"

	"github.com/erancihan/clair/internal/utils"
)

type GameType int

const (
	TypePvP GameType = iota
	TypeAgent
)

type Player string

const (
	PlayerX Player = "X"
	PlayerO Player = "O"
	Empty   Player = ""
)

type Status string

const (
	StatusOngoing Status = "Ongoing"
	StatusXWins   Status = "X Wins"
	StatusOWins   Status = "O Wins"
	StatusDraw    Status = "Draw"
	StatusWaiting Status = "Waiting" // Waiting for player 2 in PvP
)

type GameState struct {
	Board    [9]Player `json:"board"`
	Turn     Player    `json:"turn"`
	Status   Status    `json:"status"`
	GameType GameType  `json:"game_type"`
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
			Board:    [9]Player{},
			Turn:     PlayerX,
			Status:   StatusWaiting,
			GameType: gType,
		},
		clients: make(map[chan *Event]bool),
	}

	if gType == TypeAgent {
		g.State.Status = StatusOngoing
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

	clientChan := make(chan *Event, 100)
	g.clients[clientChan] = true

	// Send initial state immediately
	clientChan <- &Event{
		Type:   "update",
		GameID: g.ID,
		Data:   &g.State,
	}

	// If PvP and we have 2 clients attached, start game
	if g.State.GameType == TypePvP && g.State.Status == StatusWaiting {
		if len(g.clients) >= 2 {
			g.State.Status = StatusOngoing
			g.broadcastLocked()
		}
	}

	return clientChan
}

func (g *Game) RemoveClient(clientChan chan *Event) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.clients, clientChan)
	close(clientChan)
}

func (g *Game) MakeMove(player Player, index int) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.State.Status != StatusOngoing {
		return nil // Game not active
	}
	if g.State.Turn != player {
		return nil // Not this player's turn
	}
	if index < 0 || index >= 9 || g.State.Board[index] != Empty {
		return nil // Invalid move
	}

	g.State.Board[index] = player

	if g.checkWin(player) {
		g.State.Status = Status(player + " Wins")
	} else if g.checkDraw() {
		g.State.Status = StatusDraw
	} else {
		// Switch turn
		if player == PlayerX {
			g.State.Turn = PlayerO
		} else {
			g.State.Turn = PlayerX
		}
	}

	g.broadcastLocked()

	// If playing vs Agent and it's O's turn, trigger agent asynchronously
	if g.State.GameType == TypeAgent && g.State.Status == StatusOngoing && g.State.Turn == PlayerO {
		go g.agentMove()
	}

	return nil
}

func (g *Game) agentMove() {
	time.Sleep(500 * time.Millisecond) // Add slight delay to simulate thinking
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.State.Status != StatusOngoing || g.State.Turn != PlayerO {
		return
	}

	var empty []int
	for i, p := range g.State.Board {
		if p == Empty {
			empty = append(empty, i)
		}
	}

	if len(empty) > 0 {
		bg, _ := rand.Int(rand.Reader, big.NewInt(int64(len(empty))))
		choice := empty[bg.Int64()]
		g.State.Board[choice] = PlayerO

		if g.checkWin(PlayerO) {
			g.State.Status = StatusOWins
		} else if g.checkDraw() {
			g.State.Status = StatusDraw
		} else {
			g.State.Turn = PlayerX
		}
		g.broadcastLocked()
	}
}

func (g *Game) checkWin(player Player) bool {
	winCondition := [8][3]int{
		{0, 1, 2}, {3, 4, 5}, {6, 7, 8}, // Rows
		{0, 3, 6}, {1, 4, 7}, {2, 5, 8}, // Cols
		{0, 4, 8}, {2, 4, 6}, // Diags
	}
	for _, condition := range winCondition {
		if g.State.Board[condition[0]] == player &&
			g.State.Board[condition[1]] == player &&
			g.State.Board[condition[2]] == player {
			return true
		}
	}
	return false
}

func (g *Game) checkDraw() bool {
	for _, p := range g.State.Board {
		if p == Empty {
			return false
		}
	}
	return true
}

func (g *Game) broadcastLocked() {
	// Need to create a copy of state to avoid race condition if clients are slow to read from channel
	st := g.State
	ev := &Event{
		Type:   "update",
		GameID: g.ID,
		Data:   &st,
	}
	for client := range g.clients {
		select {
		case client <- ev:
		default: // Skip if channel is full
		}
	}
}
