package games_chess

type Color int

const (
	White Color = iota
	Black
)

type Position struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type Piece interface {
	Color() Color

	ValidMoves(board *Board, pos Position) []Position
	ToString() string
}
