package daemonws

import (
	"log/slog"

	"github.com/oklog/ulid/v2"

	"github.com/Intelligent-Internet/zenith-team/server/internal/realtime"
)

// RelayNotifier sends daemon wakeup hints to the local daemon hub and, when
// Redis is configured, publishes the same hint through the shared realtime
// relay so every API node can attempt local delivery.
type RelayNotifier struct {
	local *Hub
	relay realtime.RelayPublisher
}

func NewRelayNotifier(local *Hub, relay realtime.RelayPublisher) *RelayNotifier {
	return &RelayNotifier{local: local, relay: relay}
}

func (n *RelayNotifier) NotifyRunAvailable(runtimeID, runID string) {
	if runtimeID == "" {
		return
	}
	eventID := ulid.Make().String()
	if n.local != nil {
		n.local.notifyRunAvailable(runtimeID, runID, eventID)
	}
	if n.relay == nil {
		return
	}
	frame, err := runAvailableFrame(runtimeID, runID)
	if err != nil {
		M.WakeupPublishErrors.Add(1)
		return
	}
	shardKey := runID
	if shardKey == "" {
		shardKey = eventID
	}
	if err := n.relay.PublishWithID(realtime.ScopeDaemonRuntime, shardKey, "", frame, eventID); err != nil {
		M.WakeupPublishErrors.Add(1)
		slog.Warn(
			"daemon websocket wakeup publish failed",
			"error",
			err,
			"runtime_id",
			runtimeID,
			"run_id",
			runID,
		)
		return
	}
	M.WakeupPublishedTotal.Add(1)
}

func (n *RelayNotifier) NotifyCompaniesChanged(userID string) {
	if userID == "" {
		return
	}
	eventID := ulid.Make().String()
	if n.local != nil {
		n.local.notifyCompaniesChanged(userID, eventID)
	}
	if n.relay == nil {
		return
	}
	frame, err := companiesChangedFrame()
	if err != nil {
		M.WakeupPublishErrors.Add(1)
		return
	}
	// ScopeDaemonRuntime is the relay's daemon-only transport scope; the frame
	// type tells Hub.DeliverDaemonRuntime whether scopeID is a runtime or user
	// key. Keeping one transport scope preserves compatibility with existing
	// relay consumers while the hub enforces user-scoped delivery.
	if err := n.relay.PublishWithID(realtime.ScopeDaemonRuntime, userID, "", frame, eventID); err != nil {
		M.WakeupPublishErrors.Add(1)
		slog.Warn("daemon websocket company refresh publish failed", "error", err, "user_id", userID)
		return
	}
	M.WakeupPublishedTotal.Add(1)
}

// NotifyPendingWork fans a runtime-scoped "heartbeat now" hint out to the local
// hub and, when Redis is configured, through the relay so the API node that
// actually holds the daemon's WebSocket delivers it. Shard key is the runtime
// ID: hints for one runtime stay ordered relative to each other, and a dropped
// hint only costs the daemon its normal heartbeat delay.
func (n *RelayNotifier) NotifyPendingWork(runtimeID, kind string) {
	if runtimeID == "" {
		return
	}
	eventID := ulid.Make().String()
	if n.local != nil {
		n.local.notifyPendingWork(runtimeID, kind, eventID)
	}
	if n.relay == nil {
		return
	}
	frame, err := pendingWorkFrame(runtimeID, kind)
	if err != nil {
		M.WakeupPublishErrors.Add(1)
		return
	}
	if err := n.relay.PublishWithID(realtime.ScopeDaemonRuntime, runtimeID, "", frame, eventID); err != nil {
		M.WakeupPublishErrors.Add(1)
		slog.Warn(
			"daemon websocket pending work publish failed",
			"error",
			err,
			"runtime_id",
			runtimeID,
			"kind",
			kind,
		)
		return
	}
	M.WakeupPublishedTotal.Add(1)
}
