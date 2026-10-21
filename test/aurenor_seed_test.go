package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/aurenor"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/server"
	"github.com/erancihan/clair/internal/testsupport"
	"github.com/erancihan/clair/internal/web/pages"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type seedCounts struct {
	users, schedules, rules, events, tiers, seats, inventory int64
}

func countSeed(db *gorm.DB) seedCounts {
	var c seedCounts
	db.Model(&models.User{}).Count(&c.users)
	db.Model(&models.BookingSchedule{}).Count(&c.schedules)
	db.Model(&models.BookingAvailabilityRule{}).Count(&c.rules)
	db.Model(&models.BookingEvent{}).Count(&c.events)
	db.Model(&models.BookingTicketTier{}).Count(&c.tiers)
	db.Model(&models.BookingSeat{}).Count(&c.seats)
	db.Model(&models.BookingInventory{}).Count(&c.inventory)
	return c
}

// TestSeedIsIdempotent runs the seed twice and asserts nothing changes the second
// time, and that the expected schedule count lands.
func TestSeedIsIdempotent(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if _, err := aurenor.Seed(db, now, "seed@aurenor.test"); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	first := countSeed(db)

	if _, err := aurenor.Seed(db, now, "seed@aurenor.test"); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	second := countSeed(db)

	if first != second {
		t.Fatalf("seed not idempotent: %+v then %+v", first, second)
	}
	if first.schedules != 16 {
		t.Errorf("want 16 schedules, got %d", first.schedules)
	}
	if first.events == 0 {
		t.Error("no events seeded")
	}
	if first.rules == 0 {
		t.Error("no availability rules seeded")
	}
}

// TestSeededEventsHaveFullInventory asserts every seeded inventory row starts
// empty (nothing booked/held/blocked) with a real capacity.
func TestSeededEventsHaveFullInventory(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if _, err := aurenor.Seed(db, time.Now(), "seed@aurenor.test"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var invs []models.BookingInventory
	db.Find(&invs)
	if len(invs) == 0 {
		t.Fatal("no inventory rows seeded")
	}
	for _, inv := range invs {
		if inv.Booked != 0 || inv.Held != 0 || inv.Blocked != 0 {
			t.Errorf("inventory %d not full: booked=%d held=%d blocked=%d", inv.ID, inv.Booked, inv.Held, inv.Blocked)
		}
		if inv.Capacity < 1 {
			t.Errorf("inventory %d capacity %d < 1", inv.ID, inv.Capacity)
		}
	}
}

// TestSeededSchedulesAreBookable drives a seeded schedule end-to-end over the real
// route table: real availability, then a real guest booking.
func TestSeededSchedulesAreBookable(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if _, err := aurenor.Seed(db, time.Now(), "seed@aurenor.test"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	handler := server.NewBackEnd(context.Background(), zap.NewNop(), nil, db).Routes()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	slug := "solvarre-general-cardiology"
	from := time.Now().UTC()
	to := from.AddDate(0, 0, 10)
	avURL := fmt.Sprintf("%s/appointments/schedules/%s/availability?from=%s&to=%s",
		ts.URL, slug, from.Format(time.RFC3339), to.Format(time.RFC3339))

	resp := doJSON(t, newJarClient(), "GET", avURL, nil)
	mustStatus(t, resp, http.StatusOK, "availability")
	var slots []struct {
		StartAt string `json:"start_at"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&slots)
	resp.Body.Close()
	if len(slots) == 0 {
		t.Fatalf("seeded schedule %s has no availability in the next 10 days", slug)
	}

	guest := newJarClient()
	resp = doJSON(t, guest, "POST", ts.URL+"/appointments/schedules/"+slug+"/book",
		map[string]any{"start_at": slots[0].StartAt, "guest_name": "Test Guest"})
	mustStatus(t, resp, http.StatusCreated, "book seeded slot")
	resp.Body.Close()
}

// TestSeedSlugsAlignWithVenueCatalog checks the seed catalog's venue slugs match
// the web Venue catalog the detail pages resolve against.
func TestSeedSlugsAlignWithVenueCatalog(t *testing.T) {
	for _, s := range aurenor.HospitalVenueSlugs() {
		if _, ok := pages.FindHospital(s); !ok {
			t.Errorf("hospital venue slug %q is not in the web Venue catalog", s)
		}
	}
	for _, s := range aurenor.TheaterVenueSlugs() {
		if _, ok := pages.FindTheater(s); !ok {
			t.Errorf("theater venue slug %q is not in the web Venue catalog", s)
		}
	}
}
