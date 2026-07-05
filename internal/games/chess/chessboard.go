package games_chess

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// CastlingRights tracks which castling moves remain available. It is part of
// the FEN-style board state; the castling moves themselves are implemented in
// a later phase.
type CastlingRights struct {
	WhiteKingside  bool
	WhiteQueenside bool
	BlackKingside  bool
	BlackQueenside bool
}

type Board struct {
	Grid       [8][8]Piece
	ValidMoves map[Position][]Position // Cache of legal moves per occupied square

	// FEN-style state. EnPassant is the square a pawn may move onto to capture
	// en passant (nil when unavailable). CastlingRights, HalfmoveClock and
	// FullmoveNumber round out the information needed for castling, the
	// fifty-move rule and threefold-repetition detection in later phases.
	EnPassant      *Position
	CastlingRights CastlingRights
	HalfmoveClock  int
	FullmoveNumber int
}

// LegalMove is a from/to pair in board coordinates, used for whole-board move
// generation (perft tests, future AI). Promotion names the piece a pawn becomes
// when reaching the last rank; PawnType means "not a promotion".
type LegalMove struct {
	From      Position
	To        Position
	Promotion PieceType
}

// Direction tables shared by attack detection.
var (
	knightOffsets = [8][2]int{
		{2, 1}, {2, -1}, {-2, 1}, {-2, -1},
		{1, 2}, {1, -2}, {-1, 2}, {-1, -2},
	}
	kingOffsets = [8][2]int{
		{-1, -1}, {-1, 0}, {-1, 1},
		{0, -1}, {0, 1},
		{1, -1}, {1, 0}, {1, 1},
	}
	orthogonalDirs = [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}}
	diagonalDirs   = [4][2]int{{-1, -1}, {-1, 1}, {1, -1}, {1, 1}}
)

func NewBoard() Board {
	b := Board{
		Grid: [8][8]Piece{
			{&Rook{White}, &Knight{White}, &Bishop{White}, &Queen{White}, &King{White}, &Bishop{White}, &Knight{White}, &Rook{White}},
			{&Pawn{White}, &Pawn{White}, &Pawn{White}, &Pawn{White}, &Pawn{White}, &Pawn{White}, &Pawn{White}, &Pawn{White}},
			{},
			{},
			{},
			{},
			{&Pawn{Black}, &Pawn{Black}, &Pawn{Black}, &Pawn{Black}, &Pawn{Black}, &Pawn{Black}, &Pawn{Black}, &Pawn{Black}},
			{&Rook{Black}, &Knight{Black}, &Bishop{Black}, &Queen{Black}, &King{Black}, &Bishop{Black}, &Knight{Black}, &Rook{Black}},
		},
		CastlingRights: CastlingRights{true, true, true, true},
		FullmoveNumber: 1,
	}

	b.PopulateValidMoves() // Populate legal moves for all pieces at the start

	return b
}

// InBounds reports whether the (row, col) coordinate is on the board.
func (b *Board) InBounds(row, col int) bool {
	return row >= 0 && row < 8 && col >= 0 && col < 8
}

func (b *Board) IsValidDestination(row, col int, color Color) bool {
	if !b.InBounds(row, col) {
		return false // Out of bounds
	}

	target := b.Grid[row][col]
	if target == nil {
		return true // Empty square
	}

	return target.Color() != color
}

// applyMove applies a (possibly special) move in place, updating the FEN-style
// state. promo is the piece a promoting pawn becomes (PawnType = no promotion).
// It performs no legality checking and is meant to be called on the real board
// or on a copy (nb := *b) when probing legality or enumerating positions.
func (b *Board) applyMove(from, to Position, promo PieceType) {
	piece := b.Grid[from.Row][from.Col]
	color := piece.Color()
	isPawn := piece.Type() == PawnType
	isCapture := b.Grid[to.Row][to.Col] != nil

	prevEP := b.EnPassant
	b.EnPassant = nil

	// En passant: a pawn moving onto the recorded ep target captures the pawn
	// that sits beside the origin (it is removed, not on the destination square).
	if isPawn && prevEP != nil && to == *prevEP {
		b.Grid[from.Row][to.Col] = nil
		isCapture = true
	}

	// Relocate the piece.
	b.Grid[to.Row][to.Col] = piece
	b.Grid[from.Row][from.Col] = nil

	// Castling: a king moving two files drags the matching rook to the far side.
	if piece.Type() == KingType && abs(to.Col-from.Col) == 2 {
		if to.Col == 6 { // king-side: h-rook -> f
			b.Grid[to.Row][5] = b.Grid[to.Row][7]
			b.Grid[to.Row][7] = nil
		} else if to.Col == 2 { // queen-side: a-rook -> d
			b.Grid[to.Row][3] = b.Grid[to.Row][0]
			b.Grid[to.Row][0] = nil
		}
	}

	// Promotion.
	if isPawn && (to.Row == 0 || to.Row == 7) && promo != PawnType {
		b.Grid[to.Row][to.Col] = newPiece(promo, color)
	}

	// Record a new ep target when a pawn advances two ranks.
	if isPawn && abs(to.Row-from.Row) == 2 {
		b.EnPassant = &Position{Row: (from.Row + to.Row) / 2, Col: from.Col}
	}

	b.updateCastlingRights(from, to, piece)

	// Halfmove clock resets on a pawn move or capture, else increments.
	if isPawn || isCapture {
		b.HalfmoveClock = 0
	} else {
		b.HalfmoveClock++
	}
	if color == Black {
		b.FullmoveNumber++
	}
}

// updateCastlingRights revokes rights when a king or rook leaves its home square
// or a rook is captured on its home corner.
func (b *Board) updateCastlingRights(from, to Position, piece Piece) {
	if piece.Type() == KingType {
		if piece.Color() == White {
			b.CastlingRights.WhiteKingside = false
			b.CastlingRights.WhiteQueenside = false
		} else {
			b.CastlingRights.BlackKingside = false
			b.CastlingRights.BlackQueenside = false
		}
	}

	clear := func(p Position) {
		switch {
		case p.Row == 0 && p.Col == 0:
			b.CastlingRights.WhiteQueenside = false
		case p.Row == 0 && p.Col == 7:
			b.CastlingRights.WhiteKingside = false
		case p.Row == 7 && p.Col == 0:
			b.CastlingRights.BlackQueenside = false
		case p.Row == 7 && p.Col == 7:
			b.CastlingRights.BlackKingside = false
		}
	}
	clear(from) // a rook left this corner
	clear(to)   // a rook was captured on this corner
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// newPiece constructs a piece of the given type and color (used for promotion
// and FEN parsing).
func newPiece(t PieceType, c Color) Piece {
	switch t {
	case KnightType:
		return &Knight{c}
	case BishopType:
		return &Bishop{c}
	case RookType:
		return &Rook{c}
	case QueenType:
		return &Queen{c}
	case KingType:
		return &King{c}
	default:
		return &Pawn{c}
	}
}

// kingPos locates the king of the given color.
func (b *Board) kingPos(c Color) (Position, bool) {
	for r := 0; r < 8; r++ {
		for col := 0; col < 8; col++ {
			p := b.Grid[r][col]
			if p != nil && p.Type() == KingType && p.Color() == c {
				return Position{Row: r, Col: col}, true
			}
		}
	}
	return Position{}, false
}

// IsAttacked reports whether the given square is attacked by any piece of color
// `by`. It is deliberately independent of ValidMoves so that pawn *attacks*
// (diagonal only) are modelled correctly regardless of whether the target
// square is occupied.
func (b *Board) IsAttacked(target Position, by Color) bool {
	// Pawn attacks: a pawn of color `by` sits one rank "behind" the square it
	// attacks, diagonally. White pawns attack toward +1 row, black toward -1.
	pawnDir := 1
	if by == Black {
		pawnDir = -1
	}
	for _, dc := range [2]int{-1, 1} {
		pr, pc := target.Row-pawnDir, target.Col+dc
		if b.InBounds(pr, pc) {
			if p := b.Grid[pr][pc]; p != nil && p.Color() == by && p.Type() == PawnType {
				return true
			}
		}
	}

	// Knight attacks.
	for _, o := range knightOffsets {
		pr, pc := target.Row+o[0], target.Col+o[1]
		if b.InBounds(pr, pc) {
			if p := b.Grid[pr][pc]; p != nil && p.Color() == by && p.Type() == KnightType {
				return true
			}
		}
	}

	// King attacks (adjacency).
	for _, o := range kingOffsets {
		pr, pc := target.Row+o[0], target.Col+o[1]
		if b.InBounds(pr, pc) {
			if p := b.Grid[pr][pc]; p != nil && p.Color() == by && p.Type() == KingType {
				return true
			}
		}
	}

	// Sliding attacks: rook/queen along ranks and files, bishop/queen along
	// diagonals. Walk outward until the first piece is encountered.
	if b.sliderAttacks(target, by, orthogonalDirs[:], RookType) {
		return true
	}
	if b.sliderAttacks(target, by, diagonalDirs[:], BishopType) {
		return true
	}

	return false
}

// sliderAttacks walks each direction outward from target and reports whether the
// first piece encountered is an enemy slider of the matching kind (or a queen).
func (b *Board) sliderAttacks(target Position, by Color, dirs [][2]int, straight PieceType) bool {
	for _, d := range dirs {
		for i := 1; i < 8; i++ {
			pr, pc := target.Row+d[0]*i, target.Col+d[1]*i
			if !b.InBounds(pr, pc) {
				break
			}
			p := b.Grid[pr][pc]
			if p == nil {
				continue
			}
			if p.Color() == by && (p.Type() == straight || p.Type() == QueenType) {
				return true
			}
			break // blocked by some other piece
		}
	}
	return false
}

// InCheck reports whether the given color's king is currently under attack.
func (b *Board) InCheck(c Color) bool {
	kp, ok := b.kingPos(c)
	if !ok {
		return false
	}
	return b.IsAttacked(kp, c.Opponent())
}

// LegalMoves returns the king-safety-filtered destinations for the piece at
// `from`. A pseudo-legal move is legal only if it does not leave the mover's own
// king in check.
func (b *Board) LegalMoves(from Position) []Position {
	piece := b.Grid[from.Row][from.Col]
	if piece == nil {
		return nil
	}
	color := piece.Color()

	var legal []Position
	for _, to := range piece.ValidMoves(b, from) {
		nb := *b
		// Promotion choice does not affect the mover's own king safety, so the
		// legality probe leaves the pawn un-promoted (PawnType).
		nb.applyMove(from, to, PawnType)
		if !nb.InCheck(color) {
			legal = append(legal, to)
		}
	}
	return legal
}

// GenerateLegalMoves returns every legal move available to `color`. A pawn
// reaching the last rank expands into the four promotion choices.
func (b *Board) GenerateLegalMoves(color Color) []LegalMove {
	promoChoices := [4]PieceType{QueenType, RookType, BishopType, KnightType}

	var out []LegalMove
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			p := b.Grid[r][c]
			if p == nil || p.Color() != color {
				continue
			}
			from := Position{Row: r, Col: c}
			isPawn := p.Type() == PawnType
			for _, to := range b.LegalMoves(from) {
				if isPawn && (to.Row == 0 || to.Row == 7) {
					for _, promo := range promoChoices {
						out = append(out, LegalMove{From: from, To: to, Promotion: promo})
					}
				} else {
					out = append(out, LegalMove{From: from, To: to})
				}
			}
		}
	}
	return out
}

// HasAnyLegalMove reports whether `color` has at least one legal move. Used to
// distinguish checkmate/stalemate from an ongoing game.
func (b *Board) HasAnyLegalMove(color Color) bool {
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			p := b.Grid[r][c]
			if p == nil || p.Color() != color {
				continue
			}
			if len(b.LegalMoves(Position{Row: r, Col: c})) > 0 {
				return true
			}
		}
	}
	return false
}

func (b *Board) MovePiece(from, to Position, promo PieceType) error {
	piece := b.Grid[from.Row][from.Col]
	if piece == nil {
		return fmt.Errorf("no piece at source")
	}

	isLegal := false
	for _, move := range b.LegalMoves(from) {
		if move == to {
			isLegal = true
			break
		}
	}

	if !isLegal {
		return fmt.Errorf("invalid move for piece")
	}

	// Normalise the promotion choice: required (default queen) for a pawn
	// reaching the last rank, ignored otherwise.
	if piece.Type() == PawnType && (to.Row == 0 || to.Row == 7) {
		if promo != KnightType && promo != BishopType && promo != RookType && promo != QueenType {
			promo = QueenType
		}
	} else {
		promo = PawnType
	}

	b.applyMove(from, to, promo)

	b.PopulateValidMoves() // Update legal moves after the move

	return nil
}

func (b *Board) PopulateValidMoves() {
	// clear previous valid moves
	b.ValidMoves = make(map[Position][]Position)

	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			if b.Grid[r][c] == nil {
				continue
			}
			pos := Position{Row: r, Col: c}
			if moves := b.LegalMoves(pos); len(moves) > 0 {
				b.ValidMoves[pos] = moves
			}
		}
	}
}

// positionToString renders a board coordinate as algebraic notation, e.g. {1,4} -> "e2".
func positionToString(p Position) string {
	return fmt.Sprintf("%c%d", p.Col+'a', p.Row+1)
}

func (b *Board) MarshalJSON() ([]byte, error) {
	type cell struct {
		Color Color  `json:"color"`
		Type  string `json:"type"`
	}
	var grid [8][8]*cell

	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			if piece := b.Grid[r][c]; piece != nil {
				grid[r][c] = &cell{Color: piece.Color(), Type: piece.Type().String()}
			}
		}
	}

	validMoves := make(map[string][]string)
	for from, toList := range b.ValidMoves {
		key := positionToString(from)

		toStrList := make([]string, 0, len(toList))
		for _, to := range toList {
			toStrList = append(toStrList, positionToString(to))
		}
		validMoves[key] = toStrList
	}

	return json.Marshal(map[string]interface{}{
		"grid":        grid,
		"valid_moves": validMoves,
	})
}

// InsufficientMaterial reports whether neither side has enough material to force
// checkmate: K vs K, K vs K+minor, and K+B vs K+B with same-colored bishops.
func (b *Board) InsufficientMaterial() bool {
	type minor struct {
		pos Position
		t   PieceType
	}
	var minors []minor

	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			p := b.Grid[r][c]
			if p == nil {
				continue
			}
			switch p.Type() {
			case KingType:
				// kings are always present
			case BishopType, KnightType:
				minors = append(minors, minor{Position{r, c}, p.Type()})
			default:
				return false // a pawn, rook or queen can deliver mate
			}
		}
	}

	switch len(minors) {
	case 0, 1:
		return true // K vs K, or K + a single minor vs K
	case 2:
		// Two bishops standing on the same color square cannot force mate.
		if minors[0].t == BishopType && minors[1].t == BishopType {
			c0 := (minors[0].pos.Row + minors[0].pos.Col) % 2
			c1 := (minors[1].pos.Row + minors[1].pos.Col) % 2
			return c0 == c1
		}
		return false
	default:
		return false
	}
}

// NewBoardFromFEN builds a board from Forsyth–Edwards Notation and returns it
// along with the side to move. Used by tests today; also the foundation for the
// FEN import feature.
func NewBoardFromFEN(fen string) (Board, Color, error) {
	fields := strings.Fields(fen)
	if len(fields) < 4 {
		return Board{}, White, fmt.Errorf("invalid FEN: %q", fen)
	}

	var b Board
	b.FullmoveNumber = 1

	ranks := strings.Split(fields[0], "/")
	if len(ranks) != 8 {
		return Board{}, White, fmt.Errorf("invalid FEN board: %q", fields[0])
	}
	for i, rank := range ranks {
		row := 7 - i // FEN lists rank 8 first; Grid[7] is rank 8
		col := 0
		for j := 0; j < len(rank); j++ {
			ch := rank[j]
			if ch >= '1' && ch <= '8' {
				col += int(ch - '0')
				continue
			}
			if col > 7 {
				return Board{}, White, fmt.Errorf("invalid FEN rank: %q", rank)
			}
			p := pieceFromFEN(ch)
			if p == nil {
				return Board{}, White, fmt.Errorf("invalid FEN piece %q", string(ch))
			}
			b.Grid[row][col] = p
			col++
		}
	}

	turn := White
	if fields[1] == "b" {
		turn = Black
	}

	for _, ch := range fields[2] {
		switch ch {
		case 'K':
			b.CastlingRights.WhiteKingside = true
		case 'Q':
			b.CastlingRights.WhiteQueenside = true
		case 'k':
			b.CastlingRights.BlackKingside = true
		case 'q':
			b.CastlingRights.BlackQueenside = true
		}
	}

	if ep := fields[3]; ep != "-" && len(ep) == 2 {
		b.EnPassant = &Position{Row: int(ep[1] - '1'), Col: int(ep[0] - 'a')}
	}

	if len(fields) >= 5 {
		b.HalfmoveClock, _ = strconv.Atoi(fields[4])
	}
	if len(fields) >= 6 {
		if n, err := strconv.Atoi(fields[5]); err == nil {
			b.FullmoveNumber = n
		}
	}

	b.PopulateValidMoves()
	return b, turn, nil
}

// pieceFromFEN maps a FEN piece letter to a piece (uppercase = white).
func pieceFromFEN(ch byte) Piece {
	color := White
	lower := ch
	if ch >= 'a' && ch <= 'z' {
		color = Black
	} else {
		lower = ch + ('a' - 'A')
	}
	switch lower {
	case 'p':
		return &Pawn{color}
	case 'n':
		return &Knight{color}
	case 'b':
		return &Bishop{color}
	case 'r':
		return &Rook{color}
	case 'q':
		return &Queen{color}
	case 'k':
		return &King{color}
	}
	return nil
}

// placementFEN renders the piece-placement field of FEN (rank 8 down to 1).
func (b *Board) placementFEN() string {
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
	return sb.String()
}

// fenChar renders a piece as its FEN letter (uppercase = white).
func fenChar(p Piece) byte {
	var ch byte
	switch p.Type() {
	case PawnType:
		ch = 'p'
	case KnightType:
		ch = 'n'
	case BishopType:
		ch = 'b'
	case RookType:
		ch = 'r'
	case QueenType:
		ch = 'q'
	case KingType:
		ch = 'k'
	}
	if p.Color() == White {
		ch -= ('a' - 'A')
	}
	return ch
}
