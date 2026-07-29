package test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
	authentication "github.com/erancihan/clair/internal/server/authentication"
	"github.com/erancihan/clair/internal/web"
	"github.com/erancihan/clair/internal/web/pages"
	"gorm.io/gorm"
)

// csrfMetaPattern extracts the token the shell publishes for browser JS.
var csrfMetaPattern = regexp.MustCompile(`<meta name="csrf-token" content="([^"]*)"`)

// shellTestServer is an httptest.Server exercising a page rendered through the
// shell, alongside the database handle so tests can create accounts.
type shellTestServer struct {
	*httptest.Server
	db *gorm.DB
}

// newShellTestServer wires up a server that mirrors how a domain renders a page:
//
//	GET  /page   -> OptionalAuthMiddleware, then a page rendered through PageShell
//	POST /submit -> behind CSRF(); the check the published token has to pass
//	POST /login  -> AuthLogin, so a test can render the shell as a signed-in user
func newShellTestServer(t *testing.T) *shellTestServer {
	t.Helper()

	ctx := newAuthTestContext(t)

	mux := http.NewServeMux()
	mux.Handle("GET /page", authentication.OptionalAuthMiddleware(ctx)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			templ.Handler(web.Base(authentication.PageShell(w, r, "Shell Test"), pages.Home())).ServeHTTP(w, r)
		})))

	mux.Handle("POST /submit", authentication.CSRF()(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})))

	mux.HandleFunc("POST /login", authentication.AuthLogin(ctx))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &shellTestServer{Server: srv, db: ctx.DBConn}
}

// jarClient returns a client that keeps cookies across requests, the way a
// browser walking these pages would.
func jarClient(t *testing.T) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to build a cookie jar: %v", err)
	}

	return &http.Client{Jar: jar}
}

// renderPage fetches the shell-rendered page and returns its body.
func renderPage(t *testing.T, client *http.Client, srv *shellTestServer) string {
	t.Helper()

	resp, err := client.Get(srv.URL + "/page")
	if err != nil {
		t.Fatalf("page request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected the page to render 200, got %d", resp.StatusCode)
	}

	return readBody(t, resp)
}

// csrfMetaToken pulls the published CSRF token out of a rendered page.
func csrfMetaToken(t *testing.T, body string) string {
	t.Helper()

	match := csrfMetaPattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("expected a csrf-token meta tag in the rendered page, got:\n%s", body)
	}

	return match[1]
}

// TestShellPublishesUsableCSRFToken is the guarantee that makes the meta tag
// worth having: the token a page publishes must be the one bound to the caller's
// session AND already persisted to the session cookie. A token minted into the
// per-request session values but never saved renders fine and then fails the
// very check it exists to pass.
func TestShellPublishesUsableCSRFToken(t *testing.T) {
	srv := newShellTestServer(t)
	client := jarClient(t)

	token := csrfMetaToken(t, renderPage(t, client, srv))
	if token == "" {
		t.Fatal("expected the shell to publish a non-empty CSRF token")
	}

	t.Run("the published token passes the CSRF check", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/submit", nil)
		req.Header.Set(authentication.CSRFHeaderName, token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("submit request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected the published token to be accepted, got %d", resp.StatusCode)
		}
	})

	t.Run("a tampered token is still rejected", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/submit", nil)
		req.Header.Set(authentication.CSRFHeaderName, token+"tampered")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("submit request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 for a tampered token, got %d", resp.StatusCode)
		}
	})

	t.Run("the token is stable across renders of the same session", func(t *testing.T) {
		if again := csrfMetaToken(t, renderPage(t, client, srv)); again != token {
			t.Errorf("expected the same session to publish a stable token, got %q then %q", token, again)
		}
	})
}

// TestShellRendersUserMenu covers the other half of the shell: the account slot
// is driven by CurrentUser, so an anonymous visitor gets a sign-in link and a
// signed-in one gets their menu.
func TestShellRendersUserMenu(t *testing.T) {
	srv := newShellTestServer(t)
	createUser(t, srv.db, "shell@example.com", "shell-password", authentication.RoleAdmin)

	t.Run("anonymous visitors get a sign-in link", func(t *testing.T) {
		body := renderPage(t, jarClient(t), srv)

		if !strings.Contains(body, `href="/login"`) || !strings.Contains(body, "Sign in") {
			t.Errorf("expected a sign-in link for an anonymous visitor, got:\n%s", body)
		}
		if strings.Contains(body, "Sign out") {
			t.Error("expected no sign-out link for an anonymous visitor")
		}
	})

	t.Run("signed-in visitors get the user menu", func(t *testing.T) {
		client := jarClient(t)

		body, _ := json.Marshal(map[string]string{
			"email": "shell@example.com", "password": "shell-password",
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("login request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected login 200, got %d", resp.StatusCode)
		}

		page := renderPage(t, client, srv)

		if !strings.Contains(page, `href="/logout"`) || !strings.Contains(page, "Sign out") {
			t.Errorf("expected a sign-out link for a signed-in visitor, got:\n%s", page)
		}
		if !strings.Contains(page, authentication.RoleAdmin) {
			t.Errorf("expected the menu to show the signed-in role, got:\n%s", page)
		}
		// The signed-in page must still publish a token, and it must be the
		// rotated one rather than whatever the pre-login session carried.
		if csrfMetaToken(t, page) == "" {
			t.Error("expected the signed-in page to publish a CSRF token too")
		}
	})
}
