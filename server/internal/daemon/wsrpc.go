package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/pkg/protocol"
)

// errWSRPCUnavailable is returned by wsRPCClient.Call when there is no live
// WS connection to carry the request. Recovery is method-specific: claims can
// fall back to HTTP, while transcript appends require this acknowledged lane.
var errWSRPCUnavailable = errors.New("ws rpc: no active connection")

// errWSRPCUncertain is returned when a request's frame WAS sent but the
// connection dropped before a definitive response. The outcome is unknown
// (the server may have committed), so the caller must NOT fall back to
// another transport for the same work — that risks a double claim.
var errWSRPCUncertain = errors.New("ws rpc: sent but outcome unknown (connection lost)")

// wsRPCResponseGrace is how much longer the daemon waits for an RPC response
// beyond the server-side execution budget it requested, so a claim that
// committed just before the server deadline still reports back.
const wsRPCResponseGrace = 2 * time.Second

var wsClaimUncertainFallbackDelay = batchClaimRequestTimeout + wsRPCResponseGrace

// errWSRPCWriteBufferFull is returned when the connection's write buffer is
// saturated. The caller applies its method-specific retry or fallback policy
// rather than blocking the socket.
var errWSRPCWriteBufferFull = errors.New("ws rpc: write buffer full")

// wsOutbound is a frame queued for the WS writer. It is cancelable so an RPC
// caller that gives up before the frame has hit the socket can prevent it
// from being delivered later — otherwise a backpressured writer could deliver
// a stale runs.claim after the daemon already HTTP-fell-back, double-claiming.
type wsOutbound struct {
	data     []byte
	mu       sync.Mutex
	sent     bool
	canceled bool
}

// beginWrite is called by the writer immediately before WriteMessage. False
// means the frame was cancelled (skip it); otherwise it marks the frame sent
// so a concurrent cancel() can no longer un-send it.
func (o *wsOutbound) beginWrite() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.canceled {
		return false
	}
	o.sent = true
	return true
}

// cancel is called by an RPC caller giving up. True: the frame was still
// pending and is now guaranteed NOT delivered. False: the writer already
// began sending it.
func (o *wsOutbound) cancel() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sent {
		return false
	}
	o.canceled = true
	return true
}

// wsRPCClient is the daemon-side half of the generic WS request/response
// transport. It correlates responses to requests by request_id over the
// multiplexed control connection; when no connection is attached, Call fails
// fast with errWSRPCUnavailable and leaves recovery to the calling method.
type wsRPCClient struct {
	mu        sync.Mutex
	pending   map[string]chan protocol.RPCResponsePayload
	sendFrame func([]byte) (*wsOutbound, error)
	// rpcV1Supported belongs to the currently attached connection; attach
	// clears it so a claim racing a reconnect can never carry negotiation
	// state across connections.
	rpcV1Supported bool
	generation     uint64
	grace          time.Duration
}

func newWSRPCClient(grace time.Duration) *wsRPCClient {
	return &wsRPCClient{
		pending: make(map[string]chan protocol.RPCResponsePayload),
		grace:   grace,
	}
}

// attach binds a live connection's frame writer and clears the previous
// connection's negotiated capability. Passing nil detaches; pending requests
// are failed so their callers can apply method-specific recovery immediately.
func (c *wsRPCClient) attach(sendFrame func([]byte) (*wsOutbound, error)) uint64 {
	c.mu.Lock()
	c.generation++
	c.rpcV1Supported = false
	c.sendFrame = sendFrame
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	generation := c.generation
	c.mu.Unlock()
	return generation
}

// markRPCV1Supported records explicit server support for the currently
// attached connection. Acks received without a live sender cannot enable a
// future connection.
func (c *wsRPCClient) markRPCV1Supported(generation uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.sendFrame != nil && c.generation == generation {
		c.rpcV1Supported = true
	}
	c.mu.Unlock()
}

// supportsRPCV1 reports whether the live connection explicitly negotiated
// rpc-v1. Only a fast-path hint — call repeats the check under the mutex.
func (c *wsRPCClient) supportsRPCV1() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sendFrame != nil && c.rpcV1Supported
}

// Call issues an RPC on any attached connection.
func (c *wsRPCClient) Call(
	ctx context.Context,
	method string,
	serverTimeout time.Duration,
	reqBody, respBody any,
) (int, error) {
	return c.call(ctx, method, serverTimeout, reqBody, respBody, false)
}

// CallIfRPCV1Supported issues an RPC only when the currently attached
// connection explicitly negotiated rpc-v1. The capability check and sender
// capture happen under the same mutex, so a reconnect cannot redirect a call
// authorized by the previous connection onto its replacement.
func (c *wsRPCClient) CallIfRPCV1Supported(
	ctx context.Context,
	method string,
	serverTimeout time.Duration,
	reqBody, respBody any,
) (int, error) {
	return c.call(ctx, method, serverTimeout, reqBody, respBody, true)
}

func (c *wsRPCClient) call(
	ctx context.Context,
	method string,
	serverTimeout time.Duration,
	reqBody, respBody any,
	requireRPCV1 bool,
) (int, error) {
	if c == nil {
		return 0, errWSRPCUnavailable
	}
	var rawReq json.RawMessage
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return 0, fmt.Errorf("ws rpc: marshal request: %w", err)
		}
		rawReq = b
	}
	id := uuid.NewString()
	frame, err := json.Marshal(protocol.Message{
		Type: protocol.EventDaemonRPCRequest,
		Payload: marshalRaw(protocol.RPCRequestPayload{
			RequestID: id,
			Method:    method,
			Body:      rawReq,
			TimeoutMs: serverTimeout.Milliseconds(),
		}),
	})
	if err != nil {
		return 0, fmt.Errorf("ws rpc: marshal frame: %w", err)
	}

	ch := make(chan protocol.RPCResponsePayload, 1)
	c.mu.Lock()
	if c.sendFrame == nil || (requireRPCV1 && !c.rpcV1Supported) {
		c.mu.Unlock()
		return 0, errWSRPCUnavailable
	}
	send := c.sendFrame
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	item, err := send(frame)
	if err != nil {
		return 0, fmt.Errorf("ws rpc: send: %w", err)
	}

	// giveUp resolves an abandoned request: a still-queued frame is cancelled
	// (definitively not sent, safe HTTP fallback); a frame the writer already
	// began sending may reach the server, so the outcome is uncertain.
	giveUp := func() error {
		if item.cancel() {
			return errWSRPCUnavailable
		}
		return errWSRPCUncertain
	}

	timeout := serverTimeout + c.grace
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp, ok := <-ch:
		if !ok {
			// Connection detached mid-request; giveUp decides safety.
			return 0, giveUp()
		}
		if resp.Status >= 200 && resp.Status < 300 {
			if respBody != nil && len(resp.Body) > 0 {
				if err := json.Unmarshal(resp.Body, respBody); err != nil {
					return resp.Status, fmt.Errorf("ws rpc: decode response: %w", err)
				}
			}
			return resp.Status, nil
		}
		msg := resp.Error
		if msg == "" {
			msg = fmt.Sprintf("ws rpc status %d", resp.Status)
		}
		return resp.Status, errors.New(msg)
	case <-timer.C:
		if err := giveUp(); errors.Is(err, errWSRPCUncertain) {
			return 0, err
		}
		return 0, fmt.Errorf("ws rpc: timeout after %s: %w", timeout, errWSRPCUnavailable)
	case <-ctx.Done():
		if item.cancel() {
			return 0, ctx.Err()
		}
		// The context error stays text: "sent but outcome unknown" must not
		// collapse into context.Canceled, which this daemon reads elsewhere
		// as a clean stop with nothing in flight.
		return 0, fmt.Errorf("%w: %v", errWSRPCUncertain, ctx.Err()) //nolint:errorlint // see above
	}
}

// deliver routes an inbound rpc_response frame to the waiting Call. The send
// happens under the mutex so it is serialized with attach(nil)'s
// close+delete — this never sends on a closed channel. Unknown request ids
// are dropped.
func (c *wsRPCClient) deliver(resp protocol.RPCResponsePayload) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.pending[resp.RequestID]
	if !ok {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

// ClaimRunsWSFirst is the WS-first claim ladder: runs.claim over the WS
// control connection when one negotiated rpc-v1, an uncertain-outcome safety
// window that skips the cycle rather than double-claim, then the HTTP batch
// claim.
func (d *Daemon) ClaimRunsWSFirst(
	ctx context.Context,
	daemonID string,
	runtimeIDs []string,
	maxRuns int,
) ([]*Run, error) {
	bypassWSOnce := false
	if retryAfterNanos := d.wsClaimHTTPFallbackAfter.Load(); retryAfterNanos > 0 {
		retryAfter := time.Unix(0, retryAfterNanos)
		now := time.Now()
		if now.Before(retryAfter) {
			d.logger.Debug("ws claim outcome uncertain; delaying http fallback until safety window elapses",
				"retry_after", retryAfter.Sub(now).Round(time.Millisecond))
			return nil, nil
		}
		if d.wsClaimHTTPFallbackAfter.CompareAndSwap(retryAfterNanos, 0) {
			bypassWSOnce = true
			d.logger.Debug("previous ws claim outcome uncertain; using http fallback for this claim cycle")
		}
	}
	if !bypassWSOnce && d.wsRPC.supportsRPCV1() {
		var resp struct {
			Items []*Run `json:"items"`
		}
		status, err := d.wsRPC.CallIfRPCV1Supported(ctx, "runs.claim", batchClaimRequestTimeout, map[string]any{
			"daemonId":   daemonID,
			"runtimeIds": runtimeIDs,
			"maxRuns":    maxRuns,
		}, &resp)
		if err == nil {
			return resp.Items, nil
		}
		if errors.Is(err, errWSRPCUncertain) || (status >= 200 && status < 300) {
			// The WS claim may have committed server-side; claiming the same
			// free slots again over HTTP immediately would double-claim. Skip
			// this cycle, then force one HTTP batch claim after the safety
			// window elapses.
			delay := wsClaimUncertainFallbackDelay
			if delay < 0 {
				delay = 0
			}
			d.wsClaimHTTPFallbackAfter.Store(time.Now().Add(delay).UnixNano())
			d.logger.Debug(
				"ws claim outcome uncertain after disconnect; delaying http fallback",
				"retry_after",
				delay,
			)
			return nil, nil
		}
		d.logger.Debug("ws claim failed; falling back to http", "error", err)
	}
	return d.client.ClaimRuns(ctx, daemonID, runtimeIDs, maxRuns)
}
