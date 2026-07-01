# Chess

A from-scratch chess engine and PvP server. The engine (this package) is pure,
deterministic Go with no external dependencies; the HTTP/SSE layer lives in
`internal/server/games` and the UI in `internal/web/pages/games_chess.templ`.

## Architecture

- `piece.go` — the `Piece` interface (`Color`, `Type`, `ValidMoves`), plus the
  `Color`/`PieceType` enums and their wire encodings.
- one file per piece — each generates *pseudo-legal* moves for its kind.
- `chessboard.go` — the `Board`: grid, FEN-style state, attack detection
  (`IsAttacked`), and the legal-move layer (`LegalMoves` filters pseudo-legal
  moves that would leave the mover's own king in check).
- `game.go` — session, turn handling, SSE client fan-out, and terminal-state
  detection (checkmate/stalemate).
- `chess_test.go` — `perft` move-generation counts plus targeted rule tests.

Run the engine tests with `make test` or `go test ./internal/games/chess/...`.

## Status

### Done (Phase 0 — legal, winnable PvP)
- [x] Correct pawn moves (no forward capture, double-step requires a clear path)
- [x] Legal-move generation filtered by king safety (no moving into / leaving check)
- [x] Check, checkmate and stalemate detection → games now end
- [x] Server-authoritative turn + piece-ownership enforcement
- [x] FEN-style board state scaffolding (en-passant target, castling rights,
      halfmove/fullmove counters) — maintained by the rules added in Phase 1
- [x] `perft(1..4)` correctness tests + per-rule unit tests

## TODO

### Phase 1 — complete the ruleset
- [x] Implement promotion (with UI picker)
- [x] Implement castling
- [x] Implement en passant
- [x] Implement draw by the fifty-move rule
- [x] Implement draw by repetition detection
- [x] Implement draw by insufficient material detection
- [x] FEN import (`NewBoardFromFEN`)
- [x] Resign / draw offer & agreement

### Phase 2 — session & platform robustness (done)
- [x] Bind the seat to a secret token (reconnect + read-only spectators)
- [x] Timeout/cleanup janitor for finished & abandoned games
- [x] Game state persistence (SQLite via GORM) with startup reload
- [x] Move history (SAN) + PGN export
- [x] Turn clocks
- [x] Auto-forfeit on flag

### Phase 3 — features & polish
- [x] AI opponent (alpha-beta search, `TypeAgent`)
- [x] Game lobby / matchmaking (Quick Match)
- [x] Last-move / check highlighting, captured-piece tray
- [ ] FEN / PGN import (PGN & FEN export already shipped)
