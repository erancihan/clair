package games_chess

type Pawn struct {
	color Color
}

// Fulfilling the interface
func (p *Pawn) Color() Color { return p.color }
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

	// Move forward one square
	if board.IsValidDestination(pos.Row+direction, pos.Col, p.color) {
		moves = append(moves, Position{pos.Row + direction, pos.Col})

		// Move forward two squares from starting position
		if pos.Row == startRow && board.IsValidDestination(pos.Row+2*direction, pos.Col, p.color) {
			moves = append(moves, Position{pos.Row + 2*direction, pos.Col})
		}
	}

	// Capture diagonally
	for _, colOffset := range []int{-1, 1} {
		newCol := pos.Col + colOffset
		if board.IsValidDestination(pos.Row+direction, newCol, p.color) && board.Grid[pos.Row+direction][newCol] != nil {
			moves = append(moves, Position{pos.Row + direction, newCol})
		}
	}

	// TODO: handle promotion
	// TODO: promotion
	// en-passant and promotion logic can be added here in the future

	return moves
}
