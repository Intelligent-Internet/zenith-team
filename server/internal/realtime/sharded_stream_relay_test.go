package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	redismock "github.com/go-redis/redismock/v9"
	"github.com/redis/go-redis/v9"
)

func TestShardedStreamRelayConfigDefaults(t *testing.T) {
	relay := NewShardedStreamRelay(NewHub(), nil, nil, ShardedStreamRelayConfig{})

	if relay.config.Shards != 8 {
		t.Fatalf("expected default shard count %d, got %d", 8, relay.config.Shards)
	}
	if relay.config.StreamMaxLen != 100000 {
		t.Fatalf("expected default stream max len %d, got %d", 100000, relay.config.StreamMaxLen)
	}
	if relay.config.ReadCount != 128 {
		t.Fatalf("expected default read count %d, got %d", 128, relay.config.ReadCount)
	}
	if relay.config.ReadBlock != 5*time.Second {
		t.Fatalf("expected default read block %s, got %s", 5*time.Second, relay.config.ReadBlock)
	}
	if relay.config.ReplayGrace != 5*time.Minute {
		t.Fatalf("expected default replay grace %s, got %s", 5*time.Minute, relay.config.ReplayGrace)
	}
}

func TestShardedStreamRelayShardForScopeIsStableAndBounded(t *testing.T) {
	relay := NewShardedStreamRelay(NewHub(), nil, nil, ShardedStreamRelayConfig{Shards: 8})

	first := relay.shardFor(ScopeCompany, "company-1")
	second := relay.shardFor(ScopeCompany, "company-1")
	if first != second {
		t.Fatalf("expected stable shard selection, got %d then %d", first, second)
	}
	if first < 0 || first >= relay.config.Shards {
		t.Fatalf("shard %d out of range [0,%d)", first, relay.config.Shards)
	}
}

func TestShardedStreamRelayDeliverMessageUsesEnvelopeScope(t *testing.T) {
	hub := NewHub()
	client := attachRealtimeTestClient(hub, "company-1", "user-1")
	relay := NewShardedStreamRelay(hub, nil, nil, ShardedStreamRelayConfig{})
	ev := envelope{
		EventID:     "event-1",
		Scope:       ScopeCompany,
		ScopeID:     "company-1",
		PayloadJSON: `{"type":"run.updated"}`,
	}

	relay.deliverMessage(redis.XMessage{Values: envelopeRedisValues(ev)})

	select {
	case raw := <-client.send:
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("delivered frame is not JSON: %v", err)
		}
		if frame["eventId"] != ev.EventID {
			t.Fatalf("expected eventId %q, got %v", ev.EventID, frame["eventId"])
		}
	case <-time.After(time.Second):
		t.Fatal("expected sharded relay message to be delivered")
	}

	relay.deliverMessage(redis.XMessage{Values: envelopeRedisValues(ev)})
	select {
	case duplicate := <-client.send:
		t.Fatalf("expected duplicate event id to be deduped, got %s", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestInjectEventIDPreservesCanonicalFrameBytes(t *testing.T) {
	t.Parallel()
	frame := []byte(`{"eventId":"event-1","type":"task.updated","payload":{"taskId":"task-1"}}`)

	got := injectEventID(frame, "event-1")

	if string(got) != string(frame) {
		t.Fatalf("injectEventID() = %s, want original canonical frame %s", got, frame)
	}
}

func TestInjectEventIDReplacesConflictingFrameID(t *testing.T) {
	t.Parallel()
	frame := []byte(`{"eventId":"untrusted","type":"task.updated"}`)

	got := injectEventID(frame, "event-1")

	var decoded struct {
		EventID string `json:"eventId"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if decoded.EventID != "event-1" {
		t.Fatalf("eventId = %q, want event-1", decoded.EventID)
	}
}

type recordingDaemonDeliverer struct {
	scopeID string
	frame   []byte
	eventID string
	calls   int
}

func (d *recordingDaemonDeliverer) DeliverDaemonRuntime(scopeID string, frame []byte, eventID string) {
	d.scopeID = scopeID
	d.frame = append([]byte(nil), frame...)
	d.eventID = eventID
	d.calls++
}

// TestDeliverEnvelopeDaemonScopePassesFrameThroughUnmodified pins the daemon
// control-plane contract: daemon frames have no eventId field in their schema
// and dedup travels beside the frame as the envelope id, so the relay must
// hand the payload bytes through untouched instead of injecting an eventId.
func TestDeliverEnvelopeDaemonScopePassesFrameThroughUnmodified(t *testing.T) {
	t.Parallel()
	deliverer := &recordingDaemonDeliverer{}
	frame := `{"type":"daemon.run_available","payload":{"runtimeId":"runtime-1","runId":"run-1"}}`

	deliverEnvelope(NewHub(), deliverer, envelope{
		EventID:     "event-daemon",
		Scope:       ScopeDaemonRuntime,
		ScopeID:     "runtime-1",
		PayloadJSON: frame,
	})

	if deliverer.calls != 1 {
		t.Fatalf("DeliverDaemonRuntime calls = %d, want 1", deliverer.calls)
	}
	if deliverer.scopeID != "runtime-1" {
		t.Fatalf("scopeID = %q, want runtime-1", deliverer.scopeID)
	}
	if string(deliverer.frame) != frame {
		t.Fatalf("daemon frame was rewritten:\n got %s\nwant %s", deliverer.frame, frame)
	}
	if deliverer.eventID != "event-daemon" {
		t.Fatalf("eventID = %q, want event-daemon (dedup id rides beside the frame)", deliverer.eventID)
	}
}

func TestShardedStreamRelayCompanyUserAudienceStaysAnIntersection(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	target := attachRealtimeTestClient(hub, "company-1", "user-1")
	otherUser := attachRealtimeTestClient(hub, "company-1", "user-2")
	otherCompany := attachRealtimeTestClient(hub, "company-2", "user-1")

	deliverEnvelope(hub, nil, envelope{
		EventID: "event-private", Scope: ScopeCompanyUser, ScopeID: "user-1",
		CompanyID: "company-1", PayloadJSON: `{"type":"inbox.updated"}`,
	})

	if frame := receiveRealtimeFrame(t, target); frame.EventID != "event-private" {
		t.Fatalf("target eventId = %q, want event-private", frame.EventID)
	}
	for name, client := range map[string]*Client{
		"other user": otherUser, "other company": otherCompany,
	} {
		select {
		case frame := <-client.send:
			t.Fatalf("%s received intersection frame %s", name, frame)
		default:
		}
	}
}

func TestShardedStreamRelayDropsLegacyResourceScope(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	client := attachRealtimeTestClient(hub, "company-1", "user-1")
	deliverEnvelope(hub, nil, envelope{
		EventID: "event-resource", Scope: "run", ScopeID: "run-1",
		PayloadJSON: `{"type":"run.updated"}`,
	})
	select {
	case frame := <-client.send:
		t.Fatalf("legacy resource scope delivered frame %s", frame)
	default:
	}
}

func TestShardedStreamRelayReplayStartIDIsBounded(t *testing.T) {
	grace := 5 * time.Minute
	relay := NewShardedStreamRelay(NewHub(), nil, nil, ShardedStreamRelayConfig{
		ReplayGrace: grace,
	})

	before := time.Now().Add(-grace).UnixMilli()
	id := relay.replayStartID()
	after := time.Now().Add(-grace).UnixMilli()

	// The ID should be in the format "<millis>-0".
	var ms int64
	var seq int
	n, _ := fmt.Sscanf(id, "%d-%d", &ms, &seq)
	if n != 2 {
		t.Fatalf("replayStartID() = %q, want format <millis>-0", id)
	}
	if seq != 0 {
		t.Fatalf("replayStartID() sequence = %d, want 0", seq)
	}
	// The timestamp should be within [before, after] (inclusive).
	if ms < before || ms > after {
		t.Fatalf("replayStartID() timestamp %d outside expected window [%d, %d]", ms, before, after)
	}
}

func TestShardedStreamRelayReadShardOnceReplaysRetainedMessages(t *testing.T) {
	hub := NewHub()
	client := attachRealtimeTestClient(hub, "company-replay", "user-1")
	rdb, mock := redismock.NewClientMock()
	t.Cleanup(func() { _ = rdb.Close() })

	grace := 5 * time.Minute
	relay := NewShardedStreamRelay(hub, rdb, rdb, ShardedStreamRelayConfig{
		Shards:      1,
		ReadCount:   2,
		ReadBlock:   time.Millisecond,
		ReplayGrace: grace,
	})
	stream := ShardedStreamKey(0)

	// The initial cursor must be a bounded time-window, not "$".
	lastID := relay.replayStartID()
	if lastID == "$" {
		t.Fatal("replayStartID() returned \"$\", want a bounded time-window cursor")
	}

	// Expect an XREAD starting from the bounded cursor (not "$").
	mock.ExpectXRead(&redis.XReadArgs{
		Streams: []string{stream, lastID},
		Count:   relay.config.ReadCount,
		Block:   relay.config.ReadBlock,
	}).SetVal([]redis.XStream{{
		Stream: stream,
		Messages: []redis.XMessage{{
			ID: "1710000000000-0",
			Values: envelopeRedisValues(envelope{
				EventID:     "event-replayed",
				Scope:       ScopeCompany,
				ScopeID:     "company-replay",
				PayloadJSON: `{"type":"run.updated"}`,
			}),
		}},
	}})

	if !relay.readShardOnce(context.Background(), 0, stream, &lastID) {
		t.Fatal("expected shard reader to continue after a successful replay read")
	}
	if lastID != "1710000000000-0" {
		t.Fatalf("expected last ID to advance to %q, got %q", "1710000000000-0", lastID)
	}

	select {
	case raw := <-client.send:
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("delivered frame is not JSON: %v", err)
		}
		if frame["eventId"] != "event-replayed" {
			t.Fatalf("expected replayed eventId %q, got %v", "event-replayed", frame["eventId"])
		}
	case <-time.After(time.Second):
		t.Fatal("expected retained stream message to be delivered on replay")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
