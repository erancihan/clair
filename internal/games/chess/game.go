package games_chess

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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
	// DrawOfferedBy is the color of the player with an outstanding draw offer,
	// or nil when there is none.
	DrawOfferedBy *Color `json:"draw_offered_by"`
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
	history map[string]int // position-key counts for threefold-repetition detection

	// Secret per-seat tokens. A request is authorized to move a color only if it
	// presents the matching token; this is what actually binds a person to a seat
	// (the client-supplied color is not trusted).
	whiteToken string
	blackToken string

	lastActivity time.Time // last state change or client connect/disconnect
}

// Join assigns the caller to the next open seat and returns the seat name
// ("white", "black" or "spectator") together with its secret token ("" for a
// spectator). When the second player takes the black seat, a waiting PvP game
// starts.
func (g *Game) Join() (seat string, token string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch {
	case g.whiteToken == "":
		g.whiteToken = utils.GenerateToken()
		return "white", g.whiteToken
	case g.blackToken == "":
		g.blackToken = utils.GenerateToken()
		if g.State.GameType == TypePvP && g.State.Status == StatusWaiting {
			g.State.Status = StatusOngoing
			g.broadcastLocked()
		}
		return "black", g.blackToken
	default:
		return "spectator", ""
	}
}

// SeatColor resolves a seat token to the color it is authorized to play, or
// ("", false) for spectators and unknown tokens.
func (g *Game) SeatColor(token string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch {
	case token == "":
		return "", false
	case token == g.whiteToken:
		return "white", true
	case token == g.blackToken:
		return "black", true
	default:
		return "", false
	}
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
		clients:      make(map[chan *Event]bool),
		history:      make(map[string]int),
		lastActivity: time.Now(),
	}

	if gType == TypeAgent {
		g.State.Status = StatusWaiting
	}

	g.history[g.positionKey()] = 1 // count the starting position

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

	g.lastActivity = time.Now()

	ch := make(chan *Event, 10) // Buffered channel to prevent blocking
	g.clients[ch] = true

	// Send the current state to the newly connected client. (Game start is
	// driven by seat occupancy in Join, not by the number of SSE clients, so a
	// player opening two tabs no longer starts the game.)
	state := g.State
	ch <- &Event{
		Type:   "update",
		GameID: g.ID,
		Data:   &state,
	}

	return ch
}

func (g *Game) RemoveClient(ch chan *Event) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.lastActivity = time.Now()
	delete(g.clients, ch)
	close(ch)
}

func (g *Game) MakeMove(player string, from, to, promotion string) error {
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

	promo := PawnType // PawnType == no promotion
	if pt, ok := PieceTypeFromString(promotion); ok {
		promo = pt
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

	if err := g.State.Board.MovePiece(fromPos, toPos, promo); err != nil {
		return err
	}

	// A move supersedes any pending draw offer.
	g.State.DrawOfferedBy = nil

	// Hand the turn to the opponent, record the new position for repetition
	// tracking, then evaluate the resulting position.
	g.State.Turn = g.State.Turn.Opponent()
	g.history[g.positionKey()]++
	g.updateStatusLocked()

	g.broadcastLocked()
	return nil
}

// Resign ends the game in favor of the resigning player's opponent.
func (g *Game) Resign(player string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.State.Status != StatusOngoing {
		return nil
	}
	color, ok := ColorFromString(player)
	if !ok {
		return fmt.Errorf("invalid player %q", player)
	}

	if color == White {
		g.State.Status = StatusBlackWins
	} else {
		g.State.Status = StatusWhiteWins
	}
	g.State.DrawOfferedBy = nil
	g.broadcastLocked()
	return nil
}

// OfferDraw records an outstanding draw offer from the given player.
func (g *Game) OfferDraw(player string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.State.Status != StatusOngoing {
		return nil
	}
	color, ok := ColorFromString(player)
	if !ok {
		return fmt.Errorf("invalid player %q", player)
	}

	g.State.DrawOfferedBy = &color
	g.broadcastLocked()
	return nil
}

// AcceptDraw ends the game in a draw, but only when the accepting player is
// responding to an offer made by their opponent.
func (g *Game) AcceptDraw(player string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.State.Status != StatusOngoing {
		return nil
	}
	color, ok := ColorFromString(player)
	if !ok {
		return fmt.Errorf("invalid player %q", player)
	}
	if g.State.DrawOfferedBy == nil || *g.State.DrawOfferedBy == color {
		return nil // no opponent offer to accept
	}

	g.State.Status = StatusDraw
	g.State.DrawOfferedBy = nil
	g.broadcastLocked()
	return nil
}

// DeclineDraw clears any outstanding draw offer.
func (g *Game) DeclineDraw(player string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.State.DrawOfferedBy == nil {
		return nil
	}
	g.State.DrawOfferedBy = nil
	g.broadcastLocked()
	return nil
}

// updateStatusLocked transitions the game to a terminal status when the side to
// move has no legal reply: checkmate (the player who just moved wins) or
// stalemate (draw). Caller must hold g.mu.
func (g *Game) updateStatusLocked() {
	side := g.State.Turn

	if !g.State.Board.HasAnyLegalMove(side) {
		if g.State.Board.InCheck(side) {
			if side == White {
				g.State.Status = StatusBlackWins
			} else {
				g.State.Status = StatusWhiteWins
			}
		} else {
			g.State.Status = StatusDraw // stalemate
		}
		return
	}

	// Automatic draws while legal moves remain.
	switch {
	case g.State.Board.InsufficientMaterial():
		g.State.Status = StatusDraw
	case g.State.Board.HalfmoveClock >= 100:
		g.State.Status = StatusDraw // fifty-move rule
	case g.history[g.positionKey()] >= 3:
		g.State.Status = StatusDraw // threefold repetition
	}
}

// positionKey renders the parts of the position that define repetition (piece
// placement, side to move, castling rights, en-passant target) as a FEN-like
// string, used as the threefold-repetition map key.
func (g *Game) positionKey() string {
	b := &g.State.Board
	var sb strings.Builder

	for r := 7; r >= 0; r-- {
		empty := 0
		for c := 0; c < 8; c++ {
			p := b.Grid[r][c]
			if p == nil {
				empty++
				continue
			}
			if empty > 0 {
				sb.WriteByte(byte('0' + empty))
				empty = 0
			}
			sb.WriteByte(fenChar(p))
		}
		if empty > 0 {
			sb.WriteByte(byte('0' + empty))
		}
		if r > 0 {
			sb.WriteByte('/')
		}
	}

	sb.WriteByte(' ')
	sb.WriteString(g.State.Turn.String())

	sb.WriteByte(' ')
	cr := b.CastlingRights
	if cr.WhiteKingside {
		sb.WriteByte('K')
	}
	if cr.WhiteQueenside {
		sb.WriteByte('Q')
	}
	if cr.BlackKingside {
		sb.WriteByte('k')
	}
	if cr.BlackQueenside {
		sb.WriteByte('q')
	}

	sb.WriteByte(' ')
	if b.EnPassant != nil {
		sb.WriteString(positionToString(*b.EnPassant))
	} else {
		sb.WriteByte('-')
	}

	return sb.String()
}

const (
	cleanupInterval  = 5 * time.Minute
	finishedGameTTL  = 10 * time.Minute // finished games kept this long for late viewers
	abandonedGameTTL = 30 * time.Minute // games with no connected clients
)

var cleanupOnce sync.Once

// StartCleanup launches the background janitor that evicts finished and
// abandoned games so the in-memory store does not grow without bound. It is
// safe to call more than once; only the first call starts the loop, which runs
// until ctx is cancelled.
func StartCleanup(ctx context.Context) {
	cleanupOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(cleanupInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-ticker.C:
					runCleanup(now)
				}
			}
		}()
	})
}

// runCleanup removes games that finished a while ago or have been abandoned by
// every client. Split out from the loop so it can be tested deterministically.
func runCleanup(now time.Time) {
	games.Range(func(key, value any) bool {
		g := value.(*Game)

		g.mu.Lock()
		idle := now.Sub(g.lastActivity)
		finished := g.State.Status != StatusOngoing && g.State.Status != StatusWaiting
		abandoned := len(g.clients) == 0
		g.mu.Unlock()

		if (finished && idle > finishedGameTTL) || (abandoned && idle > abandonedGameTTL) {
			games.Delete(key)
		}
		return true
	})
}

func (g *Game) broadcastLocked() {
	g.lastActivity = time.Now()

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
