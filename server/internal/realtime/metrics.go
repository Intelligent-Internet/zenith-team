package realtime

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Metrics collects lightweight counters describing the realtime subsystem.
//
// We keep using std-library atomics rather than a Prometheus dependency; a
// future phase can re-export the same numbers.
type Metrics struct {
	DiscussionHintsDroppedTotal atomic.Int64
	ConnectsTotal               atomic.Int64
	DisconnectsTotal            atomic.Int64
	ActiveConnections           atomic.Int64
	SlowEvictionsTotal          atomic.Int64
	MessagesSentTotal           atomic.Int64
	MessagesDroppedTotal        atomic.Int64

	// ProjectFramesDroppedTotal counts frames about a Project (ADR 0017) that
	// never reached a recipient: a batch the broadcaster refused, one
	// recipient's relay write that failed, or a frame staged without the
	// audience its Project decides.
	ProjectFramesDroppedTotal atomic.Int64

	// InboundTooLargeTotal counts connections closed because a peer sent a
	// message over inboundReadLimit.
	InboundTooLargeTotal atomic.Int64

	// Per-event-type send counters keyed by event type string.
	// Value is *atomic.Int64.
	eventSent sync.Map

	// Server-owned identity-room gauges, keyed by company or user scope.
	scopeRooms sync.Map

	// Redis relay counters. Zero unless the Redis broadcaster is enabled.
	RedisXAddTotal         atomic.Int64
	RedisXAddErrors        atomic.Int64
	RedisXReadTotal        atomic.Int64
	RedisXReadErrors       atomic.Int64
	RedisLastXAddLagMicros atomic.Int64

	// RedisConnected is set by the relay on startup / reconnect.
	RedisConnected atomic.Bool
	// RedisLastError stores the most recent consumer error message.
	redisLastErrMu sync.RWMutex
	redisLastErr   string

	// NodeID is set once at boot by the relay (or empty in single-node mode).
	NodeID atomic.Value // string
}

// RedisMetricsSnapshot is the JSON projection of the relay-specific counters.
type RedisMetricsSnapshot struct {
	Connected         bool   `json:"connected"`
	NodeID            string `json:"nodeId"`
	XAddTotal         int64  `json:"xaddTotal"`
	XAddErrors        int64  `json:"xaddErrors"`
	XReadTotal        int64  `json:"xreadTotal"`
	XReadErrors       int64  `json:"xreadErrors"`
	LastXAddLagMicros int64  `json:"lastXaddLagMicros"`
	LastError         string `json:"lastError"`
}

// MetricsSnapshot is a stable JSON-friendly copy of the current counter values.
type MetricsSnapshot struct {
	DiscussionHintsDroppedTotal int64                `json:"discussionHintsDroppedTotal"`
	ProjectFramesDroppedTotal   int64                `json:"projectFramesDroppedTotal"`
	ConnectsTotal               int64                `json:"connectsTotal"`
	DisconnectsTotal            int64                `json:"disconnectsTotal"`
	ActiveConnections           int64                `json:"activeConnections"`
	SlowEvictionsTotal          int64                `json:"slowEvictionsTotal"`
	MessagesSentTotal           int64                `json:"messagesSentTotal"`
	MessagesDroppedTotal        int64                `json:"messagesDroppedTotal"`
	InboundTooLargeTotal        int64                `json:"inboundTooLargeTotal"`
	EventsSentByType            map[string]int64     `json:"eventsSentByType"`
	ActiveScopeRooms            map[string]int64     `json:"activeScopeRooms"`
	Redis                       RedisMetricsSnapshot `json:"redis"`
}

// M is the package-level metrics singleton.
var M = &Metrics{}

// loadOrInitCounter returns the counter stored under key, creating it on first
// use. The maps it serves are unexported and only ever written through here, so
// a value that is not a counter cannot occur; the checked assertions exist so a
// hypothetical bad entry yields a fresh counter instead of a panic in a metrics
// path.
func loadOrInitCounter(m *sync.Map, key string) *atomic.Int64 {
	if v, ok := m.Load(key); ok {
		if counter, ok := v.(*atomic.Int64); ok {
			return counter
		}
	}
	c := new(atomic.Int64)
	if existing, loaded := m.LoadOrStore(key, c); loaded {
		if counter, ok := existing.(*atomic.Int64); ok {
			return counter
		}
	}
	return c
}

// RecordEvent increments the per-event-type send counter.
func (m *Metrics) RecordEvent(eventType string) {
	if eventType == "" {
		return
	}
	loadOrInitCounter(&m.eventSent, eventType).Add(1)
}

// IncRoom / DecRoom adjust the active-rooms gauge for scopeType.
func (m *Metrics) IncRoom(scopeType string) { loadOrInitCounter(&m.scopeRooms, scopeType).Add(1) }
func (m *Metrics) DecRoom(scopeType string) { loadOrInitCounter(&m.scopeRooms, scopeType).Add(-1) }

// SetRedisLastError stores msg as the most recent Redis consumer error. An
// empty msg clears it.
func (m *Metrics) SetRedisLastError(msg string) {
	m.redisLastErrMu.Lock()
	m.redisLastErr = msg
	m.redisLastErrMu.Unlock()
}

func (m *Metrics) lastRedisErr() string {
	m.redisLastErrMu.RLock()
	defer m.redisLastErrMu.RUnlock()
	return m.redisLastErr
}

func snapshotCounters(s *sync.Map) map[string]int64 {
	out := map[string]int64{}
	s.Range(func(k, v any) bool {
		// Entries only ever come from loadOrInitCounter, so both assertions
		// hold; anything else is skipped rather than panicking a snapshot read.
		key, ok := k.(string)
		if !ok {
			return true
		}
		counter, ok := v.(*atomic.Int64)
		if !ok {
			return true
		}
		out[key] = counter.Load()
		return true
	})
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]int64, len(out))
	for _, k := range keys {
		ordered[k] = out[k]
	}
	return ordered
}

// Snapshot returns a JSON-friendly copy of the current counter values.
func (m *Metrics) Snapshot() MetricsSnapshot {
	nodeID := ""
	if v := m.NodeID.Load(); v != nil {
		nodeID, _ = v.(string)
	}
	return MetricsSnapshot{
		DiscussionHintsDroppedTotal: m.DiscussionHintsDroppedTotal.Load(),
		ProjectFramesDroppedTotal:   m.ProjectFramesDroppedTotal.Load(),
		ConnectsTotal:               m.ConnectsTotal.Load(),
		DisconnectsTotal:            m.DisconnectsTotal.Load(),
		ActiveConnections:           m.ActiveConnections.Load(),
		SlowEvictionsTotal:          m.SlowEvictionsTotal.Load(),
		MessagesSentTotal:           m.MessagesSentTotal.Load(),
		MessagesDroppedTotal:        m.MessagesDroppedTotal.Load(),
		InboundTooLargeTotal:        m.InboundTooLargeTotal.Load(),
		EventsSentByType:            snapshotCounters(&m.eventSent),
		ActiveScopeRooms:            snapshotCounters(&m.scopeRooms),
		Redis: RedisMetricsSnapshot{
			Connected:         m.RedisConnected.Load(),
			NodeID:            nodeID,
			XAddTotal:         m.RedisXAddTotal.Load(),
			XAddErrors:        m.RedisXAddErrors.Load(),
			XReadTotal:        m.RedisXReadTotal.Load(),
			XReadErrors:       m.RedisXReadErrors.Load(),
			LastXAddLagMicros: m.RedisLastXAddLagMicros.Load(),
			LastError:         m.lastRedisErr(),
		},
	}
}

// Reset zeroes all counters. Tests only.
func (m *Metrics) Reset() {
	m.ProjectFramesDroppedTotal.Store(0)
	m.ConnectsTotal.Store(0)
	m.DisconnectsTotal.Store(0)
	m.ActiveConnections.Store(0)
	m.SlowEvictionsTotal.Store(0)
	m.MessagesSentTotal.Store(0)
	m.MessagesDroppedTotal.Store(0)
	m.InboundTooLargeTotal.Store(0)
	m.eventSent.Range(func(k, _ any) bool { m.eventSent.Delete(k); return true })
	m.scopeRooms.Range(func(k, _ any) bool { m.scopeRooms.Delete(k); return true })
	m.RedisXAddTotal.Store(0)
	m.RedisXAddErrors.Store(0)
	m.RedisXReadTotal.Store(0)
	m.RedisXReadErrors.Store(0)
	m.RedisLastXAddLagMicros.Store(0)
	m.RedisConnected.Store(false)
	m.SetRedisLastError("")
}
