// Package live turns Postgres change notifications into fan-out for streaming
// handlers.
//
// It exists because seat state is written from more than one process, so a
// stream cannot be fed from the memory of the process serving it. Postgres is
// already the thing every writer shares; LISTEN/NOTIFY makes it the thing every
// reader shares too, without adding a broker to the deployment.
package live

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// Hub holds one Postgres listener and fans its notifications out to subscribers
// by topic.
//
// The listener is reference-counted: it connects when the first subscriber
// arrives and disconnects when the last one leaves. Nobody watching means no
// connection held, which matters because a test suite builds this router dozens
// of times.
type Hub struct {
	dsn    string
	logger *zap.Logger

	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
	// versions counts changes per topic for the life of the process. It is
	// never pruned on purpose: the snapshot cache compares stamps against it,
	// and a topic whose counter restarted at zero could hand a reader a stale
	// entry whose old stamp happens to match. One integer per topic ever seen
	// is the price, and that is bounded by the catalog, not by traffic.
	versions map[string]uint64
	cancel   context.CancelFunc
	running  bool
	// closed is set by Close and never cleared: a hub that has been told to stop
	// does not reconnect because a late subscriber arrived.
	closed bool

	connected atomic.Bool
	// everConnected distinguishes the first connection from a reconnect. Only
	// a reconnect can have missed anything.
	everConnected atomic.Bool
}

func NewHub(dsn string, logger *zap.Logger) *Hub {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Hub{
		dsn:      dsn,
		logger:   logger,
		subs:     map[string]map[chan struct{}]struct{}{},
		versions: map[string]uint64{},
	}
}

// Subscribe returns a channel that receives a signal whenever `topic` changes,
// plus the function to release it.
//
// The channel carries no payload and coalesces: it says "this changed", and the
// reader answers by re-reading the current state. A dropped duplicate signal is
// therefore harmless, which is what lets the send be non-blocking.
func (h *Hub) Subscribe(topic string) (<-chan struct{}, func()) {
	return h.SubscribeAll([]string{topic})
}

// SubscribeAll registers one channel against many topics.
//
// A board watching every showing of a theater is one reader, so it gets one
// channel and one connection. Subscribing per topic would leave the caller
// selecting over a dozen channels, and would tempt a browser into a stream per
// showing — which the connection-per-origin cap makes unworkable.
func (h *Hub) SubscribeAll(topics []string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	h.mu.Lock()
	for _, topic := range topics {
		if h.subs[topic] == nil {
			h.subs[topic] = map[chan struct{}]struct{}{}
		}
		h.subs[topic][ch] = struct{}{}
	}
	if !h.running && !h.closed && h.dsn != "" {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel, h.running = cancel, true
		go h.listen(ctx)
	}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()

			for _, topic := range topics {
				delete(h.subs[topic], ch)
				if len(h.subs[topic]) == 0 {
					delete(h.subs, topic)
				}
			}
			if len(h.subs) == 0 && h.running {
				h.cancel()
				h.running = false
				h.connected.Store(false)
				h.everConnected.Store(false)
			}
		})
	}
}

// Close stops the listener for good. Subscribers keep their channels (they
// end on their own when their handlers return) but nothing will signal them.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.running {
		h.cancel()
		h.running = false
		h.connected.Store(false)
	}
}

// Ready reports whether the listener is connected, waiting up to timeout for it
// to come up. A caller that cannot get a live listener should say so rather than
// serve a stream that will never carry anything.
func (h *Hub) Ready(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if h.connected.Load() {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (h *Hub) publish(topic string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Counted once per notification, not once per subscriber. Readers use it to
	// agree on which snapshot they are asking for, so one change costs one read
	// however many of them there are.
	h.versions[topic]++

	for ch := range h.subs[topic] {
		select {
		case ch <- struct{}{}:
		default: // a signal is already pending; one is as good as two
		}
	}
}

// Version reports how many changes this topic has announced, and whether the
// listener is live. A caller that gets live=false is on its own for freshness:
// nothing will tell it when this number should move.
func (h *Hub) Version(topic string) (uint64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.versions[topic], h.connected.Load()
}

// listen keeps one connection LISTENing, reconnecting with backoff. A dropped
// connection is a lost update, not a lost subscriber, so subscribers stay
// attached across a reconnect and simply re-read when it returns.
func (h *Hub) listen(ctx context.Context) {
	backoff := 250 * time.Millisecond
	const maxBackoff = 10 * time.Second

	for ctx.Err() == nil {
		if err := h.listenOnce(ctx); err != nil && ctx.Err() == nil {
			h.logger.Warn("seat listener dropped", zap.Error(err))
		}
		h.connected.Store(false)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

func (h *Hub) listenOnce(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, h.dsn)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	if _, err := conn.Exec(ctx, "LISTEN "+booking.NotifyChannel); err != nil {
		return err
	}
	h.connected.Store(true)
	if h.everConnected.Swap(true) {
		// Anything announced while we were away is gone; tell every subscriber
		// to re-read rather than let them serve the last thing they saw.
		h.logger.Info("seat listener reconnected; resyncing subscribers")
		h.republishAll()
	} else {
		h.logger.Info("seat listener connected")
	}

	for {
		note, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		h.publish(note.Payload)
	}
}

// republishAll wakes every subscribed topic once. Used after a reconnect: the
// listener cannot know what it missed, so every reader re-reads.
func (h *Hub) republishAll() {
	h.mu.Lock()
	topics := make([]string, 0, len(h.subs))
	for topic := range h.subs {
		topics = append(topics, topic)
	}
	h.mu.Unlock()
	for _, topic := range topics {
		h.publish(topic)
	}
}
