> **Status (2026-09-02): executed in full on 2026-08-06.** Every task below is built; the checkboxes were never ticked. Do not re-run this plan. Kept for history.

# Live Boards and Per-Service Screens Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every booking screen fetch once and subscribe once, covering exactly what it displays, and rebuild the theater board and appointment flow around that rule.

**Architecture:** A generic snapshot cache renders each expensive read once per change and shares it. Postgres `LISTEN/NOTIFY` supplies the change counter. Three surfaces consume it: the theater board (one summary request, one stream over many events), the seat screen (unchanged), and a new per-service appointment screen (one availability request, one stream). Institution pages become plain lists so no screen ever holds data for a service the visitor is not looking at.

**Tech Stack:** Go 1.25, gorm + pgx, templ, Alpine.js 3, Tailwind v4, PostgreSQL 16.

## Global Constraints

- One screen opens **one** EventSource and subscribes to **only what it renders**. HTTP/1.1 caps a browser at ~6 connections per origin and a stream holds one for its life.
- Polling stays as the fallback path on every streaming screen; a 503 from a stream endpoint is the client's signal to fall back.
- No new JS dependencies. Alpine + templ + Tailwind only.
- Tailwind v4 scans raw source text: every class must appear as a literal in the `.templ` file, including inside `:class` ternaries. Never build a class by string concatenation.
- Handlers in `internal/server/booking` stay unexported; the route table lives in `internal/server/routes/booking.go` and reaches handlers through `booking.API` methods.
- Tests needing Postgres use `testsupport.PostgresDB` / `testsupport.PostgresSchema`. Never drop tables in the shared `public` schema.
- Run `go tool templ generate` after editing any `.templ`, and `npm run css` after adding Tailwind classes.
- Existing seeded slugs are the fixtures: theater events `grand-*` / `harborlight-*`, schedules `solvarre-general-cardiology`, `consulate-*`, `bureau-*`.

## File Structure

**Created:**
- `internal/server/booking/api_snapshot.go` — generic `snapshotCache[T]` (replaces `seatCache`).
- `internal/server/booking/api_summary.go` — event summary read, cache loader, HTTP + stream handlers.
- `internal/server/booking/api_availability_stream.go` — availability snapshot loader and stream handler.
- `internal/web/pages/aurenor_datestrip.templ` — shared date-strip markup and the Alpine day-grouping helpers.
- `internal/web/pages/aurenor_service.templ` — the appointment detail screen.
- `internal/web/pages/aurenor_service_script.templ` — its Alpine component.
- `test/aurenor_service_test.go` — routes and flow for the appointment screen.

**Modified:**
- `internal/booking/notify.go` — triggers on rules and exceptions tables.
- `internal/booking/live/hub.go` — `SubscribeAll`.
- `internal/server/booking/api_seatcache.go` → deleted, folded into `api_snapshot.go`.
- `internal/server/booking/api_events.go`, `api_stream.go`, `api_tickets.go`, `api_webhook.go`, `api.go` — use the generic cache.
- `internal/server/routes/booking.go` — new API and page routes.
- `internal/web/pages/aurenor_theater_live.templ` — full-width board on one summary request + one stream.
- `internal/web/pages/aurenor_helpers.go` — service catalog lookups.
- `internal/web/pages/aurenor_hospitals.templ`, `aurenor_consulate.templ`, `aurenor_bureau.templ` — lists of links.
- `internal/web/pages/aurenor_shared.templ` — `apptGrid` becomes a link list.
- `test/aurenor_theater_test.go`, `test/aurenor_appointments_test.go`, `test/mount_seam_test.go` — updated expectations.

**Deleted:**
- `internal/web/pages/aurenor_appointments.templ` — replaced by the service screen (its `myBookings` panel moves to `aurenor_service.templ`).

---

### Task 1: Generic snapshot cache

Replaces `seatCache` with one type used three times, so seats, summaries and availability cannot drift into three different caching behaviours.

**Files:**
- Create: `internal/server/booking/api_snapshot.go`
- Delete: `internal/server/booking/api_seatcache.go`
- Modify: `internal/server/booking/api_seatcache_test.go` → rename to `api_snapshot_test.go`
- Modify: `internal/server/booking/api_events.go`, `api_stream.go`, `api_tickets.go`, `api_webhook.go`, `api.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type snapshot struct { JSON []byte; ETag string }`
  - `type snapshotCache[T any] struct { ... }`
  - `func newSnapshotCache[T any](load func(key string) T) *snapshotCache[T]`
  - `func (c *snapshotCache[T]) Snapshot(key string, version uint64) snapshot`
  - `func (c *snapshotCache[T]) Bump(key string)`
  - Keys are strings now, not `uint`: `"event:12"`, `"schedule:4"`.

- [ ] **Step 1: Rename the test file and switch it to the generic type**

```bash
git mv internal/server/booking/api_seatcache_test.go internal/server/booking/api_snapshot_test.go
```

Then replace every `newSeatCache(func(eventID uint) []seatView {` with `newSnapshotCache(func(key string) []seatView {`, every `cache.Snapshot(7, N)` with `cache.Snapshot("event:7", N)`, `cache.Snapshot(1, 1)`/`cache.Snapshot(2, 1)` with `cache.Snapshot("event:1", 1)`/`cache.Snapshot("event:2", 1)`, and `cache.Bump(7)` with `cache.Bump("event:7")`. In `TestSeatCacheKeepsEventsApart` the loader must vary by key:

```go
cache := newSnapshotCache(func(key string) []seatView {
	return []seatView{{ID: 1, Label: key, State: "free"}}
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/server/booking/ -run TestSeatCache`
Expected: FAIL — `undefined: newSnapshotCache`

- [ ] **Step 3: Write the generic cache**

Create `internal/server/booking/api_snapshot.go` with the body of the current `api_seatcache.go`, changed as follows: `seatSnapshot` → `snapshot`; `seatCache` → `snapshotCache[T any]`; `load func(eventID uint) []seatView` → `load func(key string) T`; `entries map[uint]*seatCacheEntry` → `entries map[string]*snapshotCacheEntry`; `Snapshot(eventID uint, ...)` → `Snapshot(key string, ...)`; `Bump(eventID uint)` → `Bump(key string)`. Keep every comment — the reasoning about single-flight, read-your-own-write and the version counter is unchanged.

Then delete `api_seatcache.go`.

- [ ] **Step 4: Update the three call sites**

In `api.go`:

```go
cache := newSnapshotCache(func(key string) []seatView {
	return seatViews(ctx.DBConn, eventKeyID(key), "")
})
```

Add to `api_snapshot.go`:

```go
// eventKey and scheduleKey name a cache entry. They match the notification
// payloads exactly, so a topic and a cache key are never out of step.
func eventKey(id uint) string    { return fmt.Sprintf("event:%d", id) }
func scheduleKey(id uint) string { return fmt.Sprintf("schedule:%d", id) }

// eventKeyID reads the id back out of an "event:<id>" key.
func eventKeyID(key string) uint {
	var id uint64
	_, _ = fmt.Sscanf(key, "event:%d", &id)
	return uint(id)
}
```

Change every `cache.Bump(event.ID)` to `cache.Bump(eventKey(event.ID))` in `api_tickets.go` (3 sites) and `cache.Bump(eventID)` to `cache.Bump(eventKey(eventID))` in `api_webhook.go`. Change `cache.Snapshot(event.ID, ...)` to `cache.Snapshot(eventKey(event.ID), ...)` in `api_events.go` and `api_stream.go`. Change the `*seatCache` parameter type to `*snapshotCache[[]seatView]` everywhere it appears.

In `api_stream.go`, replace `seatTopic` with `eventKey` and delete `seatTopic`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/server/booking/ -run TestSeatCache -race && go build ./... && go vet ./...`
Expected: PASS, clean build

- [ ] **Step 6: Run the full suite**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./... -count=1`
Expected: all packages ok

- [ ] **Step 7: Commit**

```bash
git add internal/server/booking/
git commit -m "refactor(booking): one snapshot cache for every shared read"
```

---

### Task 2: Notify on rule and exception changes

Availability is computed from rules, so a host editing opening hours changes what is bookable without touching inventory. Today that change is silent.

**Files:**
- Modify: `internal/booking/notify.go`
- Modify: `internal/booking/notify_test.go`

**Interfaces:**
- Consumes: `booking.NotifyChannel`, `booking.InstallNotifyTriggers` (existing).
- Produces: notifications with payload `schedule:<id>` when a rule or exception row is inserted, updated or deleted.

- [ ] **Step 1: Write the failing test**

Append to `internal/booking/notify_test.go`:

```go
// TestRuleChangeNotifies: availability is computed from rules, so editing them
// changes what is bookable even though no inventory moved. A screen showing that
// schedule has to hear about it.
func TestRuleChangeNotifies(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install triggers: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("listener connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "LISTEN "+booking.NotifyChannel); err != nil {
		t.Fatalf("listen: %v", err)
	}

	rule := models.BookingAvailabilityRule{ScheduleID: 4242, Weekday: 1, StartMin: 540, EndMin: 1020}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no notification for the new rule")
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
		note, err := conn.WaitForNotification(waitCtx)
		waitCancel()
		if err != nil {
			t.Fatalf("wait for notification: %v", err)
		}
		if note.Payload == "schedule:4242" {
			return
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./internal/booking/ -run TestRuleChangeNotifies`
Expected: FAIL — "no notification for the new rule"

- [ ] **Step 3: Add the triggers**

In `internal/booking/notify.go`, add after `createNotifyTriggerDDL`:

```go
// Availability is computed from rules and exceptions, not stored, so a change to
// either changes what is bookable without any inventory moving. OLD is used on
// DELETE because NEW is null there.
const notifyScheduleFunctionDDL = `
CREATE OR REPLACE FUNCTION booking_notify_schedule() RETURNS trigger AS $fn$
DECLARE
	sched bigint;
BEGIN
	IF TG_OP = 'DELETE' THEN
		sched := OLD.schedule_id;
	ELSE
		sched := NEW.schedule_id;
	END IF;

	IF sched IS NOT NULL THEN
		PERFORM pg_notify('` + NotifyChannel + `', 'schedule:' || sched);
	END IF;
	RETURN NULL;
END;
$fn$ LANGUAGE plpgsql
`

const dropRuleTriggerDDL = `DROP TRIGGER IF EXISTS booking_rule_notify ON booking_availability_rules`

const createRuleTriggerDDL = `
CREATE TRIGGER booking_rule_notify
AFTER INSERT OR UPDATE OR DELETE ON booking_availability_rules
FOR EACH ROW EXECUTE FUNCTION booking_notify_schedule()
`

const dropExceptionTriggerDDL = `DROP TRIGGER IF EXISTS booking_exception_notify ON booking_availability_exceptions`

const createExceptionTriggerDDL = `
CREATE TRIGGER booking_exception_notify
AFTER INSERT OR UPDATE OR DELETE ON booking_availability_exceptions
FOR EACH ROW EXECUTE FUNCTION booking_notify_schedule()
`
```

Extend `InstallNotifyTriggers` to run all of them in order. Each `Exec` takes one statement — Postgres refuses to prepare a multi-command string under gorm's `PrepareStmt`:

```go
func InstallNotifyTriggers(db *gorm.DB) error {
	for _, stmt := range []string{
		notifyFunctionDDL,
		dropNotifyTriggerDDL,
		createNotifyTriggerDDL,
		notifyScheduleFunctionDDL,
		dropRuleTriggerDDL,
		createRuleTriggerDDL,
		dropExceptionTriggerDDL,
		createExceptionTriggerDDL,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./internal/booking/ -run 'TestRuleChangeNotifies|TestInventoryChangeNotifies|TestInstallNotifyTriggersIsIdempotent'`
Expected: PASS (idempotency test proves re-running the whole set is safe)

- [ ] **Step 5: Commit**

```bash
git add internal/booking/notify.go internal/booking/notify_test.go
git commit -m "feat(booking): announce rule and exception changes"
```

---

### Task 3: Hub multi-topic subscription

One connection watching thirteen showings must register once, not thirteen times into thirteen channels a handler then has to select over.

**Files:**
- Modify: `internal/booking/live/hub.go`
- Modify: `internal/booking/live/hub_test.go`

**Interfaces:**
- Consumes: `live.NewHub`, `Subscribe`, `Version` (existing).
- Produces: `func (h *Hub) SubscribeAll(topics []string) (<-chan struct{}, func())`

- [ ] **Step 1: Write the failing test**

Append to `internal/booking/live/hub_test.go`:

```go
// TestHubSubscribeAllWakesOnAnyTopic: a board watching many showings holds one
// subscription, and any of them changing wakes it.
func TestHubSubscribeAllWakesOnAnyTopic(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install triggers: %v", err)
	}

	tier := models.BookingTicketTier{Name: "GA", Kind: "ga", PriceCents: 1000, EventID: 21}
	if err := db.Create(&tier).Error; err != nil {
		t.Fatalf("create tier: %v", err)
	}
	inv := models.BookingInventory{Capacity: 5, TierID: &tier.ID}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("create inventory: %v", err)
	}

	hub := live.NewHub(os.Getenv("DATABASE_URL"), zap.NewNop())
	signals, release := hub.SubscribeAll([]string{"event:20", "event:21", "event:22"})
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !hub.Ready(ctx, 5*time.Second) {
		t.Fatal("listener never connected")
	}

	if _, err := booking.HoldUnits(db, inv.ID, 1, "guest:board", nil, time.Minute, time.Now()); err != nil {
		t.Fatalf("hold: %v", err)
	}

	select {
	case <-signals:
	case <-time.After(8 * time.Second):
		t.Fatal("no signal for a change to one of the subscribed topics")
	}
}

// TestHubSubscribeAllReleasesEveryTopic: releasing must not leave a topic
// registered, or the listener never shuts down.
func TestHubSubscribeAllReleasesEveryTopic(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set")
	}

	hub := live.NewHub(os.Getenv("DATABASE_URL"), zap.NewNop())
	_, release := hub.SubscribeAll([]string{"event:1", "event:2"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !hub.Ready(ctx, 5*time.Second) {
		t.Fatal("listener never connected")
	}

	release()

	deadline := time.Now().Add(3 * time.Second)
	for hub.Ready(context.Background(), 0) {
		if time.Now().After(deadline) {
			t.Fatal("listener still connected after the only subscriber left")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./internal/booking/live/ -run TestHubSubscribeAll`
Expected: FAIL — `hub.SubscribeAll undefined`

- [ ] **Step 3: Implement SubscribeAll**

In `internal/booking/live/hub.go`, refactor `Subscribe` to delegate and add the many-topic form:

```go
// Subscribe returns a channel that receives a signal whenever `topic` changes,
// plus the function to release it.
//
// The channel carries no payload and coalesces: it says "this changed", and the
// reader answers by re-reading the current state. A dropped duplicate signal is
// therefore harmless, which is what lets the send be non-blocking.
func (h *Hub) Subscribe(topic string) (<-chan struct{}, func()) {
	return h.SubscribeAll([]string{topic})
}

// SubscribeAll registers one channel against many topics.
//
// A board watching every showing of a theater is one reader, so it gets one
// channel and one connection. Subscribing separately per topic would leave the
// caller selecting over a dozen channels, and would tempt a browser into a
// stream per showing — which the connection-per-origin cap makes unworkable.
func (h *Hub) SubscribeAll(topics []string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	h.mu.Lock()
	for _, topic := range topics {
		if h.subs[topic] == nil {
			h.subs[topic] = map[chan struct{}]struct{}{}
		}
		h.subs[topic][ch] = struct{}{}
	}
	if !h.running && h.dsn != "" {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel, h.running = cancel, true
		go h.listen(ctx)
	}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()

			for _, topic := range topics {
				delete(h.subs[topic], ch)
				if len(h.subs[topic]) == 0 {
					delete(h.subs, topic)
				}
			}
			if len(h.subs) == 0 && h.running {
				h.cancel()
				h.running = false
				h.connected.Store(false)
			}
		})
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./internal/booking/live/ -race`
Expected: PASS (all five hub tests)

- [ ] **Step 5: Commit**

```bash
git add internal/booking/live/
git commit -m "feat(live): one subscription across many topics"
```

---

### Task 4: Event summary endpoint

Collapses the board's thirteen requests into one.

**Files:**
- Create: `internal/server/booking/api_summary.go`
- Modify: `internal/server/booking/api.go`
- Modify: `internal/server/routes/booking.go`
- Test: `test/aurenor_theater_test.go`

**Interfaces:**
- Consumes: `snapshotCache[T]`, `eventKey`, `seatVersion` (Tasks 1 and existing).
- Produces:
  - `type eventSummary struct` with JSON fields `slug`, `name`, `start_at`, `status`, `max_per_buyer`, `tier_id`, `kind`, `price_cents`, `currency`, `remaining`
  - `func (a *API) EventSummary() http.HandlerFunc` on route `GET /events/summary?slugs=`
  - `const maxSummarySlugs = 50`

- [ ] **Step 1: Write the failing test**

Append to `test/aurenor_theater_test.go`:

```go
// TestEventSummaryAnswersTheWholeBoard: the board asks once for every showing it
// lists, instead of once per showing.
func TestEventSummaryAnswersTheWholeBoard(t *testing.T) {
	ts := newTheaterServer(t)
	slugs := aurenor.TheaterEventSlugs("grand")

	resp := doJSON(t, newJarClient(), "GET",
		ts.URL+"/events/summary?slugs="+strings.Join(slugs, ","), nil)
	mustStatus(t, resp, http.StatusOK, "summary")
	defer resp.Body.Close()

	var rows []struct {
		Slug       string    `json:"slug"`
		StartAt    time.Time `json:"start_at"`
		Kind       string    `json:"kind"`
		PriceCents int64     `json:"price_cents"`
		Remaining  int       `json:"remaining"`
		TierID     uint      `json:"tier_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode summary: %v", err)
	}

	if len(rows) != len(slugs) {
		t.Fatalf("summary returned %d rows for %d showings", len(rows), len(slugs))
	}
	for _, row := range rows {
		if row.StartAt.IsZero() || row.Kind == "" || row.TierID == 0 {
			t.Errorf("summary row for %s is incomplete: %+v", row.Slug, row)
		}
		if row.Remaining <= 0 {
			t.Errorf("summary row for %s reports %d remaining on a fresh seed", row.Slug, row.Remaining)
		}
	}
}

// TestEventSummarySkipsUnknownSlugs: a stale link in a catalog must not fail the
// whole board.
func TestEventSummarySkipsUnknownSlugs(t *testing.T) {
	ts := newTheaterServer(t)

	resp := doJSON(t, newJarClient(), "GET",
		ts.URL+"/events/summary?slugs=grand-dune-part-two-1000,no-such-showing", nil)
	mustStatus(t, resp, http.StatusOK, "summary with an unknown slug")
	defer resp.Body.Close()

	var rows []struct {
		Slug string `json:"slug"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	if len(rows) != 1 || rows[0].Slug != "grand-dune-part-two-1000" {
		t.Errorf("want only the known showing, got %+v", rows)
	}
}

// TestEventSummaryIsBounded: one request must not be able to ask for unbounded
// work.
func TestEventSummaryIsBounded(t *testing.T) {
	ts := newTheaterServer(t)

	many := make([]string, 51)
	for i := range many {
		many[i] = "grand-dune-part-two-1000"
	}

	resp := doJSON(t, newJarClient(), "GET",
		ts.URL+"/events/summary?slugs="+strings.Join(many, ","), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("51 slugs: status %d, want 400", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./test/ -run TestEventSummary`
Expected: FAIL — 404 on `/events/summary`

- [ ] **Step 3: Write the summary read and handler**

Create `internal/server/booking/api_summary.go`:

```go
package booking

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/erancihan/clair/internal/booking/live"
	"github.com/erancihan/clair/internal/database/models"
	server_context "github.com/erancihan/clair/internal/server/context"
	"gorm.io/gorm"
)

// maxSummarySlugs bounds one request. An uncaught list is an invitation to make
// a single call do arbitrary work.
const maxSummarySlugs = 50

// eventSummary is a showing as a board lists it: when it is, what it costs, and
// how much of it is left. It is deliberately not the full event: a board draws
// none of the rest.
type eventSummary struct {
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	StartAt     time.Time `json:"start_at"`
	Status      string    `json:"status"`
	MaxPerBuyer int       `json:"max_per_buyer"`
	TierID      uint      `json:"tier_id"`
	Kind        string    `json:"kind"`
	PriceCents  int64     `json:"price_cents"`
	Currency    string    `json:"currency"`
	Remaining   int       `json:"remaining"`
}

// summaryFor reads one showing's summary. Every seeded showing sells through a
// single tier, so the first tier is the showing.
func summaryFor(conn *gorm.DB, eventID uint) []eventSummary {
	var event models.BookingEvent
	if res := conn.Limit(1).Find(&event, eventID); res.RowsAffected == 0 {
		return nil
	}

	var tier models.BookingTicketTier
	if res := conn.Where("event_id = ?", event.ID).Order("id").Limit(1).Find(&tier); res.RowsAffected == 0 {
		return nil
	}

	return []eventSummary{{
		Slug: event.Slug, Name: event.Name, StartAt: event.StartAt,
		Status: event.Status, MaxPerBuyer: event.MaxPerBuyer,
		TierID: tier.ID, Kind: tier.Kind, PriceCents: tier.PriceCents,
		Currency: tier.Currency, Remaining: tierRemaining(conn, tier),
	}}
}

// requestedSummaries resolves the slugs a caller asked for, in the order they
// asked, skipping any that name nothing. A stale entry in a page's catalog is
// not a reason to fail the whole board.
func requestedSummaries(ctx server_context.BackEndContext, r *http.Request, cache *snapshotCache[[]eventSummary], hub *live.Hub) ([]eventSummary, []string, bool) {
	raw := r.URL.Query().Get("slugs")
	if raw == "" {
		return nil, nil, false
	}
	slugs := strings.Split(raw, ",")
	if len(slugs) > maxSummarySlugs {
		return nil, nil, false
	}

	conn := db(ctx, r)
	out := make([]eventSummary, 0, len(slugs))
	keys := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		var event models.BookingEvent
		if res := conn.Where("slug = ?", strings.TrimSpace(slug)).Limit(1).Find(&event); res.RowsAffected == 0 {
			continue
		}
		key := eventKey(event.ID)
		keys = append(keys, key)

		var rows []eventSummary
		if err := json.Unmarshal(cache.Snapshot(key, seatVersion(hub, event.ID)).JSON, &rows); err == nil {
			out = append(out, rows...)
		}
	}
	return out, keys, true
}

// eventSummaries answers the whole board in one request.
func eventSummaries(ctx server_context.BackEndContext, hub *live.Hub, cache *snapshotCache[[]eventSummary]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, _, ok := requestedSummaries(ctx, r, cache, hub)
		if !ok {
			http.Error(w, "slugs is required, and at most 50 of them", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, rows)
	}
}
```

- [ ] **Step 4: Extract the remaining-count helper**

`getEvent` in `api_events.go` computes `remaining` inline. Move that logic into a shared function so a summary and a full event can never disagree. Add to `api_events.go`:

```go
// tierRemaining is what is left of a tier. The two kinds are counted
// differently because they are stored differently: a GA tier has one pooled
// inventory row to subtract from, while a seated tier's availability is how many
// of its seats are still free.
func tierRemaining(conn *gorm.DB, t models.BookingTicketTier) int {
	if t.Kind == "ga" {
		inv, err := kernel.InventoryByOwner(conn, kernel.TierOwner(t.ID))
		if err != nil {
			return 0
		}
		return inv.Capacity - inv.Booked - inv.Held - inv.Blocked
	}

	var n int64
	conn.Raw(`SELECT count(*) FROM booking_seats s
		JOIN booking_inventories i ON i.seat_id = s.id
		WHERE s.tier_id = ? AND s.kind = 'sellable'
		  AND i.booked + i.held + i.blocked < i.capacity`, t.ID).Scan(&n)
	return int(n)
}
```

Replace the inline block inside `getEvent`'s loop with `remaining := tierRemaining(conn, t)`.

- [ ] **Step 5: Wire the cache and the route**

In `api.go`, add a second cache and the accessor:

```go
type API struct {
	ctx      server_context.BackEndContext
	hub      *live.Hub
	seats    *snapshotCache[[]seatView]
	summary  *snapshotCache[[]eventSummary]
}
```

Build it in `NewAPI` beside the seat cache:

```go
summary := newSnapshotCache(func(key string) []eventSummary {
	return summaryFor(ctx.DBConn, eventKeyID(key))
})
```

Rename the existing `cache` field to `seats` throughout `api.go`. Add:

```go
func (a *API) EventSummary() http.HandlerFunc { return eventSummaries(a.ctx, a.hub, a.summary) }
```

In `internal/server/routes/booking.go`, inside the `events` group, **above** `GET /{slug}` so the literal path wins over the wildcard:

```go
		// The whole board in one request: a page that lists thirteen showings
		// should ask once, not thirteen times.
		events.HandleFunc("GET /summary", api.EventSummary())
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./test/ -run TestEventSummary -v`
Expected: PASS (three tests)

- [ ] **Step 7: Commit**

```bash
git add internal/server/booking/ internal/server/routes/ test/aurenor_theater_test.go
git commit -m "feat(booking): answer a whole board in one request"
```

---

### Task 5: Event summary stream

**Files:**
- Modify: `internal/server/booking/api_summary.go`
- Modify: `internal/server/booking/api.go`
- Modify: `internal/server/routes/booking.go`
- Test: `test/aurenor_theater_test.go`

**Interfaces:**
- Consumes: `requestedSummaries`, `hub.SubscribeAll`, `seatStreamDebounce`, `seatStreamHeartbeat`, `drain` (Tasks 3–4 and existing `api_stream.go`).
- Produces: `func (a *API) EventSummaryStream() http.HandlerFunc` on `GET /events/summary/stream?slugs=`

- [ ] **Step 1: Write the failing test**

Append to `test/aurenor_theater_test.go`:

```go
// TestEventSummaryStreamPushesCounts: the board's counts go live, over one
// connection covering every showing it lists.
func TestEventSummaryStreamPushesCounts(t *testing.T) {
	ts := newTheaterServer(t)
	slugs := aurenor.TheaterEventSlugs("grand")
	target, _ := firstSeated(t, ts)
	seats := getSeats(t, ts, target)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/events/summary/stream?slugs="+strings.Join(slugs, ","), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d, want 200", resp.StatusCode)
	}

	frames := make(chan map[string]int, 4)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(frames)
				return
			}
			payload, found := strings.CutPrefix(strings.TrimSpace(line), "data: ")
			if !found {
				continue
			}
			var rows []struct {
				Slug      string `json:"slug"`
				Remaining int    `json:"remaining"`
			}
			if err := json.Unmarshal([]byte(payload), &rows); err != nil {
				continue
			}
			counts := map[string]int{}
			for _, row := range rows {
				counts[row.Slug] = row.Remaining
			}
			frames <- counts
		}
	}()

	var before int
	select {
	case first := <-frames:
		before = first[target]
		if before == 0 {
			t.Fatalf("opening frame reports nothing left for %s", target)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream sent no opening snapshot")
	}

	hold := doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+target+"/seats/hold",
		map[string]any{"seat_ids": []uint{seats[0].ID, seats[1].ID}, "guest_name": "Board Watcher"})
	mustStatus(t, hold, http.StatusCreated, "hold")
	hold.Body.Close()

	deadline := time.After(15 * time.Second)
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatal("stream closed before the change arrived")
			}
			if frame[target] == before-2 {
				return
			}
		case <-deadline:
			t.Fatalf("count for %s never fell from %d", target, before)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./test/ -run TestEventSummaryStreamPushesCounts`
Expected: FAIL — 404

- [ ] **Step 3: Write the stream handler**

Append to `internal/server/booking/api_summary.go`:

```go
// eventSummaryStream pushes a whole board as its showings change.
//
// One connection covers every showing the page lists. A stream per showing would
// be tidier to write and unusable in practice: a browser allows about six
// connections per origin over HTTP/1.1, and each open stream holds one for its
// whole life.
func eventSummaryStream(ctx server_context.BackEndContext, hub *live.Hub, cache *snapshotCache[[]eventSummary]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, keys, ok := requestedSummaries(ctx, r, cache, hub)
		if !ok {
			http.Error(w, "slugs is required, and at most 50 of them", http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		signals, release := hub.SubscribeAll(keys)
		defer release()

		if !hub.Ready(r.Context(), 2*time.Second) {
			http.Error(w, "live updates unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		send := func(payload []eventSummary) bool {
			body, err := json.Marshal(payload)
			if err != nil {
				return false
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}

		if !send(rows) {
			return
		}

		heartbeat := time.NewTicker(seatStreamHeartbeat)
		defer heartbeat.Stop()

		for {
			select {
			case <-r.Context().Done():
				return

			case <-signals:
				timer := time.NewTimer(seatStreamDebounce)
				select {
				case <-r.Context().Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				drain(signals)

				fresh, _, ok := requestedSummaries(ctx, r, cache, hub)
				if !ok || !send(fresh) {
					return
				}

			case <-heartbeat.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}
```

Add `"fmt"` to the imports.

- [ ] **Step 4: Wire it**

In `api.go`:

```go
func (a *API) EventSummaryStream() http.HandlerFunc {
	return eventSummaryStream(a.ctx, a.hub, a.summary)
}
```

In `routes/booking.go`, beside the summary route:

```go
		// The same board, pushed as its showings change.
		events.HandleFunc("GET /summary/stream", api.EventSummaryStream())
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./test/ -run TestEventSummary -v`
Expected: PASS (four tests)

- [ ] **Step 6: Commit**

```bash
git add internal/server/booking/ internal/server/routes/ test/aurenor_theater_test.go
git commit -m "feat(booking): stream a whole board over one connection"
```

---

### Task 6: Availability snapshot and stream

**Files:**
- Create: `internal/server/booking/api_availability_stream.go`
- Modify: `internal/server/booking/api_availability.go`
- Modify: `internal/server/booking/api_appointments.go`
- Modify: `internal/server/booking/api.go`
- Modify: `internal/server/routes/booking.go`
- Test: `test/aurenor_appointments_test.go`

**Interfaces:**
- Consumes: `snapshotCache[T]`, `scheduleKey`, `hub.Subscribe`, `appt.ComputeAvailability`.
- Produces:
  - `func availabilityViews(conn *gorm.DB, sched models.BookingSchedule, now time.Time) []appt.OpenSlot`
  - `func scheduleVersion(hub *live.Hub, scheduleID uint) uint64`
  - `func (a *API) AvailabilityStream() http.HandlerFunc` on `GET /appointments/schedules/{slug}/availability/stream`
  - `const availabilityWindowDays = 14`

- [ ] **Step 1: Write the failing test**

Append to `test/aurenor_appointments_test.go`:

```go
// TestAvailabilityStreamPushesBookings: a booking made in one browser reaches
// another browser's open schedule without it asking again.
func TestAvailabilityStreamPushesBookings(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := kernel.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install notify triggers: %v", err)
	}
	if _, err := aurenor.Seed(db, time.Now(), "seed@aurenor.test"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ts := httptest.NewServer(server.NewBackEnd(context.Background(), zap.NewNop(), nil, db).Routes())
	t.Cleanup(ts.Close)

	const slug = "solvarre-general-cardiology"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/appointments/schedules/"+slug+"/availability/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d, want 200", resp.StatusCode)
	}

	frames := make(chan []struct {
		StartAt   time.Time `json:"start_at"`
		Remaining int       `json:"remaining"`
	}, 4)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(frames)
				return
			}
			payload, found := strings.CutPrefix(strings.TrimSpace(line), "data: ")
			if !found {
				continue
			}
			var slots []struct {
				StartAt   time.Time `json:"start_at"`
				Remaining int       `json:"remaining"`
			}
			if err := json.Unmarshal([]byte(payload), &slots); err == nil {
				frames <- slots
			}
		}
	}()

	var chosen time.Time
	select {
	case first := <-frames:
		if len(first) == 0 {
			t.Fatal("opening frame has no slots")
		}
		chosen = first[0].StartAt
	case <-time.After(10 * time.Second):
		t.Fatal("stream sent no opening snapshot")
	}

	book := doJSON(t, newJarClient(), "POST", ts.URL+"/appointments/schedules/"+slug+"/book",
		map[string]any{"start_at": chosen.Format(time.RFC3339), "guest_name": "Other Browser"})
	mustStatus(t, book, http.StatusCreated, "book")
	book.Body.Close()

	// Capacity is 1 on a hospital department, so the booked slot leaves the list.
	deadline := time.After(15 * time.Second)
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatal("stream closed before the change arrived")
			}
			gone := true
			for _, slot := range frame {
				if slot.StartAt.Equal(chosen) {
					gone = false
				}
			}
			if gone {
				return
			}
		case <-deadline:
			t.Fatal("booked slot never left the stream")
		}
	}
}
```

Add imports to that file: `bufio`, `context`, `net/http/httptest`, `github.com/erancihan/clair/internal/database`, `kernel "github.com/erancihan/clair/internal/booking"`, `github.com/erancihan/clair/internal/server`, `github.com/erancihan/clair/internal/testsupport`, `go.uber.org/zap`.

- [ ] **Step 2: Run it to verify it fails**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./test/ -run TestAvailabilityStreamPushesBookings`
Expected: FAIL — 404

- [ ] **Step 3: Extract the availability computation**

In `api_availability.go`, pull the body out into a reusable function so the one-shot read and the stream cannot compute availability differently:

```go
// availabilityWindowDays caps how far ahead a stream reports. The schedule's own
// window is still the ceiling; this only stops a 45-day consulate schedule from
// re-rendering six weeks of slots on every change.
const availabilityWindowDays = 14

// availabilityViews computes a schedule's open slots from now to the end of its
// window. Availability is derived from rules rather than stored, which is why
// this is worth sharing between readers rather than repeating per request.
func availabilityViews(conn *gorm.DB, sched models.BookingSchedule, now time.Time) []appt.OpenSlot {
	from := now
	to := now.AddDate(0, 0, sched.WindowDays)
	if capped := now.AddDate(0, 0, availabilityWindowDays); capped.Before(to) {
		to = capped
	}

	var rules []models.BookingAvailabilityRule
	var exceptions []models.BookingAvailabilityException
	conn.Where("schedule_id = ?", sched.ID).Find(&rules)
	conn.Where("schedule_id = ?", sched.ID).Find(&exceptions)

	// Booked counts for the slots that have been materialized in this range.
	// The join is left, not inner: a slot row exists from the moment it is first
	// booked, but its inventory row is what carries the count.
	type slotCount struct {
		StartAt time.Time
		Booked  int
	}
	var counts []slotCount
	conn.Raw(`SELECT bs.start_at AS start_at, COALESCE(bi.booked, 0) AS booked
		FROM booking_slots bs
		LEFT JOIN booking_inventories bi ON bi.slot_id = bs.id
		WHERE bs.schedule_id = ? AND bs.start_at >= ? AND bs.start_at < ?`,
		sched.ID, from.UTC(), to.UTC()).Scan(&counts)

	booked := make(map[time.Time]int, len(counts))
	for _, c := range counts {
		booked[c.StartAt.UTC()] = c.Booked
	}

	slots, err := appt.ComputeAvailability(&sched, rules, exceptions, booked, from, to, now)
	if err != nil {
		return nil
	}
	return slots
}
```

Leave the existing `availability` handler as it is: it honours caller-supplied `from`/`to`, which the stream deliberately does not.

- [ ] **Step 4: Write the stream**

Create `internal/server/booking/api_availability_stream.go`:

```go
package booking

import (
	"fmt"
	"net/http"
	"time"

	appt "github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/booking/live"
	server_context "github.com/erancihan/clair/internal/server/context"
)

// scheduleVersion names which snapshot of a schedule a reader wants, on the same
// terms as seats: the change count while a listener is up, a coarse clock when
// there is none.
func scheduleVersion(hub *live.Hub, scheduleID uint) uint64 {
	if version, listening := hub.Version(scheduleKey(scheduleID)); listening {
		return version
	}
	return seatClockVersionBase + uint64(time.Now().UnixMilli()/seatPollCoalesce.Milliseconds())
}

// availabilityStream pushes a schedule's open slots as they change.
//
// Availability is computed, not stored, so it is more expensive to produce than
// a seat map and equally worth producing once per change rather than once per
// reader. The window is the server's choice, not the caller's, which keeps this
// to one shared snapshot per schedule instead of one per requested range.
func availabilityStream(ctx server_context.BackEndContext, hub *live.Hub, cache *snapshotCache[[]appt.OpenSlot]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadSchedule(ctx, r, r.PathValue("slug"))
		if !ok || !sched.Active {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		key := scheduleKey(sched.ID)
		signals, release := hub.Subscribe(key)
		defer release()

		if !hub.Ready(r.Context(), 2*time.Second) {
			http.Error(w, "live updates unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		send := func() bool {
			snap := cache.Snapshot(key, scheduleVersion(hub, sched.ID))
			if _, err := fmt.Fprintf(w, "data: %s\n\n", snap.JSON); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}

		if !send() {
			return
		}

		heartbeat := time.NewTicker(seatStreamHeartbeat)
		defer heartbeat.Stop()

		for {
			select {
			case <-r.Context().Done():
				return

			case <-signals:
				timer := time.NewTimer(seatStreamDebounce)
				select {
				case <-r.Context().Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				drain(signals)
				if !send() {
					return
				}

			case <-heartbeat.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}
```

- [ ] **Step 5: Bump the availability cache on local writes**

A booking made in this process must be visible to the next read even with no listener. In `api_appointments.go`, add a `cache *snapshotCache[[]appt.OpenSlot]` parameter to `book` and `cancelByToken`, and after a successful booking:

```go
		default:
			cache.Bump(scheduleKey(sched.ID))
			writeJSON(w, http.StatusCreated, appointment)
```

`cancelByToken` does not load a schedule, so resolve it from the appointment before cancelling:

```go
		var appointment models.BookingAppointment
		conn := db(ctx, r)
		conn.Where("cancel_token = ?", token).Limit(1).Find(&appointment)

		if err := appt.CancelByToken(conn, token); err != nil {
			ctx.Logger.Error("cancel appointment", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if appointment.ScheduleID != 0 {
			cache.Bump(scheduleKey(appointment.ScheduleID))
		}
```

- [ ] **Step 6: Wire the cache and routes**

In `api.go`, add the third cache:

```go
	availability *snapshotCache[[]appt.OpenSlot]
```

```go
availability := newSnapshotCache(func(key string) []appt.OpenSlot {
	var sched models.BookingSchedule
	if res := ctx.DBConn.Limit(1).Find(&sched, scheduleKeyID(key)); res.RowsAffected == 0 {
		return nil
	}
	return availabilityViews(ctx.DBConn, sched, time.Now())
})
```

Add to `api_snapshot.go`:

```go
// scheduleKeyID reads the id back out of a "schedule:<id>" key.
func scheduleKeyID(key string) uint {
	var id uint64
	_, _ = fmt.Sscanf(key, "schedule:%d", &id)
	return uint(id)
}
```

Accessors:

```go
func (a *API) AvailabilityStream() http.HandlerFunc {
	return availabilityStream(a.ctx, a.hub, a.availability)
}
```

Update `Book()` and `CancelByToken()` to pass `a.availability`.

In `routes/booking.go`, under the public reads of the appointments group:

```go
		// The same availability, pushed as it changes.
		appointments.HandleFunc("GET /schedules/{slug}/availability/stream", api.AvailabilityStream())
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./test/ -run 'TestAvailabilityStream|TestAurenorGuestBooksAndCancels' -v`
Expected: PASS

- [ ] **Step 8: Run the full suite**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./... -count=1`
Expected: all packages ok

- [ ] **Step 9: Commit**

```bash
git add internal/server/booking/ internal/server/routes/ test/
git commit -m "feat(booking): stream appointment availability"
```

---

### Task 7: Shared date strip

**Files:**
- Create: `internal/web/pages/aurenor_datestrip.templ`
- Test: node harness (see Step 4)

**Interfaces:**
- Consumes: `aurenorScript()` globals (`aurenorTime`, `aurenorWhen`).
- Produces:
  - `templ aurenorDateStrip()` — markup bound to `days`, `selectedDay`, `selectDay(key)` on the surrounding Alpine component.
  - `window.aurenorDays(items, field)` — groups items into `[{key,label,short,items}]` by local date.

- [ ] **Step 1: Write the component**

Create `internal/web/pages/aurenor_datestrip.templ`:

```go
package pages

// The date strip both booking surfaces share.
//
// A film shows once or twice a day and a clinic opens the same hours every day,
// so days are a filter, not a heading to scroll past. One component for both
// surfaces keeps the theater and the hospital from drifting into two different
// date pickers.
templ aurenorDateStrip() {
	<div class="-mx-4 overflow-x-auto px-4 sm:mx-0 sm:px-0">
		<div class="flex w-max gap-2 border-b border-zinc-200 pb-3 dark:border-zinc-800" role="tablist" aria-label="Choose a day">
			<template x-for="day in days" :key="day.key">
				<button type="button" role="tab" @click="selectDay(day.key)"
					:aria-selected="selectedDay === day.key"
					:class="selectedDay === day.key
						? 'bg-teal-600 text-white ring-teal-600'
						: 'bg-white text-zinc-600 ring-zinc-200 hover:ring-teal-500 dark:bg-zinc-900 dark:text-zinc-300 dark:ring-zinc-700'"
					class="flex min-w-16 flex-col items-center rounded-2xl px-3 py-2 text-center ring-1 transition">
					<span class="text-[11px] uppercase tracking-wide opacity-70" x-text="day.short"></span>
					<span class="text-base font-semibold tabular-nums" x-text="day.dayNumber"></span>
					<span class="text-[11px] opacity-70" x-text="day.month"></span>
				</button>
			</template>
		</div>
	</div>
}

// aurenorDaysScript groups a list of timestamped things into days. Both surfaces
// build their strip from whatever they already fetched, so there is no "which
// days are open" endpoint to invent.
templ aurenorDaysScript() {
	<script>
		window.aurenorDays = window.aurenorDays || function (items, field) {
			const days = [];
			let current = null;
			for (const item of items) {
				const iso = item[field];
				if (!iso) continue;
				const when = new Date(iso);
				const key = when.toDateString();
				if (!current || current.key !== key) {
					current = {
						key: key,
						label: when.toLocaleDateString([], { weekday: 'long', month: 'long', day: 'numeric' }),
						short: when.toLocaleDateString([], { weekday: 'short' }),
						dayNumber: String(when.getDate()),
						month: when.toLocaleDateString([], { month: 'short' }),
						items: [],
					};
					days.push(current);
				}
				current.items.push(item);
			}
			return days;
		};
	</script>
}
```

- [ ] **Step 2: Generate and build**

Run: `go tool templ generate && go build ./...`
Expected: clean

- [ ] **Step 3: Write the node harness for the grouping helper**

Create `/tmp/claude-*/scratchpad/days.mjs` (scratchpad, not committed):

```js
import fs from 'fs';
import vm from 'vm';

const src = fs.readFileSync(process.argv[2], 'utf8');
const sandbox = { window: {}, Date, String, console };
sandbox.globalThis = sandbox;
vm.createContext(sandbox);
vm.runInContext(src.match(/<script>([\s\S]*?)<\/script>/)[1], sandbox);

const items = [
  { start_at: '2026-08-06T10:00:00Z' },
  { start_at: '2026-08-06T19:30:00Z' },
  { start_at: '2026-08-08T12:00:00Z' },
];
const days = sandbox.window.aurenorDays(items, 'start_at');
console.log('days:', days.length, '| first has', days[0].items.length, 'items');
console.log('labels:', days.map(d => d.short + ' ' + d.dayNumber).join(', '));
if (days.length !== 2) { console.error('FAIL: expected 2 days'); process.exit(1); }
if (days[0].items.length !== 2) { console.error('FAIL: expected 2 items on day one'); process.exit(1); }
console.log('OK');
```

- [ ] **Step 4: Run it**

Run: `node /tmp/claude-*/scratchpad/days.mjs internal/web/pages/aurenor_datestrip.templ`
Expected: `days: 2 | first has 2 items` then `OK`

- [ ] **Step 5: Commit**

```bash
git add internal/web/pages/aurenor_datestrip.templ internal/web/pages/aurenor_datestrip_templ.go
git commit -m "feat(web): shared date strip"
```

---

### Task 8: Theater board rebuilt

**Files:**
- Modify: `internal/web/pages/aurenor_theater_live.templ`
- Test: `test/aurenor_theater_test.go`

**Interfaces:**
- Consumes: `GET /events/summary`, `GET /events/summary/stream`, `aurenorDateStrip()`, `aurenorDays`, `theaterAttr(v)` (existing).
- Produces: nothing other tasks consume.

- [ ] **Step 1: Write the failing render test**

Append to `test/aurenor_theater_test.go`:

```go
// TestTheaterBoardIsFullWidthWithDateTabs pins the board's shape: one date
// strip, no two-column grid of half-empty cards.
func TestTheaterBoardIsFullWidthWithDateTabs(t *testing.T) {
	srv := newRoutesServer(t)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	resp, err := client.Get(srv.URL + "/aurenor/theaters/grand")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body := readBody(t, resp)

	if !strings.Contains(body, `role="tablist"`) {
		t.Error("board renders no date strip")
	}
	if strings.Contains(body, "sm:grid-cols-2") {
		t.Error("board still lays films out in a two-column grid")
	}
	if !strings.Contains(body, "/events/summary") {
		t.Error("board does not use the one-request summary endpoint")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./test/ -run TestTheaterBoardIsFullWidthWithDateTabs`
Expected: FAIL — no tablist, grid still present

- [ ] **Step 3: Rewrite the board markup**

Replace the contents of `templ theaterLive(v Venue)` in `internal/web/pages/aurenor_theater_live.templ`:

```go
templ theaterLive(v Venue) {
	<div x-data={ theaterAttr(v) } class="space-y-6">
		@aurenorScript()
		@aurenorDaysScript()
		@theaterBoardScript()

		<p x-show="loading" class="text-sm text-zinc-500 dark:text-zinc-400" role="status">Loading showtimes…</p>
		<p x-show="!loading && error" x-cloak role="alert"
			class="text-sm text-red-600 dark:text-red-400" x-text="error"></p>

		<div x-show="!loading && days.length > 0" x-cloak>
			@aurenorDateStrip()
		</div>

		<div class="space-y-4">
			<template x-for="film in filmsOnDay" :key="film.key">
				<article class="flex flex-col gap-4 rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm sm:flex-row sm:items-start sm:gap-6 sm:p-6 dark:border-zinc-800 dark:bg-zinc-800">
					// No posters exist in this system, so the monogram is the anchor
					// that stops a full-width row from reading as unfinished.
					<span class="flex h-14 w-14 flex-none items-center justify-center rounded-xl bg-teal-50 text-xl font-semibold text-teal-700 dark:bg-teal-400/10 dark:text-teal-300"
						x-text="film.initial"></span>

					<div class="sm:w-56 sm:flex-none">
						<h3 class="text-lg font-semibold text-zinc-800 dark:text-zinc-100" x-text="film.film"></h3>
						<p class="mt-0.5 text-sm text-zinc-500 dark:text-zinc-400" x-text="film.salon"></p>
						<div class="mt-2 flex flex-wrap items-center gap-2 text-xs">
							<span class="rounded-full px-2.5 py-1 font-medium"
								:class="film.kind === 'seated'
									? 'bg-teal-50 text-teal-700 dark:bg-teal-400/10 dark:text-teal-300'
									: 'bg-zinc-100 text-zinc-600 dark:bg-zinc-700/50 dark:text-zinc-300'"
								x-text="film.kind === 'seated' ? 'Pick your seats' : 'Open seating'"></span>
							<span class="text-zinc-500 dark:text-zinc-400" x-text="money(film.priceCents)"></span>
						</div>
					</div>

					<div class="flex flex-1 flex-wrap gap-2">
						<template x-for="showing in film.showings" :key="showing.slug">
							<a :href="showing.remaining === 0 ? null : showingHref(showing)"
								:aria-disabled="showing.remaining === 0"
								:aria-label="aurenorTime(showing.start_at) + ', ' + (showing.remaining === 0 ? 'sold out' : showing.remaining + ' left')"
								:class="showing.remaining === 0
									? 'cursor-not-allowed bg-zinc-50 text-zinc-400 line-through ring-zinc-200 dark:bg-zinc-900/40 dark:text-zinc-600 dark:ring-zinc-700'
									: 'bg-white text-zinc-700 ring-zinc-200 hover:bg-teal-50 hover:ring-teal-500 dark:bg-zinc-900 dark:text-zinc-200 dark:ring-zinc-700 dark:hover:bg-teal-400/10'"
								class="block rounded-xl px-4 py-2.5 text-center ring-1 transition">
								<span class="block text-base font-medium tabular-nums" x-text="aurenorTime(showing.start_at)"></span>
								<span class="block text-xs opacity-70"
									x-text="showing.remaining === 0 ? 'sold out' : showing.remaining + ' left'"></span>
							</a>
						</template>
					</div>
				</article>
			</template>
		</div>

		<p x-show="!loading && days.length > 0 && filmsOnDay.length === 0" x-cloak
			class="rounded-2xl bg-zinc-50 px-4 py-3 text-sm text-zinc-600 dark:bg-zinc-900/50 dark:text-zinc-400">
			No showings on <span x-text="selectedDayLabel"></span>.
			<button type="button" x-show="nextDayWithShowings" @click="selectDay(nextDayWithShowings)"
				class="font-medium text-teal-600 underline dark:text-teal-400">See the next day that has some</button>
		</p>

		@myTheaterHolds()
	</div>
}
```

- [ ] **Step 4: Rewrite the board component**

Replace `templ theaterBoardScript()`'s `Alpine.data('theater', …)` body with:

```js
			Alpine.data('theater', (catalog) => ({
				catalog: catalog,
				showings: [],
				selectedDay: '',
				loading: true,
				error: '',
				stream: null,
				poller: null,

				async init() {
					await this.load();
					this.loading = false;
					if (this.showings.length > 0) this.openStream();
				},

				// One request for the whole board. Thirteen showings used to mean
				// thirteen requests, which is thirteen times the work to answer the
				// same question.
				async load() {
					try {
						const res = await fetch('/events/summary?slugs=' +
							encodeURIComponent(this.catalog.map(c => c.slug).join(',')));
						if (!res.ok) {
							this.error = 'No showings are on sale right now.';
							return;
						}
						this.apply((await res.json()) || []);
					} catch (e) {
						this.error = 'Could not reach the box office.';
					}
				},

				// One connection for the whole board, for the same reason: a browser
				// allows about six per origin and a stream holds one for its life.
				openStream() {
					if (!window.EventSource) {
						this.startPolling();
						return;
					}
					const stream = new EventSource('/events/summary/stream?slugs=' +
						encodeURIComponent(this.catalog.map(c => c.slug).join(',')));
					this.stream = stream;

					stream.onmessage = (event) => {
						try {
							this.apply(JSON.parse(event.data));
						} catch (e) {
							// A malformed frame is not worth tearing the stream down for.
						}
					};
					stream.onerror = () => {
						stream.close();
						if (this.stream === stream) this.stream = null;
						if (!this.poller) this.startPolling();
					};
				},

				startPolling() {
					this.stopPolling();
					this.poller = setInterval(() => {
						if (!document.hidden) this.load();
					}, 15000);
				},

				stopPolling() {
					if (this.poller) clearInterval(this.poller);
					this.poller = null;
				},

				apply(rows) {
					const bySlug = new Map(this.catalog.map(c => [c.slug, c]));
					this.showings = rows
						.map(row => Object.assign({}, bySlug.get(row.slug) || {}, row))
						.sort((a, b) => Date.parse(a.start_at) - Date.parse(b.start_at));
					this.error = this.showings.length === 0 ? 'No showings are on sale right now.' : '';

					// Keep the visitor on the day they chose; fall back to the first
					// day that has anything.
					const days = this.days;
					if (!days.some(day => day.key === this.selectedDay)) {
						this.selectedDay = days.length > 0 ? days[0].key : '';
					}
				},

				get days() {
					return window.aurenorDays(this.showings, 'start_at');
				},

				selectDay(key) {
					this.selectedDay = key;
				},

				get selectedDayLabel() {
					const day = this.days.find(d => d.key === this.selectedDay);
					return day ? day.label : '';
				},

				get nextDayWithShowings() {
					const day = this.days.find(d => d.items.length > 0 && d.key !== this.selectedDay);
					return day ? day.key : '';
				},

				// Films are the unit a filmgoer decides on; the day is the filter.
				get filmsOnDay() {
					const day = this.days.find(d => d.key === this.selectedDay);
					if (!day) return [];

					const byFilm = new Map();
					for (const showing of day.items) {
						if (!byFilm.has(showing.film)) {
							byFilm.set(showing.film, {
								key: showing.film.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
								film: showing.film,
								initial: showing.film.slice(0, 1),
								salon: showing.salon,
								kind: showing.kind,
								priceCents: showing.price_cents,
								showings: [],
							});
						}
						byFilm.get(showing.film).showings.push(showing);
					}
					return Array.from(byFilm.values());
				},

				showingHref(showing) {
					return window.location.pathname.replace(/\/$/, '') + '/showings/' + showing.slug;
				},

				money(cents) {
					return aurenorMoney(cents, 'USD');
				},

				destroy() {
					this.stopPolling();
					if (this.stream) this.stream.close();
				},
			}));
```

- [ ] **Step 5: Generate, build, run the tests**

Run: `go tool templ generate && npm run css && go build ./... && export $(grep -E '^DATABASE_URL=' .env | xargs) && go test ./test/ -run 'TestTheater' -v`
Expected: PASS

- [ ] **Step 6: Check the rendered page by hand**

Run: `go run ./cmd/clair server` then load `http://localhost:4000/aurenor/theaters/grand`.
Expected: a date strip; full-width rows; picking a day changes which films show; opening the same page twice and holding a seat in one moves the count in the other without a refresh.

- [ ] **Step 7: Commit**

```bash
git add internal/web/pages/ test/aurenor_theater_test.go
git commit -m "feat(web): full-width theater board with date tabs and live counts"
```

---

### Task 9: Appointment service screen

**Files:**
- Create: `internal/web/pages/aurenor_service.templ`
- Create: `internal/web/pages/aurenor_service_script.templ`
- Create: `test/aurenor_service_test.go`
- Modify: `internal/web/pages/aurenor_helpers.go`
- Modify: `internal/server/routes/booking.go`

**Interfaces:**
- Consumes: `aurenorDateStrip()`, `aurenorDays`, `GET /appointments/schedules/{slug}/availability`, its `/stream`, `POST .../book`, `POST /appointments/cancel`.
- Produces:
  - `func FindService(institution, venue, service string) (ServiceCard, bool)` in `pages`
  - `type ServiceCard struct { Institution, Venue, Slug, Title, Blurb, ScheduleSlug string }`
  - `templ AurenorService(card ServiceCard)`
  - `func serviceAttr(card ServiceCard) string`

- [ ] **Step 1: Write the failing route test**

Create `test/aurenor_service_test.go`:

```go
package test

import (
	"net/http"
	"strings"
	"testing"
)

// TestServiceScreensResolve: every service the institution pages link to has a
// screen of its own, and nothing else does.
func TestServiceScreensResolve(t *testing.T) {
	srv := newRoutesServer(t)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	tests := []struct {
		path   string
		status int
		body   string
	}{
		{path: "/aurenor/hospitals/solvarre-general/cardiology", status: http.StatusOK, body: "solvarre-general-cardiology"},
		{path: "/aurenor/consulate/tourist-visa-interview", status: http.StatusOK, body: "consulate-tourist-visa-interview"},
		{path: "/aurenor/bureau/fingerprint-appointment", status: http.StatusOK, body: "bureau-fingerprint-appointment"},
		{path: "/aurenor/hospitals/solvarre-general/no-such-department", status: http.StatusNotFound},
		{path: "/aurenor/hospitals/no-such-hospital/cardiology", status: http.StatusNotFound},
		{path: "/aurenor/consulate/no-such-service", status: http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := client.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
			if tc.body != "" {
				if body := readBody(t, resp); !strings.Contains(body, tc.body) {
					t.Errorf("page does not bind schedule %q", tc.body)
				}
			}
		})
	}
}

// TestInstitutionPagesOnlyLink: a list page must not carry availability for
// services the visitor is not looking at.
func TestInstitutionPagesOnlyLink(t *testing.T) {
	srv := newRoutesServer(t)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	for _, path := range []string{"/aurenor/hospitals/solvarre-general", "/aurenor/consulate", "/aurenor/bureau"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("request to %s failed: %v", path, err)
		}
		body := readBody(t, resp)
		resp.Body.Close()

		if strings.Contains(body, "apptBooker") {
			t.Errorf("%s still embeds a booking component per service", path)
		}
		if !strings.Contains(body, "/aurenor/") {
			t.Errorf("%s links to no service screens", path)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./test/ -run 'TestServiceScreensResolve|TestInstitutionPagesOnlyLink'`
Expected: FAIL — 404 on the service paths

- [ ] **Step 3: Add the service catalog lookup**

In `internal/web/pages/aurenor_helpers.go`:

```go
// ServiceCard is one bookable service on its own screen: which institution it
// belongs to, and which schedule it books.
type ServiceCard struct {
	Institution  string // "hospitals" | "consulate" | "bureau"
	Venue        string // hospital slug; empty for the single-body institutions
	Slug         string // "cardiology"
	Title        string
	Blurb        string
	ScheduleSlug string
}

// ServicesFor lists an institution's services. The hospital list is per venue
// because each hospital books its own inventory.
func ServicesFor(institution, venue string) []ServiceCard {
	switch institution {
	case "hospitals":
		v, ok := FindHospital(venue)
		if !ok {
			return nil
		}
		return serviceCards("hospitals", v.Slug, hospitalDepartments(v))
	case "consulate":
		return serviceCards("consulate", "", aurenorConsulateServices)
	case "bureau":
		return serviceCards("bureau", "", aurenorBureauServices)
	}
	return nil
}

func serviceCards(institution, venue string, cards []apptCard) []ServiceCard {
	out := make([]ServiceCard, len(cards))
	for i, c := range cards {
		out[i] = ServiceCard{
			Institution: institution, Venue: venue,
			Slug:  aurenor.Slugify(c.Title),
			Title: c.Title, Blurb: c.Blurb, ScheduleSlug: c.ScheduleSlug,
		}
	}
	return out
}

// FindService resolves one service screen.
func FindService(institution, venue, service string) (ServiceCard, bool) {
	for _, card := range ServicesFor(institution, venue) {
		if card.Slug == service {
			return card, true
		}
	}
	return ServiceCard{}, false
}

// ServiceHref is where a service's screen lives.
func ServiceHref(card ServiceCard) string {
	if card.Venue != "" {
		return "/aurenor/" + card.Institution + "/" + card.Venue + "/" + card.Slug
	}
	return "/aurenor/" + card.Institution + "/" + card.Slug
}

// serviceAttr binds the screen to its schedule.
func serviceAttr(card ServiceCard) string {
	cfg, _ := json.Marshal(map[string]string{
		"slug":     card.ScheduleSlug,
		"title":    card.Title,
		"backHref": serviceBackHref(card),
	})
	return "service(" + string(cfg) + ")"
}

func serviceBackHref(card ServiceCard) string {
	if card.Venue != "" {
		return "/aurenor/" + card.Institution + "/" + card.Venue
	}
	return "/aurenor/" + card.Institution
}
```

- [ ] **Step 4: Turn the institution pages into lists**

Replace `apptGrid` in `internal/web/pages/aurenor_shared.templ`:

```go
// apptGrid lists an institution's services. It renders links and nothing else:
// a page that showed availability for every service would have to subscribe to
// every service, and no visitor is looking at more than one.
templ apptGrid(cards []ServiceCard) {
	<div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
		for _, c := range cards {
			<a href={ templ.SafeURL(ServiceHref(c)) }
				class="group flex items-start justify-between gap-4 rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm transition hover:border-teal-500/40 dark:border-zinc-800 dark:bg-zinc-800">
				<span class="min-w-0">
					<span class="block font-semibold text-zinc-800 transition group-hover:text-teal-500 dark:text-zinc-100 dark:group-hover:text-teal-400">{ c.Title }</span>
					<span class="mt-1 block text-sm text-zinc-500 dark:text-zinc-400">{ c.Blurb }</span>
				</span>
				<span class="flex-none text-zinc-300 transition group-hover:text-teal-500 dark:text-zinc-600" aria-hidden="true">→</span>
			</a>
		}
	</div>
	@myBookings()
	@aurenorScript()
	@apptBookerScript()
}
```

Then delete `myBookings`, `apptBookerScript` and `apptBookable` from `aurenor_appointments.templ`, keeping only `myBookings` and its storage helpers — move those two into `aurenor_service_script.templ` in Step 6 and delete `aurenor_appointments.templ` entirely. Update the three page files to pass `ServicesFor(...)`:

- `aurenor_hospitals.templ`: `@apptGrid(ServicesFor("hospitals", v.Slug))`
- `aurenor_consulate.templ`: `@apptGrid(ServicesFor("consulate", ""))`
- `aurenor_bureau.templ`: `@apptGrid(ServicesFor("bureau", ""))`

- [ ] **Step 5: Write the service screen markup**

Create `internal/web/pages/aurenor_service.templ` with: a back link to the institution, the service title and blurb, `@aurenorDateStrip()`, times for the selected day grouped Morning / Afternoon, the booking form directly beneath the chosen time, the confirmation panel, and `@myBookings()`. The cancel-token warning appears **above** the confirm button, not after booking:

```go
templ AurenorService(card ServiceCard) {
	<div class="mt-16 sm:mt-16 sm:px-8">
		<div class="mx-auto max-w-7xl lg:px-8">
			<div class="relative px-4 sm:px-8 lg:px-12">
				<div class="mx-auto max-w-3xl" x-data={ serviceAttr(card) }>
					@aurenorScript()
					@aurenorDaysScript()
					@serviceScript()

					<a href={ templ.SafeURL(serviceBackHref(card)) }
						class="inline-flex items-center gap-1.5 text-sm text-zinc-500 transition hover:text-teal-600 dark:text-zinc-400 dark:hover:text-teal-400">
						<span aria-hidden="true">←</span> Back
					</a>

					<header class="mt-4">
						<h1 class="text-3xl font-bold tracking-tight text-zinc-800 dark:text-zinc-100 sm:text-4xl">{ card.Title }</h1>
						<p class="mt-2 text-base text-zinc-600 dark:text-zinc-400">{ card.Blurb }</p>
					</header>

					<p x-show="loading" role="status" class="mt-8 text-sm text-zinc-500 dark:text-zinc-400">Loading times…</p>
					<p x-show="error" x-cloak role="alert"
						class="mt-6 rounded-xl bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300" x-text="error"></p>

					<div x-show="!loading && !confirmation" x-cloak class="mt-8">
						<div x-show="days.length > 0">
							@aurenorDateStrip()
						</div>

						<p x-show="days.length === 0" class="rounded-2xl bg-zinc-50 px-4 py-3 text-sm text-zinc-600 dark:bg-zinc-900/50 dark:text-zinc-400">
							No times are open in the next two weeks.
						</p>

						<template x-for="group in groupsOnDay" :key="group.name">
							<div class="mt-6">
								<p class="text-xs font-medium uppercase tracking-wide text-zinc-400 dark:text-zinc-500" x-text="group.name"></p>
								<div class="mt-2 flex flex-wrap gap-2">
									<template x-for="slot in group.slots" :key="slot.start_at">
										<button type="button" @click="choose(slot)"
											:aria-pressed="chosen && chosen.start_at === slot.start_at"
											:class="chosen && chosen.start_at === slot.start_at
												? 'bg-teal-600 text-white ring-teal-600'
												: 'bg-white text-zinc-700 ring-zinc-200 hover:ring-teal-500 dark:bg-zinc-900 dark:text-zinc-200 dark:ring-zinc-700'"
											class="rounded-xl px-4 py-2.5 text-sm font-medium tabular-nums ring-1 transition"
											x-text="aurenorTime(slot.start_at)"></button>
									</template>
								</div>
							</div>
						</template>

						<div x-show="chosen" x-cloak class="mt-6 rounded-2xl border border-zinc-200 bg-white p-5 dark:border-zinc-800 dark:bg-zinc-800">
							<p class="text-sm font-medium text-zinc-800 dark:text-zinc-100">
								Booking <span x-text="chosen ? aurenorWhen(chosen.start_at) : ''"></span>
							</p>
							<div class="mt-3 grid gap-2 sm:grid-cols-2">
								<input id="service-name" x-model="guestName" type="text" placeholder="Your name (required)"
									class="rounded-xl border border-zinc-200 bg-white px-3 py-2 text-sm text-zinc-800 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"/>
								<input id="service-email" x-model="guestEmail" type="email" placeholder="Email (optional)"
									class="rounded-xl border border-zinc-200 bg-white px-3 py-2 text-sm text-zinc-800 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"/>
							</div>
							// Said before booking, while the visitor can still act on it.
							<p class="mt-3 text-xs text-zinc-500 dark:text-zinc-400">
								You are booking as a guest. The cancellation code appears once, on the next screen, and is the only way to cancel from this browser.
							</p>
							<p x-show="nameError" x-cloak role="alert" class="mt-2 text-xs text-red-600 dark:text-red-400" x-text="nameError"></p>
							<button type="button" @click="confirm()" :disabled="booking"
								class="mt-4 inline-flex items-center rounded-full bg-teal-600 px-5 py-2.5 text-sm font-medium text-white transition hover:bg-teal-500 disabled:cursor-not-allowed disabled:bg-zinc-300 dark:disabled:bg-zinc-700"
								x-text="booking ? 'Booking…' : 'Confirm booking'">Confirm booking</button>
						</div>
					</div>

					<div x-show="confirmation" x-cloak class="mt-8 rounded-3xl bg-teal-50 p-6 dark:bg-teal-400/10">
						<h2 class="text-lg font-semibold text-teal-900 dark:text-teal-100">
							Booked · <span x-text="confirmation ? aurenorWhen(confirmation.start_at) : ''"></span>
						</h2>
						<p class="mt-2 text-sm text-teal-800/80 dark:text-teal-200/70">
							Keep this cancellation code. It is the only proof this booking is yours.
						</p>
						<code class="mt-2 block break-all font-mono text-xs text-teal-900/70 dark:text-teal-100/60"
							x-text="confirmation ? confirmation.cancel_token : ''"></code>
						<button type="button" @click="reset()"
							class="mt-4 rounded-full bg-white/70 px-4 py-2 text-sm font-medium text-teal-900 transition hover:bg-white dark:bg-zinc-800/60 dark:text-teal-100">
							Book another time
						</button>
					</div>

					@myBookings()
				</div>
			</div>
		</div>
	</div>
}
```

- [ ] **Step 6: Write the component**

Create `internal/web/pages/aurenor_service_script.templ` holding `Alpine.data('service', …)` plus the `myBookings` panel and its localStorage helpers moved verbatim from `aurenor_appointments.templ`. The component:

- `init()`: `await this.load()`, then `this.openStream()`.
- `load()`: `GET /appointments/schedules/{slug}/availability`, `this.apply(slots)`.
- `openStream()`: `EventSource('/appointments/schedules/' + slug + '/availability/stream')`, `onmessage` → `apply`, `onerror` → close, fall back to `startPolling()` at 15s.
- `apply(slots)`: store, rebuild `days` via `window.aurenorDays(slots, 'start_at')`, keep `selectedDay` if it still exists else first day. **Never clear `this.error` here** — that is the bug where a 409 erased its own message.
- `groupsOnDay`: the selected day's slots split into Morning (before 12:00 local) and Afternoon.
- `choose(slot)`, `confirm()`, `reset()`, `selectDay(key)`.
- `confirm()`: POST with `X-CSRF-Token`; 201 → `confirmation = appt`, `aurenorRemember(...)`; 409 → set error, refresh, **keep the error**; 403 → stale-page message.

- [ ] **Step 7: Add the routes**

In `internal/server/routes/booking.go`, inside `mountAurenor`:

```go
		nation.HandleFunc("GET /hospitals/{slug}/{service}", servicePage("hospitals"))
		nation.HandleFunc("GET /consulate/{service}", servicePage("consulate"))
		nation.HandleFunc("GET /bureau/{service}", servicePage("bureau"))
```

and the handler:

```go
// servicePage renders one bookable service's screen, or 404s when the
// institution does not offer it.
func servicePage(institution string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		venue := ""
		if institution == "hospitals" {
			venue = req.PathValue("slug")
		}
		card, ok := pages.FindService(institution, venue, req.PathValue("service"))
		if !ok {
			http.NotFound(w, req)
			return
		}
		templ.Handler(web.Base(api_auth.PageShell(w, req, "Aurenor · "+card.Title), pages.AurenorService(card))).ServeHTTP(w, req)
	}
}
```

- [ ] **Step 8: Generate, build, run the tests**

Run: `go tool templ generate && npm run css && go build ./... && export $(grep -E '^DATABASE_URL=' .env | xargs) && go test ./test/ -run 'TestServiceScreens|TestInstitutionPagesOnlyLink|TestAurenor' -v`
Expected: PASS

- [ ] **Step 9: Update the stale appointment tests**

`test/aurenor_appointments_test.go`'s `TestAurenorPagesCarryBookers` asserts `apptBooker` appears on the institution pages. That is now false by design. Rewrite it to assert the institution pages link to service screens, and that a **service screen** binds its schedule slug:

```go
// TestAurenorServiceScreensBindTheirSchedule replaces the old expectation that
// every institution page embedded a booking component per service.
func TestAurenorServiceScreensBindTheirSchedule(t *testing.T) {
	srv := newRoutesServer(t)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	tests := map[string]string{
		"/aurenor/hospitals/solvarre-general/cardiology": "solvarre-general-cardiology",
		"/aurenor/hospitals/gilt-coast-clinic/cardiology": "gilt-coast-clinic-cardiology",
		"/aurenor/consulate/tourist-visa-interview":      "consulate-tourist-visa-interview",
		"/aurenor/bureau/fingerprint-appointment":        "bureau-fingerprint-appointment",
	}

	for path, slug := range tests {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("request to %s failed: %v", path, err)
		}
		body := readBody(t, resp)
		resp.Body.Close()

		if !strings.Contains(body, `x-data="service(`) {
			t.Errorf("%s renders no booking component", path)
		}
		if !strings.Contains(body, slug) {
			t.Errorf("%s does not bind schedule %q", path, slug)
		}
	}
}
```

Delete the old `TestAurenorPagesCarryBookers`.

- [ ] **Step 10: Run the full suite**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs); go test ./... -count=1`
Expected: all packages ok

- [ ] **Step 11: Check by hand**

Load `http://localhost:4000/aurenor/hospitals/solvarre-general`, click Cardiology, book a time in one browser window while a second has the same screen open. The second window's times must update without a refresh.

- [ ] **Step 12: Commit**

```bash
git add internal/web/pages/ internal/server/routes/ test/
git commit -m "feat(web): a screen per bookable service, live"
```

---

### Task 10: Documentation and roadmap

**Files:**
- Modify: `docs/worldbuilding/aurenor.md`
- Modify: `docs/superpowers/specs/2026-08-04-booking-ux-and-discord-removal-plan.md`

- [ ] **Step 1: Mark the roadmap**

In `docs/worldbuilding/aurenor.md`, mark step 7 (Seat selection UX) and step 8 (Appointment UX) done, and note that both surfaces are live-updating.

- [ ] **Step 2: Cross-reference the design**

In the 08-04 plan, add a line under Step C and Step D pointing at
`2026-08-06-live-boards-and-screens-design.md` as what was actually built.

- [ ] **Step 3: Commit**

```bash
git add docs/
git commit -m "docs: record the live boards work"
```

---

## Self-Review

**Spec coverage:** date strip → Tasks 7, 8, 9. Full-width board → Task 8. Summary endpoint and stream → Tasks 4, 5. Availability caching and stream → Task 6. Rule/exception triggers → Task 2. `SubscribeAll` → Task 3. Generic cache → Task 1. Per-service screens and list-only institution pages → Task 9. Polling fallback → Tasks 8, 9. Slug cap → Task 4.

**Known follow-ups, deliberately not in this plan:** the games and auth route registration still lives outside `internal/server/routes`; appointment streams send the whole window rather than a delta.
