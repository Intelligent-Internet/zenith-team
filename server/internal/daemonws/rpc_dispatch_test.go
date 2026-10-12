package daemonws

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

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/pkg/protocol"
)

// dialRPCTestConn spins up a hub-backed WS server and returns a connected
// client conn plus the hub.
func dialRPCTestConn(t *testing.T, hub *Hub, identity ClientIdentity) *websocket.Conn {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWebSocket(w, r, identity)
	}))
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func sendRPCRequest(t *testing.T, conn *websocket.Conn, req protocol.RPCRequestPayload) protocol.RPCResponsePayload {
	t.Helper()
	frame, err := json.Marshal(protocol.Message{
		Type:    protocol.EventDaemonRPCRequest,
		Payload: mustMarshalRaw(req),
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal msg: %v", err)
	}
	if msg.Type != protocol.EventDaemonRPCResponse {
		t.Fatalf("type = %q, want %q", msg.Type, protocol.EventDaemonRPCResponse)
	}
	var resp protocol.RPCResponsePayload
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	return resp
}

// TestRPCDispatch_RoundTrip pins the generic WS request/response contract: a
// daemon.rpc_request is routed to the registered handler with the connection's
// identity, and the daemon.rpc_response echoes the request id and carries the
// handler's body.
func TestRPCDispatch_RoundTrip(t *testing.T) {
	hub := NewHub()
	var gotMethod, gotDaemonID string
	hub.SetRPCHandler(
		func(_ context.Context, identity ClientIdentity, method string, _ json.RawMessage) (int, json.RawMessage, error) {
			gotMethod = method
			gotDaemonID = identity.DaemonID
			return http.StatusOK, json.RawMessage(`{"ok":true}`), nil
		},
	)
	conn := dialRPCTestConn(t, hub, ClientIdentity{DaemonID: "daemon-1", RuntimeIDs: []string{"rt-1"}})

	resp := sendRPCRequest(t, conn, protocol.RPCRequestPayload{
		RequestID: "req-1",
		Method:    "runs.claim",
		Body:      json.RawMessage(`{"maxRuns":3}`),
	})
	if resp.RequestID != "req-1" || resp.Status != http.StatusOK || string(resp.Body) != `{"ok":true}` {
		t.Fatalf("resp = %+v, want req-1/200/{ok:true}", resp)
	}
	if gotMethod != "runs.claim" || gotDaemonID != "daemon-1" {
		t.Fatalf("handler saw method=%q daemon=%q, want runs.claim/daemon-1", gotMethod, gotDaemonID)
	}
}

// TestRPCDispatch_HandlerError maps a handler error to a non-2xx response so
// the daemon can fall back to HTTP.
func TestRPCDispatch_HandlerError(t *testing.T) {
	hub := NewHub()
	hub.SetRPCHandler(
		func(context.Context, ClientIdentity, string, json.RawMessage) (int, json.RawMessage, error) {
			return 0, nil, context.DeadlineExceeded
		},
	)
	conn := dialRPCTestConn(t, hub, ClientIdentity{DaemonID: "daemon-1", RuntimeIDs: []string{"rt-1"}})
	resp := sendRPCRequest(t, conn, protocol.RPCRequestPayload{RequestID: "req-2", Method: "runs.claim"})
	if resp.RequestID != "req-2" || resp.Status < 400 || resp.Error == "" {
		t.Fatalf("resp = %+v, want req-2 with 5xx + error", resp)
	}
}

// TestRPCDispatch_NoHandler returns 503 when no RPC handler is registered so
// the daemon falls back to HTTP.
func TestRPCDispatch_NoHandler(t *testing.T) {
	hub := NewHub()
	conn := dialRPCTestConn(t, hub, ClientIdentity{DaemonID: "daemon-1", RuntimeIDs: []string{"rt-1"}})
	resp := sendRPCRequest(t, conn, protocol.RPCRequestPayload{RequestID: "req-3", Method: "runs.claim"})
	if resp.RequestID != "req-3" || resp.Status != http.StatusServiceUnavailable {
		t.Fatalf("resp = %+v, want req-3 with 503", resp)
	}
}

// TestRPCDispatch_DisconnectDuringHandlerNoPanic pins the server-side fix: an
// RPC handler that finishes AFTER the connection tears down must not send on
// the closed send channel. The handler blocks until the client has
// disconnected, then returns and attempts to write its response. Run under
// -race; passing means the guarded send + connection-ctx teardown are safe.
func TestRPCDispatch_DisconnectDuringHandlerNoPanic(t *testing.T) {
	hub := NewHub()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	hub.SetRPCHandler(
		func(context.Context, ClientIdentity, string, json.RawMessage) (int, json.RawMessage, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release // return only after the client disconnects
			return http.StatusOK, json.RawMessage(`{"ok":true}`), nil
		},
	)
	conn := dialRPCTestConn(t, hub, ClientIdentity{DaemonID: "daemon-1", RuntimeIDs: []string{"rt-1"}})

	frame, _ := json.Marshal(protocol.Message{
		Type:    protocol.EventDaemonRPCRequest,
		Payload: mustMarshalRaw(protocol.RPCRequestPayload{RequestID: "req-1", Method: "runs.claim"}),
	})
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never started")
	}

	conn.Close() // client disconnect → server readPump exits → cancel + close send
	time.Sleep(50 * time.Millisecond)
	close(release) // handler returns and tries to send its response
	time.Sleep(100 * time.Millisecond)
	// No panic == pass.
}

// TestRPCDispatch_ServerTimeoutCancelsHandler pins the timeout contract: the
// RPC request's TimeoutMs bounds server-side execution, so a slow handler is
// cancelled (its work rolled back) at the deadline rather than running to
// completion after the daemon has already timed out and fallen back to HTTP.
func TestRPCDispatch_ServerTimeoutCancelsHandler(t *testing.T) {
	hub := NewHub()
	cancelled := make(chan struct{}, 1)
	hub.SetRPCHandler(
		func(ctx context.Context, _ ClientIdentity, _ string, _ json.RawMessage) (int, json.RawMessage, error) {
			select {
			case <-ctx.Done():
				select {
				case cancelled <- struct{}{}:
				default:
				}
				return 0, nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return http.StatusOK, json.RawMessage(`{}`), nil
			}
		},
	)
	conn := dialRPCTestConn(t, hub, ClientIdentity{DaemonID: "daemon-1", RuntimeIDs: []string{"rt-1"}})

	resp := sendRPCRequest(t, conn, protocol.RPCRequestPayload{
		RequestID: "req-timeout",
		Method:    "runs.claim",
		TimeoutMs: 100,
	})
	if resp.RequestID != "req-timeout" || resp.Status < 400 || resp.Error == "" {
		t.Fatalf("resp = %+v, want an error response from the cancelled handler", resp)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler context was not cancelled at the server-side TimeoutMs deadline")
	}
}

// TestRPCDispatcher_RunsClaim pins the WS-first claim binding: a runs.claim
// RPC is dispatched directly to the claim adapter with the connection's full
// identity — including the principal authenticated at upgrade — and the body
// verbatim, and the adapter's status/body come back untouched.
func TestRPCDispatcher_RunsClaim(t *testing.T) {
	principal := auth.UserPrincipal{
		UserID:    uuid.New(),
		SessionID: uuid.New(),
		AuthKind:  auth.TokenKindBearer,
	}
	var gotIdentity ClientIdentity
	var gotMethod string
	var gotBody json.RawMessage
	claim := func(
		_ context.Context,
		identity ClientIdentity,
		method string,
		body json.RawMessage,
	) (int, json.RawMessage, error) {
		gotIdentity = identity
		gotMethod = method
		gotBody = body
		return http.StatusOK, json.RawMessage(`{"runs":[]}`), nil
	}

	d := NewRPCDispatcher(claim)
	identity := ClientIdentity{
		DaemonID:      "daemon-1",
		UserID:        principal.UserID.String(),
		RuntimeIDs:    []string{"rt-1"},
		ClientVersion: "0.1.0",
		Capabilities:  protocol.DaemonCapabilityRPCV1,
		Principal:     principal,
	}
	status, body, err := d.Dispatch(context.Background(), identity, "runs.claim", json.RawMessage(`{"maxRuns":3}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"runs":[]}` {
		t.Fatalf("status=%d body=%s, want 200/{runs:[]}", status, body)
	}
	if gotMethod != "runs.claim" {
		t.Fatalf("method = %q, want runs.claim", gotMethod)
	}
	if gotIdentity.DaemonID != identity.DaemonID ||
		gotIdentity.Principal.UserID != principal.UserID ||
		gotIdentity.Capabilities != identity.Capabilities ||
		gotIdentity.ClientVersion != identity.ClientVersion {
		t.Fatalf("identity = %+v, want the connection identity %+v", gotIdentity, identity)
	}
	if string(gotBody) != `{"maxRuns":3}` {
		t.Fatalf("body = %s, want the RPC body verbatim", gotBody)
	}
}

// TestRPCDispatcher_RunsClaimUnavailable answers 503 when no claim adapter is
// wired so the daemon falls back to HTTP.
func TestRPCDispatcher_RunsClaimUnavailable(t *testing.T) {
	d := NewRPCDispatcher(nil)
	status, _, err := d.Dispatch(context.Background(),
		ClientIdentity{DaemonID: "daemon-1"}, "runs.claim", nil)
	if status != http.StatusServiceUnavailable || err == nil {
		t.Fatalf("status=%d err=%v, want 503 + error", status, err)
	}
}

func TestRPCDispatcher_RunTranscriptAppend(t *testing.T) {
	t.Parallel()

	d := NewRPCDispatcher(func(
		context.Context, ClientIdentity, string, json.RawMessage,
	) (int, json.RawMessage, error) {
		t.Fatal("claim handler must not run for a transcript append")
		return 0, nil, nil
	})
	var gotIdentity ClientIdentity
	var gotBody json.RawMessage
	d.SetRunTranscriptAppendHandler(func(
		_ context.Context,
		identity ClientIdentity,
		_ string,
		body json.RawMessage,
	) (int, json.RawMessage, error) {
		gotIdentity = identity
		gotBody = body
		return http.StatusOK, json.RawMessage(`{"seq":4}`), nil
	})

	identity := ClientIdentity{
		DaemonID:   "daemon-1",
		RuntimeIDs: []string{"runtime-1"},
	}
	request := json.RawMessage(`{"runId":"run-1","itemKey":"item-1","role":"agent","parts":[]}`)
	status, body, err := d.Dispatch(
		context.Background(),
		identity,
		protocol.DaemonRPCMethodRunTranscriptAppend,
		request,
	)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"seq":4}` {
		t.Fatalf("status=%d body=%s, want 200/{seq:4}", status, body)
	}
	if gotIdentity.DaemonID != identity.DaemonID || len(gotIdentity.RuntimeIDs) != 1 ||
		gotIdentity.RuntimeIDs[0] != identity.RuntimeIDs[0] {
		t.Fatalf("identity = %+v, want %+v", gotIdentity, identity)
	}
	if string(gotBody) != string(request) {
		t.Fatalf("body = %s, want %s", gotBody, request)
	}
}

// TestRPCDispatcher_UnknownMethod returns 404 for an unknown method.
func TestRPCDispatcher_UnknownMethod(t *testing.T) {
	d := NewRPCDispatcher(func(
		context.Context, ClientIdentity, string, json.RawMessage,
	) (int, json.RawMessage, error) {
		t.Fatal("claim handler must not run for an unknown method")
		return 0, nil, nil
	})
	status, _, err := d.Dispatch(context.Background(),
		ClientIdentity{DaemonID: "daemon-1"}, "does.not.exist", nil)
	if status != http.StatusNotFound || err == nil {
		t.Fatalf("status=%d err=%v, want 404 + error", status, err)
	}
}
