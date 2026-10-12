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

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestStudioRelayAcrossReplicas(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr(), ContextTimeoutEnabled: true, MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	exerciseStudioRelay(t, rdb)
}

func TestStudioRelayOnlyAcceptsPinnedReplies(t *testing.T) {
	id, node, socket := uuid.New(), uuid.New(), uuid.New()
	waiter := studioRelayPending{owner: node, connection: socket, response: make(chan studioRelayResponse, 1)}
	r := &StudioRelay{pending: map[uuid.UUID]studioRelayPending{id: waiter}}
	for _, wrong := range []studioRelayResponse{
		{ID: uuid.New(), OwnerNodeID: node, ConnectionID: socket},
		{ID: id, OwnerNodeID: uuid.New(), ConnectionID: socket},
		{ID: id, OwnerNodeID: node, ConnectionID: uuid.New()},
	} {
		r.deliver(string(mustMarshalRaw(wrong)))
	}
	select {
	case <-waiter.response:
		t.Fatal("foreign or stale reply accepted")
	default:
	}
	r.deliver(string(mustMarshalRaw(studioRelayResponse{ID: id, OwnerNodeID: node, ConnectionID: socket})))
	select {
	case <-waiter.response:
	default:
		t.Fatal("pinned reply lost")
	}
}

func TestStudioRelayUnavailableRedisCannotBecomeReady(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr(), ContextTimeoutEnabled: true, MaxRetries: -1})
	defer rdb.Close()
	r, err := NewStudioRelay(t.Context(), NewHub(), rdb)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	if err := r.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	server.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("subscriber loss did not fail supervised worker")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscriber loss left relay running")
	}
	if err := r.Ping(ctx); err == nil {
		t.Fatal("dead subscriber remained ready")
	}
	if _, err := r.Connect(ctx, uuid.New(), uuid.New()); err == nil {
		t.Fatal("Redis outage admitted operation")
	}
}

func exerciseStudioRelay(t *testing.T, rdb *redis.Client) {
	t.Helper()
	ctx := t.Context()
	caller, _ := startStudioRelay(t, rdb)
	owner, hub := startStudioRelay(t, rdb)
	third, thirdHub := startStudioRelay(t, rdb)
	runtimeID, agentID := uuid.New(), uuid.New()
	principal := auth.UserPrincipal{UserID: uuid.New(), SessionID: uuid.New(), AuthKind: auth.TokenKindBearer}
	identity := ClientIdentity{
		DaemonID:   uuid.NewString(),
		RuntimeIDs: []string{runtimeID.String()},
		Principal:  principal,
	}
	ws := attachStudioDaemon(t, hub, identity)
	connection := waitStudioConnection(t, caller, runtimeID, agentID)
	if connection.Principal() != principal || connection.AgentID() != agentID {
		t.Fatal("remote connection lost the authenticated principal or Agent")
	}

	t.Run("remote probe token and receipt", func(t *testing.T) {
		done := make(chan error, 1)
		go func() { _, e := connection.Probe(ctx, runtimeID); done <- e }()
		frame, scoped := readStudioControl(t, ws, agentID)
		var probe protocol.StudioConnectionProbeRequest
		if err := json.Unmarshal(scoped.Request, &probe); err != nil {
			t.Fatal(err)
		}
		response := protocol.StudioControlResponse{
			CorrelationID: probe.ProbeID,
			Probe: &protocol.StudioConnectionProbeResponse{
				ProbeID: probe.ProbeID, RuntimeID: runtimeID, DaemonID: identity.DaemonID,
			},
		}
		writeStudioControl(t, ws, frame.Type+".response", response)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		token := protocol.StudioAccessToken{
			AccessToken: "fixture-only",
			TokenType:   "Bearer",
			AccountID:   uuid.New(),
			ExpiresAt:   time.Now().UTC().Add(time.Minute),
		}
		go func() {
			got, e := connection.Token(ctx, runtimeID)
			if e == nil && got != token {
				e = errors.New("token changed across replicas")
			}
			done <- e
		}()
		frame, scoped = readStudioControl(t, ws, agentID)
		var tokenRequest protocol.StudioTokenRequest
		if err := json.Unmarshal(scoped.Request, &tokenRequest); err != nil {
			t.Fatal(err)
		}
		writeStudioControl(
			t,
			ws,
			frame.Type+".response",
			protocol.StudioControlResponse{CorrelationID: tokenRequest.RequestID, Token: &token},
		)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		dispatch := conversation.StudioEnsureDispatch{
			RequestID:         uuid.New(),
			DispatchID:        uuid.New(),
			RuntimeID:         runtimeID,
			ActorUserID:       principal.UserID,
			ActorSessionID:    principal.SessionID,
			AuthorityType:     "human",
			DispatchExpiresAt: time.Now().Add(5 * time.Second),
			Payload: conversation.StudioReceiptRequest{
				Namespace:     uuid.New(),
				ReservationID: uuid.New(),
				Title:         "fixture",
			},
		}
		receipt := protocol.StudioReceipt{
			Namespace:     dispatch.Payload.Namespace,
			ReservationID: dispatch.Payload.ReservationID,
			ProjectID:     uuid.New(),
			State:         "bound",
		}
		go func() {
			got, e := connection.Ensure(ctx, dispatch)
			if e == nil && got.ProjectID != receipt.ProjectID {
				e = errors.New("receipt changed across replicas")
			}
			done <- e
		}()
		frame, scoped = readStudioControl(t, ws, agentID)
		var forwarded protocol.StudioDaemonDispatch
		if err := json.Unmarshal(scoped.Request, &forwarded); err != nil {
			t.Fatal(err)
		}
		if forwarded.DispatchID != dispatch.DispatchID || forwarded.Authority.DaemonID != identity.DaemonID ||
			forwarded.Request.RequestID != dispatch.RequestID {
			t.Fatal("relay changed dispatch identity")
		}
		writeStudioControl(t, ws, frame.Type+".response", protocol.StudioControlResponse{
			CorrelationID: dispatch.DispatchID,
			Dispatch: mustMarshalRaw(
				map[string]any{"dispatchId": dispatch.DispatchID, "result": map[string]any{
					"schemaVersion": 2,
					"requestId":     dispatch.RequestID,
					"operation":     "ensureBinding",
					"data":          receipt,
				}},
			),
		})
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		keys, err := rdb.Keys(ctx, "*").Result()
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			if kind := rdb.Type(ctx, key).Val(); kind != "zset" {
				t.Fatalf("RPC retained Redis data: %s (%s)", key, kind)
			}
		}
	})

	t.Run("duplicates fail closed across replicas including local owner", func(t *testing.T) {
		duplicate := attachStudioDaemon(t, thirdHub, identity)
		waitForHubIndex(t, "duplicate presence", func() bool {
			_, e := caller.Connect(ctx, runtimeID, agentID)
			return errors.Is(e, conversation.ErrStudioEditorRuntimeUnavailable)
		})
		for _, relay := range []*StudioRelay{caller, owner, third} {
			if _, e := relay.Connect(
				ctx,
				runtimeID,
				agentID,
			); !errors.Is(
				e,
				conversation.ErrStudioEditorRuntimeUnavailable,
			) {
				t.Fatalf("ambiguous connection = %v", e)
			}
		}
		if _, e := connection.Token(
			ctx,
			runtimeID,
		); !errors.Is(
			e,
			conversation.ErrStudioEditorRuntimeUnavailable,
		) {
			t.Fatalf("pinned duplicate = %v", e)
		}
		_ = duplicate.Close()
		waitStudioConnection(t, caller, runtimeID, agentID)
	})

	t.Run("overload is bounded without dropping replies", func(t *testing.T) {
		for range cap(owner.slots) {
			owner.slots <- struct{}{}
		}
		_, err := caller.Connect(ctx, runtimeID, agentID)
		for range cap(owner.slots) {
			<-owner.slots
		}
		if !errors.Is(err, conversation.ErrStudioEditorRateLimited) {
			t.Fatalf("overload = %v", err)
		}
		if err := owner.Ping(ctx); err != nil {
			t.Fatal("overload blocked health replies", err)
		}
		waitStudioConnection(t, caller, runtimeID, agentID)
	})

	t.Run("slow daemon does not block another runtime", func(t *testing.T) {
		otherRuntime := uuid.New()
		otherIdentity := identity
		otherIdentity.RuntimeIDs = []string{otherRuntime.String()}
		otherWS := attachStudioDaemon(t, hub, otherIdentity)
		other := waitStudioConnection(t, caller, otherRuntime, agentID)
		slowCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		slow := make(chan error, 1)
		go func() { _, e := connection.Probe(slowCtx, runtimeID); slow <- e }()
		readStudioControl(t, ws, agentID)
		fast := make(chan error, 1)
		go func() { _, e := other.Probe(ctx, otherRuntime); fast <- e }()
		frame, scoped := readStudioControl(t, otherWS, agentID)
		var probe protocol.StudioConnectionProbeRequest
		if err := json.Unmarshal(scoped.Request, &probe); err != nil {
			t.Fatal(err)
		}
		writeStudioControl(t, otherWS, frame.Type+".response", protocol.StudioControlResponse{
			CorrelationID: probe.ProbeID,
			Probe: &protocol.StudioConnectionProbeResponse{
				ProbeID:   probe.ProbeID,
				RuntimeID: otherRuntime,
				DaemonID:  identity.DaemonID,
			},
		})
		if e := <-fast; e != nil {
			t.Fatal(e)
		}
		if e := <-slow; !errors.Is(e, conversation.ErrStudioEditorOutcomeUnknown) {
			t.Fatalf("timeout = %v", e)
		}
		_ = otherWS.Close()
	})

	t.Run("reconnect never moves an admitted operation to the new socket", func(t *testing.T) {
		_ = ws.Close()
		waitForHubIndex(
			t,
			"old socket removed",
			func() bool { return !runtimeIndexed(hub, runtimeID.String()) },
		)
		newWS := attachStudioDaemon(t, thirdHub, identity)
		newConnection := waitStudioConnection(t, caller, runtimeID, agentID)
		if _, e := connection.Probe(
			ctx,
			runtimeID,
		); !errors.Is(
			e,
			conversation.ErrStudioEditorRuntimeUnavailable,
		) {
			t.Fatalf("old connection = %v", e)
		}
		if newConnection.Principal() != principal {
			t.Fatal("new owner principal differs")
		}
		_ = newWS.Close()
	})

	t.Run("deadline scope and authority checks happen before dispatch", func(t *testing.T) {
		rid := uuid.New()
		id := identity
		id.RuntimeIDs = []string{rid.String()}
		otherWS := attachStudioDaemon(t, hub, id)
		c := waitStudioConnection(t, caller, rid, agentID)
		if _, err := c.Token(ctx, uuid.New()); !errors.Is(err, conversation.ErrStudioEditorPermissionDenied) {
			t.Fatalf("foreign runtime = %v", err)
		}
		d := conversation.StudioEnsureDispatch{
			RuntimeID:         rid,
			ActorUserID:       uuid.New(),
			DispatchExpiresAt: time.Now().Add(time.Second),
		}
		if _, err := c.Ensure(ctx, d); !errors.Is(err, conversation.ErrStudioEditorPermissionDenied) {
			t.Fatalf("foreign user = %v", err)
		}
		d.ActorUserID = principal.UserID
		d.DispatchExpiresAt = time.Now().Add(-time.Second)
		if _, err := c.Ensure(ctx, d); !errors.Is(err, conversation.ErrStudioEditorGenerationConflict) {
			t.Fatalf("expired authority = %v", err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := c.Token(cancelled, rid); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled = %v", err)
		}
		_ = otherWS.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		if _, _, err := otherWS.ReadMessage(); err == nil {
			t.Fatal("rejected request reached daemon")
		}
		_ = otherWS.Close()
	})

	t.Run("expired presence cannot be revived by heartbeat", func(t *testing.T) {
		rid := uuid.New()
		id := identity
		id.RuntimeIDs = []string{rid.String()}
		otherWS := attachStudioDaemon(t, hub, id)
		old := waitStudioConnection(t, caller, rid, agentID)
		if err := owner.refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := caller.Connect(ctx, rid, agentID); err != nil {
			t.Fatal("heartbeat lost live socket", err)
		}
		// Expire every old member using a Redis timestamp in the past. This
		// exercises the server-side expiry predicate without a 45-second sleep.
		key := studioPresenceKey(rid.String())
		members := rdb.ZRange(ctx, key, 0, -1).Val()
		if len(members) != 1 {
			t.Fatal("missing live member")
		}
		if err := rdb.ZAdd(ctx, key, redis.Z{Score: 1, Member: members[0]}).Err(); err != nil {
			t.Fatal(err)
		}
		newWS := attachStudioDaemon(t, thirdHub, id)
		waitStudioConnection(t, caller, rid, agentID)
		if err := owner.refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := old.Probe(ctx, rid); !errors.Is(err, conversation.ErrStudioEditorRuntimeUnavailable) {
			t.Fatalf("expired socket = %v", err)
		}
		waitStudioConnection(t, caller, rid, agentID) // old cleanup preserves replacement
		_ = otherWS.Close()
		_ = newWS.Close()
	})

	t.Run("readiness traverses subscription and closing owner releases waiters", func(t *testing.T) {
		if err := caller.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		rid := uuid.New()
		id := identity
		id.RuntimeIDs = []string{rid.String()}
		otherWS := attachStudioDaemon(t, hub, id)
		c := waitStudioConnection(t, caller, rid, agentID)
		budget, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() { _, e := c.Probe(budget, rid); done <- e }()
		readStudioControl(t, otherWS, agentID)
		_ = owner.Close()
		if err := <-done; !errors.Is(err, conversation.ErrStudioEditorOutcomeUnknown) {
			t.Fatalf("closed in-flight = %v", err)
		}
		if err := owner.Ping(ctx); err == nil {
			t.Fatal("closed relay is ready")
		}
		_ = otherWS.Close()
	})
}

func startStudioRelay(t *testing.T, rdb *redis.Client) (*StudioRelay, *Hub) {
	t.Helper()
	hub := NewHub()
	relay, err := NewStudioRelay(t.Context(), hub, rdb)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, redis.ErrClosed) {
			t.Error(err)
		}
		_ = relay.Close()
	})
	return relay, hub
}

func attachStudioDaemon(t *testing.T, hub *Hub, identity ClientIdentity) *websocket.Conn {
	t.Helper()
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.HandleWebSocket(w, r, identity) }),
	)
	t.Cleanup(server.Close)
	ws, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

func waitStudioConnection(
	t *testing.T,
	relay *StudioRelay,
	runtimeID, agentID uuid.UUID,
) conversation.StudioEditorConnection {
	t.Helper()
	var connection conversation.StudioEditorConnection
	waitForHubIndex(t, "distributed Studio connection", func() bool {
		var err error
		connection, err = relay.Connect(t.Context(), runtimeID, agentID)
		return err == nil
	})
	return connection
}

func readStudioControl(
	t *testing.T,
	ws *websocket.Conn,
	agentID uuid.UUID,
) (protocol.Message, protocol.StudioScopedControl) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var frame protocol.Message
	if err := ws.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	var scoped protocol.StudioScopedControl
	if err := json.Unmarshal(frame.Payload, &scoped); err != nil || scoped.AgentID != agentID {
		t.Fatal("wrong Agent scope", err)
	}
	return frame, scoped
}

func writeStudioControl(t *testing.T, ws *websocket.Conn, kind string, response protocol.StudioControlResponse) {
	t.Helper()
	if err := ws.WriteJSON(protocol.Message{Type: kind, Payload: mustMarshalRaw(response)}); err != nil {
		t.Fatal(err)
	}
}
