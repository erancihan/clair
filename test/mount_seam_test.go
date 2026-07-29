package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erancihan/clair/internal/server"
	"go.uber.org/zap"
)

// newRoutesServer builds the real route table the way cmd does, so a test can
// assert on what Routes() actually mounts rather than on a hand-built mux.
func newRoutesServer(t *testing.T) *httptest.Server {
	t.Helper()

	backend := server.NewBackEnd(context.Background(), zap.NewNop(), nil, newAuthTestDB(t))

	srv := httptest.NewServer(backend.Routes())
	t.Cleanup(srv.Close)

	return srv
}

// TestMountedRoutes pins the routes the mount seam is responsible for wiring.
// Domains register themselves through their own Mount, so this is the check that
// moving a domain's routes out of Routes() did not quietly move them off the
// path they were served on.
func TestMountedRoutes(t *testing.T) {
	srv := newRoutesServer(t)

	// Follow no redirects: a redirect is an answer here, not a step.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	tests := []struct {
		name     string
		path     string
		status   int
		contains string
	}{
		{name: "home", path: "/", status: http.StatusOK, contains: "csrf-token"},
		{name: "login page", path: "/login", status: http.StatusOK, contains: "Sign in"},
		{name: "requester", path: "/requester", status: http.StatusOK},
		{name: "games index", path: "/games/", status: http.StatusOK},
		{name: "tic-tac-toe", path: "/games/tic-tac-toe/", status: http.StatusOK},
		{name: "chess", path: "/games/chess/", status: http.StatusOK},
		{name: "unknown path renders the 404 page", path: "/no-such-page", status: http.StatusOK, contains: "Page not found"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("request to %s failed: %v", tc.path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.status {
				t.Fatalf("expected %d from %s, got %d", tc.status, tc.path, resp.StatusCode)
			}

			if tc.contains != "" {
				if body := readBody(t, resp); !strings.Contains(body, tc.contains) {
					t.Errorf("expected %s to contain %q", tc.path, tc.contains)
				}
			}
		})
	}

	// The games streaming endpoints are mounted but reject a request without a
	// game id, which is enough to prove the domain's non-page routes came across
	// on their original paths.
	for _, path := range []string{"/games/chess/stream", "/games/tic-tac-toe/stream"} {
		t.Run("mounted "+path, func(t *testing.T) {
			resp, err := client.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("request to %s failed: %v", path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 (missing id) from %s, got %d", path, resp.StatusCode)
			}
		})
	}

	t.Run("static assets stay reachable", func(t *testing.T) {
		resp, err := client.Get(srv.URL + "/static/")
		if err != nil {
			t.Fatalf("static request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			t.Error("expected /static/ to be served, got 404")
		}
	})
}
