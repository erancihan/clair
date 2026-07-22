package test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	authentication "github.com/erancihan/clair/internal/server/authentication"
)

// newCSRFTestServer mounts a CSRF-protected mux:
//
//	GET  /token  -> returns the session-bound CSRF token (and sets the session cookie)
//	POST /submit -> returns "ok" when the CSRF check passes
func newCSRFTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	protect := authentication.CSRF()
	mux := http.NewServeMux()

	mux.Handle("GET /token", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(authentication.CSRFToken(r)))
	})))
	mux.Handle("POST /submit", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fetchCSRFToken performs the GET /token handshake and returns the token together
// with the cookies that bind it to a session.
func fetchCSRFToken(t *testing.T, srv *httptest.Server) (string, []*http.Cookie) {
	t.Helper()

	resp, err := http.Get(srv.URL + "/token")
	if err != nil {
		t.Fatalf("token request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from GET /token, got %d", resp.StatusCode)
	}

	token := readBody(t, resp)
	if token == "" {
		t.Fatal("expected a non-empty CSRF token")
	}

	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected GET /token to set a session cookie")
	}
	return token, cookies
}

func TestCSRF(t *testing.T) {
	srv := newCSRFTestServer(t)
	token, cookies := fetchCSRFToken(t, srv)

	newPost := func(body string, contentType string) *http.Request {
		var r *http.Request
		if body != "" {
			r, _ = http.NewRequest(http.MethodPost, srv.URL+"/submit", strings.NewReader(body))
			r.Header.Set("Content-Type", contentType)
		} else {
			r, _ = http.NewRequest(http.MethodPost, srv.URL+"/submit", nil)
		}
		for _, c := range cookies {
			r.AddCookie(c)
		}
		return r
	}

	t.Run("mutating request with matching header token passes", func(t *testing.T) {
		req := newPost("", "")
		req.Header.Set(authentication.CSRFHeaderName, token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("submit failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 with valid header token, got %d", resp.StatusCode)
		}
	})

	t.Run("mutating request with matching form token passes", func(t *testing.T) {
		form := url.Values{authentication.CSRFFieldName: {token}}
		req := newPost(form.Encode(), "application/x-www-form-urlencoded")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("submit failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 with valid form token, got %d", resp.StatusCode)
		}
	})

	t.Run("tokenless mutating request is rejected", func(t *testing.T) {
		req := newPost("", "")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("submit failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 without a token, got %d", resp.StatusCode)
		}
	})

	t.Run("mutating request with wrong token is rejected", func(t *testing.T) {
		req := newPost("", "")
		req.Header.Set(authentication.CSRFHeaderName, token+"tampered")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("submit failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 with a wrong token, got %d", resp.StatusCode)
		}
	})

	t.Run("safe GET method passes through", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/token")
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected GET to pass through with 200, got %d", resp.StatusCode)
		}
	})
}
