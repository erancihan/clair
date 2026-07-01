package games_chess

import (
	"strings"
	"testing"
	"time"
)

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

// --- Phase 1: resign & draw offers -----------------------------------------

func TestResignAwardsOpponent(t *testing.T) {
	g, _ := NewGame(TypePvP)
	g.State.Status = StatusOngoing

	if err := g.Resign("white"); err != nil {
		t.Fatalf("resign: %v", err)
	}
	if g.State.Status != StatusBlackWins {
		t.Errorf("white resigning should give Black Wins, got %q", g.State.Status)
	}
}

func TestDrawOfferAndAccept(t *testing.T) {
	g, _ := NewGame(TypePvP)
	g.State.Status = StatusOngoing

	if err := g.OfferDraw("white"); err != nil {
		t.Fatalf("offer: %v", err)
	}
	if g.State.DrawOfferedBy == nil || *g.State.DrawOfferedBy != White {
		t.Fatal("expected an outstanding draw offer from white")
	}

	// A player must not be able to accept their own offer.
	if err := g.AcceptDraw("white"); err != nil {
		t.Fatal(err)
	}
	if g.State.Status != StatusOngoing {
		t.Error("a player must not accept their own draw offer")
	}

	// The opponent accepts.
	if err := g.AcceptDraw("black"); err != nil {
		t.Fatal(err)
	}
	if g.State.Status != StatusDraw {
		t.Errorf("expected Draw after black accepts, got %q", g.State.Status)
	}
}

func TestMoveClearsDrawOffer(t *testing.T) {
	g, _ := NewGame(TypePvP)
	g.State.Status = StatusOngoing

	if err := g.OfferDraw("white"); err != nil {
		t.Fatal(err)
	}
	if err := g.MakeMove("white", "e2", "e4", ""); err != nil {
		t.Fatalf("e2-e4: %v", err)
	}
	if g.State.DrawOfferedBy != nil {
		t.Error("making a move should clear the pending draw offer")
	}
}

// --- Phase 2: seat identity ------------------------------------------------

func TestJoinAssignsSeatsAndStartsGame(t *testing.T) {
	g, _ := NewGame(TypePvP)

	seat1, tok1 := g.Join()
	if seat1 != "white" || tok1 == "" {
		t.Fatalf("first join should be white with a token, got %q %q", seat1, tok1)
	}
	if g.State.Status != StatusWaiting {
		t.Errorf("game should wait until the second player joins, got %q", g.State.Status)
	}

	seat2, tok2 := g.Join()
	if seat2 != "black" || tok2 == "" {
		t.Fatalf("second join should be black with a token, got %q %q", seat2, tok2)
	}
	if g.State.Status != StatusOngoing {
		t.Errorf("game should start once black joins, got %q", g.State.Status)
	}

	seat3, tok3 := g.Join()
	if seat3 != "spectator" || tok3 != "" {
		t.Errorf("third join should be a tokenless spectator, got %q %q", seat3, tok3)
	}
}

func TestSeatColorAuthorization(t *testing.T) {
	g, _ := NewGame(TypePvP)
	_, wt := g.Join()
	_, bt := g.Join()

	if c, ok := g.SeatColor(wt); !ok || c != "white" {
		t.Errorf("white token should resolve to white, got %q %v", c, ok)
	}
	if c, ok := g.SeatColor(bt); !ok || c != "black" {
		t.Errorf("black token should resolve to black, got %q %v", c, ok)
	}
	if _, ok := g.SeatColor("bogus"); ok {
		t.Error("unknown token must not be authorized")
	}
	if _, ok := g.SeatColor(""); ok {
		t.Error("empty token must not be authorized")
	}
}

// --- Phase 2: cleanup ------------------------------------------------------

func TestCleanupEvictsFinishedGames(t *testing.T) {
	g, id := NewGame(TypePvP)
	g.mu.Lock()
	g.State.Status = StatusWhiteWins
	g.lastActivity = time.Now().Add(-time.Hour)
	g.mu.Unlock()

	runCleanup(time.Now())

	if GetGame(id) != nil {
		t.Error("a finished, long-idle game should have been evicted")
	}
}

func TestCleanupKeepsFreshGames(t *testing.T) {
	_, id := NewGame(TypePvP)

	runCleanup(time.Now())

	if GetGame(id) == nil {
		t.Error("a fresh game should not be evicted")
	}
}

// --- Phase 2: clocks -------------------------------------------------------

func TestClockChargesMover(t *testing.T) {
	g, _ := NewGame(TypePvP)
	g.Join() // white
	g.Join() // black -> game starts, white's clock running

	g.mu.Lock()
	before := g.State.WhiteTimeMs
	g.turnStartedAt = time.Now().Add(-2 * time.Second) // pretend white thought for 2s
	g.mu.Unlock()

	if err := g.MakeMove("white", "e2", "e4", ""); err != nil {
		t.Fatalf("e2-e4: %v", err)
	}

	g.mu.Lock()
	spent := before - g.State.WhiteTimeMs
	g.stopClockLocked() // avoid leaving the auto-forfeit timer pending
	g.mu.Unlock()

	if spent < 1500 {
		t.Errorf("white should have been charged ~2s, got %dms", spent)
	}
}

func TestFlagTimeoutAwardsOpponent(t *testing.T) {
	g, _ := NewGame(TypePvP)
	g.Join()
	g.Join() // Ongoing, white to move

	g.mu.Lock()
	g.State.WhiteTimeMs = 0
	g.turnStartedAt = time.Now()
	g.mu.Unlock()

	g.flagTimeout(White)

	if g.State.Status != StatusBlackWins {
		t.Errorf("white flagging on time should give Black Wins, got %q", g.State.Status)
	}
}

// --- Phase 2: SAN / PGN ----------------------------------------------------

func TestMoveSAN(t *testing.T) {
	start := NewBoard()
	if san := moveSAN(&start, mustPos("g1"), mustPos("f3"), PawnType); san != "Nf3" {
		t.Errorf("knight move: want Nf3, got %q", san)
	}
	if san := moveSAN(&start, mustPos("e2"), mustPos("e4"), PawnType); san != "e4" {
		t.Errorf("pawn push: want e4, got %q", san)
	}

	cases := []struct {
		name, fen, from, to string
		promo               PieceType
		want                string
	}{
		{"castling", "4k3/8/8/8/8/8/8/4K2R w K - 0 1", "e1", "g1", PawnType, "O-O"},
		{"pawn capture", "4k3/8/8/3p4/4P3/8/8/4K3 w - - 0 1", "e4", "d5", PawnType, "exd5"},
		{"promotion", "4k3/P7/8/8/8/8/8/4K3 w - - 0 1", "a7", "a8", QueenType, "a8=Q"},
		{"disambiguation", "4k3/8/8/8/8/2N5/8/4K1N1 w - - 0 1", "c3", "e2", PawnType, "Nce2"},
	}
	for _, tc := range cases {
		b, _, err := NewBoardFromFEN(tc.fen)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if san := moveSAN(&b, mustPos(tc.from), mustPos(tc.to), tc.promo); san != tc.want {
			t.Errorf("%s: want %q, got %q", tc.name, tc.want, san)
		}
	}
}

func TestMoveHistoryAndPGN(t *testing.T) {
	g, _ := NewGame(TypePvP)
	g.Join()
	g.Join()

	for _, m := range []struct{ player, from, to string }{
		{"white", "e2", "e4"}, {"black", "e7", "e5"}, {"white", "g1", "f3"},
	} {
		if err := g.MakeMove(m.player, m.from, m.to, ""); err != nil {
			t.Fatalf("%s-%s: %v", m.from, m.to, err)
		}
	}
	g.mu.Lock()
	g.stopClockLocked()
	g.mu.Unlock()

	if len(g.State.Moves) != 3 || g.State.Moves[0] != "e4" || g.State.Moves[2] != "Nf3" {
		t.Fatalf("moves = %v, want [e4 e5 Nf3]", g.State.Moves)
	}
	if pgn := g.PGN(); !strings.Contains(pgn, "1. e4 e5 2. Nf3") {
		t.Errorf("PGN missing expected move text:\n%s", pgn)
	}
}

// --- Phase 2: persistence --------------------------------------------------

func TestBoardFENRoundTrip(t *testing.T) {
	fens := []string{
		"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
		kiwipeteFEN,
		"4k3/8/8/3pP3/8/8/8/4K3 w - d6 5 3",
	}
	for _, fen := range fens {
		b, turn, err := NewBoardFromFEN(fen)
		if err != nil {
			t.Fatalf("parse %q: %v", fen, err)
		}
		if got := b.FEN(turn); got != fen {
			t.Errorf("round-trip mismatch:\n got %q\nwant %q", got, fen)
		}
	}
}

func TestLoadSnapshot(t *testing.T) {
	snap := Snapshot{
		ID:          "snap-test-1",
		GameType:    int(TypePvP),
		Status:      string(StatusOngoing),
		FEN:         "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1",
		WhiteTimeMs: 300000,
		BlackTimeMs: 290000,
		WhiteToken:  "wtok",
		BlackToken:  "btok",
		Moves:       []string{"e4"},
	}

	g, err := LoadSnapshot(snap)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	g.mu.Lock()
	g.stopClockLocked()
	g.mu.Unlock()

	if g.State.Turn != Black {
		t.Errorf("turn = %v, want Black", g.State.Turn)
	}
	if g.State.Status != StatusOngoing {
		t.Errorf("status = %q, want Ongoing", g.State.Status)
	}
	if len(g.State.Moves) != 1 || g.State.Moves[0] != "e4" {
		t.Errorf("moves = %v, want [e4]", g.State.Moves)
	}
	if c, ok := g.SeatColor("wtok"); !ok || c != "white" {
		t.Error("white token should be restored")
	}
	if c, ok := g.SeatColor("btok"); !ok || c != "black" {
		t.Error("black token should be restored")
	}
	if GetGame("snap-test-1") == nil {
		t.Error("reloaded game should be registered in the store")
	}
}

// --- Phase 3: AI -----------------------------------------------------------

func TestBestMoveReturnsLegalMove(t *testing.T) {
	b := NewBoard()
	move, ok := bestMove(&b, White, 2)
	if !ok {
		t.Fatal("expected a move from the start position")
	}
	legal := false
	for _, m := range b.GenerateLegalMoves(White) {
		if m.From == move.From && m.To == move.To {
			legal = true
			break
		}
	}
	if !legal {
		t.Errorf("AI returned an illegal move %v-%v", move.From, move.To)
	}
}

func TestBestMoveCapturesHangingQueen(t *testing.T) {
	// Black to move; the white queen on d4 is undefended and the black queen on
	// d8 can capture it down the open d-file.
	b, turn, err := NewBoardFromFEN("3qk3/8/8/8/3Q4/8/8/4K3 b - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	move, ok := bestMove(&b, turn, 3)
	if !ok {
		t.Fatal("expected a move")
	}
	if move.From != mustPos("d8") || move.To != mustPos("d4") {
		t.Errorf("AI should capture the hanging queen (d8xd4), got %v-%v", move.From, move.To)
	}
}

func TestAgentPlaysReply(t *testing.T) {
	g, _ := NewGame(TypeAgent)
	g.Join() // human takes white; the game starts

	// White moves, making it the agent's (black's) turn.
	g.mu.Lock()
	_ = g.applyMoveLocked(mustPos("e2"), mustPos("e4"), PawnType)
	g.mu.Unlock()

	g.playAgentReply() // synchronous, no artificial delay

	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopClockLocked()

	if len(g.State.Moves) != 2 {
		t.Fatalf("expected white move + agent reply, got %d: %v", len(g.State.Moves), g.State.Moves)
	}
	if g.State.Turn != White {
		t.Errorf("after the agent replies it should be White's turn, got %v", g.State.Turn)
	}
}

// --- Phase 3: matchmaking --------------------------------------------------

func TestListOpenGames(t *testing.T) {
	g, id := NewGame(TypePvP)
	g.Join() // white only -> still waiting for an opponent

	if !contains(ListOpenGames(), id) {
		t.Error("a PvP game waiting for a second player should be listed as open")
	}

	g.Join() // black joins -> game starts
	g.mu.Lock()
	g.stopClockLocked()
	g.mu.Unlock()

	if contains(ListOpenGames(), id) {
		t.Error("a started game should no longer be listed as open")
	}
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// --- Phase 3: FEN import ---------------------------------------------------

func TestNewGameFromFEN(t *testing.T) {
	g, id, err := NewGameFromFEN(TypePvP, "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1")
	if err != nil {
		t.Fatalf("new game from FEN: %v", err)
	}
	if g.State.Turn != Black {
		t.Errorf("turn should be Black, got %v", g.State.Turn)
	}
	if GetGame(id) == nil {
		t.Error("game should be registered in the store")
	}

	if _, _, err := NewGameFromFEN(TypePvP, "not a valid fen"); err == nil {
		t.Error("expected an error for an invalid FEN")
	}
}
