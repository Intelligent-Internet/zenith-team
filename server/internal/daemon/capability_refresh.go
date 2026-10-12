package daemon

import (
	"context"
	"errors"
	"sync"
)

// capabilityRefreshType says how much one provider refresh must do. The
// values are ordered: when requests coalesce, the strongest pending type wins,
// so a later weaker request never downgrades a stronger one.
type capabilityRefreshType int

const (
	// capabilityRefreshTypeChanged publishes only to assignments whose
	// published digest differs, reusing a cached catalog when one exists.
	capabilityRefreshTypeChanged capabilityRefreshType = iota
	// capabilityRefreshTypeReobserve re-reads the provider without bypassing
	// its caches and renews every assignment's discovery freshness.
	capabilityRefreshTypeReobserve
	// capabilityRefreshTypeLinked re-reads which accounts are linked with
	// the provider's caches bypassed, reusing the rest as a reobserve does.
	// The desktop asks for it once an app it watched connects.
	capabilityRefreshTypeLinked
	// capabilityRefreshTypeForced re-reads the provider with its caches
	// bypassed; a user refresh or a native change notification asks for it.
	capabilityRefreshTypeForced
)

// capabilityRefreshQueue coalesces provider notifications onto one owned
// worker. Its stop function cancels and joins that worker before returning.
type capabilityRefreshQueue struct {
	mu       sync.Mutex
	queue    chan string
	ctx      context.Context
	cancel   context.CancelFunc
	pending  map[string]bool
	types    map[string]capabilityRefreshType
	versions map[string]uint64
	refresh  func(context.Context, string, capabilityRefreshType) error
	report   func(error)
	workers  sync.WaitGroup
}

func newCapabilityRefreshQueue(
	refresh func(context.Context, string, capabilityRefreshType) error,
) *capabilityRefreshQueue {
	return &capabilityRefreshQueue{
		pending:  make(map[string]bool),
		types:    make(map[string]capabilityRefreshType),
		versions: make(map[string]uint64),
		refresh:  refresh,
	}
}

func (q *capabilityRefreshQueue) start(parent context.Context, report func(error)) func() {
	q.mu.Lock()
	if q.cancel != nil {
		q.mu.Unlock()
		return func() {}
	}
	q.ctx, q.cancel = context.WithCancel(parent)
	q.queue = make(chan string, 8)
	q.report = report
	ctx := q.ctx
	queue := q.queue
	q.workers.Add(1)
	q.mu.Unlock()

	go func() {
		defer q.workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case provider := <-queue:
				q.run(ctx, provider)
			}
		}
	}()
	return func() {
		q.mu.Lock()
		cancel := q.cancel
		q.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		q.workers.Wait()
		q.mu.Lock()
		q.ctx = nil
		q.cancel = nil
		q.queue = nil
		q.report = nil
		clear(q.pending)
		clear(q.types)
		clear(q.versions)
		q.mu.Unlock()
	}
}

func (q *capabilityRefreshQueue) schedule(provider string, refreshType capabilityRefreshType) error {
	q.mu.Lock()
	if q.ctx == nil || q.queue == nil {
		q.mu.Unlock()
		return errors.New("capability refresh worker is not running")
	}
	if current, ok := q.types[provider]; !ok || refreshType > current {
		q.types[provider] = refreshType
	}
	q.versions[provider]++
	if q.pending[provider] {
		q.mu.Unlock()
		return nil
	}
	q.pending[provider] = true
	queue := q.queue
	ctx := q.ctx
	q.mu.Unlock()

	select {
	case queue <- provider:
		return nil
	case <-ctx.Done():
		q.mu.Lock()
		delete(q.pending, provider)
		q.mu.Unlock()
		return ctx.Err()
	default:
		q.mu.Lock()
		delete(q.pending, provider)
		q.mu.Unlock()
		return errors.New("capability refresh queue is full")
	}
}

func (q *capabilityRefreshQueue) run(ctx context.Context, provider string) {
	q.mu.Lock()
	refreshType := q.types[provider]
	delete(q.types, provider)
	version := q.versions[provider]
	report := q.report
	q.mu.Unlock()

	err := q.refresh(ctx, provider, refreshType)
	if err != nil && report != nil && !errors.Is(err, context.Canceled) {
		report(err)
	}

	q.mu.Lock()
	runAgain := ctx.Err() == nil && q.versions[provider] != version
	if !runAgain {
		delete(q.pending, provider)
	}
	queue := q.queue
	q.mu.Unlock()
	if runAgain {
		select {
		case queue <- provider:
		case <-ctx.Done():
		}
	}
}
