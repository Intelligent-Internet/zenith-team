package realtime

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

const (
	testCompanyID = "test-company"
	testUserID    = "test-user"
)

func newTestHub(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	hub := NewHub()
	go hub.Run()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r)
		if err != nil {
			return
		}
		hub.ServeClient(conn, testUserID, testCompanyID)
	})
	server := httptest.NewServer(mux)
	return hub, server
}

func connectWS(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect WebSocket: %v", err)
	}
	return conn
}

func TestHub_DeliverCompanyUserAudienceIsAnIntersection(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	target := attachRealtimeTestClient(hub, "company-1", "user-1")
	otherUser := attachRealtimeTestClient(hub, "company-1", "user-2")
	otherCompany := attachRealtimeTestClient(hub, "company-2", "user-1")

	hub.Deliver(
		audience{typ: audienceCompanyUser, companyID: "company-1", userID: "user-1"},
		[]byte(`{"eventId":"event-1","type":"inbox.updated"}`),
		"event-1",
	)

	if frame := receiveRealtimeFrame(t, target); frame.EventID != "event-1" {
		t.Fatalf("target eventId = %q, want event-1", frame.EventID)
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

func TestHub_DeliverUserAudienceCrossesCompanyRoomsForThatUserOnly(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	firstCompany := attachRealtimeTestClient(hub, "company-1", "user-1")
	secondCompany := attachRealtimeTestClient(hub, "company-2", "user-1")
	otherUser := attachRealtimeTestClient(hub, "company-1", "user-2")

	hub.Deliver(
		audience{typ: audienceUser, userID: "user-1"},
		[]byte(`{"eventId":"event-user","type":"auth.session_revoked"}`),
		"event-user",
	)
	for name, client := range map[string]*Client{
		"first company": firstCompany, "second company": secondCompany,
	} {
		if frame := receiveRealtimeFrame(t, client); frame.EventID != "event-user" {
			t.Fatalf("%s eventId = %q, want event-user", name, frame.EventID)
		}
	}
	select {
	case frame := <-otherUser.send:
		t.Fatalf("other user received global user frame %s", frame)
	default:
	}
}

// totalClients counts all currently registered clients.
func totalClients(hub *Hub) int {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	return len(hub.clients)
}

func TestHub_ClientRegistration(t *testing.T) {
	hub, server := newTestHub(t)
	defer server.Close()

	conn := connectWS(t, server)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	count := totalClients(hub)
	if count != 1 {
		t.Fatalf("expected 1 client, got %d", count)
	}
}

func TestHub_Broadcast(t *testing.T) {
	hub, server := newTestHub(t)
	defer server.Close()

	conn1 := connectWS(t, server)
	defer conn1.Close()
	conn2 := connectWS(t, server)
	defer conn2.Close()

	time.Sleep(50 * time.Millisecond)

	msg := []byte(`{"type":"issue.created","data":"test"}`)
	hub.Deliver(audience{typ: audienceCompany, companyID: testCompanyID}, msg, "event-1")

	conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, received1, err := conn1.ReadMessage()
	if err != nil {
		t.Fatalf("client 1 read error: %v", err)
	}
	if string(received1) != string(msg) {
		t.Fatalf("client 1: expected %s, got %s", msg, received1)
	}

	conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, received2, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("client 2 read error: %v", err)
	}
	if string(received2) != string(msg) {
		t.Fatalf("client 2: expected %s, got %s", msg, received2)
	}
}

func TestHub_ClientDisconnect(t *testing.T) {
	hub, server := newTestHub(t)
	defer server.Close()

	conn := connectWS(t, server)

	time.Sleep(50 * time.Millisecond)

	countBefore := totalClients(hub)
	if countBefore != 1 {
		t.Fatalf("expected 1 client before disconnect, got %d", countBefore)
	}

	conn.Close()
	time.Sleep(100 * time.Millisecond)

	countAfter := totalClients(hub)
	if countAfter != 0 {
		t.Fatalf("expected 0 clients after disconnect, got %d", countAfter)
	}
}

func TestHub_BroadcastToMultipleClients(t *testing.T) {
	hub, server := newTestHub(t)
	defer server.Close()

	const numClients = 5
	conns := make([]*websocket.Conn, numClients)
	for i := range numClients {
		conns[i] = connectWS(t, server)
		defer conns[i].Close()
	}

	time.Sleep(50 * time.Millisecond)

	count := totalClients(hub)
	if count != numClients {
		t.Fatalf("expected %d clients, got %d", numClients, count)
	}

	msg := []byte(`{"type":"test","count":5}`)
	hub.Deliver(audience{typ: audienceCompany, companyID: testCompanyID}, msg, "event-1")

	for i, conn := range conns {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, received, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("client %d read error: %v", i, err)
		}
		if string(received) != string(msg) {
			t.Fatalf("client %d: expected %s, got %s", i, msg, received)
		}
	}
}

func TestHub_MultipleBroadcasts(t *testing.T) {
	hub, server := newTestHub(t)
	defer server.Close()

	conn := connectWS(t, server)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	messages := []string{
		`{"type":"issue.created"}`,
		`{"type":"issue.updated"}`,
		`{"type":"issue.deleted"}`,
	}
	eventIDs := []string{"event-1", "event-2", "event-3"}

	for index, msg := range messages {
		hub.Deliver(audience{typ: audienceCompany, companyID: testCompanyID}, []byte(msg), eventIDs[index])
	}

	for i, expected := range messages {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, received, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("message %d read error: %v", i, err)
		}
		if string(received) != expected {
			t.Fatalf("message %d: expected %s, got %s", i, expected, received)
		}
	}
}

func TestCheckOrigin(t *testing.T) {
	prev, _ := allowedWSOrigins.Load().([]string)
	SetAllowedOrigins([]string{
		"http://localhost:3000",
		"https://zenith-team.example",
	})
	t.Cleanup(func() { SetAllowedOrigins(prev) })

	prevProxies, _ := trustedProxies.Load().([]netip.Prefix)
	SetTrustedProxies([]netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	})
	t.Cleanup(func() { SetTrustedProxies(prevProxies) })

	cases := []struct {
		name       string
		host       string
		origin     string
		fwdHost    string
		remoteAddr string
		want       bool
	}{
		{"empty origin allowed", "api.zenith-team.example", "", "", "1.2.3.4:5678", true},
		{
			"same-origin allowed (native client default)",
			"localhost:8080",
			"http://localhost:8080",
			"",
			"1.2.3.4:5678",
			true,
		},
		{
			"same-origin allowed (https)",
			"api.zenith-team.example",
			"https://api.zenith-team.example",
			"",
			"1.2.3.4:5678",
			true,
		},
		{
			"same-origin allowed (case-insensitive host, RFC 7230)",
			"API.Zenith-Team.EXAMPLE",
			"https://api.zenith-team.example",
			"",
			"1.2.3.4:5678",
			true,
		},
		{
			"whitelisted origin allowed (web cross-origin)",
			"localhost:8080",
			"http://localhost:3000",
			"",
			"1.2.3.4:5678",
			true,
		},
		{
			"whitelisted origin allowed (prod web)",
			"api.zenith-team.example",
			"https://zenith-team.example",
			"",
			"1.2.3.4:5678",
			true,
		},
		{
			"unknown origin rejected (CSWSH defense)",
			"api.zenith-team.example",
			"https://evil.example",
			"",
			"1.2.3.4:5678",
			false,
		},
		{"different port rejected", "localhost:8080", "http://localhost:9999", "", "1.2.3.4:5678", false},
		{
			"X-Forwarded-Host from trusted proxy matches origin",
			"internal.proxy",
			"https://zenith-team.example",
			"zenith-team.example",
			"127.0.0.1:5678",
			true,
		},
		{
			"X-Forwarded-Host from trusted proxy case-insensitive",
			"internal.proxy",
			"https://Zenith-Team.EXAMPLE",
			"zenith-team.example",
			"10.0.0.1:5678",
			true,
		},
		{
			"X-Forwarded-Host from untrusted source rejected",
			"internal.proxy",
			"https://example.com",
			"example.com",
			"1.2.3.4:5678",
			false,
		},
		{
			"X-Forwarded-Host from trusted proxy but evil origin rejected",
			"internal.proxy",
			"https://evil.example",
			"zenith-team.example",
			"127.0.0.1:5678",
			false,
		},
		{
			"X-Forwarded-Host present but origin matches direct Host",
			"zenith-team.example",
			"https://zenith-team.example",
			"other.host",
			"1.2.3.4:5678",
			true,
		},
		{
			"X-Forwarded-Host spoofed by attacker rejected",
			"internal.proxy",
			"https://evil.example",
			"evil.example",
			"1.2.3.4:5678",
			false,
		},
		{
			"X-Forwarded-Host from trusted CIDR range matches origin",
			"internal.proxy",
			"https://zenith-team.example",
			"zenith-team.example",
			"10.5.6.7:5678",
			true,
		},
		{
			"X-Forwarded-Host from trusted IPv6 proxy matches origin",
			"internal.proxy",
			"https://zenith-team.example",
			"zenith-team.example",
			"[::1]:5678",
			true,
		},
		{
			"X-Forwarded-Host comma list uses first (client-facing) value",
			"internal.proxy",
			"https://zenith-team.example",
			"zenith-team.example, proxy.internal",
			"127.0.0.1:5678",
			true,
		},
		{
			"X-Forwarded-Host comma list ignores trailing values",
			"internal.proxy",
			"https://staging.zenith-team.example",
			"proxy.internal, staging.zenith-team.example",
			"127.0.0.1:5678",
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/ws", nil)
			r.Host = tc.host
			r.RemoteAddr = tc.remoteAddr
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.fwdHost != "" {
				r.Header.Set("X-Forwarded-Host", tc.fwdHost)
			}
			if got := checkOrigin(r); got != tc.want {
				t.Fatalf(
					"checkOrigin(host=%q, origin=%q, X-Forwarded-Host=%q, remoteAddr=%q) = %v, want %v",
					tc.host,
					tc.origin,
					tc.fwdHost,
					tc.remoteAddr,
					got,
					tc.want,
				)
			}
		})
	}
}

// waitFor polls cond until it holds or the deadline expires. Hub registration
// and the metrics bump both happen off the connection's own goroutine, so
// asserting on them right after a read would race.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ServeClient installs the read limit before the pumps start, so the very
// first frame is already bounded.
func TestReadPump_RejectsOversizedFrame(t *testing.T) {
	hub, server := newTestHub(t)
	defer server.Close()

	conn := connectWS(t, server)
	defer conn.Close()

	waitFor(t, "client registration", func() bool { return totalClients(hub) == 1 })
	before := M.InboundTooLargeTotal.Load()

	oversized, err := json.Marshal(map[string]any{
		"type": "ping",
		"pad":  string(bytes.Repeat([]byte("a"), inboundReadLimit)),
	})
	if err != nil {
		t.Fatalf("marshal oversized frame: %v", err)
	}
	_ = conn.WriteMessage(websocket.TextMessage, oversized)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf("read after oversized frame = %v, want close code %d", err, websocket.CloseMessageTooBig)
	}

	waitFor(t, "inbound_too_large_total to increment", func() bool {
		return M.InboundTooLargeTotal.Load()-before == 1
	})
	waitFor(t, "client to be unregistered", func() bool { return totalClients(hub) == 0 })
}

// Browser sockets are receive-only. Legacy resource-control frames are
// ignored, do not create a room, and do not prevent later server delivery.
func TestReadPump_IgnoresResourceSubscriptionFrames(t *testing.T) {
	hub, server := newTestHub(t)
	defer server.Close()

	conn := connectWS(t, server)
	defer conn.Close()

	frame, err := json.Marshal(map[string]any{
		"type": "subscribe",
		"payload": map[string]string{
			"scope": "run",
			"id":    "run-1",
		},
	})
	if err != nil {
		t.Fatalf("marshal subscribe frame: %v", err)
	}
	if len(frame) >= inboundReadLimit {
		t.Fatalf("test frame is %d bytes, not under the %d byte limit", len(frame), inboundReadLimit)
	}
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatalf("write subscribe frame: %v", err)
	}
	waitFor(t, "client registration", func() bool { return totalClients(hub) == 1 })
	time.Sleep(20 * time.Millisecond)
	rooms := hub.Snapshot()["rooms"].(map[string]int)
	if _, exists := rooms["run"]; exists {
		t.Fatalf("legacy run room exists after subscribe frame: %v", rooms)
	}

	broadcaster := NewEventBroadcaster(hub, nil)
	if err := broadcaster.BroadcastToCompany(testCompanyID, BroadcastEvent{
		Type: "task.updated", CreatedAt: time.Now().UTC(),
		ActorType: string(events.ActorTypeSystem), Payload: map[string]any{"taskId": "task-1"},
	}); err != nil {
		t.Fatalf("BroadcastBroadcastToCompany() error = %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read server event: %v", err)
	}
	var got BroadcastEvent
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal server event: %v", err)
	}
	if got.Type != "task.updated" {
		t.Fatalf("type = %q, want %q", got.Type, "task.updated")
	}
}

func TestServeClientRechecksAccessAfterRegistration(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	go hub.Run()
	checked := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r)
		if err != nil {
			return
		}
		hub.ServeClient(conn, testUserID, testCompanyID, func() bool {
			hub.mu.RLock()
			registered := len(hub.clients) == 1
			hub.mu.RUnlock()
			checked <- registered
			return false
		})
	}))
	defer server.Close()
	conn := connectWS(t, server)
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("unauthorized socket remained open")
	}
	if !<-checked {
		t.Fatal("access check ran before registration")
	}
	hub.mu.RLock()
	count := len(hub.clients)
	hub.mu.RUnlock()
	if count != 0 {
		t.Fatalf("unauthorized connections=%d", count)
	}
}
