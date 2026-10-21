package booking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// snapshotLoadTimeout bounds one shared load. The entry's lock is held for the
// duration, so a load that hung would hold every reader of that key with it.
const snapshotLoadTimeout = 5 * time.Second

// snapshot is one rendered read's seat map, already rendered.
//
// The bytes are marshalled once and handed to every reader. Under load that is
// the difference that matters: a thousand watchers of one showing serialise
// nothing, they write out a slice somebody else already produced.
type snapshot struct {
	JSON []byte
	ETag string
}

// snapshotCache turns "every connection reads this" into "this is read once per
// change".
//
// The seat map is a three-table join. Serving it per connection means one hold
// on a busy showing fans out into a query per watcher, all returning the same
// rows — the classic thundering herd, and the point at which a seat map stops
// being a feature and becomes an outage.
//
// Coherence comes from a version supplied by the caller rather than from a
// timer: the notification listener counts changes, so every reader that learns
// about change N asks for version N, and exactly one of them pays for it. When
// there is no listener the caller passes a coarse clock instead, which bounds
// the same herd to a couple of queries a second.
//
// A load that fails does not replace what the cache holds. Readers get the last
// good snapshot and the stamp stays behind, so the next reader tries again; a
// transient error costs one stale frame, not a cached empty map.
type snapshotCache[T any] struct {
	load func(ctx context.Context, key string) (T, error)

	mu      sync.Mutex
	entries map[string]*snapshotCacheEntry
}

type snapshotCacheEntry struct {
	// Held across the load on purpose: concurrent readers of the same stale
	// version wait for the one query in flight instead of starting their own.
	mu     sync.Mutex
	stamp  uint64
	local  uint64
	loaded bool
	snap   snapshot
}

func newSnapshotCache[T any](load func(ctx context.Context, key string) (T, error)) *snapshotCache[T] {
	return &snapshotCache[T]{load: load, entries: map[string]*snapshotCacheEntry{}}
}

// Snapshot returns the rendered state for a key at the given version, reading
// the database only when what it holds is older than that. On a failed read it
// returns the last good snapshot if there is one, and the error otherwise.
func (c *snapshotCache[T]) Snapshot(key string, version uint64) (snapshot, error) {
	entry := c.entryFor(key)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	stamp := version + entry.local
	if entry.loaded && entry.stamp == stamp {
		return entry.snap, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), snapshotLoadTimeout)
	defer cancel()
	value, err := c.load(ctx, key)
	if err != nil {
		if entry.loaded {
			return entry.snap, nil
		}
		return snapshot{}, err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		if entry.loaded {
			return entry.snap, nil
		}
		return snapshot{}, err
	}
	sum := sha256.Sum256(payload)

	entry.snap = snapshot{JSON: payload, ETag: `W/"` + hex.EncodeToString(sum[:12]) + `"`}
	entry.stamp = stamp
	entry.loaded = true
	return entry.snap, nil
}

// Bump records that this process just changed an event's seats.
//
// The notification listener is the usual way a change is noticed, but it is not
// instant and it may be absent altogether. Without this, a buyer who holds a
// seat and immediately re-reads the map could be handed the snapshot from before
// their own write — the one thing a cache must never do.
func (c *snapshotCache[T]) Bump(key string) {
	entry := c.entryFor(key)
	entry.mu.Lock()
	entry.local++
	entry.mu.Unlock()
}

func (c *snapshotCache[T]) entryFor(key string) *snapshotCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := c.entries[key]
	if entry == nil {
		entry = &snapshotCacheEntry{}
		c.entries[key] = entry
	}
	return entry
}

// eventKey and scheduleKey name a cache entry. They match the notification
// payloads exactly, so a topic and a cache key are never out of step.
func eventKey(id uint) string    { return fmt.Sprintf("event:%d", id) }
func scheduleKey(id uint) string { return fmt.Sprintf("schedule:%d", id) }

// eventKeyID and scheduleKeyID read the id back out of a key.
func eventKeyID(key string) uint {
	var id uint64
	_, _ = fmt.Sscanf(key, "event:%d", &id)
	return uint(id)
}

func scheduleKeyID(key string) uint {
	var id uint64
	_, _ = fmt.Sscanf(key, "schedule:%d", &id)
	return uint(id)
}
