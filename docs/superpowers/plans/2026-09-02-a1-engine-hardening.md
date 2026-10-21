# A1 Engine Hardening Implementation Plan

> **Status (2026-09-02): executed in full.** Every task below is built and the whole suite passes under `-race`. Deviations from the plan as written: Task 1 also had to update a third `newToken` caller in `internal/appointments/checkout.go`; Task 2's race test seeds one inventory per round (the shared `seedInv` helper pins a unique tier id).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every mutation that writes `booked` or `held` carries the capacity predicate; every `RowsAffected`/`res.Error` is read; a GA hold is capped; an offer is claimable only by its owner; a hold on a past showing is refused; the appointment "is this time offered" check has one implementation and cannot double-materialise a DST slot.

**Architecture:** All fixes land inside the existing three domain packages (`internal/booking`, `internal/ticketing`, `internal/appointments`) plus two handler edits in `internal/server/booking`. No new packages, no schema changes. The one structural move is `appointments.OpenSlotsFor`, which pulls the duplicated "rules + exceptions + booked counts" preamble out of the HTTP layer into the domain, so the simulator can call it later.

**Tech Stack:** Go 1.26 (go.mod says 1.25), GORM + pgx/v5 (`pgconn` is vendored), PostgreSQL 16, tests via `testsupport.PostgresDB` (isolated schema per test; needs `DATABASE_URL`).

**Spec:** `docs/superpowers/specs/2026-09-02-evaluation-and-roadmap-v2.md` §2.1 rows K1–K5, K8–K15, K18, K19, K22; §2.2 rows S1, S9; §3 "A1" definition of done. K21 (per-buyer lock for appointments) is retagged to A3 in Task 10: appointments carry no owner reference on the free path, so a cap there is a rate-limit question, not an engine one.

## Global Constraints

- **Do not commit.** The owner signs commits with GPG; every "checkpoint" step below means `git status` shows only the listed files. Never run `git add`/`git commit`/`git stash`.
- **Run tests with the database:** `export $(grep -E '^DATABASE_URL=' .env | xargs)` first in every shell, or DB tests fail at bootstrap. Postgres is `docker start clair-postgres-1` if it is down; wait for `pg_isready` (the container accepts TCP before it accepts queries).
- **`PrepareStmt: true`** in production GORM: never put two SQL statements in one `Exec` string.
- **Invariant:** `booked + held + blocked <= capacity`, enforced in the `WHERE` of the statement that does the increment. Never by a later check.
- **No new words for the engine.** The package is `booking`; prose calls it "the booking engine". Never "kernel".
- **`now` is injected**, never `time.Now()` inside a domain function.
- Error sentinels are `errors.New("<package>: ...")` and matched with `errors.Is`.
- Tests are `package <pkg>_test` except where they need an unexported symbol.

---

### Task 1: One token helper, real retryable-error detection (K18, K19)

**Files:**
- Modify: `internal/booking/hold.go:19-47`
- Modify: `internal/appointments/book.go:66,191-195`
- Create: `internal/booking/retry_test.go`

**Interfaces:**
- Produces: `booking.NewToken() string` (exported; 32 hex chars from 16 random bytes).
- `isRetryable` stays unexported; its contract is "true for SQLSTATE `40001` and `40P01`, at any wrap depth, and false for everything else".

- [x] **Step 1: Write the failing test**

`internal/booking/retry_test.go` (package `booking`, not `booking_test`, because `isRetryable` is unexported):

```go
package booking

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"serialization", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"wrapped deadlock", fmt.Errorf("commit: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"digits in message only", errors.New("row 40001 not found"), false},
		{"plain error", errors.New("boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryable(c.err); got != c.want {
				t.Fatalf("isRetryable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestNewTokenIs32Hex(t *testing.T) {
	a, b := NewToken(), NewToken()
	if len(a) != 32 || a == b {
		t.Fatalf("NewToken() = %q, %q; want two distinct 32-char tokens", a, b)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/booking/ -run 'TestIsRetryable|TestNewTokenIs32Hex' -v`
Expected: FAIL — `undefined: NewToken`, and (after adding it) `digits in message only` returns true.

- [x] **Step 3: Implement**

In `internal/booking/hold.go`, replace lines 19–23 and 40–47:

```go
// NewToken returns an opaque 32-character hex handle for holds and cancel links.
func NewToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
```

```go
// isRetryable reports whether err is a Postgres serialization failure (40001)
// or deadlock (40P01), at any wrap depth. Matching on the SQLSTATE, not the
// message, so a business error that happens to mention those digits is not
// retried.
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}
```

Add `"github.com/jackc/pgx/v5/pgconn"` to the imports and delete the `"strings"` import. Replace the one call at line 87 `Token: newToken()` with `Token: NewToken()`.

In `internal/appointments/book.go`: delete `newToken()` (lines 191–195) and the `crypto/rand` and `encoding/hex` imports; change line 66 to `CancelToken: booking.NewToken(),`.

- [x] **Step 4: Run the tests**

Run: `go build ./... && go test ./internal/booking/ ./internal/appointments/`
Expected: PASS.

- [x] **Step 5: Checkpoint**

`git status` shows `internal/booking/hold.go`, `internal/booking/retry_test.go`, `internal/appointments/book.go` changed. Owner commits.

---

### Task 2: Lock the release path; put the capacity predicate on commit (K1, K2)

**Files:**
- Modify: `internal/booking/hold.go:122-160,175-203`
- Test: `internal/booking/hold_test.go`

**Interfaces:**
- No signature changes. `CommitTx` now returns `ErrSoldOut` when the row cannot take the units (capacity was reduced under it).

- [x] **Step 1: Write the failing tests**

Append to `internal/booking/hold_test.go`:

```go
// TestRelease_RacesReclaimWithoutDoubleDecrement: an expired hold can be freed
// by two paths at once — Release, and the reclaim step inside HoldUnitsTx. Only
// one of them may decrement `held`. The long-lived hold keeps `held` positive so
// the `held >= qty` guard cannot mask a second decrement.
func TestRelease_RacesReclaimWithoutDoubleDecrement(t *testing.T) {
	db := holdDB(t)
	for round := 0; round < 20; round++ {
		inv := seedInv(t, db, 20)
		base := time.Now()

		if _, err := booking.HoldUnits(db, inv.ID, 5, "owner-long", nil, time.Hour, base); err != nil {
			t.Fatalf("long hold: %v", err)
		}
		var tokens []string
		for i := 0; i < 5; i++ {
			h, err := booking.HoldUnits(db, inv.ID, 1, fmt.Sprintf("owner-short-%d", i), nil, time.Minute, base)
			if err != nil {
				t.Fatalf("short hold %d: %v", i, err)
			}
			tokens = append(tokens, h.Token)
		}

		later := base.Add(2 * time.Minute)
		var wg sync.WaitGroup
		for _, tok := range tokens {
			wg.Add(1)
			go func(tok string) {
				defer wg.Done()
				_ = booking.Release(db, tok, later)
			}(tok)
		}
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, _ = booking.HoldUnits(db, inv.ID, 1, fmt.Sprintf("reclaimer-%d", i), nil, time.Hour, later)
			}(i)
		}
		wg.Wait()

		var active int64
		db.Raw(`SELECT COALESCE(SUM(qty), 0) FROM booking_holds
			WHERE inventory_id = ? AND status = 'active' AND expires_at > ?`, inv.ID, later).Scan(&active)
		var got models.BookingInventory
		db.First(&got, inv.ID)
		if got.Held != int(active) {
			t.Fatalf("round %d: held=%d but active holds total %d", round, got.Held, active)
		}
	}
}

// TestCommit_RefusesWhenCapacityWasCut: `held` is bookkeeping; capacity is the
// truth. If the two disagree, the statement that writes `booked` must lose.
func TestCommit_RefusesWhenCapacityWasCut(t *testing.T) {
	db := holdDB(t)
	inv := seedInv(t, db, 2)
	now := time.Now()

	h, err := booking.HoldUnits(db, inv.ID, 2, "owner", nil, time.Hour, now)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	// Bypass SetCapacity on purpose: this is the corrupt state, not a valid edit.
	if err := db.Exec(`UPDATE booking_inventories SET capacity = 1 WHERE id = ?`, inv.ID).Error; err != nil {
		t.Fatal(err)
	}

	if err := booking.Commit(db, h.Token, now); !errors.Is(err, booking.ErrSoldOut) {
		t.Fatalf("commit over capacity: got %v, want ErrSoldOut", err)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Booked != 0 || got.Held != 2 {
		t.Fatalf("after refused commit booked=%d held=%d, want 0/2", got.Booked, got.Held)
	}
}
```

Add `"errors"` to the test file's imports.

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/booking/ -run 'TestRelease_RacesReclaim|TestCommit_RefusesWhenCapacityWasCut' -v -count=1`
Expected: `TestCommit_RefusesWhenCapacityWasCut` FAILS (commit succeeds, `booked=2 capacity=1`). `TestRelease_RacesReclaim…` fails in at least some runs (`held=` less than the active total); rerun with `-count=3` if the first pass is green by luck.

- [x] **Step 3: Implement**

`CommitTx` — replace the inventory update (lines 144–155) with:

```go
	// The capacity predicate is here, not only on the hold, so a `held` that
	// drifted (a double release, a cut capacity) cannot become an oversell: the
	// statement that writes `booked` is the one that checks.
	res := tx.Model(&models.BookingInventory{}).
		Where("id = ? AND held >= ? AND booked + blocked + ? <= capacity", h.InventoryID, h.Qty, h.Qty).
		UpdateColumns(map[string]any{
			"held":   gorm.Expr("held - ?", h.Qty),
			"booked": gorm.Expr("booked + ?", h.Qty),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrSoldOut
	}
```

Note the predicate is `booked + blocked + qty <= capacity`, without `held`: the units are moving out of `held` in the same statement.

`ReleaseTx` — replace the read (lines 180–187) with:

```go
	// FOR UPDATE, like CommitTx: the reclaim step in HoldUnitsTx releases expired
	// holds too, and without the lock both paths read "active" and both
	// decrement `held`. Under READ COMMITTED the locked re-read re-evaluates
	// the status predicate, so a hold reclaimed meanwhile is simply not found.
	var h models.BookingHold
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("token = ? AND status = 'active'", token).First(&h).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
```

Also change the `held - qty` update in `ReleaseTx` (lines 188–192) to check its result:

```go
	res := tx.Model(&models.BookingInventory{}).
		Where("id = ? AND held >= ?", h.InventoryID, h.Qty).
		UpdateColumn("held", gorm.Expr("held - ?", h.Qty))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("booking: release %s: held below hold qty on inventory %d", token, h.InventoryID)
	}
```

Update `CommitTx`'s doc comment: "Returns ErrSoldOut when the inventory cannot take the units (capacity reduced under the hold)."

- [x] **Step 4: Run the tests**

Run: `go test ./internal/booking/ -count=3`
Expected: PASS, three times.

- [x] **Step 5: Checkpoint**

`git status` shows `internal/booking/hold.go`, `internal/booking/hold_test.go`. Owner commits.

---

### Task 3: Read every result in the engine (K8, K9, K10, K11)

**Files:**
- Modify: `internal/booking/inventory.go:121-126`
- Modify: `internal/booking/hold.go:93-98,217-241,262-268,328-346`
- Test: `internal/booking/inventory_test.go`

**Interfaces:**
- `Block` now returns `ErrSoldOut` when the guard rejects.
- `ReapExpiredHolds` returns `(released int, err error)` where `err` is `errors.Join` of every per-hold release failure and the order-expiry update error; `released` still counts successes.

- [x] **Step 1: Write the failing test**

Append to `internal/booking/inventory_test.go`:

```go
func TestBlock_RefusesOverCapacity(t *testing.T) {
	db := memDB(t)
	inv := models.BookingInventory{Capacity: 1, TierID: uptr(9)}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	if err := booking.BookDirect(db, inv.ID, 1); err != nil {
		t.Fatalf("book: %v", err)
	}
	if err := booking.Block(db, inv.ID, 1); !errors.Is(err, booking.ErrSoldOut) {
		t.Fatalf("block over capacity: got %v, want ErrSoldOut", err)
	}
	var got models.BookingInventory
	db.First(&got, inv.ID)
	if got.Blocked != 0 {
		t.Fatalf("blocked=%d after refused block, want 0", got.Blocked)
	}
}
```

Add `"errors"` to the imports if missing.

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/booking/ -run TestBlock_RefusesOverCapacity -v`
Expected: FAIL — `block over capacity: got <nil>`.

- [x] **Step 3: Implement**

`inventory.go` `Block`:

```go
// Block removes qty sellable units without deleting the row (house/ADA/comp).
// Returns ErrSoldOut when the units are not free to block.
func Block(tx *gorm.DB, invID uint, qty int) error {
	res := tx.Model(&models.BookingInventory{}).
		Where("id = ? AND booked + held + blocked + ? <= capacity", invID, qty).
		UpdateColumn("blocked", gorm.Expr("blocked + ?", qty))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrSoldOut
	}
	return nil
}
```

`hold.go` `HoldUnitsTx` idempotent branch (lines 93–98): check the compensating decrement.

```go
	if h.ID == 0 { // conflict → an active hold already exists for (inv, owner)
		res := tx.Model(&models.BookingInventory{}).
			Where("id = ? AND held >= ?", invID, qty).
			UpdateColumn("held", gorm.Expr("held - ?", qty))
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, fmt.Errorf("booking: hold on inventory %d: could not undo provisional increment", invID)
		}
```

`hold.go` `offerToWaitlist` (lines 229–232): a re-hold that fails because the unit is gone moves on to the next unit; anything else is an error.

```go
		h, err := HoldUnitsTx(tx, invID, 1, entry.OwnerRef, nil, "waitlist_offer", offerTTL, now)
		if errors.Is(err, ErrSoldOut) {
			continue // this unit was taken between the free and the offer; try the next
		}
		if err != nil {
			return err
		}
```

`hold.go` `JoinWaitlist` (lines 265–267): check the count's error.

```go
	var ahead int64
	if err := db.Model(&models.BookingWaitlistEntry{}).
		Where("inventory_id = ? AND status = 'waiting' AND created_at < ?", invID, entry.CreatedAt).
		Count(&ahead).Error; err != nil {
		return nil, 0, err
	}
```

`hold.go` `ReapExpiredHolds`:

```go
// ReapExpiredHolds releases expired active holds and expires stale pending
// orders whose holds are all gone. Returns the number of holds released and
// every failure it met on the way; a hold that fails to release is reported,
// not silently retried forever.
func ReapExpiredHolds(db *gorm.DB, now time.Time) (int, error) {
	var holds []models.BookingHold
	if err := db.Where("status = 'active' AND expires_at <= ?", now).Limit(500).Find(&holds).Error; err != nil {
		return 0, err
	}
	released := 0
	var errs []error
	for _, h := range holds {
		if err := Release(db, h.Token, now); err != nil {
			errs = append(errs, fmt.Errorf("release %s: %w", h.Token, err))
			continue
		}
		released++
	}
	if err := db.Model(&models.BookingOrder{}).
		Where("status = 'pending' AND updated_at <= ?", now.Add(-30*time.Minute)).
		Where("NOT EXISTS (SELECT 1 FROM booking_holds WHERE booking_holds.order_id = booking_orders.id AND booking_holds.status = 'active')").
		Update("status", "expired").Error; err != nil {
		errs = append(errs, fmt.Errorf("expire stale orders: %w", err))
	}
	return released, errors.Join(errs...)
}
```

The scheduler already logs the error and still logs `released` when positive (`internal/cmd/scheduler.go:47-54`); change its `return` after the error log so both lines can print:

```go
				released, err := booking.ReapExpiredHolds(db, time.Now())
				if err != nil {
					logger.Error("reap expired holds", zap.Error(err))
				}
				if released > 0 {
					logger.Info("released expired holds", zap.Int("count", released))
				}
```

- [x] **Step 4: Run the tests**

Run: `go build ./... && go test ./internal/booking/ ./internal/ticketing/ ./internal/appointments/`
Expected: PASS (the waitlist tests exercise `offerToWaitlist` and `JoinWaitlist`; the reaper tests exercise the new return shape).

- [x] **Step 5: Checkpoint**

`git status` shows `internal/booking/inventory.go`, `internal/booking/inventory_test.go`, `internal/booking/hold.go`, `internal/cmd/scheduler.go`. Owner commits.

---

### Task 4: Ticketing refuses closed events, foreign seats and mixed currencies (K5, K14, K15)

**Files:**
- Modify: `internal/ticketing/event.go:12-17` (sentinels)
- Modify: `internal/ticketing/hold.go:44-96,183-269`
- Modify: `internal/ticketing/ticketing_test.go:92,97,249` (pass a real event id)
- Modify: `internal/server/booking/api_tickets.go` (error mapping in `holdGA` and `holdSeats`)
- Test: `internal/ticketing/hold_truth_test.go`

**Interfaces:**
- Produces: `ticketing.ErrEventClosed` ("ticketing: event is not on sale"), `ticketing.ErrMixedCurrency` ("ticketing: seats span more than one currency").
- `HoldGA` and `HoldSeatGroup` signatures unchanged. `HoldSeatGroup` now returns `ErrInvalidSeat` for a seat whose tier belongs to another event; both return `ErrEventClosed` when `now >= event.StartAt` or `event.Status != "scheduled"`.

- [x] **Step 1: Write the failing tests**

Append to `internal/ticketing/hold_truth_test.go`:

```go
// closeEvent moves an event's showtime into the past, the state a stale tab
// or a simulator running late would try to sell into.
func closeEvent(t *testing.T, db *gorm.DB, eventID uint) {
	t.Helper()
	if err := db.Model(&models.BookingEvent{}).Where("id = ?", eventID).
		Update("start_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("close event: %v", err)
	}
}

func TestHoldGARefusesStartedEvent(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 10)
	closeEvent(t, db, tier.EventID)

	_, _, err := ticketing.HoldGA(db, tier, 1, ticketing.Buyer{OwnerRef: "guest:late"}, time.Hour, time.Now(), 0)
	if !errors.Is(err, ticketing.ErrEventClosed) {
		t.Fatalf("hold on a started event: got %v, want ErrEventClosed", err)
	}
}

func TestHoldSeatGroupRefusesStartedEvent(t *testing.T) {
	db := ticketDB(t)
	seats := seatedEvent(t, db, "A-1", "A-2")
	eventID := seatOwnerEventID(t, db, seats[0])
	closeEvent(t, db, eventID)

	_, _, err := ticketing.HoldSeatGroup(db, eventID, []uint{seats[0].ID}, ticketing.Buyer{OwnerRef: "guest:late"}, time.Hour, time.Now(), 0)
	if !errors.Is(err, ticketing.ErrEventClosed) {
		t.Fatalf("seat hold on a started event: got %v, want ErrEventClosed", err)
	}
}

// TestHoldSeatGroupRefusesForeignSeat: the seat ids are caller-supplied primary
// keys. The HTTP handler checks membership, but a caller inside the process (the
// simulator, a test) gets no handler, so the domain has to check too.
func TestHoldSeatGroupRefusesForeignSeat(t *testing.T) {
	db := ticketDB(t)
	mine := seatedEvent(t, db, "A-1")
	myEvent := seatOwnerEventID(t, db, mine[0])

	other, err := ticketing.CreateEvent(db, ticketing.EventInput{
		OwnerID: 1, Slug: "ev-other", Name: "Other", StartAt: time.Now().Add(24 * time.Hour),
		Tiers: []ticketing.TierInput{{Name: "Stalls", Kind: "seated", PriceCents: 1000,
			Seats: []ticketing.SeatInput{{Label: "B-1", Section: "Stalls", RowName: "B", Number: 1}}}},
	})
	if err != nil {
		t.Fatalf("create other event: %v", err)
	}
	var foreign models.BookingSeat
	db.Joins("JOIN booking_ticket_tiers t ON t.id = booking_seats.tier_id").
		Where("t.event_id = ?", other.ID).First(&foreign)

	_, _, err = ticketing.HoldSeatGroup(db, myEvent, []uint{mine[0].ID, foreign.ID},
		ticketing.Buyer{OwnerRef: "guest:reach"}, time.Hour, time.Now(), 0)
	if !errors.Is(err, ticketing.ErrInvalidSeat) {
		t.Fatalf("hold with a foreign seat: got %v, want ErrInvalidSeat", err)
	}
	inv, _ := booking.InventoryByOwner(db, booking.SeatOwner(mine[0].ID))
	if inv.Held != 0 {
		t.Fatalf("own seat held=%d after a refused group, want 0", inv.Held)
	}
}

func TestHoldSeatGroupRefusesMixedCurrency(t *testing.T) {
	db := ticketDB(t)
	ev, err := ticketing.CreateEvent(db, ticketing.EventInput{
		OwnerID: 1, Slug: "ev-mixed", Name: "Mixed", StartAt: time.Now().Add(24 * time.Hour),
		Tiers: []ticketing.TierInput{
			{Name: "Stalls", Kind: "seated", PriceCents: 1000, Currency: "USD",
				Seats: []ticketing.SeatInput{{Label: "A-1", Section: "Stalls", RowName: "A", Number: 1}}},
			{Name: "Circle", Kind: "seated", PriceCents: 900, Currency: "EUR",
				Seats: []ticketing.SeatInput{{Label: "C-1", Section: "Circle", RowName: "C", Number: 1}}},
		},
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	var seats []models.BookingSeat
	db.Joins("JOIN booking_ticket_tiers t ON t.id = booking_seats.tier_id").
		Where("t.event_id = ?", ev.ID).Order("booking_seats.label").Find(&seats)

	_, _, err = ticketing.HoldSeatGroup(db, ev.ID, []uint{seats[0].ID, seats[1].ID},
		ticketing.Buyer{OwnerRef: "guest:fx"}, time.Hour, time.Now(), 0)
	if !errors.Is(err, ticketing.ErrMixedCurrency) {
		t.Fatalf("mixed-currency group: got %v, want ErrMixedCurrency", err)
	}
}
```

Add `"errors"` to the test file's imports.

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ticketing/ -run 'RefusesStartedEvent|RefusesForeignSeat|RefusesMixedCurrency' -v`
Expected: FAIL — `undefined: ticketing.ErrEventClosed` / `ErrMixedCurrency`; after stubbing the sentinels, each test reports `got <nil>`.

- [x] **Step 3: Implement**

`event.go` sentinels block:

```go
var (
	// ErrInvalidSeat means a requested seat is missing, not sellable, or not
	// part of the event being sold.
	ErrInvalidSeat = errors.New("ticketing: invalid or non-sellable seat")
	// ErrInvalidTier means a tier kind is not ga|seated.
	ErrInvalidTier = errors.New("ticketing: tier kind must be ga or seated")
	// ErrEventClosed means the event is cancelled or its showtime has passed.
	ErrEventClosed = errors.New("ticketing: event is not on sale")
	// ErrMixedCurrency means one seat group priced in more than one currency.
	ErrMixedCurrency = errors.New("ticketing: seats span more than one currency")
)
```

Add to `internal/ticketing/waitlist.go` (next to `lockBuyer`):

```go
// assertEventOpen refuses to sell into an event that is cancelled or has
// already started. The page hides such showtimes; the domain has to refuse
// them too, because a stale tab or an in-process caller sees no page.
func assertEventOpen(tx *gorm.DB, eventID uint, now time.Time) error {
	var ev models.BookingEvent
	if err := tx.First(&ev, eventID).Error; err != nil {
		return err
	}
	if ev.Status != "scheduled" || !now.Before(ev.StartAt) {
		return ErrEventClosed
	}
	return nil
}
```

`hold.go` `HoldGA`: after the `lockBuyer` call (line 48) add:

```go
			if err := assertEventOpen(tx, tier.EventID, now); err != nil {
				return err
			}
```

`hold.go` `HoldSeatGroup`: after its `lockBuyer` call (line 188) add the same three lines with `eventID`. Then in the seat loop replace lines 206–214 with:

```go
				var tier models.BookingTicketTier
				if err := tx.First(&tier, seat.TierID).Error; err != nil {
					return err
				}
				// A seat id is a caller-supplied key; it must belong to the event
				// this group is being sold for.
				if tier.EventID != eventID {
					return ErrInvalidSeat
				}
				inv, err := booking.InventoryByOwner(tx, booking.SeatOwner(sid))
				if err != nil {
					return err
				}
				// One order, one currency: cents from two currencies do not add.
				if currency == "" {
					currency = tier.Currency
				} else if tier.Currency != currency {
					return ErrMixedCurrency
				}
```

and change the initialiser on line 197 from `currency := "USD"` to `currency := ""`. After the loop, before `newOrder` (line 247), add:

```go
			if currency == "" {
				currency = "USD"
			}
```

`ticketing_test.go`: the three calls that pass `0` as `eventID` (lines 92, 97 in `TestHoldSeatGroup_AllOrNothing`; line 249 in `TestCapture_Seated_CreatesTicketPerSeat`) become `seatOwnerEventID(t, db, seats[0])`. Add `eventID := seatOwnerEventID(t, db, seats[0])` after each `seats := seatedEvent(...)` line and use it.

`api_tickets.go` — in `holdGA`'s switch (after the `ErrPurchaseLimit` case) and in `holdSeats`'s switch (after `ErrInvalidSeat`), add:

```go
		case errors.Is(err, ticketing.ErrEventClosed):
			http.Error(w, "this showing is no longer on sale", http.StatusConflict)
```

and in `holdSeats` only:

```go
		case errors.Is(err, ticketing.ErrMixedCurrency):
			http.Error(w, "seats must be priced in one currency", http.StatusBadRequest)
```

- [x] **Step 4: Run the tests**

Run: `go build ./... && go test ./internal/ticketing/ ./test/ -run 'Ticketing|Theater|Refuses' -count=1`
Expected: PASS.

- [x] **Step 5: Checkpoint**

`git status` shows `internal/ticketing/event.go`, `internal/ticketing/hold.go`, `internal/ticketing/waitlist.go`, `internal/ticketing/ticketing_test.go`, `internal/ticketing/hold_truth_test.go`, `internal/server/booking/api_tickets.go`. Owner commits.

---

### Task 5: Waitlist claim is owner-scoped, locked and capped (K4, S9, K11)

**Files:**
- Modify: `internal/ticketing/waitlist.go:98-167`
- Test: `internal/ticketing/hold_truth_test.go`

**Interfaces:**
- `ClaimOffer(db, token, buyer, now)` unchanged in shape. A token that is not `buyer.OwnerRef`'s returns `ErrOfferExpired` (same as unknown — the response must not confirm the token exists). A claim that would exceed `event.MaxPerBuyer` returns `ErrPurchaseLimit`.
- Produces: `tierForInventory(tx, inv) (*models.BookingTicketTier, error)` replacing `priceForInventory`.

- [x] **Step 1: Write the failing tests**

Append to `internal/ticketing/hold_truth_test.go`:

```go
// offerFor drives one unit through hold → expiry → reap so that `waiter` holds
// a live offer, and returns its token.
func offerFor(t *testing.T, db *gorm.DB, tier *models.BookingTicketTier, waiter string, base time.Time) (string, time.Time) {
	t.Helper()
	inv, err := booking.InventoryByOwner(db, booking.TierOwner(tier.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ticketing.HoldGA(db, tier, 1, ticketing.Buyer{OwnerRef: "guest:first"}, time.Minute, base, 0); err != nil {
		t.Fatalf("first hold: %v", err)
	}
	if _, _, err := booking.JoinWaitlist(db, inv.ID, waiter); err != nil {
		t.Fatalf("join: %v", err)
	}
	later := base.Add(2 * time.Minute)
	if _, err := booking.ReapExpiredHolds(db, later); err != nil {
		t.Fatalf("reap: %v", err)
	}
	var entry models.BookingWaitlistEntry
	db.Where("owner_ref = ? AND status = 'offered'", waiter).First(&entry)
	if entry.OfferToken == nil {
		t.Fatal("no offer was made")
	}
	return *entry.OfferToken, later
}

func TestClaimOfferRefusesAnotherBuyer(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 1)
	token, later := offerFor(t, db, tier, "guest:owner", time.Now())

	_, err := ticketing.ClaimOffer(db, token, ticketing.Buyer{OwnerRef: "guest:thief"}, later)
	if !errors.Is(err, ticketing.ErrOfferExpired) {
		t.Fatalf("claim by another buyer: got %v, want ErrOfferExpired", err)
	}
	if _, err := ticketing.ClaimOffer(db, token, ticketing.Buyer{OwnerRef: "guest:owner"}, later); err != nil {
		t.Fatalf("claim by the owner after a refused attempt: %v", err)
	}
}

func TestClaimOfferRespectsBuyerCap(t *testing.T) {
	db := ticketDB(t)
	tier := gaTier(t, db, 2)
	if err := db.Model(&models.BookingEvent{}).Where("id = ?", tier.EventID).Update("max_per_buyer", 1).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	// The waiter already owns one ticket, which is the whole cap. It has to be
	// a captured ticket, not a live hold: a live hold on the same tier would be
	// reused as the "offer" by the engine's one-active-hold-per-owner rule, and
	// the claim would fail for the wrong reason.
	order, _, err := ticketing.HoldGA(db, tier, 1, ticketing.Buyer{OwnerRef: "guest:greedy"}, time.Hour, base, 1)
	if err != nil {
		t.Fatalf("waiter's own hold: %v", err)
	}
	pay := models.BookingPayment{Provider: "mock", Kind: "capture", Status: "succeeded", IdempotencyKey: "greedy-1"}
	if err := booking.CaptureOrder(db, order.ID, pay, base); err != nil {
		t.Fatalf("capture waiter's ticket: %v", err)
	}
	token, later := offerFor(t, db, tier, "guest:greedy", base)

	_, err := ticketing.ClaimOffer(db, token, ticketing.Buyer{OwnerRef: "guest:greedy"}, later)
	if !errors.Is(err, ticketing.ErrPurchaseLimit) {
		t.Fatalf("claim over cap: got %v, want ErrPurchaseLimit", err)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ticketing/ -run 'TestClaimOffer' -v`
Expected: both FAIL with `got <nil>`.

- [x] **Step 3: Implement**

Replace `ClaimOffer` and `priceForInventory` in `waitlist.go` with:

```go
// ClaimOffer converts an active waitlist offer into a pending order the buyer
// can capture. The offer hold already reserves the unit; this attaches an order
// so the normal capture path issues the ticket.
//
// The token alone proves nothing: it is matched against the caller's owner
// reference, so a leaked token cannot be claimed by somebody else, and the
// answer for another buyer's token is the same as for an unknown one. The claim
// counts against the event's per-buyer cap like any other hold.
func ClaimOffer(db *gorm.DB, token string, buyer Buyer, now time.Time) (*models.BookingOrder, error) {
	var order *models.BookingOrder
	err := booking.WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			var h models.BookingHold
			res := tx.Where("token = ? AND owner_ref = ? AND status = 'active' AND purpose = 'waitlist_offer'",
				token, buyer.OwnerRef).Limit(1).Find(&h)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 || !now.Before(h.ExpiresAt) {
				return ErrOfferExpired
			}
			var inv models.BookingInventory
			if err := tx.First(&inv, h.InventoryID).Error; err != nil {
				return err
			}
			tier, err := tierForInventory(tx, inv)
			if err != nil {
				return err
			}
			var ev models.BookingEvent
			if err := tx.First(&ev, tier.EventID).Error; err != nil {
				return err
			}
			if err := lockBuyer(tx, ev.ID, buyer.OwnerRef); err != nil {
				return err
			}
			// The offer hold has no order item yet, so it is not in the count;
			// its qty is what this claim adds.
			if err := assertBuyerLimit(tx, ev.ID, buyer.OwnerRef, h.Qty, ev.MaxPerBuyer, now); err != nil {
				return err
			}

			o := newOrder(buyer, tier.PriceCents*int64(h.Qty), tier.Currency)
			if err := tx.Create(o).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.BookingHold{}).Where("id = ?", h.ID).Update("order_id", o.ID).Error; err != nil {
				return err
			}
			item := &models.BookingOrderItem{
				OrderID: o.ID, HoldToken: h.Token, InventoryID: inv.ID, Qty: h.Qty,
				PriceCents: tier.PriceCents, Status: "reserved",
			}
			if err := tx.Create(item).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.BookingWaitlistEntry{}).Where("offer_token = ?", token).Update("status", "converted").Error; err != nil {
				return err
			}
			order = o
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// tierForInventory resolves the ticket tier behind a tier- or seat-owned
// inventory row; price, currency and event all hang off it.
func tierForInventory(tx *gorm.DB, inv models.BookingInventory) (*models.BookingTicketTier, error) {
	var tier models.BookingTicketTier
	switch {
	case inv.TierID != nil:
		if err := tx.First(&tier, *inv.TierID).Error; err != nil {
			return nil, err
		}
	case inv.SeatID != nil:
		var seat models.BookingSeat
		if err := tx.First(&seat, *inv.SeatID).Error; err != nil {
			return nil, err
		}
		if err := tx.First(&tier, seat.TierID).Error; err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("ticketing: inventory %d is not a tier or seat", inv.ID)
	}
	return &tier, nil
}
```

`assertBuyerLimit` also drops its `Scan` error today (line 47–57); make it `if err := tx.Raw(...).Scan(&have).Error; err != nil { return err }`.

- [x] **Step 4: Run the tests**

Run: `go test ./internal/ticketing/ ./test/ -run 'Waitlist|ClaimOffer' -count=1 -v`
Expected: PASS, including the pre-existing `TestWaitlist_OfferClaimCapture` and `TestTicketingHTTP_WaitlistClaim`.

- [x] **Step 5: Checkpoint**

`git status` shows `internal/ticketing/waitlist.go`, `internal/ticketing/hold_truth_test.go`. Owner commits.

---

### Task 6: Cap a GA hold request (S1)

**Files:**
- Modify: `internal/server/booking/api_tickets.go:22-24,95-98`
- Test: `test/aurenor_theater_test.go`

**Interfaces:**
- Produces: `const maxGAPerRequest = 10` next to `maxSeatsPerRequest`. A `qty` above it is a 400.

- [x] **Step 1: Write the failing test**

Append to `test/aurenor_theater_test.go`:

```go
// TestTheaterGARequestIsBounded is the pooled twin of
// TestTheaterSeatRequestIsBounded: an event with no per-buyer cap still cannot
// have its whole pool drained by one request.
func TestTheaterGARequestIsBounded(t *testing.T) {
	ts := newTheaterServer(t)

	var slug string
	var tierID uint
	for _, s := range aurenor.TheaterEventSlugs("grand") {
		ev := getEvent(t, ts, s)
		for _, tier := range ev.Tiers {
			if tier.Kind == "ga" {
				slug, tierID = s, tier.ID
			}
		}
		if slug != "" {
			break
		}
	}
	if slug == "" {
		t.Fatal("no general-admission showing seeded")
	}

	resp := doJSON(t, newJarClient(), "POST",
		ts.URL+"/events/"+slug+"/tiers/"+itoa(tierID)+"/hold",
		map[string]any{"qty": 50, "guest_name": "Whole Pool"})
	mustStatus(t, resp, http.StatusBadRequest, "hold 50 GA tickets in one request")
	resp.Body.Close()
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./test/ -run TestTheaterGARequestIsBounded -count=1 -v`
Expected: FAIL — status 201 (or 409 if the pool is smaller than 50), not 400.

- [x] **Step 3: Implement**

`api_tickets.go` constants:

```go
// maxSeatsPerRequest and maxGAPerRequest bound one hold request independently
// of the event's own per-buyer cap, which is zero — unlimited — by default.
const (
	maxSeatsPerRequest = 10
	maxGAPerRequest    = 10
)
```

In `holdGA`, after `if body.Qty <= 0 { body.Qty = 1 }`:

```go
		if body.Qty > maxGAPerRequest {
			http.Error(w, "too many tickets in one request", http.StatusBadRequest)
			return
		}
```

- [x] **Step 4: Run the tests**

Run: `go test ./test/ -run 'TestTheaterGA' -count=1`
Expected: PASS.

- [x] **Step 5: Checkpoint**

`git status` shows `internal/server/booking/api_tickets.go`, `test/aurenor_theater_test.go`. Owner commits.

---

### Task 7: `OpenSlotsFor` in the domain; remaining uses the slot's own capacity (K22, K12)

**Files:**
- Modify: `internal/appointments/availability.go` (split the generator out; `SlotCount`)
- Create: `internal/appointments/open.go`
- Modify: `internal/appointments/availability_test.go:125-146`
- Modify: `internal/server/booking/api_availability.go` (both functions call `OpenSlotsFor`)
- Test: `internal/appointments/open_test.go`

**Interfaces:**
- Produces:
  - `type SlotCount struct { Booked, Capacity int }`
  - `ComputeAvailability(sched, rules, exceptions, counts map[time.Time]SlotCount, from, to, now) ([]OpenSlot, error)` — same as before except the map value.
  - `offeredStarts(sched, rules, exceptions, from, to, now) ([]time.Time, error)` (unexported; Task 8 consumes it).
  - `OpenSlotsFor(db *gorm.DB, sched *models.BookingSchedule, from, to, now time.Time) ([]OpenSlot, error)`.

- [x] **Step 1: Write the failing tests**

Update `TestComputeAvailability_FullyBookedExcluded` in `availability_test.go` so the map is the new type:

```go
	booked := map[time.Time]appointments.SlotCount{
		mustUTC(t, "2026-03-02T09:00:00Z"): {Booked: 1, Capacity: 1}, // capacity 1 → this slot is full
	}
```

Create `internal/appointments/open_test.go`:

```go
package appointments_test

import (
	"testing"

	"github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/database/models"
)

// TestOpenSlotsFor_UsesSlotCapacity: a slot that has been materialised carries
// its own capacity, and that is the number the booking guard enforces. Reporting
// the schedule's current capacity instead would advertise seats the guard will
// refuse (or hide ones it would accept).
func TestOpenSlotsFor_UsesSlotCapacity(t *testing.T) {
	db := bookDB(t)
	sched := seedSchedule(t, db) // Mon 09:00–12:00, 30 min, capacity 1
	now := at(t, "2026-03-01T00:00:00Z")
	nine := at(t, "2026-03-02T09:00:00Z")

	if _, err := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: nine, GuestName: "Ada"}, now); err != nil {
		t.Fatalf("book: %v", err)
	}
	// The host later raises the schedule's capacity; the 09:00 slot was
	// materialised at 1 and stays 1.
	if err := db.Model(&models.BookingSchedule{}).Where("id = ?", sched.ID).Update("capacity", 3).Error; err != nil {
		t.Fatal(err)
	}
	sched.Capacity = 3

	slots, err := appointments.OpenSlotsFor(db, sched, at(t, "2026-03-02T00:00:00Z"), at(t, "2026-03-03T00:00:00Z"), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range slots {
		if s.StartAt.Equal(nine) {
			t.Fatalf("09:00 is full at its materialised capacity of 1, but was reported with remaining=%d", s.Remaining)
		}
	}
	if len(slots) != 5 || slots[0].Remaining != 3 {
		t.Fatalf("want 5 open slots at the new capacity 3, got %d (first remaining=%d)", len(slots), slots[0].Remaining)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/appointments/ -run 'TestOpenSlotsFor|TestComputeAvailability_FullyBookedExcluded' -v`
Expected: FAIL — `undefined: appointments.SlotCount`, `undefined: appointments.OpenSlotsFor`.

- [x] **Step 3: Implement**

Rewrite `internal/appointments/availability.go` from the `OpenSlot` type down:

```go
// OpenSlot is a bookable time offered by a schedule, in UTC.
type OpenSlot struct {
	StartAt   time.Time `json:"start_at"`
	EndAt     time.Time `json:"end_at"`
	Remaining int       `json:"remaining"` // capacity - already booked
}

// SlotCount is what a materialised slot knows about itself: how many are
// booked, and the capacity it was materialised with. A slot that has never been
// booked has no row and no entry; its capacity is the schedule's.
type SlotCount struct {
	Booked   int
	Capacity int
}

// ComputeAvailability returns bookable slots whose start is in [from, to).
//
// The instants come from offeredStarts; this only subtracts what is booked.
// A slot's remaining count is measured against the capacity it was materialised
// with, because that is the number the booking guard enforces.
func ComputeAvailability(
	sched *models.BookingSchedule,
	rules []models.BookingAvailabilityRule,
	exceptions []models.BookingAvailabilityException,
	counts map[time.Time]SlotCount,
	from, to, now time.Time,
) ([]OpenSlot, error) {
	starts, err := offeredStarts(sched, rules, exceptions, from, to, now)
	if err != nil {
		return nil, err
	}
	dur := time.Duration(sched.SlotDurationMin) * time.Minute
	out := make([]OpenSlot, 0, len(starts))
	for _, startUTC := range starts {
		capacity := sched.Capacity
		c, ok := counts[startUTC]
		if ok && c.Capacity > 0 {
			capacity = c.Capacity
		}
		if remaining := capacity - c.Booked; remaining > 0 {
			out = append(out, OpenSlot{StartAt: startUTC, EndAt: startUTC.Add(dur), Remaining: remaining})
		}
	}
	return out, nil
}

// offeredStarts returns, in order, every UTC instant the rules offer in
// [from, to), honouring lead time, the booking window, exceptions and DST.
// Capacity is not its concern.
//
// All wall-clock math happens in the schedule's timezone, then converts to UTC
// — this is what makes it DST-correct: a "09:00 Monday" rule stays 09:00 local
// across a DST boundary. A local time that does not exist (spring-forward gap)
// is skipped; a local time that exists twice (fall-back) is offered once, at
// the instant time.Date resolves it to, so a booking can never materialise two
// slots for one wall-clock time.
func offeredStarts(
	sched *models.BookingSchedule,
	rules []models.BookingAvailabilityRule,
	exceptions []models.BookingAvailabilityException,
	from, to, now time.Time,
) ([]time.Time, error) {
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		return nil, err
	}

	exByDate := map[string][]models.BookingAvailabilityException{}
	for _, e := range exceptions {
		exByDate[e.Date] = append(exByDate[e.Date], e)
	}

	earliest := now.Add(time.Duration(sched.LeadTimeMin) * time.Minute)
	latest := now.AddDate(0, 0, sched.WindowDays)

	var out []time.Time

	// Walk day by day in local time so weekday and DST are correct.
	start := from.In(loc)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	for day.Before(to) {
		dateKey := day.Format("2006-01-02")
		wd := int(day.Weekday())

		for _, rule := range rules {
			if rule.Weekday != wd {
				continue
			}
			for m := rule.StartMin; m+sched.SlotDurationMin <= rule.EndMin; m += sched.SlotDurationMin {
				local := time.Date(day.Year(), day.Month(), day.Day(), 0, m, 0, 0, loc)
				// Skip a wall time that does not exist on this date (DST gap):
				// time.Date normalizes it forward, changing the minute-of-day.
				if local.Hour()*60+local.Minute() != m {
					continue
				}

				startUTC := local.UTC()
				if startUTC.Before(from) || !startUTC.Before(to) {
					continue
				}
				if startUTC.Before(earliest) || startUTC.After(latest) {
					continue
				}
				if blockedByException(exByDate[dateKey], m, m+sched.SlotDurationMin) {
					continue
				}
				out = append(out, startUTC)
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return out, nil
}
```

Keep `blockedByException` as is.

Create `internal/appointments/open.go`:

```go
package appointments

import (
	"time"

	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
)

// OpenSlotsFor loads a schedule's rules, exceptions and materialised slot
// counts and returns its open slots in [from, to). This is the one place that
// knows how availability is assembled from rows; the HTTP layer and, later, the
// simulator both call it rather than repeating the queries.
func OpenSlotsFor(db *gorm.DB, sched *models.BookingSchedule, from, to, now time.Time) ([]OpenSlot, error) {
	var rules []models.BookingAvailabilityRule
	if err := db.Where("schedule_id = ?", sched.ID).Find(&rules).Error; err != nil {
		return nil, err
	}
	var exceptions []models.BookingAvailabilityException
	if err := db.Where("schedule_id = ?", sched.ID).Find(&exceptions).Error; err != nil {
		return nil, err
	}

	// Only slots somebody has booked exist as rows. The join is left, not inner:
	// a slot row exists from the moment it is first booked, but its inventory
	// row is what carries the count.
	type row struct {
		StartAt  time.Time
		Booked   int
		Capacity int
	}
	var rows []row
	if err := db.Raw(`SELECT bs.start_at AS start_at, COALESCE(bi.booked, 0) AS booked, bs.capacity AS capacity
		FROM booking_slots bs
		LEFT JOIN booking_inventories bi ON bi.slot_id = bs.id
		WHERE bs.schedule_id = ? AND bs.start_at >= ? AND bs.start_at < ?`,
		sched.ID, from.UTC(), to.UTC()).Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[time.Time]SlotCount, len(rows))
	for _, r := range rows {
		counts[r.StartAt.UTC()] = SlotCount{Booked: r.Booked, Capacity: r.Capacity}
	}

	return ComputeAvailability(sched, rules, exceptions, counts, from, to, now)
}
```

`internal/server/booking/api_availability.go` — replace the body of `availability` from `conn := db(ctx, r)` to the end with:

```go
		slots, err := appt.OpenSlotsFor(db(ctx, r), &sched, from, to, now)
		if err != nil {
			ctx.Logger.Error("compute availability", zap.Error(err))
			http.Error(w, "could not compute availability", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, slots)
```

and replace the body of `availabilityViews` after the `to` clamp with:

```go
	slots, err := appt.OpenSlotsFor(conn, &sched, from, to, now)
	if err != nil {
		return nil
	}
	return slots
```

The `models` import in `api_availability.go` stays: `availabilityViews` still takes a `models.BookingSchedule`. Delete the local `slotCount` struct types and the `gorm` import only if `availabilityViews`'s `conn *gorm.DB` parameter is the last use (it is not; keep it).

- [x] **Step 4: Run the tests**

Run: `go build ./... && go test ./internal/appointments/ ./internal/server/... ./test/ -run 'Availability|Appointments|Aurenor|OpenSlots' -count=1`
Expected: PASS.

- [x] **Step 5: Checkpoint**

`git status` shows `internal/appointments/availability.go`, `internal/appointments/open.go`, `internal/appointments/availability_test.go`, `internal/appointments/open_test.go`, `internal/server/booking/api_availability.go`. Owner commits.

---

### Task 8: One "is this time offered" implementation, inside the transaction (K13)

**Files:**
- Modify: `internal/appointments/book.go:35-78,144-189`
- Modify: `internal/appointments/checkout.go:25-35`
- Test: `internal/appointments/book_test.go`

**Interfaces:**
- Consumes: `offeredStarts` from Task 7.
- `validateOpen(tx *gorm.DB, sched, startUTC, now) error` now takes the transaction and is called inside it. `Book` is wrapped in `booking.WithWriteRetry`.

- [x] **Step 1: Write the failing test**

Append to `internal/appointments/book_test.go`:

```go
// TestBook_DSTFallBack_OneInstantPerWallTime: on the night clocks go back, the
// local hour 01:00–02:00 happens twice. The generator offers it once; the
// booking check must agree with the generator, or the second instant books a
// second, invisible, full-capacity slot for the same wall time.
//
// Europe/Lisbon leaves summer time on Sun 25 Oct 2026 at 02:00 WEST → 01:00 WET,
// so local 01:00 is both 00:00Z (WEST, first pass) and 01:00Z (WET, second).
func TestBook_DSTFallBack_OneInstantPerWallTime(t *testing.T) {
	db := bookDB(t)
	sched := &models.BookingSchedule{
		OwnerID: 1, Slug: "night-desk", Name: "Night Desk",
		Timezone: "Europe/Lisbon", SlotDurationMin: 60, Capacity: 1, WindowDays: 365,
	}
	if err := db.Create(sched).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.BookingAvailabilityRule{
		ScheduleID: sched.ID, Weekday: int(time.Sunday), StartMin: 0, EndMin: 3 * 60,
	}).Error; err != nil {
		t.Fatal(err)
	}
	now := at(t, "2026-10-01T00:00:00Z")
	first := at(t, "2026-10-25T00:00:00Z")  // 01:00 WEST
	second := at(t, "2026-10-25T01:00:00Z") // 01:00 WET

	_, errFirst := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: first, GuestName: "A"}, now)
	_, errSecond := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: second, GuestName: "B"}, now)

	okFirst, okSecond := errFirst == nil, errSecond == nil
	if okFirst == okSecond {
		t.Fatalf("exactly one instant of local 01:00 must be bookable; first=%v second=%v", errFirst, errSecond)
	}
	rejected := errSecond
	if okSecond {
		rejected = errFirst
	}
	if !errors.Is(rejected, appointments.ErrNotOpen) {
		t.Fatalf("the other instant must be ErrNotOpen, got %v", rejected)
	}
	var slots int64
	db.Model(&models.BookingSlot{}).Where("schedule_id = ?", sched.ID).Count(&slots)
	if slots != 1 {
		t.Fatalf("materialised %d slots for one wall time, want 1", slots)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/appointments/ -run TestBook_DSTFallBack -v`
Expected: FAIL — `exactly one instant … must be bookable; first=<nil> second=<nil>`.

- [x] **Step 3: Implement**

`book.go` — `Book` becomes:

```go
func Book(db *gorm.DB, in BookInput, now time.Time) (*models.BookingAppointment, error) {
	startUTC := in.StartAt.UTC()
	if !startUTC.After(now) {
		return nil, ErrPast
	}

	var appt *models.BookingAppointment
	err := booking.WithWriteRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			// Inside the transaction, so an exception inserted between the check
			// and the write is seen by the write.
			if err := validateOpen(tx, in.Schedule, startUTC, now); err != nil {
				return err
			}
			slot, err := ensureSlot(tx, in.Schedule, startUTC)
			if err != nil {
				return err
			}
			inv, err := booking.EnsureInventory(tx, booking.SlotOwner(slot.ID), slot.Capacity)
			if err != nil {
				return err
			}
			if err := booking.BookDirect(tx, inv.ID, 1); err != nil {
				return err
			}
			a := &models.BookingAppointment{
				ScheduleID:  in.Schedule.ID,
				SlotID:      slot.ID,
				UserID:      in.UserID,
				GuestName:   in.GuestName,
				GuestEmail:  in.GuestEmail,
				StartAt:     startUTC,
				EndAt:       slot.EndAt,
				Status:      "confirmed",
				CancelToken: booking.NewToken(),
			}
			if err := tx.Create(a).Error; err != nil {
				return err
			}
			appt = a
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return appt, nil
}
```

`validateOpen` becomes:

```go
// validateOpen checks that startUTC is one of the instants the schedule
// offers on that local day — the same generator the availability endpoint
// uses, so the two can never disagree about a DST edge, lead time, the window
// or an exception. Capacity is NOT checked here — the atomic guard in the
// caller handles that race-free.
func validateOpen(tx *gorm.DB, sched *models.BookingSchedule, startUTC, now time.Time) error {
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		return err
	}
	local := startUTC.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)

	var rules []models.BookingAvailabilityRule
	if err := tx.Where("schedule_id = ? AND weekday = ?", sched.ID, int(day.Weekday())).Find(&rules).Error; err != nil {
		return err
	}
	var exs []models.BookingAvailabilityException
	if err := tx.Where("schedule_id = ? AND date = ?", sched.ID, day.Format("2006-01-02")).Find(&exs).Error; err != nil {
		return err
	}

	starts, err := offeredStarts(sched, rules, exs, startUTC, startUTC.Add(time.Nanosecond), now)
	if err != nil {
		return err
	}
	for _, s := range starts {
		if s.Equal(startUTC) {
			return nil
		}
	}
	return ErrNotOpen
}
```

`checkout.go` `HoldSlot`: delete the `validateOpen(db, …)` call before the transaction (lines 29–31) and add, as the first statement inside `db.Transaction(func(tx *gorm.DB) error {`:

```go
			if err := validateOpen(tx, in.Schedule, startUTC, now); err != nil {
				return err
			}
```

- [x] **Step 4: Run the tests**

Run: `go test ./internal/appointments/ ./test/ -run 'Book|Appointment|Aurenor|Paid' -count=1`
Expected: PASS — including `TestBook_RejectsUnofferedTimes`, the exception tests and the HTTP paid flow.

- [x] **Step 5: Checkpoint**

`git status` shows `internal/appointments/book.go`, `internal/appointments/checkout.go`, `internal/appointments/book_test.go`. Owner commits.

---

### Task 9: A cancelled appointment refunds only an appointment order (K3)

**Files:**
- Modify: `internal/appointments/book.go` (`CancelByToken`, the order-item lookup)
- Test: `internal/appointments/book_test.go`

**Interfaces:** none change.

- [x] **Step 1: Write the failing test**

Append to `internal/appointments/book_test.go`:

```go
// TestCancelByToken_DoesNotRefundATicketOrder: fulfilled_id is the id of
// whatever a fulfiller created, in its own table. Appointment 7 and ticket 7 are
// different rows that share a number, so the refund lookup must be scoped to
// the order's domain.
func TestCancelByToken_DoesNotRefundATicketOrder(t *testing.T) {
	db := bookDB(t)
	sched := seedSchedule(t, db)
	now := at(t, "2026-03-01T00:00:00Z")

	appt, err := appointments.Book(db, appointments.BookInput{Schedule: sched, StartAt: at(t, "2026-03-02T09:00:00Z"), GuestName: "Ada"}, now)
	if err != nil {
		t.Fatalf("book: %v", err)
	}

	// A paid ticket order whose fulfilled id happens to equal the appointment's.
	inv, err := booking.InventoryForSlot(db, appt.SlotID)
	if err != nil {
		t.Fatal(err)
	}
	order := models.BookingOrder{Domain: "event", OwnerRef: "guest:z", Status: "paid", TotalCents: 5000, Currency: "USD"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	item := models.BookingOrderItem{OrderID: order.ID, InventoryID: inv.ID, Qty: 1, PriceCents: 5000, Status: "fulfilled", FulfilledID: &appt.ID}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if err := appointments.CancelByToken(db, appt.CancelToken); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	var got models.BookingOrder
	db.First(&got, order.ID)
	if got.Status != "paid" {
		t.Fatalf("ticket order status = %q after cancelling an appointment, want paid", got.Status)
	}
	var refunds int64
	db.Model(&models.BookingPayment{}).Where("order_id = ? AND kind = 'refund'", order.ID).Count(&refunds)
	if refunds != 0 {
		t.Fatalf("%d refund rows written against a ticket order", refunds)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/appointments/ -run TestCancelByToken_DoesNotRefundATicketOrder -v`
Expected: FAIL — `ticket order status = "refunded"`.

- [x] **Step 3: Implement**

In `CancelByToken`, replace the item lookup:

```go
		// If this appointment came from a paid order, refund it (mock refund) and
		// mark the order/item refunded. Free appointments have no order item.
		// fulfilled_id is only unique within a domain, so the order's domain is
		// part of the match.
		var item models.BookingOrderItem
		if err := tx.Joins("JOIN booking_orders o ON o.id = booking_order_items.order_id").
			Where("booking_order_items.fulfilled_id = ? AND booking_order_items.status = ? AND o.domain = ?",
				a.ID, "fulfilled", "appointment").
			Limit(1).Find(&item).Error; err != nil {
			return err
		}
```

- [x] **Step 4: Run the tests**

Run: `go test ./internal/appointments/ ./test/ -run 'Cancel|Refund|PaidAppointment' -count=1`
Expected: PASS, including `TestAppointmentsHTTP_PaidCancelRefund`.

- [x] **Step 5: Checkpoint**

`git status` shows `internal/appointments/book.go`, `internal/appointments/book_test.go`. Owner commits.

---

### Task 10: Full verification and the paper trail

**Files:**
- Modify: `docs/superpowers/specs/2026-09-02-evaluation-and-roadmap-v2.md` (row K21: step `A1` → `A3`; append "Done 2026-09-xx" to the A1 bullet in §3)
- Modify: `docs/worldbuilding/aurenor.md` (A1 bullet gets `*(done)*`)

- [x] **Step 1: Whole suite, race detector on**

Run: `export $(grep -E '^DATABASE_URL=' .env | xargs) && go build ./... && go vet ./... && go test -race -count=1 ./...`
Expected: every package `ok`, no `DATA RACE`.

- [x] **Step 2: Definition-of-done walk**

Confirm each by pointing at the test that proves it:
- capacity predicate on every write of `booked`: `BookDirect` (existing), `CommitTx` (`TestCommit_RefusesWhenCapacityWasCut`);
- every `RowsAffected`/`res.Error` read: `Block` (`TestBlock_RefusesOverCapacity`), release/reclaim (`TestRelease_RacesReclaimWithoutDoubleDecrement`);
- GA hold capped: `TestTheaterGARequestIsBounded`;
- offer claimable only by owner: `TestClaimOfferRefusesAnotherBuyer`;
- hold on a past showing refused: `TestHoldGARefusesStartedEvent`, `TestHoldSeatGroupRefusesStartedEvent`;
- one offered-time implementation: `TestBook_DSTFallBack_OneInstantPerWallTime`.

- [x] **Step 3: Docs**

In the evaluation doc, change row K21's last cell from `A1` to `A3` and add to its Fix cell: "Appointments' free path carries no owner reference; a per-guest cap there is rate limiting (A3 S7), not an engine rule." In §3 A1 and in `aurenor.md`'s A1 bullet, append `*(done)*`.

- [x] **Step 4: Checkpoint**

`git status` shows the two docs plus everything from Tasks 1–9. Owner commits the lot.
