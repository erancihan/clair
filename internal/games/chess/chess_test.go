package games_chess

import "testing"

// --- helpers ---------------------------------------------------------------

func mustPos(sq string) Position {
	return Position{Row: int(sq[1] - '1'), Col: int(sq[0] - 'a')}
}

func place(b *Board, sq string, p Piece) {
	pos := mustPos(sq)
	b.Grid[pos.Row][pos.Col] = p
}

func containsPos(moves []Position, sq string) bool {
	want := mustPos(sq)
	for _, m := range moves {
		if m == want {
			return true
		}
	}
	return false
}

// perft counts the number of leaf nodes in the legal-move tree to the given
// depth. It is the standard correctness check for chess move generators: if the
// counts match the known reference values, pseudo-legal generation, capture
// handling and check filtering are all behaving.
func perft(b *Board, side Color, depth int) int {
	if depth == 0 {
		return 1
	}
	moves := b.GenerateLegalMoves(side)
	if depth == 1 {
		return len(moves)
	}
	nodes := 0
	for _, m := range moves {
		nb := *b
		nb.applyMove(m.From, m.To)
		nodes += perft(&nb, side.Opponent(), depth-1)
	}
	return nodes
}

// --- perft -----------------------------------------------------------------

func TestPerftStartingPosition(t *testing.T) {
	// Reference values for the standard starting position. Castling, en passant
	// and promotion do not occur within these depths, so they hold for the
	// Phase 0 engine. (En passant first appears at depth 5.)
	cases := []struct {
		depth int
		nodes int
	}{
		{1, 20},
		{2, 400},
		{3, 8902},
		{4, 197281},
	}

	for _, tc := range cases {
		if tc.depth >= 4 && testing.Short() {
			t.Logf("skipping perft depth %d in -short mode", tc.depth)
			continue
		}
		board := NewBoard()
		got := perft(&board, White, tc.depth)
		if got != tc.nodes {
			t.Errorf("perft(%d) = %d, want %d", tc.depth, got, tc.nodes)
		}
	}
}

// --- pawn rules ------------------------------------------------------------

func TestPawnForwardAndDoubleStep(t *testing.T) {
	b := &Board{}
	place(b, "a2", &Pawn{White})

	moves := b.LegalMoves(mustPos("a2"))
	if len(moves) != 2 || !containsPos(moves, "a3") || !containsPos(moves, "a4") {
		t.Fatalf("pawn on a2 should reach a3 and a4, got %v", moves)
	}
}

func TestPawnBlockedOneSquareAhead(t *testing.T) {
	b := &Board{}
	place(b, "a2", &Pawn{White})
	place(b, "a3", &Pawn{Black}) // directly blocking

	if moves := b.LegalMoves(mustPos("a2")); len(moves) != 0 {
		t.Fatalf("pawn blocked directly ahead should have no moves, got %v", moves)
	}
}

func TestPawnCannotDoubleStepThroughPiece(t *testing.T) {
	b := &Board{}
	place(b, "a2", &Pawn{White})
	place(b, "a4", &Pawn{Black}) // two ahead: blocks the double step only

	moves := b.LegalMoves(mustPos("a2"))
	if len(moves) != 1 || !containsPos(moves, "a3") {
		t.Fatalf("pawn should only advance to a3, got %v", moves)
	}
}

func TestPawnCannotCaptureForward(t *testing.T) {
	b := &Board{}
	place(b, "e4", &Pawn{White})
	place(b, "e5", &Pawn{Black}) // enemy directly ahead

	if moves := b.LegalMoves(mustPos("e4")); len(moves) != 0 {
		t.Fatalf("pawn must not capture straight ahead, got %v", moves)
	}
}

func TestPawnCapturesDiagonally(t *testing.T) {
	b := &Board{}
	place(b, "d4", &Pawn{White})
	place(b, "e5", &Pawn{Black}) // diagonal capture target

	moves := b.LegalMoves(mustPos("d4"))
	if !containsPos(moves, "e5") {
		t.Errorf("pawn on d4 should be able to capture e5, got %v", moves)
	}
	if !containsPos(moves, "d5") {
		t.Errorf("pawn on d4 should be able to advance to d5, got %v", moves)
	}
	if containsPos(moves, "c5") {
		t.Errorf("pawn on d4 should not move to empty c5, got %v", moves)
	}
}

// --- king safety -----------------------------------------------------------

func TestKingCannotMoveIntoCheck(t *testing.T) {
	b := &Board{}
	place(b, "e1", &King{White})
	place(b, "d8", &Rook{Black}) // controls the entire d-file

	moves := b.LegalMoves(mustPos("e1"))
	if containsPos(moves, "d1") || containsPos(moves, "d2") {
		t.Errorf("king must not step onto the attacked d-file, got %v", moves)
	}
	if !containsPos(moves, "e2") {
		t.Errorf("king should be able to move to the safe e2 square, got %v", moves)
	}
}

func TestPinnedPieceCannotExposeKing(t *testing.T) {
	b := &Board{}
	place(b, "e1", &King{White})
	place(b, "e2", &Knight{White}) // pinned to the king along the e-file
	place(b, "e8", &Rook{Black})

	if moves := b.LegalMoves(mustPos("e2")); len(moves) != 0 {
		t.Fatalf("absolutely pinned knight should have no legal moves, got %v", moves)
	}
}

// --- game termination ------------------------------------------------------

func TestCheckmateDetection(t *testing.T) {
	// Black king on h8 is mated by the white queen on g7, defended by the white
	// king on f6. Black is to move.
	b := &Board{}
	place(b, "h8", &King{Black})
	place(b, "g7", &Queen{White})
	place(b, "f6", &King{White})

	if !b.InCheck(Black) {
		t.Fatal("black king should be in check")
	}
	if b.HasAnyLegalMove(Black) {
		t.Fatal("black should have no legal moves (checkmate)")
	}

	g := &Game{State: GameState{Board: *b, Turn: Black, Status: StatusOngoing}}
	g.updateStatusLocked()
	if g.State.Status != StatusWhiteWins {
		t.Errorf("expected White Wins on checkmate, got %q", g.State.Status)
	}
}

func TestStalemateDetection(t *testing.T) {
	// Black king on h8 has no legal move but is not in check: stalemate.
	b := &Board{}
	place(b, "h8", &King{Black})
	place(b, "f7", &King{White})
	place(b, "g6", &Queen{White})

	if b.InCheck(Black) {
		t.Fatal("black king should not be in check in a stalemate")
	}
	if b.HasAnyLegalMove(Black) {
		t.Fatal("black should have no legal moves (stalemate)")
	}

	g := &Game{State: GameState{Board: *b, Turn: Black, Status: StatusOngoing}}
	g.updateStatusLocked()
	if g.State.Status != StatusDraw {
		t.Errorf("expected Draw on stalemate, got %q", g.State.Status)
	}
}

// --- end-to-end move flow --------------------------------------------------

func TestMakeMoveRejectsMovingOpponentPiece(t *testing.T) {
	g, _ := NewGame(TypePvP)
	g.State.Status = StatusOngoing // skip the lobby wait

	// White to move tries to push a black pawn (e7-e6).
	if err := g.MakeMove("white", "e7", "e6"); err == nil {
		t.Error("expected error when moving an opponent's piece, got nil")
	}
	if g.State.Turn != White {
		t.Errorf("turn should remain White after a rejected move, got %v", g.State.Turn)
	}
}

func TestMakeMoveHappyPathSwitchesTurn(t *testing.T) {
	g, _ := NewGame(TypePvP)
	g.State.Status = StatusOngoing

	if err := g.MakeMove("white", "e2", "e4"); err != nil {
		t.Fatalf("e2-e4 should be legal, got %v", err)
	}
	if g.State.Turn != Black {
		t.Errorf("turn should pass to Black after White moves, got %v", g.State.Turn)
	}
	// The white pawn should now be on e4 and e2 empty.
	if p := g.State.Board.Grid[mustPos("e4").Row][mustPos("e4").Col]; p == nil || p.Type() != PawnType {
		t.Error("expected a pawn on e4 after e2-e4")
	}
	if g.State.Board.Grid[mustPos("e2").Row][mustPos("e2").Col] != nil {
		t.Error("expected e2 to be empty after e2-e4")
	}
}
