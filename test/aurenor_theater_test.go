package test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/aurenor"
	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/server"
	"github.com/erancihan/clair/internal/testsupport"
	"go.uber.org/zap"
	gormlogger "gorm.io/gorm/logger"
)

// TestTheaterEventSlugsMatchTheSeed keeps the list a page renders and the list
// the seeder creates derived from the same source: a showing the page offers
// but the seeder never made would 404 on open.
func TestTheaterEventSlugsMatchTheSeed(t *testing.T) {
	seeded := map[string]bool{}
	for _, e := range aurenor.AllEvents(time.Now()) {
		seeded[e.Slug] = true
	}

	total := 0
	for _, venue := range aurenor.TheaterVenueSlugs() {
		slugs := aurenor.TheaterEventSlugs(venue, time.Now())
		if len(slugs) == 0 {
			t.Errorf("theater %q has no showings", venue)
		}
		for _, s := range slugs {
			if !seeded[s] {
				t.Errorf("theater %q offers showing %q, which the seeder never creates", venue, s)
			}
			total++
		}
	}

	if total != len(seeded) {
		t.Errorf("theaters offer %d showings but %d are seeded; some are unreachable", total, len(seeded))
	}
}

// TestTheaterPageIsLive asserts the detail page drives the real ticketing
// engine: the seat-map component bound to seeded showing slugs, and none of the
// inert mock chips it used to render.
func TestTheaterPageIsLive(t *testing.T) {
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

	if !strings.Contains(body, `x-data="theater(`) {
		t.Error("theater page renders no seat-map component")
	}
	for _, slug := range aurenor.TheaterEventSlugs("grand", time.Now()) {
		if !strings.Contains(body, slug) {
			t.Errorf("theater page does not embed showing %q", slug)
		}
	}
	// The mock board printed its showtimes into the HTML ("10:45"); real ones are
	// fetched per showing and formatted in the visitor's timezone.
	if strings.Contains(body, "10:45") {
		t.Error("theater page still renders hardcoded mock showtimes")
	}
	// The payment behind checkout is simulated, and the page has to say so.
	if !strings.Contains(body, "payment is mocked") {
		t.Error("theater page drops the notice that payment is simulated")
	}
	if !strings.Contains(body, `x-data="theaterTickets"`) {
		t.Error("theater page renders no my-tickets panel")
	}
}

// theaterEvent is the shape GET /events/{slug} answers with.
type theaterEvent struct {
	Event struct {
		ID          uint      `json:"id"`
		Slug        string    `json:"slug"`
		Name        string    `json:"name"`
		StartAt     time.Time `json:"start_at"`
		Status      string    `json:"status"`
		MaxPerBuyer int       `json:"max_per_buyer"`
	} `json:"event"`
	Tiers []struct {
		ID         uint   `json:"id"`
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		PriceCents int64  `json:"price_cents"`
		Remaining  int    `json:"remaining"`
	} `json:"tiers"`
}

type theaterSeat struct {
	ID      uint   `json:"id"`
	Label   string `json:"label"`
	Section string `json:"section"`
	RowName string `json:"row_name"`
	Number  int    `json:"number"`
	State   string `json:"state"`
}

func newTheaterServer(t *testing.T) *httptest.Server {
	t.Helper()

	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	// The seat stream is fed by a Postgres trigger, which is schema-scoped and so
	// has to be installed in this test's own schema the way Migrate installs it in
	// a real one.
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install notify triggers: %v", err)
	}
	if _, err := aurenor.Seed(db, time.Now(), "seed@aurenor.test"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ts := httptest.NewServer(server.NewBackEnd(context.Background(), zap.NewNop(), nil, db).Routes())
	t.Cleanup(ts.Close)
	return ts
}

func getEvent(t *testing.T, ts *httptest.Server, slug string) theaterEvent {
	t.Helper()

	resp := doJSON(t, newJarClient(), "GET", ts.URL+"/events/"+slug, nil)
	mustStatus(t, resp, http.StatusOK, "get event "+slug)
	defer resp.Body.Close()

	var ev theaterEvent
	if err := json.NewDecoder(resp.Body).Decode(&ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return ev
}

func getSeats(t *testing.T, ts *httptest.Server, slug string) []theaterSeat {
	t.Helper()

	resp := doJSON(t, newJarClient(), "GET", ts.URL+"/events/"+slug+"/seats", nil)
	mustStatus(t, resp, http.StatusOK, "seat map "+slug)
	defer resp.Body.Close()

	var seats []theaterSeat
	if err := json.NewDecoder(resp.Body).Decode(&seats); err != nil {
		t.Fatalf("decode seats: %v", err)
	}
	return seats
}

// firstSeated finds a seeded showing that sells assigned seats.
func firstSeated(t *testing.T, ts *httptest.Server) (string, theaterEvent) {
	t.Helper()

	for _, slug := range aurenor.TheaterEventSlugs("grand", time.Now()) {
		ev := getEvent(t, ts, slug)
		for _, tier := range ev.Tiers {
			if tier.Kind == "seated" {
				return slug, ev
			}
		}
	}
	t.Fatal("no seated showing seeded for the grand theater")
	return "", theaterEvent{}
}

// TestTheaterShowingReadsAreLive covers what the page renders before anybody
// clicks: real showings with real remaining counts, and a seat map that starts
// entirely free and ordered the way a map is drawn.
func TestTheaterShowingReadsAreLive(t *testing.T) {
	ts := newTheaterServer(t)

	slug, ev := firstSeated(t, ts)
	if ev.Event.Status != "scheduled" {
		t.Errorf("showing %s status %q, want scheduled", slug, ev.Event.Status)
	}
	if ev.Event.MaxPerBuyer <= 0 {
		t.Errorf("showing %s has no per-buyer cap; the seat picker has nothing to enforce", slug)
	}

	seats := getSeats(t, ts, slug)
	if len(seats) == 0 {
		t.Fatalf("showing %s has no seats", slug)
	}
	for _, s := range seats {
		if s.State != "free" {
			t.Errorf("freshly seeded seat %s is %q, want free", s.Label, s.State)
		}
	}

	var seatedRemaining int
	for _, tier := range ev.Tiers {
		if tier.Kind == "seated" {
			seatedRemaining = tier.Remaining
		}
	}
	if seatedRemaining != len(seats) {
		t.Errorf("seated tier reports %d remaining but the map has %d seats", seatedRemaining, len(seats))
	}
}

// TestTheaterSeatHold drives the flow the seat map offers a guest: hold real
// seats, watch them go unavailable to everybody else, and get refused when
// asking for more than the event allows.
func TestTheaterSeatHold(t *testing.T) {
	ts := newTheaterServer(t)
	slug, ev := firstSeated(t, ts)

	seats := getSeats(t, ts, slug)
	pick := []uint{seats[0].ID, seats[1].ID}

	guest := newJarClient()
	resp := doJSON(t, guest, "POST", ts.URL+"/events/"+slug+"/seats/hold",
		map[string]any{"seat_ids": pick, "guest_name": "Wren Aldis"})
	mustStatus(t, resp, http.StatusCreated, "hold seats")

	var held struct {
		HoldTokens []string `json:"hold_tokens"`
		TotalCents int64    `json:"total_cents"`
		Order      struct {
			ID     uint   `json:"id"`
			Status string `json:"status"`
		} `json:"order"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&held); err != nil {
		t.Fatalf("decode hold: %v", err)
	}
	resp.Body.Close()

	if len(held.HoldTokens) != len(pick) {
		t.Errorf("got %d hold tokens for %d seats", len(held.HoldTokens), len(pick))
	}
	if held.TotalCents <= 0 {
		t.Error("hold has no total to pay")
	}
	if held.Order.Status != "pending" {
		t.Errorf("order status %q, want pending", held.Order.Status)
	}

	// Another browser must see those seats as taken.
	after := map[uint]string{}
	for _, s := range getSeats(t, ts, slug) {
		after[s.ID] = s.State
	}
	for _, id := range pick {
		if after[id] != "held" {
			t.Errorf("seat %d reads %q to another viewer, want held", id, after[id])
		}
	}

	// The same seats cannot be held twice.
	resp = doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+slug+"/seats/hold",
		map[string]any{"seat_ids": pick, "guest_name": "Sable Voreth"})
	mustStatus(t, resp, http.StatusConflict, "re-hold held seats")
	resp.Body.Close()

	// One buyer cannot drain the salon.
	var free []uint
	for _, s := range getSeats(t, ts, slug) {
		if s.State == "free" {
			free = append(free, s.ID)
		}
	}
	overCap := ev.Event.MaxPerBuyer + 1
	if len(free) < overCap {
		t.Fatalf("only %d free seats left, need %d to test the cap", len(free), overCap)
	}
	resp = doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+slug+"/seats/hold",
		map[string]any{"seat_ids": free[:overCap], "guest_name": "Greedy"})
	mustStatus(t, resp, http.StatusConflict, "hold past the per-buyer cap")
	resp.Body.Close()
}

// TestTheaterSeatHoldRejectsForeignSeats checks a seat id from one showing
// cannot be held through another: the ids are caller-supplied primary keys.
func TestTheaterSeatHoldRejectsForeignSeats(t *testing.T) {
	ts := newTheaterServer(t)

	slugs := aurenor.TheaterEventSlugs("grand", time.Now())
	var seatedSlugs []string
	for _, slug := range slugs {
		if len(getSeats(t, ts, slug)) > 0 {
			seatedSlugs = append(seatedSlugs, slug)
		}
	}
	if len(seatedSlugs) < 2 {
		t.Skip("need two seated showings to cross them")
	}

	foreign := getSeats(t, ts, seatedSlugs[1])[0].ID
	resp := doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+seatedSlugs[0]+"/seats/hold",
		map[string]any{"seat_ids": []uint{foreign}, "guest_name": "Wrong Room"})
	mustStatus(t, resp, http.StatusBadRequest, "hold a seat from another showing")
	resp.Body.Close()
}

// TestTheaterGAHold covers the pooled path: no seat ids, a quantity, and an
// expiry the page counts down from.
func TestTheaterGAHold(t *testing.T) {
	ts := newTheaterServer(t)

	var slug string
	var tierID uint
	var before int
	for _, s := range aurenor.TheaterEventSlugs("grand", time.Now()) {
		ev := getEvent(t, ts, s)
		for _, tier := range ev.Tiers {
			if tier.Kind == "ga" {
				slug, tierID, before = s, tier.ID, tier.Remaining
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
		map[string]any{"qty": 2, "guest_name": "Wren Aldis"})
	mustStatus(t, resp, http.StatusCreated, "hold GA")

	var held struct {
		HoldToken  string    `json:"hold_token"`
		ExpiresAt  time.Time `json:"expires_at"`
		TotalCents int64     `json:"total_cents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&held); err != nil {
		t.Fatalf("decode GA hold: %v", err)
	}
	resp.Body.Close()

	if held.HoldToken == "" {
		t.Error("GA hold returned no token")
	}
	if !held.ExpiresAt.After(time.Now()) {
		t.Errorf("GA hold expires at %s, which is not in the future", held.ExpiresAt)
	}

	after := 0
	for _, tier := range getEvent(t, ts, slug).Tiers {
		if tier.ID == tierID {
			after = tier.Remaining
		}
	}
	if after != before-2 {
		t.Errorf("GA remaining went %d → %d, want a drop of 2", before, after)
	}
}

// TestTheaterHoldNeedsCSRF pins the hold routes behind the page's own token.
func TestTheaterHoldNeedsCSRF(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	seat := getSeats(t, ts, slug)[0].ID

	body := strings.NewReader(`{"seat_ids":[` + itoa(seat) + `],"guest_name":"No Token"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/events/"+slug+"/seats/hold", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := newJarClient().Do(req)
	if err != nil {
		t.Fatalf("hold without CSRF: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("hold without a CSRF token: status %d, want 403", resp.StatusCode)
	}
}

func itoa(u uint) string {
	return strconv.FormatUint(uint64(u), 10)
}

// TestTheaterSeatHoldReportsExpiry: the page counts down from this. Without it
// the countdown is guesswork, and it tells the buyer their seats are gone while
// the server still holds them.
func TestTheaterSeatHoldReportsExpiry(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	seats := getSeats(t, ts, slug)

	before := time.Now()
	resp := doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+slug+"/seats/hold",
		map[string]any{"seat_ids": []uint{seats[0].ID}, "guest_name": "Clock Watcher"})
	mustStatus(t, resp, http.StatusCreated, "hold")

	var held struct {
		ExpiresAt time.Time `json:"expires_at"`
		SeatCount int       `json:"seat_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&held); err != nil {
		t.Fatalf("decode hold: %v", err)
	}
	resp.Body.Close()

	if held.ExpiresAt.Before(before) {
		t.Errorf("hold expires at %s, before the request was even made", held.ExpiresAt)
	}
	if held.SeatCount != 1 {
		t.Errorf("hold covers %d seats, want 1", held.SeatCount)
	}
}

// TestTheaterDuplicateSeatIDsAreOneSeat: a double-submitted seat is one seat,
// not a seat that belongs to another event.
func TestTheaterDuplicateSeatIDsAreOneSeat(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	seat := getSeats(t, ts, slug)[0].ID

	resp := doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+slug+"/seats/hold",
		map[string]any{"seat_ids": []uint{seat, seat}, "guest_name": "Double Tap"})
	mustStatus(t, resp, http.StatusCreated, "hold the same seat twice in one request")

	var held struct {
		HoldTokens []string `json:"hold_tokens"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&held)
	resp.Body.Close()

	if len(held.HoldTokens) != 1 {
		t.Errorf("got %d holds for one seat named twice", len(held.HoldTokens))
	}
}

// TestTheaterSeatRequestIsBounded: an event with no per-buyer cap still cannot
// be emptied by one request.
func TestTheaterSeatRequestIsBounded(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)

	var ids []uint
	for _, s := range getSeats(t, ts, slug)[:20] {
		ids = append(ids, s.ID)
	}

	resp := doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+slug+"/seats/hold",
		map[string]any{"seat_ids": ids, "guest_name": "Whole Salon"})
	mustStatus(t, resp, http.StatusBadRequest, "hold 20 seats in one request")
	resp.Body.Close()
}

// TestTheaterReleaseHold covers giving seats back before the hold lapses, which
// until now only the reaper could do.
func TestTheaterReleaseHold(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	seats := getSeats(t, ts, slug)
	pick := []uint{seats[0].ID, seats[1].ID}

	guest := newJarClient()
	resp := doJSON(t, guest, "POST", ts.URL+"/events/"+slug+"/seats/hold",
		map[string]any{"seat_ids": pick, "guest_name": "Changed My Mind"})
	mustStatus(t, resp, http.StatusCreated, "hold")

	var held struct {
		HoldTokens []string `json:"hold_tokens"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&held)
	resp.Body.Close()

	// A stranger holding the tokens cannot free somebody else's seats.
	resp = doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+slug+"/holds/release",
		map[string]any{"hold_tokens": held.HoldTokens})
	mustStatus(t, resp, http.StatusOK, "release by a stranger")
	var stranger struct {
		Released int `json:"released"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&stranger)
	resp.Body.Close()
	if stranger.Released != 0 {
		t.Errorf("a stranger released %d holds they did not place", stranger.Released)
	}

	resp = doJSON(t, guest, "POST", ts.URL+"/events/"+slug+"/holds/release",
		map[string]any{"hold_tokens": held.HoldTokens})
	mustStatus(t, resp, http.StatusOK, "release")
	var freed struct {
		Released int `json:"released"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&freed)
	resp.Body.Close()
	if freed.Released != 2 {
		t.Errorf("released %d holds, want 2", freed.Released)
	}

	states := map[uint]string{}
	for _, s := range getSeats(t, ts, slug) {
		states[s.ID] = s.State
	}
	for _, id := range pick {
		if states[id] != "free" {
			t.Errorf("released seat %d reads %q, want free", id, states[id])
		}
	}

	// The button gets pressed twice.
	resp = doJSON(t, guest, "POST", ts.URL+"/events/"+slug+"/holds/release",
		map[string]any{"hold_tokens": held.HoldTokens})
	mustStatus(t, resp, http.StatusOK, "second release")
	resp.Body.Close()
}

// TestTheaterReleaseNeedsCSRF pins the new route into the guarded group.
func TestTheaterReleaseNeedsCSRF(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/events/"+slug+"/holds/release",
		strings.NewReader(`{"hold_tokens":["whatever"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := newJarClient().Do(req)
	if err != nil {
		t.Fatalf("release without CSRF: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("release without a CSRF token: status %d, want 403", resp.StatusCode)
	}
}

// TestTheaterShowingsCarryTheirTierShape guards the page's first paint against
// the seed: the board draws kind, price and the seat grid before any request
// answers, so those have to be the same numbers the seeder used.
func TestTheaterShowingsCarryTheirTierShape(t *testing.T) {
	seeded := map[string]aurenor.EventDef{}
	for _, e := range aurenor.AllEvents(time.Now()) {
		seeded[e.Slug] = e
	}

	for _, venue := range aurenor.TheaterVenueSlugs() {
		for _, s := range aurenor.TheaterShowings(venue, time.Now()) {
			def, ok := seeded[s.Slug]
			if !ok {
				t.Errorf("showing %q is not seeded", s.Slug)
				continue
			}
			if s.Kind != def.Tier.Kind {
				t.Errorf("%s: page says kind %q, seed says %q", s.Slug, s.Kind, def.Tier.Kind)
			}
			if s.PriceCents != def.Tier.PriceCents {
				t.Errorf("%s: page says %d cents, seed says %d", s.Slug, s.PriceCents, def.Tier.PriceCents)
			}
			if s.Kind == "seated" && (s.Rows != def.Tier.Rows || s.Cols != def.Tier.Cols) {
				t.Errorf("%s: page draws a %dx%d grid, seed builds %dx%d",
					s.Slug, s.Rows, s.Cols, def.Tier.Rows, def.Tier.Cols)
			}
		}
	}
}

// TestTheaterSeatStreamPushesChanges is the contract that replaced polling: a
// hold placed by one visitor reaches another visitor's open seat map without
// them asking again.
func TestTheaterSeatStreamPushesChanges(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	seats := getSeats(t, ts, slug)
	target := seats[0].ID

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events/"+slug+"/seats/stream", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("stream content-type %q", ct)
	}

	frames := make(chan []theaterSeat, 4)
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
			var seats []theaterSeat
			if err := json.Unmarshal([]byte(payload), &seats); err == nil {
				frames <- seats
			}
		}
	}()

	// The first frame is the current truth, so a subscriber knows where it stands
	// without a separate fetch.
	select {
	case first := <-frames:
		if len(first) != len(seats) {
			t.Errorf("first frame has %d seats, want %d", len(first), len(seats))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream sent no opening snapshot")
	}

	// Somebody else takes a seat.
	hold := doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+slug+"/seats/hold",
		map[string]any{"seat_ids": []uint{target}, "guest_name": "Other Browser"})
	mustStatus(t, hold, http.StatusCreated, "hold from another browser")
	hold.Body.Close()

	deadline := time.After(15 * time.Second)
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatal("stream closed before the change arrived")
			}
			for _, s := range frame {
				if s.ID == target && s.State == "held" {
					return
				}
			}
		case <-deadline:
			t.Fatal("held seat never arrived on the stream")
		}
	}
}

// TestTheaterSeatStreamUnknownEvent keeps the stream honest about what exists.
func TestTheaterSeatStreamUnknownEvent(t *testing.T) {
	ts := newTheaterServer(t)

	resp := doJSON(t, newJarClient(), "GET", ts.URL+"/events/no-such-showing/seats/stream", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("stream for an unknown event: status %d, want 404", resp.StatusCode)
	}
}

// TestEventSummaryAnswersTheWholeBoard: the board asks once for every showing it
// lists, instead of once per showing.
func TestEventSummaryAnswersTheWholeBoard(t *testing.T) {
	ts := newTheaterServer(t)
	slugs := aurenor.TheaterEventSlugs("grand", time.Now())

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
	known := aurenor.TheaterEventSlugs("grand", time.Now())[0]

	resp := doJSON(t, newJarClient(), "GET",
		ts.URL+"/events/summary?slugs="+known+",no-such-showing", nil)
	mustStatus(t, resp, http.StatusOK, "summary with an unknown slug")
	defer resp.Body.Close()

	var rows []struct {
		Slug string `json:"slug"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	if len(rows) != 1 || rows[0].Slug != known {
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

// TestEventSummaryStreamPushesCounts: the board's counts go live, over one
// connection covering every showing it lists.
func TestEventSummaryStreamPushesCounts(t *testing.T) {
	ts := newTheaterServer(t)
	slugs := aurenor.TheaterEventSlugs("grand", time.Now())
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
	case first, ok := <-frames:
		if !ok {
			t.Fatal("stream closed without sending a frame")
		}
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

// TestTheaterBoardIsFullWidthWithDateTabs pins the board's shape: one date
// strip, no two-column grid of half-empty cards, one request for the lot.
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

// TestTheaterGARequestIsBounded is the pooled twin of
// TestTheaterSeatRequestIsBounded: an event with no per-buyer cap still cannot
// have its whole pool drained by one request.
func TestTheaterGARequestIsBounded(t *testing.T) {
	ts := newTheaterServer(t)

	var slug string
	var tierID uint
	for _, s := range aurenor.TheaterEventSlugs("grand", time.Now()) {
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

// countingLogger counts SQL statements that touch a table, so a test can say
// "this change cost one lookup, not one per showing".
type countingLogger struct {
	gormlogger.Interface
	table string
	n     int64
	mu    sync.Mutex
	seen  []string
}

func (l *countingLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	if strings.Contains(sql, l.table) {
		atomic.AddInt64(&l.n, 1)
		l.mu.Lock()
		l.seen = append(l.seen, sql)
		l.mu.Unlock()
	}
	l.Interface.Trace(ctx, begin, fc, err)
}

func (l *countingLogger) reset() {
	atomic.StoreInt64(&l.n, 0)
	l.mu.Lock()
	l.seen = nil
	l.mu.Unlock()
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

	slugs := aurenor.TheaterEventSlugs("grand", time.Now())
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

	slug, _ := firstSeated(t, ts)
	seat := getSeats(t, ts, slug)[0].ID

	counter.reset()
	hold := doJSON(t, newJarClient(), "POST", ts.URL+"/events/"+slug+"/seats/hold",
		map[string]any{"seat_ids": []uint{seat}, "guest_name": "Counter"})
	mustStatus(t, hold, http.StatusCreated, "hold")
	hold.Body.Close()

	select {
	case <-frames:
	case <-time.After(10 * time.Second):
		t.Fatal("no frame after the hold")
	}
	// The hold's own handler resolves its slug once, and the changed showing's
	// summary reloads once. Everything else on the board must come from cache.
	if got := atomic.LoadInt64(&counter.n); got > 3 {
		counter.mu.Lock()
		defer counter.mu.Unlock()
		t.Fatalf("one change cost %d booking_events lookups across %d showings; want at most 3:\n  %s",
			got, len(slugs), strings.Join(counter.seen, "\n  "))
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
		`EventSource.CLOSED`,            // fallback only on a real refusal
		`/events/summary/stream?slugs=`, // the GA showing's one connection
		`/seats/stream`,                 // the seated showing's one connection
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
	if !strings.Contains(body, "try { window.localStorage.setItem(window.AURENOR_HOLDS_KEY") {
		t.Error("saving a hold to storage is not guarded; a quota error would report a successful hold as a failure")
	}
}
