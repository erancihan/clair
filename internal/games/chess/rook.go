package games_chess

type Rook struct {
	color Color
}

// Fulfilling the interface
func (r *Rook) Color() Color { return r.color }
func (r *Rook) ToString() string {
	if r.color == White {
		return "♖"
	}
	return "♜"
}

// The Movement Logic
func (r *Rook) ValidMoves(b *Board, pos Position) []Position {
	var moves []Position

	// Directions a rook can move: up, down, left, right
	directions := []Position{
		{-1, 0}, {1, 0}, {0, -1}, {0, 1},
	}

	for _, dir := range directions {
		for i := 1; i < 8; i++ {
			newRow := pos.Row + dir.Row*i
			newCol := pos.Col + dir.Col*i

			// Ask the board if this is a legal square to land on
			if b.IsValidDestination(newRow, newCol, r.color) {
				moves = append(moves, Position{newRow, newCol})
				// If there's a piece in the way, we can't move further in this direction
				if b.Grid[newRow][newCol] != nil {
					break
				}
			} else {
				break // Out of bounds or blocked by own piece
			}
		}
	}

	return moves
}
