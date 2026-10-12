package daemon

import (
	"sync"
	"time"
)

// reconcileBroadcaster fans out a "reconcile now" signal to any number of
// listeners using the close-and-replace channel pattern. Subscribers call
// notify() to obtain a snapshot channel that closes on the next broadcast.
//
// It exists because the run-cancellation watchers park on a coarse 5s
// ticker: when the daemon's WS connection drops and reconnects, anything the
// server changed during the gap would stay invisible until the next tick.
// broadcast() lets the WS connect path nudge every waiter immediately.
//
// Edge-triggered with one-slot replay: a broadcast with no subscribers makes
// the next notify() return an already-closed channel, so a late subscriber
// observes the missed event exactly once. broadcast() debounces inside
// minBroadcastInterval so a flapping connection cannot stampede the server.
type reconcileBroadcaster struct {
	mu                   sync.Mutex
	ch                   chan struct{}
	pending              bool
	lastBroadcast        time.Time
	minBroadcastInterval time.Duration
	now                  func() time.Time
}

func newReconcileBroadcaster() *reconcileBroadcaster {
	return &reconcileBroadcaster{
		ch:                   make(chan struct{}),
		minBroadcastInterval: time.Second,
		now:                  time.Now,
	}
}

// notify returns a channel that closes on the next broadcast. The returned
// channel is single-shot: subscribers call notify() again after waking.
func (b *reconcileBroadcaster) notify() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending {
		b.pending = false
		replay := make(chan struct{})
		close(replay)
		return replay
	}
	return b.ch
}

// broadcast wakes every current subscriber, then installs a fresh channel.
// Calls within minBroadcastInterval of the previous broadcast are dropped;
// the return value reports whether the signal fired.
func (b *reconcileBroadcaster) broadcast() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if !b.lastBroadcast.IsZero() && now.Sub(b.lastBroadcast) < b.minBroadcastInterval {
		return false
	}
	b.lastBroadcast = now
	b.pending = true
	close(b.ch)
	b.ch = make(chan struct{})
	return true
}
