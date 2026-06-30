package games_chess

type King struct {
	color Color
}

// Fulfilling the interface
func (k *King) Color() Color    { return k.color }
func (k *King) Type() PieceType { return KingType }
func (k *King) ToString() string {
	if k.color == White {
		return "♔"
	}
	return "♚"
}

// The Movement Logic
func (k *King) ValidMoves(board *Board, pos Position) []Position {
	var moves []Position

	// Directions a king can move: one square in any direction
	directions := []Position{
		{-1, -1}, {-1, 0}, {-1, 1},
		{0, -1}, {0, 1},
		{1, -1}, {1, 0}, {1, 1},
	}

	for _, dir := range directions {
		newRow := pos.Row + dir.Row
		newCol := pos.Col + dir.Col

		// Ask the board if this is a legal square to land on
		if board.IsValidDestination(newRow, newCol, k.color) {
			moves = append(moves, Position{newRow, newCol})
		}
	}

	moves = append(moves, k.castlingMoves(board, pos)...)

	return moves
}

// castlingMoves returns the king's two-square castling destinations that are
// currently legal: the side still has the right, the squares between king and
// rook are empty, and the king neither starts in, passes through, nor lands on
// an attacked square.
func (k *King) castlingMoves(board *Board, pos Position) []Position {
	row := 0
	kingside, queenside := board.CastlingRights.WhiteKingside, board.CastlingRights.WhiteQueenside
	if k.color == Black {
		row = 7
		kingside, queenside = board.CastlingRights.BlackKingside, board.CastlingRights.BlackQueenside
	}

	// The king must be on its home square, with at least one right intact.
	if pos.Row != row || pos.Col != 4 || (!kingside && !queenside) {
		return nil
	}

	opp := k.color.Opponent()
	if board.IsAttacked(Position{row, 4}, opp) {
		return nil // cannot castle out of check
	}

	var moves []Position
	// King-side: f and g empty; e, f, g unattacked.
	if kingside &&
		board.Grid[row][5] == nil && board.Grid[row][6] == nil &&
		!board.IsAttacked(Position{row, 5}, opp) && !board.IsAttacked(Position{row, 6}, opp) {
		moves = append(moves, Position{row, 6})
	}
	// Queen-side: b, c and d empty; e, d, c unattacked.
	if queenside &&
		board.Grid[row][1] == nil && board.Grid[row][2] == nil && board.Grid[row][3] == nil &&
		!board.IsAttacked(Position{row, 3}, opp) && !board.IsAttacked(Position{row, 2}, opp) {
		moves = append(moves, Position{row, 2})
	}
	return moves
}
