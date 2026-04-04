package games_chess

type Queen struct {
	color Color
}

// Fulfilling the interface
func (q *Queen) Color() Color { return q.color }
func (q *Queen) ToString() string {
	if q.color == White {
		return "♕"
	}
	return "♛"
}

// The Movement Logic
func (q *Queen) ValidMoves(board *Board, pos Position) []Position {
	var moves []Position

	// Directions a queen can move: straight and diagonals
	directions := []Position{
		{-1, 0}, {1, 0}, {0, -1}, {0, 1}, // Straight
		{-1, -1}, {-1, 1}, {1, -1}, {1, 1}, // Diagonal
	}

	for _, dir := range directions {
		for i := 1; i < 8; i++ {
			newRow := pos.Row + dir.Row*i
			newCol := pos.Col + dir.Col*i

			// Ask the board if this is a legal square to land on
			if board.IsValidDestination(newRow, newCol, q.color) {
				moves = append(moves, Position{newRow, newCol})
				// If there's a piece in the way, we can't move further in this direction
				if board.Grid[newRow][newCol] != nil {
					break
				}
			} else {
				break // Out of bounds or blocked by own piece
			}
		}
	}

	return moves
}
