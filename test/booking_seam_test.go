package test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/server/booking"
)

// This file covers the seams the booking domain sits on rather than its booking
// logic: which routes require what, and how the domain meets the shared
// authentication layer. The flows themselves are covered by
// appointments_http_test.go and ticketing_http_test.go.

// TestBooking_RoutesRequireAuth pins the routes that are about one account's own
// rows. These have to reject an anonymous caller, since there is no account whose
// schedules or appointments they could be asking for.
func TestBooking_RoutesRequireAuth(t *testing.T) {
	ts, _, _ := newApptServer(t)

	cases := []struct {
		name, method, path string
		body               any
	}{
		{"create schedule", "POST", "/appointments/schedules", mondayRuleSchedule("nope", nil)},
		{"list schedules", "GET", "/appointments/schedules", nil},
		{"my appointments", "GET", "/appointments/me", nil},
		{"create event", "POST", "/events", map[string]any{"slug": "x", "name": "X"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doJSON(t, newJarClient(), tc.method, ts.URL+tc.path, tc.body)
			defer resp.Body.Close()

			// A JSON caller gets 401 rather than the browser's redirect to /login,
			// which is the content negotiation the auth layer performs.
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("anonymous %s %s: status %d, want 401", tc.method, tc.path, resp.StatusCode)
			}
		})
	}
}

// TestBooking_GuestRoutesAllowAnonymous is the other half of the classification:
// booking and holding must NOT require an account. Requiring one is precisely the
// friction this domain exists to avoid, so an anonymous caller reaching these
// routes has to get a real answer rather than a 401.
func TestBooking_GuestRoutesAllowAnonymous(t *testing.T) {
	ts, host, _ := newApptServer(t)
	registerAndLogin(t, ts, host, "guest_routes_host@example.com")

	slug := "oh-anon"
	resp := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", mondayRuleSchedule(slug, nil))
	mustStatus(t, resp, http.StatusCreated, "create schedule")
	resp.Body.Close()

	guest := newJarClient()
	resp = doJSON(t, guest, "POST", ts.URL+"/appointments/schedules/"+slug+"/book",
		map[string]any{"start_at": nextMonday09Z().Format(time.RFC3339), "guest_name": "Ada"})
	mustStatus(t, resp, http.StatusCreated, "guest books without an account")
	resp.Body.Close()
}

// TestBooking_MutatingRoutesRequireCSRF checks that a mutating booking request
// with no token is refused.
//
// A session cookie alone is not enough: browsers attach it to cross-site requests
// too, so without this check another origin could post here as the signed-in
// caller. The token has to be presented explicitly.
func TestBooking_MutatingRoutesRequireCSRF(t *testing.T) {
	ts, host, _ := newApptServer(t)
	registerAndLogin(t, ts, host, "csrf_host@example.com")

	// Deliberately bypasses doJSON, which is what attaches the token.
	body, _ := json.Marshal(mondayRuleSchedule("oh-csrf", nil))
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/appointments/schedules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := host.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("authenticated POST with no CSRF token: status %d, want 403", resp.StatusCode)
	}
}

// TestBooking_CollectionRoutesAreNotSubtrees guards the routing hazard that
// registering a collection route as "/" inside its group would create.
//
// A pattern ending in a slash matches a whole subtree, so "POST
// /appointments/schedules/" would answer every unmatched POST beneath it: a typo
// or a probe would reach the create handler. What is asserted here is that the
// create handler does not run, which is the part that would be a real bug —
// unmatched paths fall through to the site-wide handler, so the status is that
// handler's to decide, not this domain's.
func TestBooking_CollectionRoutesAreNotSubtrees(t *testing.T) {
	ts, host, db := newApptServer(t)
	registerAndLogin(t, ts, host, "subtree_host@example.com")

	for _, path := range []string{
		"/appointments/schedules/some-slug/not-a-route",
		"/events/some-slug/not-a-route",
	} {
		resp := doJSON(t, host, "POST", ts.URL+path,
			map[string]any{"slug": "subtree-probe", "name": "Probe", "timezone": "UTC",
				"slot_duration_min": 30, "capacity": 1})
		resp.Body.Close()

		if resp.StatusCode == http.StatusCreated {
			t.Errorf("POST %s was handled as a creation (201)", path)
		}
	}

	var schedules, events int64
	db.Model(&models.BookingSchedule{}).Where("slug = ?", "subtree-probe").Count(&schedules)
	db.Model(&models.BookingEvent{}).Where("slug = ?", "subtree-probe").Count(&events)
	if schedules != 0 || events != 0 {
		t.Errorf("unmatched POSTs created rows: %d schedules, %d events", schedules, events)
	}
}

// TestBooking_PaymentWebhookRejectsUnsignedDeliveries covers the authentication of
// the one route that turns a hold into a paid booking.
//
// It has no session and no CSRF token, so the signature over the body is the only
// thing standing between a stranger and a free appointment. Each case here is a
// way of not having a valid one.
func TestBooking_PaymentWebhookRejectsUnsignedDeliveries(t *testing.T) {
	ts, _, _ := newApptServer(t)

	payload, _ := json.Marshal(map[string]any{"delivery_id": "forged-1", "order_id": 1})

	post := func(t *testing.T, signature string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/webhooks/payments/mock", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if signature != "" {
			req.Header.Set("X-Payment-Signature", signature)
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			t.Fatalf("webhook request failed: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	t.Run("no secret configured", func(t *testing.T) {
		t.Setenv(booking.PaymentWebhookSecretEnv, "")
		// Refusing to serve beats accepting deliveries nobody can vouch for.
		if got := post(t, "deadbeef"); got != http.StatusServiceUnavailable {
			t.Fatalf("status %d, want 503", got)
		}
	})

	t.Run("no signature", func(t *testing.T) {
		t.Setenv(booking.PaymentWebhookSecretEnv, testPaymentSecret)
		if got := post(t, ""); got != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", got)
		}
	})

	t.Run("wrong signature", func(t *testing.T) {
		t.Setenv(booking.PaymentWebhookSecretEnv, testPaymentSecret)

		// A well-formed signature made with the wrong secret: this is the case a
		// length or format check would wave through.
		mac := hmac.New(sha256.New, []byte("not-the-secret"))
		mac.Write(payload)
		if got := post(t, hex.EncodeToString(mac.Sum(nil))); got != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", got)
		}
	})

	t.Run("valid signature over an unknown order", func(t *testing.T) {
		t.Setenv(booking.PaymentWebhookSecretEnv, testPaymentSecret)

		unknown, _ := json.Marshal(map[string]any{"delivery_id": "d-1", "order_id": 999999})
		mac := hmac.New(sha256.New, []byte(testPaymentSecret))
		mac.Write(unknown)

		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/webhooks/payments/mock", bytes.NewReader(unknown))
		req.Header.Set("X-Payment-Signature", hex.EncodeToString(mac.Sum(nil)))
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			t.Fatalf("webhook request failed: %v", err)
		}
		defer resp.Body.Close()

		// Authentic, but about nothing: the signature proves who sent it, not that
		// the order exists.
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status %d, want 404", resp.StatusCode)
		}
	})
}

// TestBooking_GuestWorkSurvivesLogin is the guest migrator end to end: somebody
// holds a paid slot as a guest, then signs in to pay.
//
// Without the migrator the hold they are about to pay for is stranded. It is
// stamped with the guest reference they had when they took it, and every request
// after login carries their user reference instead — so checkout would be paying
// for something the account does not own.
func TestBooking_GuestWorkSurvivesLogin(t *testing.T) {
	ts, host, db := newApptServer(t)
	registerAndLogin(t, ts, host, "migrator_host@example.com")

	slug := "oh-migrate"
	resp := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", mondayRuleSchedule(slug, map[string]any{
		"requires_payment": true, "price_cents": 5000, "currency": "USD", "hold_ttl_min": 15,
	}))
	mustStatus(t, resp, http.StatusCreated, "create paid schedule")
	resp.Body.Close()

	// A guest holds the slot before having an account at all.
	guest := newJarClient()
	resp = doJSON(t, guest, "POST", ts.URL+"/appointments/schedules/"+slug+"/hold",
		map[string]any{"start_at": nextMonday09Z().Format(time.RFC3339), "guest_name": "Ada"})
	mustStatus(t, resp, http.StatusCreated, "guest holds slot")

	var held struct {
		Order     models.BookingOrder `json:"order"`
		HoldToken string              `json:"hold_token"`
	}
	json.NewDecoder(resp.Body).Decode(&held)
	resp.Body.Close()

	var hold models.BookingHold
	if err := db.Where("token = ?", held.HoldToken).First(&hold).Error; err != nil {
		t.Fatalf("load hold: %v", err)
	}
	if !isGuestRef(hold.OwnerRef) {
		t.Fatalf("hold owner_ref %q, want a guest reference before login", hold.OwnerRef)
	}

	// The same browser now registers and signs in, carrying its guest cookie.
	email := "migrator_buyer@example.com"
	resp = doJSON(t, guest, "POST", ts.URL+"/api/v1/auth/register",
		map[string]string{"username": "buyer", "email": email, "password": "pw123456"})
	resp.Body.Close()
	resp = doJSON(t, guest, "POST", ts.URL+"/api/v1/auth/login",
		map[string]string{"email": email, "password": "pw123456"})
	mustStatus(t, resp, http.StatusOK, "guest logs in")
	resp.Body.Close()

	var user models.User
	if err := db.Where("email = ?", email).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	wantRef := fmt.Sprintf("user:%d", user.ID)

	if err := db.Where("token = ?", held.HoldToken).First(&hold).Error; err != nil {
		t.Fatalf("reload hold: %v", err)
	}
	if hold.OwnerRef != wantRef {
		t.Errorf("hold owner_ref %q after login, want %q", hold.OwnerRef, wantRef)
	}

	var order models.BookingOrder
	if err := db.First(&order, held.Order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if order.OwnerRef != wantRef {
		t.Errorf("order owner_ref %q after login, want %q", order.OwnerRef, wantRef)
	}
	// The order also carries the id as a column, so what a capture fulfils is
	// linked to the account and not only to the session that started it.
	if order.UserID == nil || *order.UserID != user.ID {
		t.Errorf("order user_id %v after login, want %d", order.UserID, user.ID)
	}

	// Signing in again must not disturb what the first login already moved: the
	// guest cookie outlives the login, so this runs the migrator a second time.
	resp = doJSON(t, guest, "POST", ts.URL+"/api/v1/auth/login",
		map[string]string{"email": email, "password": "pw123456"})
	mustStatus(t, resp, http.StatusOK, "second login")
	resp.Body.Close()

	if err := db.Where("token = ?", held.HoldToken).First(&hold).Error; err != nil {
		t.Fatalf("reload hold after second login: %v", err)
	}
	if hold.OwnerRef != wantRef {
		t.Errorf("hold owner_ref %q after second login, want %q", hold.OwnerRef, wantRef)
	}

	// And the account can now pay for what it holds.
	resp = capturePayment(t, ts, held.Order.ID, "migrated-1")
	mustStatus(t, resp, http.StatusOK, "capture migrated order")
	resp.Body.Close()

	var appointments []models.BookingAppointment
	db.Where("status = ?", "confirmed").Find(&appointments)
	if len(appointments) != 1 {
		t.Fatalf("want 1 confirmed appointment after capture, got %d", len(appointments))
	}
	if appointments[0].UserID == nil || *appointments[0].UserID != user.ID {
		t.Errorf("appointment user_id %v, want %d", appointments[0].UserID, user.ID)
	}
}

// isGuestRef reports whether ref is an anonymous visitor's owner reference, in
// the "guest:<sid>" form the authentication layer produces.
func isGuestRef(ref string) bool {
	return len(ref) > len("guest:") && ref[:len("guest:")] == "guest:"
}
