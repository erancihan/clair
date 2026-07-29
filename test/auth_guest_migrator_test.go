package test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	authentication "github.com/erancihan/clair/internal/server/authentication"
	"gorm.io/gorm"
)

// migrationCall is one invocation of a fake migrator.
type migrationCall struct {
	migrator string
	guestRef string
	userRef  string
}

// migratorRecorder collects the calls fake migrators receive.
//
// The registry is process-wide and has no removal, which is deliberate — domains
// register once at startup. A test therefore filters by the guest reference it
// owns, so calls made by other tests in this binary cannot affect its
// assertions.
type migratorRecorder struct {
	mu    sync.Mutex
	calls []migrationCall
}

// record appends a call. Safe to call from any migrator.
func (r *migratorRecorder) record(call migrationCall) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, call)
}

// callsFor returns the recorded calls carrying the given guest reference.
func (r *migratorRecorder) callsFor(guestRef string) []migrationCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matched []migrationCall
	for _, call := range r.calls {
		if call.guestRef == guestRef {
			matched = append(matched, call)
		}
	}

	return matched
}

// recorded is shared by every migrator registered below, so one login produces
// one comparable list of calls across all of them.
var recorded = &migratorRecorder{}

// registerFakeMigrators registers three migrators, once for the whole test
// binary: one that fails, one that panics, and one that succeeds — in that
// order, so the successful one only records if neither of the others aborted the
// chain.
var registerFakeMigrators = sync.OnceFunc(func() {
	record := func(name string) func(context.Context, *gorm.DB, string, string) {
		return func(_ context.Context, _ *gorm.DB, guestRef, userRef string) {
			recorded.record(migrationCall{migrator: name, guestRef: guestRef, userRef: userRef})
		}
	}

	failing := record("failing")
	authentication.RegisterGuestMigrator("failing", func(ctx context.Context, db *gorm.DB, guestRef, userRef string) error {
		failing(ctx, db, guestRef, userRef)
		return errors.New("migration blew up")
	})

	panicking := record("panicking")
	authentication.RegisterGuestMigrator("panicking", func(ctx context.Context, db *gorm.DB, guestRef, userRef string) error {
		panicking(ctx, db, guestRef, userRef)
		panic("migration panicked")
	})

	succeeding := record("succeeding")
	authentication.RegisterGuestMigrator("succeeding", func(ctx context.Context, db *gorm.DB, guestRef, userRef string) error {
		succeeding(ctx, db, guestRef, userRef)
		return nil
	})
})

// guestSession performs the GET /owner handshake, returning the visitor's guest
// owner reference and the "sid" cookie that carries it.
func guestSession(t *testing.T, srv *authTestServer) (string, *http.Cookie) {
	t.Helper()

	resp, err := http.Get(srv.URL + "/owner")
	if err != nil {
		t.Fatalf("owner request failed: %v", err)
	}
	defer resp.Body.Close()

	guestRef := readBody(t, resp)
	if !strings.HasPrefix(guestRef, "guest:") {
		t.Fatalf("expected a guest owner reference, got %q", guestRef)
	}

	sid := findCookie(resp, "sid")
	if sid == nil {
		t.Fatal("expected the owner handshake to set a sid cookie")
	}

	return guestRef, sid
}

// loginJSONWithCookies signs in over JSON while presenting cookies, and returns
// the response so the caller can assert on the login contract itself.
func loginJSONWithCookies(t *testing.T, srv *authTestServer, email, password string, cookies ...*http.Cookie) *http.Response {
	t.Helper()

	body := strings.NewReader(fmt.Sprintf(`{"email":%q,"password":%q}`, email, password))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login", body)
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}

	return resp
}

// TestGuestMigratorsRunOnLogin covers the hand-over from a guest identity to an
// account: the migrators run with both owner references, and — the requirement
// that matters — none of them can cost the visitor their login.
func TestGuestMigratorsRunOnLogin(t *testing.T) {
	registerFakeMigrators()

	srv := newAuthTestServer(t)
	user := createUser(t, srv.db, "guest-merge@example.com", "guest-merge-password", "")

	guestRef, sid := guestSession(t, srv)

	resp := loginJSONWithCookies(t, srv, "guest-merge@example.com", "guest-merge-password", sid)
	defer resp.Body.Close()

	t.Run("a failing and a panicking migrator do not break the login", func(t *testing.T) {
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected login to still return 200, got %d", resp.StatusCode)
		}
		if body := readBody(t, resp); body != "" {
			t.Errorf("expected a bare 200 from a JSON login, got body %q", body)
		}
	})

	calls := recorded.callsFor(guestRef)

	t.Run("every registered migrator runs", func(t *testing.T) {
		if len(calls) != 3 {
			t.Fatalf("expected all 3 migrators to run, got %d: %+v", len(calls), calls)
		}

		// The successful migrator is registered last, so its call is the proof
		// that neither the error nor the panic aborted the chain.
		if calls[len(calls)-1].migrator != "succeeding" {
			t.Errorf("expected the chain to continue past the failures, got %+v", calls)
		}
	})

	t.Run("both owner references are supplied", func(t *testing.T) {
		wantUserRef := fmt.Sprintf("user:%d", user.ID)

		for _, call := range calls {
			if call.guestRef != guestRef {
				t.Errorf("%s: expected guest ref %q, got %q", call.migrator, guestRef, call.guestRef)
			}
			if call.userRef != wantUserRef {
				t.Errorf("%s: expected user ref %q, got %q", call.migrator, wantUserRef, call.userRef)
			}
		}
	})
}

// TestGuestMigratorsSkippedWithoutGuestSession covers the other half: a visitor
// who arrives with no "sid" cookie has no guest identity, so there is nothing to
// migrate and nothing should be invoked — nor should the login start minting a
// guest cookie just to have a reference to pass.
func TestGuestMigratorsSkippedWithoutGuestSession(t *testing.T) {
	registerFakeMigrators()

	srv := newAuthTestServer(t)
	createUser(t, srv.db, "no-guest@example.com", "no-guest-password", "")

	before := len(recorded.callsFor(""))

	resp := loginJSONWithCookies(t, srv, "no-guest@example.com", "no-guest-password")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected login 200, got %d", resp.StatusCode)
	}

	if after := len(recorded.callsFor("")); after != before {
		t.Errorf("expected no migrator calls without a guest session, got %d new", after-before)
	}

	if findCookie(resp, "sid") != nil {
		t.Error("expected login not to mint a guest sid cookie")
	}
}
