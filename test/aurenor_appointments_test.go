package test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/aurenor"
	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/server"
	"github.com/erancihan/clair/internal/testsupport"
	"github.com/erancihan/clair/internal/web/pages"
	"go.uber.org/zap"
)

// TestAurenorScheduleSlugsAreSeeded is the drift guard between the buttons and
// the rows: every schedule slug a page renders must be a schedule the seeder
// creates, or the visitor gets a 404 from a control that looks bookable.
func TestAurenorScheduleSlugsAreSeeded(t *testing.T) {
	seeded := map[string]bool{}
	for _, s := range aurenor.AllSchedules() {
		seeded[s.Slug] = true
	}

	rendered := pages.AppointmentScheduleSlugs()
	if len(rendered) == 0 {
		t.Fatal("no schedule slugs rendered by the Aurenor pages")
	}

	renderedSet := map[string]bool{}
	for _, slug := range rendered {
		renderedSet[slug] = true
		if !seeded[slug] {
			t.Errorf("page renders schedule slug %q, which the seeder never creates", slug)
		}
	}

	// And the other way: a seeded schedule no page offers is a service nobody
	// can reach.
	for slug := range seeded {
		if !renderedSet[slug] {
			t.Errorf("schedule %q is seeded but no page renders it", slug)
		}
	}
}

// TestAurenorServiceScreensBindTheirSchedule replaces the old expectation that
// every institution page embedded a booking component per service: they are
// lists now, and the screen behind each link is what books.
func TestAurenorServiceScreensBindTheirSchedule(t *testing.T) {
	srv := newRoutesServer(t)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	tests := map[string]string{
		"/aurenor/hospitals/solvarre-general/cardiology":  "solvarre-general-cardiology",
		"/aurenor/hospitals/gilt-coast-clinic/cardiology": "gilt-coast-clinic-cardiology",
		"/aurenor/consulate/tourist-visa-interview":       "consulate-tourist-visa-interview",
		"/aurenor/bureau/fingerprint-appointment":         "bureau-fingerprint-appointment",
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

// TestWiredAurenorPagesDropThePreviewBadge pins the badge to what is actually
// mock: the appointment pages are live, the theater is not.
func TestWiredAurenorPagesDropThePreviewBadge(t *testing.T) {
	srv := newRoutesServer(t)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	const badge = "Preview ·"

	tests := []struct {
		path      string
		wantBadge bool
	}{
		{path: "/aurenor/hospitals", wantBadge: false},
		{path: "/aurenor/hospitals/solvarre-general", wantBadge: false},
		{path: "/aurenor/consulate", wantBadge: false},
		{path: "/aurenor/bureau", wantBadge: false},
		{path: "/aurenor/theaters/grand", wantBadge: true},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := client.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("request to %s failed: %v", tc.path, err)
			}
			defer resp.Body.Close()

			has := strings.Contains(readBody(t, resp), badge)
			if has != tc.wantBadge {
				t.Errorf("%s: preview badge present=%v, want %v", tc.path, has, tc.wantBadge)
			}
		})
	}
}

// aurenorSlots reads the open slots of a seeded schedule over the next week the
// way the page's JS does.
func aurenorSlots(t *testing.T, ts *httptest.Server, slug string) []struct {
	StartAt   time.Time `json:"start_at"`
	EndAt     time.Time `json:"end_at"`
	Remaining int       `json:"remaining"`
} {
	t.Helper()

	from := time.Now().UTC()
	to := from.AddDate(0, 0, 7)
	url := fmt.Sprintf("%s/appointments/schedules/%s/availability?from=%s&to=%s",
		ts.URL, slug, from.Format(time.RFC3339), to.Format(time.RFC3339))

	resp := doJSON(t, newJarClient(), "GET", url, nil)
	mustStatus(t, resp, http.StatusOK, "availability for "+slug)
	defer resp.Body.Close()

	var slots []struct {
		StartAt   time.Time `json:"start_at"`
		EndAt     time.Time `json:"end_at"`
		Remaining int       `json:"remaining"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&slots); err != nil {
		t.Fatalf("decode availability: %v", err)
	}
	return slots
}

// TestAurenorGuestBooksAndCancels drives the flow the page offers a visitor with
// no account: real availability, a booking that consumes the slot, then a cancel
// with the issued token that gives the slot back.
func TestAurenorGuestBooksAndCancels(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if _, err := aurenor.Seed(db, time.Now(), "seed@aurenor.test"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ts := httptest.NewServer(server.NewBackEnd(context.Background(), zap.NewNop(), nil, db).Routes())
	t.Cleanup(ts.Close)

	// Capacity is 1 on a hospital department, so a booked slot leaves availability
	// entirely — which is the strongest signal the booking really landed.
	const slug = "solvarre-general-cardiology"

	before := aurenorSlots(t, ts, slug)
	if len(before) == 0 {
		t.Fatalf("%s offers no slots in the next week", slug)
	}
	chosen := before[0].StartAt

	guest := newJarClient()
	resp := doJSON(t, guest, "POST", ts.URL+"/appointments/schedules/"+slug+"/book",
		map[string]any{
			"start_at":    chosen.Format(time.RFC3339),
			"guest_name":  "Wren Aldis",
			"guest_email": "wren@aurenor.example",
		})
	mustStatus(t, resp, http.StatusCreated, "book")

	var appt struct {
		CancelToken string    `json:"cancel_token"`
		Status      string    `json:"status"`
		StartAt     time.Time `json:"start_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&appt); err != nil {
		t.Fatalf("decode appointment: %v", err)
	}
	resp.Body.Close()

	if appt.CancelToken == "" {
		t.Fatal("booking returned no cancel_token; a guest could never cancel")
	}
	if appt.Status != "confirmed" {
		t.Errorf("status %q, want confirmed", appt.Status)
	}

	if slotOffered(aurenorSlots(t, ts, slug), chosen) {
		t.Error("booked slot is still offered")
	}

	// A second booker racing for the same full slot is refused, not queued.
	other := newJarClient()
	resp = doJSON(t, other, "POST", ts.URL+"/appointments/schedules/"+slug+"/book",
		map[string]any{"start_at": chosen.Format(time.RFC3339), "guest_name": "Sable Voreth"})
	mustStatus(t, resp, http.StatusConflict, "double book")
	resp.Body.Close()

	cancelURL := ts.URL + "/appointments/cancel"
	resp = doJSON(t, guest, "POST", cancelURL, map[string]any{"token": appt.CancelToken})
	mustStatus(t, resp, http.StatusOK, "cancel")
	resp.Body.Close()

	if !slotOffered(aurenorSlots(t, ts, slug), chosen) {
		t.Error("cancelled slot was not returned to availability")
	}

	// The cancel link lands in an email and gets clicked twice.
	resp = doJSON(t, guest, "POST", cancelURL, map[string]any{"token": appt.CancelToken})
	mustStatus(t, resp, http.StatusOK, "second cancel")
	resp.Body.Close()
}

// TestAurenorBookingNeedsCSRF checks the page's own token is what authorizes the
// write: the same request without it is refused.
func TestAurenorBookingNeedsCSRF(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if _, err := aurenor.Seed(db, time.Now(), "seed@aurenor.test"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ts := httptest.NewServer(server.NewBackEnd(context.Background(), zap.NewNop(), nil, db).Routes())
	t.Cleanup(ts.Close)

	const slug = "bureau-fingerprint-appointment"
	slots := aurenorSlots(t, ts, slug)
	if len(slots) == 0 {
		t.Fatalf("%s offers no slots in the next week", slug)
	}

	body := strings.NewReader(fmt.Sprintf(`{"start_at":%q,"guest_name":"No Token"}`,
		slots[0].StartAt.Format(time.RFC3339)))
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/appointments/schedules/"+slug+"/book", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := newJarClient().Do(req)
	if err != nil {
		t.Fatalf("book without CSRF: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("book without a CSRF token: status %d, want 403", resp.StatusCode)
	}
}

func slotOffered(slots []struct {
	StartAt   time.Time `json:"start_at"`
	EndAt     time.Time `json:"end_at"`
	Remaining int       `json:"remaining"`
}, at time.Time) bool {
	for _, s := range slots {
		if s.StartAt.Equal(at) {
			return true
		}
	}
	return false
}

// TestAvailabilityStreamPushesBookings: a booking made in one browser reaches
// another browser's open schedule without it asking again.
func TestAvailabilityStreamPushesBookings(t *testing.T) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	if err := booking.InstallNotifyTriggers(db); err != nil {
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

	type openSlot struct {
		StartAt   time.Time `json:"start_at"`
		Remaining int       `json:"remaining"`
	}
	frames := make(chan []openSlot, 4)
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
			var slots []openSlot
			if err := json.Unmarshal([]byte(payload), &slots); err == nil {
				frames <- slots
			}
		}
	}()

	var chosen time.Time
	select {
	case first, ok := <-frames:
		if !ok {
			t.Fatal("stream closed without sending a frame")
		}
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

// availabilityServer seeds Aurenor and returns a server.
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
