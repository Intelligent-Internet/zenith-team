package daemonws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Intelligent-Internet/zenith-team/server/internal/realtime"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func TestNotifyRunAvailable(t *testing.T) {
	M.Reset()
	defer M.Reset()

	hub := NewHub()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWebSocket(w, r, ClientIdentity{RuntimeIDs: []string{"runtime-1"}})
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	waitForHubIndex(t, "runtime", func() bool { return runtimeIndexed(hub, "runtime-1") })

	hub.NotifyRunAvailable("runtime-1", "run-1")

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if msg.Type != protocol.EventDaemonRunAvailable {
		t.Fatalf("message type = %q, want %q", msg.Type, protocol.EventDaemonRunAvailable)
	}

	var payload protocol.RunAvailablePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.RuntimeID != "runtime-1" || payload.RunID != "run-1" {
		t.Fatalf("payload = %+v, want runtime/run IDs", payload)
	}
}

func TestNotifyCompaniesChangedSupportsAccountOnlyConnection(t *testing.T) {
	M.Reset()
	defer M.Reset()

	hub := NewHub()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWebSocket(w, r, ClientIdentity{UserID: "user-1"})
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	waitForHubIndex(t, "user", func() bool { return userIndexed(hub, "user-1") })

	hub.NotifyCompaniesChanged("user-1")

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if msg.Type != protocol.EventDaemonCompaniesChanged {
		t.Fatalf("message type = %q, want %q", msg.Type, protocol.EventDaemonCompaniesChanged)
	}
}

func TestRelayNotifierPublishesDaemonRuntimeScope(t *testing.T) {
	M.Reset()
	defer M.Reset()

	relay := &recordingRelayPublisher{}
	notifier := NewRelayNotifier(nil, relay)

	notifier.NotifyRunAvailable("runtime-1", "run-1")

	if relay.scopeType != realtime.ScopeDaemonRuntime {
		t.Fatalf("scopeType = %q, want %q", relay.scopeType, realtime.ScopeDaemonRuntime)
	}
	if relay.scopeID != "run-1" {
		t.Fatalf("scopeID = %q, want run_id shard key", relay.scopeID)
	}
	if relay.eventID == "" {
		t.Fatal("expected event id")
	}
	if M.WakeupPublishedTotal.Load() != 1 {
		t.Fatalf("published metric = %d, want 1", M.WakeupPublishedTotal.Load())
	}

	var msg protocol.Message
	if err := json.Unmarshal(relay.frame, &msg); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if msg.Type != protocol.EventDaemonRunAvailable {
		t.Fatalf("message type = %q, want %q", msg.Type, protocol.EventDaemonRunAvailable)
	}
	var payload protocol.RunAvailablePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.RuntimeID != "runtime-1" || payload.RunID != "run-1" {
		t.Fatalf("payload = %+v, want runtime/run IDs", payload)
	}
}

func TestRelayNotifierPublishesCompaniesChanged(t *testing.T) {
	M.Reset()
	defer M.Reset()

	relay := &recordingRelayPublisher{}
	notifier := NewRelayNotifier(nil, relay)

	notifier.NotifyCompaniesChanged("user-1")

	if relay.scopeType != realtime.ScopeDaemonRuntime {
		t.Fatalf("scopeType = %q, want %q", relay.scopeType, realtime.ScopeDaemonRuntime)
	}
	if relay.scopeID != "user-1" {
		t.Fatalf("scopeID = %q, want user shard key", relay.scopeID)
	}
	if relay.eventID == "" {
		t.Fatal("expected event id")
	}
	var msg protocol.Message
	if err := json.Unmarshal(relay.frame, &msg); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if msg.Type != protocol.EventDaemonCompaniesChanged {
		t.Fatalf("message type = %q, want %q", msg.Type, protocol.EventDaemonCompaniesChanged)
	}
}

func TestRelayNotifierDedupsLocalRedisLoopback(t *testing.T) {
	M.Reset()
	defer M.Reset()

	hub := NewHub()
	client := attachDaemonTestClient(hub, "runtime-1")
	relay := &localFirstDaemonRelayPublisher{t: t, client: client}
	notifier := NewRelayNotifier(hub, relay)

	notifier.NotifyRunAvailable("runtime-1", "run-1")

	if !relay.called {
		t.Fatal("expected relay publish to be invoked")
	}
	if relay.eventID == "" {
		t.Fatal("expected event id")
	}
	if M.WakeupDeliveredHit.Load() != 1 {
		t.Fatalf("delivered hit metric = %d, want 1", M.WakeupDeliveredHit.Load())
	}

	hub.DeliverDaemonRuntime(relay.scopeID, relay.frame, relay.eventID)

	select {
	case duplicate := <-client.send:
		t.Fatalf("expected redis loopback to be deduped, got duplicate %s", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
	if M.WakeupDeliveredHit.Load() != 1 {
		t.Fatalf("delivered hit metric after loopback = %d, want 1", M.WakeupDeliveredHit.Load())
	}
	if M.WakeupDeliveredMiss.Load() != 0 {
		t.Fatalf("delivered miss metric after dedup = %d, want 0", M.WakeupDeliveredMiss.Load())
	}
}

func TestRelayNotifierDedupsCompaniesChangedLoopback(t *testing.T) {
	M.Reset()
	defer M.Reset()

	hub := NewHub()
	client := attachDaemonUserTestClient(hub, "user-1")
	relay := &localFirstDaemonRelayPublisher{t: t, client: client}
	notifier := NewRelayNotifier(hub, relay)

	notifier.NotifyCompaniesChanged("user-1")
	if !relay.called || relay.eventID == "" {
		t.Fatal("expected local delivery followed by relay publish")
	}

	hub.DeliverDaemonRuntime(relay.scopeID, relay.frame, relay.eventID)
	select {
	case duplicate := <-client.send:
		t.Fatalf("expected redis loopback to be deduped, got duplicate %s", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}

// TestHeartbeatRoundTrip pins the WS heartbeat contract: a daemon.heartbeat
// frame invokes the registered HeartbeatHandler with the runtime ID, and the
// hub serializes the returned ack as a daemon.heartbeat_ack on the wire.
func TestHeartbeatRoundTrip(t *testing.T) {
	M.Reset()
	defer M.Reset()

	hub := NewHub()
	var calls atomic.Int32
	hub.SetHeartbeatHandler(
		func(
			_ context.Context,
			identity ClientIdentity,
			beat protocol.DaemonHeartbeatRequestPayload,
		) (*protocol.DaemonHeartbeatAckPayload, error) {
			calls.Add(1)
			if len(identity.CompanyIDs) != 1 || identity.CompanyIDs[0] != "co-1" {
				t.Errorf("identity companies = %v, want [co-1]", identity.CompanyIDs)
			}
			if beat.CredentialIsolation != protocol.CredentialIsolationEnforced {
				t.Errorf("credentialIsolation = %q, want the frame's report", beat.CredentialIsolation)
			}
			return &protocol.DaemonHeartbeatAckPayload{
				RuntimeID:          beat.RuntimeID,
				Status:             "ok",
				ServerCapabilities: []string{protocol.DaemonCapabilityRPCV1},
			}, nil
		},
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWebSocket(w, r, ClientIdentity{
			CompanyIDs: []string{"co-1"},
			RuntimeIDs: []string{"runtime-1"},
		})
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	hbFrame, err := json.Marshal(protocol.Message{
		Type: protocol.EventDaemonHeartbeat,
		Payload: mustMarshalRaw(protocol.DaemonHeartbeatRequestPayload{
			RuntimeID:           "runtime-1",
			CredentialIsolation: protocol.CredentialIsolationEnforced,
		}),
	})
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, hbFrame); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal ack envelope: %v", err)
	}
	if msg.Type != protocol.EventDaemonHeartbeatAck {
		t.Fatalf("ack type = %q, want %q", msg.Type, protocol.EventDaemonHeartbeatAck)
	}
	var ack protocol.DaemonHeartbeatAckPayload
	if err := json.Unmarshal(msg.Payload, &ack); err != nil {
		t.Fatalf("unmarshal ack payload: %v", err)
	}
	if ack.RuntimeID != "runtime-1" {
		t.Fatalf("ack runtime_id = %q, want runtime-1", ack.RuntimeID)
	}
	if len(ack.ServerCapabilities) != 1 || ack.ServerCapabilities[0] != protocol.DaemonCapabilityRPCV1 {
		t.Fatalf(
			"ack server_capabilities = %v, want [%s]",
			ack.ServerCapabilities,
			protocol.DaemonCapabilityRPCV1,
		)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HeartbeatHandler invocations = %d, want 1", got)
	}
}

// TestHeartbeatHandlerCtxGenerouslyBounded pins the revised invariant: the
// hub bounds the handler with a GENEROUS server-owned deadline — far above
// any legitimate heartbeat-carried claim, far below forever — so a wedged
// statement cannot park a goroutine for the life of the process. Client
// cancellation still never propagates. Cancelling mid-statement only rolls
// the claim transaction back; a claim that committed but lost its ack is
// what the dispatched-at reclaim fence recovers.
func TestHeartbeatHandlerCtxGenerouslyBounded(t *testing.T) {
	M.Reset()
	defer M.Reset()

	hub := NewHub()
	const stall = 250 * time.Millisecond
	hub.SetHeartbeatHandler(
		func(
			ctx context.Context,
			_ ClientIdentity,
			beat protocol.DaemonHeartbeatRequestPayload,
		) (*protocol.DaemonHeartbeatAckPayload, error) {
			select {
			case <-time.After(stall):
			case <-ctx.Done():
				t.Errorf(
					"handler ctx was cancelled (deadline=%v) — pending-work invariant violated",
					ctx.Err(),
				)
				return nil, ctx.Err()
			}
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Errorf("handler ctx must carry the hub's own generous deadline")
			} else if until := time.Until(deadline); until < 20*time.Second {
				t.Errorf("deadline %s from now is too tight for a heartbeat-carried claim", until)
			}
			return &protocol.DaemonHeartbeatAckPayload{RuntimeID: beat.RuntimeID, Status: "ok"}, nil
		},
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWebSocket(w, r, ClientIdentity{RuntimeIDs: []string{"runtime-1"}})
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	hbFrame, err := json.Marshal(protocol.Message{
		Type:    protocol.EventDaemonHeartbeat,
		Payload: mustMarshalRaw(protocol.DaemonHeartbeatRequestPayload{RuntimeID: "runtime-1"}),
	})
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, hbFrame); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(stall + 2*time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if msg.Type != protocol.EventDaemonHeartbeatAck {
		t.Fatalf("ack type = %q, want %q", msg.Type, protocol.EventDaemonHeartbeatAck)
	}
}

// TestHeartbeatRejectsUnauthorizedRuntime verifies that a heartbeat for a
// runtime outside the connection's authenticated set is dropped silently —
// no handler call, no ack frame.
func TestHeartbeatRejectsUnauthorizedRuntime(t *testing.T) {
	M.Reset()
	defer M.Reset()

	hub := NewHub()
	var called atomic.Bool
	hub.SetHeartbeatHandler(
		func(
			context.Context,
			ClientIdentity,
			protocol.DaemonHeartbeatRequestPayload,
		) (*protocol.DaemonHeartbeatAckPayload, error) {
			called.Store(true)
			return &protocol.DaemonHeartbeatAckPayload{Status: "ok"}, nil
		},
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWebSocket(w, r, ClientIdentity{RuntimeIDs: []string{"runtime-1"}})
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	hbFrame, err := json.Marshal(protocol.Message{
		Type:    protocol.EventDaemonHeartbeat,
		Payload: mustMarshalRaw(protocol.DaemonHeartbeatRequestPayload{RuntimeID: "runtime-other"}),
	})
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, hbFrame); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatalf("expected no ack for unauthorized runtime, got message")
	}
	if called.Load() {
		t.Fatalf("HeartbeatHandler invoked for unauthorized runtime")
	}
}

// waitForHubIndex polls until the hub's registration goroutine has indexed a
// freshly dialed connection, failing the test after a second. The delivery
// indexes are internal, so tests read them directly under the hub lock.
func waitForHubIndex(t *testing.T, what string, indexed func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !indexed() {
		if time.Now().After(deadline) {
			t.Fatalf("%s connection was not registered", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runtimeIndexed(hub *Hub, runtimeID string) bool {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	return len(hub.byRuntime[runtimeID]) > 0
}

func userIndexed(hub *Hub, userID string) bool {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	return len(hub.byUser[userID]) > 0
}

func attachDaemonTestClient(hub *Hub, runtimeID string) *client {
	c := &client{
		send:     make(chan []byte, 2),
		runtimes: map[string]struct{}{runtimeID: {}},
	}

	hub.mu.Lock()
	hub.clients[c] = true
	hub.byRuntime[runtimeID] = map[*client]bool{c: true}
	hub.mu.Unlock()

	return c
}

func attachDaemonUserTestClient(hub *Hub, userID string) *client {
	c := &client{
		send:     make(chan []byte, 2),
		identity: ClientIdentity{UserID: userID},
		runtimes: map[string]struct{}{},
	}

	hub.mu.Lock()
	hub.clients[c] = true
	hub.byUser[userID] = map[*client]bool{c: true}
	hub.mu.Unlock()

	return c
}

type recordingRelayPublisher struct {
	scopeType string
	scopeID   string
	exclude   string
	frame     []byte
	eventID   string
}

func (r *recordingRelayPublisher) PublishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error {
	r.scopeType = scopeType
	r.scopeID = scopeID
	r.exclude = exclude
	r.frame = append([]byte(nil), frame...)
	r.eventID = id
	return nil
}

type localFirstDaemonRelayPublisher struct {
	t      *testing.T
	client *client

	called     bool
	scopeType  string
	scopeID    string
	exclude    string
	frame      []byte
	eventID    string
	localFrame []byte
}

func (p *localFirstDaemonRelayPublisher) PublishWithID(
	scopeType, scopeID, exclude string,
	frame []byte,
	id string,
) error {
	p.called = true
	p.scopeType = scopeType
	p.scopeID = scopeID
	p.exclude = exclude
	p.frame = append([]byte(nil), frame...)
	p.eventID = id

	select {
	case p.localFrame = <-p.client.send:
	default:
		p.t.Fatal("expected local fanout to happen before relay publish")
	}
	return nil
}
