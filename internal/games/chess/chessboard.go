package games_chess

import (
	"encoding/json"
	"fmt"
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
// generation (perft tests, future AI).
type LegalMove struct {
	From Position
	To   Position
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

// applyMove relocates the piece from->to without any legality checking. It is
// meant to be called on a copy of the board (nb := *b) when probing move
// legality or enumerating positions.
//
// NOTE: en passant capture removal, castling rook relocation, promotion and the
// FEN-state bookkeeping are added alongside those rules in a later phase.
func (b *Board) applyMove(from, to Position) {
	b.Grid[to.Row][to.Col] = b.Grid[from.Row][from.Col]
	b.Grid[from.Row][from.Col] = nil
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
		nb.applyMove(from, to)
		if !nb.InCheck(color) {
			legal = append(legal, to)
		}
	}
	return legal
}

// GenerateLegalMoves returns every legal move available to `color`.
func (b *Board) GenerateLegalMoves(color Color) []LegalMove {
	var out []LegalMove
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			p := b.Grid[r][c]
			if p == nil || p.Color() != color {
				continue
			}
			from := Position{Row: r, Col: c}
			for _, to := range b.LegalMoves(from) {
				out = append(out, LegalMove{From: from, To: to})
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

func (b *Board) MovePiece(from, to Position) error {
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

	b.applyMove(from, to)

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
