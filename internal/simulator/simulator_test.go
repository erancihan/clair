package simulator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/aurenor"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/simulator"
	"github.com/erancihan/clair/internal/testsupport"
	"gorm.io/gorm"
)

func TestGuard(t *testing.T) {
	cases := []struct {
		dsn, env string
		allow    bool
		ok       bool
	}{
		{"postgres://clair:clair@localhost:5432/clair?sslmode=disable", "development", false, true},
		{"postgres://clair:clair@postgres:5432/clair", "", false, true},
		{"host=127.0.0.1 port=5432 dbname=clair", "", false, true},
		{"postgres://u:p@db.example.net:5432/clair?sslmode=require", "", false, false},
		{"host=db.example.net dbname=clair", "", false, false},
		{"postgres://clair:clair@localhost:5432/clair", "production", false, false},
		{"postgres://u:p@db.example.net:5432/clair", "production", true, true},
		{"not a dsn", "", false, false},
	}
	for _, c := range cases {
		err := simulator.Guard(c.dsn, c.env, c.allow)
		if (err == nil) != c.ok {
			t.Errorf("Guard(%q, %q, %v) = %v, want ok=%v", c.dsn, c.env, c.allow, err, c.ok)
		}
		if err != nil && !errors.Is(err, simulator.ErrProductionDSN) {
			t.Errorf("Guard(%q) error %v is not ErrProductionDSN", c.dsn, err)
		}
	}
}

func seeded(t *testing.T) (*gorm.DB, time.Time) {
	t.Helper()
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	now := time.Now()
	if _, err := aurenor.Seed(db, now, "seed@aurenor.test"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db, now
}

// TestTickFillsTowardTheTarget: repeated ticks book seats, tickets and
// appointments, all under the sim namespace, and no showing goes past the
// target its curve allows.
func TestTickFillsTowardTheTarget(t *testing.T) {
	db, now := seeded(t)
	cfg := simulator.DefaultConfig()
	cfg.CancelRate, cfg.AbandonRate = 0, 0
	cfg.MaxUnitsPerShowing = 50
	sim := simulator.New(db, cfg, 1)

	var total simulator.Report
	for i := 0; i < 4; i++ {
		rep, err := sim.Tick(context.Background(), now)
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		total.TicketsSold += rep.TicketsSold
		total.AppointmentsBooked += rep.AppointmentsBooked
	}
	if total.TicketsSold == 0 || total.AppointmentsBooked == 0 {
		t.Fatalf("four ticks did nothing: %+v", total)
	}

	var foreign int64
	db.Model(&models.BookingOrder{}).Where("owner_ref NOT LIKE ?", simulator.OwnerPrefix+"%").Count(&foreign)
	if foreign != 0 {
		t.Errorf("%d orders outside the sim namespace", foreign)
	}
	var appts []models.BookingAppointment
	db.Find(&appts)
	for _, a := range appts {
		if !strings.HasPrefix(a.GuestEmail, simulator.OwnerPrefix) {
			t.Errorf("appointment %d not marked as simulated: %q", a.ID, a.GuestEmail)
		}
	}

	// Occupancy per showing stays under the target (the curve is at most the
	// target, and a tick never overshoots its gap).
	var events []models.BookingEvent
	db.Find(&events)
	for _, ev := range events {
		var sums struct{ Taken, Capacity int }
		db.Raw(`SELECT COALESCE(SUM(i.booked + i.held), 0) AS taken, COALESCE(SUM(i.capacity), 0) AS capacity
			FROM booking_inventories i LEFT JOIN booking_seats st ON st.id = i.seat_id
			JOIN booking_ticket_tiers t ON t.id = COALESCE(i.tier_id, st.tier_id)
			WHERE t.event_id = ?`, ev.ID).Scan(&sums)
		if sums.Capacity > 0 && float64(sums.Taken)/float64(sums.Capacity) > cfg.TargetOccupancy+0.01 {
			t.Errorf("%s at %d/%d, over the %.0f%% target", ev.Slug, sums.Taken, sums.Capacity, cfg.TargetOccupancy*100)
		}
	}

	// Every sim ticket order is paid and has tickets.
	var unpaid int64
	db.Model(&models.BookingOrder{}).Where("domain = 'event' AND status <> 'paid'").Count(&unpaid)
	if unpaid != 0 {
		t.Errorf("%d event orders not paid with no abandon rate", unpaid)
	}
}

// TestTickChurns: with certain churn the simulator cancels its own orders and
// appointments and leaves abandoned holds for the reaper.
func TestTickChurns(t *testing.T) {
	db, now := seeded(t)
	cfg := simulator.DefaultConfig()
	cfg.CancelRate, cfg.AbandonRate = 0, 0
	cfg.MaxUnitsPerShowing = 50
	sim := simulator.New(db, cfg, 2)
	for i := 0; i < 3; i++ {
		if _, err := sim.Tick(context.Background(), now); err != nil {
			t.Fatalf("fill tick: %v", err)
		}
	}

	cfg.CancelRate, cfg.AbandonRate = 1, 1
	churn := simulator.New(db, cfg, 3)
	rep, err := churn.Tick(context.Background(), now)
	if err != nil {
		t.Fatalf("churn tick: %v", err)
	}
	if rep.OrdersCancelled == 0 || rep.AppointmentsDropped == 0 || rep.HoldsAbandoned == 0 {
		t.Fatalf("churn tick %+v, want cancels, drops and abandoned holds", rep)
	}

	var refunded int64
	db.Model(&models.BookingOrder{}).Where("status = 'refunded'").Count(&refunded)
	if refunded == 0 {
		t.Error("no refunded orders after a cancel")
	}
	var cancelled int64
	db.Model(&models.BookingAppointment{}).Where("status = 'cancelled'").Count(&cancelled)
	if cancelled == 0 {
		t.Error("no cancelled appointments after a drop")
	}
	var abandoned int64
	db.Model(&models.BookingHold{}).Where("status = 'active' AND expires_at <= ?", now.Add(cfg.AbandonTTL)).Count(&abandoned)
	if abandoned == 0 {
		t.Error("no short-lived abandoned holds")
	}
}

// TestRunnerIsASingleRunner: a second runner on the same database stands by
// while the first holds the lock, and takes over once it lets go.
func TestRunnerIsASingleRunner(t *testing.T) {
	db, now := seeded(t)
	cfg := simulator.DefaultConfig()
	cfg.CancelRate, cfg.AbandonRate = 0, 0

	first := simulator.NewRunner(db, simulator.New(db, cfg, 4))
	second := simulator.NewRunner(db, simulator.New(db, cfg, 5))
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })

	ctx := context.Background()
	if _, ran, err := first.Tick(ctx, now); err != nil || !ran {
		t.Fatalf("first runner: ran=%v err=%v", ran, err)
	}
	if _, ran, err := second.Tick(ctx, now); err != nil || ran {
		t.Fatalf("second runner while the first holds the lock: ran=%v err=%v", ran, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	if _, ran, err := second.Tick(ctx, now); err != nil || !ran {
		t.Fatalf("second runner after the first let go: ran=%v err=%v", ran, err)
	}
}
