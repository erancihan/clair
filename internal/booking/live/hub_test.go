package live_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/booking/live"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/testsupport"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// TestHubDeliversInventoryChanges is the whole point of the package: a change
// made through one connection reaches a subscriber on another.
func TestHubDeliversInventoryChanges(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install triggers: %v", err)
	}

	tier := models.BookingTicketTier{Name: "GA", Kind: "ga", PriceCents: 1000, EventID: 7}
	if err := db.Create(&tier).Error; err != nil {
		t.Fatalf("create tier: %v", err)
	}
	inv := models.BookingInventory{Capacity: 5, TierID: &tier.ID}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("create inventory: %v", err)
	}

	hub := live.NewHub(os.Getenv("DATABASE_URL"), zap.NewNop())
	signals, release := hub.Subscribe("event:7")
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !hub.Ready(ctx, 5*time.Second) {
		t.Fatal("listener never connected")
	}

	if _, err := booking.HoldUnits(db, inv.ID, 1, "guest:hubtest", nil, time.Minute, time.Now()); err != nil {
		t.Fatalf("hold: %v", err)
	}

	select {
	case <-signals:
	case <-time.After(8 * time.Second):
		t.Fatal("no signal for a change to the subscribed event")
	}
}

// TestHubReleasesItsConnection: nobody watching, nothing held open.
func TestHubReleasesItsConnection(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set")
	}

	hub := live.NewHub(os.Getenv("DATABASE_URL"), zap.NewNop())
	_, release := hub.Subscribe("event:1")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !hub.Ready(ctx, 5*time.Second) {
		t.Fatal("listener never connected")
	}

	release()

	deadline := time.Now().Add(3 * time.Second)
	for hub.Ready(context.Background(), 0) {
		if time.Now().After(deadline) {
			t.Fatal("listener still connected after the last subscriber left")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHubSubscriberIsIsolated: a change to one event does not wake watchers of
// another.
func TestHubSubscriberIsIsolated(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install triggers: %v", err)
	}

	tier := models.BookingTicketTier{Name: "GA", Kind: "ga", PriceCents: 1000, EventID: 11}
	if err := db.Create(&tier).Error; err != nil {
		t.Fatalf("create tier: %v", err)
	}
	inv := models.BookingInventory{Capacity: 5, TierID: &tier.ID}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("create inventory: %v", err)
	}

	hub := live.NewHub(os.Getenv("DATABASE_URL"), zap.NewNop())
	other, release := hub.Subscribe("event:999999")
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !hub.Ready(ctx, 5*time.Second) {
		t.Fatal("listener never connected")
	}

	if _, err := booking.HoldUnits(db, inv.ID, 1, "guest:isolation", nil, time.Minute, time.Now()); err != nil {
		t.Fatalf("hold: %v", err)
	}

	select {
	case <-other:
		t.Fatal("a watcher of another event was woken")
	case <-time.After(1500 * time.Millisecond):
	}
}

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
