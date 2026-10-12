package daemonws

import "sync/atomic"

// Metrics collects lightweight counters for the daemon WebSocket control
// plane. Same std-library-atomics pattern as internal/realtime: no Prometheus
// dependency; a future phase can re-export the same numbers.
type Metrics struct {
	ConnectsTotal      atomic.Int64
	DisconnectsTotal   atomic.Int64
	ActiveConnections  atomic.Int64
	SlowEvictionsTotal atomic.Int64

	WakeupPublishedTotal atomic.Int64
	WakeupPublishErrors  atomic.Int64
	WakeupReceivedTotal  atomic.Int64
	WakeupDeliveredHit   atomic.Int64
	WakeupDeliveredMiss  atomic.Int64
}

// MetricsSnapshot is the JSON projection of daemon WebSocket counters.
type MetricsSnapshot struct {
	ConnectsTotal            int64 `json:"connectsTotal"`
	DisconnectsTotal         int64 `json:"disconnectsTotal"`
	ActiveConnections        int64 `json:"activeConnections"`
	SlowEvictionsTotal       int64 `json:"slowEvictionsTotal"`
	WakeupPublishedTotal     int64 `json:"wakeupPublishedTotal"`
	WakeupPublishErrors      int64 `json:"wakeupPublishErrors"`
	WakeupReceivedTotal      int64 `json:"wakeupReceivedTotal"`
	WakeupDeliveredHitTotal  int64 `json:"wakeupDeliveredHitTotal"`
	WakeupDeliveredMissTotal int64 `json:"wakeupDeliveredMissTotal"`
}

// M is the package-level metrics singleton.
var M = &Metrics{}

func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		ConnectsTotal:            m.ConnectsTotal.Load(),
		DisconnectsTotal:         m.DisconnectsTotal.Load(),
		ActiveConnections:        m.ActiveConnections.Load(),
		SlowEvictionsTotal:       m.SlowEvictionsTotal.Load(),
		WakeupPublishedTotal:     m.WakeupPublishedTotal.Load(),
		WakeupPublishErrors:      m.WakeupPublishErrors.Load(),
		WakeupReceivedTotal:      m.WakeupReceivedTotal.Load(),
		WakeupDeliveredHitTotal:  m.WakeupDeliveredHit.Load(),
		WakeupDeliveredMissTotal: m.WakeupDeliveredMiss.Load(),
	}
}

func (m *Metrics) Reset() {
	m.ConnectsTotal.Store(0)
	m.DisconnectsTotal.Store(0)
	m.ActiveConnections.Store(0)
	m.SlowEvictionsTotal.Store(0)
	m.WakeupPublishedTotal.Store(0)
	m.WakeupPublishErrors.Store(0)
	m.WakeupReceivedTotal.Store(0)
	m.WakeupDeliveredHit.Store(0)
	m.WakeupDeliveredMiss.Store(0)
}
