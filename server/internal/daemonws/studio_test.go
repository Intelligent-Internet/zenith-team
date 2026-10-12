package daemonws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestStudioRelayCorrelation(t *testing.T) {
	t.Parallel()
	h := NewHub()
	rid := uuid.New()
	agentID := uuid.New()
	principal := auth.UserPrincipal{UserID: uuid.New(), SessionID: uuid.New()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.HandleWebSocket(
			w,
			r,
			ClientIdentity{DaemonID: "fixture", RuntimeIDs: []string{rid.String()}, Principal: principal},
		)
	}))
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	var connection conversation.StudioEditorConnection
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		connection, err = h.Connect(context.Background(), rid, agentID)
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if connection.Principal() != principal {
		t.Fatal("principal not pinned")
	}
	done := make(chan error, 1)
	go func() { _, e := connection.Probe(context.Background(), rid); done <- e }()
	var frame protocol.Message
	if err = ws.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	var scoped protocol.StudioScopedControl
	if err = json.Unmarshal(frame.Payload, &scoped); err != nil || scoped.AgentID != agentID {
		t.Fatal("Agent profile was not pinned")
	}
	var req protocol.StudioConnectionProbeRequest
	if err = json.Unmarshal(scoped.Request, &req); err != nil {
		t.Fatal(err)
	}
	response := protocol.StudioControlResponse{
		CorrelationID: req.ProbeID,
		Probe: &protocol.StudioConnectionProbeResponse{
			ProbeID:             req.ProbeID,
			RuntimeID:           rid,
			DaemonID:            "fixture",
			EditorSchemaVersion: 2,
		},
	}
	response.CorrelationID = uuid.New()
	if err = ws.WriteJSON(
		protocol.Message{Type: protocol.EventStudioConnectionProbeResponse, Payload: mustMarshalRaw(response)},
	); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		t.Fatalf("foreign correlation accepted: %v", e)
	case <-time.After(20 * time.Millisecond):
	}
	response.CorrelationID = req.ProbeID
	if err = ws.WriteJSON(
		protocol.Message{Type: protocol.EventStudioConnectionProbeResponse, Payload: mustMarshalRaw(response)},
	); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	wantToken := protocol.StudioAccessToken{
		AccessToken: "fixture-studio-token", TokenType: "Bearer",
		AccountID: uuid.New(), Origin: "https://studio.example.com",
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	tokenCtx, stopToken := context.WithTimeout(t.Context(), time.Second)
	defer stopToken()
	go func() {
		token, tokenErr := connection.Token(tokenCtx, rid)
		if tokenErr == nil && token != wantToken {
			tokenErr = errors.New("Studio credential differs from the daemon response")
		}
		done <- tokenErr
	}()
	if err = ws.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(frame.Payload, &scoped); err != nil || scoped.AgentID != agentID {
		t.Fatal("token profile was not pinned")
	}
	var tokenRequest struct {
		RequestID uuid.UUID `json:"requestId"`
		RuntimeID uuid.UUID `json:"runtimeId"`
	}
	if err = json.Unmarshal(scoped.Request, &tokenRequest); err != nil ||
		tokenRequest.RequestID == uuid.Nil || tokenRequest.RuntimeID != rid {
		t.Fatal("Studio token request has no correlation or runtime identity")
	}
	if frame.Type != protocol.EventStudioTokenRequest ||
		frame.Type+".response" != protocol.EventStudioTokenResponse {
		t.Fatalf("unpaired Studio token frame: %s", frame.Type)
	}
	if err = ws.WriteJSON(protocol.Message{
		Type: protocol.EventStudioTokenResponse,
		Payload: mustMarshalRaw(protocol.StudioControlResponse{
			CorrelationID: tokenRequest.RequestID, Token: &wantToken,
		}),
	}); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err = connection.Probe(ctx, rid); !errors.Is(err, conversation.ErrStudioEditorOutcomeUnknown) {
		t.Fatalf("timeout = %v", err)
	}
	ws.Close()
}

func TestStudioDaemonSocketExpiresWithAccessToken(t *testing.T) {
	t.Parallel()
	h := NewHub()
	rid := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.HandleWebSocket(w, r, ClientIdentity{
			DaemonID: "fixture", RuntimeIDs: []string{rid.String()},
			Principal: auth.UserPrincipal{
				UserID: uuid.New(), SessionID: uuid.New(),
				AccessExpiresAt: time.Now().Add(100 * time.Millisecond),
			},
		})
	}))
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _, err = ws.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("socket read = %v, want token-expiry closure", err)
	}
}

func TestStudioCallRejectsCancelledAndDuplicateDispatch(t *testing.T) {
	c := &client{ctx: t.Context(), send: make(chan []byte, 2)}
	s := &studioConnection{client: c, agentID: uuid.New()}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.call(ctx, protocol.EventStudioEditorDispatch, uuid.New(), nil, time.Second); err == nil {
		t.Fatal("cancelled dispatch accepted")
	}
	if len(c.send) != 0 {
		t.Fatal("cancelled dispatch reached daemon")
	}
	id := uuid.New()
	original := studioPending{response: make(chan protocol.StudioControlResponse, 1)}
	c.studioPending = map[uuid.UUID]studioPending{id: original}
	if _, err := s.call(
		t.Context(),
		protocol.EventStudioEditorDispatch,
		id,
		nil,
		time.Millisecond,
	); !errors.Is(
		err,
		conversation.ErrStudioEditorRequestConflict,
	) {
		t.Fatalf("duplicate dispatch = %v", err)
	}
	if c.studioPending[id].response != original.response || len(c.send) != 0 {
		t.Fatal("duplicate dispatch replaced original waiter or reached daemon")
	}
}

func TestStudioDisconnectAfterDispatchHasUnknownOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &client{ctx: ctx, send: make(chan []byte, 1)}
	s := &studioConnection{client: c, agentID: uuid.New()}
	done := make(chan error, 1)
	go func() {
		_, err := s.call(t.Context(), protocol.EventStudioEditorDispatch, uuid.New(), nil, time.Second)
		done <- err
	}()
	<-c.send
	cancel()
	if err := <-done; !errors.Is(err, conversation.ErrStudioEditorOutcomeUnknown) {
		t.Fatalf("lost response = %v", err)
	}
}
