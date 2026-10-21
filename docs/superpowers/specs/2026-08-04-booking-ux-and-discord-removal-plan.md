> **Status (2026-09-02): completed.** Steps A–D here are roadmap v1 steps 5–8, all built. Steps C and D were redesigned mid-way by `2026-08-06-live-boards-and-screens-design.md`. Kept for history; line-number citations are stale.

# Booking UX, Hold Truth, and Discord Removal — Plan

Near-term work, ahead of the simulator and retention steps in
`2026-08-04-aurenor-implementation-plan.md`. It comes from three notes after the
theater page went live:

1. Drop Discord — nothing has used it for a long time.
2. The movie and seat-selection experience was never designed.
3. Seat selection and holding do not handle duplicates or repeat holds correctly.

Every claim below was verified against the tree, and the hold defects were
reproduced against live PostgreSQL rather than reasoned about. Where an audit
claim did not survive verification it is recorded as refuted, so nobody chases
it later.

Steps are ordered by dependency: **A (Discord out, scheduler wired) → B (hold
truth) → C (seat UX) → D (appointment UX)**. C consumes B's `expires_at` and
release route; B's expiry behaviour is only observable once A runs the reaper.

---

## Step A — REMOVE DISCORD, WIRE THE SCHEDULER

Discord is reachable from exactly one entry point: `clair scheduler`. That chain
holds three separate boot-killers, which is why no scheduled job has ever run.
`server` and `seed` never touch it.

### A.1 What is actually there

| Where | What |
|---|---|
| `internal/cmd/scheduler.go:31` | `clair.NewEventLoop()`, unconditional, the only caller in the repo |
| `internal/clair/eventLoop.go:33-39` | reads `DISCORD_CHANNEL_ID`, `logger.Fatal` when empty — `.env:25` and `.env.example:44` ship it empty |
| `internal/discord-bot/DiscordBot.go:24-31` | second `Fatal`, on empty `DISCORD_BOT_AUTH_KEY` |
| `internal/aws-sqs-handler/AWSSQSHandler.go:42-47` | third killer: live `GetQueueUrl` then `log.Panicf` at construction |
| `internal/utils/sqsToDiscordMsg.go` | **package `utils`**, imports `discordgo`, returns `*discordgo.MessageSend` |

The last row is the trap. `internal/utils` is imported by 15 non-vendor files
(`internal/server/server.go`, `internal/database/database.go`, `internal/cmd/*`,
`internal/games/*`, and two test files). Dropping `discordgo` from `go.mod`
without deleting that file breaks the whole build, not just the scheduler.

### A.2 Delete (5 files, 2 package directories)

- `internal/clair/eventLoop.go` — `internal/clair` survives: `sentry.go` is
  Discord-free and `root.go:18` calls `SetupSentry`.
- `internal/discord-bot/DiscordBot.go` — whole package, one importer.
- `internal/aws-sqs-handler/AWSSQSHandler.go` — whole package; SQS exists here
  only to feed Discord.
- `internal/utils/sqsToDiscordMsg.go` — `SQSLambdaToDiscordMessage` (`:64`) already
  has zero callers.
- `internal/utils/hexToInt.go` — both call sites are in the file above.

### A.3 Edit

- `internal/cmd/scheduler.go` — drop lines 31-35 and the `internal/clair` import,
  then fill the two `// TODO:` stubs at `:22-26` (see A.5).
- `.env` / `.env.example` — remove the Discord block (`.env:21-25`,
  `.env.example:40-44`) and the AWS block (`.env:15-19`, `.env.example:34-38`).
  `Makefile:2-3` exports every key in `.env`, which is how a stale value re-arms
  a removed code path; leave nothing behind.
- `Dockerfile:40-42` — three Discord `ARG`s in the final stage with no matching
  `ENV`. `docker-compose.yaml:25-26` (`env_file`) is the real config path.
- `package.json:4` — description still calls the project a Discord bot.
- `README.md` — Discord-framed end to end; ~90 of 142 lines, including the only
  scheduler service definition in the repo (`:44-72`) and four Discord webhook
  roadmap items (`:139-142`). Rewrite around what the project is now.
- `internal/cmd/seed.go:16-17` — stale comment about the `DISCORD_CHANNEL_ID`
  blocker.
- Docs: this file's sibling plan (Step 4 section), the older
  `2026-08-03-booking-simulations-ui-plan.md:91,178`, and
  `docs/worldbuilding/aurenor.md`.

### A.4 Modules, and the one ordering rule

Dropped once nothing imports them: `discordgo` (`go.mod:9`),
`gorilla/websocket` (`go.mod:39` — discordgo is its only importer;
`gorilla/sessions` does **not** depend on it), `aws-lambda-go` (`go.mod:7`),
`aws-sdk-go` (`go.mod:8`), `jmespath` (`go.mod:47`). ~3.7 MB of vendor. Run
`make tidy` (`go mod tidy && go mod vendor`) to regenerate `go.sum` and
`vendor/modules.txt`.

**Order matters in one direction only.** Deleting the files first is safe —
verified in a scratch copy: with `go.mod` untouched, `go build -mod=vendor ./...`
and `go vet -mod=vendor ./...` are both clean, and no test references a removed
symbol. Editing `go.mod` first is not: it fails with `inconsistent vendoring`,
and that failure hits `go build`, `go vet`, the Docker image, and CI
(`.github/workflows/test.yaml:45-49`). Do the file deletions and the module
change in the same commit, deletions first.

### A.5 Give the scheduler real work

Removing Discord leaves `clair scheduler` as an empty daemon: `gocron` with zero
jobs plus a handler-less `http.Server{Addr: ":8080"}` blocking on `ctx.Done()`.
Wire the reaper in the same change:

```go
db, err := database.New(ctx)          // fills the TODO at scheduler.go:22-26; copy server.go:33-37
if err != nil { return err }

scheduler := gocron.NewScheduler(time.UTC)

scheduler.Every(1).Minute().SingletonMode().Do(func() {
    n, err := booking.ReapExpiredHolds(db, time.Now())
    if err != nil {
        logger.Error("reaper", zap.Error(err))
    } else if n > 0 {
        logger.Info("reaped holds", zap.Int("n", n))
    }
})
// Simulator and retention register here later.
```

`booking.ReapExpiredHolds` (`internal/booking/hold.go:330`) is well tested — three
test callers — but has **no production caller**. This is its first live wiring.

If the scheduler is not wanted at all, the alternative is deleting the command,
which also means editing `internal/cmd/root.go:62`.

### A.6 Definition of Done

`go build ./...`, `go vet ./...`, `make test` clean. `grep -ri discord` finds
nothing outside `docs/` history. `clair scheduler` boots, stays up, and reaps.
`make tidy` leaves a `go.mod` with no unused requires.

---

## Step B — HOLD TRUTH *(done)*

**Contract change worth knowing:** GA `qty` is now the **total** the buyer wants,
not an increment. Holding 2 and asking for 3 tops up to 3; asking for 1 changes
nothing and answers with the 2 that are really held. The old behaviour counted it
additively in the cap check while the kernel quietly kept the original hold —
which is exactly how the page came to render tickets that did not exist. Both
hold responses now carry what is actually held (`qty` / `seat_count`) and the
server's own `expires_at`. `TestTicketingHTTP_PerBuyerLimit` was updated to the
new contract.


Ten defects, each reproduced. Two audit claims were **refuted** and are not work:
a latent seat double-book (its precondition is unreachable — seat inventories are
created capacity-1 at `internal/ticketing/event.go:87` and nothing raises them),
and "guests can reset the cap by clearing cookies" (the documented, intended cost
of guest checkout, `mount.go:26-33`).

### B.1 Blocker

**Expired holds are never freed.** No production reaper caller, no release route,
and the client's expiry path is fabricated. Fixed by Step A.

### B.2 Major — the per-buyer cap is not a cap

- **Concurrent requests bypass it.** `assertBuyerLimit`
  (`internal/ticketing/waitlist.go:22-42`) is a plain non-locking `SELECT` run at
  the top of the transaction (`hold.go:40` GA, `:95` seats) before any row is
  locked, and a seated group touches disjoint per-seat rows, so nothing
  serialises racers under `READ COMMITTED`. Reproduced: four concurrent 2-seat
  holds, same owner, `MaxPerBuyer=2` → all four returned `nil`, buyer held 8.
  Fix: count under a lock that the hold path also takes — either a
  `SELECT … FOR UPDATE` on the buyer's open order for this event, or an advisory
  lock keyed on `(event_id, owner_ref)` around the assert plus the hold.
- **Expired holds still count against it.** `waitlist.go:35` predicates on
  `(oi.status = 'fulfilled' OR h.status = 'active')` with no `expires_at` test —
  contradicting its own comment at `:20-21`. Reproduced: a lapsed 2-unit hold
  against cap 4 made the next `qty=3` fail with `ErrPurchaseLimit`. Fix: add
  `AND (h.expires_at IS NULL OR h.expires_at > ?)`.

### B.3 Major — the page reports holds it did not get

`HoldGA` (`internal/ticketing/hold.go:51-58`) returns the buyer's pre-existing
order and hold untouched whenever an active hold exists for `(tier, owner)`,
ignoring the newly requested `qty`, and answers 201. Reproduced: `qty=2` then
`qty=3` → same order, total unchanged, `held=2`, while the page renders
"Holding 3 · $160.00" because the count comes from client state
(`aurenor_theater_live.templ:336`). Fix: either top up the existing hold to the
requested quantity or answer 409 with what is already held — then have the client
render the server's numbers, never its own.

### B.4 Major — no way to give a hold back

`mountEvents` (`internal/server/booking/mount.go:168-182`) registers two hold
routes, two waitlist routes and an offer claim. Nothing releases.
`booking.Release`/`ReleaseTx` exist with no caller outside the reaper. The
client's "Start over" only nulls local state. Fix: add
`POST /events/{slug}/holds/release` taking the hold tokens, CSRF-guarded, owner
checked against `OwnerRef`, idempotent — the ticketing counterpart of
`cancelByToken`.

### B.5 Minor, but each one lies to the user

- **Duplicate `seat_ids` produce a false 400.** `tickets.go:134-140` compares
  `count(*)` over `s.id IN ?` against `len(seat_ids)`, so `[5,5]` returns "one or
  more seats do not belong to this event" about a seat that does. Duplicates
  cannot inflate anything (the kernel rolls the group back), so this is a message
  bug, not a security one. Fix: dedupe before the guard, and 400 only on genuine
  non-membership.
- **Seat holds omit `expires_at`.** `tickets.go:158-162` returns order, tokens and
  total; `holdGA` returns `expires_at` at `:99`. The client invents the deadline
  (`aurenor_theater_live.templ:155,340`), then tells the user the seats are back
  on sale while the server still holds them. Fix: return the hold's real
  `expires_at`.
- **Re-holding your own seat says "no longer available."** Capacity-1 seat
  inventories make the invariant fail before the idempotent branch at
  `internal/booking/hold.go:90-105` can be reached. Fix: detect the caller's own
  active hold and answer idempotently, or say "you already hold this seat".
- **No bound on `len(seat_ids)` when `MaxPerBuyer=0`.** Reproduced: one buyer
  held all 12 seats of an unlimited event in one request. Fix: a hard per-request
  ceiling independent of the event cap.
- **Client cap ignores what the buyer already holds** and `selected[]` is never
  reconciled against a refreshed map (`aurenor_theater_live.templ:277-283,247`).
  Fixed as part of Step C.

### B.6 Definition of Done

New tests in `internal/ticketing` and `test/`: concurrent holds cannot exceed
`MaxPerBuyer`; an expired hold stops counting; duplicate `seat_ids` succeed or
fail honestly; a seat hold response carries `expires_at`; release frees the seat
and is idempotent; re-holding your own seat is not reported as sold out.

---

## Step C — SEAT SELECTION UX *(done)*

**What was actually built** is recorded in
`2026-08-06-live-boards-and-screens-design.md`: seat selection became a screen of
its own rather than an in-card stage, and the board was rebuilt full width around
a date strip.


Three designs were drafted independently (focused overlay / in-card stage /
guided wizard) and judged. **Winner: the in-card stage**, with quantity-first and
"pick best available" grafted from the wizard, and the map plumbing, staleness
handling and error copy grafted from the overlay.

Rejected on purpose: the full-viewport overlay (it introduces `role="dialog"`, a
scrim, a hand-rolled focus trap, iOS scroll lock, `x-teleport` and `:inert` all at
once, and a leaky trap is more hostile than the bug being fixed); the four-stage
wizard (costs a decided visitor two extra taps); query-param routing (defer);
a second exported catalog function for film grouping (Alpine can derive it).

### C.1 The complaint, precisely

Clicking "Choose seats" opens a panel that is a sibling **after** a 2-column grid
of up to 13 showing cards — roughly 1,600–3,000px below the button, with no
scroll, no focus move, and no acknowledgement inside the viewport. The audit
found 21 defects on this page: 7 blockers, 12 major, 2 minor. Beyond placement:
seats are 28px with 10px labels and no accessible name; free and taken seats
differ only in grey; after a hold the user sees UUIDs and cannot tell which seats
they hold; closing the panel silently destroys that hold; the "no showings" error
is nested inside the hidden panel and can never render.

### C.2 The design

**Board: 13 cards → 4 film cards.** Group by film, each group sorted by start,
groups sorted by earliest remaining showtime. Chips (`Assigned seating` /
`General admission`, price, `N showtimes`) render from the catalog with zero
fetches, so the first paint is four real cards instead of "Loading showings…".
Showtime tiles group by day, reusing the `days` getter shape from
`aurenor_appointments.templ:173-191`.

**Stage in place.** Opening a showtime expands the stage *inside* the `<article>`
that owns the tile: the card widens to `sm:col-span-2`, gains a teal ring, the
other film cards collapse their tile rows
(`grid-rows-[1fr]` → `grid-rows-[0fr]` under `motion-safe:`), and a **conditional**
`scrollIntoView` fires only when the card is off-screen or below 40% of the
viewport. Focus moves to the stage heading; Escape closes; focus returns to the
tile. Three simultaneous in-viewport changes mean the click cannot read as a
no-op.

**Everything the buyer needs is above the map**: showtime and salon, an error slot
with `role="alert"`, then `Choose up to 6 seats · $22.00 each`, then a
quantity-first row (`How many seats?  [−] 2 [+]`, with the `+` correctly disabled
at the cap) and `[ Pick best available ]` which picks the best contiguous run and
announces it. General admission stops there — 6 of the grand theater's 13
showings are GA, so one shape must serve both. The seat map is refinement, not a
toll booth.

**Map.** One scroller holding SCREEN and the rows (SCREEN currently sits outside
and stands still while seats slide under it), `mx-auto w-max` so it centres when
it fits — measured, no panning at `sm` and up for any seeded map, so panning is a
phone-only concern. Row letters `sticky left-0`. Seats `h-9 w-9` on touch,
`h-8 w-8` on desktop. Five states from one `seatState()` — free, selected, yours,
taken, blocked — differing in fill, edge **and** glyph so they survive greyscale,
with the legend rendered through the same functions so it cannot drift from the
map (today it already does). `role="grid"`/`gridcell`, `aria-label` per seat,
`aria-disabled` rather than `disabled` so taken seats stay readable, and a roving
`tabindex` so 120 seats are one tab stop.

**Sticky summary bar** (`sticky bottom-0`, built from the floating-chrome recipe
at `header.templ:27`): removable seat chips as the undo path for a mis-tap,
`2 of 6 · $44.00`, an `aria-live="polite"` line, and the primary button. In hold
mode: `Held A-7, A-8 · $44.00 · 09:58 left`. No fixed positioning, no body
padding, no safe-area maths.

**Truth about holds.** `yours` is decided from local hold state because the
server reports every hold as `held` — the copy must say a hold placed on your
phone shows as plain grey on your laptop. The receipt names seats, salon and
time, keeps the not-wired sentence, and labels the tokens as a box-office
reference with a copy button. "Start over" becomes "Hide this hold", explains
that the seats stay held until the timer runs out, and confirms first. A
localStorage ledger plus a "My holds" strip mirrors `myBookings`, with copy that
says forgetting does not release.

**Staleness.** 20s `loadSeats({quiet:true})` polling while a seated stage is
open, skipped when the tab is hidden or a hold is in flight, plus a refresh on
`visibilitychange`. A 409 **diffs** rather than wipes: refetch, drop only the
seats that are gone, name them, flash them, keep the rest. Raw API strings are
replaced by a status→copy map.

### C.3 Files

- `internal/aurenor/catalog.go` — extend `Showing` with `Kind`, `PriceCents`,
  `Rows`, `Cols` from `filmSpec.Tier`. Additive; only `aurenor_helpers.go:81`
  reads `Showing` fields and the tests use `TheaterEventSlugs`. Buys chips on
  first paint, a skeleton at the real grid size, and GA-vs-seated before any
  fetch. Add a catalog test that Kind/Rows/Cols agree with `AllEvents` per slug.
- `internal/web/pages/aurenor_theater_live.templ` — markup rewrite; keeps its
  signature and `x-data`, so no route or mount change. Move `@aurenorScript()`
  **first**, before any cloaked node — that alone fixes the flash, without
  touching `index.templ`.
- `internal/web/pages/aurenor_theater_script.templ` — new; `Alpine.data('theater')`,
  `Alpine.data('theaterHolds')`, the holds-storage trio and `myTheaterHolds()`.
  Mirrors the `apptBookerScript` split.
- Tests: render assertions that the board's error and empty blocks live outside
  every `x-show="active"`, that `[x-cloak]` is emitted before the first cloaked
  node, and that `theaterAttr` still emits parseable JSON.

Tailwind v4 scans raw text, so every new class must appear as a literal in the
`.templ` source — including inside `:class` ternaries. Never build a class by
concatenation.

### C.4 Ship order

1. **Fixes the complaint** — catalog fields, film grouping, in-card stage,
   collapse, conditional scroll, focus, Escape, sticky bar with cap/price/total,
   `@aurenorScript()` first.
2. **Map and a11y** — five seat states, grid roles, roving tabindex, sticky row
   headers, SCREEN inside the scroller, 36/32px targets, quantity-first and
   auto-pick, real skeletons, live regions.
3. **Hold truth** — labelled receipt, `yours` state, copyable tokens,
   `dismissHold()`, localStorage ledger and "My holds", 409 diff, error map, 20s
   polling.

---

## Step D — APPOINTMENT UX PASS *(done)*

**What was actually built** is recorded in
`2026-08-06-live-boards-and-screens-design.md`: a screen per service rather than
a two-pane page, so no screen holds availability for a service nobody is looking
at.


The same audit run over `/aurenor/hospitals/{slug}`, `/aurenor/consulate` and
`/aurenor/bureau` found the same class of defect, so one pass fixes both surfaces.
Four blockers:

- **The guest form renders below the whole slot list**
  (`aurenor_appointments.templ:48`), so picking a time appears to do nothing —
  the same complaint as the theater, same fix: bring the next step to the user.
- **Expand-in-place dumps 172 chips into one card** and stretches its grid-row
  sibling (`aurenor_shared.templ:53`). The bureau is 15-minute slots across
  Mon–Sat over a 7-day window. Group by day and collapse.
- **A 409 silently erases its own error message** (`:261`) — it sets `error`, then
  `loadSlots()` clears it.
- **A successful booking gives no visible feedback on capacity>1 schedules**
  (`:67`) — the slot is still there with one fewer left, and nothing says the
  booking landed.

Majors: the cancel-token warning appears only after it is too late to act on;
"My bookings" is undiscoverable (below the grid, hidden when empty, no anchor);
rows read "Cardiology" with no hospital, or literally "Appointment" for
server-merged rows; cancel is one tap with no confirmation and no success
feedback.

---

## Method

Twelve agents: six parallel audits (Discord footprint, theater UX, hold
correctness, appointment UX, repo conventions, plan-doc structure), three
independent UX designs, one judge, two adversarial verifiers. The verifiers ran
against live PostgreSQL and refuted two claims, corrected several citations, and
found four things the audits missed — including that `root.go:62` registers the
scheduler command, and that deleting files before touching `go.mod` is the safe
order.
