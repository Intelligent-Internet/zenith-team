package daemonws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/pkg/protocol"
)

// StudioRelay routes admission RPCs to the process holding the authenticated
// daemon socket. Pub/Sub carries transient requests and replies (including
// tokens); none of these frames belong in the retained realtime event streams.
// PostgreSQL/Studio reservations remain the authority for mutation recovery.
type StudioRelay struct {
	hub       *Hub
	redis     *redis.Client
	nodeID    uuid.UUID
	sub       *redis.PubSub
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
	mu        sync.Mutex
	pending   map[uuid.UUID]studioRelayPending
	slots     chan struct{}
}

var _ conversation.StudioEditorRelay = (*StudioRelay)(nil)

type studioRelayRequest struct {
	ID           uuid.UUID                          `json:"id"`
	CallerNodeID uuid.UUID                          `json:"callerNodeId"`
	ConnectionID uuid.UUID                          `json:"connectionId"`
	RuntimeID    uuid.UUID                          `json:"runtimeId"`
	AgentID      uuid.UUID                          `json:"agentId"`
	Method       string                             `json:"method"`
	Deadline     time.Time                          `json:"deadline"`
	Dispatch     *conversation.StudioEnsureDispatch `json:"dispatch,omitempty"`
}

type studioRelayResponse struct {
	ID           uuid.UUID       `json:"id"`
	OwnerNodeID  uuid.UUID       `json:"ownerNodeId"`
	ConnectionID uuid.UUID       `json:"connectionId"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorCode    string          `json:"errorCode,omitempty"`
}

// Use explicit wire fields: UserPrincipal is the sole in-process principal,
// but has no JSON tags and must not introduce PascalCase transport keys.
type studioRelayPrincipal struct {
	UserID          uuid.UUID        `json:"userId"`
	SessionID       uuid.UUID        `json:"sessionId"`
	AuthKind        auth.TokenKind   `json:"authKind"`
	AccessExpiresAt time.Time        `json:"accessExpiresAt"`
	CompanyID       uuid.UUID        `json:"companyId"`
	Role            auth.CompanyRole `json:"role"`
}

type studioRelayPending struct {
	owner      uuid.UUID
	connection uuid.UUID
	response   chan studioRelayResponse
}

type studioRemoteConnection struct {
	relay      *StudioRelay
	owner      uuid.UUID
	connection uuid.UUID
	runtimeID  uuid.UUID
	agentID    uuid.UUID
	principal  auth.UserPrincipal
}

// NewStudioRelay subscribes before HTTP starts accepting daemon sockets or
// admission requests. Run must be supervised for the lifetime of the server.
func NewStudioRelay(ctx context.Context, hub *Hub, rdb *redis.Client) (*StudioRelay, error) {
	// PUBLISH must not be automatically retried after an ambiguous network
	// failure. Own this client so other Redis consumers keep their policy.
	options := *rdb.Options()
	options.MaxRetries = -1
	options.ContextTimeoutEnabled = true
	options.PushNotificationProcessor = nil
	r := &StudioRelay{
		hub: hub, redis: redis.NewClient(&options), nodeID: uuid.New(), closed: make(chan struct{}),
		pending: make(map[uuid.UUID]studioRelayPending), slots: make(chan struct{}, 128),
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r.sub = r.redis.Subscribe(ctx, r.requestChannel(r.nodeID), r.responseChannel(r.nodeID))
	// Receive both subscription acknowledgements before permitting publication.
	for range 2 {
		if _, err := r.sub.Receive(ctx); err != nil {
			_ = r.sub.Close()
			_ = r.redis.Close()
			return nil, fmt.Errorf("subscribe Studio relay: %w", err)
		}
	}
	hub.studioRelay = r // installed during composition, before any connections
	return r, nil
}

func (r *StudioRelay) requestChannel(node uuid.UUID) string {
	return "daemon:studio:v1:requests:" + node.String()
}

func (r *StudioRelay) responseChannel(node uuid.UUID) string {
	return "daemon:studio:v1:responses:" + node.String()
}
func studioPresenceKey(runtimeID string) string { return "daemon:studio:v1:presence:" + runtimeID }
func (r *StudioRelay) member(c *client) string {
	return r.nodeID.String() + ":" + c.connectionID.String()
}

// Each socket owns its own sorted-set member. An old disconnect can only remove
// that member, never a replacement's. Multiple live members fail closed across
// ALL replicas. Redis time avoids clock skew between API nodes.
var studioPresenceScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1])*1000 + math.floor(tonumber(t[2])/1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
if ARGV[1] == 'add' or (ARGV[1] == 'refresh' and redis.call('ZSCORE', KEYS[1], ARGV[2])) then
  redis.call('ZADD', KEYS[1], now+45000, ARGV[2])
  redis.call('PEXPIRE', KEYS[1], 45000)
end
return redis.call('ZRANGE', KEYS[1], 0, -1)
`)

func (r *StudioRelay) register(c *client) error {
	c.studioPresenceMu.Lock()
	defer c.studioPresenceMu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	for runtimeID := range c.runtimes {
		if err := studioPresenceScript.Run(ctx, r.redis, []string{studioPresenceKey(runtimeID)}, "add", r.member(c)).
			Err(); err != nil {
			return err
		}
	}
	c.studioPresenceReady = true
	return nil
}

func (r *StudioRelay) unregister(c *client) {
	c.studioPresenceMu.Lock()
	defer c.studioPresenceMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pipe := r.redis.Pipeline()
	for runtimeID := range c.runtimes {
		pipe.ZRem(ctx, studioPresenceKey(runtimeID), r.member(c))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("Studio relay presence cleanup failed", "node_id", r.nodeID, "connection_id", c.connectionID)
	}
}

func (r *StudioRelay) clients() []*client {
	r.hub.mu.RLock()
	defer r.hub.mu.RUnlock()
	clients := make([]*client, 0, len(r.hub.clients))
	for c := range r.hub.clients {
		clients = append(clients, c)
	}
	return clients
}

func (r *StudioRelay) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	pipe := r.redis.Pipeline()
	checks := make(map[*client][]*redis.Cmd)
	for _, c := range r.clients() {
		c.studioPresenceMu.Lock()
		if c.studioPresenceReady && c.ctx.Err() == nil {
			for runtimeID := range c.runtimes {
				checks[c] = append(
					checks[c],
					studioPresenceScript.Eval(
						ctx,
						pipe,
						[]string{studioPresenceKey(runtimeID)},
						"refresh",
						r.member(c),
					),
				)
			}
		}
		c.studioPresenceMu.Unlock()
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	for c, commands := range checks {
		for _, command := range commands {
			members, err := command.StringSlice()
			if err != nil {
				return err
			}
			if slices.Contains(members, r.member(c)) {
				continue
			}
			// An expired socket never reclaims ownership via heartbeat. Reconnect
			// authenticates a new socket and invalidates in-flight admission.
			r.hub.unregister(c)
			_ = c.conn.Close()
			break
		}
	}
	return nil
}

func (r *StudioRelay) owner(ctx context.Context, runtimeID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	members, err := studioPresenceScript.Run(ctx, r.redis, []string{studioPresenceKey(runtimeID.String())}, "read", "").
		StringSlice()
	if err != nil {
		return uuid.Nil, uuid.Nil, conversation.ErrStudioEditorAuthorityUnavailable
	}
	if len(members) != 1 {
		return uuid.Nil, uuid.Nil, conversation.ErrStudioEditorRuntimeUnavailable
	}
	parts := strings.Split(members[0], ":")
	if len(parts) != 2 {
		return uuid.Nil, uuid.Nil, conversation.ErrStudioEditorUpstreamInvalid
	}
	node, e1 := uuid.Parse(parts[0])
	connection, e2 := uuid.Parse(parts[1])
	if e1 != nil || e2 != nil {
		return uuid.Nil, uuid.Nil, conversation.ErrStudioEditorUpstreamInvalid
	}
	return node, connection, nil
}

// Run fails on subscription/registry failure so serveApplication drains this
// process instead of advertising unreachable sockets. The process supervisor
// restarts it; subscriptions and presence are re-established before serving.
func (r *StudioRelay) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = r.Close() })
	defer stopClose()
	var workers sync.WaitGroup
	defer workers.Wait()
	defer func() { cancel(); _ = r.Close() }()
	errorsCh := make(chan error, 1)
	workers.Go(func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.refresh(ctx); err != nil {
					errorsCh <- err
					_ = r.Close()
					return
				}
			}
		}
	})
	for {
		message, err := r.sub.ReceiveMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case refreshErr := <-errorsCh:
				return fmt.Errorf("refresh Studio relay: %w", refreshErr)
			default:
			}
			select {
			case <-r.closed:
				return redis.ErrClosed
			default:
			}
			return fmt.Errorf("receive Studio relay: %w", err)
		}
		if message.Channel == r.responseChannel(r.nodeID) {
			r.deliver(message.Payload)
			continue
		}
		var req studioRelayRequest
		if len(message.Payload) > protocol.StudioRequestLimit ||
			json.Unmarshal([]byte(message.Payload), &req) != nil ||
			req.ID == uuid.Nil ||
			req.CallerNodeID == uuid.Nil {
			continue
		}
		if req.Method == "ping" {
			r.reply(ctx, req, nil, nil)
			continue
		}
		select {
		case r.slots <- struct{}{}:
			workers.Go(func() {
				defer func() { <-r.slots }()
				result, callErr := r.invoke(ctx, req)
				r.reply(ctx, req, result, callErr)
			})
		default:
			r.reply(ctx, req, nil, conversation.ErrStudioEditorRateLimited)
		}
	}
}

// Close stops publication/waiters and unregisters only this process's sockets.
func (r *StudioRelay) Close() error {
	r.closeOnce.Do(func() {
		close(r.closed)
		r.closeErr = r.sub.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pipe := r.redis.Pipeline()
		for _, c := range r.clients() {
			c.cancel()
			// Join any registration before removing its exact members. Socket
			// teardown also removes them; both removals are idempotent.
			c.studioPresenceMu.Lock()
			c.studioPresenceReady = false
			c.studioPresenceMu.Unlock()
			_ = c.conn.Close()
			for runtimeID := range c.runtimes {
				pipe.ZRem(ctx, studioPresenceKey(runtimeID), r.member(c))
			}
		}
		_, err := pipe.Exec(ctx)
		r.closeErr = errors.Join(r.closeErr, err, r.redis.Close())
	})
	return r.closeErr
}

func (r *StudioRelay) Ping(ctx context.Context) error {
	_, err := r.exchange(ctx, r.nodeID, studioRelayRequest{Method: "ping"})
	return err
}

func (r *StudioRelay) Connect(
	ctx context.Context,
	runtimeID, agentID uuid.UUID,
) (conversation.StudioEditorConnection, error) {
	node, connection, err := r.owner(ctx, runtimeID)
	if err != nil {
		return nil, err
	}
	s := &studioRemoteConnection{
		relay:      r,
		owner:      node,
		connection: connection,
		runtimeID:  runtimeID,
		agentID:    agentID,
	}
	var p studioRelayPrincipal
	if err := s.call(ctx, "pin", nil, &p); err != nil {
		return nil, err
	}
	s.principal = auth.UserPrincipal{
		UserID:          p.UserID,
		SessionID:       p.SessionID,
		AuthKind:        p.AuthKind,
		AccessExpiresAt: p.AccessExpiresAt,
		CompanyID:       p.CompanyID,
		Role:            p.Role,
	}
	return s, nil
}

func (r *StudioRelay) invoke(ctx context.Context, req studioRelayRequest) (any, error) {
	select {
	case <-r.closed:
		return nil, conversation.ErrStudioEditorAuthorityUnavailable
	default:
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.Deadline.IsZero() || !req.Deadline.After(time.Now()) {
		return nil, conversation.ErrStudioEditorAuthorityUnavailable
	}
	ctx, cancel := context.WithDeadline(ctx, req.Deadline)
	defer cancel()
	node, connection, err := r.owner(ctx, req.RuntimeID)
	if err != nil {
		return nil, err
	}
	if node != r.nodeID || connection != req.ConnectionID {
		return nil, conversation.ErrStudioEditorRuntimeUnavailable
	}
	r.hub.mu.RLock()
	var local *studioConnection
	for c := range r.hub.byRuntime[req.RuntimeID.String()] {
		if c.connectionID == connection {
			local = &studioConnection{client: c, agentID: req.AgentID}
			break
		}
	}
	r.hub.mu.RUnlock()
	if local == nil || local.client.ctx.Err() != nil {
		return nil, conversation.ErrStudioEditorRuntimeUnavailable
	}
	p := local.Principal()
	if !p.AccessExpiresAt.IsZero() && !p.AccessExpiresAt.After(time.Now()) {
		return nil, conversation.ErrStudioEditorRuntimeUnavailable
	}
	switch req.Method {
	case "pin":
		return studioRelayPrincipal{
			p.UserID,
			p.SessionID,
			p.AuthKind,
			p.AccessExpiresAt,
			p.CompanyID,
			p.Role,
		}, nil
	case "probe":
		return local.Probe(ctx, req.RuntimeID)
	case "token":
		return local.Token(ctx, req.RuntimeID)
	case "ensure", "verify":
		d := req.Dispatch
		if d == nil || d.RuntimeID != req.RuntimeID || d.ActorUserID != p.UserID {
			return nil, conversation.ErrStudioEditorPermissionDenied
		}
		if !d.DispatchExpiresAt.After(time.Now()) {
			return nil, conversation.ErrStudioEditorGenerationConflict
		}
		if req.Method == "ensure" {
			return local.Ensure(ctx, *d)
		}
		return nil, local.VerifyProject(ctx, *d)
	default:
		return nil, conversation.ErrStudioEditorUnsupported
	}
}

func (r *StudioRelay) exchange(ctx context.Context, node uuid.UUID, req studioRelayRequest) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req.ID, req.CallerNodeID = uuid.New(), r.nodeID
	req.Deadline, _ = ctx.Deadline()
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > protocol.StudioRequestLimit {
		return nil, conversation.ErrStudioEditorUnsupported
	}
	pending := studioRelayPending{
		owner:      node,
		connection: req.ConnectionID,
		response:   make(chan studioRelayResponse, 1),
	}
	r.mu.Lock()
	if len(r.pending) >= 256 {
		r.mu.Unlock()
		return nil, conversation.ErrStudioEditorRateLimited
	}
	r.pending[req.ID] = pending
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, req.ID); r.mu.Unlock() }()
	select {
	case <-r.closed:
		return nil, conversation.ErrStudioEditorAuthorityUnavailable
	default:
	}
	// PUBLISH is never replayed at this layer. A transport failure may happen
	// after delivery, so it must not invite an automatic mutation retry.
	pubCtx, stopPublish := context.WithTimeout(ctx, 2*time.Second)
	count, err := r.redis.Publish(pubCtx, r.requestChannel(node), raw).Result()
	stopPublish()
	if err != nil {
		return nil, conversation.ErrStudioEditorOutcomeUnknown
	}
	if count == 0 {
		return nil, conversation.ErrStudioEditorRuntimeUnavailable
	}
	select {
	case <-ctx.Done():
		return nil, conversation.ErrStudioEditorOutcomeUnknown
	case <-r.closed:
		return nil, conversation.ErrStudioEditorOutcomeUnknown
	case response := <-pending.response:
		if response.ErrorCode != "" {
			return nil, studioControlError(response.ErrorCode)
		}
		return response.Result, nil
	}
}

func (r *StudioRelay) reply(ctx context.Context, req studioRelayRequest, result any, callErr error) {
	response := studioRelayResponse{ID: req.ID, OwnerNodeID: r.nodeID, ConnectionID: req.ConnectionID}
	if callErr != nil {
		response.ErrorCode = studioControlCode(callErr)
		slog.Debug("Studio relay operation failed", "node_id", r.nodeID, "caller_node_id", req.CallerNodeID,
			"request_id", req.ID, "runtime_id", req.RuntimeID, "connection_id", req.ConnectionID,
			"operation", req.Method, "error_code", response.ErrorCode)
	} else {
		response.Result = mustMarshalRaw(result)
	}
	raw := mustMarshalRaw(response)
	if len(raw) > 64<<10 {
		response.Result = nil
		response.ErrorCode = "upstream_invalid"
		raw = mustMarshalRaw(response)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := r.redis.Publish(ctx, r.responseChannel(req.CallerNodeID), []byte(raw)).Err(); err != nil {
		slog.Warn(
			"Studio relay reply failed",
			"node_id",
			r.nodeID,
			"caller_node_id",
			req.CallerNodeID,
			"request_id",
			req.ID,
		)
	}
}

func (r *StudioRelay) deliver(raw string) {
	var response studioRelayResponse
	if len(raw) > 64<<10 || json.Unmarshal([]byte(raw), &response) != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pending, ok := r.pending[response.ID]
	if !ok || pending.owner != response.OwnerNodeID || pending.connection != response.ConnectionID {
		return
	}
	select {
	case pending.response <- response:
	default:
	}
}

func (s *studioRemoteConnection) Principal() auth.UserPrincipal { return s.principal }
func (s *studioRemoteConnection) AgentID() uuid.UUID            { return s.agentID }

func (s *studioRemoteConnection) call(
	ctx context.Context,
	method string,
	dispatch *conversation.StudioEnsureDispatch,
	output any,
) error {
	req := studioRelayRequest{
		ConnectionID: s.connection,
		RuntimeID:    s.runtimeID,
		AgentID:      s.agentID,
		Method:       method,
		Dispatch:     dispatch,
	}
	var raw json.RawMessage
	var err error
	if s.owner == s.relay.nodeID {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req.Deadline, _ = ctx.Deadline()
		var result any
		result, err = s.relay.invoke(ctx, req)
		if err == nil {
			raw = mustMarshalRaw(result)
		}
	} else {
		raw, err = s.relay.exchange(ctx, s.owner, req)
	}
	if err != nil {
		return err
	}
	if output != nil && json.Unmarshal(raw, output) != nil {
		return conversation.ErrStudioEditorUpstreamInvalid
	}
	return nil
}

func (s *studioRemoteConnection) Probe(
	ctx context.Context,
	runtimeID uuid.UUID,
) (conversation.StudioConnectionResponse, error) {
	var result conversation.StudioConnectionResponse
	if runtimeID != s.runtimeID {
		return result, conversation.ErrStudioEditorPermissionDenied
	}
	err := s.call(ctx, "probe", nil, &result)
	return result, err
}

func (s *studioRemoteConnection) Token(ctx context.Context, runtimeID uuid.UUID) (protocol.StudioAccessToken, error) {
	var result protocol.StudioAccessToken
	if runtimeID != s.runtimeID {
		return result, conversation.ErrStudioEditorPermissionDenied
	}
	err := s.call(ctx, "token", nil, &result)
	return result, err
}

func (s *studioRemoteConnection) Ensure(
	ctx context.Context,
	d conversation.StudioEnsureDispatch,
) (conversation.StudioReceiptResponse, error) {
	var result conversation.StudioReceiptResponse
	err := s.call(ctx, "ensure", &d, &result)
	return result, err
}

func (s *studioRemoteConnection) VerifyProject(ctx context.Context, d conversation.StudioEnsureDispatch) error {
	return s.call(ctx, "verify", &d, nil)
}

func studioControlCode(err error) string {
	for _, code := range []string{"authority_unavailable", "permission_denied", "binding_conflict", "generation_conflict", "request_conflict", "reauth_required", "upstream_denied", "source_unavailable", "rate_limited", "outcome_unknown", "unsupported", "origin_conflict", "account_conflict", "runtime_unavailable"} {
		if errors.Is(err, studioControlError(code)) {
			return code
		}
	}
	return "upstream_invalid"
}
