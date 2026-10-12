package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"strings"
	"time"

	"boardly.dev/boardly/server/pkg/runfailure"
)

// This file owns the Claude Code stream-json wire shapes and the terminal
// contract that turns a finished stream into a Result.

// ── Claude SDK JSON types ──

type claudeSDKMessage struct {
	Type      string          `json:"type"`
	Message   json.RawMessage `json:"message,omitempty"`
	Subtype   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Model     string          `json:"model,omitempty"`
	Plugins   json.RawMessage `json:"plugins,omitempty"`

	// result fields
	ResultText string `json:"result,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	// TerminalReason is Claude Code's structured statement of why the turn
	// ended. Read separately from IsError because the CLI computes the two
	// independently — see claudeTerminalReasonFailure.
	TerminalReason string                            `json:"terminal_reason,omitempty"`
	DurationMs     float64                           `json:"duration_ms,omitempty"`
	NumTurns       int                               `json:"num_turns,omitempty"`
	TotalCostUSD   *float64                          `json:"total_cost_usd,omitempty"`
	Usage          *claudeUsage                      `json:"usage,omitempty"`
	ModelUsage     map[string]claudeResultModelUsage `json:"modelUsage,omitempty"`

	// log fields
	Log *claudeLogEntry `json:"log,omitempty"`

	// control request fields
	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`
}

type claudeLogEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type claudeMessageContent struct {
	ID          string               `json:"id,omitempty"`
	Role        string               `json:"role"`
	Model       string               `json:"model"`
	Content     []claudeContentBlock `json:"content"`
	ContentText *string              `json:"-"`
	Usage       *claudeUsage         `json:"usage,omitempty"`
}

func (content *claudeMessageContent) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID      string          `json:"id,omitempty"`
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *claudeUsage    `json:"usage,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	raw := bytes.TrimSpace(wire.Content)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return errors.New("Claude message is missing content")
	}
	content.ID = wire.ID
	content.Role = wire.Role
	content.Model = wire.Model
	content.Usage = wire.Usage
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return fmt.Errorf("decode Claude string content: %w", err)
		}
		content.ContentText = &text
		return nil
	}
	if raw[0] != '[' {
		return errors.New("Claude message content must be a string or array")
	}
	if err := json.Unmarshal(raw, &content.Content); err != nil {
		return fmt.Errorf("decode Claude content blocks: %w", err)
	}
	return nil
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type claudeResultModelUsage struct {
	InputTokens              int64    `json:"inputTokens"`
	OutputTokens             int64    `json:"outputTokens"`
	CacheReadInputTokens     int64    `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64    `json:"cacheCreationInputTokens"`
	CostUSD                  *float64 `json:"costUSD,omitempty"`
}

type claudeContentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	ServerName string          `json:"server_name,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`

	textPresent     bool
	thinkingPresent bool
}

func (block *claudeContentBlock) UnmarshalJSON(data []byte) error {
	type wireBlock claudeContentBlock
	var decoded wireBlock
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, field := range []string{"text", "thinking"} {
		raw, present := fields[field]
		if !present {
			continue
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '"' {
			return fmt.Errorf("Claude %s content must be a string", field)
		}
	}
	*block = claudeContentBlock(decoded)
	_, block.textPresent = fields["text"]
	_, block.thinkingPresent = fields["thinking"]
	return nil
}

type claudeControlRequestPayload struct {
	Subtype  string          `json:"subtype"`
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

// claudeTerminalReasonFailure turns Claude Code's structured terminal_reason
// into an error string when the reason means the turn did not actually produce
// an answer, and returns "" when it says nothing of the sort.
//
// Only prompt_too_long is recognized, and deliberately so. Every other terminal
// reason the CLI can report either already arrives with is_error set (api_error,
// model_error, error_max_turns …) or is a legitimate completion, so widening
// this would second-guess a contract that works — while prompt_too_long is the
// one case observed reaching the platform as a clean success. The CLI computes
// is_error from whether the last message it rendered was an API error and
// terminal_reason from why the turn stopped; when compaction fails and the run
// ends on a message that is not itself flagged, the two disagree and only
// terminal_reason is telling the truth.
//
// The returned text quotes the enum token so runfailure.Classify lands it in
// agent_error.context_overflow without depending on the CLI's prose, which
// varies by release and is empty in some shapes. That reason retires the
// saturated session so the next run on the conversation starts fresh — the
// automated form of the transcript-archiving workaround users reported.
func claudeTerminalReasonFailure(terminalReason, resultText string) string {
	if strings.TrimSpace(terminalReason) != runfailure.TerminalReasonPromptTooLong {
		return ""
	}
	msg := "claude ended the turn with terminal_reason=" + runfailure.TerminalReasonPromptTooLong +
		": the session's context window is exhausted and compaction could not recover it"
	if detail := strings.TrimSpace(resultText); detail != "" {
		msg += " (" + detail + ")"
	}
	return msg
}

func claudeResultUsage(msg claudeSDKMessage, fallbackModel string) map[string]TokenUsage {
	var turns *int32
	if msg.NumTurns > 0 {
		value := int32(msg.NumTurns)
		turns = &value
	}
	var duration *int64
	if msg.DurationMs > 0 {
		value := int64(math.Round(msg.DurationMs))
		duration = &value
	}
	if len(msg.ModelUsage) > 0 {
		usage := make(map[string]TokenUsage, len(msg.ModelUsage))
		for model, u := range msg.ModelUsage {
			if model == "" ||
				!claudeUsageHasTokens(
					u.InputTokens,
					u.OutputTokens,
					u.CacheReadInputTokens,
					u.CacheCreationInputTokens,
				) {
				continue
			}
			fact := TokenUsage{
				InputTokens:        u.InputTokens,
				OutputTokens:       u.OutputTokens,
				CacheReadTokens:    u.CacheReadInputTokens,
				CacheWriteTokens:   u.CacheCreationInputTokens,
				UsageBasis:         UsageBasisPerRun,
				Turns:              turns,
				ProviderDurationMs: duration,
				RawJSON:            usageFragment(u),
			}
			if u.CostUSD != nil {
				fact.CostUSDTicks = claudeCostTicks(*u.CostUSD)
			} else if len(msg.ModelUsage) == 1 && msg.TotalCostUSD != nil {
				fact.CostUSDTicks = claudeCostTicks(*msg.TotalCostUSD)
			}
			usage[model] = fact
		}
		if len(usage) > 0 {
			return usage
		}
	}

	model := msg.Model
	if model == "" {
		model = fallbackModel
	}
	if msg.Usage == nil || model == "" || !claudeUsageHasTokens(
		msg.Usage.InputTokens,
		msg.Usage.OutputTokens,
		msg.Usage.CacheReadInputTokens,
		msg.Usage.CacheCreationInputTokens,
	) {
		return nil
	}
	usage := map[string]TokenUsage{
		model: {
			InputTokens:        msg.Usage.InputTokens,
			OutputTokens:       msg.Usage.OutputTokens,
			CacheReadTokens:    msg.Usage.CacheReadInputTokens,
			CacheWriteTokens:   msg.Usage.CacheCreationInputTokens,
			UsageBasis:         UsageBasisPerRun,
			Turns:              turns,
			ProviderDurationMs: duration,
			RawJSON:            usageFragment(msg.Usage),
		},
	}
	if msg.TotalCostUSD != nil {
		fact := usage[model]
		fact.CostUSDTicks = claudeCostTicks(*msg.TotalCostUSD)
		usage[model] = fact
	}
	return usage
}

func claudeCostTicks(value float64) *int64 {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value > float64(math.MaxInt64)/1e10 {
		return nil
	}
	ticks := int64(math.Round(value * 1e10))
	return &ticks
}

func claudeUsageHasTokens(input, output, cacheRead, cacheWrite int64) bool {
	return input > 0 || output > 0 || cacheRead > 0 || cacheWrite > 0
}

func claudeToolResultHasAsyncLaunch(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	switch v := value.(type) {
	case map[string]any:
		if claudeMapHasAsyncLaunchStatus(v) {
			return true
		}
		if content, ok := v["content"].([]any); ok {
			return claudeArrayHasAsyncLaunchStatus(content)
		}
	case []any:
		return claudeArrayHasAsyncLaunchStatus(v)
	}
	return false
}

func claudeToolResultOutput(blockType string, raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	// Structured tool output is the tool's own value, not the surrounding
	// Claude stream event. Preserve semantic data. Advisor redacted results
	// are the sole provider-owned opaque ciphertext removed below.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return string(raw)
	}
	if blockType == "advisor_tool_result" {
		value = stripClaudeOpaqueResultFields(value)
	}
	if content, ok := value.([]any); ok {
		value = toolTranscriptContent(content)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

func stripClaudeOpaqueResultFields(value any) any {
	switch value := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(value))
		for key, child := range value {
			if key == "encrypted_content" || key == "encryptedContent" {
				continue
			}
			clean[key] = stripClaudeOpaqueResultFields(child)
		}
		return clean
	case []any:
		clean := make([]any, len(value))
		for index, child := range value {
			clean[index] = stripClaudeOpaqueResultFields(child)
		}
		return clean
	default:
		return value
	}
}

func claudeToolResultStatusDetails(
	blockType string,
	raw json.RawMessage,
	isError bool,
	toolName string,
) (ToolResultStatus, string) {
	failure := "Claude tool call failed"
	if toolName != "" {
		failure = "Claude tool " + toolName + " failed"
	}
	if isError {
		return ToolResultStatusFailed, failure
	}
	if !claudeProviderOwnedResultStatus(blockType) {
		return ToolResultStatusCompleted, ""
	}
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ToolResultStatusCompleted, ""
	}
	if code := claudeWebSearchErrorCode(value); code != "" {
		return ToolResultStatusFailed, failure + ": " + code
	}
	return ToolResultStatusCompleted, ""
}

func claudeProviderOwnedResultStatus(blockType string) bool {
	switch blockType {
	case "web_search_tool_result", "web_fetch_tool_result",
		"code_execution_tool_result", "bash_code_execution_tool_result",
		"text_editor_code_execution_tool_result", "tool_search_tool_result",
		"advisor_tool_result":
		return true
	default:
		return false
	}
}

func claudeWebSearchErrorCode(value any) string {
	switch value := value.(type) {
	case map[string]any:
		if blockType, _ := value["type"].(string); strings.HasSuffix(blockType, "_error") {
			if code, _ := value["error_code"].(string); code != "" {
				return code
			}
			return blockType
		}
		if code, _ := value["error_code"].(string); code != "" {
			return code
		}
	case []any:
		for _, item := range value {
			if code := claudeWebSearchErrorCode(item); code != "" {
				return code
			}
		}
	}
	return ""
}

func claudeArrayHasAsyncLaunchStatus(values []any) bool {
	for _, value := range values {
		if item, ok := value.(map[string]any); ok && claudeMapHasAsyncLaunchStatus(item) {
			return true
		}
	}
	return false
}

func claudeMapHasAsyncLaunchStatus(value map[string]any) bool {
	status, ok := value["status"].(string)
	return ok && status == "async_launched"
}

// ── Stream-json terminal contract ──

// assistantTurn is the classified outcome of one assistant stream event.
//
// The parser answers one question per event: may a fallback answer captured
// earlier in the run survive it?
type assistantTurn struct {
	text        string
	toolUses    int
	contractErr error
	// understood is false when the event's message body failed to parse, or
	// carried a content block type the backend does not handle. In both cases
	// the backend cannot tell whether the model delivered its answer inside
	// that event, so an earlier fallback must not outlive it.
	understood bool
}

// resolveFallback returns the empty-result fallback after this turn, given the
// fallback held before it. It is the single place the stream-json backend
// encodes which turns may leave an answer behind.
func (t assistantTurn) resolveFallback(prior string) string {
	switch {
	case t.toolUses > 0 || !t.understood:
		// A turn that invokes a tool is intermediate even when it also
		// contains narration. An unreadable turn tells us nothing at all —
		// the model may have answered in a shape we failed to parse. Neither
		// may leave a stale answer behind for the terminal result to adopt:
		// that is how pre-tool narration would reach a user as the final
		// answer, which the fail-closed contract below exists to stop.
		return ""
	case t.text != "":
		return t.text
	default:
		// Understood, text-less and tool-less — a thinking-only turn. It
		// carries neither an answer nor an intermediacy signal, so it must
		// leave an answer the model already delivered untouched.
		return prior
	}
}

// streamTerminalState keeps the user-facing final answer separate from the
// streamed completed items. Only a terminal result (or the last complete
// assistant item after an explicitly successful empty result) may become
// Result.Output.
type streamTerminalState struct {
	lastAssistantText string
	finalResultText   string
	sawResult         bool
	resultIsError     bool
	scanErr           error
	contractErr       error
	// terminalReasonError, when non-empty, is a failure the backend read out of
	// a STRUCTURED field on the terminal result event.
	//
	// It is not a claim that is_error missed the failure — on every frame
	// captured so far the two fire together. It is a claim about which one
	// NAMES the failure: Claude Code sets is_error from whether the last
	// message it rendered was an API error, and terminal_reason from why the
	// turn ended, so only the latter identifies the condition without relying
	// on the CLI's prose. Backends that read no such field leave this empty
	// and keep the pre-existing contract.
	terminalReasonError string
}

// finalizeStreamResult applies the fail-closed terminal contract for the
// stream-json protocol. A clean process exit is not proof that the protocol
// completed: success requires a result event. Failed runs always return an
// empty output so upstream fallbacks can never mistake a partial transcript
// for a final answer.
//
// A SUCCESSFUL run with no deliverable text returns an empty output for the same
// reason. Empty is the platform's signal for "this turn produced no final text",
// and every delivery surface owns its own decision keyed on it. Returning prose
// here instead makes the output non-empty and silently defeats those decisions
// at once — which words a user sees belongs to each delivery surface; the
// stream parser only reports whether text exists.
func finalizeStreamResult(
	provider string,
	timeout time.Duration,
	runErr error,
	writeErr error,
	exitErr error,
	sessionID string,
	state streamTerminalState,
	completionGuardError string,
) (status ResultStatus, output, errMsg string) {
	status = ResultCompleted
	switch {
	case state.terminalReasonError != "":
		// A recognized structured terminal reason wins outright — including
		// over is_error, which is the ordering that matters in practice.
		//
		// On the shape actually captured from Claude Code 2.1.220/2.1.221, a
		// context-exhausted turn arrives as is_error:true AND
		// terminal_reason:prompt_too_long together. Letting is_error go first
		// makes the structured branch dead code on the only frame we have, and
		// hands the failure whatever prose the CLI happened to put in `result`:
		// today that string classifies correctly by luck, but an empty or
		// reworded `result` degrades to "returned an error result without
		// details" → agent_error.unknown, which no resume blacklist covers, so
		// the saturated session stays pinned and the stall returns.
		//
		// Ordering it first costs nothing: the branch only fires for a reason
		// the backend positively recognized, and the message it builds carries
		// the CLI's own `result` text along as detail, so nothing is lost for
		// diagnosis.
		status = "failed"
		errMsg = state.terminalReasonError
	case state.resultIsError:
		status = "failed"
		errMsg = state.finalResultText
		if errMsg == "" {
			errMsg = provider + " returned an error result without details"
		}
	}

	switch {
	case status == "completed" && errors.Is(runErr, context.DeadlineExceeded):
		status = "timeout"
		errMsg = fmt.Sprintf("%s timed out after %s", provider, timeout)
	case status == "completed" && errors.Is(runErr, context.Canceled):
		status = "aborted"
		errMsg = "execution cancelled"
	case state.scanErr != nil && status == "completed":
		status = "failed"
		errMsg = fmt.Sprintf("%s stdout read error: %v", provider, state.scanErr)
	case state.contractErr != nil && status == "completed":
		status = "failed"
		errMsg = fmt.Sprintf("%s stream contract error: %v", provider, state.contractErr)
	case writeErr != nil && status == "completed" && sessionID == "":
		status = "failed"
		errMsg = fmt.Sprintf("write %s input: %v", provider, writeErr)
	case exitErr != nil && status == "completed":
		status = "failed"
		errMsg = fmt.Sprintf("%s exited with error: %v", provider, exitErr)
	case !state.sawResult && status == "completed":
		status = "failed"
		errMsg = provider + " stream ended without terminal result"
	}

	if status == "completed" && completionGuardError != "" {
		status = "failed"
		errMsg = completionGuardError
	}

	if status != "completed" {
		return status, "", errMsg
	}
	if state.finalResultText != "" {
		return status, state.finalResultText, ""
	}
	return status, state.lastAssistantText, ""
}

type streamProtocolObservation struct {
	provider                   string
	cliVersion                 string
	model                      string
	exitCode                   int
	eventCount                 int
	invalidEventCount          int
	assistantEventCount        int
	toolUseCount               int
	sawResult                  bool
	resultIsError              bool
	resultBytes                int
	lastAssistantBytes         int
	scannerError               bool
	lastEventType              string
	anthropicBaseURLConfigured bool
	// unhandledEventTypeCount / unhandledEventTypes / unhandledSubtypeCount
	// report stream events the parser did not turn into messages. They belong on
	// this line rather than only in a separate warning so they can be read
	// together with toolUseCount and invalidEventCount.
	//
	// They are evidence, not a verdict. A non-zero count means the stream
	// carried events we do not handle and is the starting point for identifying
	// a protocol change; a zero count means only that none were observed at the
	// top level, and does not establish that the agent used no tools — a CLI can
	// execute tools without serializing the updates at all, and a new shape can
	// be nested inside a type we already recognize.
	unhandledEventTypeCount int
	unhandledEventTypes     string
	unhandledSubtypeCount   int
	// unreadableAssistantCount reports assistant events whose message body did
	// not parse, or that carried a content block type the backend does not
	// handle. Unlike unhandledEventTypeCount (a TOP-LEVEL event type we skip),
	// this counts events we accepted and then could not fully read.
	//
	// Non-zero means the run dropped its empty-result fallback defensively, so
	// a run that ended with no final text may have had an answer we could not
	// see. It is the first number to check when a known-good agent starts
	// returning empty outputs after a CLI upgrade.
	unreadableAssistantCount int
}

// logStreamProtocolObservation records only protocol metadata. It deliberately
// excludes assistant/result text, tool input/output, the configured base URL,
// and environment values so diagnosing a missing terminal event cannot leak the
// run transcript or provider credentials into daemon logs.
func logStreamProtocolObservation(logger *slog.Logger, obs streamProtocolObservation) {
	logger.Info("agent stream protocol summary",
		"provider", obs.provider,
		"cli_version", obs.cliVersion,
		"model", obs.model,
		"exit_code", obs.exitCode,
		"event_count", obs.eventCount,
		"invalid_event_count", obs.invalidEventCount,
		"assistant_event_count", obs.assistantEventCount,
		"tool_use_count", obs.toolUseCount,
		"saw_result", obs.sawResult,
		"result_is_error", obs.resultIsError,
		"result_bytes", obs.resultBytes,
		"last_assistant_bytes", obs.lastAssistantBytes,
		"scanner_error", obs.scannerError,
		"last_event_type", obs.lastEventType,
		"unhandled_event_type_count", obs.unhandledEventTypeCount,
		"unhandled_event_types", obs.unhandledEventTypes,
		"unhandled_subtype_count", obs.unhandledSubtypeCount,
		"unreadable_assistant_count", obs.unreadableAssistantCount,
		"anthropic_base_url_configured", obs.anthropicBaseURLConfigured,
	)
}

func streamProcessExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
