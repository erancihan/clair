package test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestServiceScreensResolve: every service the institution pages link to has a
// screen of its own, and nothing else does.
func TestServiceScreensResolve(t *testing.T) {
	srv := newRoutesServer(t)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	tests := []struct {
		path   string
		status int
		body   string
	}{
		{path: "/aurenor/hospitals/solvarre-general/cardiology", status: http.StatusOK, body: "solvarre-general-cardiology"},
		{path: "/aurenor/hospitals/gilt-coast-clinic/dermatology", status: http.StatusOK, body: "gilt-coast-clinic-dermatology"},
		{path: "/aurenor/consulate/tourist-visa-interview", status: http.StatusOK, body: "consulate-tourist-visa-interview"},
		{path: "/aurenor/bureau/fingerprint-appointment", status: http.StatusOK, body: "bureau-fingerprint-appointment"},
		{path: "/aurenor/hospitals/solvarre-general/no-such-department", status: http.StatusNotFound},
		{path: "/aurenor/hospitals/no-such-hospital/cardiology", status: http.StatusNotFound},
		{path: "/aurenor/consulate/no-such-service", status: http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := client.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
			if tc.body != "" {
				body := readBody(t, resp)
				if !strings.Contains(body, tc.body) {
					t.Errorf("page does not bind schedule %q", tc.body)
				}
				if !strings.Contains(body, `x-data="service(`) {
					t.Error("page renders no booking component")
				}
			}
		})
	}
}

// TestInstitutionPagesOnlyLink: a list page must not carry availability for
// services the visitor is not looking at. That is what keeps one screen to one
// subscription.
func TestInstitutionPagesOnlyLink(t *testing.T) {
	srv := newRoutesServer(t)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	for _, path := range []string{"/aurenor/hospitals/solvarre-general", "/aurenor/consulate", "/aurenor/bureau"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("request to %s failed: %v", path, err)
		}
		body := readBody(t, resp)
		resp.Body.Close()

		if strings.Contains(body, "apptBooker") {
			t.Errorf("%s still embeds a booking component per service", path)
		}
		if strings.Contains(body, "availability") {
			t.Errorf("%s fetches availability for services it only lists", path)
		}
		if !strings.Contains(body, "/aurenor/") {
			t.Errorf("%s links to no service screens", path)
		}
	}
}

// TestServiceScreenSurvivesAStreamBlip: EventSource fires onerror while it is
// retrying, not only when refused. Closing on the first one retires live
// updates for the whole session.
func TestServiceScreenSurvivesAStreamBlip(t *testing.T) {
	srv := newRoutesServer(t)
	resp, err := http.Get(srv.URL + "/aurenor/hospitals/solvarre-general/cardiology")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("service screen: status %d", resp.StatusCode)
	}
	if !strings.Contains(string(raw), "EventSource.CLOSED") {
		t.Error("the service screen still falls back to polling on any onerror")
	}
}

func TestServiceScreenGuardsItsStorageWrite(t *testing.T) {
	srv := newRoutesServer(t)
	resp, err := http.Get(srv.URL + "/aurenor/hospitals/solvarre-general/cardiology")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), "try { window.localStorage.setItem(AURENOR_BOOKINGS_KEY") {
		t.Error("saving a booking to storage is not guarded")
	}
}
