package realtime

import (
	"encoding/json"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/events"
)

func TestEventBroadcasterSingleNodeAddsEventID(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	client := attachRealtimeTestClient(hub, "company-1", "user-1")
	broadcaster := NewEventBroadcaster(hub, nil)

	err := broadcaster.BroadcastToCompany("company-1",
		BroadcastEvent{
			Type:      "task.updated",
			CreatedAt: time.Date(2026, 8, 19, 8, 30, 0, 0, time.UTC),
			ActorType: string(events.ActorTypeSystem),
			Payload:   map[string]any{"taskId": "task-1"},
		},
	)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	raw := receiveRealtimeBytes(t, client)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal canonical frame: %v", err)
	}
	for _, field := range []string{
		"eventId", "type", "companyId", "createdAt", "actorId", "actorType", "payload",
	} {
		if _, exists := fields[field]; !exists {
			t.Fatalf("canonical frame is missing %q: %s", field, raw)
		}
	}
	if len(fields) != 7 {
		t.Fatalf("canonical frame fields = %d, want 7: %s", len(fields), raw)
	}
	// Absent means JSON null, never an ambiguous empty string: this fact has
	// no actor and no tenant.
	if string(fields["actorId"]) != "null" {
		t.Fatalf("actorId = %s, want null", fields["actorId"])
	}
	if string(fields["companyId"]) != "null" {
		t.Fatalf("companyId = %s, want null", fields["companyId"])
	}
	var frame BroadcastEvent
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal realtime frame: %v", err)
	}
	if frame.EventID == "" {
		t.Fatal("eventId is empty")
	}
	if frame.Type != "task.updated" {
		t.Fatalf("type = %q, want %q", frame.Type, "task.updated")
	}
}

// TestEventBroadcasterKeepsAPreStampedIDAcrossBothRevocationPaths pins the
// revocation contract: the user frame and the goodbye share one id, so the
// one socket that sits in both audiences reads the fact once and then closes.
func TestEventBroadcasterKeepsAPreStampedIDAcrossBothRevocationPaths(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	client := attachRealtimeTestClient(hub, "company-1", "user-1")
	broadcaster := NewEventBroadcaster(hub, nil)
	frame := BroadcastEvent{
		EventID:   NewEventID(),
		Type:      "auth.company_access_revoked",
		CreatedAt: time.Date(2026, 9, 20, 8, 30, 0, 0, time.UTC),
		ActorType: string(events.ActorTypeUser),
		Payload:   map[string]any{"companyId": "company-1", "reason": "membership_removed"},
	}
	if err := broadcaster.BroadcastToUser("user-1", frame); err != nil {
		t.Fatalf("BroadcastToUser() error = %v", err)
	}
	if err := broadcaster.DisconnectCompanyUser("company-1", "user-1", frame); err != nil {
		t.Fatalf("DisconnectCompanyUser() error = %v", err)
	}
	if got := receiveRealtimeFrame(t, client); got.EventID != frame.EventID {
		t.Fatalf("eventId = %q, want the pre-stamped %q", got.EventID, frame.EventID)
	}
	// The goodbye carried the same id, so the channel holds nothing else and
	// is closed by the disconnect.
	raw, open := <-client.send
	if open {
		t.Fatalf("second frame delivered for one revocation: %s", raw)
	}
}

// TestEventBroadcasterEmitsUserActorType pins the envelope's actor-kind
// vocabulary on the wire: a user-attributed event carries actorType "user" —
// the one actor vocabulary (user|agent|system) the whole API uses.
func TestEventBroadcasterEmitsUserActorType(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	client := attachRealtimeTestClient(hub, "company-1", "user-1")
	broadcaster := NewEventBroadcaster(hub, nil)

	actorID := "user-1"
	err := broadcaster.BroadcastToCompany("company-1",
		BroadcastEvent{
			Type:      "task.updated",
			CreatedAt: time.Now().UTC(),
			ActorID:   &actorID,
			ActorType: string(events.ActorTypeUser),
			Payload:   map[string]any{"taskId": "task-1"},
		},
	)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	frame := receiveRealtimeFrame(t, client)
	if frame.ActorType != "user" {
		t.Fatalf("actorType = %q, want %q", frame.ActorType, "user")
	}
	if frame.ActorID == nil || *frame.ActorID != actorID {
		t.Fatalf("actorId = %v, want %q", frame.ActorID, actorID)
	}
}

func TestEventBroadcasterRelaysTheLocalEventIDAndDeduplicatesLoopback(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	client := attachRealtimeTestClient(hub, "company-1", "user-1")
	publisher := &localFirstPublisher{t: t, client: client}
	broadcaster := NewEventBroadcaster(hub, publisher)

	err := broadcaster.BroadcastToCompany("company-1",
		BroadcastEvent{
			Type: "task.updated", CreatedAt: time.Now().UTC(),
			ActorType: string(events.ActorTypeSystem), Payload: map[string]any{"taskId": "task-1"},
		},
	)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if !publisher.called {
		t.Fatal("relay publish was not invoked")
	}
	if publisher.scopeType != ScopeCompany || publisher.scopeID != "company-1" {
		t.Fatalf("relay scope = %s/%s, want %s/company-1", publisher.scopeType, publisher.scopeID, ScopeCompany)
	}

	var relayedFrame BroadcastEvent
	if err := json.Unmarshal(publisher.frame, &relayedFrame); err != nil {
		t.Fatalf("unmarshal relayed frame: %v", err)
	}
	if relayedFrame.EventID == "" || relayedFrame.EventID != publisher.eventID {
		t.Fatalf("relayed eventId = %q, publisher id = %q", relayedFrame.EventID, publisher.eventID)
	}
	if relayedFrame.EventID != publisher.localFrame.EventID {
		t.Fatalf("local eventId = %q, relay eventId = %q", publisher.localFrame.EventID, relayedFrame.EventID)
	}

	deliverEnvelope(hub, nil, envelope{
		EventID: publisher.eventID, Scope: ScopeCompany, ScopeID: "company-1",
		PayloadJSON: string(publisher.frame),
	})
	select {
	case duplicate := <-client.send:
		t.Fatalf("relay loopback delivered duplicate %s", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}

func attachRealtimeTestClient(hub *Hub, companyID, userID string) *Client {
	client := &Client{
		send:      make(chan []byte, 2),
		companyID: companyID,
		userID:    userID,
		rooms:     map[scopeKey]bool{},
	}
	hub.mu.Lock()
	hub.clients[client] = true
	hub.joinIdentityRoomLocked(client, ScopeCompany, companyID)
	hub.joinIdentityRoomLocked(client, ScopeUser, userID)
	hub.mu.Unlock()
	return client
}

func receiveRealtimeFrame(t *testing.T, client *Client) BroadcastEvent {
	t.Helper()
	raw := receiveRealtimeBytes(t, client)
	var frame BroadcastEvent
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal realtime frame: %v", err)
	}
	return frame
}

func receiveRealtimeBytes(t *testing.T, client *Client) []byte {
	t.Helper()
	select {
	case raw := <-client.send:
		return raw
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for realtime frame")
		return nil
	}
}

type localFirstPublisher struct {
	t      *testing.T
	client *Client

	called     bool
	scopeType  string
	scopeID    string
	companyID  string
	frame      []byte
	eventID    string
	localFrame BroadcastEvent
}

func (p *localFirstPublisher) PublishWithID(
	scopeType, scopeID, companyID string,
	frame []byte,
	eventID string,
) error {
	p.called = true
	p.scopeType = scopeType
	p.scopeID = scopeID
	p.companyID = companyID
	p.frame = append([]byte(nil), frame...)
	p.eventID = eventID
	p.localFrame = receiveRealtimeFrame(p.t, p.client)
	return nil
}

// expectNoRealtimeBytes fails when a frame arrives for a client that should
// not have been addressed — the check that a narrow route stayed narrow.
func expectNoRealtimeBytes(t *testing.T, client *Client) {
	t.Helper()
	select {
	case frame := <-client.send:
		t.Fatalf("client received a frame it should not have: %s", frame)
	default:
	}
}

func TestDisconnectCompanyUserSendsReceiptAndStopsCompanyFrames(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	removed := attachRealtimeTestClient(hub, "company-1", "user-1")
	otherCompany := attachRealtimeTestClient(hub, "company-2", "user-1")
	otherMember := attachRealtimeTestClient(hub, "company-1", "user-2")
	b := NewEventBroadcaster(hub, nil)
	event := BroadcastEvent{
		Type:      "access.ended",
		CreatedAt: time.Now().UTC(),
		ActorType: "system",
		Payload:   map[string]any{},
	}
	if err := b.DisconnectCompanyUser("company-1", "user-1", event); err != nil {
		t.Fatal(err)
	}
	if got := receiveRealtimeFrame(t, removed); got.Type != event.Type {
		t.Fatalf("receipt=%+v", got)
	}
	if _, open := <-removed.send; open {
		t.Fatal("removed connection still open")
	}
	if err := b.BroadcastToCompany("company-1", event); err != nil {
		t.Fatal(err)
	}
	receiveRealtimeFrame(t, otherMember)
	if err := b.BroadcastToCompany("company-2", event); err != nil {
		t.Fatal(err)
	}
	receiveRealtimeFrame(t, otherCompany)
}

func TestRelayDisconnectCompanyUser(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	removed := attachRealtimeTestClient(hub, "company-1", "user-1")
	deliverEnvelope(
		hub,
		nil,
		envelope{
			Scope:       ScopeDisconnectCompanyUser,
			ScopeID:     "user-1",
			CompanyID:   "company-1",
			EventID:     "revoke-1",
			PayloadJSON: `{"type":"access.ended"}`,
		},
	)
	receiveRealtimeBytes(t, removed)
	if _, open := <-removed.send; open {
		t.Fatal("relayed revocation left socket open")
	}
	if !validRelayRoute(ScopeDisconnectCompanyUser, "user-1", "company-1") {
		t.Fatal("revocation route rejected")
	}
}
