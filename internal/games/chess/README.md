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

### Phase 2 — session & platform robustness (partly done)
- [x] Bind the seat to a secret token (reconnect + read-only spectators)
- [x] Timeout/cleanup janitor for finished & abandoned games
- [x] Move history (SAN) + PGN export
- [x] Turn clocks
- [x] Auto-forfeit on flag
- [ ] Game state persistence. An earlier SQLite write-through was reverted, and
      the database is PostgreSQL-only now. Games live in a `sync.Map` and are
      lost on restart. Restoring this means giving the games domain real tables
      through `games.Models()`, which is currently empty.

### Phase 3 — features & polish
- [x] AI opponent (alpha-beta search, `TypeAgent`)
- [x] Game lobby / matchmaking (Quick Match)
- [x] Last-move / check highlighting, captured-piece tray
- [x] FEN import + FEN/PGN export (PGN import — SAN parsing — still open)
- [x] Seats attributed to an owner; `GET /mine` finds a player's live games

## Identity

**Chess is anonymous by default. No route requires a login.**

Every seat is attributed to an owner reference from the authentication layer —
`user:<id>` when the visitor is signed in, `guest:<sid>` otherwise, backed by a
long-lived HttpOnly cookie. `OptionalAuthMiddleware` runs on every application
route, so both resolve without chess mounting any middleware of its own.

Two identifiers do different jobs, and the difference matters:

| | what it is | what it does |
|---|---|---|
| Seat token | 192-bit secret, returned once at join | **authorizes** moves; the browser never sends it automatically |
| Owner ref | derived from the caller's cookies | **attributes** a seat, so a player can find it again |

An owner reference is not a credential. Moving still requires the seat token,
which is why a cross-site request cannot play a move on somebody's behalf.

- `GET /games/chess/mine` lists the live games the caller holds a seat in, with
  the seat token, so a player who lost their local copy can resume. It is
  **ephemeral** — served from the in-memory store, so it is empty after a
  restart and blind to other instances. The response says `"ephemeral": true`
  rather than implying durable history. It carries seat tokens, so it must stay
  same-origin: never serve it with a permissive CORS header.
- Signing in unlocks nothing today. Rated play is the first thing that will
  require a real account (`CurrentUser`), and it will live behind
  `AuthMiddleware` on its own group.
- A guest's games do **not** follow them to their account on login. The
  `GuestMigrator` hook takes a `*gorm.DB` to re-point rows, and chess has no
  rows. It gets registered when game history is persisted, not before.

### CSRF

The chess POST routes are deliberately not behind `api_auth.CSRF()`: a move is
authorized by the seat token in the request body, so a cross-site POST cannot
forge one. Adopt it — front end and middleware in the same change — as soon as
either lands:

1. a route that forfeits or destroys owner-attributed state (resign, abandon or
   delete a game), or
2. durable, user-visible history (ratings, leaderboards).

The reasoning lives next to the routes in `internal/server/games/chess_mount.go`.

### Deployment note

`SESSION_KEY` is mandatory when `APP_ENV=production` — the server panics at
startup without it. The guest identity chess relies on is signed with it.
