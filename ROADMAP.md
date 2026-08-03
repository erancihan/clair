# Chess Roadmap

Status of the chess feature (`internal/games/chess` engine, `internal/server/games`
HTTP/SSE layer, `internal/web/pages/games_chess.templ` UI).

The engine is pure, deterministic Go. Correctness is anchored by `perft`
move-generation counts in `chess_test.go` — the standard way to validate a chess
move generator against known reference values.

---

## Phase 0 — Legal, winnable PvP ✅ done

- Legal move generation filtered by king safety (`LegalMoves` rejects moves that
  leave your own king in check; handles pins and check evasion).
- Check / checkmate / stalemate detection — games now terminate.
- Correct pawn moves (no forward capture, double-step needs a clear path).
- Server-authoritative turn + piece-ownership enforcement; hardened input parsing.
- `Color` unification + `PieceType.Type()`; FEN-style board-state scaffolding
  (`EnPassant`, `CastlingRights`, halfmove/fullmove counters).
- Frontend seat persistence (refresh no longer demotes the creator to black).
- Tests: `perft(1..4) = 20 / 400 / 8902 / 197281` + per-rule unit tests.

## Phase 1 — Complete the ruleset ✅ done

Goal: a fully rules-compliant game. The FEN scaffolding from Phase 0 exists to
make these clean.

- [x] **Promotion** — `MakeMove`/`actionRequest` carry a promotion choice;
      `applyMove` swaps the pawn on the last rank; `GenerateLegalMoves` emits one
      entry per choice; UI piece picker (defaults to queen).
- [x] **En passant** — `applyMove` removes the passed pawn; the ep target is set
      on a double-push and cleared otherwise.
- [x] **Castling** — king two-square moves gated on `CastlingRights`, empty path,
      and `!IsAttacked` over the king's path (no castling out of / through / into
      check); `applyMove` relocates the rook; rights revoked on king/rook move or
      rook capture.
- [x] **Draw rules** — fifty-move (`HalfmoveClock`), threefold repetition
      (position history), insufficient material.
- [x] **FEN import** (`NewBoardFromFEN`) — used by the perft tests; the basis for
      the Phase 3 FEN import/export feature.
- [x] **Resign / draw offer** — `Resign`/`OfferDraw`/`AcceptDraw`/`DeclineDraw`
      game actions, dispatched via the action endpoint, with resign/draw UI
      controls; a move supersedes any pending offer.

Validation: standard perft positions pass to reference counts — start position
to depth 5, Kiwipete (castling) to depth 4 = 4,085,603, Position 3 (en passant /
pins), Position 5 (promotion) — plus per-rule unit tests.

## Phase 2 — Session & platform robustness ⚠️ partly done

Goal: trustworthy multiplayer that survives restarts and abuse. Everything here
landed except persistence, so games are still lost on restart.

- [x] Bind the white/black seat to a secret token (the server no longer trusts
      the client-asserted color); a `/join` endpoint assigns seats; read-only
      spectators; token-based reconnection survives refresh.
- [x] Game lifecycle: a background janitor evicts finished (10 min) and abandoned
      (30 min, no clients) games from the in-memory store, fixing the leak.
- [ ] Persistence: **not done.** An earlier SQLite write-through was reverted,
      and the database is PostgreSQL-only now, so games live in a `sync.Map` and
      do not survive a restart. Reinstating it means filling in the games
      domain's `Models()`, which the mount seam left empty for exactly this. Move
      history / PGN shipped separately and are unaffected.
- [x] Turn clocks (10 min/side) in `GameState`, charged per move with a
      server-side timer that auto-forfeits on flag; the client interpolates the
      running clock between updates.

## Phase 3 — Features & polish ✅ core done

Goal: it feels like a real chess site.

- [x] AI opponent: `TypeAgent` enabled for chess — a material-evaluation
      alpha-beta (negamax) search with capture-ordered pruning, playing black and
      replying asynchronously after each human move. "Play vs Computer" in the UI.
- [x] Lobby / matchmaking: `GET /open` lists PvP games waiting for a player, and
      a "Quick Match" button joins a random one (or opens a new game to wait).
- [x] Board UX: move history, last-move + check highlighting, captured-piece
      tray, and dropped the 🚧 marker on the games page (drag-and-drop / sounds
      still open).
- [x] FEN & PGN **export** (Board.FEN, the `/pgn` endpoint) and FEN **import**
      (start a game from any position). PGN import (needs a SAN parser) is still
      open, as are optional drag-and-drop and move sounds.
- [x] Identity: seats are attributed to an owner reference (`user:<id>` when
      signed in, `guest:<sid>` otherwise) and `GET /mine` lists a player's live
      games so they can resume one. Play stays anonymous — no route requires a
      login. See `internal/games/chess/README.md` for the full policy, including
      why CSRF is deferred and what will reverse that.

## Phase 4 — Stretch

- [ ] Persist games (fills `games.Models()`); unblocks the two items below and
      lets a guest's games follow them to their account via `GuestMigrator`.
- [ ] Account-linked game history, Elo / ratings — the first feature that will
      require a real account rather than a guest reference.
- [ ] Opening-book hints, analysis mode.
- [ ] Tournaments / brackets.

---

**Ordering rationale:** Phase 1 in the order above (promotion + en passant are
smallest and unblock perft-5 validation; castling next; draw rules last since
threefold needs the history map). Phase 2 before Phase 3 so the AI and lobby sit
on a sound session model.
