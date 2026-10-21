# A2 Live Layer Resilience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Status (2026-09-02): executed in full.** Every task below is built and the whole suite passes under `-race`. Deviations from the plan as written: Task 4 also had to move the summary stream's first read *after* `hub.SubscribeAll`/`Ready` — a snapshot taken while the hub is idle is stamped with the clock stand-in, and the first change afterwards reloaded every showing on the board (the test measured 15 lookups for one hold before that fix, 3 after). The per-change lookup test records the statements it counts and prints them on failure.

**Goal:** A dropped LISTEN connection resyncs every open stream; a failed load never replaces a good snapshot; the GA showing, the empty board and any screen after a network blip keep receiving updates; a reload keeps the buyer's own hold; the appointment strip and stream agree on the window.

**Architecture:** Server side, three seams change shape: the notify triggers announce inserts and capacity edits; the hub republishes every subscribed topic when its listener reconnects; the snapshot cache's loader takes a context and returns an error, and the cache keeps its last good snapshot when a load fails. The summary stream resolves slugs once per connection. Availability's polled path serves the same shared snapshot as the stream, so the two cannot disagree. Client side, the three Alpine components open their stream unconditionally, fall back to polling only when EventSource reports `CLOSED`, and the seat screen restores the buyer's hold from storage on init.

**Tech Stack:** Go 1.26, GORM + pgx/v5, PostgreSQL 16 LISTEN/NOTIFY, templ + Alpine.js 3 (scripts are inline in `.templ`; run `go generate ./...` after editing them), Go HTTP tests over `httptest.Server`.

**Spec:** `docs/superpowers/specs/2026-09-02-evaluation-and-roadmap-v2.md` §2.1 K16; §2.2 S2, S3, S4, S5, S6, S8, S17, S18; §2.3 W1–W6; §3 "A2" definition of done. Retagged by this plan (Task 9): K17 stays as documented behaviour, S21 moves to A3.

## Global Constraints

- **Do not commit.** The owner signs commits with GPG. "Checkpoint" means `git status` shows only the listed files.
- **Run tests with the database:** `export $(grep -E '^DATABASE_URL=' .env | xargs)` first. Postgres is `docker start clair-postgres-1` if down.
- **`PrepareStmt: true`** in production GORM: one SQL statement per `Exec` string. The notify DDL is a list of single statements for this reason.
- **One screen, one connection.** No page may open more than one EventSource. The GA showing gets its live count from the summary stream, not a second stream.
- **Cost per change, not per viewer.** Nothing added may run a query per subscriber on every notification.
- **After editing a `.templ` file:** `go generate ./...` before building or testing; the generated `*_templ.go` is what compiles.
- **Never "kernel".** The package is `booking`; prose says "the booking engine".
- Test helpers already exist: `testsupport.PostgresDB(t, database.MigrationModels()...)`, `newTheaterServer(t)`, `getEvent`, `getSeats`, `firstSeated`, `doJSON`, `mustStatus`, `newJarClient`, `itoa` in `test/aurenor_theater_test.go`; the availability-stream reader pattern is in `test/aurenor_appointments_test.go:267-340`.

---

### Task 1: Triggers announce inserts and capacity edits (K16)

**Files:**
- Modify: `internal/booking/notify.go`
- Test: `internal/booking/notify_test.go`

**Interfaces:**
- `InstallNotifyTriggers(db)` unchanged; it now installs one more trigger, `booking_inventory_notify_insert` (AFTER INSERT, no WHEN), and the UPDATE trigger's WHEN clause also fires on `capacity`.

- [x] **Step 1: Write the failing tests**

Append to `internal/booking/notify_test.go`:

```go
// listenFor opens a raw LISTEN connection on the notify channel and returns a
// function that waits, up to 10s, for a payload equal to want.
func listenFor(t *testing.T) (waitFor func(want string)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("listener connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, "LISTEN "+booking.NotifyChannel); err != nil {
		t.Fatalf("listen: %v", err)
	}
	return func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if time.Now().After(deadline) {
				t.Fatalf("no notification %q", want)
			}
			waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
			note, err := conn.WaitForNotification(waitCtx)
			waitCancel()
			if err != nil {
				t.Fatalf("wait for notification: %v", err)
			}
			if note.Payload == want {
				return
			}
		}
	}
}

// TestCapacityChangeNotifies: a capacity edit changes what is open without any
// unit moving; a screen showing that inventory has to hear about it.
func TestCapacityChangeNotifies(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install triggers: %v", err)
	}
	waitFor := listenFor(t)

	tier := models.BookingTicketTier{Name: "GA", Kind: "ga", PriceCents: 1000, EventID: 4343}
	if err := db.Create(&tier).Error; err != nil {
		t.Fatal(err)
	}
	inv := models.BookingInventory{Capacity: 10, TierID: &tier.ID}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	if err := booking.SetCapacity(db, inv.ID, 12); err != nil {
		t.Fatalf("set capacity: %v", err)
	}
	waitFor("event:4343")
}

// TestInventoryInsertNotifies: the first booking of a slot materialises its
// inventory row. That insert is the change the screen is waiting for.
func TestInventoryInsertNotifies(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install triggers: %v", err)
	}
	waitFor := listenFor(t)

	slot := models.BookingSlot{ScheduleID: 7171, StartAt: time.Now().Add(time.Hour), EndAt: time.Now().Add(2 * time.Hour), Capacity: 1}
	if err := db.Create(&slot).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := booking.EnsureInventory(db, booking.SlotOwner(slot.ID), 1); err != nil {
		t.Fatalf("ensure inventory: %v", err)
	}
	waitFor("schedule:7171")
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/booking/ -run 'TestCapacityChangeNotifies|TestInventoryInsertNotifies' -count=1 -v`
Expected: both FAIL with `no notification "event:4343"` / `no notification "schedule:7171"`.

- [x] **Step 3: Implement**

In `notify.go`, change `createNotifyTriggerDDL`'s WHEN clause and add an insert trigger:

```go
const createNotifyTriggerDDL = `
CREATE TRIGGER booking_inventory_notify
AFTER UPDATE ON booking_inventories
FOR EACH ROW
WHEN (
	OLD.booked   IS DISTINCT FROM NEW.booked   OR
	OLD.held     IS DISTINCT FROM NEW.held     OR
	OLD.blocked  IS DISTINCT FROM NEW.blocked  OR
	OLD.capacity IS DISTINCT FROM NEW.capacity
)
EXECUTE FUNCTION booking_notify_inventory()
`

// A slot's inventory row is created the first time somebody books it, and that
// insert is itself the change a screen showing the schedule is waiting for.
const dropNotifyInsertTriggerDDL = `
DROP TRIGGER IF EXISTS booking_inventory_notify_insert ON booking_inventories
`

const createNotifyInsertTriggerDDL = `
CREATE TRIGGER booking_inventory_notify_insert
AFTER INSERT ON booking_inventories
FOR EACH ROW
EXECUTE FUNCTION booking_notify_inventory()
`
```

Add `dropNotifyInsertTriggerDDL, createNotifyInsertTriggerDDL` to the statement list in `InstallNotifyTriggers`, right after `createNotifyTriggerDDL`. Update the doc comment above the DDL: "The WHEN clause keeps it quiet: only a change to what is booked, held, blocked or offered (capacity) is worth telling anyone about, plus the insert that brings a row into being."

- [x] **Step 4: Run the tests**

Run: `go test ./internal/booking/ ./internal/booking/live/ -count=1`
Expected: PASS.

- [x] **Step 5: Checkpoint** — `internal/booking/notify.go`, `internal/booking/notify_test.go`.

---

### Task 2: The hub republishes on reconnect (S2, K17 documented)

**Files:**
- Modify: `internal/booking/live/hub.go`
- Test: `internal/booking/live/hub_test.go`

**Interfaces:**
- No signature changes. Behaviour: when `listenOnce` succeeds after a previous connection was lost, every topic with a subscriber gets one `publish`, so readers re-read.

- [x] **Step 1: Write the failing test**

Append to `internal/booking/live/hub_test.go`:

```go
// killListener terminates the hub's LISTEN backend from the server side, the
// way a restarted database or a dropped network path would.
func killListener(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer conn.Close(ctx)
	tag, err := conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE query = 'LISTEN `+booking.NotifyChannel+`' AND pid <> pg_backend_pid()`)
	if err != nil {
		t.Fatalf("terminate listener: %v", err)
	}
	if tag.RowsAffected() == 0 {
		t.Fatal("no LISTEN backend found to terminate")
	}
}

// TestHubResyncsAfterReconnect: a change made while the listener was down is
// lost on the wire, so when the listener comes back every subscriber must be
// told to re-read, or it serves pre-outage state forever while looking healthy.
func TestHubResyncsAfterReconnect(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set")
	}
	hub := live.NewHub(os.Getenv("DATABASE_URL"), zap.NewNop())
	signals, release := hub.Subscribe("event:31")
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if !hub.Ready(ctx, 5*time.Second) {
		t.Fatal("listener never connected")
	}
	before, _ := hub.Version("event:31")

	killListener(t)

	// It comes back on its own...
	if !hub.Ready(ctx, 10*time.Second) {
		t.Fatal("listener never reconnected")
	}
	// ...and the reconnect itself is a signal.
	select {
	case <-signals:
	case <-time.After(5 * time.Second):
		t.Fatal("no resync signal after the listener reconnected")
	}
	if after, _ := hub.Version("event:31"); after <= before {
		t.Fatalf("version did not advance on reconnect: before=%d after=%d", before, after)
	}
}
```

Add `"github.com/jackc/pgx/v5"` to the test imports.

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/booking/live/ -run TestHubResyncsAfterReconnect -count=1 -v`
Expected: FAIL — `no resync signal after the listener reconnected`.

- [x] **Step 3: Implement**

In `hub.go`, add a field and the republish:

```go
type Hub struct {
	dsn    string
	logger *zap.Logger

	mu       sync.Mutex
	subs     map[string]map[chan struct{}]struct{}
	// versions counts changes per topic for the life of the process. It is
	// never pruned on purpose: the snapshot cache compares stamps against it,
	// and a topic whose counter restarted at zero could hand a reader a stale
	// entry whose old stamp happens to match. One integer per topic ever seen
	// is the price, and that is bounded by the catalog, not by traffic.
	versions map[string]uint64
	cancel   context.CancelFunc
	running  bool

	connected atomic.Bool
	// everConnected distinguishes the first connection from a reconnect. Only
	// a reconnect can have missed anything.
	everConnected atomic.Bool
}
```

Replace the body of `listenOnce` after the LISTEN succeeds:

```go
	if _, err := conn.Exec(ctx, "LISTEN "+booking.NotifyChannel); err != nil {
		return err
	}
	h.connected.Store(true)
	if h.everConnected.Swap(true) {
		// Anything announced while we were away is gone; tell every subscriber
		// to re-read rather than let them serve the last thing they saw.
		h.logger.Info("seat listener reconnected; resyncing subscribers")
		h.republishAll()
	} else {
		h.logger.Info("seat listener connected")
	}
```

and add:

```go
// republishAll wakes every subscribed topic once. Used after a reconnect: the
// listener cannot know what it missed, so every reader re-reads.
func (h *Hub) republishAll() {
	h.mu.Lock()
	topics := make([]string, 0, len(h.subs))
	for topic := range h.subs {
		topics = append(topics, topic)
	}
	h.mu.Unlock()
	for _, topic := range topics {
		h.publish(topic)
	}
}
```

Also reset `everConnected` when the last subscriber leaves (in the release closure, next to `h.connected.Store(false)`): `h.everConnected.Store(false)` — a fresh first subscriber is a first connection, not a reconnect.

- [x] **Step 4: Run the tests**

Run: `go test ./internal/booking/live/ -count=1`
Expected: PASS (all six).

- [x] **Step 5: Checkpoint** — `internal/booking/live/hub.go`, `internal/booking/live/hub_test.go`.

---

### Task 3: A failed or slow load never replaces a good snapshot (S3, S8)

**Files:**
- Modify: `internal/server/booking/api_snapshot.go`
- Modify: `internal/server/booking/api.go` (the three loaders)
- Modify: `internal/server/booking/api_events.go` (`seatViews` returns an error; `seatMap` handles it)
- Modify: `internal/server/booking/api_summary.go` (`summaryFor` returns an error)
- Modify: `internal/server/booking/api_availability.go` (`availabilityViews` returns an error)
- Modify: `internal/server/booking/api_stream.go`, `api_availability_stream.go` (skip a frame on error)
- Test: `internal/server/booking/api_snapshot_test.go`

**Interfaces:**
- `newSnapshotCache[T](load func(ctx context.Context, key string) (T, error))`
- `(c *snapshotCache[T]) Snapshot(key string, version uint64) (snapshot, error)` — on a load error, returns the previous snapshot with a nil error if one exists (and does not advance the stamp, so the next call retries); returns a zero snapshot and the error when nothing has ever loaded.
- `const snapshotLoadTimeout = 5 * time.Second` — the context handed to the loader.
- `seatViews(conn, eventID, section) ([]seatView, error)`; `summaryFor(conn, eventID) ([]eventSummary, error)`; `availabilityViews(conn, sched, now) ([]appt.OpenSlot, error)`.

- [x] **Step 1: Write the failing tests**

Append to `internal/server/booking/api_snapshot_test.go`:

```go
// TestSnapshotCacheKeepsLastGoodOnFailure: a transient database error must not
// become a cached, ETagged, empty seat map that every reader is then served
// until the next notification happens to arrive.
func TestSnapshotCacheKeepsLastGoodOnFailure(t *testing.T) {
	fail := false
	var loads int64
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		atomic.AddInt64(&loads, 1)
		if fail {
			return nil, errors.New("connection reset")
		}
		return []seatView{{ID: 1, Label: "A-1", State: "free"}}, nil
	})

	good, err := cache.Snapshot("event:7", 1)
	if err != nil {
		t.Fatal(err)
	}

	fail = true
	got, err := cache.Snapshot("event:7", 2)
	if err != nil {
		t.Fatalf("a failed refresh with a good snapshot in hand should not error, got %v", err)
	}
	if string(got.JSON) != string(good.JSON) || got.ETag != good.ETag {
		t.Fatal("a failed load replaced the last good snapshot")
	}

	// The failure was not cached: the next read at the same version tries again.
	fail = false
	if _, err := cache.Snapshot("event:7", 2); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&loads); got != 3 {
		t.Fatalf("want 3 loads (good, failed, retried), got %d", got)
	}
}

// TestSnapshotCacheReportsFirstLoadFailure: with nothing good to fall back on,
// the caller has to be told, so it can answer 503 instead of "[]".
func TestSnapshotCacheReportsFirstLoadFailure(t *testing.T) {
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		return nil, errors.New("connection reset")
	})
	if _, err := cache.Snapshot("event:7", 1); err == nil {
		t.Fatal("first load failed but Snapshot returned no error")
	}
}

// TestSnapshotCacheLoadHasADeadline: one stuck query must not pin every reader
// of that key forever.
func TestSnapshotCacheLoadHasADeadline(t *testing.T) {
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	start := time.Now()
	_, err := cache.Snapshot("event:7", 1)
	if err == nil {
		t.Fatal("a load that never returns should surface as an error")
	}
	if elapsed := time.Since(start); elapsed > snapshotLoadTimeout+2*time.Second {
		t.Fatalf("load blocked for %s, want about %s", elapsed, snapshotLoadTimeout)
	}
}
```

Add `"context"`, `"errors"`, `"time"` to the imports.

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/server/booking/ -run 'TestSnapshotCache' -count=1`
Expected: build FAIL — the loader signature does not match, `snapshotLoadTimeout` undefined.

- [x] **Step 3: Implement**

`api_snapshot.go`:

```go
package booking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// snapshotLoadTimeout bounds one shared load. The entry's lock is held for the
// duration, so a load that hung would hold every reader of that key with it.
const snapshotLoadTimeout = 5 * time.Second

type snapshot struct {
	JSON []byte
	ETag string
}

// snapshotCache ... (keep the existing doc comment) ...
//
// A load that fails does not replace what the cache holds. Readers get the last
// good snapshot and the stamp stays behind, so the next reader tries again; a
// transient error costs one stale frame, not a cached empty map.
type snapshotCache[T any] struct {
	load func(ctx context.Context, key string) (T, error)

	mu      sync.Mutex
	entries map[string]*snapshotCacheEntry
}

func newSnapshotCache[T any](load func(ctx context.Context, key string) (T, error)) *snapshotCache[T] {
	return &snapshotCache[T]{load: load, entries: map[string]*snapshotCacheEntry{}}
}

// Snapshot returns the rendered state for a key at the given version, reading
// the database only when what it holds is older than that. On a failed read it
// returns the last good snapshot if there is one, and the error otherwise.
func (c *snapshotCache[T]) Snapshot(key string, version uint64) (snapshot, error) {
	entry := c.entryFor(key)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	stamp := version + entry.local
	if entry.loaded && entry.stamp == stamp {
		return entry.snap, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), snapshotLoadTimeout)
	defer cancel()
	value, err := c.load(ctx, key)
	if err != nil {
		if entry.loaded {
			return entry.snap, nil
		}
		return snapshot{}, err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		if entry.loaded {
			return entry.snap, nil
		}
		return snapshot{}, err
	}
	sum := sha256.Sum256(payload)

	entry.snap = snapshot{JSON: payload, ETag: `W/"` + hex.EncodeToString(sum[:12]) + `"`}
	entry.stamp = stamp
	entry.loaded = true
	return entry.snap, nil
}
```

Keep `Bump`, `entryFor`, the key helpers.

`api_events.go` — `seatViews` returns `([]seatView, error)`: `if err := conn.Raw(query, args...).Scan(&rows).Error; err != nil { return nil, err }`. `seatMap`'s section branch: `views, err := seatViews(db(ctx, r), event.ID, section); if err != nil { ctx.Logger.Error(...); http.Error(w, "seat map unavailable", http.StatusServiceUnavailable); return }`. The cached branch: `snap, err := cache.Snapshot(...); if err != nil { same 503 }`.

`api_summary.go` — `summaryFor` returns `([]eventSummary, error)`; check `.Error` on both `Find`s; return `nil, nil` when the event or tier is absent. In `requestedSummaries`, `snap, err := cache.Snapshot(key, ...); if err != nil { continue }`.

`api_availability.go` — `availabilityViews(conn, sched, now) ([]appt.OpenSlot, error)` returns the `OpenSlotsFor` error instead of `nil`.

`api.go` — the three loaders:

```go
	seats := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		return seatViews(bctx.DBConn.WithContext(ctx), eventKeyID(key), "")
	})
	summary := newSnapshotCache(func(ctx context.Context, key string) ([]eventSummary, error) {
		return summaryFor(bctx.DBConn.WithContext(ctx), eventKeyID(key))
	})
	availability := newSnapshotCache(func(ctx context.Context, key string) ([]appt.OpenSlot, error) {
		conn := bctx.DBConn.WithContext(ctx)
		var sched models.BookingSchedule
		res := conn.Limit(1).Find(&sched, scheduleKeyID(key))
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, nil
		}
		return availabilityViews(conn, sched, time.Now())
	})
```

(`NewAPI`'s parameter is named `ctx` today; rename it `bctx` in this function so the loader's `context.Context` can be `ctx`. Add `"context"` to the imports.)

`api_stream.go` and `api_availability_stream.go` — the `send` closure: `snap, err := cache.Snapshot(...); if err != nil { return true }` (skip the frame, keep the stream) — except for the first frame, where the handler should answer `503 "live updates unavailable"` before headers are written. Restructure: compute the first snapshot before `w.WriteHeader`; on error return 503; then write headers and send it.

- [x] **Step 4: Run the tests**

Run: `go build ./... && go test ./internal/server/booking/ -count=1 && go test ./test/ -run 'Theater|Availability|Summary|Aurenor' -count=1`
Expected: PASS.

- [x] **Step 5: Checkpoint** — the seven files above.

---

### Task 4: Per-change cost on the board stream; section and event reads off the cache (S5, S6, S17)

**Files:**
- Modify: `internal/server/booking/api_summary.go`
- Modify: `internal/server/booking/api_events.go` (`seatMap` section branch; `getEvent`; new `tierRemainingAll`)
- Test: `test/aurenor_theater_test.go`

**Interfaces:**
- `resolveEventIDs(conn *gorm.DB, slugs []string) []uint` — ids in request order, unknown slugs skipped.
- `summariesFor(hub *live.Hub, cache *snapshotCache[[]eventSummary], ids []uint) []eventSummary` — no database access of its own; reads the cache per id.
- `tierRemainingAll(conn *gorm.DB, eventID uint) (map[uint]int, error)` — one query for every tier of an event.

- [x] **Step 1: Write the failing tests**

Append to `test/aurenor_theater_test.go`:

```go
// countingLogger counts SQL statements that touch a table, so a test can say
// "this change cost one lookup, not one per showing".
type countingLogger struct {
	gormlogger.Interface
	table string
	n     int64
}

func (l *countingLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	if strings.Contains(sql, l.table) {
		atomic.AddInt64(&l.n, 1)
	}
	l.Interface.Trace(ctx, begin, fc, err)
}

// TestEventSummaryStreamCostsOneLookupPerChange: the board stream used to
// re-resolve every slug on every change. With N viewers of a 13-showing board
// that is 13N event lookups per hold.
func TestEventSummaryStreamCostsOneLookupPerChange(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatal(err)
	}
	if _, err := aurenor.Seed(db, time.Now(), "seed@aurenor.test"); err != nil {
		t.Fatal(err)
	}
	counter := &countingLogger{Interface: db.Logger, table: `"booking_events"`}
	db.Logger = counter

	ts := httptest.NewServer(server.NewBackEnd(context.Background(), zap.NewNop(), nil, db).Routes())
	t.Cleanup(ts.Close)

	slugs := aurenor.TheaterEventSlugs("grand")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/events/summary/stream?slugs="+strings.Join(slugs, ","), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	frames := make(chan string, 8)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(frames)
				return
			}
			if payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: "); ok {
				frames <- payload
			}
		}
	}()
	select {
	case <-frames:
	case <-time.After(10 * time.Second):
		t.Fatal("no opening frame")
	}

	atomic.StoreInt64(&counter.n, 0)
	slug, _ := firstSeated(t, ts)
	seat := getSeats(t, ts, slug)[0].ID
	hold := doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+slug+"/seats/hold",
		map[string]any{"seat_ids": []uint{seat}, "guest_name": "Counter"})
	mustStatus(t, hold, http.StatusCreated, "hold")
	hold.Body.Close()

	select {
	case <-frames:
	case <-time.After(10 * time.Second):
		t.Fatal("no frame after the hold")
	}
	// The hold's own handler resolves its slug once; the changed showing's
	// summary reloads once. Everything else on the board must come from cache.
	if got := atomic.LoadInt64(&counter.n); got > 3 {
		t.Fatalf("one change cost %d booking_events lookups; want at most 3", got)
	}
}

// TestSeatMapSectionFiltersTheSharedSnapshot: narrowing to a section is a
// filter over the map everyone shares, not a separate query per request.
func TestSeatMapSectionFiltersTheSharedSnapshot(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	all := getSeats(t, ts, slug)
	section := all[0].Section

	resp := doJSON(t, newJarClient(), "GET", ts.URL+"/events/"+slug+"/seats?section="+url.QueryEscape(section), nil)
	mustStatus(t, resp, http.StatusOK, "seats by section")
	var got []theaterSeat
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(got) == 0 {
		t.Fatal("no seats in the section")
	}
	for _, s := range got {
		if s.Section != section {
			t.Fatalf("seat %s is in section %q, asked for %q", s.Label, s.Section, section)
		}
	}
}
```

Add imports as needed: `"sync/atomic"`, `"net/url"`, `gormlogger "gorm.io/gorm/logger"`, `"github.com/erancihan/clair/internal/testsupport"`, `"github.com/erancihan/clair/internal/database"`, `"github.com/erancihan/clair/internal/server"`, `"go.uber.org/zap"` (check which are already present; `test/aurenor_appointments_test.go` imports most of them and is the model). `theaterSeat` must carry `Section string \`json:"section"\`` — add the field if it is missing.

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./test/ -run 'TestEventSummaryStreamCostsOneLookupPerChange|TestSeatMapSectionFiltersTheSharedSnapshot' -count=1 -v`
Expected: the first FAILS with a count well above 3 (about the number of grand-theater slugs plus two). The second may pass already — it pins behaviour Step 3 must preserve.

- [x] **Step 3: Implement**

`api_summary.go` — split resolution from reading:

```go
// resolveEventIDs turns a caller's slug list into event ids, in the order
// asked, skipping any that name nothing. Called once per request or stream:
// the ids do not change while a connection is open, so nothing re-resolves
// them per change.
func resolveEventIDs(conn *gorm.DB, slugs []string) []uint {
	ids := make([]uint, 0, len(slugs))
	for _, slug := range slugs {
		var event models.BookingEvent
		if res := conn.Where("slug = ?", strings.TrimSpace(slug)).Limit(1).Find(&event); res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		ids = append(ids, event.ID)
	}
	return ids
}

// summariesFor reads the shared snapshot of each showing. It touches no table
// itself; a change to one showing costs one reload of that showing's summary.
func summariesFor(hub *live.Hub, cache *snapshotCache[[]eventSummary], ids []uint) []eventSummary {
	rows := make([]eventSummary, 0, len(ids))
	for _, id := range ids {
		snap, err := cache.Snapshot(eventKey(id), seatVersion(hub, id))
		if err != nil {
			continue
		}
		var summaries []eventSummary
		if err := json.Unmarshal(snap.JSON, &summaries); err == nil {
			rows = append(rows, summaries...)
		}
	}
	return rows
}

// requestedSlugs parses and bounds the slugs parameter.
func requestedSlugs(r *http.Request) ([]string, bool) {
	raw := r.URL.Query().Get("slugs")
	if raw == "" {
		return nil, false
	}
	slugs := strings.Split(raw, ",")
	if len(slugs) > maxSummarySlugs {
		return nil, false
	}
	return slugs, true
}

func topicsFor(ids []uint) []string {
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = eventKey(id)
	}
	return keys
}
```

`eventSummaries`: `slugs, ok := requestedSlugs(r)`; 400 if not ok; `ids := resolveEventIDs(db(ctx, r), slugs)`; `writeJSON(w, 200, summariesFor(hub, cache, ids))`.

`eventSummaryStream`: same parse and resolve once; `signals, release := hub.SubscribeAll(topicsFor(ids))`; the first frame and every later frame call `summariesFor(hub, cache, ids)`. Delete `requestedSummaries`.

`api_events.go` — `seatMap` section branch reads the cached snapshot and filters:

```go
		snap, err := cache.Snapshot(eventKey(event.ID), seatVersion(hub, event.ID))
		if err != nil {
			ctx.Logger.Error("seat map", zap.Error(err))
			http.Error(w, "seat map unavailable", http.StatusServiceUnavailable)
			return
		}

		// A narrowed map is the shared map with rows dropped, so it costs no
		// query of its own. It is rare enough not to deserve an ETag.
		if section := r.URL.Query().Get("section"); section != "" {
			var all []seatView
			_ = json.Unmarshal(snap.JSON, &all)
			narrowed := make([]seatView, 0, len(all))
			for _, s := range all {
				if s.Section == section {
					narrowed = append(narrowed, s)
				}
			}
			writeJSON(w, http.StatusOK, narrowed)
			return
		}
```

(Then the ETag/304 branch as today.) `seatViews`'s `section` parameter is now unused by handlers; keep the function signature for the stream loader but pass `""`, or drop the parameter — drop it, and remove the `if section != ""` block.

`getEvent` — replace the per-tier loop:

```go
		remaining, err := tierRemainingAll(conn, event.ID)
		if err != nil {
			ctx.Logger.Error("tier remaining", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		views := make([]tierView, 0, len(tiers))
		for _, t := range tiers {
			views = append(views, tierView{BookingTicketTier: t, Remaining: remaining[t.ID]})
		}
```

and add, next to `tierRemaining`:

```go
// tierRemainingAll answers "what is left" for every tier of an event in one
// query. GA tiers subtract from their pooled row; seated tiers count seats
// still free. The two branches of the UNION are the two branches of
// tierRemaining, which stays for the single-tier callers.
func tierRemainingAll(conn *gorm.DB, eventID uint) (map[uint]int, error) {
	type row struct {
		TierID    uint
		Remaining int
	}
	var rows []row
	err := conn.Raw(`
		SELECT t.id AS tier_id,
		       GREATEST(i.capacity - i.booked - i.held - i.blocked, 0) AS remaining
		FROM booking_ticket_tiers t
		JOIN booking_inventories i ON i.tier_id = t.id
		WHERE t.event_id = ? AND t.kind = 'ga'
		UNION ALL
		SELECT t.id AS tier_id, count(s.id) AS remaining
		FROM booking_ticket_tiers t
		LEFT JOIN booking_seats s ON s.tier_id = t.id AND s.kind = 'sellable'
		LEFT JOIN booking_inventories i ON i.seat_id = s.id
		    AND i.booked + i.held + i.blocked < i.capacity
		WHERE t.event_id = ? AND t.kind = 'seated'
		GROUP BY t.id`, eventID, eventID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uint]int, len(rows))
	for _, r := range rows {
		out[r.TierID] = r.Remaining
	}
	return out, nil
}
```

Note the seated branch counts `i.id IS NOT NULL` implicitly: `count(s.id)` would count seats regardless of the inventory join. Use `count(i.id)` so only seats whose inventory has room are counted.

- [x] **Step 4: Run the tests**

Run: `go build ./... && go test ./test/ -run 'Theater|Summary|Seed' -count=1`
Expected: PASS, including `TestSeededEventsHaveFullInventory` (remaining == capacity through the new query).

- [x] **Step 5: Checkpoint** — `api_summary.go`, `api_events.go`, `test/aurenor_theater_test.go`.

---

### Task 5: Availability: bounded `from`, cached polled path, one window (S4, S18, W4)

**Files:**
- Modify: `internal/server/booking/api_availability.go`
- Modify: `internal/server/booking/api.go` (`Availability()` passes hub and cache)
- Modify: `internal/web/pages/aurenor_service.templ` (the empty-state copy)
- Test: `test/aurenor_appointments_test.go`

**Interfaces:**
- `availability(ctx, hub, cache) http.HandlerFunc`. With no `from`/`to` it serves the shared snapshot with `ETag` and honours `If-None-Match`. With either parameter it computes directly, with `from` floored at `now` and `to` capped at the schedule window.
- `availabilityWindowDays` is deleted. The shared snapshot covers `now .. now + sched.WindowDays`: the schedule's window is the one truth, and the snapshot is computed once per change, so its cost is not per reader.

- [x] **Step 1: Write the failing tests**

Append to `test/aurenor_appointments_test.go`:

```go
// availabilityServer seeds Aurenor and returns a server plus the DB.
func availabilityServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatal(err)
	}
	if _, err := aurenor.Seed(db, time.Now(), "seed@aurenor.test"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.NewBackEnd(context.Background(), zap.NewNop(), nil, db).Routes())
	t.Cleanup(ts.Close)
	return ts
}

type availabilitySlot struct {
	StartAt time.Time `json:"start_at"`
}

func getAvailability(t *testing.T, ts *httptest.Server, path string) (*http.Response, []availabilitySlot) {
	t.Helper()
	resp := doJSON(t, newJarClient(), "GET", ts.URL+path, nil)
	var slots []availabilitySlot
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&slots)
	}
	resp.Body.Close()
	return resp, slots
}

// TestAvailabilityFromIsFlooredAtNow: `from` is caller-supplied and the
// generator walks day by day from it. Eight centuries of days is not a request.
func TestAvailabilityFromIsFlooredAtNow(t *testing.T) {
	ts := availabilityServer(t)
	start := time.Now()
	resp, slots := getAvailability(t, ts, "/appointments/schedules/consulate-tourist-visa-interview/availability?from=1200-01-01T00:00:00Z")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("an ancient from took %s", time.Since(start))
	}
	if len(slots) > 0 && slots[0].StartAt.Before(start) {
		t.Fatalf("first slot %s is before now", slots[0].StartAt)
	}
}

// TestAvailabilityDefaultIsCachedWithETag: the polled path and the stream share
// one snapshot, so a poller that saw it already gets a 304.
func TestAvailabilityDefaultIsCachedWithETag(t *testing.T) {
	ts := availabilityServer(t)
	const path = "/appointments/schedules/solvarre-general-cardiology/availability"

	first := doJSON(t, newJarClient(), "GET", ts.URL+path, nil)
	mustStatus(t, first, http.StatusOK, "availability")
	etag := first.Header.Get("ETag")
	first.Body.Close()
	if etag == "" {
		t.Fatal("default availability carries no ETag")
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Header.Set("If-None-Match", etag)
	again, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	again.Body.Close()
	if again.StatusCode != http.StatusNotModified {
		t.Fatalf("same ETag answered %d, want 304", again.StatusCode)
	}
}

// TestAvailabilityGETAndStreamAgreeOnTheWindow: the page loads with one and
// listens with the other; if their horizons differ the date strip truncates
// itself moments after it is drawn.
func TestAvailabilityGETAndStreamAgreeOnTheWindow(t *testing.T) {
	ts := availabilityServer(t)
	const slug = "consulate-tourist-visa-interview" // the longest seeded window
	_, polled := getAvailability(t, ts, "/appointments/schedules/"+slug+"/availability")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/appointments/schedules/"+slug+"/availability/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	var streamed []availabilitySlot
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal("stream closed before its first frame")
		}
		if payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: "); ok {
			if err := json.Unmarshal([]byte(payload), &streamed); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if len(polled) != len(streamed) {
		t.Fatalf("GET returned %d slots, stream's first frame %d", len(polled), len(streamed))
	}
	if len(polled) > 0 && !polled[len(polled)-1].StartAt.Equal(streamed[len(streamed)-1].StartAt) {
		t.Fatalf("last slot differs: GET %s, stream %s", polled[len(polled)-1].StartAt, streamed[len(streamed)-1].StartAt)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./test/ -run 'TestAvailabilityFromIsFlooredAtNow|TestAvailabilityDefaultIsCachedWithETag|TestAvailabilityGETAndStreamAgreeOnTheWindow' -count=1 -v`
Expected: the ETag test FAILS (no ETag); the window test FAILS (GET has ~45 days of slots, the stream 14); the `from` test likely FAILS on time.

- [x] **Step 3: Implement**

`api_availability.go`:

```go
// availability returns the open slots of a schedule.
//
// With no range asked for, it serves the snapshot every reader of the schedule
// shares — the same bytes the stream's first frame carries — with an ETag, so a
// poller that already has them gets a 304. A caller-supplied range is computed
// directly, floored at now and capped at the schedule's own window.
func availability(ctx server_context.BackEndContext, hub *live.Hub, cache *snapshotCache[[]appt.OpenSlot]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadSchedule(ctx, r, r.PathValue("slug"))
		if !ok || !sched.Active {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}

		q := r.URL.Query()
		if q.Get("from") == "" && q.Get("to") == "" {
			snap, err := cache.Snapshot(scheduleKey(sched.ID), scheduleVersion(hub, sched.ID))
			if err != nil {
				ctx.Logger.Error("availability snapshot", zap.Error(err))
				http.Error(w, "could not compute availability", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("ETag", snap.ETag)
			w.Header().Set("Cache-Control", "no-cache")
			if r.Header.Get("If-None-Match") == snap.ETag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(snap.JSON)
			return
		}

		now := time.Now()

		// `from` is caller-supplied and the generator walks day by day from it;
		// nothing before now is bookable, so nothing before now is computed.
		from := now
		if ts, err := time.Parse(time.RFC3339, q.Get("from")); err == nil && ts.After(now) {
			from = ts
		}

		// The schedule's own booking window is the ceiling. A caller asking for a
		// range beyond it is clamped rather than refused, but it can never widen
		// how far ahead the schedule is bookable.
		to := now.AddDate(0, 0, sched.WindowDays)
		if ts, err := time.Parse(time.RFC3339, q.Get("to")); err == nil && ts.Before(to) {
			to = ts
		}

		slots, err := appt.OpenSlotsFor(db(ctx, r), &sched, from, to, now)
		if err != nil {
			ctx.Logger.Error("compute availability", zap.Error(err))
			http.Error(w, "could not compute availability", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, slots)
	}
}

// availabilityViews computes a schedule's open slots from now to the end of its
// window. Availability is derived from rules rather than stored, which is why
// it is produced once per change and shared, rather than repeated per reader —
// and why the window is the schedule's whole window: the cost is per change,
// and a screen must never be shown less than the schedule offers.
func availabilityViews(conn *gorm.DB, sched models.BookingSchedule, now time.Time) ([]appt.OpenSlot, error) {
	return appt.OpenSlotsFor(conn, &sched, now, now.AddDate(0, 0, sched.WindowDays), now)
}
```

Delete `availabilityWindowDays`. Add the `live` import. In `api.go`: `func (a *API) Availability() http.HandlerFunc { return availability(a.ctx, a.hub, a.availability) }`.

`internal/web/pages/aurenor_service.templ` line 43: change "No times are open in the next two weeks" to "No times are open right now." Then `go generate ./...`.

- [x] **Step 4: Run the tests**

Run: `go generate ./... && go build ./... && go test ./test/ -run 'Availability|Appointments|Aurenor|Service' -count=1`
Expected: PASS.

- [x] **Step 5: Checkpoint** — `api_availability.go`, `api.go`, `aurenor_service.templ` (+ its `_templ.go`), `test/aurenor_appointments_test.go`.

---

### Task 6: Every screen keeps its stream (W1, W2, W3)

**Files:**
- Modify: `internal/web/pages/aurenor_showing_script.templ` (`init`, `openStream`, `onerror`)
- Modify: `internal/web/pages/aurenor_theater_live.templ` (`init`, `onerror`)
- Modify: `internal/web/pages/aurenor_service_script.templ` (`onerror`)
- Test: `test/aurenor_theater_test.go` (new `TestShowingPageIsLive`), `test/aurenor_service_test.go`

**Interfaces:** none (browser code). The GA showing subscribes to `/events/summary/stream?slugs=<slug>` and reads `remaining` from the row; the seated showing keeps `/seats/stream`. Both are one connection.

- [x] **Step 1: Write the failing tests**

Append to `test/aurenor_theater_test.go`:

```go
// TestShowingPageIsLive covers the seat screen the way TestTheaterPageIsLive
// covers the board: it resolves for a seeded showing, 404s for an unknown one,
// carries its slug into the component, and its script opens a stream whether
// the showing is seated or open seating, falling back to polling only when the
// browser reports the stream CLOSED.
func TestShowingPageIsLive(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)

	resp := doJSON(t, newJarClient(), "GET", ts.URL+"/aurenor/theaters/grand/showings/"+slug, nil)
	mustStatus(t, resp, http.StatusOK, "showing page")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)

	for _, want := range []string{
		`x-data="showing(`,
		slug,
		`EventSource.CLOSED`,          // fallback only on a real refusal
		`/events/summary/stream?slugs=`, // the GA showing's one connection
		`/seats/stream`,               // the seated showing's one connection
	} {
		if !strings.Contains(body, want) {
			t.Errorf("showing page lacks %q", want)
		}
	}

	missing := doJSON(t, newJarClient(), "GET", ts.URL+"/aurenor/theaters/grand/showings/no-such-showing", nil)
	mustStatus(t, missing, http.StatusNotFound, "unknown showing")
	missing.Body.Close()
}

// TestTheaterBoardAlwaysOpensItsStream: an empty or failed first load must not
// leave the board dead until a reload.
func TestTheaterBoardAlwaysOpensItsStream(t *testing.T) {
	ts := newTheaterServer(t)
	resp := doJSON(t, newJarClient(), "GET", ts.URL+"/aurenor/theaters/grand", nil)
	mustStatus(t, resp, http.StatusOK, "board")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)

	if strings.Contains(body, "if (this.showings.length > 0) this.openStream()") {
		t.Error("the board still gates its stream on the first load succeeding")
	}
	if !strings.Contains(body, "EventSource.CLOSED") {
		t.Error("the board still falls back to polling on any onerror")
	}
}
```

Append to `test/aurenor_service_test.go`:

```go
// TestServiceScreenSurvivesAStreamBlip: EventSource fires onerror while it is
// retrying, not only when refused. Closing on the first one retires live
// updates for the whole session.
func TestServiceScreenSurvivesAStreamBlip(t *testing.T) {
	ts := newServiceServer(t)
	resp := doJSON(t, newJarClient(), "GET", ts.URL+"/aurenor/hospitals/solvarre-general/cardiology", nil)
	mustStatus(t, resp, http.StatusOK, "service screen")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), "EventSource.CLOSED") {
		t.Error("the service screen still falls back to polling on any onerror")
	}
}
```

(`newServiceServer` — use whatever helper `test/aurenor_service_test.go` already builds its server with; if it is inline, extract it to a helper of that name.)

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./test/ -run 'TestShowingPageIsLive|TestTheaterBoardAlwaysOpensItsStream|TestServiceScreenSurvivesAStreamBlip' -count=1 -v`
Expected: all three FAIL on the missing/present strings.

- [x] **Step 3: Implement**

`aurenor_showing_script.templ` — `init`:

```js
				async init() {
					await this.loadEvent();
					if (!this.loadError) {
						if (this.kind === 'seated') await this.loadSeats();
						this.restoreHold();
						this.openStream();
						document.addEventListener('visibilitychange', () => {
							// Only matters on the polling fallback; the stream never went away.
							if (!document.hidden && this.poller) this.refresh({ quiet: true });
						});
					}
					this.loading = false;
				},

				// What "refresh" means depends on what this showing sells: a seat
				// map for assigned seats, a count for open seating.
				async refresh(opts) {
					if (this.kind === 'seated') return this.loadSeats(opts);
					return this.loadEvent();
				},
```

(`restoreHold` is added in Task 7; for this task add a stub `restoreHold() {},` so the script parses, and Task 7 replaces it.)

`startPolling`: call `this.refresh({ quiet: true })` instead of `this.loadSeats(...)`.

`openStream` — one connection either way, and fall back only on a real refusal:

```js
				// Seats and counts change because of other people, so the page is told
				// rather than asking. One connection either way: the seat map for
				// assigned seating, the board summary (which carries `remaining`) for
				// open seating.
				openStream() {
					if (!window.EventSource) {
						this.startPolling();
						return;
					}
					const seated = this.kind === 'seated';
					const url = seated
						? '/events/' + encodeURIComponent(this.slug) + '/seats/stream'
						: '/events/summary/stream?slugs=' + encodeURIComponent(this.slug);
					const stream = new EventSource(url);
					this.stream = stream;

					stream.onmessage = (event) => {
						try {
							const data = JSON.parse(event.data);
							if (seated) {
								this.applySeats(data);
							} else {
								const row = (data || []).find(r => r.slug === this.slug);
								if (row) this.remaining = row.remaining;
							}
						} catch (e) {
							// A malformed frame is not worth tearing the stream down for.
						}
					};

					// EventSource fires onerror while it is retrying as well as when it
					// was refused. Only a refusal (503, wrong content-type) leaves it
					// CLOSED; a dropped packet leaves it CONNECTING and it reconnects on
					// its own. Fall back only on the refusal.
					stream.onerror = () => {
						if (stream.readyState !== EventSource.CLOSED) return;
						if (this.stream === stream) this.stream = null;
						if (!this.poller) this.startPolling();
					};
				},
```

`aurenor_theater_live.templ` — `init`:

```js
				async init() {
					await this.load();
					this.loading = false;
					// Always. The stream's first frame is the board, so an empty or
					// failed first load is repaired by it rather than left until reload.
					this.openStream();
				},
```

and its `onerror` becomes the same four-line body as above (readyState gate).

`aurenor_service_script.templ` — `onerror` becomes the same four-line body.

Then `go generate ./...`.

- [x] **Step 4: Run the tests**

Run: `go generate ./... && go build ./... && go test ./test/ -run 'Showing|Theater|Service' -count=1`
Expected: PASS. Also run `node --check` on the extracted script if node is on the machine: `curl -s http://localhost:4000/... ` is not available in tests, so instead: `go run ./cmd/clair server` is not needed — extract with `sed -n '/<script>/,/<\/script>/p' internal/web/pages/aurenor_showing_script.templ | sed '1d;$d' > /tmp/claude-1000/-home-cigzy-w-github-com-erancihan-clair/d0951b6b-14fa-4442-9ccf-eba99841dc9c/scratchpad/showing.js && node --check` that file, and the same for the other two scripts.

- [x] **Step 5: Checkpoint** — the three `.templ` files (+ their `_templ.go`), `test/aurenor_theater_test.go`, `test/aurenor_service_test.go`.

---

### Task 7: A reload keeps the buyer's hold; storage failures are not booking failures (W5, W6)

**Files:**
- Modify: `internal/web/pages/aurenor_showing_script.templ` (`restoreHold`)
- Modify: `internal/web/pages/aurenor_script.templ` (`aurenorSaveHolds`)
- Modify: `internal/web/pages/aurenor_service_script.templ` (`aurenorSaveStored`)
- Test: `test/aurenor_theater_test.go`, `test/aurenor_service_test.go`

- [x] **Step 1: Write the failing tests**

Append to `test/aurenor_theater_test.go`:

```go
// TestShowingPageRestoresItsHold: the hold is in localStorage from the moment
// it is placed; a reload must find it there, or the buyer's own seats render
// as taken and their countdown vanishes.
func TestShowingPageRestoresItsHold(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	resp := doJSON(t, newJarClient(), "GET", ts.URL+"/aurenor/theaters/grand/showings/"+slug, nil)
	mustStatus(t, resp, http.StatusOK, "showing page")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)

	if !strings.Contains(body, "window.aurenorHolds().find(") {
		t.Error("the showing screen never reads its own hold back from storage")
	}
	if !strings.Contains(body, "try {\n\t\t\t\twindow.localStorage.setItem(window.AURENOR_HOLDS_KEY") &&
		!strings.Contains(body, "try { window.localStorage.setItem(window.AURENOR_HOLDS_KEY") {
		t.Error("saving a hold to storage is not guarded; a quota error would report a successful hold as a failure")
	}
}
```

Append to `test/aurenor_service_test.go`:

```go
func TestServiceScreenGuardsItsStorageWrite(t *testing.T) {
	ts := newServiceServer(t)
	resp := doJSON(t, newJarClient(), "GET", ts.URL+"/aurenor/hospitals/solvarre-general/cardiology", nil)
	mustStatus(t, resp, http.StatusOK, "service screen")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), "try { window.localStorage.setItem(AURENOR_BOOKINGS_KEY") {
		t.Error("saving a booking to storage is not guarded")
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./test/ -run 'TestShowingPageRestoresItsHold|TestServiceScreenGuardsItsStorageWrite' -count=1 -v`
Expected: FAIL on the missing strings.

- [x] **Step 3: Implement**

`aurenor_showing_script.templ` — replace the Task 6 stub:

```js
				// A hold placed before a reload is still the buyer's: it is in storage
				// with its expiry, and the server still honours it. Without this a
				// reload shows their own seats as taken and drops the countdown.
				restoreHold() {
					const mine = window.aurenorHolds().find(h => h.slug === this.slug);
					if (!mine) return;
					this.hold = mine;
					this.startCountdown();
				},
```

`aurenor_script.templ` — `aurenorSaveHolds`:

```js
		window.aurenorSaveHolds = window.aurenorSaveHolds || function (list) {
			// Storage can be full, disabled or private. Losing the panel is a
			// degraded page; it must not read as a failed booking to the caller.
			try { window.localStorage.setItem(window.AURENOR_HOLDS_KEY, JSON.stringify(list)); } catch (e) {}
			window.dispatchEvent(new CustomEvent(window.AURENOR_HOLDS_EVENT));
		};
```

and in `aurenorHolds`, guard the prune write the same way: `try { window.localStorage.setItem(...) } catch (e) {}`.

`aurenor_service_script.templ` — `aurenorSaveStored`:

```js
		function aurenorSaveStored(list) {
			try { window.localStorage.setItem(AURENOR_BOOKINGS_KEY, JSON.stringify(list)); } catch (e) {}
			// The screen and the panel are separate components; this is how a
			// booking made in one reaches the other without a page reload.
			window.dispatchEvent(new CustomEvent(AURENOR_BOOKINGS_EVENT));
		}
```

Adjust the Task 7 test's expected substring to match the exact whitespace you used (the test lists two accepted forms; pick one and keep it).

Then `go generate ./...`.

- [x] **Step 4: Run the tests**

Run: `go generate ./... && go build ./... && go test ./test/ -run 'Showing|Service|Theater' -count=1`
Expected: PASS. `node --check` the three extracted scripts as in Task 6.

- [x] **Step 5: Checkpoint** — the three `.templ` files (+ `_templ.go`), the two test files.

---

### Task 8: Full verification

- [x] **Step 1:** `export $(grep -E '^DATABASE_URL=' .env | xargs) && go generate ./... && go build ./... && go vet ./... && go test -race -count=1 ./...` — every package `ok`, no `DATA RACE`.
- [x] **Step 2: Definition-of-done walk**, each pointed at its test: LISTEN drop resyncs (`TestHubResyncsAfterReconnect`); failed load keeps last good (`TestSnapshotCacheKeepsLastGoodOnFailure`); GA showing / empty board / blip keep updates (`TestShowingPageIsLive`, `TestTheaterBoardAlwaysOpensItsStream`, `TestServiceScreenSurvivesAStreamBlip`); reload keeps hold (`TestShowingPageRestoresItsHold`); strip and stream agree (`TestAvailabilityGETAndStreamAgreeOnTheWindow`).
- [x] **Step 3: Live check** (optional, if the owner wants to see it): `go run ./cmd/clair server`, open a GA showing in one tab and the board in another, hold from the board's seated showing, watch both move; `docker restart clair-postgres-1` with a board open, watch it resync without a reload.

---

### Task 9: Paper trail

**Files:**
- Modify: `docs/superpowers/specs/2026-09-02-evaluation-and-roadmap-v2.md`
- Modify: `docs/worldbuilding/aurenor.md`

- [x] **Step 1:** In the evaluation doc: row K17's Fix cell becomes "Kept on purpose: one integer per topic ever seen, bounded by the catalog; pruning would reset a counter the snapshot cache compares stamps against. Documented on the field." and its Step cell `—`. Row S21's Step cell becomes `A3` (it pays off with graceful shutdown). Row W4's Fix cell: "Done by dropping the 14-day cap: the shared snapshot covers the schedule's whole window; the cost is per change, not per reader." Append ` *(done 2026-09-02)*` to the A2 bullet in §3.
- [x] **Step 2:** In `aurenor.md`, the A2 bullet gets `*(done)*`.
- [x] **Step 3:** Tick every box in this plan and add a status banner under the title: "Status (date): executed in full" plus any deviations.
