package test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

type orderViewJSON struct {
	ID        uint   `json:"id"`
	Status    string `json:"status"`
	EventSlug string `json:"event_slug"`
	Tickets   []struct {
		Code   string `json:"code"`
		Seat   string `json:"seat"`
		Status string `json:"status"`
	} `json:"tickets"`
}

func listOrders(t *testing.T, c *http.Client, base string) []orderViewJSON {
	t.Helper()
	resp := doJSON(t, c, "GET", base+"/events/orders", nil)
	mustStatus(t, resp, http.StatusOK, "list orders")
	defer resp.Body.Close()
	var out []orderViewJSON
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode orders: %v", err)
	}
	return out
}

// TestTheaterCheckoutIssuesTicketsAndCancelRefunds drives B2 over the real
// route table: hold two seats, pay, see the tickets, cancel, see the seats free.
func TestTheaterCheckoutIssuesTicketsAndCancelRefunds(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	seats := getSeats(t, ts, slug)
	pick := []uint{seats[0].ID, seats[1].ID}

	guest := newJarClient()

	// Nothing held yet.
	resp := doJSON(t, guest, "POST", ts.URL+"/events/"+slug+"/checkout", map[string]any{"guest_name": "Ada"})
	mustStatus(t, resp, http.StatusConflict, "checkout with nothing held")
	resp.Body.Close()

	resp = doJSON(t, guest, "POST", ts.URL+"/events/"+slug+"/seats/hold", map[string]any{"seat_ids": pick, "guest_name": "Ada"})
	mustStatus(t, resp, http.StatusCreated, "hold")
	resp.Body.Close()

	resp = doJSON(t, guest, "POST", ts.URL+"/events/"+slug+"/checkout", map[string]any{"guest_name": " "})
	mustStatus(t, resp, http.StatusBadRequest, "checkout without a name")
	resp.Body.Close()

	resp = doJSON(t, guest, "POST", ts.URL+"/events/"+slug+"/checkout", map[string]any{"guest_name": "Ada", "guest_email": "ada@aurenor.test"})
	mustStatus(t, resp, http.StatusCreated, "checkout")
	var paid struct {
		Orders  []orderViewJSON `json:"orders"`
		Partial bool            `json:"partial"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&paid); err != nil {
		t.Fatalf("decode checkout: %v", err)
	}
	resp.Body.Close()
	if len(paid.Orders) != 1 || len(paid.Orders[0].Tickets) != 2 || paid.Partial {
		t.Fatalf("checkout answered %+v", paid)
	}

	for _, s := range getSeats(t, ts, slug) {
		if s.ID == pick[0] || s.ID == pick[1] {
			if s.State == "free" {
				t.Errorf("paid seat %s reads free", s.Label)
			}
		}
	}

	orders := listOrders(t, guest, ts.URL)
	if len(orders) != 1 || orders[0].EventSlug != slug || orders[0].Status != "paid" {
		t.Fatalf("my orders: %+v", orders)
	}
	if others := listOrders(t, newJarClient(), ts.URL); len(others) != 0 {
		t.Errorf("a stranger sees %d orders", len(others))
	}

	cancelURL := ts.URL + "/events/orders/" + strconv.FormatUint(uint64(orders[0].ID), 10) + "/cancel"
	resp = doJSON(t, newJarClient(), "POST", cancelURL, nil)
	mustStatus(t, resp, http.StatusNotFound, "cancel by a stranger")
	resp.Body.Close()

	resp = doJSON(t, guest, "POST", cancelURL, nil)
	mustStatus(t, resp, http.StatusOK, "cancel")
	resp.Body.Close()

	orders = listOrders(t, guest, ts.URL)
	if len(orders) != 1 || orders[0].Status != "refunded" {
		t.Fatalf("after cancel: %+v", orders)
	}
	for _, tk := range orders[0].Tickets {
		if tk.Status != "refunded" {
			t.Errorf("ticket %s is %q after cancel", tk.Code, tk.Status)
		}
	}
	for _, s := range getSeats(t, ts, slug) {
		if (s.ID == pick[0] || s.ID == pick[1]) && s.State != "free" {
			t.Errorf("cancelled seat %s reads %q, want free", s.Label, s.State)
		}
	}
}

// TestWebhookAfterHoldLapsedAnswersRefunded: K6 at the HTTP edge. A provider
// delivering after the hold ran out gets a 200 it can stop retrying on.
func TestWebhookAfterHoldLapsedAnswersRefunded(t *testing.T) {
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	seats := getSeats(t, ts, slug)

	guest := newJarClient()
	resp := doJSON(t, guest, "POST", ts.URL+"/events/"+slug+"/seats/hold", map[string]any{"seat_ids": []uint{seats[0].ID}, "guest_name": "Late"})
	mustStatus(t, resp, http.StatusCreated, "hold")
	var held struct {
		Order struct {
			ID uint `json:"id"`
		} `json:"order"`
		HoldTokens []string `json:"hold_tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&held); err != nil {
		t.Fatalf("decode hold: %v", err)
	}
	resp.Body.Close()

	// The buyer gives the seat back; the provider's delivery is already in flight.
	resp = doJSON(t, guest, "POST", ts.URL+"/events/"+slug+"/holds/release", map[string]any{"hold_tokens": held.HoldTokens})
	mustStatus(t, resp, http.StatusOK, "release")
	resp.Body.Close()

	resp = capturePayment(t, ts, held.Order.ID, "late-delivery")
	mustStatus(t, resp, http.StatusOK, "late webhook")
	var body struct {
		Status string `json:"status"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body.Status != "refunded" {
		t.Errorf("late webhook status %q, want refunded", body.Status)
	}
}
