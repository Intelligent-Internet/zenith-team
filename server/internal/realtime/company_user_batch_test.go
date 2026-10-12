package realtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

type batchRelayStub struct {
	frames []CompanyUserRelayFrame
	err    error
	cancel bool
}

func (r *batchRelayStub) PublishWithID(string, string, string, []byte, string) error {
	panic("legacy relay called by batch")
}

func (r *batchRelayStub) PublishCompanyUserBatch(
	ctx context.Context,
	frames []CompanyUserRelayFrame,
) []RelayBatchResult {
	r.frames = append(r.frames, frames...)
	if r.cancel {
		<-ctx.Done()
		r.err = ctx.Err()
	}
	results := make([]RelayBatchResult, len(frames))
	for i, f := range frames {
		results[i] = RelayBatchResult{EventID: f.EventID, Err: r.err}
	}
	return results
}

func TestCompanyUserBatchIsScopedAndKeepsLocalDeliveryOnRelayFailure(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	a := attachRealtimeTestClient(hub, "company-a", "a")
	b := attachRealtimeTestClient(hub, "company-a", "b")
	outside := attachRealtimeTestClient(hub, "company-b", "a")
	relay := &batchRelayStub{err: errors.New("relay offline")}
	broadcaster := NewEventBroadcaster(hub, relay)
	event := BroadcastEvent{
		Type:      "conversation.discussion_changed",
		CreatedAt: time.Now(),
		ActorType: "system",
		Payload:   map[string]any{"conversationId": "discussion"},
	}
	result, err := broadcaster.BroadcastToCompanyUsers(context.Background(), "company-a", []string{"a", "b"}, event)
	if err != nil || len(result) != 2 || len(relay.frames) != 2 {
		t.Fatalf("result=%+v %v", result, err)
	}
	first, second := receiveRealtimeFrame(t, a), receiveRealtimeFrame(t, b)
	if first.EventID == second.EventID || first.EventID != relay.frames[0].EventID ||
		second.EventID != relay.frames[1].EventID {
		t.Fatal("per-recipient event identities changed")
	}
	for _, r := range result {
		if !errors.Is(r.Err, relay.err) {
			t.Fatalf("lost command error: %+v", r)
		}
	}
	select {
	case frame := <-outside.send:
		t.Fatalf("foreign company got %s", frame)
	default:
	}
}

func TestCompanyUserBatchHonorsDeadlineAndBound(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	relay := &batchRelayStub{cancel: true}
	b := NewEventBroadcaster(hub, relay)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	event := BroadcastEvent{Type: "conversation.discussion_changed", CreatedAt: time.Now()}
	if _, err := b.BroadcastToCompanyUsers(
		ctx,
		"company",
		[]string{"a"},
		event,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancelled batch=%v", err)
	}
	if len(relay.frames) != 0 {
		t.Fatal("cancelled batch reached relay")
	}
	users := make([]string, 51)
	for i := range users {
		users[i] = "a"
	}
	if _, err := b.BroadcastToCompanyUsers(context.Background(), "company", users, event); err == nil {
		t.Fatal("unbounded batch accepted")
	}
}
