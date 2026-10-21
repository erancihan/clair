package test

import (
	"net/http"
	"strings"
	"testing"
)

// TestShopMountSeam pins the routing shape the shop domain relies on and that
// docs/shop-plan.md §4.3 explains: an exact landing pattern, a redirect from
// the trailing-slash form, and a shop-owned subtree fallback that answers a
// real 404 instead of letting unmatched paths fall through to the site's
// method-less "/" catch-all (which answers 200).
func TestShopMountSeam(t *testing.T) {
	srv := newRoutesServer(t)

	// A redirect is an answer here, not a step.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	tests := []struct {
		name     string
		method   string
		path     string
		status   int
		location string
		contains string
	}{
		{name: "landing renders through the shell", method: http.MethodGet, path: "/shop", status: http.StatusOK, contains: `<meta name="csrf-token"`},
		{name: "landing carries the shop heading", method: http.MethodGet, path: "/shop", status: http.StatusOK, contains: ">Shop<"},
		{name: "trailing slash redirects to the canonical landing", method: http.MethodGet, path: "/shop/", status: http.StatusMovedPermanently, location: "/shop"},
		{name: "unknown shop path is a real 404", method: http.MethodGet, path: "/shop/nonexistent", status: http.StatusNotFound, contains: "Page not found"},
		{name: "wrong verb under the shop is a real 404", method: http.MethodDelete, path: "/shop/products", status: http.StatusNotFound},
		{name: "header links to the shop", method: http.MethodGet, path: "/shop", status: http.StatusOK, contains: `href="/shop"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request to %s %s failed: %v", tc.method, tc.path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.status {
				t.Fatalf("expected %d from %s %s, got %d", tc.status, tc.method, tc.path, resp.StatusCode)
			}
			if tc.location != "" {
				if got := resp.Header.Get("Location"); got != tc.location {
					t.Errorf("expected Location %q from %s, got %q", tc.location, tc.path, got)
				}
			}
			if tc.contains != "" {
				if body := readBody(t, resp); !strings.Contains(body, tc.contains) {
					t.Errorf("expected %s to contain %q", tc.path, tc.contains)
				}
			}
		})
	}
}
