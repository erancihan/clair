package games_chess

type Pawn struct {
	color Color
}

// Fulfilling the interface
func (p *Pawn) Color() Color    { return p.color }
func (p *Pawn) Type() PieceType { return PawnType }
func (p *Pawn) ToString() string {
	if p.color == White {
		return "♙"
	}
	return "♟"
}

// The Movement Logic
func (p *Pawn) ValidMoves(board *Board, pos Position) []Position {
	var moves []Position

	direction := 1
	startRow := 1
	if p.color == Black {
		direction = -1
		startRow = 6
	}

	// Move forward one square: only onto an empty square (pawns never capture
	// straight ahead).
	oneRow := pos.Row + direction
	if board.InBounds(oneRow, pos.Col) && board.Grid[oneRow][pos.Col] == nil {
		moves = append(moves, Position{oneRow, pos.Col})

		// Move forward two squares from the starting rank: both the square
		// being passed over and the destination must be empty.
		twoRow := pos.Row + 2*direction
		if pos.Row == startRow && board.InBounds(twoRow, pos.Col) && board.Grid[twoRow][pos.Col] == nil {
			moves = append(moves, Position{twoRow, pos.Col})
		}
	}

	// Capture diagonally: only when an enemy piece occupies the target square.
	for _, colOffset := range []int{-1, 1} {
		newCol := pos.Col + colOffset
		if !board.InBounds(oneRow, newCol) {
			continue
		}
		target := board.Grid[oneRow][newCol]
		if target != nil && target.Color() != p.color {
			moves = append(moves, Position{oneRow, newCol})
		}
		// En passant target is tracked on the board (Board.EnPassant). Move
		// generation and the accompanying capture are implemented in a later
		// phase; the field is consulted here only when set.
		if board.EnPassant != nil && board.EnPassant.Row == oneRow && board.EnPassant.Col == newCol {
			moves = append(moves, Position{oneRow, newCol})
		}
	}

	return moves
}
