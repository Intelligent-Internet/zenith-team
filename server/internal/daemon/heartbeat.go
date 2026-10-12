package daemon

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Heartbeat (dual transport)
// ---------------------------------------------------------------------------

// wsHeartbeatFreshness is how long a WS heartbeat ack suppresses the HTTP
// heartbeat for a runtime: 2× HeartbeatInterval, so one dropped WS ack keeps
// HTTP suppressed but two missed acks re-enable it.
func (d *Daemon) wsHeartbeatFreshness() time.Duration {
	if d.cfg.HeartbeatInterval <= 0 {
		return 30 * time.Second
	}
	return 2 * d.cfg.HeartbeatInterval
}

func (d *Daemon) recordWSHeartbeatAck(runtimeID string) {
	if runtimeID == "" {
		return
	}
	d.wsHBMu.Lock()
	d.wsHBLastAck[runtimeID] = time.Now()
	d.wsHBMu.Unlock()
}

func (d *Daemon) wsHeartbeatRecentlyAcked(runtimeID string) bool {
	d.wsHBMu.RLock()
	last, ok := d.wsHBLastAck[runtimeID]
	d.wsHBMu.RUnlock()
	if !ok {
		return false
	}
	return time.Since(last) < d.wsHeartbeatFreshness()
}

// clearWSHeartbeatAcks drops all freshness records — called on WS disconnect
// so HTTP heartbeats resume on the next tick.
func (d *Daemon) clearWSHeartbeatAcks() {
	d.wsHBMu.Lock()
	for k := range d.wsHBLastAck {
		delete(d.wsHBLastAck, k)
	}
	d.wsHBMu.Unlock()
}

// heartbeatLoop supervises per-runtime HTTP heartbeat goroutines. Each
// runtime gets an independent ticker so one slow heartbeat cannot block the
// rest of the set.
func (d *Daemon) heartbeatLoop(ctx context.Context) {
	runtimeSetCh, unsub := d.runtimeSet.Subscribe()
	defer unsub()

	cancels := make(map[string]context.CancelFunc)
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	sync := func() {
		want := make(map[string]struct{})
		for _, rid := range d.allRuntimeIDs() {
			want[rid] = struct{}{}
		}
		for rid, cancel := range cancels {
			if _, ok := want[rid]; !ok {
				cancel()
				delete(cancels, rid)
			}
		}
		for rid := range want {
			if _, ok := cancels[rid]; ok {
				continue
			}
			rctx, rcancel := context.WithCancel(ctx)
			cancels[rid] = rcancel
			go d.runRuntimeHeartbeat(rctx, rid)
		}
	}

	sync()
	for {
		select {
		case <-ctx.Done():
			return
		case <-runtimeSetCh:
			sync()
		}
	}
}

// runRuntimeHeartbeat owns the HTTP heartbeat schedule for a single runtime.
// The first tick fires after a jittered delay (up to one full interval) so a
// batch of registrations does not thundering-herd the server.
func (d *Daemon) runRuntimeHeartbeat(ctx context.Context, rid string) {
	interval := d.cfg.HeartbeatInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if jitter := time.Duration(rand.Int63n(int64(interval))); jitter > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter):
		}
	}

	consecutiveTransientFailures := 0
	tick := func() {
		if d.runHeartbeatTick(ctx, rid) {
			consecutiveTransientFailures++
			if consecutiveTransientFailures == 2 {
				d.client.CloseIdleConnections()
			}
			return
		}
		consecutiveTransientFailures = 0
	}

	tick()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// runHeartbeatTick returns true on a transient failure that should count
// toward stale idle-connection cleanup. The HTTP tick is skipped entirely
// while a WS heartbeat ack is fresh — the WS path already keeps last_seen_at
// current, and HTTP resumes automatically when the freshness window expires.
func (d *Daemon) runHeartbeatTick(ctx context.Context, rid string) bool {
	if d.wsHeartbeatRecentlyAcked(rid) {
		d.logger.Debug("heartbeat: skipping HTTP tick, WS recently acked", "runtime_id", rid)
		return false
	}
	resp, err := d.client.SendHeartbeat(ctx, rid, d.credentialIsolation())
	if err != nil {
		if ctx.Err() == nil {
			if isRuntimeNotFoundError(err) {
				// Server says this runtime is gone — recover instead of
				// looping on the dead UUID.
				d.startRuntimeGoneRecovery(rid)
				return false
			}
			d.logger.Warn("heartbeat failed", "runtime_id", rid, "error", err)
		}
		return ctx.Err() == nil && isTransientError(err)
	}
	if resp != nil && resp.RuntimeGone {
		d.startRuntimeGoneRecovery(rid)
		return false
	}
	d.handleHeartbeatActions(rid, resp)
	return false
}

func (d *Daemon) handleHeartbeatActions(runtimeID string, response *HeartbeatResponse) {
	if response == nil {
		return
	}
	if d.plugins != nil {
		d.plugins.setPresentation(
			heartbeatHasServerCapability(response, protocol.DaemonCapabilityPluginPresentationV1),
		)
	}
	if response.PendingModelList == nil ||
		!heartbeatHasServerCapability(response, protocol.DaemonCapabilityModelDiscoveryV1) {
		return
	}
	runtime := d.findRuntime(runtimeID)
	if runtime == nil {
		return
	}
	go d.handleRuntimeModelDiscovery(*runtime, response.PendingModelList.RequestID)
}

func heartbeatHasServerCapability(response *HeartbeatResponse, wanted string) bool {
	for _, capability := range response.ServerCapabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func (d *Daemon) handleRuntimeModelDiscovery(runtime Runtime, requestID string) {
	if requestID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(d.recoveryContext(), modelDiscoveryWorkTimeout)
	defer cancel()

	entry, ok := d.agents()[runtime.Provider]
	if !ok || entry.Path == "" {
		d.reportRuntimeModelDiscovery(ctx, runtime.ID, requestID, ModelDiscoveryResult{
			Status: "failed",
			Models: []agent.RuntimeModel{},
			Error: &ModelDiscoveryError{
				Code: "discovery_failed", Message: "Provider executable is unavailable",
			},
		})
		return
	}

	discoveryCtx, discoveryCancel := context.WithTimeout(ctx, modelDiscoveryCommandTimeout)
	models, err := agent.DiscoverRuntimeModels(discoveryCtx, runtime.Provider, entry.Path)
	discoveryCancel()
	if err != nil {
		message := "Runtime model discovery failed"
		if errors.Is(err, context.DeadlineExceeded) {
			message = "Runtime model discovery timed out"
		}
		d.logger.Warn(
			"runtime model discovery failed",
			"runtime_id", runtime.ID,
			"request_id", requestID,
			"provider", runtime.Provider,
			"error", err,
		)
		d.reportRuntimeModelDiscovery(ctx, runtime.ID, requestID, ModelDiscoveryResult{
			Status: "failed",
			Models: []agent.RuntimeModel{},
			Error:  &ModelDiscoveryError{Code: "discovery_failed", Message: message},
		})
		return
	}
	d.reportRuntimeModelDiscovery(ctx, runtime.ID, requestID, ModelDiscoveryResult{
		Status: "completed", Models: models,
	})
}

func (d *Daemon) reportRuntimeModelDiscovery(
	ctx context.Context,
	runtimeID string,
	requestID string,
	result ModelDiscoveryResult,
) {
	if err := d.client.ReportRuntimeModelDiscovery(ctx, runtimeID, requestID, result); err != nil {
		d.logger.Error(
			"runtime model discovery report failed",
			"runtime_id", runtimeID,
			"request_id", requestID,
			"error", err,
		)
	}
}

// handlePendingWorkHint reacts to a server-pushed daemon.pending_work frame
// by sending ONE immediate HTTP heartbeat for the runtime. The hint carries
// no work, so it is safe to lose, duplicate, or ignore; the coalescing and
// per-runtime rate limit stop a caller loop from amplifying heartbeats. It
// deliberately bypasses the WS-freshness suppression — that exists to avoid
// duplicate periodic writes, not to block an explicitly requested pull.
func (d *Daemon) handlePendingWorkHint(runtimeID, kind string) {
	if runtimeID == "" {
		return
	}
	if d.findRuntime(runtimeID) == nil {
		return
	}
	if kind == protocol.PendingWorkKindRuntimeRefresh {
		// A person asked for this machine's setup to be checked again; the
		// answer comes back through registration, not a heartbeat, and the
		// refresh keeps its own floor instead of the heartbeat bookkeeping.
		d.refreshRuntimeSetupOnce("runtime_refresh hint")
		return
	}

	d.pendingWorkMu.Lock()
	for id, at := range d.pendingWorkLastRun {
		if time.Since(at) > pendingWorkHintBookkeepingTTL {
			delete(d.pendingWorkLastRun, id)
		}
	}
	if _, inflight := d.pendingWorkInflight[runtimeID]; inflight {
		d.pendingWorkMu.Unlock()
		return
	}
	if last, ok := d.pendingWorkLastRun[runtimeID]; ok && time.Since(last) < pendingWorkHintMinInterval {
		d.pendingWorkMu.Unlock()
		d.logger.Debug("pending work hint throttled", "runtime_id", runtimeID, "kind", kind)
		return
	}
	d.pendingWorkInflight[runtimeID] = struct{}{}
	d.pendingWorkLastRun[runtimeID] = time.Now()
	d.pendingWorkMu.Unlock()
	defer func() {
		d.pendingWorkMu.Lock()
		delete(d.pendingWorkInflight, runtimeID)
		d.pendingWorkMu.Unlock()
	}()

	ctx := d.recoveryContext()
	if ctx.Err() != nil {
		return
	}
	hbCtx, cancel := context.WithTimeout(ctx, pendingWorkHeartbeatTimeout)
	resp, err := d.client.SendHeartbeat(hbCtx, runtimeID, d.credentialIsolation())
	cancel()
	if err != nil {
		if isRuntimeNotFoundError(err) {
			d.startRuntimeGoneRecovery(runtimeID)
			return
		}
		d.logger.Debug(
			"pending work hint heartbeat failed",
			"runtime_id",
			runtimeID,
			"kind",
			kind,
			"error",
			err,
		)
		return
	}
	if resp != nil && resp.RuntimeGone {
		d.startRuntimeGoneRecovery(runtimeID)
		return
	}
	d.handleHeartbeatActions(runtimeID, resp)
}
