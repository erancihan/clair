package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
)

type tierResp struct {
	ID        uint `json:"id"`
	Remaining int  `json:"remaining"`
}

func getEventTiers(t *testing.T, c *http.Client, ts, slug string) []tierResp {
	t.Helper()
	resp := doJSON(t, c, "GET", ts+"/events/"+slug, nil)
	mustStatus(t, resp, http.StatusOK, "get event")
	var body struct {
		Tiers []tierResp `json:"tiers"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	return body.Tiers
}

func TestTicketingHTTP_GA(t *testing.T) {
	ts, host, db := newApptServer(t)
	registerAndLogin(t, ts, host, "ga_host@example.com")

	resp := doJSON(t, host, "POST", ts.URL+"/events", map[string]any{
		"slug": "concert", "name": "Concert", "start_at": time.Now().Add(48 * time.Hour).Format(time.RFC3339),
		"tiers": []map[string]any{{"name": "GA", "kind": "ga", "price_cents": 8000, "capacity": 3}},
	})
	mustStatus(t, resp, http.StatusCreated, "create event")
	resp.Body.Close()

	tiers := getEventTiers(t, host, ts.URL, "concert")
	if len(tiers) != 1 || tiers[0].Remaining != 3 {
		t.Fatalf("want 1 tier, remaining 3, got %+v", tiers)
	}
	tierID := tiers[0].ID

	// Guest holds 2 GA tickets.
	guest := newJarClient()
	resp = doJSON(t, guest, "POST", fmt.Sprintf("%s/events/concert/tiers/%d/hold", ts.URL, tierID),
		map[string]any{"qty": 2, "guest_name": "Ada"})
	mustStatus(t, resp, http.StatusCreated, "hold GA")
	var held struct {
		Order struct {
			ID uint `json:"id"`
		} `json:"order"`
	}
	json.NewDecoder(resp.Body).Decode(&held)
	resp.Body.Close()

	if r := getEventTiers(t, host, ts.URL, "concert")[0].Remaining; r != 1 {
		t.Fatalf("after holding 2, remaining want 1, got %d", r)
	}

	// Capture → 2 tickets issued.
	resp = capturePayment(t, ts, held.Order.ID, "ga-1")
	mustStatus(t, resp, http.StatusOK, "capture")
	resp.Body.Close()

	var tickets int64
	db.Model(&models.BookingTicket{}).Count(&tickets)
	if tickets != 2 {
		t.Fatalf("want 2 tickets, got %d", tickets)
	}
	// Over-hold the remaining pool guardrail: ask for 5 → 409.
	resp = doJSON(t, newJarClient(), "POST", fmt.Sprintf("%s/events/concert/tiers/%d/hold", ts.URL, tierID),
		map[string]any{"qty": 5})
	mustStatus(t, resp, http.StatusConflict, "over-hold")
	resp.Body.Close()
}

func seatMap(t *testing.T, c *http.Client, ts, slug string) []struct {
	ID    uint   `json:"id"`
	Label string `json:"label"`
	State string `json:"state"`
} {
	t.Helper()
	resp := doJSON(t, c, "GET", ts+"/events/"+slug+"/seats", nil)
	mustStatus(t, resp, http.StatusOK, "seat map")
	var seats []struct {
		ID    uint   `json:"id"`
		Label string `json:"label"`
		State string `json:"state"`
	}
	json.NewDecoder(resp.Body).Decode(&seats)
	resp.Body.Close()
	return seats
}

func TestTicketingHTTP_Seated(t *testing.T) {
	ts, host, db := newApptServer(t)
	registerAndLogin(t, ts, host, "seat_host@example.com")

	resp := doJSON(t, host, "POST", ts.URL+"/events", map[string]any{
		"slug": "cinema", "name": "Cinema", "start_at": time.Now().Add(48 * time.Hour).Format(time.RFC3339),
		"tiers": []map[string]any{{
			"name": "Orchestra", "kind": "seated", "price_cents": 12000,
			"seats": []map[string]any{
				{"label": "A-1", "section": "Orchestra", "row_name": "A", "number": 1},
				{"label": "A-2", "section": "Orchestra", "row_name": "A", "number": 2},
				{"label": "A-3", "section": "Orchestra", "row_name": "A", "number": 3},
			},
		}},
	})
	mustStatus(t, resp, http.StatusCreated, "create seated event")
	resp.Body.Close()

	seats := seatMap(t, host, ts.URL, "cinema")
	if len(seats) != 3 {
		t.Fatalf("want 3 seats, got %d", len(seats))
	}
	byLabel := map[string]uint{}
	for _, s := range seats {
		if s.State != "free" {
			t.Fatalf("seat %s state=%s, want free", s.Label, s.State)
		}
		byLabel[s.Label] = s.ID
	}

	// Guest 1 holds A-1 + A-2.
	g1 := newJarClient()
	resp = doJSON(t, g1, "POST", ts.URL+"/events/cinema/seats/hold",
		map[string]any{"seat_ids": []uint{byLabel["A-1"], byLabel["A-2"]}, "guest_name": "Ada"})
	mustStatus(t, resp, http.StatusCreated, "hold seats")
	var held struct {
		Order struct {
			ID uint `json:"id"`
		} `json:"order"`
	}
	json.NewDecoder(resp.Body).Decode(&held)
	resp.Body.Close()

	// Guest 2 wants A-2 + A-3 → all-or-nothing conflict (A-2 taken); A-3 stays free.
	resp = doJSON(t, newJarClient(), "POST", ts.URL+"/events/cinema/seats/hold",
		map[string]any{"seat_ids": []uint{byLabel["A-2"], byLabel["A-3"]}})
	mustStatus(t, resp, http.StatusConflict, "contended seat hold")
	resp.Body.Close()

	states := map[string]string{}
	for _, s := range seatMap(t, host, ts.URL, "cinema") {
		states[s.Label] = s.State
	}
	if states["A-1"] != "held" || states["A-2"] != "held" || states["A-3"] != "free" {
		t.Fatalf("unexpected seat states: %+v", states)
	}

	// Capture guest 1 → A-1, A-2 booked; 2 seated tickets.
	resp = capturePayment(t, ts, held.Order.ID, "seat-1")
	mustStatus(t, resp, http.StatusOK, "capture")
	resp.Body.Close()

	states = map[string]string{}
	for _, s := range seatMap(t, host, ts.URL, "cinema") {
		states[s.Label] = s.State
	}
	if states["A-1"] != "booked" || states["A-2"] != "booked" || states["A-3"] != "free" {
		t.Fatalf("after capture states: %+v", states)
	}
	var tickets int64
	db.Model(&models.BookingTicket{}).Where("seat_id IS NOT NULL").Count(&tickets)
	if tickets != 2 {
		t.Fatalf("want 2 seated tickets, got %d", tickets)
	}
}

func TestTicketingHTTP_PerBuyerLimit(t *testing.T) {
	ts, host, _ := newApptServer(t)
	registerAndLogin(t, ts, host, "limit_host@example.com")

	resp := doJSON(t, host, "POST", ts.URL+"/events", map[string]any{
		"slug": "limited", "name": "Limited", "start_at": time.Now().Add(48 * time.Hour).Format(time.RFC3339),
		"max_per_buyer": 2,
		"tiers":         []map[string]any{{"name": "GA", "kind": "ga", "price_cents": 8000, "capacity": 10}},
	})
	mustStatus(t, resp, http.StatusCreated, "create event")
	resp.Body.Close()
	tierID := getEventTiers(t, host, ts.URL, "limited")[0].ID

	guest := newJarClient()
	// qty 2 is fine.
	resp = doJSON(t, guest, "POST", fmt.Sprintf("%s/events/limited/tiers/%d/hold", ts.URL, tierID), map[string]any{"qty": 2})
	mustStatus(t, resp, http.StatusCreated, "hold 2")
	resp.Body.Close()
	// The same buyer asking for 1 more exceeds the cap of 2.
	resp = doJSON(t, guest, "POST", fmt.Sprintf("%s/events/limited/tiers/%d/hold", ts.URL, tierID), map[string]any{"qty": 1})
	mustStatus(t, resp, http.StatusConflict, "hold beyond limit")
	resp.Body.Close()
}

func TestTicketingHTTP_WaitlistClaim(t *testing.T) {
	ts, host, db := newApptServer(t)
	registerAndLogin(t, ts, host, "wl_host@example.com")

	resp := doJSON(t, host, "POST", ts.URL+"/events", map[string]any{
		"slug": "soldout", "name": "SoldOut", "start_at": time.Now().Add(48 * time.Hour).Format(time.RFC3339),
		"tiers": []map[string]any{{"name": "GA", "kind": "ga", "price_cents": 8000, "capacity": 1}},
	})
	mustStatus(t, resp, http.StatusCreated, "create event")
	resp.Body.Close()
	tierID := getEventTiers(t, host, ts.URL, "soldout")[0].ID

	// Buyer 1 grabs the only unit.
	g1 := newJarClient()
	resp = doJSON(t, g1, "POST", fmt.Sprintf("%s/events/soldout/tiers/%d/hold", ts.URL, tierID), map[string]any{"qty": 1})
	mustStatus(t, resp, http.StatusCreated, "hold")
	resp.Body.Close()

	// Buyer 2 joins the waitlist.
	g2 := newJarClient()
	resp = doJSON(t, g2, "POST", fmt.Sprintf("%s/events/soldout/tiers/%d/waitlist", ts.URL, tierID), map[string]any{"guest_name": "Bob"})
	mustStatus(t, resp, http.StatusCreated, "join waitlist")
	var wl struct {
		Position int `json:"position"`
	}
	json.NewDecoder(resp.Body).Decode(&wl)
	resp.Body.Close()
	if wl.Position != 1 {
		t.Fatalf("waitlist position=%d, want 1", wl.Position)
	}

	// Simulate the reaper firing after buyer 1's hold expires → offered to B.
	if _, err := booking.ReapExpiredHolds(db, time.Now().Add(11*time.Minute)); err != nil {
		t.Fatalf("reap: %v", err)
	}
	var entry models.BookingWaitlistEntry
	if res := db.Where("status = ?", "offered").Limit(1).Find(&entry); res.RowsAffected != 1 || entry.OfferToken == nil {
		t.Fatalf("no waitlist offer created (rows=%d)", res.RowsAffected)
	}

	// Buyer 2 claims the offer, then captures.
	resp = doJSON(t, g2, "POST", ts.URL+"/events/soldout/waitlist/claim", map[string]string{"token": *entry.OfferToken, "guest_name": "Bob"})
	mustStatus(t, resp, http.StatusCreated, "claim")
	var claimed struct {
		Order struct {
			ID uint `json:"id"`
		} `json:"order"`
	}
	json.NewDecoder(resp.Body).Decode(&claimed)
	resp.Body.Close()

	resp = capturePayment(t, ts, claimed.Order.ID, "wl-http-1")
	mustStatus(t, resp, http.StatusOK, "capture")
	resp.Body.Close()

	var tickets int64
	db.Model(&models.BookingTicket{}).Count(&tickets)
	if tickets != 1 {
		t.Fatalf("want 1 ticket after claim+capture, got %d", tickets)
	}
}
