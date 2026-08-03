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

	// Remaining time per player, as of the start of the current turn. Clients
	// interpolate the running side's clock locally between updates.
	WhiteTimeMs int64 `json:"white_time_ms"`
	BlackTimeMs int64 `json:"black_time_ms"`

	// Moves played so far, in Standard Algebraic Notation.
	Moves []string `json:"moves"`

	// Board highlights for the UI: the last move's squares and the square of a
	// king currently in check ("" when none).
	LastFrom    string `json:"last_from"`
	LastTo      string `json:"last_to"`
	CheckSquare string `json:"check_square"`
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

	// Owner references for the two seats, in the "user:<id>" / "guest:<sid>" form
	// the authentication layer produces. These attribute a seat to a visitor so
	// they can find their way back to it; they are NOT a credential. Moving still
	// requires the seat token above, because an owner reference is derived from a
	// cookie the browser sends on its own.
	whiteOwner string
	blackOwner string

	lastActivity  time.Time   // last state change or client connect/disconnect
	turnStartedAt time.Time   // when the current side's clock started running
	timer         *time.Timer // fires when the side to move runs out of time
}

const initialClockMs int64 = 10 * 60 * 1000 // 10 minutes per player

const (
	aiDepth         = 3
	agentThinkDelay = 400 * time.Millisecond
)

// Join assigns the caller to the next open seat without recording who they are.
// It is equivalent to JoinAs with an empty owner reference.
func (g *Game) Join() (seat string, token string) {
	return g.JoinAs("")
}

// JoinAs assigns the caller to the next open seat and returns the seat name
// ("white", "black" or "spectator") together with its secret token ("" for a
// spectator). When the second player takes the black seat, a waiting PvP game
// starts.
//
// owner is the caller's owner reference from the authentication layer, recorded
// against the seat so GamesForOwner can find it again. Pass "" to leave the seat
// unattributed; that is the only difference from Join.
func (g *Game) JoinAs(owner string) (seat string, token string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch {
	case g.whiteToken == "":
		g.whiteToken = utils.GenerateToken()
		g.whiteOwner = owner
		// In a game against the AI the human takes white and play begins at once.
		if g.State.GameType == TypeAgent && g.State.Status == StatusWaiting {
			g.State.Status = StatusOngoing
			g.startClockLocked()
			g.broadcastLocked()
		}
		return "white", g.whiteToken
	case g.blackToken == "":
		g.blackToken = utils.GenerateToken()
		g.blackOwner = owner
		if g.State.GameType == TypePvP && g.State.Status == StatusWaiting {
			g.State.Status = StatusOngoing
			g.startClockLocked()
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
	return newGame(gType, NewBoard(), White)
}

// NewGameFromFEN creates a game whose starting position is parsed from FEN.
func NewGameFromFEN(gType GameType, fen string) (*Game, string, error) {
	board, turn, err := NewBoardFromFEN(fen)
	if err != nil {
		return nil, "", err
	}
	g, id := newGame(gType, board, turn)
	return g, id, nil
}

func newGame(gType GameType, board Board, turn Color) (*Game, string) {
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
			Board:       board,
			Turn:        turn,
			Status:      StatusWaiting, // Waiting for player 2 in PvP
			GameType:    gType,
			WhiteTimeMs: initialClockMs,
			BlackTimeMs: initialClockMs,
			Moves:       []string{},
		},
		clients:      make(map[chan *Event]bool),
		history:      make(map[string]int),
		lastActivity: time.Now(),
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

// ListOpenGames returns the IDs of PvP games still waiting for a second player.
func ListOpenGames() []string {
	var out []string
	games.Range(func(key, value any) bool {
		g := value.(*Game)
		g.mu.Lock()
		open := g.State.GameType == TypePvP && g.State.Status == StatusWaiting && g.blackToken == ""
		g.mu.Unlock()
		if open {
			out = append(out, key.(string))
		}
		return true
	})
	return out
}

// OwnedGame is one live game a visitor holds a seat in, as returned by
// GamesForOwner.
//
// Token is the seat's secret, handed back so the owner can resume play after
// losing their local copy of it. That is safe only because an owner reference is
// resolved from the caller's own cookies and the response is same-origin: never
// serve this shape with a permissive CORS header.
type OwnedGame struct {
	GameID string   `json:"game_id"`
	Color  string   `json:"color"`
	Token  string   `json:"token"`
	Status Status   `json:"status"`
	Type   GameType `json:"type"`
	Turn   Color    `json:"turn"`
}

// GamesForOwner returns the live games in which owner holds a seat.
//
// The result is drawn from the in-memory store, so it covers only games this
// process is currently holding: it is empty after a restart, it does not see
// games served by another instance, and the janitor drops finished and abandoned
// games from it on the usual schedule. It is a "where was I?" lookup, not game
// history. An empty owner reference matches nothing, so unattributed seats stay
// unreachable through it.
func GamesForOwner(owner string) []OwnedGame {
	if owner == "" {
		return nil
	}

	out := []OwnedGame{}
	games.Range(func(key, value any) bool {
		g := value.(*Game)

		g.mu.Lock()
		var color, token string
		switch owner {
		case g.whiteOwner:
			color, token = "white", g.whiteToken
		case g.blackOwner:
			color, token = "black", g.blackToken
		}
		status, gType, turn := g.State.Status, g.State.GameType, g.State.Turn
		g.mu.Unlock()

		if color != "" {
			out = append(out, OwnedGame{
				GameID: key.(string),
				Color:  color,
				Token:  token,
				Status: status,
				Type:   gType,
				Turn:   turn,
			})
		}
		return true
	})
	return out
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

	if err := g.applyMoveLocked(fromPos, toPos, promo); err != nil {
		return err
	}

	// If this is a game against the AI and it is now the agent's turn, respond.
	if g.State.GameType == TypeAgent && g.State.Status == StatusOngoing && g.State.Turn == Black {
		go g.agentMove()
	}

	return nil
}

// applyMoveLocked applies an already-validated move, updating SAN, clocks, turn,
// repetition history and status, then broadcasts. Caller holds g.mu.
func (g *Game) applyMoveLocked(from, to Position, promo PieceType) error {
	// Render SAN from the pre-move position, before the board is mutated.
	san := moveSAN(&g.State.Board, from, to, promo)

	if err := g.State.Board.MovePiece(from, to, promo); err != nil {
		return err
	}

	g.State.DrawOfferedBy = nil // a move supersedes any pending draw offer
	g.chargeClockLocked()

	// Hand the turn to the opponent, record the new position for repetition
	// tracking, then evaluate the resulting position.
	g.State.Turn = g.State.Turn.Opponent()
	g.history[g.positionKey()]++
	g.updateStatusLocked()

	// Append the check/checkmate marker, record SAN, and update board highlights.
	inCheck := g.State.Board.InCheck(g.State.Turn)
	if g.State.Status == StatusWhiteWins || g.State.Status == StatusBlackWins {
		san += "#" // the only wins this flow produces are by checkmate
	} else if inCheck {
		san += "+"
	}
	g.State.Moves = append(g.State.Moves, san)

	g.State.LastFrom = positionToString(from)
	g.State.LastTo = positionToString(to)
	g.State.CheckSquare = ""
	if g.State.Status == StatusOngoing && inCheck {
		if kp, ok := g.State.Board.kingPos(g.State.Turn); ok {
			g.State.CheckSquare = positionToString(kp)
		}
	}

	g.armClockLocked()
	g.broadcastLocked()
	return nil
}

// agentMove waits briefly (so the reply feels natural) then plays the AI's move.
func (g *Game) agentMove() {
	time.Sleep(agentThinkDelay)
	g.playAgentReply()
}

// playAgentReply searches for and plays the AI's move (Black). The search runs
// without the lock; the board is snapshotted first and the result re-validated
// before it is applied.
func (g *Game) playAgentReply() {
	g.mu.Lock()
	if g.State.GameType != TypeAgent || g.State.Status != StatusOngoing || g.State.Turn != Black {
		g.mu.Unlock()
		return
	}
	boardCopy := g.State.Board
	g.mu.Unlock()

	move, ok := bestMove(&boardCopy, Black, aiDepth)
	if !ok {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.State.GameType != TypeAgent || g.State.Status != StatusOngoing || g.State.Turn != Black {
		return // state changed while searching
	}
	_ = g.applyMoveLocked(move.From, move.To, move.Promotion)
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
	g.stopClockLocked()
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
	g.stopClockLocked()
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

// PGN renders the game as Portable Game Notation.
func (g *Game) PGN() string {
	g.mu.Lock()
	defer g.mu.Unlock()

	result := "*"
	switch g.State.Status {
	case StatusWhiteWins:
		result = "1-0"
	case StatusBlackWins:
		result = "0-1"
	case StatusDraw:
		result = "1/2-1/2"
	}

	var sb strings.Builder
	sb.WriteString("[Event \"Casual Game\"]\n")
	sb.WriteString("[Site \"clair\"]\n")
	sb.WriteString("[White \"White\"]\n")
	sb.WriteString("[Black \"Black\"]\n")
	sb.WriteString("[Result \"" + result + "\"]\n\n")

	for i, san := range g.State.Moves {
		if i%2 == 0 {
			sb.WriteString(fmt.Sprintf("%d. ", i/2+1))
		}
		sb.WriteString(san)
		sb.WriteByte(' ')
	}
	sb.WriteString(result)
	sb.WriteByte('\n')
	return sb.String()
}

// positionKey renders the parts of the position that define repetition (piece
// placement, side to move, castling rights, en-passant target) as a FEN-like
// string, used as the threefold-repetition map key.
func (g *Game) positionKey() string {
	b := &g.State.Board
	var sb strings.Builder

	sb.WriteString(b.placementFEN())

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

// startClockLocked begins the side-to-move's clock; called when the game
// transitions to Ongoing.
func (g *Game) startClockLocked() {
	g.turnStartedAt = time.Now()
	g.armClockLocked()
}

// chargeClockLocked subtracts the time spent this turn from the mover's clock
// and restarts the turn timestamp.
func (g *Game) chargeClockLocked() {
	if g.turnStartedAt.IsZero() {
		return // clock never started (e.g. a directly-constructed test game)
	}
	elapsed := time.Since(g.turnStartedAt).Milliseconds()
	if g.State.Turn == White {
		g.State.WhiteTimeMs = max(0, g.State.WhiteTimeMs-elapsed)
	} else {
		g.State.BlackTimeMs = max(0, g.State.BlackTimeMs-elapsed)
	}
	g.turnStartedAt = time.Now()
}

// remainingMsLocked reports a color's remaining time, accounting for the clock
// currently ticking on the side to move.
func (g *Game) remainingMsLocked(c Color) int64 {
	ms := g.State.WhiteTimeMs
	if c == Black {
		ms = g.State.BlackTimeMs
	}
	if g.State.Status == StatusOngoing && g.State.Turn == c && !g.turnStartedAt.IsZero() {
		ms -= time.Since(g.turnStartedAt).Milliseconds()
	}
	return max(0, ms)
}

// armClockLocked (re)schedules the auto-forfeit timer for the side to move.
func (g *Game) armClockLocked() {
	g.stopClockLocked()
	if g.State.Status != StatusOngoing || g.turnStartedAt.IsZero() {
		return
	}
	turn := g.State.Turn
	remaining := g.remainingMsLocked(turn)
	g.timer = time.AfterFunc(time.Duration(remaining)*time.Millisecond, func() {
		g.flagTimeout(turn)
	})
}

func (g *Game) stopClockLocked() {
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

// flagTimeout ends the game when `turn` has run out of time and it is still
// their move. The opponent wins on time.
func (g *Game) flagTimeout(turn Color) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.State.Status != StatusOngoing || g.State.Turn != turn {
		return // a move was made, or the game already ended
	}
	if g.remainingMsLocked(turn) > 0 {
		return // not actually out of time
	}

	if turn == White {
		g.State.WhiteTimeMs = 0
		g.State.Status = StatusBlackWins
	} else {
		g.State.BlackTimeMs = 0
		g.State.Status = StatusWhiteWins
	}
	g.stopClockLocked()
	g.broadcastLocked()
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
