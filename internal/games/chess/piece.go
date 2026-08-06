package games_chess

import "encoding/json"

type Color int

const (
	White Color = iota
	Black
)

// String renders the color using the wire format consumed by the frontend.
func (c Color) String() string {
	if c == Black {
		return "black"
	}
	return "white"
}

// MarshalJSON keeps the JSON contract as "white"/"black" even though the
// engine works with the Color enum internally.
func (c Color) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.String())
}

// Opponent returns the opposing color.
func (c Color) Opponent() Color {
	if c == White {
		return Black
	}
	return White
}

// ColorFromString parses the wire representation back into a Color.
func ColorFromString(s string) (Color, bool) {
	switch s {
	case "white":
		return White, true
	case "black":
		return Black, true
	}
	return White, false
}

// PieceType identifies the kind of a piece without resorting to type switches.
type PieceType int

const (
	PawnType PieceType = iota
	KnightType
	BishopType
	RookType
	QueenType
	KingType
)

// String renders the piece type using the wire format consumed by the frontend.
func (t PieceType) String() string {
	switch t {
	case PawnType:
		return "pawn"
	case KnightType:
		return "knight"
	case BishopType:
		return "bishop"
	case RookType:
		return "rook"
	case QueenType:
		return "queen"
	case KingType:
		return "king"
	}
	return ""
}

// PieceTypeFromString parses a promotion choice from the wire. It only accepts
// the legal promotion targets; everything else (including "pawn"/"king"/"")
// reports false so callers can apply their own default.
func PieceTypeFromString(s string) (PieceType, bool) {
	switch s {
	case "queen":
		return QueenType, true
	case "rook":
		return RookType, true
	case "bishop":
		return BishopType, true
	case "knight":
		return KnightType, true
	}
	return PawnType, false
}

type Position struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type Piece interface {
	Color() Color
	Type() PieceType

	ValidMoves(board *Board, pos Position) []Position
	ToString() string
}
