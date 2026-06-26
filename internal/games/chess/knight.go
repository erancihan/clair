package games_chess

// Knight struct
type Knight struct {
	color Color
}

// Fulfilling the interface
func (k *Knight) Color() Color    { return k.color }
func (k *Knight) Type() PieceType { return KnightType }
func (k *Knight) ToString() string {
	if k.color == White {
		return "♘"
	}
	return "♞"
}

// The Movement Logic
func (k *Knight) ValidMoves(b *Board, pos Position) []Position {
	var moves []Position

	// The 8 possible L-shapes a knight can make
	jumps := []Position{
		{2, 1}, {2, -1}, {-2, 1}, {-2, -1},
		{1, 2}, {1, -2}, {-1, 2}, {-1, -2},
	}

	for _, jump := range jumps {
		newRow := pos.Row + jump.Row
		newCol := pos.Col + jump.Col

		// Ask the board if this is a legal square to land on
		if b.IsValidDestination(newRow, newCol, k.color) {
			moves = append(moves, Position{newRow, newCol})
		}
	}

	return moves
}
