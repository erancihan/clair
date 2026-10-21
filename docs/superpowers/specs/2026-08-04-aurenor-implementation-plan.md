> **Status (2026-09-02):** Steps 1–3 here are built (roadmap 2–4). Step 4 was superseded by `2026-08-04-booking-ux-and-discord-removal-plan.md`, now also built. Steps 5–7 (Simulator, Retention, Map) remain the detailed design for roadmap v2 phases B and C, with corrections listed in `2026-09-02-evaluation-and-roadmap-v2.md` §3 B4: `database.New` no longer self-migrates (`clair migrate` is explicit), the simulator package is `internal/simulator`, and a rolling calendar (B1) plus an order-cancel path (B2) are prerequisites. File paths cited below predate the `api_*.go` rename and the move of the route table to `internal/server/routes/`.

# Aurenor — Master Implementation Plan (Roadmap Steps 2–4, 9–11)

This is the single, sequenced plan assembled from the six per-step plans. Steps are ordered strictly by dependency:

```
Step 1 SEED AURENOR ─┬─> Step 2 APPOINTMENTS LIVE ─┐
                     └─> Step 3 THEATER LIVE ───────┤
                                                    ├─> Step 7 MAP & GAME
A DISCORD OUT + SCHEDULER ─┬─> Step 5 SIMULATOR ────┤
  (was Step 4)             └─> Step 6 RETENTION ─────┘
        └─> B HOLD TRUTH ─> C SEAT UX ─> D APPOINTMENT UX
```
A–D are in `2026-08-04-booking-ux-and-discord-removal-plan.md`.

**Status (2026-08-04):** Steps 1–3 are done. `clair seed` creates 16 schedules, 84 rules and 17 events and a second run creates nothing; the hospital, consulate and bureau pages book real appointments; the theater page holds real seats. Step 4 is **superseded** — Discord is being removed outright rather than gated, and the seat-selection experience needs a design pass before more engine work. Both live in `2026-08-04-booking-ux-and-discord-removal-plan.md`, which runs before Steps 5–7 here.

Rules honored: **Seed before Appointments/Theater**; **the scheduler-blocker fix before Simulator**; **Retention after the scheduler fix**. The scheduler-blocker fix — duplicated in the source Simulator and Retention plans — is factored into its own **Step 4** so Steps 5 and 6 are purely additive edits on top of it.

### Reconciliations baked into this plan (see Open Questions)
- **One seeder, not two.** The source Appointments plan proposed a second seeder (`internal/server/booking/seed.go`, `booking.Seed(db)`, `hospital-`-prefixed slugs, "20 schedules"). It is **superseded** by Step 1's `internal/aurenor` seeder. The canonical schedule count is **16** (8 hospital-dept + 4 consulate + 4 bureau); "20" was a miscount.
- **Canonical slug scheme (Step 1):** appointments `<venue>-<dept>` (`solvarre-general-cardiology`), `consulate-<service>`, `bureau-<service>`; theater events `<theater>-<film>-<HHMM>` (`grand-dune-part-two-1000`). The Theater plan's `aurenor-<theater>-<n>` scheme is superseded.
- **Timezone:** `Europe/Lisbon` (Step 1's choice); tests must tolerate non-UTC `start_at`.
- **One `clair seed` command** (Step 1) seeds both appointments and theater events; the Theater plan's separate `seed aurenor` subcommand is folded in. Retention's `--backfill` attaches to this same `SeedCmd`.

---

## Step 1 — SEED AURENOR (Roadmap Step 2)

Create real, idempotent seed data keyed to the Aurenor Venue catalog slugs, delivered as a one-shot `clair seed` cobra subcommand. No UI edits, no scheduler work.

### 1.1 Mechanism
A dedicated cobra subcommand `clair seed`, mirroring `ServerCmd` (`internal/cmd/server.go:15`). A manual subcommand is re-runnable, cannot race a multi-instance boot, and is independent of the scheduler process entirely.
- `db, err := database.New(ctx)` gives a migrated DB for free — `database.New` calls `AutoMigrate(MigrationModels()...)` (`internal/database/database.go:55`). Requires `DATABASE_URL` (already in `.env`).
- No Valkey needed — every seed call takes `*gorm.DB` only.

Seed logic lives in a new **dependency-light package `internal/aurenor`**, split so the pure catalog can later be imported by `internal/web/pages` (Steps 2/3) without pulling in gorm/ticketing:
- `internal/aurenor/catalog.go` — pure data + slug helpers, **no gorm import**.
- `internal/aurenor/seed.go` — `Seed(db, now, hostEmail)` entry point, imports `models`, `ticketing`, `gorm`.

### 1.2 Slug scheme
The Venue catalog (`internal/web/pages/aurenor_helpers.go:35-43`) defines only theaters and hospitals. Consulate and bureau are flat pages with no catalog entry (`mount.go:86-87`), so they key off their route path segment.

Appointment schedule slugs (`BookingSchedule.Slug`, globally unique):

| Collection | Prefix source | Slug pattern | Count |
|---|---|---|---|
| Hospitals | `Venue.Slug` (`solvarre-general`, `gilt-coast-clinic`) | `<venue>-<dept>` e.g. `solvarre-general-cardiology` | 2×4 = 8 |
| Consulate | route segment `consulate` | `consulate-<service>` e.g. `consulate-tourist-visa` | 4 |
| Bureau | route segment `bureau` | `bureau-<service>` e.g. `bureau-drivers-licence-renewal` | 4 |

Departments/services match the mock `apptCard` titles verbatim: Cardiology/Pediatrics/General Practice/Dermatology; Tourist/Work/Student Visa + Residence Permit Renewal; Driver's Licence/ID Card Renewal + Fingerprint + Address Registration. **Total: 16 schedules.**

Event slugs (`BookingEvent.Slug`, globally unique) — one event per *showing* (a film at a specific time), because inventory is per-event: `<theater>-<film>-<HHMM>` e.g. `grand-dune-part-two-1000`, `harborlight-poor-things-1630`.

Step 1 does **not** edit `aurenor_helpers.go`; Steps 2/3 add slug fields sourced from this catalog.

### 1.3 Catalog data (`internal/aurenor/catalog.go`)
```go
const (
    TZ               = "Europe/Lisbon"      // valid IANA
    Currency         = "USD"
    DefaultHostEmail = "host@aurenor.example"
)

type RuleDef struct{ Weekday, StartMin, EndMin int } // Weekday 0=Sun; minutes from local midnight
type ScheduleDef struct {
    Slug, Name, Description                             string
    SlotDurationMin, Capacity, LeadTimeMin, WindowDays int
    Rules                                              []RuleDef
} // all seed schedules are free: RequiresPayment=false

type TierDef struct {
    Name, Kind, Section string // Kind "ga"|"seated"
    PriceCents          int64
    Capacity            int    // GA pool
    Rows, Cols          int    // seated grid → Rows*Cols sellable seats
}
type EventDef struct {
    Slug, Name, Salon string
    DayOffset         int    // StartAt = now.Date + DayOffset at HH:MM in TZ
    Hour, Minute      int
    Tier              TierDef
}
```
Fixed values:
- Hospital depts: `SlotDurationMin:30, Capacity:1, LeadTimeMin:120, WindowDays:30`, Mon–Fri 09:00–17:00 (`Weekday 1..5, StartMin 540, EndMin 1020`).
- Consulate: `30 / 3 / 1440 / 45`, Mon–Fri 09:00–16:00 (540–960).
- Bureau: `15 / 4 / 60 / 21`, Mon–Fri 08:30–16:30 (510–990) **plus** Sat 09:00–12:00 (`Weekday 6, 540–720`).
- Theater tiers: IMAX seated `2200`, standard seated `1800`, GA `1400`. Grand: Dune Part Two (Salon 1 IMAX, seated 8×15=120) at 10:00/13:30/19:00/21:45; Oppenheimer (Salon 4, GA 120) 11:15/15:00/20:30; The Northman (Salon 7, GA 96) 12:00/18:15/22:00; Poor Things (Salon 2, seated 6×13=78) 10:45/14:20/19:40. Harborlight: Dune Part Two (Screen A, GA 80) 14:00/20:00; Poor Things (Screen B, seated 5×10=50) 16:30/21:00. Showings spread over `DayOffset 1..7` (future-dated).

Helpers: `mondayToFriday(start,end int) []RuleDef`; `Slugify(s string) string`; `AllSchedules() []ScheduleDef`; `AllEvents(now time.Time) []EventDef`; `startAt(now, def)`.

### 1.4 Domain calls (`internal/aurenor/seed.go`)
```go
func Seed(db *gorm.DB, now time.Time, hostEmail string) (Report, error)
```
**Host user** (both flows need non-zero OwnerID):
```go
host := models.User{Email: hostEmail}
db.Where(models.User{Email: hostEmail}).
   Attrs(models.User{Username: "Aurenor Host", Role: "user"}).
   FirstOrCreate(&host)   // idempotent on Email (uniqueIndex)
```
**Appointments** — write models directly (not HTTP `createSchedule`, which forces caller OwnerID/session). One tx per schedule; insert rules only when the row was newly created:
```go
sched := models.BookingSchedule{Slug: def.Slug}
res := tx.Where(models.BookingSchedule{Slug: def.Slug}).
   Attrs(models.BookingSchedule{
       OwnerID: host.ID, Name: def.Name, Description: def.Description,
       Timezone: TZ, SlotDurationMin: def.SlotDurationMin, Capacity: def.Capacity,
       LeadTimeMin: def.LeadTimeMin, WindowDays: def.WindowDays,
       Active: true, RequiresPayment: false, Currency: Currency,
   }).FirstOrCreate(&sched)
if res.RowsAffected == 1 {
    for _, rl := range def.Rules {
        tx.Create(&models.BookingAvailabilityRule{
            ScheduleID: sched.ID, Weekday: rl.Weekday, StartMin: rl.StartMin, EndMin: rl.EndMin,
        })
    }
}
```
Do **not** pre-create `BookingSlot`/inventory — appointment slots are lazily materialized on first book (`internal/appointments/book.go:127`).

**Events** — `ticketing.CreateEvent` (`internal/ticketing/event.go:49`) does a plain `tx.Create` and errors on duplicate slug, so pre-check and skip for idempotency:
```go
var n int64
db.Model(&models.BookingEvent{}).Where("slug = ?", def.Slug).Count(&n)
if n > 0 { continue }

tier := ticketing.TierInput{Name: def.Tier.Name, Kind: def.Tier.Kind,
    PriceCents: def.Tier.PriceCents, Currency: Currency}
switch def.Tier.Kind {
case "ga":
    tier.Capacity = def.Tier.Capacity
case "seated":
    for r := 0; r < def.Tier.Rows; r++ {
        for c := 0; c < def.Tier.Cols; c++ {
            row := string(rune('A' + r))
            tier.Seats = append(tier.Seats, ticketing.SeatInput{
                Label: fmt.Sprintf("%s-%d", row, c+1),
                Section: def.Tier.Section, RowName: row, Number: c + 1,
            })
        }
    }
}
_, err := ticketing.CreateEvent(db, ticketing.EventInput{
    OwnerID: host.ID, Slug: def.Slug, Name: def.Name,
    StartAt: startAt(now, def), Timezone: TZ, MaxPerBuyer: 6,
    Tiers: []ticketing.TierInput{tier},
})
```
`CreateEvent` eagerly materializes a pooled `BookingInventory` per GA tier and a capacity-1 inventory per seat via `booking.EnsureInventory`, all in one tx. `Report` returns per-kind created/skipped counts.

### 1.5 Fixed vs. simulator-filled
Fixed by this seed: 1 host `User`; 16 `BookingSchedule` + weekly `BookingAvailabilityRule` (free, active); theater `BookingEvent` + `BookingTicketTier` + `BookingSeat` + `BookingInventory` at **full** capacity. Deliberately NOT seeded (left for Step 5): any `BookingAppointment` (slots stay unmaterialized), any `BookingHold`/`BookingOrder` churn / scarcity, `BookingAvailabilityException`. Seed starts everything full.

### 1.6 Tests
Pure unit — `internal/aurenor/catalog_test.go` (no DB, always runs):
- Slugs unique within `AllSchedules()` and `AllEvents(fixedNow)`.
- Every `RuleDef`: `0<=Weekday<=6`, `0<=StartMin<EndMin<=1440`.
- `time.LoadLocation(TZ)` succeeds; every `Capacity>=1`; seated `Rows*Cols>=1`; GA `Capacity>=1`; seated `Rows,Cols>0`.

Integration — `test/aurenor_seed_test.go` (`testsupport.PostgresDB(t, database.MigrationModels()...)`, skipped when `DATABASE_URL` unset):
- `TestSeedIsIdempotent`: run `aurenor.Seed` twice; equal row counts across runs for `User`, `BookingSchedule`, `BookingAvailabilityRule`, `BookingEvent`, `BookingTicketTier`, `BookingSeat`, `BookingInventory`.
- `TestSeededSchedulesAreBookable`: over `server.NewBackEnd(...).Routes()`, `GET /appointments/schedules/{slug}/availability` returns ≥1 slot; `POST /appointments/schedules/{slug}/book` (with CSRF) returns 201.
- `TestSeededEventsHaveInventory`: `GET /events/{slug}` → tiers with `remaining == capacity`; seated `GET /events/{slug}/seats` returns `Rows*Cols` entries, all `state:"free"`.
- `TestSeedSlugsAlignWithVenueCatalog`: every hospital `ScheduleDef` → `pages.FindHospital(venuePrefix)` ok; every theater `EventDef` → `pages.FindTheater(venuePrefix)` ok; consulate/bureau prefixes equal the constants.

### 1.7 Ordered tasks
1. `internal/aurenor/catalog.go` — types, constants, dept/service/film data, helpers. No gorm import.
2. `internal/aurenor/catalog_test.go`.
3. `internal/aurenor/seed.go` — `Seed(db, now, hostEmail) (Report, error)`.
4. `internal/cmd/seed.go` — `SeedCmd(ctx) *cobra.Command` patterned on `ServerCmd`: `database.New(ctx)`, read `SEED_HOST_EMAIL` (default `aurenor.DefaultHostEmail`), call `aurenor.Seed(db, time.Now(), email)`, log `Report`. (Retention Step 6 later adds `--backfill` here.)
5. `internal/cmd/root.go:62` — `rootCmd.AddCommand(SeedCmd(ctx))`.
6. `Makefile` — `seed:` target mirroring `run:` (`"${OUTFILE}" seed`).
7. `.env` / `.env.example` — document optional `SEED_HOST_EMAIL`.
8. `test/aurenor_seed_test.go`.
9. Verify: `go build ./...`, `go vet ./...`, `make db-up && make test`, `make build && "./builds/clair.bin" seed` twice.

### 1.8 Definition of Done
`clair seed` builds, runs against fresh Postgres, self-migrates, exits 0, logs created/skipped; second run changes no row counts. Exactly 16 schedules (each Active, RequiresPayment=false, host-owned, with rules); each bookable via live `/appointments` routes. Theater events for both venues with tiers + full inventory (`remaining == capacity`). Every seeded slug prefixed by a real Venue slug or the consulate-/bureau- constant. Pure + integration tests pass; `go build`/`go vet` clean; scheduler and all `*.templ` untouched.

**Correctness constraints:** schedules must be `RequiresPayment=false`; `CreateEvent` is not idempotent (guard events by slug pre-check); appointment slots left unmaterialized; timezone must be a loadable IANA string.

---

## Step 2 — APPOINTMENTS LIVE (Roadmap Step 3)

Wire `AurenorHospital`, `AurenorConsulate`, `AurenorBureau` to the real appointment engine: availability → book → confirm → cancel, plus a "my bookings" panel. Inline via Alpine, guest-allowed, CSRF-correct. Theater (`showingCard`) is Step 3.

**Prerequisite: Step 1 seed exists.** The public booking path (`loadSchedule`, `WHERE slug=?`, no owner check) works against any rows regardless of owner. The source plan's standalone `booking.Seed` is dropped — this step consumes Step 1's `internal/aurenor` catalog.

Engine constraints the seeds honor (from Step 1): `BookingSchedule.Slug` uniqueIndex; `book` refuses `RequiresPayment=true` (`appointments.go:35`); `Active` must be true or availability/book 404; `Timezone` must be valid IANA.

### 2.1 Slug scheme — apptGrid → real schedules
Every `apptCard` gains a `ScheduleSlug` equal to the API `{slug}`, using Step 1's canonical scheme:

| Page | Card | Schedule slug |
|---|---|---|
| Hospital `solvarre-general` | Cardiology / Pediatrics / General Practice / Dermatology | `solvarre-general-<dept>` |
| Hospital `gilt-coast-clinic` | (same four) | `gilt-coast-clinic-<dept>` |
| Consulate | Tourist / Work / Student Visa; Residence Permit Renewal | `consulate-tourist-visa`, `consulate-work-visa`, `consulate-student-visa`, `consulate-residence-permit-renewal` |
| Bureau | Driver's Licence / ID Card Renewal; Fingerprint; Address Registration | `bureau-drivers-licence-renewal`, `bureau-id-card-renewal`, `bureau-fingerprint`, `bureau-address-registration` |

Hospital departments map to a **per-venue** slug (`v.Slug+"-"+deptSlug`) so each hospital books its own inventory. Consulate/Bureau are flat singletons. The exact slug strings must be produced by the same `internal/aurenor` catalog the seeder uses, so DB rows and buttons never drift.

### 2.2 Data-model edits (`internal/web/pages/aurenor_helpers.go`)
1. Extend `apptCard`:
   ```go
   type apptCard struct { Title, Blurb, ScheduleSlug string }
   ```
2. Add consulate/bureau catalogs (none today), sourced from `aurenor` slug helpers:
   ```go
   var aurenorConsulateServices = []apptCard{ {Title:"Tourist Visa Interview", Blurb:"...", ScheduleSlug:"consulate-tourist-visa"}, ... }
   var aurenorBureauServices = []apptCard{ ... }
   ```
3. Hospital-department builder:
   ```go
   func hospitalDepartments(v Venue) []apptCard {
       return []apptCard{ {Title:"Cardiology", Blurb:"...", ScheduleSlug:v.Slug+"-cardiology"}, ... }
   }
   ```
4. Add a test asserting every rendered `ScheduleSlug` has a matching seed row (drift guard against Step 1's `AllSchedules()`).

### 2.3 Alpine component + fetch contract
New templ file `internal/web/pages/aurenor_appointments.templ` holding:
- a `<script>` registering the component (pattern from `games_tictactoe.templ:113`: `document.addEventListener('alpine:init', () => Alpine.data('apptBooker', () => ({...})))`),
- an `[x-cloak]{display:none}` style block,
- `templ apptBookable(c apptCard)` rendering one card with `x-data="apptBooker('<c.ScheduleSlug>')"`,
- a `myBookings()` panel component.

`apptGrid` loops `apptBookable(c)` instead of the disabled button.

Component state: `{ slug, slots:[], loading:false, error:'', chosen:null, guestName:'', guestEmail:'', booking:false, confirmation:null, mine:[] }`.

**Load availability — public GET, NO CSRF:**
```js
async loadSlots() {
  this.loading = true; this.error = '';
  const res = await fetch(`/appointments/schedules/${this.slug}/availability`);
  if (!res.ok) { this.error = 'Could not load times'; this.loading = false; return; }
  this.slots = (await res.json()) || [];   // 200 body may be JSON null → []
  this.loading = false;
}
```
Element shape (`internal/appointments/availability.go:13`): `{ "start_at":"...Z", "end_at":"...Z", "remaining":N }`, RFC3339. `remaining===0` → sold-out chip. 404 (plain text) if slug unknown/inactive. Render with `x-for`; `s.start_at` label via `new Date(s.start_at).toLocaleString()`.

**Book — guest-allowed POST, CSRF REQUIRED:**
```js
async book(startAt) {
  this.booking = true; this.error = '';
  const csrf = document.querySelector('meta[name="csrf-token"]').content;
  const res = await fetch(`/appointments/schedules/${this.slug}/book`, {
    method:'POST',
    headers:{ 'Content-Type':'application/json', 'X-CSRF-Token':csrf },
    body: JSON.stringify({ start_at:startAt, guest_name:this.guestName, guest_email:this.guestEmail }),
  });
  if (res.status === 201) {
    const appt = await res.json();     // BookingAppointment incl. cancel_token
    this.confirmation = appt; this.rememberBooking(appt); await this.loadSlots();
  } else if (res.status === 409) { this.error = await res.text(); await this.loadSlots(); }
  else if (res.status === 403) { this.error = 'Session expired, please reload'; }
  else { this.error = await res.text(); }
  this.booking = false;
}
```
Body = `bookReq` (`appointments.go:17`): `{start_at, guest_name, guest_email}`; `start_at` must be the exact RFC3339 string from availability. Signed-in callers auto-linked server-side. Status branches: 201 `BookingAppointment` JSON (snake_case incl. `cancel_token`); 400 past/bad payload; 409 `ErrNotOpen`/`ErrSoldOut`; 403 CSRF; 404 unknown/inactive; 500 server error. **Errors are plain text — read with `res.text()`, never `res.json()`.**

**Cancel — guest-allowed POST, CSRF REQUIRED, token in query:**
```js
async cancel(token) {
  const csrf = document.querySelector('meta[name="csrf-token"]').content;
  const res = await fetch(`/appointments/cancel?token=${encodeURIComponent(token)}`, {
    method:'POST', headers:{ 'X-CSRF-Token':csrf },
  });
  if (res.ok) { this.forgetBooking(token); await this.loadSlots(); }
  else this.error = await res.text();
}
```
Token is a query param (`appointments.go:140`), idempotent.

**My bookings — signed-in augmentation, GET /me, auth, NO CSRF:** `GET /appointments/me` returns 401 for guests, JSON array (ordered `start_at DESC`) for signed-in. Guest bookings never appear there (`UserID=nil`). So the view is **localStorage-first** and optionally merged with `/me`:
```js
async loadMine() {
  this.mine = this.storedBookings();
  const res = await fetch('/appointments/me');
  if (res.ok) { const server = await res.json(); /* merge by cancel_token, server wins */ }
}
```

### 2.4 cancel_token storage
Guest bookings have no account; `cancel_token` from the 201 body is the only ownership proof. `localStorage` key `aurenor_bookings` → JSON array of `{cancel_token, schedule_slug, title, start_at, end_at, guest_name}`. `rememberBooking(appt)` on 201; `forgetBooking(token)` on successful cancel; `storedBookings()` for the panel. Do not rely on cookies for the token.

### 2.5 Templ edits
1. `internal/web/pages/aurenor_shared.templ` — `apptGrid` (L45): replace disabled `<button>` (L51-54) with `@apptBookable(c)`. `aurenorPage` (L5): gate the preview badge (L11-13) — add `wired bool` param (or split into `aurenorPageLive`), badge only when `!wired`. Pass `wired=true` from the three now-live pages; theater stays `false` until Step 3.
2. `internal/web/pages/aurenor_appointments.templ` (new): `apptBooker` script, `[x-cloak]`, `apptBookable`, `myBookings` panel.
3. `internal/web/pages/aurenor_hospitals.templ` — `AurenorHospital(v Venue)` (L16): `@apptGrid(hospitalDepartments(v))`; live frame.
4. `internal/web/pages/aurenor_consulate.templ` — `@apptGrid(aurenorConsulateServices)`; live frame.
5. `internal/web/pages/aurenor_bureau.templ` — `@apptGrid(aurenorBureauServices)`; live frame.
6. `internal/web/pages/aurenor_helpers.go` — `apptCard.ScheduleSlug`, consulate/bureau catalogs, `hospitalDepartments`.
7. `docs/worldbuilding/aurenor.md` — roadmap: mark "Appointments live" *(done)*, "Theater live" *(next)*. The roadmap is internal; it is no longer rendered on the Projects page, so `projects.templ` needs no edit.
8. Run `templ generate`.

No route changes: `mountExamples` serves the pages, JSON API is mounted, `PageShell` publishes `<meta name="csrf-token">` (`index.templ:16`), all `/aurenor` routes run under `OptionalAuthMiddleware`.

### 2.6 CSRF correctness
The meta token is minted+persisted by `ensureCSRFToken` during `PageShell` render (`csrf.go:53`); the session cookie carries the matching value; `CSRF()` compares header/form vs session (`csrf.go:96`). Read `meta[name="csrf-token"]`, send as `X-CSRF-Token`. GET availability and GET /me are unprotected.

### 2.7 Tests — httptest through real routes
No server httptest harness exists; build one. New `internal/server/booking/appointments_http_test.go`, package `booking`. DB via `testsupport.PostgresDB(t, database.MigrationModels()...)`.

Harness: `ctx := server_context.BackEndContext{DBConn: db, Logger: zap.NewNop()}` (ValKey nil); `mux := router.NewRouter(); app := mux.Middleware(api_auth.OptionalAuthMiddleware(ctx)); booking.Mount(app, ctx)`; serve via `httptest.NewServer`. Seed with `aurenor.Seed(db,...)` (or inline schedule+rule).

**CSRF acquisition:** `cookiejar`-backed `http.Client`; (1) `GET /aurenor/hospitals/solvarre-general` — persists session cookie, embeds the meta token (scrape via regex); (2) POST with that token in `X-CSRF-Token` + jar cookie.

Cases: (1) availability shape (200 array; unknown slug 404); (2) book happy path (201, `cancel_token`, `status:"confirmed"`, remaining drops); (3) sold out (capacity-1, second book → 409 "slot is full"); (4) not open (start outside rules → 409); (5) past → 400; (6) CSRF missing → 403; (7) cancel (200, remaining restored, second cancel 200 idempotent, empty token 400); (8) `/me` 401 for guest; (9) slug/seed contract table test (every templ `ScheduleSlug` has a seeded schedule). Existing domain tests cover `Book`/`ComputeAvailability`/`CancelByToken`.

### 2.8 Definition of Done
On each of the three pages a guest can see real availability, book, watch `remaining` decrement, and cancel with the stored `cancel_token`. Sold-out disabled; concurrent 409 surfaces + refreshes. "My bookings" lists localStorage for guests, merges `/me` when signed in. No preview badge on the three wired pages; theater keeps it. HTTP CSRF book→cancel round-trip and slug/seed contract tests pass; `go build` clean; templ regenerated.

### 2.9 Call-outs
- Guest "my bookings" is client-side by necessity (`/me` auth-only, guest `UserID=nil`).
- Error bodies plain text.
- Tests need Postgres (no sqlite fallback).
- The hold reaper / paid `/hold` flow is irrelevant here (free schedules never enter it).

---

## Step 3 — THEATER LIVE (Roadmap Step 4)

Wire `AurenorTheater` (`/aurenor/theaters/{slug}`) to the real ticketing engine: browse real showings, open an interactive seat map, place a real seat/GA hold — **up to `held`**. Checkout/capture stays stubbed.

**Prerequisite: Step 1 seed exists** (theater events with seated + GA tiers at full inventory).

### 3.1 How showings are sourced
There is **no `GET /events` collection endpoint**; adding one is out of scope. **Decision — Option A: slug list in the venue catalog.** Each theater `Venue` gains an ordered list of Step-1 event slugs; the detail page renders that list into the Alpine component, which fetches `GET /events/{slug}` per showing (public, no CSRF) to get name, `start_at`, `tiers[].remaining`. Zero new API surface. (Option B — a mock-scoped `GET /aurenor/theaters/{slug}/showings` DB query — is the documented follow-up if per-page request count matters.)

Venue.Events must carry Step 1's canonical slugs (`grand-dune-part-two-1000`, etc.), NOT `aurenor-<theater>-<n>`.

### 3.2 The seat-map Alpine component
New file `internal/web/pages/aurenor_theater_live.templ`: one templ component + `<script>` `Alpine.data('theater', …)`, pattern from `games_tictactoe.templ:113` (`alpine:init`, `x-data`, `x-show`/`x-for`/`x-text`/`@click`, `[x-cloak]{display:none}`).

State: `showings:[]` (`{slug,name,startAt,seatedTier:{id,remaining},gaTier:{id,price,remaining}}`), `active:null`, `seats:[]`, `selected:[]`, `qty:1`, `maxPerBuyer:0`, `hold:null`, `countdown:''`, `error:''`, `loading:false`.

Flow:
1. `init()` — read the injected JSON slug list, `Promise.all` a `GET /events/{slug}` per slug; map `event.name`, `event.start_at`, `event.max_per_buyer`; find seated + GA tiers, keep `id`+`remaining`; render a card per showing with real "N left" and sold-out when `remaining===0`.
2. Open a showing — set `active`, `GET /events/{active.slug}/seats` (optionally `?section=`), store `seats`; render grid grouped by `section` then `row_name`, ordered by `number`; selectable only when `state==='free'` (`held|booked|blocked` greyed).
3. Selection cap — toggle free seats in `selected`; enforce `maxPerBuyer` client-side; server enforces via `ErrPurchaseLimit` → 409.
4. Hold (seated) — `POST /events/{active.slug}/seats/hold` with `X-CSRF-Token` and `{ seat_ids: selected, guest_name, guest_email }`; on 201 store `hold.tokens = resp.hold_tokens`, `hold.totalCents = resp.total_cents`. **Response has no `expires_at`** — derive countdown from `cartTTL=10min` at POST time (`tickets.go:20`).
5. Hold (GA) — `POST /events/{active.slug}/tiers/{gaTier.id}/hold` with CSRF and `{ qty, guest_name, guest_email }`; GA response **does** include `expires_at`.
6. Countdown — `setInterval` to `mm:ss`; at zero clear `hold`, re-fetch the seat map (seat flips to `free` only after `ReapExpiredHolds` runs — Step 4), tell the user the hold lapsed.
7. Errors — 409 sold-out/limit, 400 seat-not-in-event/invalid, 404 event missing/not scheduled; re-fetch seat map on 409.

CSRF recipe (holds only; reads need no token):
```js
const csrf = document.querySelector('meta[name="csrf-token"]').content;
const res = await fetch(`/events/${slug}/seats/hold`, {
  method:'POST', headers:{ 'Content-Type':'application/json', 'X-CSRF-Token':csrf },
  body: JSON.stringify({ seat_ids:this.selected, guest_name:this.name, guest_email:this.email }),
});
```
Guest holds are allowed; ownership is `api_auth.OwnerRef` (guest sid), migrates on login via the `"booking"` migrator.

### 3.3 Templ edits
- `internal/web/pages/aurenor_helpers.go` — extend `Venue` with `Events []string`; populate `aurenorTheaters` (`grand`, `harborlight`) with Step-1 slugs; add `func venueEventsJSON(v Venue) string` (`json.Marshal(v.Events)`) for safe embedding.
- `internal/web/pages/aurenor_theaters.templ` — `AurenorTheater(v Venue)` (L16): delete the four `@showingCard(...)` (L18–21); render `@theaterLive(v)`. `AurenorTheaters()` unchanged.
- `internal/web/pages/aurenor_theater_live.templ` (new) — `templ theaterLive(v Venue)`: `x-data="theater()"` container, showings `x-for`, seat-map modal/panel grid, GA qty stepper, hold summary with countdown, guest inputs, error line, `[x-cloak]`, and the `Alpine.data('theater', …)` script; inject `venueEventsJSON(v)`.
- `internal/web/pages/aurenor_shared.templ` — `aurenorPage`: gate the preview badge (same param introduced in Step 2); `AurenorTheater` passes `wired=true`/`preview=false`. Leave `showingCard`/`apptGrid` for still-mock pages.
- `docs/worldbuilding/aurenor.md` — roadmap: step 4 → *(done)*, step 5 → *(next)*. Not `projects.templ`; the roadmap is internal and no longer rendered.
- Run `templ generate`.

### 3.4 Backend edits
No route/handler changes for the happy path (Option A reuses public reads + CSRF-guarded holds). Theater events come from Step 1's `clair seed` (no separate `seed aurenor` command).

### 3.5 What stays stubbed, and why
- **Checkout/capture** — the only route reaching `kernel.CaptureOrder` is the HMAC-signed `POST /webhooks/payments/{provider}` (disabled when `BOOKING_PAYMENT_WEBHOOK_SECRET` unset); no browser/session capture route. UI ends at a confirmed hold (tokens, total, countdown) with copy stating payment isn't wired.
- **Release/cancel of an event hold** — events have no cancel route; abandoned holds clear only via `ReapExpiredHolds` (wired in Step 4). Countdown-to-zero clears local state and re-fetches but the seat won't free until the reaper runs.
- **Read-your-tickets** — no `GET /events/.../orders` endpoint.
- **Waitlist** — routes exist (`POST /events/{slug}/tiers/{tierID}/waitlist`, `.../seats/{seatID}/waitlist`, CSRF) but deferred.

### 3.6 Tests
Go (load-bearing): (1) seed idempotency (via Step 1); (2) availability read (`GET /events/{slug}` seated `remaining`==free seats, GA `remaining`==capacity; `/seats` all `free`, ordered); (3) seat hold e2e via httptest with CSRF (`hold_tokens` length==seat count, order pending; re-`GET /seats` shows `held`; re-hold → 409); (4) cap enforcement (`MaxPerBuyer:2`, request 3 → 409 `ErrPurchaseLimit`); (5) cross-event guard (seat ids from another event → 400); (6) missing CSRF → rejected. Templ render: (7) `AurenorTheater(v)` renders `x-data="theater(...)"`, embeds `v.Events` JSON, no `disabled` chip, no preview badge; (8) `AurenorTheaters()` keeps `institutionCard`s + preview badge. Manual: (9) seed → visit `/aurenor/theaters/grand` → showings with real "N left" → open → grid → select to cap → Hold → tokens+countdown; second browser sees `held`; countdown→0 local reset.

### 3.7 Definition of Done
`/aurenor/theaters/{slug}` loads real showings from `GET /events/{slug}` with live remaining + sold-out; no hardcoded films/times; no preview badge. Interactive seat map from `GET /events/{slug}/seats` with `held/booked/blocked` unselectable, selection capped at `max_per_buyer`. Hold (seated `/seats/hold`, GA `/tiers/{id}/hold`) with valid CSRF returns tokens/total and runs a countdown (GA `expires_at`; seats client `cartTTL`); concurrent viewer sees held seats. Errors 409/400/404 mapped. Checkout/capture, hold-release, my-tickets stubbed with in-UI copy. `go build`/`templ generate`/`go test` clean.

### Key code coordinates
Public reads: `getEvent` (`events.go:124`), `seatMap` (`events.go:175`), routes `mount.go:170-171`. Holds: `holdSeats` (`tickets.go:116`, returns `hold_tokens`, no `expires_at`), `holdGA` (`tickets.go:64`, returns `expires_at`), routes `mount.go:174-176`, `cartTTL=10m` (`tickets.go:20`). CSRF: meta `index.templ:16`, header `X-CSRF-Token` (`csrf.go:14`). Alpine/CSRF pattern: `games_tictactoe.templ:113-233`.

---

## Step 4 — SCHEDULER UNBLOCK + REAPER WIRING → **superseded**

This step planned to *gate* the Discord loop behind a non-empty
`DISCORD_CHANNEL_ID` and leave `eventLoop.go` in place. Discord is now being
removed from the product outright, so the gate is moot and the blast radius is
larger than this section assumed (the `discordgo` types live in package
`internal/utils`, which 15 files import).

Read **Step A of `2026-08-04-booking-ux-and-discord-removal-plan.md`** instead.
It carries the verified file list, the module list, the one ordering rule that
keeps the build green, and the same reaper wiring this section described.

What still holds from here: the reaper registration shape
(`Every(1).Minute().SingletonMode()` around
`booking.ReapExpiredHolds`, `internal/booking/hold.go:330`), and the fact that
Steps 5 and 6 register their jobs in the same `RunE`.

---

## Step 5 — SIMULATOR (Roadmap Step 9)

A background job inside `clair scheduler` that continuously fills Aurenor bookings on realistic demand curves by calling the **domain Go functions in-process** (not HTTP), so every write passes the kernel's atomic capacity guard (`booking.BookDirect`/`booking.HoldUnitsTx`) and the `booked+held+blocked <= capacity` invariant can never be violated.

**Prerequisites:** Step 4 (scheduler unblock) and Step 1 (seed). The sim is a consumer of seeded data.

### 5.1 Design decision: in-process domain calls
- **Appointments:** `appointments.Book(db, appointments.BookInput{...}, now)` — the free flow; lazily materializes slot + inventory and does the atomic increment in one tx (`internal/appointments/book.go:35`). One call = one confirmed appointment.
- **Theater GA:** `ticketing.HoldGA(db, tier, qty, buyer, ttl, now, maxPerBuyer)` → `booking.CaptureOrder(db, orderID, mockPayment, now)`. Hold moves `capacity→held`; capture commits `held→booked` and issues tickets (`internal/booking/hold.go:274`). **A hold alone is not a sale — the sim MUST capture.**
- **Theater seated:** `ticketing.HoldSeatGroup(db, eventID, seatIDs, buyer, ttl, now, maxPerBuyer)` → `booking.CaptureOrder` per order.

Capture in-process is fine: the HMAC webhook is only the external path; `booking.CaptureOrder` is a plain exported func; the sim supplies `models.BookingPayment{Provider:"sim", Kind:"capture", Status:"succeeded", IdempotencyKey:"sim-<orderID>-<nonce>"}` (unique key makes re-capture a no-op, `hold.go:278`). Driving HTTP would need CSRF + forged HMAC — pure overhead; the invariant is enforced in the kernel, not the handlers.

### 5.2 Package layout
```
internal/simulator/
  simulator.go   // Simulator struct, New(), Tick(), ConfigFromEnv()
  demand.go      // demand-curve math (pure, unit-tested)
  targets.go     // pick which schedules/events to act on this tick
  appointments.go// fillAppointments() — appointments.Book path
  theaters.go    // fillTheaters()     — HoldGA/HoldSeatGroup + CaptureOrder path
  demand_test.go
  simulator_test.go
```
cmd wiring: only the `if SIM_ENABLED` block in `scheduler.go` (added on top of Step 4). No new subcommand.

```go
type Simulator struct { db *gorm.DB; log *zap.Logger; Cfg Config; rng *rand.Rand }
type Config struct {
    IntervalSec  int     // SIM_INTERVAL_SEC, default 30
    MaxActions   int     // SIM_MAX_ACTIONS per tick, default 8 — the throttle
    BaseRate     float64 // SIM_BASE_RATE, default 1.0
    Jitter       float64 // SIM_JITTER 0..1, default 0.3
    TheaterMinQty, TheaterMaxQty int // 2..3 per purchase
    HorizonDays  int     // SIM_HORIZON_DAYS, default 14
}
```

### 5.3 Tick model + throttle
One `Tick()` every `SIM_INTERVAL_SEC` (default 30s), `SingletonMode`. Each tick: (1) compute demand multiplier `d ∈ [0,~1.5]` from `now`; (2) `budget = round(MaxActions * d * BaseRate * jitter())` clamped `[0,MaxActions]` (`jitter()` = `1 ± Jitter*rand`); `budget==0` → no-op; (3) select targets weighted by per-institution demand; (4) dispatch each action to `fillAppointments`/`fillTheaters`, swallowing per-action errors (sold out, not-open) at debug. Throttle: budget cap bounds DB write pressure regardless of venue count (default ≈16 bookings/min at peak, ~0 at 4am).

Target selection (`targets.go`): `schedules := WHERE active=true`; `events := WHERE status='scheduled' AND start_at>now`. Weight = base demand `d` × institution-type factor (theaters skew evenings/weekends; hospitals weekday daytime). Weighted sampling.

### 5.4 Demand curve (`demand.go` — pure, the unit-test target)
```
func hourWeight(kind string, h int) float64:
    "theater":     peak ~19:00–22:00; near 0 before 12:00; w = clamp((h-12)/10,0,1)^1.5 + evening bump 18–22
    "appointment": daytime plateau 08:00–17:00 (~1), ~0.05 outside, lunch dip 12–13
func dayWeight(kind string, wd time.Weekday) float64:
    theater:     Fri/Sat/Sun 1.0, Mon–Thu 0.5
    appointment: Mon–Fri 1.0, Sat 0.4, Sun 0.05
func demand(kind string, now time.Time) float64:
    return baseline + hourWeight(kind, now.Hour()) * dayWeight(kind, now.Weekday())
```

### 5.5 fillAppointments (`appointments.go`)
```
func fillAppointments(sched, now):
    from := now; to := now.AddDate(0,0, min(Cfg.HorizonDays, sched.WindowDays))
    rules, exceptions, booked := load(...)      // booked = slot start_at -> booked count
    open := appointments.ComputeAvailability(sched, rules, exceptions, booked, from, to, now)
    if len(open) == 0: return
    slot := weightedPick(open, func(s)= recencyWeight(s.StartAt, now) * demand("appointment", s.StartAt))
    _, err := appointments.Book(db, appointments.BookInput{
        Schedule: sched, StartAt: slot.StartAt, UserID: nil,
        GuestName: fakeName(), GuestEmail: fakeEmail(),
    }, now)   // ErrSoldOut/ErrNotOpen -> log debug, move on
```
`recencyWeight` biases near-term slots; combined with day/hour `demand` on the slot's own timestamp this reproduces "weekday mornings quiet, fill toward night" and "weekends fill earlier in the week."

### 5.6 fillTheaters (`theaters.go`)
```
func fillTheaters(event, now):
    tiers := WHERE event_id = event.ID
    tier  := weightedPick(tiers by remaining desc)
    qty   := randInt(Cfg.TheaterMinQty, Cfg.TheaterMaxQty)   // 2..3
    buyer := ticketing.Buyer{ OwnerRef: fmt.Sprintf("sim:%d", uniqueSid()), UserID:nil, Name:fakeName(), Email:fakeEmail() }
    if tier.Kind == "ga":
        order, _, err := ticketing.HoldGA(db, tier, qty, buyer, cartTTL, now, event.MaxPerBuyer)
        if err: return
        capture(order.ID)
    else:
        seatIDs := pick qty free sellable seats
        if len(seatIDs)==0: return
        order, _, err := ticketing.HoldSeatGroup(db, event.ID, seatIDs, buyer, cartTTL, now, event.MaxPerBuyer)
        if err: return
        capture(order.ID)

func capture(orderID):
    pay := models.BookingPayment{ OrderID:orderID, Provider:"sim", ProviderRef:"sim",
        Kind:"capture", Status:"succeeded", AmountCents:/*order.TotalCents*/, Currency:order.Currency,
        IdempotencyKey: fmt.Sprintf("sim-%d", orderID) }
    _ = booking.CaptureOrder(db, orderID, pay, time.Now())
```
`HoldGA`/`HoldSeatGroup` enforce `event.MaxPerBuyer` per `OwnerRef` (`hold.go:40`); a **fresh unique `OwnerRef` per purchase** avoids the limit AND avoids the idempotent-hold reuse (`HoldGA` is idempotent per `(tier, owner_ref)`, `hold.go:51`) so the pool actually drains. The `sim:` prefix (not `user:`/`guest:`) keeps sim traffic identifiable for retention and demos.

### 5.7 Gating & safety (env via `utils.GetEnv`)
| Env | Default | Effect |
|---|---|---|
| `SIM_ENABLED` | `false` | Master switch; job registered only when `=="true"`. |
| `SIM_INTERVAL_SEC` | `30` | Tick period. |
| `SIM_MAX_ACTIONS` | `8` | Per-tick action budget cap. |
| `SIM_BASE_RATE` | `1.0` | Demand multiplier. |
| `SIM_JITTER` | `0.3` | Budget randomness. |
| `SIM_HORIZON_DAYS` | `14` | Fill horizon. |
| `SIM_ALLOW_PROD` | `false` | Prod-DSN guard override. |

Guards: (1) **prod-DSN guard** — inspect `DATABASE_URL`; if host not in `{localhost,127.0.0.1,db,postgres}` and not containing `dev`/`local`/`test`, and `SIM_ALLOW_PROD != "true"`, **refuse to register** and log loudly (fail-closed). (2) **single-runner** — `SingletonMode()` for intra-process; a Postgres advisory lock (`pg_try_advisory_lock`/`pg_advisory_unlock`) at tick start for multi-instance (don't depend on Valkey — it's optional/nil). (3) **best-effort** — swallow per-action errors at debug; wrap `Tick` in `recover()` so a panic never takes down the reaper. (4) **bounded write pressure** — `MaxActions` × advisory lock × SingletonMode. (5) **namespace isolation** — `OwnerRef` prefix `sim:`, payment `Provider:"sim"`.

Wire the `SIM_ENABLED` block into `scheduler.go` (on top of Step 4):
```go
if utils.GetEnv("SIM_ENABLED", "false") == "true" {
    sim := simulator.New(db, logger, simulator.ConfigFromEnv())
    scheduler.Every(sim.Cfg.IntervalSec).Seconds().SingletonMode().Do(sim.Tick)
    logger.Info("simulator enabled", zap.Int("interval_sec", sim.Cfg.IntervalSec))
}
```
Document `SIM_*` in `.env`/`.env.example`.

### 5.8 Tests
Pure (no DB): `demand_test.go` table-driven across 24h×7d (theater peaks evening/weekend, appointments weekday daytime, overnight ≈0); budget math (`MaxActions` respected, `budget==0` at 4am, jitter in `[1-J,1+J]` with seeded rng); `weightedPick`/`recencyWeight` statistical with fixed seed; prod-DSN guard table.
Integration (Postgres): seed a free schedule + GA event, run `Tick` N times at frozen `now`, assert appointment `booked` rose but never exceeded `Capacity`; GA `booked+held+blocked <= capacity` always; captured orders `status:"paid"` with issued `BookingTicket`s; all sim orders `OwnerRef LIKE 'sim:%'`. Sold-out safety (near-full capacity, hammer `Tick`, assert `ErrSoldOut` swallowed, no over-book). Reaper interplay (hold, advance `now` past TTL, `ReapExpiredHolds` releases).
Manual: `SIM_ENABLED=true SIM_INTERVAL_SEC=2 make run-scheduler` on dev DB.

### 5.9 Definition of Done
`clair scheduler` runs and the reaper ticks. With `SIM_ENABLED=true`, booked-counts climb following the demand curve; theater purchases consume 2–3 seats and settle to `paid` orders with issued tickets (permanent, not transient). Invariant holds under load; sold-out swallowed. Sim refuses non-local DSN unless overridden; single-runner. All sim rows identifiable. Unit + integration tests green; `SIM_*` documented.

### Verified facts
`scheduler.go:22-26` TODO stubs, `:31` unconditional `NewEventLoop` (fixed in Step 4). `ReapExpiredHolds(db,now)(int,error)` at `hold.go:330`. `appointments.Book` free/atomic (`book.go:35-78`); `ComputeAvailability` (`availability.go:25`) reusable. `HoldGA` idempotent per `(tier,owner_ref)` (`hold.go:32`). `CaptureOrder` idempotent via `IdempotencyKey` (`hold.go:274-278`). `database.New` self-migrates.

---

## Step 6 — RETENTION (Roadmap Step 10): monthly Postgres sweep

Expire every booking/schedule artifact after one month via a periodic Postgres sweep. No Valkey TTL. **Depends on Step 4** (the scheduler is where retention registers) and **Step 1** (backfill attaches to `SeedCmd`).

### 6.1 Goal & non-goals
Goal: an idempotent, batched `booking.PurgeOldBookingData(db, now, opts)` that hard-deletes booking rows older than a configurable age, wired into `clair scheduler` as a gated daily job, plus a one-shot backfill so the first sweep has data. Non-goals: no Valkey; no soft-delete (`gorm.Model`/`DeletedAt` not used — deletes are already hard); no FK/schema change to the live selling path; retention never touches `User`.

### 6.2 Retention predicate — business time, not `CreatedAt`
Predicate on the thing's own past time, with a status guard on money rows. `cutoff := now.Add(-maxAge)`, `maxAge` from `RETENTION_MAX_AGE` (default `720h`, `time.ParseDuration`).

| Table | Predicate | Basis |
|---|---|---|
| `BookingAppointment` | `end_at < cutoff` | `booking_appointment.go:20` |
| `BookingSlot` | `end_at < cutoff` | `booking_slot.go:17` |
| `BookingEvent` | `start_at < cutoff` (catalog gate) | `booking_event.go:18` |
| `BookingTicket` | order item's order purged (join) OR event `start_at < cutoff` | anchor on `EventID` |
| `BookingOrder` | `status IN ('expired','cancelled','refunded') AND updated_at < cutoff` (+ optional aggressive branch) | `booking_order.go:24,15` |
| `BookingHold` | `status != 'active' AND updated_at < cutoff` | never delete `active` |
| `BookingWaitlistEntry` | `status != 'waiting' AND updated_at < cutoff` | never delete live `waiting` |
| `BookingOrderItem` | `order_id IN (purged)` | |
| `BookingPayment` | order purged | |
| `BookingSeat` | tier in purged events (catalog gate) | |
| `BookingTicketTier` | `event_id IN (purged)` (catalog gate) | |
| `BookingAvailabilityRule`/`Exception` | schedule purged (catalog gate) | |
| `BookingSchedule` | only when fully drained (catalog gate) | |
| `BookingInventory` | orphaned owner AND `held=0 AND booked=0 AND blocked=0` | `booking_inventory.go:25-32` |

**Catalog knob:** default keeps `BookingSchedule`/`BookingEvent` (Aurenor pages still resolve) and purges only transactional descendants; `RETENTION_PURGE_CATALOG=true` also drops drained schedules/events. **Paid orders:** for the sim you generally want paid orders + tickets to age out — recommended predicate adds `(domain='event' AND event.start_at<cutoff)`/`(domain='appointment' AND appt.end_at<cutoff)`, gated behind `RETENTION_PURGE_PAID` (default `true`).

### 6.3 Delete ordering — children before parents (no cascade)
Confirmed: **no DB-level FKs** anywhere in `internal/database/models/booking_*.go` (plain `*ID uint` columns, indexes only). Postgres won't cascade and won't block a parent delete, but delete children first to keep each intermediate state consistent. Order:
1. Compute driving id sets (select-only): `purgedEventIDs`, `purgedScheduleIDs`, `purgedOrderIDs`.
2. `BookingTicket` (items of purgedOrderIDs OR event in purgedEventIDs) → 3. `BookingAppointment` (`end_at<cutoff`) → 4. `BookingOrderItem` (`order_id IN purged`) → 5. `BookingPayment` (order purged) → 6. `BookingOrder` (`id IN purged`) → 7. `BookingHold` (`status!='active' AND updated_at<cutoff`) → 8. `BookingWaitlistEntry` (`status!='waiting' AND updated_at<cutoff`) → 9. `BookingSlot` (`end_at<cutoff`) → 10. `BookingSeat` (tiers of purgedEventIDs, catalog gate) → 11. `BookingTicketTier` (`event_id IN purged`, catalog gate) → 12. `BookingInventory` orphan sweep:
```sql
DELETE FROM booking_inventory bi
WHERE bi.held = 0 AND bi.booked = 0 AND bi.blocked = 0
  AND (
    (bi.slot_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM booking_slots s WHERE s.id = bi.slot_id))
    OR (bi.tier_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM booking_ticket_tiers t WHERE t.id = bi.tier_id))
    OR (bi.seat_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM booking_seats se WHERE se.id = bi.seat_id))
  );
```
(inventory is 1:1 with its owner — `SlotID/TierID/SeatID` uniqueIndex — so owner-orphan ⇒ dead; the `held/booked/blocked=0` guard guarantees a live hold is never yanked). → 13. `BookingAvailabilityRule`/`Exception` (schedule purged, catalog gate) → 14. `BookingSchedule` (catalog gate) → 15. `BookingEvent` (catalog gate). **Cascade FK migration rejected** for default (schema-change risk on the live path; cascade lacks the `held=0` guard).

### 6.4 Batching — mirror `ReapExpiredHolds`
Template: `Limit(500)`, injected `now`, returns a count (`internal/booking/hold.go:330`). Each table drains in a loop `DELETE FROM t WHERE id IN (SELECT id FROM t WHERE <pred> LIMIT 500)` until `RowsAffected==0`. Signature/report:
```go
func PurgeOldBookingData(db *gorm.DB, now time.Time, opts PurgeOpts) (PurgeReport, error)
type PurgeOpts struct { MaxAge time.Duration; PurgeCatalog, PurgePaid bool }
type PurgeReport struct {
    Tickets, Appointments, OrderItems, Payments, Orders,
    Holds, Waitlist, Slots, Seats, Tiers, Inventory,
    Rules, Exceptions, Schedules, Events int
}
```
**Transaction shape (B, chosen):** per-table, per-batch short transactions (locks release between batches, a crash resumes on the next tick) — mirrors the reaper, which is not one giant tx. Compute driving id sets at the top of each table's drain. Wrap the job in `SingletonMode()`.

### 6.5 Register the retention job (`scheduler.go`, on top of Step 4)
```go
if utils.GetEnv("RETENTION_ENABLED", "false") == "true" {
    scheduler.Cron(utils.GetEnv("RETENTION_CRON", "0 3 * * *")).SingletonMode().Do(func() {
        maxAge, _ := time.ParseDuration(utils.GetEnv("RETENTION_MAX_AGE", "720h"))
        rep, err := booking.PurgeOldBookingData(db, time.Now(), booking.PurgeOpts{
            MaxAge:       maxAge,
            PurgeCatalog: utils.GetEnv("RETENTION_PURGE_CATALOG", "false") == "true",
            PurgePaid:    utils.GetEnv("RETENTION_PURGE_PAID", "true") == "true",
        })
        // log rep + err via utils.NewLogger("retention")
    })
}
```
`gocron.Scheduler.Cron` (`vendor/.../gocron/scheduler.go:828`) + `.SingletonMode()`.

### 6.6 Env knobs (`.env`/`.env.example`)
`RETENTION_ENABLED` (`false`), `RETENTION_MAX_AGE` (`720h`), `RETENTION_CRON` (`0 3 * * *`), `RETENTION_PURGE_CATALOG` (`false`), `RETENTION_PURGE_PAID` (`true`), `REAPER_ENABLED` (optional, `true`). Valkey irrelevant.

### 6.7 One-shot historical backfill
Add `--backfill` and `--backfill-days` (default 45) to Step 1's `internal/cmd/seed.go` `SeedCmd` (do NOT build a parallel command). Creates a handful of **past** terminal artifacts: `BookingSchedule` + `BookingSlot{EndAt:now-45d}` + `BookingAppointment{Status:"cancelled", EndAt:now-45d}`; `BookingEvent{StartAt:now-45d}` + tier + `BookingOrder{Status:"refunded", UpdatedAt:now-45d}` + order item + payment + `BookingTicket`; a `BookingHold{Status:"released", UpdatedAt:now-45d}`; an expired `BookingWaitlistEntry`. **Because `Create` autoupdates `UpdatedAt=now`, the backfill must overwrite `UpdatedAt`/`EndAt`/`StartAt` with a follow-up `db.Model(&row).UpdateColumn("updated_at", past)`** (UpdateColumn skips autoupdate) or predicates never fire. Idempotent via a fixed backfill slug prefix (e.g. `aurenor-backfill-*`), skip-if-exists. Optional `--run-retention-once` on `scheduler.go` for manual verification.

### 6.8 Tests — new `internal/booking/retention_test.go` (package `booking_test`, `testsupport.PostgresDB`, skips when no DB)
1. `TestPurge_DeletesAgedTerminalRows` — cancelled appt `EndAt=now-45d`, refunded order+item+payment+ticket aged out, released hold, expired waitlist, orphaned inventory → all gone; report counts match.
2. `TestPurge_PreservesLiveFutureData` — confirmed appt `EndAt=now+7d`, `active` hold, `waiting` waitlist, event `StartAt=now+7d`, inventory `held>0` → nothing deleted.
3. `TestPurge_NeverDeletesActiveHoldInventory` — inventory `held=1` whose owner slot is past-dated → survives (the `held=0` guard wins). Critical safety test.
4. `TestPurge_Idempotent` — run twice; second returns all-zero, no error.
5. `TestPurge_Batching` — >500 aged appointments → all deleted (LIMIT 500 loop drains fully).
6. `TestPurge_CatalogGating` — `PurgeCatalog=false` keeps drained past schedule/event; `true` deletes them.
7. `TestReapExpiredHolds_Wired` — only if a gap exists in `hold_test.go`.
Backfill back-dating test: `--backfill` produces rows with `UpdatedAt < now-30d`.

Run: `TEST_DATABASE_URL=... go test ./internal/booking/... ./internal/cmd/...`; `go build ./...`.

### 6.9 Ordered tasks
1. (Done in Step 4) scheduler unblock + reaper wiring.
2. Write `booking.PurgeOldBookingData` in `internal/booking/retention.go` (`PurgeOpts`, `PurgeReport`, batched deletes in §6.3 order, inventory guard).
3. Register the retention `Cron` job in `scheduler.go` gated by `RETENTION_ENABLED`.
4. Env docs.
5. Add `--backfill`/`--backfill-days` to `internal/cmd/seed.go` (UpdateColumn back-dating, idempotent by slug prefix); optional `--run-retention-once`.
6. `internal/booking/retention_test.go` + backfill test.
7. Verify end-to-end: `clair seed --backfill` → run retention once → backfilled rows gone, seeded catalog intact.

### 6.10 Definition of Done
`clair scheduler` stays up; reaper on 1-min singleton. `booking.PurgeOldBookingData` exists in `internal/booking/retention.go`: business-time predicate + status guard, never plain `CreatedAt`; children→parents; batched LIMIT 500; per-table `PurgeReport`; injectable `now`/`maxAge`; never deletes `active` holds, `waiting` waitlist, or `held>0` inventory. Registered as a gated daily `Cron` singleton, disabled by default; env documented. `clair seed --backfill` inserts idempotent, correctly back-dated terminal rows (UpdateColumn verified). Tests pass against real Postgres (skip cleanly with none). End-to-end manual check passes.

### Key files
New: `internal/booking/retention.go`, `internal/booking/retention_test.go`. Edit: `internal/cmd/scheduler.go`, `internal/cmd/seed.go`, `.env`, `.env.example`. Reuse: `internal/booking/hold.go:330`, `internal/testsupport/pg.go:33`, `internal/booking/hold_test.go`.

---

## Step 7 — MAP & GAME (Roadmap Step 11): phased outline

Downstream and exploratory. Assumes Steps 1–6 landed. This is a phased outline, not a full implementation spec.

### 7.1 What `/aurenor` becomes
Today bare `/aurenor` is unclaimed (`mount.go:79` group has no `GET /` handler; only the six sub-routes). Step 7 claims it as the **country home**. Two stackable responsibilities:
- **Landing/index** — an `aurenorPage`-framed page linking the four institution collections (theaters, hospitals, consulate, bureau) with blurbs, using `institutionCard`. Low-effort; ship first regardless of the map.
- **Map host** — the landing embeds/links a map of the Gilt Coast whose regions/cities (Solvarre capital, Marisk, Aurevik — `aurenor_helpers.go:36-42`) are clickable into those collections.

Register `aurenor.HandleFunc("GET /", page("Aurenor", pages.AurenorHome))` in `mountExamples` (`mount.go:79`). No new middleware — inherits `OptionalAuthMiddleware`, so a signed-in visitor is distinguishable via `page.User`.

### 7.2 Map — three options
| Option | What | Effort | Tradeoff |
|---|---|---|---|
| **A. Static SVG (recommended first)** | Hand-authored inline SVG coastline + three labelled city dots, each an `<a href>` into a collection; Tailwind/dark-mode. | S (1–2d) | Decorative geography; art is the work; zero data coupling. |
| **B. Data-driven overlay** | City→institution links generated from the venue catalogs (`Location` strings); city hotspot lists venues there. Needs a clean `City` field/normalizer on `Venue`. | M (3–5d) | Stays in sync as venues are seeded. |
| **C. Interactive canvas / pan-zoom** | Alpine pan/zoom, hover cards with live availability from `GET /appointments/schedules/{slug}/availability` + `GET /events/{slug}/seats` (public, no CSRF). | L (1–2+wk) | Where "map" blends into "game". Defer until B ships. |

Recommended: A → B → (C only if the game demands it). All reuse CSP-free inline-asset discipline and the dark-mode/Tailwind system; no external tiles/libraries.

### 7.3 How a game sits on the same namespace/data
The booking engine is already a simulation substrate (seats, holds, expiry, inventory contention, the sim tick). A game is a thin play-layer, not a new engine.
- **Namespace:** `/aurenor/*` (e.g. `/aurenor/play`), served by `mountExamples`, framed by `aurenorPage`, wrapped by `PageShell` (CSRF + current-user). Alpine `fetch` pattern (`games_tictactoe.templ:113-233`) with the CSRF recipe (read `<meta name="csrf-token">`, send `X-CSRF-Token`).
- **Data:** cities are the board; each institution a bookable node; "playing" = reserving against the real API or racing the simulator for scarce seats. Ownership/scoring keys off `OwnerRef` (`session.go:74`): `user:<id>` vs `guest:<sid>`; guest play migrates on login via the `"booking"` migrator.
- **Loop:** the scheduler's sim + reaper (Steps 4/5) supply the world moving on its own; retention (Step 6) purges old rounds.
Keep the game read-mostly at first (leaderboard/status over live availability) before write-competitive mechanics.

### 7.4 Suggested split & Definition of Done
Ship `/aurenor` landing + **Map Option A** as the concrete Step-7 deliverable now (register `GET /`, `institutionCard` links, static SVG with clickable city hotspots, CSP-safe, dark-mode). Spin the **game into a gated Step 8** once its mechanic is defined. DoD for the shippable slice: bare `/aurenor` resolves to `pages.AurenorHome`; the four collections are reachable from it; Map Option A renders and links correctly; `go build`/`templ generate` clean.

### 7.5 Open questions (must answer before the game is buildable) — see the plan's Open Questions list
What is the game (blocking); map art source; city model (first-class `City` vs normalizer); does bare `/aurenor` = landing/map/both; guest-playable vs sign-in; Map vs Game as one deliverable or two; and confirming the Step-4 scheduler fix has landed before any live-world game logic.

---

## Cross-cutting verification checklist
- After each step: `go build ./...`, `go vet ./...`, `templ generate` (Steps 2/3/7), `go test ./...` (Postgres via `testsupport`; DB-gated tests skip when `DATABASE_URL`/`TEST_DATABASE_URL` unset).
- End-to-end demo path: `clair seed` (Step 1) → book/cancel on the three appointment pages and hold seats on the theater page (Steps 2/3) → `clair scheduler` boots and the reaper ticks (Step A) → `SIM_ENABLED=true` fills realistic scarcity (Step 5) → `clair seed --backfill` + one retention pass leaves backfilled rows gone and the seeded catalog intact (Step 6) → `/aurenor` landing + map ties it together (Step 7).