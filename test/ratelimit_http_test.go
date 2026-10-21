package test

import (
	"net/http"
	"testing"
)

// TestGuestWritesAreRateLimited: a hold endpoint with no per-buyer cap and no
// rate limit is a loop away from an empty salon.
func TestGuestWritesAreRateLimited(t *testing.T) {
	t.Setenv("RATE_LIMIT_GUEST_RPS", "1")
	t.Setenv("RATE_LIMIT_GUEST_BURST", "2")
	ts := newTheaterServer(t)
	slug, _ := firstSeated(t, ts)
	client := newJarClient()

	codes := []int{}
	for i := 0; i < 4; i++ {
		resp := doJSON(t, client, "POST", ts.URL+"/events/"+slug+"/holds/release", map[string]any{"hold_tokens": []string{"nope"}})
		codes = append(codes, resp.StatusCode)
		resp.Body.Close()
	}
	if codes[0] == http.StatusTooManyRequests || codes[3] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v; want the first to pass and the fourth to be 429", codes)
	}
}
