// Package daemonws is the daemon WebSocket control plane: a hub of daemon connections indexed by
// runtime and user; best-effort wakeup hints; heartbeat frames; and
// request/response RPCs multiplexed over the same socket.
package daemonws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/pkg/protocol"
)

const (
	writeWait         = 10 * time.Second
	pongWait          = 60 * time.Second
	pingPeriod        = (pongWait * 9) / 10
	daemonWSReadLimit = 8 << 20
)

// ClientIdentity captures the already-authenticated daemon connection scope.
type ClientIdentity struct {
	DaemonID      string
	UserID        string
	CompanyIDs    []string
	RuntimeIDs    []string
	ClientVersion string
	// Capabilities is the raw X-Client-Capabilities header captured at connect,
	// so RPC handlers can honor the same capability gating as the HTTP path.
	Capabilities string
	// Principal is the bearer principal authenticated at connect. RPC
	// dispatch hands it to the transport-free handler cores so they see the
	// same auth scope as the HTTP path.
	Principal auth.UserPrincipal
}

func (i ClientIdentity) PrimaryCompanyID() string {
	if len(i.CompanyIDs) == 0 {
		return ""
	}
	return i.CompanyIDs[0]
}

type client struct {
	hub      *Hub
	conn     *websocket.Conn
	send     chan []byte
	identity ClientIdentity
	runtimes map[string]struct{}

	// ctx is cancelled when the connection tears down, so async RPC handlers
	// stop instead of running against a dead socket. cancel is invoked from
	// readPump's defer.
	ctx    context.Context
	cancel context.CancelFunc

	// sendMu guards sendClosed so a late async send (e.g. an RPC response
	// goroutine) can never write to the closed send channel. teardown flips
	// sendClosed under sendMu before closing send.
	sendMu     sync.Mutex
	sendClosed bool

	dedupMu  sync.Mutex
	seenIDs  map[string]struct{}
	seenList []string

	// rpcSem bounds concurrent RPC handlers for this connection.
	rpcSem              chan struct{}
	studioMu            sync.Mutex
	studioPending       map[uuid.UUID]studioPending
	connectionID        uuid.UUID
	studioPresenceMu    sync.Mutex
	studioPresenceReady bool
}

// trySend delivers frame to the write pump without blocking and without ever
// writing to a closed channel (safe against concurrent teardown). Returns false
// when the buffer is full or the connection is closing.
func (c *client) trySend(frame []byte) bool {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.sendClosed {
		return false
	}
	select {
	case c.send <- frame:
		return true
	default:
		return false
	}
}

// maxInFlightRPCPerClient caps concurrent RPCs per daemon socket.
const maxInFlightRPCPerClient = 8

const eventDedupCapacity = 128

// markSeen records eventID as already delivered to this client. Empty event IDs
// disable dedup and are always delivered.
func (c *client) markSeen(eventID string) bool {
	if eventID == "" {
		return true
	}
	c.dedupMu.Lock()
	defer c.dedupMu.Unlock()
	if c.seenIDs == nil {
		c.seenIDs = make(map[string]struct{}, eventDedupCapacity)
	}
	if _, ok := c.seenIDs[eventID]; ok {
		return false
	}
	c.seenIDs[eventID] = struct{}{}
	c.seenList = append(c.seenList, eventID)
	if len(c.seenList) > eventDedupCapacity {
		drop := c.seenList[0]
		c.seenList = c.seenList[1:]
		delete(c.seenIDs, drop)
	}
	return true
}

// HeartbeatHandler processes a daemon.heartbeat frame. The hub has already
// checked that beat.RuntimeID is one of the connection's runtimes; the
// handler checks the rest of the frame and returns the ack payload to send
// back. Returning an error skips the ack and is logged.
type HeartbeatHandler func(
	ctx context.Context,
	identity ClientIdentity,
	beat protocol.DaemonHeartbeatRequestPayload,
) (*protocol.DaemonHeartbeatAckPayload, error)

// RPCHandler processes a generic daemon.rpc_request. It dispatches on method,
// scopes work to the authenticated identity, and returns an HTTP-style status
// plus a response body or error. A returned error becomes a non-2xx RPC
// response; retry and fallback policy is method-specific. The handler runs in
// its own goroutine, so it must not assume it owns the read pump.
type RPCHandler func(ctx context.Context, identity ClientIdentity, method string, body json.RawMessage) (status int, respBody json.RawMessage, err error)

// Hub keeps daemon WebSocket connections indexed by runtime and user ID. Wakeup frames
// are best-effort; runs.claim retains its HTTP fallback, while durable
// transcript appends require the WebSocket RPC acknowledgement.
type Hub struct {
	upgrader websocket.Upgrader

	mu        sync.RWMutex
	clients   map[*client]bool
	byRuntime map[string]map[*client]bool
	byUser    map[string]map[*client]bool

	hbMu        sync.RWMutex
	onHeartbeat HeartbeatHandler

	rpcMu sync.RWMutex
	onRPC RPCHandler
	// Installed before serving, when cross-replica Studio admission is enabled.
	studioRelay *StudioRelay
}

func NewHub() *Hub {
	return &Hub{
		upgrader: websocket.Upgrader{
			// Daemon clients authenticate with Authorization headers before the
			// upgrade. Browsers cannot set those headers through the native WS API,
			// and the daemon plane does not accept cookies, so cookie-based CSWSH
			// does not apply to this endpoint. Re-evaluate this if daemon auth ever
			// grows cookie fallback.
			CheckOrigin: func(*http.Request) bool { return true },
		},
		clients:   make(map[*client]bool),
		byRuntime: make(map[string]map[*client]bool),
		byUser:    make(map[string]map[*client]bool),
	}
}

// SetHeartbeatHandler installs the callback used for daemon.heartbeat frames.
// Wiring is done after handler construction because the handler depends on
// DB queries that aren't available when the hub is built. A nil handler
// disables WS heartbeat processing — daemons fall back to HTTP heartbeat
// transparently because their fallback timer fires whenever no ack arrives.
func (h *Hub) SetHeartbeatHandler(fn HeartbeatHandler) {
	if h == nil {
		return
	}
	h.hbMu.Lock()
	h.onHeartbeat = fn
	h.hbMu.Unlock()
}

func (h *Hub) heartbeatHandler() HeartbeatHandler {
	h.hbMu.RLock()
	defer h.hbMu.RUnlock()
	return h.onHeartbeat
}

// SetRPCHandler installs the callback used for daemon.rpc_request frames.
// Like SetHeartbeatHandler it is wired after handler construction. A nil
// handler disables WS RPC; only methods with an explicit fallback may proceed.
func (h *Hub) SetRPCHandler(fn RPCHandler) {
	if h == nil {
		return
	}
	h.rpcMu.Lock()
	h.onRPC = fn
	h.rpcMu.Unlock()
}

func (h *Hub) rpcHandler() RPCHandler {
	h.rpcMu.RLock()
	defer h.rpcMu.RUnlock()
	return h.onRPC
}

func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request, identity ClientIdentity) {
	if len(identity.RuntimeIDs) == 0 && identity.UserID == "" {
		http.Error(w, `{"error":"runtime_ids or user identity required"}`, http.StatusBadRequest)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("daemon websocket upgrade failed", "error", err)
		return
	}

	runtimes := make(map[string]struct{}, len(identity.RuntimeIDs))
	for _, runtimeID := range identity.RuntimeIDs {
		if runtimeID != "" {
			runtimes[runtimeID] = struct{}{}
		}
	}
	c := &client{
		hub:          h,
		conn:         conn,
		send:         make(chan []byte, 16),
		identity:     identity,
		runtimes:     runtimes,
		rpcSem:       make(chan struct{}, maxInFlightRPCPerClient),
		connectionID: uuid.New(),
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	h.register(c)
	if h.studioRelay != nil {
		if err := h.studioRelay.register(c); err != nil {
			slog.Warn("Studio relay socket registration failed", "daemon_id", identity.DaemonID)
			h.unregister(c)
			_ = conn.Close()
			return
		}
	}

	go c.writePump()
	go c.readPump()
}

// NotifyRunAvailable sends a best-effort wakeup to daemons watching runtimeID.
func (h *Hub) NotifyRunAvailable(runtimeID, runID string) {
	h.notifyRunAvailable(runtimeID, runID, "")
}

// NotifyCompaniesChanged asks every connected daemon authenticated as userID
// to reconcile its company membership set.
func (h *Hub) NotifyCompaniesChanged(userID string) {
	h.notifyCompaniesChanged(userID, "")
}

// NotifyPendingWork tells daemons watching runtimeID that a heartbeat-carried
// request is queued, so they can heartbeat now instead of waiting for the next
// scheduled tick. Best-effort like every other hub notification: the daemon's
// own heartbeat schedule remains the correctness path.
func (h *Hub) NotifyPendingWork(runtimeID, kind string) {
	h.notifyPendingWork(runtimeID, kind, "")
}

func (h *Hub) notifyRunAvailable(runtimeID, runID, eventID string) {
	if h == nil || runtimeID == "" {
		return
	}
	data, err := runAvailableFrame(runtimeID, runID)
	if err != nil {
		return
	}
	delivered, deduped := h.notifyFrame(runtimeID, data, eventID)
	if delivered {
		M.WakeupDeliveredHit.Add(1)
	} else if !deduped {
		M.WakeupDeliveredMiss.Add(1)
	}
}

func (h *Hub) notifyCompaniesChanged(userID, eventID string) {
	if h == nil || userID == "" {
		return
	}
	data, err := companiesChangedFrame()
	if err != nil {
		return
	}
	h.notifyUserFrame(userID, data, eventID)
}

func (h *Hub) notifyPendingWork(runtimeID, kind, eventID string) {
	if h == nil || runtimeID == "" {
		return
	}
	data, err := pendingWorkFrame(runtimeID, kind)
	if err != nil {
		return
	}
	delivered, deduped := h.notifyFrame(runtimeID, data, eventID)
	if delivered {
		M.WakeupDeliveredHit.Add(1)
	} else if !deduped {
		M.WakeupDeliveredMiss.Add(1)
	}
}

func (h *Hub) DeliverDaemonRuntime(scopeID string, frame []byte, eventID string) {
	if h == nil {
		return
	}
	M.WakeupReceivedTotal.Add(1)
	var msg protocol.Message
	if err := json.Unmarshal(frame, &msg); err != nil {
		slog.Debug(
			"daemon websocket relay: invalid frame",
			"error",
			err,
			"scope_id",
			scopeID,
			"event_id",
			eventID,
		)
		M.WakeupDeliveredMiss.Add(1)
		return
	}
	switch msg.Type {
	case protocol.EventDaemonRunAvailable:
		var payload protocol.RunAvailablePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil || payload.RuntimeID == "" {
			slog.Debug(
				"daemon websocket relay: invalid run_available payload",
				"error",
				err,
				"scope_id",
				scopeID,
				"event_id",
				eventID,
			)
			M.WakeupDeliveredMiss.Add(1)
			return
		}
		delivered, deduped := h.notifyFrame(payload.RuntimeID, frame, eventID)
		if delivered {
			M.WakeupDeliveredHit.Add(1)
		} else if !deduped {
			M.WakeupDeliveredMiss.Add(1)
		}
	case protocol.EventDaemonCompaniesChanged:
		delivered, deduped := h.notifyUserFrame(scopeID, frame, eventID)
		if delivered {
			M.WakeupDeliveredHit.Add(1)
		} else if !deduped {
			M.WakeupDeliveredMiss.Add(1)
		}
	case protocol.EventDaemonPendingWork:
		var payload protocol.PendingWorkPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil || payload.RuntimeID == "" {
			slog.Debug(
				"daemon websocket relay: invalid pending_work payload",
				"error",
				err,
				"scope_id",
				scopeID,
				"event_id",
				eventID,
			)
			M.WakeupDeliveredMiss.Add(1)
			return
		}
		delivered, deduped := h.notifyFrame(payload.RuntimeID, frame, eventID)
		if delivered {
			M.WakeupDeliveredHit.Add(1)
		} else if !deduped {
			M.WakeupDeliveredMiss.Add(1)
		}
	default:
		M.WakeupDeliveredMiss.Add(1)
		return
	}
}

func (h *Hub) notifyFrame(runtimeID string, data []byte, eventID string) (delivered, deduped bool) {
	h.mu.RLock()
	clients := h.byRuntime[runtimeID]
	slow := make([]*client, 0)
	for c := range clients {
		if !c.markSeen(eventID) {
			deduped = true
			continue
		}
		select {
		case c.send <- data:
			delivered = true
		default:
			slow = append(slow, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range slow {
		h.unregister(c)
		c.conn.Close()
	}
	if len(slow) > 0 {
		M.SlowEvictionsTotal.Add(int64(len(slow)))
	}
	return delivered, deduped
}

func (h *Hub) notifyUserFrame(userID string, data []byte, eventID string) (delivered, deduped bool) {
	h.mu.RLock()
	clients := h.byUser[userID]
	slow := make([]*client, 0)
	for c := range clients {
		if !c.markSeen(eventID) {
			deduped = true
			continue
		}
		select {
		case c.send <- data:
			delivered = true
		default:
			slow = append(slow, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range slow {
		h.unregister(c)
		c.conn.Close()
	}
	if len(slow) > 0 {
		M.SlowEvictionsTotal.Add(int64(len(slow)))
	}
	return delivered, deduped
}

func runAvailableFrame(runtimeID, runID string) ([]byte, error) {
	return json.Marshal(protocol.Message{
		Type: protocol.EventDaemonRunAvailable,
		Payload: mustMarshalRaw(protocol.RunAvailablePayload{
			RuntimeID: runtimeID,
			RunID:     runID,
		}),
	})
}

func companiesChangedFrame() ([]byte, error) {
	return json.Marshal(protocol.Message{
		Type:    protocol.EventDaemonCompaniesChanged,
		Payload: mustMarshalRaw(protocol.CompaniesChangedPayload{}),
	})
}

func pendingWorkFrame(runtimeID, kind string) ([]byte, error) {
	return json.Marshal(protocol.Message{
		Type: protocol.EventDaemonPendingWork,
		Payload: mustMarshalRaw(protocol.PendingWorkPayload{
			RuntimeID: runtimeID,
			Kind:      kind,
		}),
	})
}

func mustMarshalRaw(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

func (h *Hub) register(c *client) {
	h.mu.Lock()
	h.clients[c] = true
	for runtimeID := range c.runtimes {
		conns := h.byRuntime[runtimeID]
		if conns == nil {
			conns = make(map[*client]bool)
			h.byRuntime[runtimeID] = conns
		}
		conns[c] = true
	}
	if userID := c.identity.UserID; userID != "" {
		conns := h.byUser[userID]
		if conns == nil {
			conns = make(map[*client]bool)
			h.byUser[userID] = conns
		}
		conns[c] = true
	}
	total := len(h.clients)
	h.mu.Unlock()

	M.ConnectsTotal.Add(1)
	M.ActiveConnections.Add(1)
	slog.Info("daemon websocket connected",
		"daemon_id", c.identity.DaemonID,
		"user_id", c.identity.UserID,
		"company_id", c.identity.PrimaryCompanyID(),
		"company_ids", c.identity.CompanyIDs,
		"runtimes", len(c.runtimes),
		"client_version", c.identity.ClientVersion,
		"total_clients", total,
	)
}

func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	if !h.clients[c] {
		h.mu.Unlock()
		return
	}
	delete(h.clients, c)
	if c.cancel != nil {
		c.cancel()
	}
	for runtimeID := range c.runtimes {
		if conns := h.byRuntime[runtimeID]; conns != nil {
			delete(conns, c)
			if len(conns) == 0 {
				delete(h.byRuntime, runtimeID)
			}
		}
	}
	if userID := c.identity.UserID; userID != "" {
		if conns := h.byUser[userID]; conns != nil {
			delete(conns, c)
			if len(conns) == 0 {
				delete(h.byUser, userID)
			}
		}
	}
	c.sendMu.Lock()
	c.sendClosed = true
	close(c.send)
	c.sendMu.Unlock()
	total := len(h.clients)
	h.mu.Unlock()

	if h.studioRelay != nil {
		h.studioRelay.unregister(c)
	}

	M.DisconnectsTotal.Add(1)
	M.ActiveConnections.Add(-1)
	slog.Info("daemon websocket disconnected",
		"daemon_id", c.identity.DaemonID,
		"user_id", c.identity.UserID,
		"company_id", c.identity.PrimaryCompanyID(),
		"company_ids", c.identity.CompanyIDs,
		"runtimes", len(c.runtimes),
		"total_clients", total,
	)
}

func (c *client) readPump() {
	defer func() {
		// Cancel first so async RPC handlers stop before we close the send
		// channel, then unregister (which marks send closed).
		c.cancel()
		c.hub.unregister(c)
		c.conn.Close()
	}()

	// Read limit sized for daemon.rpc_request frames carrying a machine's full
	// runtime_id set, well above the tiny heartbeat/wakeup frames.
	c.conn.SetReadLimit(daemonWSReadLimit)
	// A read deadline can only fail on a connection that is already dead, and
	// the ReadMessage below then fails and tears the client down, so there is
	// nothing to act on here.
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
			) {
				slog.Debug(
					"daemon websocket read error",
					"error",
					err,
					"daemon_id",
					c.identity.DaemonID,
				)
			}
			return
		}
		c.handleFrame(raw)
	}
}

func (c *client) handleFrame(raw []byte) {
	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		slog.Debug("daemon websocket invalid frame", "error", err, "daemon_id", c.identity.DaemonID)
		return
	}
	switch msg.Type {
	case protocol.EventStudioConnectionProbeResponse,
		protocol.EventStudioEditorDispatchResponse,
		protocol.EventStudioBindingVerifyResponse,
		protocol.EventStudioTokenResponse:
		c.handleStudioResponse(msg.Type, msg.Payload)
	case protocol.EventDaemonHeartbeat:
		c.handleHeartbeatFrame(msg.Payload)
	case protocol.EventDaemonRPCRequest:
		c.handleRPCFrame(msg.Payload)
	default:
		// Unknown app messages are intentionally ignored for forward
		// compatibility with future daemon → server message types.
	}
}

// handleRPCFrame processes a generic daemon.rpc_request: it runs the
// registered RPC handler in its own goroutine (so a DB-bound claim does not
// stall the read pump or the next heartbeat) and writes back a
// daemon.rpc_response echoing the request id. A missing handler or a full
// in-flight slot yields a non-2xx response for method-specific recovery.
func (c *client) handleRPCFrame(raw json.RawMessage) {
	var req protocol.RPCRequestPayload
	if err := json.Unmarshal(raw, &req); err != nil {
		slog.Debug("daemon websocket rpc invalid payload", "error", err, "daemon_id", c.identity.DaemonID)
		return
	}
	if req.RequestID == "" {
		slog.Debug("daemon websocket rpc missing request_id", "daemon_id", c.identity.DaemonID)
		return
	}
	handler := c.hub.rpcHandler()
	if handler == nil {
		c.sendRPCResponse(req.RequestID, http.StatusServiceUnavailable, nil, "rpc handler unavailable")
		return
	}
	// Bound concurrent handlers; if saturated, tell the daemon to retry or use
	// that method's fallback rather than queueing unbounded work on one socket.
	select {
	case c.rpcSem <- struct{}{}:
	default:
		c.sendRPCResponse(req.RequestID, http.StatusTooManyRequests, nil, "too many in-flight rpc requests")
		return
	}
	go func() {
		defer func() { <-c.rpcSem }()
		// Bound server-side execution by the caller's requested budget (in
		// addition to the connection ctx), so a slow RPC is cancelled — and its
		// work rolled back — rather than committing after the daemon has already
		// timed out and fallen back to HTTP. The daemon waits a grace period
		// beyond this budget, so a claim that DID commit before the deadline
		// still reports back in time.
		hctx := c.ctx
		if req.TimeoutMs > 0 {
			var cancel context.CancelFunc
			hctx, cancel = context.WithTimeout(c.ctx, time.Duration(req.TimeoutMs)*time.Millisecond)
			defer cancel()
		}
		status, body, err := handler(hctx, c.identity, req.Method, req.Body)
		if err != nil {
			if status < 400 {
				status = http.StatusInternalServerError
			}
			c.sendRPCResponse(req.RequestID, status, nil, err.Error())
			return
		}
		c.sendRPCResponse(req.RequestID, status, body, "")
	}()
}

func (c *client) sendRPCResponse(requestID string, status int, body json.RawMessage, errMsg string) {
	frame, err := json.Marshal(protocol.Message{
		Type: protocol.EventDaemonRPCResponse,
		Payload: mustMarshalRaw(protocol.RPCResponsePayload{
			RequestID: requestID,
			Status:    status,
			Body:      body,
			Error:     errMsg,
		}),
	})
	if err != nil {
		slog.Debug("daemon websocket rpc response marshal failed", "error", err)
		return
	}
	if !c.trySend(frame) {
		// Send buffer full or connection closing — drop the response; the
		// daemon's per-request timeout fires and its method-specific recovery
		// takes over.
		slog.Debug("daemon websocket rpc response dropped",
			"daemon_id", c.identity.DaemonID, "request_id", requestID)
	}
}

// handleHeartbeatFrame processes an inbound daemon.heartbeat from the daemon,
// invokes the hub's handler, and writes back a daemon.heartbeat_ack.
func (c *client) handleHeartbeatFrame(raw json.RawMessage) {
	handler := c.hub.heartbeatHandler()
	if handler == nil {
		// Server doesn't have a heartbeat handler wired — daemon will time
		// out waiting for an ack and fall back to HTTP heartbeat.
		return
	}

	var payload protocol.DaemonHeartbeatRequestPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Debug("daemon websocket heartbeat invalid payload", "error", err, "daemon_id", c.identity.DaemonID)
		return
	}
	if payload.RuntimeID == "" {
		slog.Debug("daemon websocket heartbeat missing runtime_id", "daemon_id", c.identity.DaemonID)
		return
	}
	if _, ok := c.runtimes[payload.RuntimeID]; !ok {
		// The connection authenticated for a fixed runtime set; reject any
		// heartbeat for a runtime the client did not register for.
		slog.Warn("daemon websocket heartbeat for unauthorized runtime",
			"daemon_id", c.identity.DaemonID,
			"runtime_id", payload.RuntimeID)
		return
	}

	// The handler runs on the read pump, so a wedged statement inside it
	// blocks every subsequent frame while the goroutine outlives the socket.
	// A generous server-owned timeout bounds that: far above any legitimate
	// claim, far below forever. Cancelling mid-statement only rolls the
	// transaction back, and a claim that committed before the deadline but
	// lost its ack is exactly what the dispatched-at reclaim fence recovers —
	// the same lost-response contract the HTTP path relies on. Client
	// cancellation still never propagates here.
	handlerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ack, err := handler(handlerCtx, c.identity, payload)
	if err != nil {
		slog.Warn("daemon websocket heartbeat handler failed",
			"error", err,
			"daemon_id", c.identity.DaemonID,
			"runtime_id", payload.RuntimeID)
		return
	}
	if ack == nil {
		return
	}
	frame, err := json.Marshal(protocol.Message{
		Type:    protocol.EventDaemonHeartbeatAck,
		Payload: mustMarshalRaw(ack),
	})
	if err != nil {
		slog.Debug("daemon websocket heartbeat ack marshal failed", "error", err)
		return
	}
	if !c.trySend(frame) {
		// Send buffer full or connection closing — drop; HTTP heartbeat resumes.
		slog.Debug("daemon websocket heartbeat ack dropped",
			"daemon_id", c.identity.DaemonID,
			"runtime_id", payload.RuntimeID)
	}
}

func (c *client) writePump() {
	// The socket principal is frozen at upgrade. Reconnect at token expiry
	// so subsequent Studio admission gets freshly authenticated authority.
	var expired <-chan time.Time
	if expiry := c.identity.Principal.AccessExpiresAt; !expiry.IsZero() {
		timer := time.NewTimer(time.Until(expiry))
		defer timer.Stop()
		expired = timer.C
	}
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case <-expired:
			return
		case message, ok := <-c.send:
			// A write deadline only fails on a dead connection; the write
			// below then fails and this pump returns.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Best-effort courtesy close frame: the send channel is
				// already closed and this client is going away regardless.
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				slog.Debug(
					"daemon websocket write error",
					"error",
					err,
					"daemon_id",
					c.identity.DaemonID,
				)
				return
			}
		case <-ticker.C:
			// Same as above: a failed deadline surfaces as a failed ping.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
