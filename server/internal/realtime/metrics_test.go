package realtime

import (
	"sync"
	"testing"
)

func TestMetrics_RecordEvent(t *testing.T) {
	m := &Metrics{}

	m.RecordEvent("issue.updated")
	m.RecordEvent("issue.updated")
	m.RecordEvent("run.message")
	m.RecordEvent("") // ignored

	snap := m.Snapshot()
	events := snap.EventsSentByType
	if events["issue.updated"] != 2 {
		t.Errorf("issue.updated count = %d, want 2", events["issue.updated"])
	}
	if events["run.message"] != 1 {
		t.Errorf("run.message count = %d, want 1", events["run.message"])
	}
	if _, ok := events[""]; ok {
		t.Errorf("empty event type should not be recorded")
	}
}

func TestMetrics_RecordEvent_Concurrent(t *testing.T) {
	m := &Metrics{}
	var wg sync.WaitGroup
	const goroutines = 50
	const perGoroutine = 200
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perGoroutine {
				m.RecordEvent("comment.created")
			}
		}()
	}
	wg.Wait()

	snap := m.Snapshot()
	events := snap.EventsSentByType
	want := int64(goroutines * perGoroutine)
	if events["comment.created"] != want {
		t.Errorf("comment.created count = %d, want %d", events["comment.created"], want)
	}
}

func TestMetrics_Snapshot_IncludesCounters(t *testing.T) {
	m := &Metrics{}
	m.ConnectsTotal.Store(10)
	m.DisconnectsTotal.Store(3)
	m.ActiveConnections.Store(7)
	m.SlowEvictionsTotal.Store(2)
	m.MessagesSentTotal.Store(123)
	m.MessagesDroppedTotal.Store(4)

	snap := m.Snapshot()
	for name, value := range map[string]struct {
		got  int64
		want int64
	}{
		"connectsTotal":        {got: snap.ConnectsTotal, want: 10},
		"disconnectsTotal":     {got: snap.DisconnectsTotal, want: 3},
		"activeConnections":    {got: snap.ActiveConnections, want: 7},
		"slowEvictionsTotal":   {got: snap.SlowEvictionsTotal, want: 2},
		"messagesSentTotal":    {got: snap.MessagesSentTotal, want: 123},
		"messagesDroppedTotal": {got: snap.MessagesDroppedTotal, want: 4},
	} {
		if value.got != value.want {
			t.Errorf("%s = %d, want %d", name, value.got, value.want)
		}
	}
}
