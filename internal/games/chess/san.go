package games_chess

import "strings"

// moveSAN renders a move in Standard Algebraic Notation, without the trailing
// check ("+") or checkmate ("#") marker — the caller appends that after the move
// is applied. `b` is the position *before* the move.
func moveSAN(b *Board, from, to Position, promo PieceType) string {
	piece := b.Grid[from.Row][from.Col]
	if piece == nil {
		return ""
	}

	// Castling.
	if piece.Type() == KingType && abs(to.Col-from.Col) == 2 {
		if to.Col == 6 {
			return "O-O"
		}
		return "O-O-O"
	}

	capture := b.Grid[to.Row][to.Col] != nil
	if piece.Type() == PawnType && from.Col != to.Col && b.Grid[to.Row][to.Col] == nil {
		capture = true // en passant
	}

	var sb strings.Builder

	if piece.Type() == PawnType {
		if capture {
			sb.WriteByte(byte('a' + from.Col)) // origin file
			sb.WriteByte('x')
		}
		sb.WriteString(positionToString(to))
		if promo != PawnType && (to.Row == 0 || to.Row == 7) {
			sb.WriteByte('=')
			sb.WriteByte(pieceLetter(promo))
		}
		return sb.String()
	}

	sb.WriteByte(pieceLetter(piece.Type()))
	sb.WriteString(disambiguation(b, piece, from, to))
	if capture {
		sb.WriteByte('x')
	}
	sb.WriteString(positionToString(to))
	return sb.String()
}

func pieceLetter(t PieceType) byte {
	switch t {
	case KnightType:
		return 'N'
	case BishopType:
		return 'B'
	case RookType:
		return 'R'
	case QueenType:
		return 'Q'
	case KingType:
		return 'K'
	}
	return '?'
}

// disambiguation returns the minimal file/rank/both hint needed when another
// same-type piece of the same color could also legally move to `to`.
func disambiguation(b *Board, piece Piece, from, to Position) string {
	var others []Position
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			if r == from.Row && c == from.Col {
				continue
			}
			p := b.Grid[r][c]
			if p == nil || p.Type() != piece.Type() || p.Color() != piece.Color() {
				continue
			}
			for _, m := range b.LegalMoves(Position{Row: r, Col: c}) {
				if m == to {
					others = append(others, Position{Row: r, Col: c})
					break
				}
			}
		}
	}
	if len(others) == 0 {
		return ""
	}

	sameFile, sameRank := false, false
	for _, o := range others {
		if o.Col == from.Col {
			sameFile = true
		}
		if o.Row == from.Row {
			sameRank = true
		}
	}
	switch {
	case !sameFile:
		return string(byte('a' + from.Col))
	case !sameRank:
		return string(byte('1' + from.Row))
	default:
		return positionToString(from)
	}
}
