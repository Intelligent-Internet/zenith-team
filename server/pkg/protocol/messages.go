package protocol

import (
	"encoding/json"

	"github.com/google/uuid"
)

// DaemonConfig contains server-owned endpoint configuration, never user credentials.
type DaemonConfig struct {
	StudioAPIOrigin string `json:"studioApiOrigin"`
}

// Daemon/server capability tokens exchanged during registration and
// heartbeats. Only capabilities with a live consumer exist; new flows add
// their token together with their code.
const (
	// DaemonCapabilityRPCV1 advertises that the daemon can carry
	// request/response RPCs over the WebSocket control connection. Gated so
	// only daemon+server pairs that both support it route claim over WS;
	// everyone else keeps using the HTTP claim endpoint.
	DaemonCapabilityRPCV1 = "rpc-v1"
	// DaemonCapabilityModelDiscoveryV1 advertises heartbeat-carried, exact-
	// runtime asynchronous model discovery.
	DaemonCapabilityModelDiscoveryV1 = "model-discovery-v1"
	// DaemonCapabilityPluginPresentationV1 advertises, in the server's
	// heartbeat ack, that Plugin discovery accepts the Codex presentation
	// fields. A daemon sends them only after an ack that carried it.
	DaemonCapabilityPluginPresentationV1 = "plugin-presentation-v1"
)

// Daemon RPC methods. A method exists here only when both daemon and server
// have a live caller/handler pair.
const (
	DaemonRPCMethodRunsClaim           = "runs.claim"
	DaemonRPCMethodRunTranscriptAppend = "runs.transcript.append"
)

// Message is the envelope for daemon-plane WebSocket frames. The browser
// plane wraps events in its own richer envelope (eventId, type, createdAt,
// actor) — see internal/realtime.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// RPCRequestPayload is the generic daemon→server request envelope carried in
// a Message of type EventDaemonRPCRequest. RequestID correlates the response;
// Method selects the server-side handler (e.g. "runs.claim"); Body is the
// method-specific request JSON.
type RPCRequestPayload struct {
	RequestID string          `json:"requestId"`
	Method    string          `json:"method"`
	Body      json.RawMessage `json:"body,omitempty"`
	// TimeoutMs is the server-side execution budget in milliseconds. The
	// server bounds the handler's context by it so a slow RPC is cancelled
	// (its work rolled back) rather than committing after the daemon has
	// already timed out waiting and fallen back to HTTP. 0 means no
	// server-side bound (connection-lifetime only).
	TimeoutMs int64 `json:"timeoutMs,omitempty"`
}

// RPCResponsePayload is the server→daemon reply, carried in a Message of type
// EventDaemonRPCResponse. RequestID echoes the request. Status mirrors an
// HTTP status so the daemon treats WS and HTTP outcomes uniformly. Exactly
// one of Body / Error is meaningful: Body on success (2xx), Error on failure.
type RPCResponsePayload struct {
	RequestID string          `json:"requestId"`
	Status    int             `json:"status"`
	Body      json.RawMessage `json:"body,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// RunAvailablePayload is sent from server to daemon as a wakeup hint. The
// daemon still claims work through the claim ladder (WS RPC, then HTTP).
//
// RunID is an optional UUID string used for the wake-up log. Claim remains
// authoritative about which Run the daemon can execute.
type RunAvailablePayload struct {
	RuntimeID string `json:"runtimeId"`
	RunID     string `json:"runId,omitempty"`
}

// CompaniesChangedPayload is an account-scoped hint that asks a daemon to
// reconcile its company membership set. The server remains authoritative; no
// company data is embedded in the event.
type CompaniesChangedPayload struct{}

// PendingWorkPayload is sent from server to daemon as a wakeup hint when a
// heartbeat-carried request is enqueued for a runtime. The daemon responds by
// sending one immediate heartbeat for RuntimeID instead of waiting for its
// next scheduled tick; the request itself is still claimed through the normal
// heartbeat path, so this event carries no work and is safe to lose,
// duplicate, or ignore. Kind is advisory, with one exception: the
// PendingWorkKindRuntimeRefresh kind asks the daemon to probe its provider
// setup again and register the result instead of heartbeating.
type PendingWorkPayload struct {
	RuntimeID string `json:"runtimeId"`
	Kind      string `json:"kind,omitempty"`
}

// PendingWorkKindRuntimeRefresh is the pending-work kind a person's
// "Refresh setup" sends: the daemon re-probes every provider CLI on the
// machine (path, version, sign-in) and registers what it found. The server
// stays authoritative and learns the outcome through the ordinary register
// upsert, so a lost or duplicated hint costs only another click.
const PendingWorkKindRuntimeRefresh = "runtime_refresh"

// RunCancelReasonTaskReassigned is the reason a confirmed handoff cancels a
// Task's live Runs with (ADR 0014). The server writes it onto the Run and the
// daemon's run-status read returns it, which is how the daemon tells a
// handoff from every other cancel and supersedes the Run's worktree instead
// of completing it.
const RunCancelReasonTaskReassigned = "task_reassigned"

// RunItemRole is the author role of one immutable provider-normalized Run
// transcript item. Provider-native role spellings never cross this wire.
type RunItemRole string

const (
	RunItemRoleAgent  RunItemRole = "agent"
	RunItemRoleTool   RunItemRole = "tool"
	RunItemRoleSystem RunItemRole = "system"
)

// RunItemPartType is the closed Zenith Team vocabulary for durable Run output.
// Adapters discard their vendor envelopes after producing these safe parts.
type RunItemPartType string

const (
	RunItemPartTypeText       RunItemPartType = "text"
	RunItemPartTypeReasoning  RunItemPartType = "reasoning"
	RunItemPartTypeToolCall   RunItemPartType = "toolCall"
	RunItemPartTypeToolResult RunItemPartType = "toolResult"
	RunItemPartTypeError      RunItemPartType = "error"
)

// RunTranscriptMaxItemBytes is the canonical JSON size ceiling for one
// completed provider item. The daemon enforces it before opening an RPC frame
// and the execution service enforces it again before the database write.
const RunTranscriptMaxItemBytes = 2 << 20

// RunItemPart is one typed fragment of an immutable Run transcript item.
// Type selects the meaningful fields; the server validates that combination
// before persistence.
type RunItemPart struct {
	Final      bool                `json:"final,omitempty"`
	Type       RunItemPartType     `json:"type"`
	Text       string              `json:"text,omitempty"`
	CallID     string              `json:"callId,omitempty"`
	Name       string              `json:"name,omitempty"`
	Input      json.RawMessage     `json:"input,omitempty"`
	Output     string              `json:"output,omitempty"`
	Status     RunToolResultStatus `json:"status,omitempty"`
	ExitCode   *int64              `json:"exitCode,omitempty"`
	DurationMs *int64              `json:"durationMs,omitempty"`
}

// RunTranscriptPart is the sealed browser-safe output union. The flat
// RunItemPart above exists only at the daemon input and canonicalization
// boundary; readers never receive a bag of shape-dependent optional fields.
type RunTranscriptPart interface {
	runTranscriptPart()
}

type RunTextPart struct {
	Final bool            `json:"final,omitempty"`
	Type  RunTextPartType `json:"type"`
	Text  string          `json:"text"`
}

type RunTextPartType string

const RunTextPartTypeText RunTextPartType = "text"

func (RunTextPart) runTranscriptPart() {}

type RunReasoningPart struct {
	Type RunReasoningPartType `json:"type"`
	Text string               `json:"text"`
}

type RunReasoningPartType string

const RunReasoningPartTypeReasoning RunReasoningPartType = "reasoning"

func (RunReasoningPart) runTranscriptPart() {}

type RunToolCallPart struct {
	Type   RunToolCallPartType `json:"type"`
	CallID string              `json:"callId"`
	Name   string              `json:"name"`
	Input  json.RawMessage     `json:"input,omitempty"`
}

type RunToolCallPartType string

const RunToolCallPartTypeToolCall RunToolCallPartType = "toolCall"

func (RunToolCallPart) runTranscriptPart() {}

type RunToolResultPart struct {
	Type       RunToolResultPartType `json:"type"`
	CallID     string                `json:"callId"`
	Name       string                `json:"name,omitempty"`
	Output     string                `json:"output,omitempty"`
	Status     RunToolResultStatus   `json:"status,omitempty"`
	ExitCode   *int64                `json:"exitCode,omitempty"`
	DurationMs *int64                `json:"durationMs,omitempty"`
}

type RunToolResultPartType string

const RunToolResultPartTypeToolResult RunToolResultPartType = "toolResult"

type RunToolResultStatus string

const (
	RunToolResultStatusInProgress RunToolResultStatus = "in_progress"
	RunToolResultStatusCompleted  RunToolResultStatus = "completed"
	RunToolResultStatusFailed     RunToolResultStatus = "failed"
	RunToolResultStatusDeclined   RunToolResultStatus = "declined"
	RunToolResultStatusCancelled  RunToolResultStatus = "cancelled"
)

func (RunToolResultPart) runTranscriptPart() {}

type RunErrorPart struct {
	Type RunErrorPartType `json:"type"`
	Text string           `json:"text"`
}

type RunErrorPartType string

const RunErrorPartTypeError RunErrorPartType = "error"

func (RunErrorPart) runTranscriptPart() {}

// RunTranscriptAppendRequest carries exactly one completed provider item
// from a daemon to the server. RequestID on the surrounding RPC frame is only
// transport correlation; ItemKey is the item's natural replay identity.
//
// RunID is the UUID string generated when PostgreSQL inserts the Run.
type RunTranscriptAppendRequest struct {
	RunID   uuid.UUID     `json:"runId"`
	ItemKey string        `json:"itemKey"`
	Role    RunItemRole   `json:"role"`
	Parts   []RunItemPart `json:"parts"`
}

// RunTranscriptAppendReceipt is returned only after PostgreSQL commits the
// item. ItemDigest is lowercase SHA-256 hex over the canonical Zenith Team item.
//
// Seq and ThroughSeq are int64 because seq is now the conversation-wide
// counter, not a per-run ordinal: one busy run pushes a conversation's seq far
// past what a per-run count would reach, and both are bounded by
// MaxJSONSafeInteger at allocation.
type RunTranscriptAppendReceipt struct {
	ID          string    `json:"id"`
	RunID       uuid.UUID `json:"runId"`
	ItemKey     string    `json:"itemKey"`
	Seq         int64     `json:"seq"`
	ThroughSeq  int64     `json:"throughSeq"`
	ItemDigest  string    `json:"itemDigest"`
	CommittedAt string    `json:"committedAt"`
}

// RunTerminalHeadMismatch is the body of the 409 a terminal transition
// (complete / fail) returns when the daemon's proof does not name the run's
// committed transcript prefix. It carries the server's ACTUAL head so the
// daemon can reconcile.
//
// Without it the daemon has nothing to reconcile against: it re-sends the same
// stale proof, is refused again, and the run stays non-terminal with a durable
// answer stranded in the conversation (bug R1). On receiving this the daemon
// must adopt ThroughSeq as its head — re-appending any item the server has not
// acknowledged first, which is idempotent through the item_key replay fence —
// and re-send the terminal transition with the new proof. It must never invent
// a head it has not been told.
type RunTerminalHeadMismatch struct {
	RunID uuid.UUID `json:"runId"`
	// ThroughSeq is the highest conversation seq committed to this run's
	// transcript — the exact value a fresh proof must carry to be accepted.
	ThroughSeq int64 `json:"throughSeq"`
}

// RunMessagePayload is one committed provider-normalized transcript item,
// broadcast as EventRunMessageCreated after its transaction commits. It carries only
// the routing identities a client cannot join from its caches — runId and
// conversationId; the run itself, already cached from lifecycle events,
// carries the task and agent.
type RunMessagePayload struct {
	ID             string              `json:"id"`
	RunID          uuid.UUID           `json:"runId"`
	ConversationID string              `json:"conversationId"`
	ItemKey        string              `json:"itemKey"`
	Seq            int64               `json:"seq"`
	Role           RunItemRole         `json:"role"`
	Parts          []RunTranscriptPart `json:"parts"`
	ItemDigest     string              `json:"itemDigest"`
	ThroughSeq     int64               `json:"throughSeq"`
	CreatedAt      string              `json:"createdAt"`
}

// Daemon roster actions carried by DaemonRegisterPayload.Action. Update is
// a user-plane rename or visibility change; tools have their own narrow signal.
const (
	DaemonRosterActionRegister   = "register"
	DaemonRosterActionDeregister = "deregister"
	DaemonRosterActionStaleSweep = "stale_sweep"
	DaemonRosterActionRuntimeGC  = "runtime_gc"
	DaemonRosterActionUpdate     = "update"
	// DaemonRosterActionToolsChanged only changes the runtime tool projection,
	// not Agent bindings, permissions or Runs.
	DaemonRosterActionToolsChanged = "tools_changed"
	// DaemonRosterActionRemove is the owner's soft removal of one runtime:
	// the row left the roster (and the agents it bound may have moved), so
	// clients refetch runtimes and agents. DaemonID and RuntimeIDs name the
	// machine and the removed row.
	DaemonRosterActionRemove = "remove"
)

// DaemonRegisterPayload is the single roster-changed broadcast
// (EventDaemonRosterChanged): clients refetch the runtime roster on any action.
type DaemonRegisterPayload struct {
	Action     string   `json:"action"`
	CompanyID  string   `json:"companyId,omitempty"`
	DaemonID   string   `json:"daemonId,omitempty"`
	RuntimeIDs []string `json:"runtimeIds,omitempty"`
}

// DaemonHeartbeatRequestPayload is sent from daemon to server over WebSocket
// to update last_seen_at and pull pending actions for a single runtime.
// Mirrors the body of POST /api/daemon/heartbeat so both transports share
// identical semantics.
type DaemonHeartbeatRequestPayload struct {
	RuntimeID string `json:"runtimeId"`
	// CredentialIsolation is the runtime's report, CredentialIsolationEnforced
	// or CredentialIsolationNotEnforced; empty from a daemon that predates it.
	CredentialIsolation string `json:"credentialIsolation,omitempty"`
}

// HeartbeatStatusRuntimeGone is the ack Status used when the runtime row no
// longer exists server-side. Companion to DaemonHeartbeatAckPayload.RuntimeGone.
const HeartbeatStatusRuntimeGone = "runtime_gone"

// DaemonHeartbeatAckPayload is the server's reply to
// DaemonHeartbeatRequestPayload. JSON shape mirrors the HTTP heartbeat
// response so daemon code can decode either. ServerCapabilities is explicit
// server-to-daemon protocol negotiation — a daemon must not infer support
// from its own advertised capabilities.
//
// RuntimeGone is the WebSocket replacement for the HTTP 404 "runtime not
// found" response: when the server discovers the runtime row was deleted
// (UI delete, offline GC) it acks with Status=HeartbeatStatusRuntimeGone and
// RuntimeGone=true instead of tearing down the connection. The daemon prunes
// the stale runtime and re-registers.
type DaemonHeartbeatAckPayload struct {
	RuntimeID          string                           `json:"runtimeId"`
	Status             string                           `json:"status"`
	ServerCapabilities []string                         `json:"serverCapabilities,omitempty"`
	RuntimeGone        bool                             `json:"runtimeGone,omitempty"`
	PendingModelList   *DaemonHeartbeatPendingModelList `json:"pendingModelList,omitempty"`
}

// DaemonHeartbeatPendingModelList names one request atomically claimed for
// the runtime whose heartbeat produced this ack.
type DaemonHeartbeatPendingModelList struct {
	RequestID string `json:"requestId"`
}
