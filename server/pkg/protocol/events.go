// Package protocol holds the wire vocabulary shared by every Boardly
// transport and binary: the daemon control-frame names, the message envelope
// and daemon frame payloads, the activity entity/actor vocabulary, and the
// wire-side value contracts (phone, text, timestamps, workspace names).
// It declares no DTOs and no general-purpose helpers — parsing and pgtype
// boundary utilities live in internal/util.
package protocol

// The daemon WS control-plane frames. Each rides the protocol.Message
// envelope between server and daemon, never the browser envelope; they name
// requests and hints, not committed facts, which is why they are not events
// on the bus.
//
// The browser-plane event vocabulary lives with its producers: each domain
// declares its topic and event-name constants in internal/<domain>/events.go,
// and cmd/server/realtime_routing.go is the one list of what reaches a
// browser.
//
// heartbeat / heartbeat_ack carry the runtime heartbeat and its receipt;
// run_available, pending_work and companies_changed are best-effort wakeup
// hints; rpc_request / rpc_response multiplex request/response RPCs over the
// socket.
const (
	EventDaemonHeartbeat        = "daemon.heartbeat"
	EventDaemonHeartbeatAck     = "daemon.heartbeat_ack"
	EventDaemonRunAvailable     = "daemon.run_available"
	EventDaemonPendingWork      = "daemon.pending_work"
	EventDaemonRPCRequest       = "daemon.rpc_request"
	EventDaemonRPCResponse      = "daemon.rpc_response"
	EventDaemonCompaniesChanged = "daemon.companies_changed"
)
