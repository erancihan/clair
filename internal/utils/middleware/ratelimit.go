package middleware

import (
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ClientIP is the address a limit is keyed on. It is the socket's peer unless
// TRUST_PROXY_HEADERS=true, in which case the first X-Forwarded-For hop is
// taken instead — trusting that header from an untrusted peer would let a
// caller pick its own bucket.
func ClientIP(r *http.Request) string {
	if os.Getenv("TRUST_PROXY_HEADERS") == "true" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type bucket struct {
	tokens float64
	last   time.Time
}

// PerIP is a token bucket per client address: `burst` requests at once, then
// `rps` a second. Buckets idle for ten minutes are dropped on the next sweep,
// so the map is bounded by recent callers rather than by every address ever
// seen.
func PerIP(rps float64, burst int) func(http.Handler) http.Handler {
	var (
		mu      sync.Mutex
		buckets = map[string]*bucket{}
		sweeps  int
	)
	const idle = 10 * time.Minute

	allow := func(ip string, now time.Time) bool {
		mu.Lock()
		defer mu.Unlock()

		if sweeps++; sweeps%1000 == 0 {
			for k, b := range buckets {
				if now.Sub(b.last) > idle {
					delete(buckets, k)
				}
			}
		}

		b := buckets[ip]
		if b == nil {
			b = &bucket{tokens: float64(burst), last: now}
			buckets[ip] = b
		}
		b.tokens += now.Sub(b.last).Seconds() * rps
		if b.tokens > float64(burst) {
			b.tokens = float64(burst)
		}
		b.last = now
		if b.tokens < 1 {
			return false
		}
		b.tokens--
		return true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !allow(ClientIP(r), time.Now()) {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MaxConcurrentPerIP caps how many requests one address has in flight. It is
// for streams: each one holds a connection for as long as the tab is open, and
// a browser is allowed about six per origin, so a dozen from one address is a
// script, not a person.
func MaxConcurrentPerIP(n int) func(http.Handler) http.Handler {
	var (
		mu       sync.Mutex
		inflight = map[string]int{}
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r)
			mu.Lock()
			if inflight[ip] >= n {
				mu.Unlock()
				w.Header().Set("Retry-After", "5")
				http.Error(w, "too many open streams", http.StatusTooManyRequests)
				return
			}
			inflight[ip]++
			mu.Unlock()
			defer func() {
				mu.Lock()
				if inflight[ip]--; inflight[ip] <= 0 {
					delete(inflight, ip)
				}
				mu.Unlock()
			}()
			next.ServeHTTP(w, r)
		})
	}
}
