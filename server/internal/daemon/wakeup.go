package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"boardly.dev/boardly/server/pkg/protocol"
)

var errRuntimeSetChanged = errors.New("runtime set changed")

const (
	runWakeupMaxBackoff = 30 * time.Second

	// The authenticated control connection carries runs.claim RPC responses,
	// not only small wakeup hints — one response can hold a whole claim batch.
	runWakeupReadLimit int64 = 64 << 20
)

var (
	runWakeupPongWait          = 60 * time.Second
	runWakeupWriteWait         = 10 * time.Second
	runWakeupBackoffResetAfter = 10 * time.Second
)

type runWakeup struct {
	runtimeID string
}

// runWakeupLoop maintains the daemon WS control connection with 1s→30s
// exponential backoff, ±20% jitter, reset after 10s of connection uptime.
func (d *Daemon) runWakeupLoop(ctx context.Context, runWakeups chan<- runWakeup) {
	backoff := time.Second
	runtimeSetCh, unsub := d.runtimeSet.Subscribe()
	defer unsub()

	for {
		runtimeIDs := d.claimableRuntimeIDs()
		connectedFor, err := d.runWakeupConnection(ctx, runtimeIDs, runWakeups, runtimeSetCh)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRuntimeSetChanged) {
			backoff = time.Second
			continue
		}
		if shouldResetRunWakeupBackoff(connectedFor) {
			backoff = time.Second
		}
		if err != nil {
			d.logger.Debug(
				"run wakeup websocket unavailable; polling fallback remains active",
				"error",
				err,
				"retry_in",
				backoff,
			)
		}

		if err := sleepWithContextOrRuntimeChange(ctx, jitterDuration(backoff), runtimeSetCh); err != nil {
			return
		}
		if backoff < runWakeupMaxBackoff {
			backoff *= 2
			if backoff > runWakeupMaxBackoff {
				backoff = runWakeupMaxBackoff
			}
		}
	}
}

func shouldResetRunWakeupBackoff(connectedFor time.Duration) bool {
	if connectedFor <= 0 {
		return false
	}
	return runWakeupBackoffResetAfter <= 0 || connectedFor >= runWakeupBackoffResetAfter
}

func jitterDuration(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := d / 5
	if spread <= 0 {
		return d
	}
	delta := time.Duration(rand.Int63n(int64(spread)*2+1)) - spread
	return d + delta
}

func (d *Daemon) runWakeupConnection(
	ctx context.Context,
	runtimeIDs []string,
	runWakeups chan<- runWakeup,
	runtimeSetCh <-chan struct{},
) (time.Duration, error) {
	wsURL, err := runWakeupURL(d.cfg.ServerBaseURL, runtimeIDs)
	if err != nil {
		return 0, err
	}

	headers := http.Header{}
	if token, err := d.client.bearer(ctx); err == nil && token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	if d.client.platform != "" {
		headers.Set("X-Client-Platform", d.client.platform)
	}
	if d.client.version != "" {
		headers.Set("X-Client-Version", d.client.version)
	}
	if d.client.os != "" {
		headers.Set("X-Client-OS", d.client.os)
	}
	headers.Set(daemonIDHeader, d.cfg.DaemonID)
	// Same capabilities as the HTTP path so a claim built over this WS
	// connection gets identical capability gating.
	headers.Set("X-Client-Capabilities", daemonClientCapabilities())

	// ProxyFromEnvironment so a corporate-egress host can still dial; with no
	// proxy variables set the connection stays direct.
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
	conn, handshakeResp, err := dialer.DialContext(ctx, wsURL, headers)
	if handshakeResp != nil && handshakeResp.Body != nil {
		// gorilla hands back the upgrade response so a caller can inspect a
		// refused handshake, and has already replaced its body with an
		// in-memory buffer. Closing it is a no-op that keeps the reader
		// accounted for on both the success and the failure path.
		_ = handshakeResp.Body.Close()
	}
	if err != nil {
		return 0, err
	}
	connectedAt := time.Now()
	uptime := func() time.Duration { return time.Since(connectedAt) }
	defer conn.Close()
	// HTTP heartbeats resume the moment WS detaches so the freshness window
	// from a previous connection cannot keep them silenced past disconnect.
	defer d.clearWSHeartbeatAcks()

	d.logger.Info("run wakeup websocket connected", "runtimes", len(runtimeIDs))
	signalRunWakeup(runWakeups, "")
	// The reconcile broadcaster nudges the coarse-ticker loops (run
	// cancellation watchers) to re-check immediately after a WS gap.
	if d.reconcile != nil {
		d.reconcile.broadcast()
	}
	// Claims resume with this connection and freeze whatever Connector
	// discovery the cloud holds, so re-observe at once if it is aging out
	// after a network gap or a sleep.
	if d.connectors != nil {
		d.connectors.scheduleReconnectRefresh()
	}

	// Serialize all writes through a single channel: gorilla/websocket does
	// not allow concurrent WriteMessage calls. Sized to fit a full
	// per-runtime heartbeat batch plus headroom.
	writeBufSize := 16
	if 2*len(runtimeIDs) > writeBufSize {
		writeBufSize = 2 * len(runtimeIDs)
	}
	writes := make(chan *wsOutbound, writeBufSize)
	writerDone := make(chan struct{})
	go d.runWSWriter(conn, writes, writerDone)

	// Attach the generic WS RPC sender to this connection's write channel.
	// Guarded so a Call racing teardown never sends on the closed channel.
	var sendMu sync.Mutex
	sendClosed := false
	wsRPCGeneration := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if sendClosed {
			return nil, errWSRPCUnavailable
		}
		item := &wsOutbound{data: frame}
		select {
		case writes <- item:
			return item, nil
		default:
			return nil, errWSRPCWriteBufferFull
		}
	})
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		d.runWSHeartbeatSender(heartbeatCtx, runtimeIDs, writes)
	}()

	studio := &studioControl{daemon: d}
	defer studio.close()
	stopStudioIdle := studio.startIdle(ctx)
	defer stopStudioIdle()
	sendStudio := func(frame []byte) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if sendClosed {
			return
		}
		select {
		case writes <- &wsOutbound{data: frame}:
		default:
		}
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.readStudioWakeupMessages(heartbeatCtx, conn, runWakeups, wsRPCGeneration, studio, sendStudio)
	}()

	// Teardown order matters: close the socket FIRST so a queued runs.claim
	// frame can never be flushed after attach(nil) already made the RPC fall
	// over to HTTP (which would double-claim); then detach RPC, flip the
	// send-closed flag, stop the heartbeat sender, and only then close writes.
	defer func() {
		conn.Close()
		d.wsRPC.attach(nil)
		sendMu.Lock()
		sendClosed = true
		sendMu.Unlock()
		cancelHeartbeat()
		<-hbDone
		close(writes)
		<-writerDone
	}()

	select {
	case <-ctx.Done():
		return uptime(), ctx.Err()
	case <-runtimeSetCh:
		return uptime(), errRuntimeSetChanged
	case err := <-errCh:
		return uptime(), err
	}
}

// runWSWriter funnels writes into a single goroutine — gorilla/websocket
// requires all WriteMessage calls to come from one goroutine.
func (d *Daemon) runWSWriter(conn *websocket.Conn, writes <-chan *wsOutbound, done chan<- struct{}) {
	defer close(done)
	for item := range writes {
		// Skip frames whose RPC caller already gave up; beginWrite also marks
		// the frame sent so a racing cancel() can no longer reclaim it.
		if !item.beginWrite() {
			continue
		}
		// A deadline that cannot be set means the connection is already
		// broken; the WriteMessage below reports that as a write failure and
		// tears the connection down, so there is nothing extra to act on.
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, item.data); err != nil {
			d.logger.Debug("run wakeup websocket write failed", "error", err)
			conn.Close()
			// Drain remaining frames so producers don't block while waiting
			// for runWakeupConnection to close the channel.
			//nolint:revive // empty-block: discarding every queued frame is the drain.
			for range writes {
			}
			return
		}
	}
}

// runWSHeartbeatSender emits a daemon.heartbeat per runtime every
// HeartbeatInterval; the first batch fires immediately. Frames are queued to
// the writer; on backlog the beat is dropped and HTTP picks it up next tick.
func (d *Daemon) runWSHeartbeatSender(ctx context.Context, runtimeIDs []string, writes chan<- *wsOutbound) {
	d.sendWSHeartbeats(ctx, runtimeIDs, writes)
	interval := d.cfg.HeartbeatInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.sendWSHeartbeats(ctx, runtimeIDs, writes)
		}
	}
}

func (d *Daemon) sendWSHeartbeats(ctx context.Context, runtimeIDs []string, writes chan<- *wsOutbound) {
	for _, rid := range runtimeIDs {
		if ctx.Err() != nil {
			return
		}
		frame, err := json.Marshal(protocol.Message{
			Type: protocol.EventDaemonHeartbeat,
			Payload: marshalRaw(protocol.DaemonHeartbeatRequestPayload{
				RuntimeID:           rid,
				CredentialIsolation: d.credentialIsolation(),
			}),
		})
		if err != nil {
			d.logger.Debug("ws heartbeat marshal failed", "error", err, "runtime_id", rid)
			continue
		}
		select {
		case writes <- &wsOutbound{data: frame}:
		case <-ctx.Done():
			return
		default:
			// Writer is backed up; drop this beat. HTTP heartbeat resumes on
			// its next tick once the freshness window expires.
			d.logger.Debug("ws heartbeat dropped: writer backlog", "runtime_id", rid)
		}
	}
}

func marshalRaw(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// handleWSHeartbeatAckForConnection dispatches one heartbeat_ack received
// over the WS connection that wsRPCGeneration names. A RuntimeGone ack is the
// WebSocket twin of an HTTP 404 "runtime not found": route it through the
// same self-heal entry point and do NOT record a freshness mark — pretending
// the runtime is alive would let HTTP keep skipping its own heartbeat against
// the dead UUID.
func (d *Daemon) handleWSHeartbeatAckForConnection(ack *HeartbeatResponse, wsRPCGeneration uint64) {
	if ack == nil || ack.RuntimeID == "" {
		return
	}
	if ack.RuntimeGone {
		d.startRuntimeGoneRecovery(ack.RuntimeID)
		return
	}
	for _, capability := range ack.ServerCapabilities {
		if capability == protocol.DaemonCapabilityRPCV1 {
			d.wsRPC.markRPCV1Supported(wsRPCGeneration)
			break
		}
	}
	d.recordWSHeartbeatAck(ack.RuntimeID)
	d.handleHeartbeatActions(ack.RuntimeID, ack)
}

func (d *Daemon) readStudioWakeupMessages(
	ctx context.Context,
	conn *websocket.Conn,
	runWakeups chan<- runWakeup,
	wsRPCGeneration uint64,
	studio *studioControl,
	send func([]byte),
) error {
	d.configureRunWakeupReadLiveness(conn)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if err := d.extendRunWakeupReadDeadline(conn); err != nil {
			return err
		}
		var msg protocol.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			d.logger.Debug("run wakeup websocket invalid message", "error", err)
			continue
		}
		switch msg.Type {
		case protocol.EventStudioConnectionProbe,
			protocol.EventStudioEditorDispatch,
			protocol.EventStudioBindingVerify,
			protocol.EventStudioTokenRequest:
			if studio != nil {
				studio.accept(ctx, msg.Type, msg.Payload, send)
			}
		case protocol.EventDaemonRunAvailable:
			var payload protocol.RunAvailablePayload
			if len(msg.Payload) > 0 {
				if err := json.Unmarshal(msg.Payload, &payload); err != nil {
					d.logger.Debug("run wakeup websocket invalid payload", "error", err)
					continue
				}
			}
			if payload.RuntimeID != "" {
				d.logger.Debug(
					"run wakeup received",
					"runtime_id",
					payload.RuntimeID,
					"run_id",
					payload.RunID,
				)
			}
			signalRunWakeup(runWakeups, payload.RuntimeID)
		case protocol.EventDaemonPendingWork:
			var payload protocol.PendingWorkPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				d.logger.Debug("pending work websocket invalid payload", "error", err)
				continue
			}
			if payload.RuntimeID == "" {
				continue
			}
			// Own goroutine: the hint triggers an HTTP heartbeat, and the read
			// pump must stay free for the next frame.
			go d.handlePendingWorkHint(payload.RuntimeID, payload.Kind)
		case protocol.EventDaemonCompaniesChanged:
			// Account-scoped membership hint — reconcile companies now.
			go d.syncCompaniesOnce("companies_changed hint")
		case protocol.EventDaemonHeartbeatAck:
			var ack HeartbeatResponse
			if err := json.Unmarshal(msg.Payload, &ack); err != nil {
				d.logger.Debug("ws heartbeat ack invalid payload", "error", err)
				continue
			}
			d.handleWSHeartbeatAckForConnection(&ack, wsRPCGeneration)
		case protocol.EventDaemonRPCResponse:
			var resp protocol.RPCResponsePayload
			if err := json.Unmarshal(msg.Payload, &resp); err != nil {
				d.logger.Debug("ws rpc response invalid payload", "error", err)
				continue
			}
			d.wsRPC.deliver(resp)
		}
	}
}

func (d *Daemon) configureRunWakeupReadLiveness(conn *websocket.Conn) {
	conn.SetReadLimit(runWakeupReadLimit)
	if err := d.extendRunWakeupReadDeadline(conn); err != nil {
		d.logger.Debug("run wakeup websocket read deadline failed", "error", err)
	}
	conn.SetPongHandler(func(string) error {
		return d.extendRunWakeupReadDeadline(conn)
	})
	conn.SetPingHandler(func(appData string) error {
		if err := d.extendRunWakeupReadDeadline(conn); err != nil {
			return err
		}
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(runWakeupWriteWait))
	})
}

func (d *Daemon) extendRunWakeupReadDeadline(conn *websocket.Conn) error {
	return conn.SetReadDeadline(time.Now().Add(runWakeupPongWait))
}

func signalRunWakeup(runWakeups chan<- runWakeup, runtimeID string) {
	select {
	case runWakeups <- runWakeup{runtimeID: runtimeID}:
	default:
	}
}

func runWakeupURL(baseURL string, runtimeIDs []string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("invalid daemon server URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("daemon server URL must use http, https, ws, or wss")
	}

	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/daemon/ws"
	u.RawPath = ""
	q := u.Query()
	ids := append([]string(nil), runtimeIDs...)
	sort.Strings(ids)
	if len(ids) > 0 {
		q.Set("runtime_ids", strings.Join(ids, ","))
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

func sleepWithContextOrRuntimeChange(ctx context.Context, d time.Duration, runtimeSetCh <-chan struct{}) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runtimeSetCh:
		return nil
	case <-timer.C:
		return nil
	}
}
