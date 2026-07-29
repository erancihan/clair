package test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/erancihan/clair/internal/utils/router"
)

// tagMiddleware returns middleware that appends tag to the X-Tag response header,
// so a test can read back exactly which middleware ran for a route.
func tagMiddleware(tag string) router.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("X-Tag", tag)
			next.ServeHTTP(w, r)
		})
	}
}

// tagsFor drives one request through mux and returns the tags the middleware
// chain recorded.
func tagsFor(mux *router.Router, path string) []string {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	return rec.Result().Header.Values("X-Tag")
}

// TestSubRoutersDoNotShareMiddleware pins the isolation guarantee every domain
// relies on: two sub-routers derived from the same parent must each see only
// their own middleware. Deriving with a plain append would reuse the parent
// slice's spare capacity, letting the second derivation overwrite the first's
// middleware — so a domain mounting its own group could silently strip or
// inherit another domain's.
func TestSubRoutersDoNotShareMiddleware(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

	// newParent returns a router whose middleware slice has spare capacity, which
	// is the condition under which the sharing bug appears at all. Separate Use
	// calls grow the slice geometrically, leaving len < cap.
	newParent := func() *router.Router {
		mux := router.NewRouter()
		mux.Use(tagMiddleware("g1"))
		mux.Use(tagMiddleware("g2"))
		mux.Use(tagMiddleware("g3"))

		return mux
	}

	t.Run("Middleware derivations stay independent", func(t *testing.T) {
		mux := newParent()

		// Derive both sub-routers before registering anything, which is what a
		// domain does when it keeps a guarded router around to hang routes off.
		first := mux.Middleware(tagMiddleware("first"))
		second := mux.Middleware(tagMiddleware("second"))

		first.HandleFunc("GET /first", ok)
		second.HandleFunc("GET /second", ok)

		if got := tagsFor(mux, "/first"); len(got) != 4 || got[3] != "first" {
			t.Errorf("expected /first to end with the first middleware, got %v", got)
		}
		if got := tagsFor(mux, "/second"); len(got) != 4 || got[3] != "second" {
			t.Errorf("expected /second to end with the second middleware, got %v", got)
		}
	})

	t.Run("sibling groups stay independent", func(t *testing.T) {
		mux := newParent()

		mux.Group("alpha", func(r *router.Router) {
			r.HandleFunc("GET /", ok)
		}, tagMiddleware("alpha"))

		mux.Group("beta", func(r *router.Router) {
			r.HandleFunc("GET /", ok)
		}, tagMiddleware("beta"))

		if got := tagsFor(mux, "/alpha/"); len(got) != 4 || got[3] != "alpha" {
			t.Errorf("expected /alpha/ to end with the alpha middleware, got %v", got)
		}
		if got := tagsFor(mux, "/beta/"); len(got) != 4 || got[3] != "beta" {
			t.Errorf("expected /beta/ to end with the beta middleware, got %v", got)
		}
	})

	t.Run("a group does not leak middleware back to its parent", func(t *testing.T) {
		mux := newParent()

		mux.Group("guarded", func(r *router.Router) {
			r.HandleFunc("GET /", ok)
		}, tagMiddleware("guard"))

		mux.HandleFunc("GET /open", ok)

		if got := tagsFor(mux, "/open"); len(got) != 3 {
			t.Errorf("expected /open to run only the three global middleware, got %v", got)
		}
	})
}
