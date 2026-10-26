package booking

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSnapshotCacheCoalescesConcurrentReaders is the load property the seat map
// rests on: a thousand people watching one showing must cost one query per
// change, not a thousand.
func TestSnapshotCacheCoalescesConcurrentReaders(t *testing.T) {
	var loads int64
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		atomic.AddInt64(&loads, 1)
		return []seatView{{ID: 1, Label: "A-1", State: "free"}}, nil
	})

	const readers = 500
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = cache.Snapshot("event:7", 1)
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&loads); got != 1 {
		t.Errorf("%d readers of one unchanged version caused %d queries, want 1", readers, got)
	}
}

// TestSnapshotCacheRefreshesOncePerVersion: a change costs exactly one query no
// matter how many connections notice it.
func TestSnapshotCacheRefreshesOncePerVersion(t *testing.T) {
	var loads int64
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		n := atomic.AddInt64(&loads, 1)
		return []seatView{{ID: 1, Label: "A-1", State: "free", Number: int(n)}}, nil
	})

	_, _ = cache.Snapshot("event:7", 1)

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cache.Snapshot("event:7", 2)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&loads); got != 2 {
		t.Errorf("one new version caused %d total queries, want 2", got)
	}
}

// TestSnapshotCacheKeepsEventsApart: events must not share a snapshot.
func TestSnapshotCacheKeepsEventsApart(t *testing.T) {
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		return []seatView{{ID: 1, Label: key, State: "free"}}, nil
	})

	a, _ := cache.Snapshot("event:1", 1)
	b, _ := cache.Snapshot("event:2", 1)

	if string(a.JSON) == string(b.JSON) {
		t.Error("two events served the same snapshot")
	}
	if a.ETag == b.ETag {
		t.Error("two events served the same ETag")
	}
}

// TestSnapshotCacheETagTracksContent: the polling fallback answers 304 off this, so
// it has to change when the seats do and hold still when they do not.
func TestSnapshotCacheETagTracksContent(t *testing.T) {
	state := "free"
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		return []seatView{{ID: 1, Label: "A-1", State: state}}, nil
	})

	first, _ := cache.Snapshot("event:7", 1)
	again, _ := cache.Snapshot("event:7", 1)
	if first.ETag != again.ETag {
		t.Error("ETag changed without a new version")
	}

	state = "held"
	changed, _ := cache.Snapshot("event:7", 2)
	if changed.ETag == first.ETag {
		t.Error("ETag did not change when the seats did")
	}
}

// TestSnapshotCacheBumpBeatsAStaleVersion covers read-your-own-write: this process
// changed the seats, so the next read must not answer from before that change,
// even while the version it was given stands still.
func TestSnapshotCacheBumpBeatsAStaleVersion(t *testing.T) {
	state := "free"
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		return []seatView{{ID: 1, Label: "A-1", State: state}}, nil
	})

	before, _ := cache.Snapshot("event:7", 1)

	state = "held"
	cache.Bump("event:7")
	after, _ := cache.Snapshot("event:7", 1)

	if string(after.JSON) == string(before.JSON) {
		t.Error("a read after a local write returned the snapshot from before it")
	}
}

// TestSnapshotCacheKeepsLastGoodOnFailure: a transient database error must not
// become a cached, ETagged, empty seat map that every reader is then served
// until the next notification happens to arrive.
func TestSnapshotCacheKeepsLastGoodOnFailure(t *testing.T) {
	fail := false
	var loads int64
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		atomic.AddInt64(&loads, 1)
		if fail {
			return nil, errors.New("connection reset")
		}
		return []seatView{{ID: 1, Label: "A-1", State: "free"}}, nil
	})

	good, err := cache.Snapshot("event:7", 1)
	if err != nil {
		t.Fatal(err)
	}

	fail = true
	got, err := cache.Snapshot("event:7", 2)
	if err != nil {
		t.Fatalf("a failed refresh with a good snapshot in hand should not error, got %v", err)
	}
	if string(got.JSON) != string(good.JSON) || got.ETag != good.ETag {
		t.Fatal("a failed load replaced the last good snapshot")
	}

	// The failure was not cached: the next read at the same version tries again.
	fail = false
	if _, err := cache.Snapshot("event:7", 2); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&loads); got != 3 {
		t.Fatalf("want 3 loads (good, failed, retried), got %d", got)
	}
}

// TestSnapshotCacheReportsFirstLoadFailure: with nothing good to fall back on,
// the caller has to be told, so it can answer 503 instead of "[]".
func TestSnapshotCacheReportsFirstLoadFailure(t *testing.T) {
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		return nil, errors.New("connection reset")
	})
	if _, err := cache.Snapshot("event:7", 1); err == nil {
		t.Fatal("first load failed but Snapshot returned no error")
	}
}

// TestSnapshotCacheLoadHasADeadline: one stuck query must not pin every reader
// of that key forever.
func TestSnapshotCacheLoadHasADeadline(t *testing.T) {
	cache := newSnapshotCache(func(ctx context.Context, key string) ([]seatView, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	start := time.Now()
	_, err := cache.Snapshot("event:7", 1)
	if err == nil {
		t.Fatal("a load that never returns should surface as an error")
	}
	if elapsed := time.Since(start); elapsed > snapshotLoadTimeout+2*time.Second {
		t.Fatalf("load blocked for %s, want about %s", elapsed, snapshotLoadTimeout)
	}
}
