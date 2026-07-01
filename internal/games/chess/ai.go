package games_chess

import "sort"

// Material values in centipawns.
const (
	pawnVal   = 100
	knightVal = 320
	bishopVal = 330
	rookVal   = 500
	queenVal  = 900
	kingVal   = 20000
	mateScore = 1_000_000
)

func pieceValue(t PieceType) int {
	switch t {
	case PawnType:
		return pawnVal
	case KnightType:
		return knightVal
	case BishopType:
		return bishopVal
	case RookType:
		return rookVal
	case QueenType:
		return queenVal
	case KingType:
		return kingVal
	}
	return 0
}

// evaluate returns the material balance from White's perspective (centipawns).
func evaluate(b *Board) int {
	score := 0
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			p := b.Grid[r][c]
			if p == nil {
				continue
			}
			v := pieceValue(p.Type())
			if p.Color() == White {
				score += v
			} else {
				score -= v
			}
		}
	}
	return score
}

func evalFor(b *Board, color Color) int {
	s := evaluate(b)
	if color == Black {
		return -s
	}
	return s
}

// bestMove chooses a move for `color` via fixed-depth alpha-beta (negamax)
// search. The bool is false when there are no legal moves.
func bestMove(b *Board, color Color, depth int) (LegalMove, bool) {
	moves := b.GenerateLegalMoves(color)
	if len(moves) == 0 {
		return LegalMove{}, false
	}
	orderMoves(b, moves)

	best := moves[0]
	bestScore := -mateScore - 1
	alpha, beta := -mateScore-1, mateScore+1
	for _, m := range moves {
		nb := *b
		nb.applyMove(m.From, m.To, m.Promotion)
		score := -negamax(&nb, color.Opponent(), depth-1, -beta, -alpha)
		if score > bestScore {
			bestScore = score
			best = m
		}
		if score > alpha {
			alpha = score
		}
	}
	return best, true
}

func negamax(b *Board, color Color, depth, alpha, beta int) int {
	if depth == 0 {
		return evalFor(b, color)
	}

	moves := b.GenerateLegalMoves(color)
	if len(moves) == 0 {
		if b.InCheck(color) {
			return -mateScore - depth // checkmated; nearer mates score worse
		}
		return 0 // stalemate
	}
	orderMoves(b, moves)

	best := -mateScore - 1
	for _, m := range moves {
		nb := *b
		nb.applyMove(m.From, m.To, m.Promotion)
		score := -negamax(&nb, color.Opponent(), depth-1, -beta, -alpha)
		if score > best {
			best = score
		}
		if best > alpha {
			alpha = best
		}
		if alpha >= beta {
			break // beta cutoff
		}
	}
	return best
}

// orderMoves puts captures first (most valuable victim first) to improve
// alpha-beta pruning.
func orderMoves(b *Board, moves []LegalMove) {
	sort.SliceStable(moves, func(i, j int) bool {
		return captureValue(b, moves[i]) > captureValue(b, moves[j])
	})
}

func captureValue(b *Board, m LegalMove) int {
	if t := b.Grid[m.To.Row][m.To.Col]; t != nil {
		return pieceValue(t.Type())
	}
	return 0
}
