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

## Phase 2 — Session & platform robustness ✅ done

Goal: trustworthy multiplayer that survives restarts and abuse.

- [x] Bind the white/black seat to a secret token (the server no longer trusts
      the client-asserted color); a `/join` endpoint assigns seats; read-only
      spectators; token-based reconnection survives refresh.
- [x] Game lifecycle: a background janitor evicts finished (10 min) and abandoned
      (30 min, no clients) games from the in-memory store, fixing the leak.
- [x] Persistence: games (board FEN, clocks, tokens, SAN moves) are written
      through to SQLite via GORM and in-progress games are reloaded on startup;
      move history / PGN shipped separately.
- [x] Turn clocks (10 min/side) in `GameState`, charged per move with a
      server-side timer that auto-forfeits on flag; the client interpolates the
      running clock between updates.

## Phase 3 — Features & polish 🚧 in progress

Goal: it feels like a real chess site.

- [x] AI opponent: `TypeAgent` enabled for chess — a material-evaluation
      alpha-beta (negamax) search with capture-ordered pruning, playing black and
      replying asynchronously after each human move. "Play vs Computer" in the UI.
- [x] Lobby / matchmaking: `GET /open` lists PvP games waiting for a player, and
      a "Quick Match" button joins a random one (or opens a new game to wait).
- [x] Board UX: move history, last-move + check highlighting, captured-piece
      tray, and dropped the 🚧 marker on the games page (drag-and-drop / sounds
      still open).
- [ ] FEN / PGN import & export (engine state already maps cleanly to FEN).

## Phase 4 — Stretch

- [ ] Account-linked game history, Elo / ratings.
- [ ] Opening-book hints, analysis mode.
- [ ] Tournaments / brackets.

---

**Ordering rationale:** Phase 1 in the order above (promotion + en passant are
smallest and unblock perft-5 validation; castling next; draw rules last since
threefold needs the history map). Phase 2 before Phase 3 so the AI and lobby sit
on a sound session model.
