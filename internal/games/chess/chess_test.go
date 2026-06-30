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
		nb.applyMove(m.From, m.To, m.Promotion)
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
	if err := g.MakeMove("white", "e7", "e6", ""); err == nil {
		t.Error("expected error when moving an opponent's piece, got nil")
	}
	if g.State.Turn != White {
		t.Errorf("turn should remain White after a rejected move, got %v", g.State.Turn)
	}
}

func TestMakeMoveHappyPathSwitchesTurn(t *testing.T) {
	g, _ := NewGame(TypePvP)
	g.State.Status = StatusOngoing

	if err := g.MakeMove("white", "e2", "e4", ""); err != nil {
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

// --- Phase 1: special-move perft -------------------------------------------

// Standard perft positions whose reference node counts exercise castling, en
// passant (including the en-passant-discovered-check edge case) and promotion.
const (
	kiwipeteFEN  = "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1"
	position3FEN = "8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1"
	position5FEN = "rnbq1k1r/pp1Pbppp/2p5/8/2B5/8/PPP1NnPP/RNBQK2R w KQ - 1 8"
)

func TestPerftSpecialPositions(t *testing.T) {
	cases := []struct {
		name  string
		fen   string
		depth int
		want  int
		slow  bool
	}{
		{"kiwipete d3 (castling)", kiwipeteFEN, 3, 97862, false},
		{"position3 d4 (en passant)", position3FEN, 4, 43238, false},
		{"position5 d3 (promotion)", position5FEN, 3, 62379, false},
		{"kiwipete d4", kiwipeteFEN, 4, 4085603, true},
	}

	for _, tc := range cases {
		if tc.slow && testing.Short() {
			t.Logf("skipping %s in -short mode", tc.name)
			continue
		}
		board, turn, err := NewBoardFromFEN(tc.fen)
		if err != nil {
			t.Fatalf("%s: parse FEN: %v", tc.name, err)
		}
		if got := perft(&board, turn, tc.depth); got != tc.want {
			t.Errorf("%s: perft = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// --- Phase 1: special moves ------------------------------------------------

func TestCastlingMovesRook(t *testing.T) {
	board, _, err := NewBoardFromFEN("4k3/8/8/8/8/8/8/4K2R w K - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := board.MovePiece(mustPos("e1"), mustPos("g1"), PawnType); err != nil {
		t.Fatalf("kingside castling should be legal: %v", err)
	}
	if p := board.Grid[mustPos("g1").Row][mustPos("g1").Col]; p == nil || p.Type() != KingType {
		t.Error("king should be on g1 after castling")
	}
	if p := board.Grid[mustPos("f1").Row][mustPos("f1").Col]; p == nil || p.Type() != RookType {
		t.Error("rook should have moved to f1")
	}
	if board.Grid[mustPos("h1").Row][mustPos("h1").Col] != nil {
		t.Error("h1 should be empty after castling")
	}
}

func TestCannotCastleThroughCheck(t *testing.T) {
	// Black rook on f8 attacks f1, the square the white king would pass over.
	board, _, err := NewBoardFromFEN("4kr2/8/8/8/8/8/8/4K2R w K - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if containsPos(board.LegalMoves(mustPos("e1")), "g1") {
		t.Error("king must not castle through the attacked f1 square")
	}
}

func TestEnPassantCapture(t *testing.T) {
	// Black has just played d7-d5; white pawn e5 captures en passant onto d6.
	board, _, err := NewBoardFromFEN("4k3/8/8/3pP3/8/8/8/4K3 w - d6 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := board.MovePiece(mustPos("e5"), mustPos("d6"), PawnType); err != nil {
		t.Fatalf("en passant should be legal: %v", err)
	}
	if p := board.Grid[mustPos("d6").Row][mustPos("d6").Col]; p == nil || p.Type() != PawnType {
		t.Error("capturing pawn should be on d6")
	}
	if board.Grid[mustPos("d5").Row][mustPos("d5").Col] != nil {
		t.Error("the captured pawn on d5 should be removed")
	}
}

func TestPromotionToQueen(t *testing.T) {
	board, _, err := NewBoardFromFEN("4k3/P7/8/8/8/8/8/4K3 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := board.MovePiece(mustPos("a7"), mustPos("a8"), QueenType); err != nil {
		t.Fatalf("promotion should be legal: %v", err)
	}
	p := board.Grid[mustPos("a8").Row][mustPos("a8").Col]
	if p == nil || p.Type() != QueenType || p.Color() != White {
		t.Errorf("a8 should hold a white queen, got %v", p)
	}
}

// --- Phase 1: draws --------------------------------------------------------

func TestInsufficientMaterial(t *testing.T) {
	cases := []struct {
		fen  string
		want bool
	}{
		{"4k3/8/8/8/8/8/8/4K3 w - - 0 1", true},   // K vs K
		{"4k3/8/8/8/8/8/8/4KB2 w - - 0 1", true},  // K+B vs K
		{"4k3/8/8/8/8/8/8/4KN2 w - - 0 1", true},  // K+N vs K
		{"4k3/8/8/8/8/8/8/3QK3 w - - 0 1", false}, // K+Q vs K
		{"4k3/8/8/8/8/8/8/R3K3 w - - 0 1", false}, // K+R vs K
	}
	for _, tc := range cases {
		board, _, err := NewBoardFromFEN(tc.fen)
		if err != nil {
			t.Fatal(err)
		}
		if got := board.InsufficientMaterial(); got != tc.want {
			t.Errorf("InsufficientMaterial(%q) = %v, want %v", tc.fen, got, tc.want)
		}
	}
}

func TestFiftyMoveRuleDraw(t *testing.T) {
	board, _, err := NewBoardFromFEN("4k3/8/8/8/8/8/8/R3K3 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	board.HalfmoveClock = 100
	g := &Game{State: GameState{Board: board, Turn: White, Status: StatusOngoing}, history: map[string]int{}}
	g.updateStatusLocked()
	if g.State.Status != StatusDraw {
		t.Errorf("expected draw by the fifty-move rule, got %q", g.State.Status)
	}
}

func TestThreefoldRepetitionDraw(t *testing.T) {
	g, _ := NewGame(TypePvP)
	g.State.Status = StatusOngoing

	// Shuffling both knights out and back returns to the start position; two
	// full cycles make it the third occurrence.
	cycle := [...]struct{ player, from, to string }{
		{"white", "g1", "f3"}, {"black", "g8", "f6"},
		{"white", "f3", "g1"}, {"black", "f6", "g8"},
	}
	for i := 0; i < 2; i++ {
		for _, m := range cycle {
			if err := g.MakeMove(m.player, m.from, m.to, ""); err != nil {
				t.Fatalf("%s-%s: %v", m.from, m.to, err)
			}
		}
	}
	if g.State.Status != StatusDraw {
		t.Errorf("expected draw by threefold repetition, got %q", g.State.Status)
	}
}
