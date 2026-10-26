# Evaluation of steps 1–8 and roadmap v2

**Date:** 2026-09-02
**Status:** current. This document is the work list for everything after roadmap step 8. `docs/worldbuilding/aurenor.md` carries the short form of the roadmap; this file carries the evidence behind it.

## 1. Where things stand

Roadmap steps 1–8 are built and green. With Postgres up, `go test -count=1 ./...` passes every package with zero skips: `internal/appointments`, `internal/aurenor`, `internal/booking`, `internal/booking/live`, `internal/server/booking`, `internal/ticketing`, `test`. `go build ./...` and `go vet ./...` are clean. Nothing from steps 5–8 is committed yet; the whole working tree (62 entries) is one unmerged change set on `feat/booking-systems`.

Four read-only audits were run over the tree on 2026-09-02, one per layer: the domain engine, the HTTP/streaming layer, the web pages, and docs/ops. Every finding below was spot-checked against the source before being listed. Findings the audits raised that did not survive verification, or that were formatting-only, are omitted.

The short verdict: the booking engine's central idea holds (the capacity guard in `BookDirect` and `HoldUnitsTx` is correct, expiry comparisons are consistent everywhere), the live layer is designed right (one screen, one connection, cost per change not per viewer), and the seed catalog is idempotent. The weaknesses cluster in three places:

1. **Code around the guard, not the guard.** `held` is maintained incrementally by several paths that do not all take the same lock, and the commit step trusts that arithmetic without re-checking capacity.
2. **Rules applied on one path and skipped on its twin.** Seated holds are capped, GA holds are not; release is owner-scoped, waitlist claim is not; the seat map is cached, availability is not; the seated showing streams, the GA showing does not.
3. **The operator path is broken.** `make build` and `make run` disagree on the binary name, the server exits 0 when the database is unreachable, the Docker image has no entrypoint, and CI runs only `./test/...`, skipping eleven test files.

Two facts change the roadmap's order. The seed catalog dates showings `now + 1..7` days and keys them by a date-free slug, so a re-run skips them all and after seven days the site has zero future showings. And there is no cancel/refund path for ticket orders at all (`booking.Cancel` has exactly one caller, in appointments). The simulator as specced cannot run on top of either.

## 2. Verified findings

Severity: **bug** breaks a stated invariant or a documented behaviour; **security** is reachable by an anonymous request; **ops** blocks running or deploying; **gap** is missing coverage or a missing path the roadmap needs; **smell** is structure worth fixing while nearby. Each item names the roadmap v2 step that owns it.

### 2.1 Domain engine (`internal/booking`, `internal/ticketing`, `internal/appointments`)

| # | Where | Sev | Finding | Fix | Step |
|---|---|---|---|---|---|
| K1 | `booking/hold.go:179-195` | bug | `ReleaseTx` reads the hold with a bare `First`, while `CommitTx` takes `FOR UPDATE`. The reclaim CTE in `HoldUnitsTx` can release the same hold between the read and the `held - qty` update; `WHERE held >= ?` does not catch it because other holds keep `held` positive. | Add `clause.Locking{Strength: "UPDATE"}` to the read and re-check `status = 'active'` after it. | A1 |
| K2 | `booking/hold.go:144-149` | bug | `CommitTx` moves `held → booked` with `WHERE id = ? AND held >= ?` and no capacity predicate. Any `held` undercount (K1) becomes an oversell. | Add `AND booked + ? <= capacity` so the statement that writes `booked` enforces the invariant. | A1 |
| K3 | `appointments/book.go:103` | bug | The cancel-refund lookup is `WHERE fulfilled_id = ? AND status = 'fulfilled'` with no domain scope. `ticketFulfiller` also writes `fulfilled_id`, so cancelling appointment 5 can refund the order behind ticket 5. | Join `booking_orders` and require `domain = 'appointment'`, or add a `fulfilled_kind` column. | A1 |
| K4 | `ticketing/waitlist.go:101-136` | security | `ClaimOffer` looks the hold up by token only: no owner check, no `lockBuyer`, no `assertBuyerLimit`, no event check. A leaked offer token is claimable by anyone and bypasses `MaxPerBuyer`. `ReleaseHolds` next to it is correctly owner-scoped. | Require `h.OwnerRef == buyer.OwnerRef` and wrap in the same lock and limit as `HoldGA`. | A1 |
| K5 | `ticketing/hold.go:38,176` | bug | Nothing refuses a hold on a showing that has already started. `now` is used only for TTL and buyer limits; no comparison against `event.StartAt` anywhere in ticketing or the handlers. | Reject `event.StartAt <= now` in the domain functions. | A1 |
| K6 | `booking/hold.go:274-323` | gap | A hold that lapses between checkout and the webhook makes `CaptureOrder` fail and roll back the payment row too. No void/refund path exists outside `CancelByToken`. | Record the payment in its own transaction; add an engine `RefundOrder`. | B2 |
| K7 | `booking/hold.go:108-119` | gap | `booking.Cancel` has one caller (`appointments/book.go:96`). Nothing ever decrements `booked` for a tier or seat inventory, so a captured sale is irreversible. | Add `CancelOrder(db, orderID, now)` that releases each item's units and drives the refund. Needed by the simulator for churn. | B2 |
| K8 | `booking/inventory.go:122-126` | bug | `Block` returns nil when the capacity guard rejects it; it never inspects `RowsAffected`, unlike `BookDirect` and `SetCapacity`. Also has no callers. | Return `ErrSoldOut` on zero rows, or delete it with `SetCapacity` (only called from its own test). | A1 |
| K9 | `booking/hold.go:229-232` | bug | `offerToWaitlist` does `return nil` on a `HoldUnitsTx` error, so freeing 3 units offers at most one and any real error is dropped. | `continue` on `ErrSoldOut`, return anything else. | A1 |
| K10 | `booking/hold.go:336-344` | bug | The reaper discards every `Release` error and never checks the order-expiry `Update`'s error. A hold that fails to release is re-selected every minute with nothing logged. | Return or log release errors; check the update. | A1 |
| K11 | `booking/hold.go:93-98`, `waitlist.go:106`, `hold.go:266` | bug | Three places ignore `RowsAffected` or `res.Error` and report success or the wrong sentinel (a DB failure in `ClaimOffer` surfaces as 410 Gone). | Check the result before interpreting it. | A1 |
| K12 | `appointments/availability.go:80` vs `booking/inventory.go:60-72` | bug | Availability computes `remaining` from `sched.Capacity`, but `EnsureInventory` ignores its capacity argument once the row exists and bookings are guarded by `slot.Capacity`. After a capacity edit the two disagree. | Read `slot.Capacity` in the availability query. | A1 |
| K13 | `appointments/book.go:40,159-176` vs `availability.go:55-85` | bug | "Is this time offered" is implemented twice. `validateOpen` runs outside the booking transaction and without `WithWriteRetry`; on the Lisbon fall-back hour it accepts two UTC instants while the generator emits one, so `ensureSlot` can materialise a second full-capacity slot. | Express `validateOpen` as membership in the generator's output, inside the transaction. | A1 |
| K14 | `ticketing/hold.go:198-227` | smell | `HoldSeatGroup` never checks a seat belongs to `eventID`; the handler does. Tests call it with `eventID = 0`, which silently disables `lockBuyer` and the cap. A simulator calling it in-process gets no handler. | Validate `tier.EventID == eventID` in the domain. | A1 |
| K15 | `ticketing/hold.go:214` | bug | A mixed-tier seat group sums cents across currencies and labels the total with the last seat's currency. | Reject groups spanning more than one currency. | A1 |
| K16 | `booking/notify.go:58-62` | bug | The inventory trigger fires on UPDATE of `booked/held/blocked` only. A capacity change, or the INSERT that materialises a slot's inventory, never reaches the hub. | Add `capacity` to the `WHEN` clause and an `AFTER INSERT` trigger. | A2 |
| K17 | `booking/live/hub.go:134` | smell | `versions[topic]++` on every notification, never pruned; a long-running process accumulates an entry per topic it ever saw. | Kept on purpose: one integer per topic ever seen, bounded by the catalog; pruning would reset a counter the snapshot cache compares stamps against and could serve a stale entry. Documented on the field. | — |
| K18 | `booking/hold.go:40-47` | smell | Retryable errors detected by `strings.Contains(err.Error(), "40001")`. | `errors.As` against `*pgconn.PgError`, compare `.Code`. | A1 |
| K19 | `booking/hold.go:19`, `appointments/book.go:191` | smell | `newToken()` defined twice, byte-identical. | Export one. | A1 |
| K20 | `appointments/book.go:96` | smell | `CancelByToken` calls `time.Now()` internally; every other entry point takes `now`. | Add the parameter. The simulator needs it. | B4 |
| K21 | `ticketing/waitlist.go:28-62` | smell | `lockBuyer`/`assertBuyerLimit` are ticketing-private; appointments has neither, so one guest can book every slot in a schedule. | Appointments' free path carries no owner reference; a per-guest cap there is rate limiting (A3 S7), not an engine rule. | A3 |
| K22 | `server/booking/api_availability.go:47-77,99-129` | smell | The "load rules + exceptions + booked counts" preamble is duplicated verbatim between `availability()` and `availabilityViews()`. | Move into `appointments.OpenSlotsFor(db, sched, from, to, now)`. The simulator needs exactly this call. | B4 |

Not dead, contrary to first impression: the waitlist (`JoinWaitlistTier/Seat`, `ClaimOffer`) and paid appointments (`HoldSlot`) are both routed and tested end to end.

Test gaps in this layer, in the order to close them: concurrent `Release` vs reaper (K1/K2); DST fall-back (only spring-forward is covered, and not for Lisbon); capture whose hold lapsed mid-checkout; `appointments.Book` under concurrency (only `BookDirect` is raced); `ClaimOffer` by a non-owner; mixed-currency group.

### 2.2 HTTP and streaming (`internal/server`, `internal/booking/live`)

| # | Where | Sev | Finding | Fix | Step |
|---|---|---|---|---|---|
| S1 | `server/booking/api_tickets.go:94-98` | security | GA hold `qty` is floored at 1 and never capped; `MaxPerBuyer` defaults to 0 (unlimited). One anonymous request can drain a tier. Seated holds have `maxSeatsPerRequest`. | Add `maxGAPerRequest`, reject above it with 400. | A1 |
| S2 | `booking/live/hub.go:156-175` | bug | When the LISTEN connection drops, `listen` reconnects with backoff but never republishes. Open streams keep heartbeating and serve pre-outage state forever. | On reconnect, publish every subscribed topic once so readers resync. | A2 |
| S3 | `server/booking/api_events.go:222` + `api_snapshot.go:68-77` | bug | `conn.Raw(...).Scan(&rows)` discards the error; an empty result is marshalled, hashed and cached with a valid ETag until the next NOTIFY. | Loader returns `(T, error)`; keep the previous snapshot on failure. | A2 |
| S4 | `server/booking/api_availability.go:31-36` | security | `to` is clamped but `from` is not floored at `now`; `ComputeAvailability` walks day by day from `from`. `?from=1200-01-01` is ~300k iterations on a public GET. | Clamp `from = max(now, from)`. | A2 |
| S5 | `server/booking/api_summary.go:183` | perf | The board stream resolves slug → event id per subscriber on every change: up to 50 lookups × viewers per hold, against a 20-connection pool. | Resolve once at stream open; reuse ids. | A2 |
| S6 | `server/booking/api_events.go:174-177` | perf | `?section=` bypasses the snapshot cache and ETag on a public read; nothing uses the parameter. | Drop it, or filter the cached snapshot in memory. | A2 |
| S7 | `server/routes/booking.go:81-89,157-169` | security | No rate limiting on guest writes or the three SSE routes. Each `POST /waitlist` from a fresh `sid` is a new row. | Per-IP limiter on the guest group; per-IP concurrent-stream cap. | A3 |
| S8 | `server/booking/api_snapshot.go:60-68` | bug | The loader runs under `entry.mu` with no context or deadline; one stuck query pins every reader of that key. | `context.WithTimeout` on the load; return the previous snapshot on timeout. | A2 |
| S9 | `server/booking/api_tickets.go:347-372` | security | `claimOffer` checks neither ownership nor the event in the path (HTTP face of K4). | Same fix as K4. | A1 |
| S10 | `cmd/server.go:86-90` | bug | `srv.Shutdown` waits for SSE handlers that never return; every stop blocks the full 10 s then hard-kills. | Cancel a server-closing context from `RegisterOnShutdown`; select on it in each stream loop. | A3 |
| S11 | `cmd/server.go:39-41` | ops | Database connect failure logs and `return nil`: exit 0, no restart from Docker/systemd. | `return err`. | A3 |
| S12 | `server/booking/api.go:38` | bug | `NewAPI` appends to the global guest-migrator registry on every call; no dedupe. | Key the registry by name. | A3 |
| S13 | `server/server.go:167-175` | bug | Unknown URLs render the 404 page with HTTP 200; `templ.Handler` leaves `Status` at 0. | `templ.WithStatus(http.StatusNotFound)`. | A3 |
| S14 | `server/server.go:39-44` | security | `http.Server` has no `ReadHeaderTimeout`, `IdleTimeout` or `MaxHeaderBytes`. | Set the first two; leave `WriteTimeout` unset for SSE. | A3 |
| S15 | `server/booking/api_events.go:103-108`, `api_schedules.go:143-147` | bug | Every create failure is reported as 409 "slug likely taken". | Check SQLSTATE `23505`; 500 otherwise. | A3 |
| S16 | `server/booking/api_http.go:24-28` | smell | Success is JSON, every error is `text/plain` via `http.Error`. | `writeError(w, code, msg)` with the JSON envelope. | C1 — changes what the pages parse; land it with the frontend pass. |
| S17 | `server/booking/api_events.go:138-141` | perf | `getEvent` is uncached with an N+1 over tiers via `tierRemaining`; the showing page calls it on load. | One grouped query, or route through the snapshot cache. | A2 |
| S18 | `server/booking/api_availability.go:21-81` | perf | The polled availability path is uncached while the stream is cached; every client that hits one `onerror` polls it at 15 s forever. | Serve the default range from `a.availability` with an ETag. | A2 |
| S19 | `database/database.go:45,57` | ops | GORM logs every statement at Info, unconditionally. | Level from env; default Warn. | A3 |
| S20 | `cmd/root.go:69-71` | security | `http.ListenAndServe("localhost:6060", nil)` runs for every subcommand, ignores `--profile`, ignores its error, serves `DefaultServeMux`. `net/http/pprof` is not imported, so it serves nothing today. | Import pprof, gate behind `--profile`, log the error. | A3 |
| S21 | `server/booking/api.go:42` | smell | The hub reads `DATABASE_URL` from the environment, not the injected context, and has no `Close`. Test schemas share one NOTIFY channel. | Pass the DSN through `BackEndContext`; add `Hub.Close`. | A3 |
| S22 | `cmd/scheduler.go:66-87` | ops | Binds `:8080` with a nil handler, no health route, and never calls `srv.Shutdown`. | Register `/healthz` and shut it down, or drop the listener. | A3 |
| S23 | `server/booking/api_appointments.go:144` | security | The cancel token, sole authorisation for a guest booking, travels in the query string. | Read it from the JSON body. | A3 |
| S24 | `server/booking/api_webhook.go:113` | smell | `{provider}` is persisted verbatim with no allowlist. (Replay is handled: `IdempotencyKey` is unique and the compare is `hmac.Equal`.) | Validate against a known set. | A3 |
| S25 | `server/booking/api.go:74-120` | smell | 25 one-line accessors that re-thread the same three fields. | Make handlers methods on `*API`; delete the accessor layer. | — kept: the accessor layer is what lets the route table live outside the domain package; 25 one-liners are the price of that boundary. |
| S26 | `server/server.go:60-84,141-158` | smell | `games.Mount` keeps its routes inside the domain (and admits no CSRF on its POSTs); health/auth/users/pages are inline; `users.HandleFunc("GET /")` is the subtree hazard `routes/booking.go` documents. | Move games and the shared API into `routes/`. | A3 |
| S27 | `server/server.go:103-121` | gap | Health reports Valkey but never pings Postgres; a `GET /api/v1/protected` echo route ships beside it; version is hardcoded. | DB ping; delete the echo; build-stamped version. | A3 |

Test gaps: ETag/304 on `/events/{slug}/seats`; GA quantity bound; releasing another owner's token; claiming an offer against the wrong event; webhook replay at HTTP level; hub reconnect.

### 2.3 Web pages (`internal/web`)

No constraint violations: no sign-in link, no "government", no real place name in copy, routes name systems. Two adjacent notes: `catalog.go` pins `TZ = "Europe/Lisbon"` and `Currency = "USD"`; the timezone is returned by `GET /appointments/schedules/{slug}`. Not displayed as a name today. Generated `*_templ.go` files are all present and current; Tailwind v4 auto-detection reaches the templ files including class strings inside `<script>`.

| # | Where | Sev | Finding | Fix | Step |
|---|---|---|---|---|---|
| W1 | `pages/aurenor_showing_script.templ:64` | bug | `openStream()` is only called inside `if (this.kind === 'seated')`. A GA showing opens no stream and no poller; `remaining` is frozen from first paint. | Open the stream for both kinds; GA re-runs `loadEvent()` per tick. | A2 |
| W2 | `pages/aurenor_theater_live.templ:98` | bug | `if (this.showings.length > 0) this.openStream()`. A failed or empty first `/events/summary` leaves the board dead until reload. | Always open the stream; let `apply()` fill the board. | A2 |
| W3 | `pages/aurenor_showing_script.templ:170`, `aurenor_theater_live.templ:141`, `aurenor_service_script.templ:109` | bug | `stream.onerror` closes unconditionally. EventSource fires `onerror` while retrying (`readyState === CONNECTING`), so one blip retires live updates for the session. | Close and fall back only when `readyState === EventSource.CLOSED`. | A2 |
| W4 | `pages/aurenor_service_script.templ:75` vs `api_availability.go:87` | bug | Initial `GET /availability` returned `now + WindowDays` (21/30/45) while the stream's first frame was capped at 14 days; the date strip truncated after load. Copy said "next two weeks". | Done by dropping the 14-day cap: the shared snapshot covers the schedule's whole window and the default GET serves that same snapshot with an ETag. The cost is per change, not per reader. | A2 |
| W5 | `pages/aurenor_showing_script.templ:62` | bug | `init()` never reads `window.aurenorHolds()`; reloading the seat screen loses the receipt, countdown and Release button, and own seats render as taken. | Restore `this.hold` from storage in `init()`; restart the countdown if live. | A2 |
| W6 | `pages/aurenor_script.templ:76`, `aurenor_service_script.templ:30` | bug | `localStorage.setItem` is uncaught inside the `try` of `place()`/`confirm()`; a quota error reports a successful hold as "Could not reach the box office". | Try/catch the writes; treat storage loss as a degraded panel. | A2 |
| W7 | `pages/aurenor_showing_script.templ:278` | bug | `seatLimit` returns `Math.max(1, ...)`, so a sold-out GA showing renders an enabled Hold button that always 409s. | Branch on `remaining === 0`; sold-out panel. | C1 |
| W8 | `pages/aurenor_service_script.templ:134`, `aurenor_theater_live.templ:172` | ux | When the chosen day empties, `apply()` silently jumps to `days[0]`. | Set the live-region notice. | C1 |
| W9 | `pages/aurenor_showing.templ:113` | a11y | `role="gridcell"` on the `<button>` discards the button role and makes `aria-pressed` meaningless. | Wrap: `<div role="gridcell"><button aria-pressed>`. | C1 |
| W10 | `pages/aurenor_datestrip.templ:14` | a11y | `role="tablist"/"tab"` with no `aria-controls`, no tabpanel, no roving tabindex, no arrow keys; 45 tab stops on the consulate strip. | Roving tabindex + arrow keys, or plain buttons with `aria-pressed`. | C1 |
| W11 | `pages/aurenor_showing_script.templ:256,259`, `aurenor_showing.templ:143` | a11y | "taken" ≈ 3.3:1 and "not for sale" ≈ 1.4:1 contrast at 11 px. | Darken both. | C1 |
| W12 | `pages/aurenor_service.templ:87`, `aurenor_showing.templ:200` | a11y | Nothing moves focus or announces after a booking or hold succeeds; focus falls to `<body>`. | `tabindex="-1"` + `role="status"` on the panels; focus them. | C1 |
| W13 | `pages/aurenor_service.templ:29` | ux | The service screen never names the institution; two hospitals' cardiology pages are identical above the fold. | Carry the venue name into `ServiceCard`; render as subtitle and in the back link. | C1 |
| W14 | `pages/aurenor_script.templ:68,101` | bug | My-holds pruning uses the raw client clock (no `clockOffset`) and the countdown ticks every 30 s. | Global offset next to `aurenorCSRF`; tick per second. | C1 |
| W15 | `pages/aurenor_showing.templ:155` | ux | The sticky buyer bar stacks chips, notice, two inputs, error, totals, button and fine print; on a phone it covers a third of the viewport. | Keep count/total/button sticky; inputs inline above. | C1 |
| W16 | `pages/aurenor_showing_script.templ:341,347,364` | bug/a11y | After a failed auto-pick `autoPicked` stays true and the stepper drifts from the selection; auto-pick moves the tab stop but not focus. | Clear the flag on the no-run branch; reuse the focus block. | C1 |
| W17 | `pages/aurenor_theater_live.templ:71,167` | dead/ux | "No showings on <day>" panel and `nextDayWithShowings` are unreachable (days only exist when populated); an empty board renders as a red `role="alert"`. | Delete the dead branch; add a neutral `empty` state. | C1 |
| W18 | `pages/aurenor_helpers.go:182`, `components/header.templ:5,76` | dead/bug | `apptBookerAttr` has no callers; `NavItem`'s active branch is unreachable (`isActive` always false); `max-w-9xl`/`lg:max-w-12xl` do not exist in Tailwind and emit nothing. | Delete; compute active from path; use `max-w-7xl`. | C1 |
| W19 | `tailwind.config.js`, `gulpfile.js:22` | dead/bug | The config is inert under v4 and its `content` glob points at a path that does not exist; `npm run dev` throws on the unregistered `go-server` task. | Delete the config; pin `@source` in `main.css`; drop `go-server`. | A3 |
| W20 | `routes/booking.go:182`, `components/nav.go:17` | ux | `/aurenor` 404s and no nav entry exists; a deep-linked visitor has no route back. | Mount an `/aurenor` index listing the four systems. | C1 |
| W21 | `web/page.go:30` | constraint | A stale doc comment says the shell renders a sign-in link for anonymous visitors. | Rewrite: the account slot renders nothing when `User` is nil. | A4 |

Test gaps: no test at all for `/aurenor/theaters/{slug}/showings/{event}` (200 for a valid slug, 404 for unknown, `x-data` carries the seeded slug); nothing counts stream URLs per rendered page to assert "one screen, one connection".

### 2.4 Docs and ops

| # | Where | Sev | Finding | Fix | Step |
|---|---|---|---|---|---|
| O1 | `Makefile:9,27,40-51` | ops | `OUTFILE := builds/clair.bin` but `build:` writes `builds/clair`; `run`, `migrate`, `seed` run a binary `build` never produces, and `run` passes `--verbose`, which is not registered. | One output name; drop `--verbose` or register it. | A3 |
| O2 | `Makefile:82`, `.github/workflows/test.yaml:52` | ops | `make test` is `go test ./test/... -v`; CI never runs the 11 `_test.go` files under `internal/`. | `go test -race ./...`. | A4 |
| O3 | `Dockerfile` | ops | No `CMD`/`ENTRYPOINT`; compose papers over it with `command: ./clair.bin server`. `COPY . ./` precedes `COPY go.mod go.sum`, so dependency layers never cache. Six final-stage `ARG`s do nothing. | `ENTRYPOINT ["./clair.bin"]`, `CMD ["server"]`; reorder copies; delete the ARGs. | A3 |
| O4 | `docker-compose.yaml`, `docker-compose.dev.yaml` | ops | No compose service migrates, seeds or runs the scheduler; `DATABASE_AUTO_MIGRATE` is false, so the server boots against an empty schema and the reaper never runs. `docker-compose.yaml` pulls `ghcr.io/…:latest` while `make docker` tags `clair`. | One-shot `migrate` with `service_completed_successfully`, optional `seed`, a `scheduler` service; a `build:` stanza. | A3 |
| O5 | `Makefile:22-24`, `README.md:13-19` | ops | Fresh clone: `make build` runs `npm run css` but nothing runs `npm install`. | `npm ci` in `assets:` and README step 2. | A3 |
| O6 | `internal/clair/sentry.go:13,20` | bug | Sentry dropped events when `ENVIRONMENT == "development"`, which was the default; production reported nothing. | Resolved by removal on 2026-09-03: the owner judged Sentry stale. Module, code, env var and docs are gone. | A3 |
| O7 | `.env.example` | gap | `PUBLIC_PATH`, `ENVIRONMENT`, `TEST_DATABASE_URL` are read in code and documented nowhere. The untracked `.env` still carries Discord/AWS keys and lacks `SESSION_KEY`, `DATABASE_AUTO_MIGRATE`, `BOOKING_PAYMENT_WEBHOOK_SECRET`, `SEED_HOST_EMAIL`. | Add the rows; regenerate `.env`. | A3 |
| O8 | `go.mod:39` | ops | `go mod tidy -diff` exits 1: `jackc/pgx/v5` is imported directly by `hub.go` but marked indirect. | `make tidy`; add `tidy -diff` as a CI gate. | A4 |
| O9 | repo root | gap | No lint config; CI runs generate, vet, build, test and nothing else. `build.yaml` is named "Build and Deploy", only builds, tags `latest`, and does not run on PRs. | golangci-lint (staticcheck + errcheck baseline); SHA tags; PR-triggered `docker build`. | A4 |
| O10 | `.dockerignore` | smell | Excludes `vendor` but not `node_modules/`, `builds/` or `.git/`; still references `fly.toml`. | Mirror `.gitignore`. | A3 |
| O11 | `internal/aurenor/seed.go:79-87`, `catalog.go:261-280` | bug | Event slugs are `theater-film-HHMM` (date-free) and `startAt` is `now + DayOffset` with `DayOffset ∈ 1..7`. Re-runs skip all 17 by slug. Probe: eight days later, 0/17 showings are in the future. | Date-bearing slugs plus a scheduled job keeping an N-day window. | B1 |
| O12 | `internal/aurenor/catalog.go:74-76` vs `pages/aurenor_helpers.go:23-47` | smell | The twelve service names are typed twice (catalog bare, page with blurbs); films are correctly single-sourced. Only rendered ⊆ seeded is asserted. | Move blurbs into the catalog; assert both directions. | B1 |
| O13 | `internal/aurenor/catalog_test.go` | gap | Only "16 schedules" is asserted; the 84 rules and 17 events are checked by `len != 0`. | Assert all three. | B1 |
| O14 | `README.md:94-104` | stale | README's roadmap leaves hold truth and the UX passes unchecked; `aurenor.md` marks them done. | Replace with a pointer. Done in this pass. | A5 |
| O15 | `docs/superpowers/*` | stale | The supersession chain (08-03 → 08-04 plan → 08-04 ux → 08-06 design → 08-06 plan) is stated nowhere end to end. The 08-06 plan has 66/66 boxes unchecked and its last task asks for edits that already exist; the 08-04 plan's status line says "Steps 1–3 done" and its "Verified facts" claim `database.New` self-migrates (it does not since PR #18). The two specs disagree on the simulator package name (`internal/simulator` vs `internal/sim`). Five cited handler paths were renamed. `docs/` is untracked. | Status banners on every doc; archive the two executed plans; one status source; commit `docs/`. Package name: `internal/simulator`. | A5 |
| O16 | `Makefile` | gap | No `run-server`/`run-scheduler` targets; the 08-04 spec's manual test step names one. | Add both. | A3 |

## 3. Roadmap v2

Three phases. **A** makes what exists hold and makes it runnable by someone else. **B** completes the loop the simulator needs. **C** is polish that waits for the owner's own frontend review.

### Phase A — Stabilise

- **A1. Engine hardening.** *(done 2026-09-02)* K1–K5, K8–K15, K18, K19, K22, S1, S9 (K21 retagged to A3). Concurrency tests for release-vs-reaper and `appointments.Book`; a Lisbon fall-back DST test. Definition of done: every mutation that writes `booked` or `held` carries the capacity predicate; every `RowsAffected`/`res.Error` is read; a GA hold is capped; an offer is claimable only by its owner; a hold on a past showing is refused.
- **A2. Live layer resilience.** *(done 2026-09-02)* S2–S6, S8, S17, S18, S21, K16, K17, W1–W6. Definition of done: a dropped LISTEN connection resyncs every open stream; a failed load never replaces a good snapshot; the GA showing, the empty board and any screen after a network blip keep receiving updates; a reload keeps the buyer's own hold; the appointment strip and stream agree on the window.
- **A3. Operator path and hardening.** *(done 2026-10-21)* O1, O3–O7, O10, O16, S7, S10–S16, S19, S20, S22–S27, W19. Definition of done: `git clone && make build && make migrate && make seed && make run` works on a fresh machine; `docker compose up` migrates, seeds, runs server and scheduler; a bad `DATABASE_URL` exits non-zero; shutdown drains SSE; unknown URLs return 404; health pings Postgres.
- **A4. CI and quality gates.** O2, O8, O9, W21. `go test -race ./...`, `go mod tidy -diff`, golangci-lint, PR-triggered `docker build`.
- **A5. Docs consolidation.** O14, O15. `aurenor.md` is the only status source; every spec and plan carries a status banner; executed plans move to `archive/`; `docs/` is committed.

### Phase B — Complete the loop

- **B1. Rolling calendar.** O11–O13. Date-bearing event slugs; a scheduler job that keeps a 14-day window of showings and prunes nothing (retention does that); catalog as the single source for service names and blurbs.
- **B2. Theater checkout and cancel.** K6, K7. A mock-payment checkout that captures a hold into a paid order with issued tickets, so the theater has a terminal state in the UI; `CancelOrder`/`RefundOrder` in the engine; a my-tickets panel. Prerequisite for simulator churn.
- **B3. Retention.** `PurgeOldBookingData(db, now)` deleting by business time (`event.start_at`, `slot.end_at`), status-guarded on money rows, batched, with supporting indexes; a gated daily scheduler job; never touches seeded schedules, rules, tiers or seats. Small, and the safety net that keeps B4's output bounded.
- **B4. Simulator.** K20, K22. Package `internal/simulator`. Demand-curve fill through in-process domain calls; converges toward a target occupancy per showing/slot rather than draining to sold out, using B2's cancel path and abandoned holds for churn; `sim:` owner-ref namespace; prod-DSN guard; advisory-lock single runner; wired into `clair scheduler` behind `SIM_ENABLED`. The 2026-08-04 spec's sections 5.1–5.9 remain the detailed design, corrected for: `database.New` no longer migrates; `HoldSeatGroup` returns `[]BookingHold`; `ReleaseHolds` is owner-scoped; NOTIFY triggers mean sim writes reach open boards with no extra work.

### Phase C — Polish

- **C1. Frontend accessibility and UX pass.** W7–W18, W20. Waits for the owner's own review of the current screens.
- **C2. Host surface.** The host routes (create event/schedule, rules, exceptions) exist but no page drives them; a seeded host cannot see or manage anything.
- **C3. Deployment.** SHA-tagged images, a real deploy step, migration ordering, per-service healthchecks.
- **C4. Map and game.** Unchanged and last. Depends on nothing above and blocks nothing; explicitly out of scope until A and B land.

### What changed from v1 and why

- **Simulator moved from next to fourth-in-B.** Its own spec assumes a 14-day horizon, but the catalog produces showings only for days 1–7 at seed time and re-runs skip them; from day 8 there is nothing to fill. It also needs a cancel path that does not exist, and a rolling calendar that does not exist.
- **Retention moved ahead of the simulator.** It is smaller, it is the bound on simulator output, and it is easier to test on hand-made rows.
- **Theater checkout is now a step.** V1 said "checkout stays stubbed" and left it there. A simulator that can only hold-and-lapse produces no permanent state, and the engine has no way to undo a sale.
- **Phase A is new.** Every item in it is either a live invariant hole, a public endpoint without a bound, or a reason a fresh clone cannot run. The eight steps marked done are not defensible until the tests that cover them run in CI.
- **Map and game is labelled out of scope.** Listing it beside operational work made the roadmap read as product exploration.
