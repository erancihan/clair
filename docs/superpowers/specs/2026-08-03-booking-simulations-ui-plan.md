# North Coast Booking Simulations — One Waterfall Implementation Plan

> **Superseded — kept for history.** The setting became the fictional nation
> **Aurenor** (`docs/worldbuilding/aurenor.md`) and the routes moved from
> `/booking-examples/{system}` to `/aurenor/{institution}` collections. The
> naming, routes, and page names below are all out of date. The current plan is
> `docs/superpowers/specs/2026-08-04-aurenor-implementation-plan.md`. What still
> holds from this document is the phase *ordering* — UI first, then the
> simulator, then retention — and the grounding notes in section 0.

Three imaginary North Coast institutions — **Theater** (seat booking), **Hospital** (doctor appointments), **Municipality** (municipal service appointments) — as one portfolio feature over the existing `booking` reservation kernel. The order is strict and non-negotiable per the directive: **UI first, then the backend simulator, then 1-month retention.** Every early phase ships something demoable using only endpoints that already exist; each later phase closes exactly one named gap.

---

## 0. Grounding (verified against the tree)

- `booking.Mount(app, s.context)` is already wired at `internal/server/server.go:81`, already behind `OptionalAuthMiddleware` (`server.go:72`). **No `server.go` edit is required** to add booking's HTML surface.
- Today `booking.Mount` registers **JSON routes only** — there is no HTML page in the domain. The page-mount pattern to copy is `internal/server/games/mount.go` (`r.Group(...) → templ.Handler(web.Base(api_auth.PageShell(w,r,title), pages.X()))`).
- `internal/web/pages/` has no booking pages yet (home/games/projects/login/requester/404 only).
- `NavLinks` (`internal/web/components/nav.go`) has Home / Projects / Games — one appended line adds booking.
- **Endpoints that already work** and everything early leans on:
  - Public reads: `GET /appointments/schedules/{slug}/availability`, `GET /events/{slug}`, `GET /events/{slug}/seats`.
  - Guest writes (behind `api_auth.CSRF()`): `POST /appointments/schedules/{slug}/book`, `POST /appointments/cancel?token=`, `POST /events/{slug}/seats/hold`, `POST /events/{slug}/tiers/{tierID}/hold`, waitlist joins/claim.
  - Authed: `GET /appointments/me`, `POST /events` / `POST /appointments/schedules` (create, owner-scoped), `GET /appointments/schedules` (owner-scoped).
- **CSRF is mandatory on every booking write** — the shell publishes `<meta name="csrf-token">`; every mutating `fetch` sends `X-CSRF-Token`. Do **not** copy tic-tac-toe's tokenless fetches.

## Information-architecture decision (route names the system, not the institution)

Each design independently assumed it owned `GET /booking` plus its own institution sub-path. **Rejected.** These are example cases of two underlying booking *systems*; the route names the **system**, never the fictional institution. North Coast Hospital and North Coast Municipality differ only in example data, so they share one route — "municipality" never appears in a path or as a label.

Two systems, two pages. The **Projects page is the portal/hub** — there is no separate hub page and no new nav entry; the examples are reached from Projects:

```
GET /booking-examples/appointments   → appointment-scheduling system. One page.
                                        Browse every example schedule (tagged
                                        North Coast Hospital / North Coast
                                        Municipality), book inline.
GET /booking-examples/seating         → seat-reservation system. One page.
                                        Pick a showing, seat map, hold inline.
```

Institution is a **tag/filter only**. The booking flow lives on the system page as Alpine state keyed by the selected schedule slug — no per-schedule route, so the once-only `cancel_token` survives without a page load. Optional `?schedule={slug}` query pre-selects a card for deep-linking without adding a route.

> The Phase B/C and gap sections below were drafted against the earlier `/booking/{institution}` sketch. Their page content, Alpine flows, backend gaps, simulator, and TTL design all still stand — only the **web routes collapse to the two flat pages above, with institution as a tag**. Backend/API paths introduced in Phase B are named when that phase starts.

---

## Phase A — UI first (ships on existing endpoints only)

### A0 — Projects portal section + the two system pages (no data)

1. Edit `internal/web/pages/projects.templ`: add a "Booking system simulations" section (heading + short intro + two links) after the projects card grid — one to `/booking-examples/appointments`, one to `/booking-examples/seating`. Build both pages in this same change so neither link 404s.
2. New `internal/web/pages/booking_appointments.templ` (`templ BookingAppointments()`) and `internal/web/pages/booking_seating.templ` (`templ BookingSeating()`): standard 4-div shell + heading; A0 ships them as skeletons, A1/A2 fill them.
3. Register `r.Group("booking-examples", …)` in `internal/server/booking/mount.go` alongside `mountAppointments`/`mountEvents`: `GET /appointments` → `pages.BookingAppointments()`, `GET /seating` → `pages.BookingSeating()`. New imports: `net/http`, `github.com/a-h/templ`, `internal/web`, `internal/web/pages` (`api_auth` already imported). No `server.go` edit (`booking.Mount` already at `server.go:81`). No new `NavLinks` entry (Projects is the portal).
4. `go generate ./...`.

**Deliverable:** Projects section + both system pages render live in the shell (header, dark-mode for free). No backend dependency, no dead links.

### A1 — Appointment-scheduling page (the simplest full sim)

`GET /booking-examples/appointments` → `pages.BookingAppointments()`. One page, everything inline. First because the free appointment path (`availability → book → cancel-by-token`) is the only end-to-end-complete engine path with **no new backend**.

- **Catalog (static, compiled-in):** a Go `catalog` slice, each entry `{slug, institution, title, blurb, tz}` with `institution` ∈ {North Coast Hospital, North Coast Municipality} (G1: no discovery endpoint). Rendered as cards, filterable by the `institution` tag (Alpine `x-model`). Hospital entries = doctors/departments; Municipality entries = services (driver's licence, ID renewal, fingerprint).
- **Inline booking flow** (one Alpine `bookingFlow()` state machine — no navigation, so the once-only `cancel_token` survives):
  - `slots`: `GET /appointments/schedules/{slug}/availability?from=&to=` (real). Group UTC slots by local date using the catalog `tz`; loading/empty/error via `x-cloak`.
  - `details`: guest `name`+`email` form (free schedules have no server hold; selection is client-side optimistic).
  - submit: `POST /appointments/schedules/{slug}/book` `{start_at, guest_name, guest_email}` + `X-CSRF-Token`. `201` → stash incl. `cancel_token` to `localStorage`, advance; `409` → slot taken, refetch; `400/403/5xx` → inline error, keep form.
  - `confirm`: show the `cancel_token` prominently ("save this — the only way to cancel"), copy-link, cancel, book-another.
- **Your bookings** (this browser): a panel on the same page merging `localStorage` with best-effort `GET /appointments/me` (200 merge / 401 local-only, G3). Cancel → `POST /appointments/cancel?token=` + CSRF; always-200 (G7) → non-committal "cancellation submitted".

Both North Coast Hospital and North Coast Municipality run on this one page — same engine, different tagged data.

**Deliverable:** full honest guest lifecycle (browse → book → confirm → view → cancel) for both appointment examples on endpoints that exist today.

### A2 — Seat-reservation page (theater seat map; browse + checkout stubbed)

`GET /booking-examples/seating` → `pages.BookingSeating()`. One page, inline. (Extract the shared appointment widgets from A1 into `internal/web/components/booking_*.templ` first if useful, but seating shares little with the appointment flow.)

- **Showings:** film/showing cards from a **seeded feed** (hardcoded list or static JSON) — `GET /events` (list) does not exist. Films grouped client-side; swap to real `GET /events` when it lands.
- **Seat map (core):** `seatMap()` Alpine component (copy the `games_tictactoe.templ` `alpine:init` pattern). Selecting a showing reads real `GET /events/{slug}` (tiers + remaining) and `GET /events/{slug}/seats?section=`; grid grouped by `row_name`, colored by `state`; selection capped by `max_per_buyer`; tier legend + running total; client `findTogether(n)` adjacency; client-side 10-min countdown (seat-hold has no `expires_at`). Real `POST /events/{slug}/seats/hold` (+ `.../tiers/{tierID}/hold` for GA). Sold-out → refetch / waitlist. Buildable today **up to "held."**
- **Checkout / receipt / tickets:** built inline as UI but **blocked** — no browser checkout, no `GET /orders/*`. Unblocked in Phase B (sim mock-pay + sim reads).

**Deliverable:** real browse → pick-seats → hold demo, no engine change; checkout awaits Phase B.

---

## Phase B — Backend simulator (fill data slowly; every entry 1-month TTL)

Runs in **`SchedulerCmd`** (`internal/cmd/scheduler.go`), not in `ServerCmd` (N web replicas ⇒ N concurrent generators). New package `internal/sim/` (`Tick`, backfill, demand curves) importing `appointments`/`ticketing`/`booking`.

**Prerequisite (real, in-code blocker):** `SchedulerCmd` today constructs `clair.NewEventLoop()`, which calls `logger.Fatal` when `DISCORD_CHANNEL_ID` is empty — the scheduler process cannot boot without Discord config. **Gate the Discord loop behind its own env flag first**, turning the scheduler into a general periodic-jobs worker. Then fill the two `// TODO:` stubs with `database.New(ctx)` and `utils.NewValKeyClient(ctx)` (the exact calls already in `ServerCmd`).

Register jobs next to the existing `Every(5).Seconds().Do(el.Loop)`:

```go
scheduler.Every(1).Minute().Do(func() { booking.ReapExpiredHolds(db, time.Now()) })   // already written, currently unwired
scheduler.Every(1).Hour().Do(func()   { booking.PurgeOldBookingData(db, time.Now()) }) // new (Phase C)
if simEnabled() { scheduler.Every(45).Seconds().Do(func() { sim.Tick(db, time.Now()) }) } // hard-gated
```

### B1 — Write-through, never straight-to-models

The invariant `booked + held + blocked ≤ capacity` is maintained **incrementally** by the kernel (enforced only in each mutation's WHERE clause, not a DB constraint). A raw INSERT of an appointment/ticket desyncs `booked`/`remaining` and silently breaks availability. So the generator writes through the domain APIs:

- **Appointments (free):** `appointments.Book(db, BookInput{Schedule, StartAt, GuestName, GuestEmail}, now)` — one applicant per transaction; `validateOpen`/lead-time/window keep sim data consistent with what availability returns.
- **Theater (paid → 'paid' with issued tickets):** `ticketing.HoldSeatGroup(...)` / `ticketing.HoldGA(...)` → **immediately** `booking.CaptureOrder(db, order.ID, mockPayment("sim-"+uuid), now)` so holds never outlive a tick (else the 1-min reaper releases them and "sold" evaporates). A distinct synthetic `owner_ref` per simulated buyer (one active hold per `(inventory, owner_ref)` — reused refs collapse into one hold).
- **Straight-to-models only for catalog seeding** — `BookingSchedule`+rules and events via `ticketing.CreateEvent` (tables that own no kernel counter). Seed a fixed roster once (3 theaters × showtimes, 4 clinics, 3 municipal services), then only *fill*.

### B2 — Slow accretion via reconcile-toward-target

One shared demand model: `target(slot, now) = round(C · finalFraction(slot) · progress(now, T))`, computed in the **venue's IANA tz**. Each `Tick` shuffles open future slots and closes only the current deficit under a **global throttle** (~≤20 transactions/tick). "Slowly, at intervals" = the target grows with wall-clock time; each tick closes a sliver.

- **Theater:** `finalFraction` = time-of-day (morning 0.15 → night 0.92) × day-of-week (weekend ~1.0) × jitter, clamp ≤0.97. `progress` = logistic on `daysUntil`: weekend shows `open:7,width:2` (fill across the week), weekday `open:2,width:1.2` (surge ~48h out). 2–3 adjacent seats/txn (adjacency is a query on `Section/RowName/Number`), captured immediately.
- **Hospital/Municipality:** per-schedule `simProfile` (popular family doctor `baseDemand:0.95`, niche desk `0.3`; some services legitimately hit 1.0). `progress` `open:21,width:6` (clinic) / `open:14,width:5` (municipal). ≤3 applicants/slot/tick.

### B3 — Unblock the theater checkout (sim only)

Add a **dev-only, CSRF-guarded** `POST /booking/theater/orders/{id}/pay` that calls `booking.CaptureOrder` directly (supplying `BookingPayment{IdempotencyKey:"sim-"+uuid}`, bypassing HMAC), plus sim reads `GET /orders/{id}` and `GET /orders/me` querying `BookingOrder`/`BookingTicket`. This lights up theater Pages 3–5. Each is one named gap, sim-gated.

### B4 — Historical backfill (one-shot)

The live loop can only book **future** slots (`Book`/`HoldSeatGroup` reject past times). To demo retention immediately, a one-shot backfill seeds past-dated rows: replicate `ensureSlot → EnsureInventory → BookDirect(+1) → insert` in one tx, then overwrite `CreatedAt`/`StartAt` across the last 30–60 days. This is the **one sanctioned straight-to-models path** and it must still increment the kernel counter. Same `SIM_ENABLED` gate; run once.

### B5 — Hard gating

`SIM_ENABLED=true` (default off) **and** refuse to run against a production DSN (deny known prod hosts). Fake bookings in prod is the worst-case failure. Enforce a scheduler singleton (single replica, or `pg_try_advisory_lock` per job — Valkey is nil-able/fail-open, so don't depend on it for the lock).

---

## Phase C — 1-month retention

### Mechanism: Postgres sweeper, **not** Valkey-TTL

Confirmed in code: `NewValKeyClient` returns `nil` when unset/unreachable, is used only for `PING`, and nothing writes keys. Bookings are relational Postgres aggregates guarded by an invariant. A 30-day TTL on a lossy, fail-open Valkey mirror would evict the mirror while the authoritative Postgres tables keep growing. Valkey-TTL is right only for the minute-scale hold/offer accelerator, never month retention.

### Predicate: business time, not `created_at`

- Event subtree purgeable when `event.start_at < now − 30d` (show is over).
- Appointment/slot subtree purgeable when `slot.start_at < now − 30d` — purge **slots**, keep the long-lived `BookingSchedule` + rules.
- Orphan kernel rows (`released`/`committed` holds, `expired`/`cancelled` orders with no live parent) by `created_at < now − 30d`.

`CreatedAt` and `StartAt/EndAt` exist on every relevant model — **no schema change needed** for a flat business-time policy.

### Sweep: ordered bottom-up deletes (there is no `ON DELETE CASCADE` anywhere)

`booking.PurgeOldBookingData` (new `internal/booking/retention.go`), mirroring `ReapExpiredHolds` (batched `Limit(500)`, loop-until-a-pass-deletes-<batch):

- **Phase A (read-only):** collect purgeable `eventIDs` (`booking_events.start_at < cutoff`) and `slotIDs` (`booking_slots.start_at < cutoff`), each `LIMIT 500`.
- **Phase B (one tx, scoped to those roots), delete children before parents:** tickets → appointments → order_items → payments → holds → waitlist_entries → orders → inventories → seats → ticket_tiers → events → slots.
- **Final independent pass:** orphan holds/orders by status + `created_at < cutoff`, guarding that no purged inventory still has an `active` hold.

Prefer `= ANY($1)` array params over sprawling `IN (...)` lists (`PrepareStmt:true` bloats the statement cache on variable-length INs).

**Long-term alternative:** add `constraint:OnDelete:CASCADE` FK tags via a hand-written migration (AutoMigrate will **not** retrofit cascade onto existing FKs) — then Phase B collapses to "delete the roots." Ship the ordered delete now; add cascade later.

---

## Backend gaps (consolidated, ranked)

**Appointments (Hospital + Municipality):**
1. G1 — no public discovery endpoint → *static compiled-in catalog*; target `GET /appointments/catalog`.
2. G2 — no public schedule-metadata read; availability carries no name/tz/duration/`requires_payment` → *baked into catalog, free-only assumption*; target public `GET /appointments/schedules/{slug}` subset. **2b (gov):** `BookingSchedule` has no structured fields for required documents / office address / category / "fee at counter" — crammed into `Description` today.
3. G3 — no guest lookup by token/email → *localStorage, this-browser-only*.
4. G6 — no mailer; `cancel_token` returned once → *on-screen + write-through localStorage*.
5. G7 — cancel always-200 on unknown token → *non-committal "submitted" copy*.
6. G4/G5 — no checkout route, hold reaper unwired in prod → *avoided by free-schedule framing*.

**Theater (ticketing), ranked by how hard they block:**
1. Browser checkout/pay — capture is webhook-only (HMAC), no browser path → **sim mock-pay** (B3).
2. `GET /events` list/search/filter — no browse source → **seeded feed**.
3. `GET /orders/{id}` — no post-hold status / receipt → **sim read** (B3).
4. `GET /orders/me` — no ticketing equivalent of `myAppointments` → **sim read** (B3).
5. `POST …/holds/{token}/release` — no abandon route; backed-out seats lock for the full 10-min TTL.
6. "My waitlist offers" read — `offer_token` returned by no read route.
- Fields/models: seat-hold response has no `expires_at`; `seatView` has no `tier_id`/`price_cents`; `BookingEvent` has no film metadata; `BookingSeat` has no x/y geometry; no Venue/Salon or Showing/Screening model (all faked via slug prefix `film@salon@time` + seat `section`).

**Simulator / retention:**
- Scheduler boots-to-Fatal without Discord (gate first); no `ON DELETE CASCADE` (ordered deletes); `created_at ≠ "event over"` (use `start_at`); prod-contamination guard; local-tz demand correctness.

---

## Net shape

Phases **A0–A2 ship a fully working, honest free-appointment lifecycle on endpoints that exist today.** A3 adds a real seat-pick-and-hold demo with no engine change. Phase **B** fills data slowly through the domain APIs (invariant-safe) and unblocks theater checkout with sim-gated stubs. Phase **C** bounds every table at 30 days with a business-time Postgres sweep. UI is provably first; the backend simulator and TTL are strictly downstream.
