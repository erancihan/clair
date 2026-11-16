package test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// getPage loads an HTML page the way a browser does: no Accept: application/json,
// so an authentication failure is the redirect to /login rather than a 401.
func getPage(t *testing.T, c *http.Client, url string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp, readBody(t, resp)
}

// TestHostPages_AnonymousIsSentToLogin pins the guard on the host pages: they
// sit behind AuthMiddleware, so an anonymous browser gets the login redirect
// with a next path, and never the page.
func TestHostPages_AnonymousIsSentToLogin(t *testing.T) {
	ts, _, _ := newApptServer(t)
	anon := newJarClient()

	for _, path := range []string{"/host", "/host/schedules/anything"} {
		resp, body := getPage(t, anon, ts.URL+path)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s: status %d, want 302", path, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/login?next=") {
			t.Errorf("%s: redirected to %q, want /login?next=...", path, loc)
		}
		if strings.Contains(body, "hostHome") || strings.Contains(body, "hostSchedule") {
			t.Errorf("%s: an anonymous response carried the host page", path)
		}
	}

	// /dashboard is where a sign-in lands; it forwards to the host page, which
	// does the guarding.
	resp, _ := getPage(t, anon, ts.URL+"/dashboard")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/host" {
		t.Errorf("/dashboard: status %d to %q, want 302 to /host", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Nothing an anonymous visitor sees links to the host pages or to sign-in.
	_, home := getPage(t, anon, ts.URL+"/")
	for _, link := range []string{`href="/host"`, `href="/login"`, `href="/dashboard"`} {
		if strings.Contains(home, link) {
			t.Errorf("the anonymous home page contains %s", link)
		}
	}
}

// TestHostPages_SignedInHostGetsThePages checks the pages render for an
// account, bind the Alpine components the script defines, publish a CSRF token
// for them to send, and refuse a schedule the caller does not own.
func TestHostPages_SignedInHostGetsThePages(t *testing.T) {
	ts, host, _ := newApptServer(t)
	registerAndLogin(t, ts, host, "pages_host@example.com")

	resp, body := getPage(t, host, ts.URL+"/host")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/host: status %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{
		`x-data="hostHome"`, `x-data="hostEvent"`,
		`Alpine.data('hostHome'`, `Alpine.data('hostEvent'`, `Alpine.data('hostSchedule'`,
		`<meta name="csrf-token" content="`,
		// The signed-in account menu is the way in.
		`href="/host"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/host does not contain %s", want)
		}
	}
	if m := csrfMetaRe.FindStringSubmatch(body); m == nil || m[1] == "" {
		t.Error("/host published an empty csrf token")
	}

	// Signed in, /dashboard still lands on the host page.
	resp, _ = getPage(t, host, ts.URL+"/dashboard")
	if resp.Header.Get("Location") != "/host" {
		t.Errorf("/dashboard redirected to %q, want /host", resp.Header.Get("Location"))
	}

	slug := "pages-oh"
	r := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", mondayRuleSchedule(slug, nil))
	mustStatus(t, r, http.StatusCreated, "create schedule")
	r.Body.Close()

	resp, body = getPage(t, host, ts.URL+"/host/schedules/"+slug)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("schedule page: status %d, want 200", resp.StatusCode)
	}
	// The slug reaches the component as JSON inside the attribute, which templ
	// escapes; match on the escaped form a browser would unescape.
	if !strings.Contains(body, `x-data="hostSchedule({&#34;slug&#34;:&#34;`+slug+`&#34;})"`) {
		t.Error("schedule page does not bind hostSchedule to its slug")
	}

	// Unknown, and somebody else's, are both a plain 404.
	resp, _ = getPage(t, host, ts.URL+"/host/schedules/no-such-schedule")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown schedule page: status %d, want 404", resp.StatusCode)
	}
	other := newJarClient()
	registerAndLogin(t, ts, other, "pages_other@example.com")
	resp, _ = getPage(t, other, ts.URL+"/host/schedules/"+slug)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("another host's schedule page: status %d, want 404", resp.StatusCode)
	}
}

// TestHostPages_Flows drives the routes the host pages call, with the request
// bodies the page script builds, and pins the response shapes it reads.
func TestHostPages_Flows(t *testing.T) {
	ts, host, _ := newApptServer(t)
	registerAndLogin(t, ts, host, "flows_host@example.com")

	// ---- create a schedule, as hostHome.create() sends it: no rules ----
	slug := "flows-oh"
	r := doJSON(t, host, "POST", ts.URL+"/appointments/schedules", map[string]any{
		"slug": slug, "name": "Office hours", "description": "", "timezone": "UTC",
		"slot_duration_min": 30, "capacity": 1, "lead_time_min": 0, "window_days": 30,
		"requires_payment": false,
	})
	mustStatus(t, r, http.StatusCreated, "create schedule")
	var created struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&created); err != nil || created.Slug != slug {
		t.Fatalf("create answered slug %q (%v); the page redirects on it", created.Slug, err)
	}
	r.Body.Close()

	// The list the overview renders.
	r = doJSON(t, host, "GET", ts.URL+"/appointments/schedules", nil)
	mustStatus(t, r, http.StatusOK, "list schedules")
	var list []map[string]any
	_ = json.NewDecoder(r.Body).Decode(&list)
	r.Body.Close()
	if len(list) != 1 || list[0]["slug"] != slug || list[0]["slot_duration_min"] == nil {
		t.Fatalf("schedule list = %v, want the one schedule with its snake_case fields", list)
	}

	base := ts.URL + "/appointments/schedules/" + slug

	// ---- add a rule: Monday 09:00 until midnight, which the page sends as 1440 ----
	r = doJSON(t, host, "POST", base+"/rules", map[string]int{"weekday": 1, "start_min": 540, "end_min": 1440})
	mustStatus(t, r, http.StatusCreated, "add rule")
	r.Body.Close()

	// ---- add exceptions: a whole day, and part of one ----
	day := time.Now().UTC().AddDate(0, 0, 7).Format("2006-01-02")
	r = doJSON(t, host, "POST", base+"/exceptions", map[string]any{"date": day})
	mustStatus(t, r, http.StatusCreated, "add whole-day exception")
	r.Body.Close()
	r = doJSON(t, host, "POST", base+"/exceptions", map[string]any{"date": day, "start_min": 720, "end_min": 780})
	mustStatus(t, r, http.StatusCreated, "add partial exception")
	r.Body.Close()

	// A refusal comes back as plain text, which the page shows as it is.
	r = doJSON(t, host, "POST", base+"/rules", map[string]int{"weekday": 1, "start_min": 600, "end_min": 540})
	mustStatus(t, r, http.StatusBadRequest, "backwards rule")
	if msg := readBody(t, r); !strings.Contains(msg, "invalid availability rule") {
		t.Errorf("refusal body %q, want the route's plain-text reason", msg)
	}

	// ---- the editor's view: rules and exceptions keyed the way the script reads them ----
	type editorView struct {
		Schedule struct {
			Timezone string `json:"timezone"`
		} `json:"schedule"`
		Rules []struct {
			ID       uint
			Weekday  int
			StartMin int
			EndMin   int
		} `json:"rules"`
		Exceptions []struct {
			ID       uint
			Date     string
			StartMin *int
			EndMin   *int
		} `json:"exceptions"`
	}
	load := func() editorView {
		t.Helper()
		r := doJSON(t, host, "GET", base, nil)
		mustStatus(t, r, http.StatusOK, "get schedule")
		defer r.Body.Close()
		var raw map[string][]map[string]any
		var v editorView
		b := readBody(t, r)
		_ = json.Unmarshal([]byte(b), &raw)
		_ = json.Unmarshal([]byte(b), &v)
		// The page reads rule.ID, rule.Weekday, ex.Date and friends: pin that the
		// keys really are spelled that way.
		for _, key := range []string{"ID", "Weekday", "StartMin", "EndMin"} {
			for _, rule := range raw["rules"] {
				if _, ok := rule[key]; !ok {
					t.Fatalf("rule is missing %q: %v", key, rule)
				}
			}
		}
		for _, key := range []string{"ID", "Date", "StartMin", "EndMin"} {
			for _, ex := range raw["exceptions"] {
				if _, ok := ex[key]; !ok {
					t.Fatalf("exception is missing %q: %v", key, ex)
				}
			}
		}
		return v
	}

	v := load()
	if len(v.Rules) != 1 || v.Rules[0].Weekday != 1 || v.Rules[0].StartMin != 540 || v.Rules[0].EndMin != 1440 {
		t.Fatalf("rules = %+v, want the Monday 09:00–24:00 window", v.Rules)
	}
	if len(v.Exceptions) != 2 {
		t.Fatalf("exceptions = %+v, want two", v.Exceptions)
	}
	var whole, partial uint
	for _, ex := range v.Exceptions {
		if ex.StartMin == nil {
			whole = ex.ID
		} else {
			partial = ex.ID
		}
	}
	if whole == 0 || partial == 0 {
		t.Fatalf("exceptions = %+v, want one whole-day and one partial", v.Exceptions)
	}

	// ---- delete what was added ----
	r = doJSON(t, host, "DELETE", base+"/rules/"+itoa(v.Rules[0].ID), nil)
	mustStatus(t, r, http.StatusNoContent, "delete rule")
	r.Body.Close()
	r = doJSON(t, host, "DELETE", base+"/exceptions/"+itoa(whole), nil)
	mustStatus(t, r, http.StatusNoContent, "delete exception")
	r.Body.Close()

	v = load()
	if len(v.Rules) != 0 || len(v.Exceptions) != 1 || v.Exceptions[0].ID != partial {
		t.Fatalf("after deletes: rules %+v, exceptions %+v", v.Rules, v.Exceptions)
	}

	// ---- bookings: a booked guest shows up on the host's list ----
	r = doJSON(t, host, "POST", base+"/rules", map[string]int{"weekday": 1, "start_min": 540, "end_min": 720})
	mustStatus(t, r, http.StatusCreated, "re-add rule")
	r.Body.Close()
	start := nextMonday09Z()
	r = doJSON(t, newJarClient(), "POST", base+"/book", map[string]any{
		"start_at": start.Format(time.RFC3339), "guest_name": "Ada", "guest_email": "ada@example.com",
	})
	mustStatus(t, r, http.StatusCreated, "guest books")
	r.Body.Close()

	r = doJSON(t, host, "GET", base+"/bookings", nil)
	mustStatus(t, r, http.StatusOK, "list bookings")
	var bookings []map[string]any
	_ = json.NewDecoder(r.Body).Decode(&bookings)
	r.Body.Close()
	if len(bookings) != 1 || bookings[0]["guest_name"] != "Ada" || bookings[0]["status"] != "confirmed" {
		t.Fatalf("bookings = %v, want Ada's confirmed booking", bookings)
	}

	// ---- create events, as hostEvent.create() sends them ----
	at := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Minute).Format(time.RFC3339)
	r = doJSON(t, host, "POST", ts.URL+"/events", map[string]any{
		"slug": "flows-ga", "name": "Open night", "start_at": at, "timezone": "UTC", "max_per_buyer": 0,
		"tiers": []map[string]any{{"name": "General admission", "kind": "ga", "price_cents": 1500, "capacity": 100}},
	})
	mustStatus(t, r, http.StatusCreated, "create GA event")
	r.Body.Close()

	// The seated form sends rows A, B… numbered from 1.
	var seats []map[string]any
	for _, row := range []string{"A", "B"} {
		for n := 1; n <= 3; n++ {
			seats = append(seats, map[string]any{"label": row + itoa(uint(n)), "row_name": row, "number": n})
		}
	}
	r = doJSON(t, host, "POST", ts.URL+"/events", map[string]any{
		"slug": "flows-seated", "name": "Recital", "start_at": at, "timezone": "UTC", "max_per_buyer": 4,
		"tiers": []map[string]any{{"name": "Stalls", "kind": "seated", "price_cents": 2000, "seats": seats}},
	})
	mustStatus(t, r, http.StatusCreated, "create seated event")
	var event map[string]any
	_ = json.NewDecoder(r.Body).Decode(&event)
	r.Body.Close()
	if event["slug"] != "flows-seated" || event["start_at"] == nil {
		t.Fatalf("event = %v; the page shows its name, slug and start", event)
	}

	r = doJSON(t, host, "GET", ts.URL+"/events/flows-seated/seats", nil)
	mustStatus(t, r, http.StatusOK, "seat map")
	var seatMap []map[string]any
	_ = json.NewDecoder(r.Body).Decode(&seatMap)
	r.Body.Close()
	if len(seatMap) != 6 {
		t.Errorf("seat map has %d seats, want 6", len(seatMap))
	}
}
