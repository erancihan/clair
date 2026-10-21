package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestPerIPAllowsBurstThenRefuses(t *testing.T) {
	h := PerIP(1, 3)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	codes := make([]int, 0, 5)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/x", nil)
		req.RemoteAddr = "203.0.113.7:4444"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	want := []int{204, 204, 204, 429, 429}
	for i := range want {
		if codes[i] != want[i] {
			t.Fatalf("request %d: got %d, want %d (all: %v)", i+1, codes[i], want[i], codes)
		}
	}
}

func TestPerIPKeepsClientsApart(t *testing.T) {
	h := PerIP(1, 1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, ip := range []string{"203.0.113.1:1", "203.0.113.2:1"} {
		req := httptest.NewRequest("POST", "/x", nil)
		req.RemoteAddr = ip
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 204 {
			t.Fatalf("first request from %s got %d", ip, rec.Code)
		}
	}
}

func TestPerIPRefills(t *testing.T) {
	h := PerIP(100, 1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	req := httptest.NewRequest("POST", "/x", nil)
	req.RemoteAddr = "203.0.113.9:1"
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)
	if second.Code != 429 {
		t.Fatalf("second immediate request got %d, want 429", second.Code)
	}
	time.Sleep(30 * time.Millisecond)
	third := httptest.NewRecorder()
	h.ServeHTTP(third, req)
	if third.Code != 204 {
		t.Fatalf("after refill got %d, want 204", third.Code)
	}
}

func TestMaxConcurrentPerIP(t *testing.T) {
	release := make(chan struct{})
	h := MaxConcurrentPerIP(2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(204)
	}))
	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/stream", nil)
			req.RemoteAddr = "203.0.113.5:1"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // both in flight
	req := httptest.NewRequest("GET", "/stream", nil)
	req.RemoteAddr = "203.0.113.5:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	codes[2] = rec.Code
	close(release)
	wg.Wait()
	if codes[2] != 429 || codes[0] != 204 || codes[1] != 204 {
		t.Fatalf("codes = %v, want [204 204 429]", codes)
	}
}

func TestClientIPHonoursForwardedForOnlyWhenTrusted(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.4, 10.0.0.2")
	if got := ClientIP(req); got != "10.0.0.2" {
		t.Fatalf("untrusted: got %q, want the socket address", got)
	}
	t.Setenv("TRUST_PROXY_HEADERS", "true")
	if got := ClientIP(req); got != "198.51.100.4" {
		t.Fatalf("trusted: got %q, want the first forwarded hop", got)
	}
}
