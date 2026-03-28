package games_chess

type Bishop struct {
	color Color
}

// Fulfilling the interface
func (b *Bishop) Color() Color { return b.color }
func (b *Bishop) ToString() string {
	if b.color == White {
		return "♗"
	}
	return "♝"
}

// The Movement Logic
func (b *Bishop) ValidMoves(board *Board, pos Position) []Position {
	var moves []Position

	// Directions a bishop can move: diagonals
	directions := []Position{
		{-1, -1}, {-1, 1}, {1, -1}, {1, 1},
	}

	for _, dir := range directions {
		for i := 1; i < 8; i++ {
			newRow := pos.Row + dir.Row*i
			newCol := pos.Col + dir.Col*i

			// Ask the board if this is a legal square to land on
			if board.IsValidDestination(newRow, newCol, b.color) {
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
