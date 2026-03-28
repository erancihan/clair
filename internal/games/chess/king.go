package games_chess

type King struct {
	color Color
}

// Fulfilling the interface
func (k *King) Color() Color { return k.color }
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

	return moves
}
