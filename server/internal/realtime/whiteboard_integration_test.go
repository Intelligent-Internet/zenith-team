//go:build integration

package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Each replica owns its HTTP upgrade surface, Hub and Redis relay. Authorization
// belongs to the handler/service tests; this exercises the real socket fanout
// and the transport's revocation fence rather than directly draining Hub queues.
func TestWhiteboardReplicasDeliverHintsPresenceAndRevocation(t *testing.T) {
	client := isolatedDiscussionRedis(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	company := NewEventID()
	type replica struct {
		hub       *Hub
		server    *httptest.Server
		durable   *EventBroadcaster
		ephemeral *EphemeralRelay
	}
	start := func(user string) replica {
		hub := NewHub()
		go hub.Run()
		relay := NewShardedStreamRelay(
			hub,
			client,
			client,
			ShardedStreamRelayConfig{Shards: 2, ReadBlock: 100 * time.Millisecond},
		)
		relay.Start(ctx)
		t.Cleanup(func() { relay.Stop(); cancel(); relay.Wait() })
		ephemeral, err := NewEphemeralRelay(ctx, hub, client)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(ephemeral.Close)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := Upgrade(w, r)
			if err != nil {
				return
			}
			hub.ServeClient(conn, user, company)
		}))
		t.Cleanup(server.Close)
		return replica{hub, server, NewEventBroadcaster(hub, relay), ephemeral}
	}
	memberA, memberB := uuid.New(), uuid.New()
	a, b := start(memberA.String()), start(memberB.String())
	dial := func(server *httptest.Server) *websocket.Conn {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	ca, cb := dial(a.server), dial(b.server)
	read := func(conn *websocket.Conn, expected string) BroadcastEvent {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var frame BroadcastEvent
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type != expected {
			t.Fatalf("type=%s want %s", frame.Type, expected)
		}
		return frame
	}
	frame := BroadcastEvent{
		Type:      "whiteboard.changed",
		CompanyID: &company,
		CreatedAt: time.Now().UTC(),
		ActorType: "user",
		Payload:   map[string]any{"boardId": "board-a", "sequence": 12},
	}
	if _, err := a.durable.BroadcastToCompanyUsers(
		ctx,
		company,
		[]string{memberA.String(), memberB.String()},
		frame,
	); err != nil {
		t.Fatal(err)
	}
	first, peer := read(ca, frame.Type), read(cb, frame.Type)
	if first.EventID == "" || peer.EventID == "" {
		t.Fatal("replicas did not deliver the committed hint")
	}
	frame.Type = "whiteboard.presence"
	frame.Payload = map[string]any{
		"boardId": "board-a",
		"session": map[string]any{
			"sessionId":    "gesture-a",
			"dragPreviews": []any{map[string]any{"id": "note-a", "x": 24, "y": 48}},
		},
	}
	if err := a.ephemeral.PublishToCompanyUsers(ctx, company, []uuid.UUID{memberA, memberB}, frame); err != nil {
		t.Fatal(err)
	}
	first, peer = read(ca, frame.Type), read(cb, frame.Type)
	if first.EventID != peer.EventID {
		t.Fatal("presence replicas diverged")
	}
	encoded, err := json.Marshal(peer.Payload)
	if err != nil || !strings.Contains(string(encoded), "dragPreviews") {
		t.Fatalf("gesture missing: %s %v", encoded, err)
	}
	frame.Type = "auth.company_access_revoked"
	frame.Payload = map[string]any{}
	if err := a.durable.DisconnectCompanyUser(company, memberB.String(), frame); err != nil {
		t.Fatal(err)
	}
	read(cb, frame.Type)
	if _, _, err = cb.ReadMessage(); err == nil {
		t.Fatal("revoked socket stayed open")
	}
	frame.Type = "whiteboard.presence"
	if err = a.ephemeral.PublishToCompanyUsers(ctx, company, []uuid.UUID{memberA, memberB}, frame); err != nil {
		t.Fatal(err)
	}
	read(ca, frame.Type)
	if got := b.hub.Snapshot()["connections"]; got != 0 {
		t.Fatalf("revoked replica connections=%v", got)
	}
}
