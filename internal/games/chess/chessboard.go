package games_chess

import (
	"encoding/json"
	"fmt"
)

type Board struct {
	Grid       [8][8]Piece
	ValidMoves map[Position][]Position // Cache for valid moves of pieces
}

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
		ValidMoves: nil,
	}

	b.PopulateValidMoves() // Populate valid moves for all pieces at the start

	return b
}

func (b *Board) IsValidDestination(row, col int, color Color) bool {
	if row < 0 || row >= 8 || col < 0 || col >= 8 {
		return false // Out of bounds
	}

	target := b.Grid[row][col]
	if target == nil {
		return true // Empty square
	}

	return target.Color() != color
}

func (b *Board) MovePiece(from, to Position) error {
	piece := b.Grid[from.Row][from.Col]
	if piece == nil {
		return fmt.Errorf("no piece at source")
	}

	validMoves := piece.ValidMoves(b, from)
	isValid := false
	for _, move := range validMoves {
		if move == to {
			isValid = true
			break
		}
	}

	if !isValid {
		return fmt.Errorf("invalid move for piece")
	}

	b.Grid[to.Row][to.Col] = piece
	b.Grid[from.Row][from.Col] = nil

	b.PopulateValidMoves() // Update valid moves after the move

	return nil
}

func (b *Board) PopulateValidMoves() {
	// clear previous valid moves
	b.ValidMoves = make(map[Position][]Position)

	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			piece := b.Grid[r][c]
			if piece != nil {
				pos := Position{Row: r, Col: c}
				b.ValidMoves[pos] = piece.ValidMoves(b, pos)
			}
		}
	}
}

func (b *Board) MarshalJSON() ([]byte, error) {
	var grid [8][8]*struct {
		Color string `json:"color"`
		Type  string `json:"type"`
	}

	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			if piece := b.Grid[r][c]; piece != nil {
				color := "white"
				if piece.Color() == Black {
					color = "black"
				}

				pieceType := ""
				switch piece.(type) {
				case *Pawn:
					pieceType = "pawn"
				case *Rook:
					pieceType = "rook"
				case *Knight:
					pieceType = "knight"
				case *Bishop:
					pieceType = "bishop"
				case *Queen:
					pieceType = "queen"
				case *King:
					pieceType = "king"
				}

				grid[r][c] = &struct {
					Color string `json:"color"`
					Type  string `json:"type"`
				}{Color: color, Type: pieceType}
			}
		}
	}

	validMoves := make(map[string][]string)
	for from, toList := range b.ValidMoves {
		key := fmt.Sprintf("%c%d", from.Col+'a', from.Row+1)

		var toStrList []string
		for _, to := range toList {
			toStrList = append(toStrList, fmt.Sprintf("%c%d", to.Col+'a', to.Row+1))
		}
		validMoves[key] = toStrList
	}

	return json.Marshal(map[string]interface{}{
		"grid":        grid,
		"valid_moves": validMoves,
	})
}
