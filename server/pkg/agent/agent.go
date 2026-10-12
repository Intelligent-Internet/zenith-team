// Package agent provides a unified interface for executing prompts via
// coding agents (Claude Code, Codex): one provider-neutral Provider interface,
// one ordered execution-event stream, and private per-provider implementations
// organized as sibling files (claude_provider*.go, codex_provider*.go).
// New(provider, cfg) is the one construction seam; the daemon never names
// a concrete provider type.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"boardly.dev/boardly/server/pkg/protocol"
)

// Provider is the unified interface for executing prompts via coding
// agents; call sites read it as agent.Provider.
type Provider interface {
	// Execute runs a prompt and returns one ordered event stream. The caller
	// must drain Execution.Events through the exactly-one Finished event. A
	// returned error means execution never started and no stream exists; after
	// Execute returns an Execution, every asynchronous failure is reported by
	// Finished rather than a second error channel.
	Execute(ctx context.Context, prompt string, opts ExecOptions) (*Execution, error)
}

// ToolPolicy selects a provider-neutral execution surface. The zero value
// preserves the provider's ordinary daemon behavior.
type ToolPolicy string

const (
	// ToolPolicyConnectorOnly limits an execution to its exact frozen
	// Connector apps and explicitly allowed MCP server namespaces.
	ToolPolicyConnectorOnly ToolPolicy = "connector-only"
)

// ExecOptions configures a single execution.
type ExecOptions struct {
	// RequireFinalAnswerProof asks the adapter to persist an explicitly proven
	// final answer. Unlabelled progress must never be promoted to an answer.
	RequireFinalAnswerProof bool
	// DisabledPlugins narrows provider-native plugin availability for this process.
	DisabledPlugins []string
	Cwd             string
	Model           string
	// SystemPrompt carries the provider-neutral runtime brief. Each provider
	// delivers a non-empty value through its native instruction mechanism;
	// empty leaves that mechanism unset.
	SystemPrompt              string
	ThreadName                string
	MaxTurns                  int
	Timeout                   time.Duration
	SemanticInactivityTimeout time.Duration
	// IdleWatchdogTimeout optionally narrows the daemon's generic no-message
	// watchdog for this execution. Zero keeps the daemon-wide window, and a
	// value above that window cannot extend the global safety bound.
	IdleWatchdogTimeout time.Duration
	// HandshakeTimeout bounds startup RPCs for providers with a long-lived
	// protocol transport. It is consumed by the codex app-server adapter;
	// zero uses the provider default rather than disabling the bound.
	HandshakeTimeout time.Duration
	ResumeSessionID  string // if non-empty, resume a previous agent session
	// ResumeExpected records that this run intended to continue a prior
	// conversation, independent of ResumeSessionID (which a fallback retry
	// may clear). When it is true but the provider ends up on a fresh thread —
	// the live resume RPC was rejected, or a transport failure forced a fresh
	// retry — the provider surfaces a continuity notice instead of silently
	// restarting.
	ResumeExpected bool
	// ResumeContinuityNotice is the text to prepend to the first turn when
	// ResumeExpected holds but the provider lands on a fresh thread anyway.
	// The caller owns the wording; empty means say nothing, and the caller
	// MUST leave it empty when its own prompt already carries the notice.
	ResumeContinuityNotice string
	// ExtraArgs is honored only by providers that opt in by reading it.
	ExtraArgs  []string        // daemon-wide default CLI arguments appended before CustomArgs
	CustomArgs []string        // per-agent CLI arguments appended after ExtraArgs
	McpConfig  json.RawMessage // if non-nil, MCP server config to pass via --mcp-config
	// ToolPolicy opts into a provider-neutral restricted execution surface.
	// Empty preserves the provider's default behavior.
	ToolPolicy ToolPolicy
	// AllowedMCPServers is the exact MCP namespace allowlist under
	// ToolPolicyConnectorOnly. It has no meaning under the default policy.
	AllowedMCPServers []string
	// ConnectorApps is the exact Codex app scope frozen for this run. Nil
	// inherits the provider default; non-nil, including empty, is fail-closed.
	ConnectorApps []ConnectorApp
	// PluginDirectories are daemon-resolved, provider-local Claude plugin roots.
	// They are never serialized into the cloud run contract.
	PluginDirectories []string
	// ThinkingLevel is the runtime-native reasoning/effort value (Claude's
	// "low|medium|high|xhigh|max", Codex's "none|minimal|low|medium|high|
	// xhigh"). Empty means "use the runtime/model default" — a provider that
	// consumes this skips its effort injection so the upstream CLI's own
	// default applies.
	ThinkingLevel string
	// ClaudeSettingsPath is a daemon-owned, run-local settings file passed
	// through Claude Code's --settings flag. Other providers ignore it.
	ClaudeSettingsPath string
	// Sandbox, when set, starts the provider inside the daemon's macOS
	// Seatbelt profile. A Seatbelt cannot start inside another, so a
	// sandboxed Codex is never asked for its own (connector.go). Nil
	// launches the provider directly.
	Sandbox *Sandbox
	// ClaudeSkillsRoot is a Run-owned additional directory containing .claude/skills.
	ClaudeSkillsRoot string
}

// runContext derives the execution context for an agent subprocess from the
// configured per-run timeout. A positive timeout imposes a hard wall-clock
// deadline; a zero (or negative) timeout imposes NO deadline, leaving
// liveness entirely to the daemon's inactivity watchdog so a session that
// keeps emitting events is never killed merely for running long. The caller
// owns the returned CancelFunc and must call it to release resources.
func runContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

// Execution represents one running provider invocation. Events preserves the
// provider's observed order and closes immediately after Finished is sent. A
// consumer must acknowledge each CompletedItem after it has durably accepted
// that item; the adapter does not advance to the next durable observation until
// then. A caller that stops draining or acknowledging the stream intentionally
// stops provider progress until the execution context is cancelled.
type Execution struct {
	Events <-chan Event
}

type acknowledgement struct {
	done chan struct{}
	once sync.Once
}

func newAcknowledgement() *acknowledgement {
	return &acknowledgement{done: make(chan struct{})}
}

func (ack *acknowledgement) acknowledge() {
	if ack == nil {
		return
	}
	ack.once.Do(func() { close(ack.done) })
}

// executionEventStream is the adapter-owned half of Execution. Completed
// items carry a private acknowledgement back to the sending adapter; all
// other non-delta events preserve ordinary channel backpressure.
type executionEventStream struct {
	events chan Event
}

type executionEventEmitter func(context.Context, Event) bool

func newExecutionEventStream() *executionEventStream {
	return &executionEventStream{events: make(chan Event)}
}

func (stream *executionEventStream) execution() *Execution {
	return &Execution{Events: stream.events}
}

func (stream *executionEventStream) close() {
	close(stream.events)
}

func (stream *executionEventStream) send(ctx context.Context, event Event) bool {
	if _, disposable := event.(ItemDelta); disposable {
		select {
		case stream.events <- event:
		default:
		}
		return true
	}

	var ack *acknowledgement
	if item, durable := event.(CompletedItem); durable {
		ack = newAcknowledgement()
		item.acknowledgement = ack
		event = item
	}

	delivered := false
	select {
	case stream.events <- event:
		delivered = true
	default:
	}
	if !delivered {
		select {
		case stream.events <- event:
		case <-ctx.Done():
			return false
		}
	}
	if ack == nil {
		return true
	}
	select {
	case <-ack.done:
		return true
	case <-ctx.Done():
		return false
	}
}

// Event is the closed provider-neutral execution stream. Only the event types
// in this file implement it.
type Event interface {
	isExecutionEvent()
}

// DeltaChannel identifies which completed-item part a delta previews.
type DeltaChannel string

const (
	DeltaChannelText      DeltaChannel = "text"
	DeltaChannelReasoning DeltaChannel = "reasoning"
	DeltaChannelTool      DeltaChannel = "tool"
)

// DeltaOperation describes whether Content extends or replaces the current
// local scratch value for an item and channel.
type DeltaOperation string

const (
	DeltaOperationAppend  DeltaOperation = "append"
	DeltaOperationReplace DeltaOperation = "replace"
)

// ItemDelta is disposable local-only partial output. Its ItemKey correlates it
// with a later CompletedItem; callers may coalesce or drop any delta without
// losing durable state and must never persist it as transcript history.
type ItemDelta struct {
	ItemKey   string         `json:"itemKey"`
	Channel   DeltaChannel   `json:"channel"`
	Operation DeltaOperation `json:"operation"`
	Content   string         `json:"content"`
}

func (ItemDelta) isExecutionEvent() {}

// Role is the Boardly-owned speaker vocabulary for completed items. It is
// the same vocabulary the transcript wire carries (agent/tool/system), so
// nothing between a provider adapter and PostgreSQL translates roles.
type Role string

const (
	RoleAgent  Role = "agent"
	RoleTool   Role = "tool"
	RoleSystem Role = "system"
)

// Part is the closed Boardly-owned content vocabulary. Vendor wire objects are
// decoded into these values inside pkg/agent and never cross this seam.
type Part interface {
	isItemPart()
}

type TextPart struct {
	Text  string `json:"text"`
	Final bool   `json:"final,omitempty"`
}

func (TextPart) isItemPart() {}

type ReasoningPart struct {
	Text string `json:"text"`
}

func (ReasoningPart) isItemPart() {}

type ToolCallPart struct {
	CallID string         `json:"callId"`
	Name   string         `json:"name"`
	Input  map[string]any `json:"input,omitempty"`
}

func (ToolCallPart) isItemPart() {}

type ToolResultPart struct {
	CallID string `json:"callId"`
	Name   string `json:"name,omitempty"`
	Output string `json:"output,omitempty"`
	// FilePath is daemon-local provider output that must be materialized as an
	// Artifact. It is deliberately omitted from the durable transcript wire.
	FilePath   string           `json:"-"`
	Status     ToolResultStatus `json:"status,omitempty"`
	ExitCode   *int64           `json:"exitCode,omitempty"`
	DurationMs *int64           `json:"durationMs,omitempty"`
}

func (ToolResultPart) isItemPart() {}

type ToolResultStatus string

const (
	ToolResultStatusInProgress ToolResultStatus = "in_progress"
	ToolResultStatusCompleted  ToolResultStatus = "completed"
	ToolResultStatusFailed     ToolResultStatus = "failed"
	ToolResultStatusDeclined   ToolResultStatus = "declined"
	ToolResultStatusCancelled  ToolResultStatus = "cancelled"
)

type ErrorPart struct {
	Message string `json:"message"`
}

func (ErrorPart) isItemPart() {}

// CompletedItem is one immutable durable transcript entity. Adapters must not
// mutate Key, Role, or any Part after sending this value.
type CompletedItem struct {
	Key   string `json:"key"`
	Role  Role   `json:"role"`
	Parts []Part `json:"parts"`

	acknowledgement *acknowledgement
}

func (CompletedItem) isExecutionEvent() {}

// Acknowledge releases the provider to deliver its next durable observation.
// The daemon calls it only after PostgreSQL commits the item and returns the
// authoritative transcript head. Manually constructed test items have no
// acknowledgement and make this method a no-op.
func (item CompletedItem) Acknowledge() {
	item.acknowledgement.acknowledge()
}

// ToolActivityPhase marks where a provider tool item is in its lifecycle.
// Tool activity is ephemeral control metadata used only for watchdog
// selection; it is never persisted or broadcast as transcript content. Only
// the start is ever announced this way — completion always arrives as the
// item's own tool-result part.
type ToolActivityPhase string

const ToolActivityPhaseStarted ToolActivityPhase = "started"

// ToolActivity brackets a provider-native tool item whose execution can be
// silent for longer than the ordinary provider idle window. ItemKey is stable
// within the execution, so duplicate lifecycle observations are idempotent.
type ToolActivity struct {
	ItemKey string            `json:"itemKey"`
	Phase   ToolActivityPhase `json:"phase"`
}

func (ToolActivity) isExecutionEvent() {}

// SessionReady announces that SessionID is safe for the daemon to persist as
// a resume pointer. Providers must not emit it before their resume artifact is
// usable.
type SessionReady struct {
	SessionID string `json:"sessionId"`
}

func (SessionReady) isExecutionEvent() {}

// Finished is the exactly-one terminal event. No event may follow it.
type Finished struct {
	Result Result `json:"result"`
}

func (Finished) isExecutionEvent() {}

// providerMessage is the private common shape used by the Codex legacy and
// app-server decoders before they normalize into the public event stream.
// It is deliberately not part of the provider seam.
type providerMessageType string

const (
	providerMessageText          providerMessageType = "text"
	providerMessageThinking      providerMessageType = "thinking"
	providerMessageToolUse       providerMessageType = "tool-use"
	providerMessageToolResult    providerMessageType = "tool-result"
	providerMessageStatus        providerMessageType = "status"
	providerMessageError         providerMessageType = "error"
	providerMessageLog           providerMessageType = "log"
	providerMessageItemDelta     providerMessageType = "item-delta"
	providerMessageCompletedItem providerMessageType = "completed-item"
)

type providerMessage struct {
	FinalAnswer bool
	Type        providerMessageType
	Content     string         // text content (Text, Error, Log)
	Tool        string         // tool name (ToolUse, ToolResult)
	CallID      string         // tool call ID (ToolUse, ToolResult)
	Input       map[string]any // tool input (ToolUse)
	Output      string         // tool output (ToolResult)
	FilePath    string         // daemon-local provider output path (ToolResult)
	Status      string         // agent status string (Status)
	ExitCode    *int64         // normalized command exit code when reported
	DurationMs  *int64         // normalized provider-reported tool duration
	Level       string         // log level (Log)
	SessionID   string         // provider session id (Status), for early resume-pointer pinning
	ItemKey     string         // provider-native item identity when available
	Channel     DeltaChannel   // item delta channel
	Operation   DeltaOperation // item delta append/replace semantics
	Role        Role           // normalized role (CompletedItem)
	Parts       []Part         // normalized durable parts (CompletedItem)
	processed   *acknowledgement
}

func (message providerMessage) acknowledgeProcessing() {
	message.processed.acknowledge()
}

// UsageBasis names what a usage fact's counters measure.
type UsageBasis string

const (
	// UsageBasisPerRun means the counters cover this run's own consumption.
	// The default when a provider stamps nothing.
	UsageBasisPerRun UsageBasis = "per_run"
	// UsageBasisSessionCumulative means the provider reports totals for the
	// whole resumed session, so the fact supersedes rather than adds to the
	// session's earlier runs. The codex adapter reports on this basis.
	UsageBasisSessionCumulative UsageBasis = "session_cumulative"
)

// TokenUsage tracks token consumption for a single model.
type TokenUsage struct {
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	ReasoningTokens    *int64
	Turns              *int32
	ProviderDurationMs *int64
	CostUSDTicks       *int64
	// UsageBasis is what the counters measure; empty means per_run.
	UsageBasis UsageBasis
	// RawJSON is the provider's sanitized usage-only fragment. It never carries
	// the surrounding response, transcript, prompts, or tool data.
	RawJSON string
}

func usageFragment(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

// ResultStatus is the closed vocabulary of provider-session outcomes.
type ResultStatus string

const (
	ResultCompleted ResultStatus = "completed"
	ResultFailed    ResultStatus = "failed"
	ResultAborted   ResultStatus = "aborted"
	ResultTimeout   ResultStatus = "timeout"
	ResultCancelled ResultStatus = "cancelled"
	// ResultIdleWatchdog is stamped by the consumer when its idle watchdog
	// force-stopped a silent provider — a first-class outcome, not a
	// provider-emitted one.
	ResultIdleWatchdog ResultStatus = "idle_watchdog"
)

// Result is the final outcome after an agent session completes.
type Result struct {
	Status     ResultStatus
	Output     string // final user-facing output selected by the provider
	Error      string // error message if failed
	DurationMs int64
	SessionID  string
	Usage      map[string]TokenUsage // keyed by model name
	// ResumeRejected is positive evidence that this run's requested resume
	// was itself refused — the transcript is gone, the session belongs to
	// another provider account, or the session exists but its history can no
	// longer be replayed to the provider. What unites these is that the
	// resume CANNOT continue and only starting over can cure it, so it is
	// what the daemon's fresh-session fallback looks for first. Note the
	// last case keeps a non-empty SessionID — the daemon gates on this
	// boolean, not an empty id.
	//
	// Providers must NOT set it for failures a new session cannot cure:
	// network drops, rate limits, quota, provider 5xx, or auth errors. Those
	// keep the session pointer so the platform's own retry can resume the
	// truncated conversation.
	ResumeRejected bool
	// codexInitializeRetrySafe is provider-internal evidence that an
	// initialize timeout happened before semantic activity and after the
	// process tree was reaped. Not part of the public result contract.
	codexInitializeRetrySafe bool
	// codexStartupRefreshRetrySafe is provider-internal evidence that the
	// first turn produced no semantic progress because Codex could not load
	// its model catalog, and that the process tree was reaped afterwards.
	codexStartupRefreshRetrySafe bool
}

// Config configures a Provider instance.
type Config struct {
	ExecutablePath string            // path to the CLI binary (claude, codex)
	CLIVersion     string            // detected version paired with ExecutablePath; observation only, never used to choose behavior
	Env            map[string]string // extra environment variables
	Logger         *slog.Logger
	RunID          string
	RuntimeID      string
	DaemonVersion  string
	CodexVersion   string
}

// launchHeaders maps each supported provider to the user-visible skeleton
// the daemon spawns before any custom args are appended. Intentionally
// minimal — only the command + a short mode label. Internal flags, transport
// values, and environment variables are deliberately omitted so the string
// is a hint about *what* users are extending, not a dump of the full
// command line.
var launchHeaders = map[string]string{
	"claude": "claude (stream-json)",
	"codex":  "codex app-server",
}

// LaunchHeader returns the user-visible launch skeleton for a provider, or
// an empty string if the provider is unknown. Callers (the runtimes API, the
// desktop probe) render this as a preview so users understand which command
// their custom args get appended to.
func LaunchHeader(provider string) string {
	return launchHeaders[provider]
}

// New constructs the Provider for a provider name. Adding a provider means
// adding its case here and its sibling <name>_provider*.go files — nothing
// else in the daemon changes. The name vocabulary is centralized in
// protocol.SupportedProviders.
func New(provider string, cfg Config) (Provider, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	switch provider {
	case "claude":
		return &claudeProvider{cfg: cfg}, nil
	case "codex":
		return &codexProvider{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unknown provider: %q (supported: %s)",
			provider, strings.Join(protocol.SupportedProviders, ", "))
	}
}
