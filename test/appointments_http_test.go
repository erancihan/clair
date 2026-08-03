package test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/server"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	"github.com/erancihan/clair/internal/server/booking"
	"github.com/erancihan/clair/internal/testsupport"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// newApptServer boots the real Routes() over an isolated Postgres schema and
// returns an httptest server, a cookie-jar client, and the DB (for assertions).
// Skips when DATABASE_URL is unset.
func newApptServer(t *testing.T) (*httptest.Server, *http.Client, *gorm.DB) {
	db := testsupport.PostgresDB(t, database.MigrationModels()...)
	handler := server.NewBackEnd(context.Background(), zap.NewNop(), nil, db).Routes()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, newJarClient(), db
}

func newJarClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// csrfMetaRe matches the token the page shell publishes for browser JS to read.
var csrfMetaRe = regexp.MustCompile(`<meta name="csrf-token" content="([^"]*)"`)

// csrfToken fetches the CSRF token bound to this client's session the way a
// browser does: by loading a page and reading the meta tag off it.
//
// It has to be fetched per request rather than once per client, because logging
// in clears the token — it is bound to the session, and the session changes
// identity at that point.
func csrfToken(t *testing.T, c *http.Client, targetURL string) string {
	t.Helper()

	u, err := url.Parse(targetURL)
	if err != nil {
		t.Fatalf("parse %s: %v", targetURL, err)
	}

	resp, err := c.Get(u.Scheme + "://" + u.Host + "/")
	if err != nil {
		t.Fatalf("fetch page shell for CSRF token: %v", err)
	}
	defer resp.Body.Close()

	page, _ := io.ReadAll(resp.Body)
	m := csrfMetaRe.FindSubmatch(page)
	if m == nil {
		t.Fatal("page shell published no csrf-token meta tag")
	}

	return html.UnescapeString(string(m[1]))
}

// doJSON sends a JSON request, attaching a CSRF token on the mutating methods
// the booking routes require one for. Safe methods are left alone, matching what
// the middleware enforces.
func doJSON(t *testing.T, c *http.Client, method, url string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Identify as a JSON client so an authentication failure comes back as a 401
	// rather than the 302 to /login a browser gets. The auth layer negotiates on
	// this header, and without it these API calls look like page loads.
	req.Header.Set("Accept", "application/json")
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set(api_auth.CSRFHeaderName, csrfToken(t, c, url))
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

// testPaymentSecret is the shared secret the tests sign mock provider deliveries
// with. The webhook refuses to run without one configured.
const testPaymentSecret = "test-payment-webhook-secret"

// capturePayment delivers a signed payment webhook that captures orderID, which
// is the only route that turns a hold into a booked appointment or a ticket.
//
// The signature is computed over the exact bytes sent, since that is what the
// handler verifies — signing a re-marshalled copy would be a different body.
func capturePayment(t *testing.T, ts *httptest.Server, orderID uint, deliveryID string) *http.Response {
	t.Helper()
	t.Setenv(booking.PaymentWebhookSecretEnv, testPaymentSecret)

	payload, _ := json.Marshal(map[string]any{
		"delivery_id": deliveryID, "order_id": orderID, "kind": "capture",
	})

	mac := hmac.New(sha256.New, []byte(testPaymentSecret))
	mac.Write(payload)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/webhooks/payments/mock", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Payment-Signature", hex.EncodeToString(mac.Sum(nil)))

	// A provider has no session and no cookie jar, so this deliberately does not
	// go through the browser client used elsewhere in these tests.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("payment webhook: %v", err)
	}
	return resp
}

func mustStatus(t *testing.T, resp *http.Response, want int, ctx string) {
	t.Helper()
	if resp.StatusCode != want {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("%s: status %d, want %d (%s)", ctx, resp.StatusCode, want, string(b))
	}
}

func registerAndLogin(t *testing.T, ts *httptest.Server, c *http.Client, email string) {
	t.Helper()
	resp := doJSON(t, c, "POST", ts.URL+"/api/v1/auth/register",
		map[string]string{"username": "host", "email": email, "password": "pw123456"})
	resp.Body.Close()
	resp = doJSON(t, c, "POST", ts.URL+"/api/v1/auth/login",
		map[string]string{"email": email, "password": "pw123456"})
	mustStatus(t, resp, http.StatusOK, "login")
	resp.Body.Close()
}

// nextMonday09Z returns the next future Monday at 09:00 UTC (aligns with a
// Mon 09:00–12:00 rule and stays inside the booking window).
func nextMonday09Z() time.Time {
	d := time.Now().UTC().AddDate(0, 0, 1)
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, 1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 9, 0, 0, 0, time.UTC)
}

func mondayRuleSchedule(slug string, extra map[string]any) map[string]any {
	body := map[string]any{
		"slug": slug, "name": "Sched", "timezone": "UTC",
		"slot_duration_min": 30, "capacity": 1, "window_days": 90,
		"rules": []map[string]int{{"weekday": int(time.Monday), "start_min": 540, "end_min": 720}},
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestAppointmentsHTTP_FreeFlow(t *testing.T) {
	ts, host, _ := newApptServer(t)
	registerAndLogin(t, ts, host, "free_host@example.com")

	slug := "oh-free"
	resp := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", mondayRuleSchedule(slug, nil))
	mustStatus(t, resp, http.StatusCreated, "create schedule")
	resp.Body.Close()

	start := nextMonday09Z()
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	avURL := fmt.Sprintf("%s/appointments/schedules/%s/availability?from=%s&to=%s",
		ts.URL, slug, day.Format(time.RFC3339), day.AddDate(0, 0, 1).Format(time.RFC3339))

	resp = doJSON(t, host, "GET", avURL, nil)
	mustStatus(t, resp, http.StatusOK, "availability")
	var slots []map[string]any
	json.NewDecoder(resp.Body).Decode(&slots)
	resp.Body.Close()
	if len(slots) != 6 {
		t.Fatalf("want 6 availability slots (09:00–11:30), got %d", len(slots))
	}

	// A guest books the 09:00 slot.
	guest := newJarClient()
	bookBody := map[string]any{"start_at": start.Format(time.RFC3339), "guest_name": "Ada"}
	resp = doJSON(t, guest, "POST", ts.URL+"/appointments/schedules/"+slug+"/book", bookBody)
	mustStatus(t, resp, http.StatusCreated, "book")
	var appt struct {
		CancelToken string `json:"cancel_token"`
	}
	json.NewDecoder(resp.Body).Decode(&appt)
	resp.Body.Close()
	if appt.CancelToken == "" {
		t.Fatal("book response missing cancel_token")
	}

	// Same slot, capacity 1 → sold out.
	resp = doJSON(t, newJarClient(), "POST", ts.URL+"/appointments/schedules/"+slug+"/book", bookBody)
	mustStatus(t, resp, http.StatusConflict, "double book")
	resp.Body.Close()

	// Cancel frees it; re-book succeeds.
	resp = doJSON(t, guest, "POST", ts.URL+"/appointments/cancel?token="+appt.CancelToken, nil)
	mustStatus(t, resp, http.StatusOK, "cancel")
	resp.Body.Close()

	resp = doJSON(t, newJarClient(), "POST", ts.URL+"/appointments/schedules/"+slug+"/book", bookBody)
	mustStatus(t, resp, http.StatusCreated, "re-book after cancel")
	resp.Body.Close()
}

func TestAppointmentsHTTP_PaidFlow(t *testing.T) {
	ts, host, db := newApptServer(t)
	registerAndLogin(t, ts, host, "paid_host@example.com")

	slug := "visa-paid"
	resp := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", mondayRuleSchedule(slug, map[string]any{
		"requires_payment": true, "price_cents": 5000, "currency": "USD", "hold_ttl_min": 10,
	}))
	mustStatus(t, resp, http.StatusCreated, "create paid schedule")
	resp.Body.Close()

	start := nextMonday09Z().Format(time.RFC3339)

	// /book is rejected on a paid schedule.
	resp = doJSON(t, newJarClient(), "POST", ts.URL+"/appointments/schedules/"+slug+"/book",
		map[string]any{"start_at": start})
	mustStatus(t, resp, http.StatusBadRequest, "book on paid schedule")
	resp.Body.Close()

	// A guest holds the slot; capture creates the confirmed appointment.
	guest := newJarClient()
	resp = doJSON(t, guest, "POST", ts.URL+"/appointments/schedules/"+slug+"/hold",
		map[string]any{"start_at": start, "guest_name": "Ada", "guest_email": "ada@example.com"})
	mustStatus(t, resp, http.StatusCreated, "hold")
	var held struct {
		Order struct {
			ID uint `json:"id"`
		} `json:"order"`
	}
	json.NewDecoder(resp.Body).Decode(&held)
	resp.Body.Close()
	if held.Order.ID == 0 {
		t.Fatal("hold response missing order id")
	}

	// The slot is reserved during checkout — nobody else can hold it.
	resp = doJSON(t, newJarClient(), "POST", ts.URL+"/appointments/schedules/"+slug+"/hold",
		map[string]any{"start_at": start})
	mustStatus(t, resp, http.StatusConflict, "concurrent hold")
	resp.Body.Close()

	resp = capturePayment(t, ts, held.Order.ID, "evt-1")
	mustStatus(t, resp, http.StatusOK, "capture")
	resp.Body.Close()

	// Re-delivering the same event must not capture a second time. Providers
	// retry, so this has to be a no-op rather than a duplicate charge.
	resp = capturePayment(t, ts, held.Order.ID, "evt-1")
	mustStatus(t, resp, http.StatusOK, "idempotent capture")
	resp.Body.Close()

	// Exactly one confirmed appointment; order paid; slot committed.
	var appts int64
	db.Model(&models.BookingAppointment{}).Where("status = ?", "confirmed").Count(&appts)
	if appts != 1 {
		t.Fatalf("want 1 confirmed appointment, got %d", appts)
	}
	var order models.BookingOrder
	db.First(&order, held.Order.ID)
	if order.Status != "paid" {
		t.Fatalf("order status=%q, want paid", order.Status)
	}
}

func availabilityCount(t *testing.T, c *http.Client, ts *httptest.Server, slug string, day time.Time) int {
	t.Helper()
	url := fmt.Sprintf("%s/appointments/schedules/%s/availability?from=%s&to=%s",
		ts.URL, slug, day.Format(time.RFC3339), day.AddDate(0, 0, 1).Format(time.RFC3339))
	resp := doJSON(t, c, "GET", url, nil)
	mustStatus(t, resp, http.StatusOK, "availability")
	var slots []map[string]any
	json.NewDecoder(resp.Body).Decode(&slots)
	resp.Body.Close()
	return len(slots)
}

func TestAppointmentsHTTP_RulesAndExceptions(t *testing.T) {
	ts, host, _ := newApptServer(t)
	registerAndLogin(t, ts, host, "rules_host@example.com")

	slug := "oh-rules"
	// Create a schedule with NO rules yet.
	body := mondayRuleSchedule(slug, nil)
	body["rules"] = []map[string]int{}
	resp := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", body)
	mustStatus(t, resp, http.StatusCreated, "create schedule")
	resp.Body.Close()

	start := nextMonday09Z()
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)

	if n := availabilityCount(t, host, ts, slug, day); n != 0 {
		t.Fatalf("no rules yet, want 0 slots, got %d", n)
	}

	// Add a Mon 09:00–12:00 rule → 6 slots.
	resp = doJSON(t, host, "POST", ts.URL+"/appointments/schedules/"+slug+"/rules",
		map[string]int{"weekday": int(time.Monday), "start_min": 540, "end_min": 720})
	mustStatus(t, resp, http.StatusCreated, "add rule")
	var rule struct {
		ID uint `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&rule)
	resp.Body.Close()
	if n := availabilityCount(t, host, ts, slug, day); n != 6 {
		t.Fatalf("after add rule want 6 slots, got %d", n)
	}

	// Block the whole target Monday → 0 slots.
	resp = doJSON(t, host, "POST", ts.URL+"/appointments/schedules/"+slug+"/exceptions",
		map[string]any{"date": start.Format("2006-01-02")})
	mustStatus(t, resp, http.StatusCreated, "add exception")
	var ex struct {
		ID uint `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&ex)
	resp.Body.Close()
	if n := availabilityCount(t, host, ts, slug, day); n != 0 {
		t.Fatalf("after whole-day block want 0 slots, got %d", n)
	}

	// Remove the exception → back to 6.
	resp = doJSON(t, host, "DELETE", fmt.Sprintf("%s/appointments/schedules/%s/exceptions/%d", ts.URL, slug, ex.ID), nil)
	mustStatus(t, resp, http.StatusNoContent, "delete exception")
	resp.Body.Close()
	if n := availabilityCount(t, host, ts, slug, day); n != 6 {
		t.Fatalf("after delete exception want 6 slots, got %d", n)
	}

	// Remove the rule → 0.
	resp = doJSON(t, host, "DELETE", fmt.Sprintf("%s/appointments/schedules/%s/rules/%d", ts.URL, slug, rule.ID), nil)
	mustStatus(t, resp, http.StatusNoContent, "delete rule")
	resp.Body.Close()
	if n := availabilityCount(t, host, ts, slug, day); n != 0 {
		t.Fatalf("after delete rule want 0 slots, got %d", n)
	}

	// Ownership: a different user cannot see this schedule's detail.
	other := newJarClient()
	registerAndLogin(t, ts, other, "rules_other@example.com")
	resp = doJSON(t, other, "GET", ts.URL+"/appointments/schedules/"+slug, nil)
	mustStatus(t, resp, http.StatusNotFound, "other user schedule detail")
	resp.Body.Close()
}

func TestAppointmentsHTTP_MyAppointmentsAndBookings(t *testing.T) {
	ts, host, _ := newApptServer(t)
	registerAndLogin(t, ts, host, "list_host@example.com")

	slug := "oh-list"
	resp := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", mondayRuleSchedule(slug, nil))
	mustStatus(t, resp, http.StatusCreated, "create schedule")
	resp.Body.Close()

	// A logged-in booker books a slot → appointment is linked to them.
	booker := newJarClient()
	registerAndLogin(t, ts, booker, "booker@example.com")
	start := nextMonday09Z().Format(time.RFC3339)
	resp = doJSON(t, booker, "POST", ts.URL+"/appointments/schedules/"+slug+"/book",
		map[string]any{"start_at": start})
	mustStatus(t, resp, http.StatusCreated, "booker books")
	resp.Body.Close()

	// Booker sees it in /me.
	resp = doJSON(t, booker, "GET", ts.URL+"/appointments/me", nil)
	mustStatus(t, resp, http.StatusOK, "my appointments")
	var mine []map[string]any
	json.NewDecoder(resp.Body).Decode(&mine)
	resp.Body.Close()
	if len(mine) != 1 {
		t.Fatalf("booker /me want 1, got %d", len(mine))
	}

	// A different logged-in user has none.
	stranger := newJarClient()
	registerAndLogin(t, ts, stranger, "stranger@example.com")
	resp = doJSON(t, stranger, "GET", ts.URL+"/appointments/me", nil)
	mustStatus(t, resp, http.StatusOK, "stranger /me")
	var none []map[string]any
	json.NewDecoder(resp.Body).Decode(&none)
	resp.Body.Close()
	if len(none) != 0 {
		t.Fatalf("stranger /me want 0, got %d", len(none))
	}

	// Host sees the booking; a non-owner cannot.
	resp = doJSON(t, host, "GET", ts.URL+"/appointments/schedules/"+slug+"/bookings", nil)
	mustStatus(t, resp, http.StatusOK, "host bookings")
	var bookings []map[string]any
	json.NewDecoder(resp.Body).Decode(&bookings)
	resp.Body.Close()
	if len(bookings) != 1 {
		t.Fatalf("host bookings want 1, got %d", len(bookings))
	}
	resp = doJSON(t, booker, "GET", ts.URL+"/appointments/schedules/"+slug+"/bookings", nil)
	mustStatus(t, resp, http.StatusNotFound, "non-owner bookings")
	resp.Body.Close()
}

func TestAppointmentsHTTP_PaidCancelRefund(t *testing.T) {
	ts, host, db := newApptServer(t)
	registerAndLogin(t, ts, host, "refund_host@example.com")

	slug := "visa-refund"
	resp := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", mondayRuleSchedule(slug, map[string]any{
		"requires_payment": true, "price_cents": 5000, "currency": "USD", "hold_ttl_min": 10,
	}))
	mustStatus(t, resp, http.StatusCreated, "create paid schedule")
	resp.Body.Close()

	start := nextMonday09Z().Format(time.RFC3339)
	guest := newJarClient()
	resp = doJSON(t, guest, "POST", ts.URL+"/appointments/schedules/"+slug+"/hold",
		map[string]any{"start_at": start, "guest_name": "Ada"})
	mustStatus(t, resp, http.StatusCreated, "hold")
	var held struct {
		Order struct {
			ID uint `json:"id"`
		} `json:"order"`
	}
	json.NewDecoder(resp.Body).Decode(&held)
	resp.Body.Close()

	resp = capturePayment(t, ts, held.Order.ID, "evt-1")
	mustStatus(t, resp, http.StatusOK, "capture")
	resp.Body.Close()

	// Grab the created appointment's cancel token (would be emailed in production).
	var appt models.BookingAppointment
	db.Where("status = ?", "confirmed").First(&appt)
	if appt.CancelToken == "" {
		t.Fatal("no confirmed appointment / cancel token")
	}

	// Cancel → slot freed, order refunded, refund payment recorded.
	resp = doJSON(t, guest, "POST", ts.URL+"/appointments/cancel?token="+appt.CancelToken, nil)
	mustStatus(t, resp, http.StatusOK, "cancel")
	resp.Body.Close()

	var order models.BookingOrder
	db.First(&order, held.Order.ID)
	if order.Status != "refunded" {
		t.Fatalf("order status=%q, want refunded", order.Status)
	}
	var refunds int64
	db.Model(&models.BookingPayment{}).Where("order_id = ? AND kind = ?", held.Order.ID, "refund").Count(&refunds)
	if refunds != 1 {
		t.Fatalf("want 1 refund payment, got %d", refunds)
	}
	var inv models.BookingInventory
	db.Where("slot_id IS NOT NULL").First(&inv)
	if inv.Booked != 0 {
		t.Fatalf("after refund booked=%d, want 0 (slot freed)", inv.Booked)
	}
}
