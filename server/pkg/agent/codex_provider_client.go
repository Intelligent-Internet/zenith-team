package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"boardly.dev/boardly/server/pkg/redact"
)

// ── codexClient: JSON-RPC 2.0 transport ──

type codexClient struct {
	cfg                Config
	toolPolicy         ToolPolicy
	stdin              interface{ Write([]byte) (int, error) }
	mu                 sync.Mutex
	nextID             int
	pending            map[int]*pendingRPC
	processDone        chan struct{}
	processErr         error
	handshakeTimeout   time.Duration
	pid                int
	attempt            int
	activeLaunches     int64
	threadStartSent    bool
	threadStartStarted time.Time
	identityMu         sync.RWMutex
	threadID           string
	turnID             string
	onMessage          func(providerMessage)
	onSemanticActivity func(description string)
	onTurnDone         func(aborted bool)
	// onFinalAnswer fires only for an agent message the app-server itself
	// labeled `phase: "final_answer"` — the turn's deliverable, as opposed to
	// the intermediate agent messages that narrate work between tool calls.
	// Both still reach onMessage: the transcript keeps the whole timeline, only
	// Result.Output is narrowed to the deliverable.
	onFinalAnswer func(text string)
	// acceptNotification isolates the active turn from same-thread history
	// replay emitted while thread/resume is restoring prior conversation.
	// Unit-level protocol tests leave it nil and exercise dispatch directly.
	acceptNotification func(method string, params map[string]any) bool
	// onDiscardedNotification preserves out-of-band safety signals (such as
	// suppressing an initialize retry after observed activity) without letting
	// filtered history mutate current-turn output or lifecycle state.
	onDiscardedNotification func(method string, params map[string]any)

	notificationProtocol string // "unknown", "legacy", "raw"
	turnStarted          bool
	completedTurnIDs     map[string]bool

	usageMu sync.Mutex
	usage   TokenUsage // accumulated from turn events

	turnErrorMu sync.Mutex
	turnError   string // captured from turn/completed status=failed or terminal error notifications
}

// codexTurnNotificationGate keeps resume-time history replay from mutating the
// output or ending the new turn. Codex app-server can emit notifications before
// the turn/start RPC response, so the gate is armed before that request and uses
// turn/started (or legacy task_started) as the actual current-turn boundary.
// Protocols that omit those start events also omit a reliable current-turn ID;
// for compatibility, their post-arm events remain accepted. We therefore rely
// on the single stdout reader's ordering guarantee that resume history emitted
// before the thread/resume response is processed while the gate is still closed.
// A replay emitted after arm but before a start event cannot be distinguished
// from a valid legacy current-turn event without breaking those older streams.
// Its mutable lifecycle fields are only touched by the stdout reader goroutine;
// armed is atomic because the lifecycle goroutine flips it.
type codexTurnNotificationGate struct {
	armed   atomic.Bool
	started bool
	turnID  string
}

func (g *codexTurnNotificationGate) arm() {
	g.armed.Store(true)
}

func (g *codexTurnNotificationGate) accept(method string, params map[string]any) bool {
	if !g.armed.Load() {
		return false
	}

	if method == "codex/event" || strings.HasPrefix(method, "codex/event/") {
		msg, _ := params["msg"].(map[string]any)
		msgType, _ := msg["type"].(string)
		if msgType == "task_started" {
			g.started = true
			return true
		}
		// Older Codex event streams can omit task_started. Once turn/start is
		// armed, keep that compatibility; pre-arm replay is still excluded.
		return true
	}

	switch {
	case method == "turn/started":
		g.started = true
		g.turnID = extractNestedString(params, "turn", "id")
		return true
	case method == "turn/completed":
		if !g.started {
			// Older app-server versions can complete a turn without first
			// emitting turn/started. The pre-arm boundary still rejects resume
			// replay, while this keeps those versions functional.
			return true
		}
		turnID := extractNestedString(params, "turn", "id")
		return g.turnID == "" || turnID == "" || turnID == g.turnID
	case method == "thread/status/changed" || strings.HasPrefix(method, "item/"):
		if !g.started {
			return true
		}
		turnID, _ := params["turnId"].(string)
		return g.turnID == "" || turnID == "" || turnID == g.turnID
	default:
		// A terminal error may be the first notification produced by a failed
		// turn/start, so it must remain observable even without turn/started.
		return true
	}
}

func (c *codexClient) setTurnError(msg string) {
	msg = sanitizeAgentDiagnostic(msg)
	if msg == "" {
		return
	}
	c.turnErrorMu.Lock()
	defer c.turnErrorMu.Unlock()
	if c.turnError == "" {
		c.turnError = msg
	}
}

func (c *codexClient) getTurnError() string {
	c.turnErrorMu.Lock()
	defer c.turnErrorMu.Unlock()
	return c.turnError
}

func (c *codexClient) setThreadID(threadID string) {
	c.identityMu.Lock()
	c.threadID = threadID
	c.identityMu.Unlock()
}

func (c *codexClient) getThreadID() string {
	c.identityMu.RLock()
	defer c.identityMu.RUnlock()
	return c.threadID
}

func (c *codexClient) setTurnID(turnID string) {
	c.identityMu.Lock()
	c.turnID = turnID
	c.identityMu.Unlock()
}

func (c *codexClient) getTurnID() string {
	c.identityMu.RLock()
	defer c.identityMu.RUnlock()
	return c.turnID
}

type pendingRPC struct {
	ch     chan rpcResult
	method string
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type codexHandshakeTimeoutError struct {
	Method  string
	Timeout time.Duration
}

func (e *codexHandshakeTimeoutError) Error() string {
	return fmt.Sprintf("%s: %s did not respond after %s", CodexHandshakeTimeoutMarker, e.Method, e.Timeout)
}

func (e *codexHandshakeTimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}

func isCodexHandshakeRPC(method string) bool {
	switch method {
	case "initialize", "thread/start", "thread/resume", "thread/inject_items", "thread/name/set", "turn/start":
		return true
	default:
		return false
	}
}

func codexRequestContextError(ctx context.Context) error {
	var handshakeErr *codexHandshakeTimeoutError
	if errors.As(context.Cause(ctx), &handshakeErr) {
		return handshakeErr
	}
	return ctx.Err()
}

func (c *codexClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	requestCtx := ctx
	cancelRequest := func() {}
	if c.handshakeTimeout > 0 && isCodexHandshakeRPC(method) {
		timeoutErr := &codexHandshakeTimeoutError{Method: method, Timeout: c.handshakeTimeout}
		requestCtx, cancelRequest = context.WithTimeoutCause(ctx, c.handshakeTimeout, timeoutErr)
	}
	defer cancelRequest()

	c.mu.Lock()
	if c.processErr != nil {
		err := c.processErr
		c.mu.Unlock()
		return nil, err
	}
	if c.processDone == nil {
		c.processDone = make(chan struct{})
	}
	processDone := c.processDone
	c.nextID++
	id := c.nextID
	pr := &pendingRPC{ch: make(chan rpcResult, 1), method: method}
	c.pending[id] = pr
	c.mu.Unlock()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	data = append(data, '\n')
	if _, err := c.stdin.Write(data); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("write %s: %w", method, err)
	}
	if method == "turn/start" {
		threadID := ""
		if paramMap, ok := params.(map[string]any); ok {
			threadID, _ = paramMap["threadId"].(string)
		}
		c.cfg.Logger.Info("codex turn/start sent", "request_id", id, "thread_id", threadID)
	}

	select {
	case res := <-pr.ch:
		if res.err != nil && requestCtx.Err() != nil {
			return nil, codexRequestContextError(requestCtx)
		}
		return res.result, res.err
	case <-processDone:
		select {
		case res := <-pr.ch:
			if res.err != nil && requestCtx.Err() != nil {
				return nil, codexRequestContextError(requestCtx)
			}
			return res.result, res.err
		default:
		}
		c.mu.Lock()
		delete(c.pending, id)
		err := c.processErr
		c.mu.Unlock()
		if requestCtx.Err() != nil {
			return nil, codexRequestContextError(requestCtx)
		}
		if err == nil {
			err = errCodexProcessExited
		}
		return nil, err
	case <-requestCtx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, codexRequestContextError(requestCtx)
	}
}

func (c *codexClient) notify(method string) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	_, _ = c.stdin.Write(data)
}

func (c *codexClient) respond(id int, result any) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	_, _ = c.stdin.Write(data)
}

func (c *codexClient) respondError(id, code int, message string) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	_, _ = c.stdin.Write(data)
}

func (c *codexClient) closeAllPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, pr := range c.pending {
		pr.ch <- rpcResult{err: err}
		delete(c.pending, id)
	}
}

func (c *codexClient) markProcessExited(err error) {
	if err == nil {
		err = errCodexProcessExited
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.processErr == nil {
		c.processErr = err
		if c.processDone != nil {
			close(c.processDone)
		}
	}
	for id, pr := range c.pending {
		pr.ch <- rpcResult{err: err}
		delete(c.pending, id)
	}
}

func (c *codexClient) getProcessErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.processErr
}

// isCodexResumeOverflow reports whether a failed startOrResumeThread failed
// because the thread/resume RESPONSE did not fit in our stdout line buffer.
//
// Codex serializes the entire thread into that single response, so a long
// enough thread overflows agentStreamMaxLineBytes and the reader goroutine
// dies with bufio.ErrTooLong. Every other transport error means codex is
// gone; this one means codex is fine and we cannot read it. The distinction
// is what makes it a resume REJECTION: the thread cannot be handed to us at
// all, so only starting over can cure it — exactly what Result.ResumeRejected
// documents, and the evidence the daemon's fresh-session fallback looks for
// first.
//
// Recovery has to go through the daemon rather than a local thread/start
// fallback. By the time we get here the reader goroutine has exited and codex
// is blocked writing the rest of the oversized line into a pipe nobody drains,
// so this process can no longer answer any RPC. The daemon's fresh-session
// retry re-execs codex, which is the only way back.
//
// Requires ResumeSessionID: an overflow on a thread/start response is a
// different failure and nothing about the session pointer would fix it.
func isCodexResumeOverflow(opts ExecOptions, err error) bool {
	return opts.ResumeSessionID != "" && errors.Is(err, bufio.ErrTooLong)
}

func isCodexTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errCodexProcessExited) {
		return true
	}
	var handshakeErr *codexHandshakeTimeoutError
	if errors.As(err, &handshakeErr) {
		return true
	}
	return strings.HasPrefix(err.Error(), "write ")
}

func (c *codexClient) handleLine(line string) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return
	}

	// Check if it's a response to our request
	if _, hasID := raw["id"]; hasID {
		if _, hasResult := raw["result"]; hasResult {
			c.handleResponse(raw)
			return
		}
		if _, hasError := raw["error"]; hasError {
			c.handleResponse(raw)
			return
		}
		// Server request (has id + method)
		if _, hasMethod := raw["method"]; hasMethod {
			c.handleServerRequest(raw)
			return
		}
	}

	// Notification (no id, has method)
	if _, hasMethod := raw["method"]; hasMethod {
		c.handleNotification(raw)
	}
}

func (c *codexClient) handleResponse(raw map[string]json.RawMessage) {
	var id int
	if err := json.Unmarshal(raw["id"], &id); err != nil {
		return
	}

	c.mu.Lock()
	pr, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()

	if !ok {
		return
	}

	if errData, hasErr := raw["error"]; hasErr {
		var rpcErr struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(errData, &rpcErr)
		pr.ch <- rpcResult{err: fmt.Errorf("%s: %s (code=%d)", pr.method, rpcErr.Message, rpcErr.Code)}
	} else {
		// The stdout reader must publish the selected thread identity before it
		// can dispatch the next notification. Waiting for the request goroutine
		// to consume this buffered response leaves a scheduling window where a
		// notification from another thread can be accepted as ours.
		if pr.method == "thread/start" || pr.method == "thread/resume" {
			if threadID := extractThreadID(raw["result"]); threadID != "" {
				c.setThreadID(threadID)
			}
		}
		pr.ch <- rpcResult{result: raw["result"]}
	}
}

func (c *codexClient) handleServerRequest(raw map[string]json.RawMessage) {
	var id int
	_ = json.Unmarshal(raw["id"], &id)

	var method string
	_ = json.Unmarshal(raw["method"], &method)

	if c.toolPolicy == ToolPolicyConnectorOnly {
		switch method {
		case "item/commandExecution/requestApproval", "execCommandApproval",
			"item/fileChange/requestApproval", "applyPatchApproval":
			c.respond(id, map[string]any{"decision": "decline"})
		case "item/permissions/requestApproval":
			c.respond(id, map[string]any{
				"permissions": map[string]any{},
				"scope":       "turn",
			})
		case "mcpServer/elicitation/request":
			c.respond(id, map[string]any{"action": "decline", "content": nil, "_meta": nil})
		default:
			msg := fmt.Sprintf("unsupported codex app-server request: %s", method)
			c.cfg.Logger.Warn("codex: rejected connector-only server request", "method", method, "id", id)
			c.setTurnError(msg)
			c.respondError(id, -32601, msg)
		}
		return
	}

	// Auto-approve all exec/patch requests in ordinary daemon mode.
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		c.respond(id, map[string]any{"decision": "accept"})
	case "item/fileChange/requestApproval", "applyPatchApproval":
		c.respond(id, map[string]any{"decision": "accept"})
	case "item/permissions/requestApproval":
		c.respond(id, codexPermissionsApprovalResponse(raw["params"], c.cfg.Logger))
	case "mcpServer/elicitation/request":
		c.respond(id, map[string]any{"action": "accept", "content": nil, "_meta": nil})
	default:
		msg := fmt.Sprintf("unsupported codex app-server request: %s", method)
		c.cfg.Logger.Warn("codex: unhandled server request", "method", method, "id", id)
		c.setTurnError(msg)
		c.respondError(id, -32601, msg)
	}
}

// codexPermissionsApprovalResponse builds the auto-grant reply for a Codex
// item/permissions/requestApproval server request. In daemon mode there is no
// human to approve, so we echo back the requested network / fileSystem profile
// and scope it to the current turn, mirroring the other auto-accept branches in
// handleServerRequest.
//
// The grant is intentionally limited to the network / fileSystem keys we
// understand. A parse failure and any dropped key are logged so that a future
// app-server protocol that adds a new permission shape is visible in daemon
// logs instead of being silently narrowed away.
func codexPermissionsApprovalResponse(params json.RawMessage, logger *slog.Logger) map[string]any {
	var payload struct {
		Permissions map[string]any `json:"permissions"`
	}
	if err := json.Unmarshal(params, &payload); err != nil && logger != nil {
		logger.Warn(
			"codex: failed to parse permission approval request; granting empty turn-scoped profile",
			"error",
			err,
		)
	}

	granted := map[string]any{}
	var dropped []string
	for key, value := range payload.Permissions {
		switch key {
		case "network", "fileSystem":
			if value != nil {
				granted[key] = value
			}
		default:
			dropped = append(dropped, key)
		}
	}
	if len(dropped) > 0 && logger != nil {
		sort.Strings(dropped)
		logger.Warn(
			"codex: dropping unrecognized permission keys from approval request; add explicit handling if the app-server protocol expanded",
			"keys",
			dropped,
		)
	}

	return map[string]any{
		"permissions": granted,
		"scope":       "turn",
	}
}

func (c *codexClient) handleNotification(raw map[string]json.RawMessage) {
	var method string
	_ = json.Unmarshal(raw["method"], &method)

	var params map[string]any
	if p, ok := raw["params"]; ok {
		_ = json.Unmarshal(p, &params)
	}
	// Filter multiplexed subagent threads before the current-turn gate. The
	// gate mutates started/turnID on turn/started; letting another thread reach
	// it first can replace the main turn ID and make subsequent main-thread
	// items/completion look stale.
	if c.isNotificationFromOtherThread(params) {
		return
	}
	if c.acceptNotification != nil && !c.acceptNotification(method, params) {
		if c.onDiscardedNotification != nil {
			c.onDiscardedNotification(method, params)
		}
		return
	}

	// Legacy codex/event notifications
	if method == "codex/event" || strings.HasPrefix(method, "codex/event/") {
		c.notificationProtocol = "legacy"
		msgData, ok := params["msg"]
		if !ok {
			return
		}
		msgMap, ok := msgData.(map[string]any)
		if !ok {
			return
		}
		c.handleEvent(msgMap)
		return
	}

	// Raw v2 notifications
	if c.notificationProtocol != "legacy" {
		if c.notificationProtocol == "unknown" &&
			(method == "turn/started" || method == "turn/completed" ||
				method == "thread/started" || strings.HasPrefix(method, "item/")) {
			c.notificationProtocol = "raw"
		}

		if c.notificationProtocol == "raw" {
			c.handleRawNotification(method, params)
		}
	}
}

// codexPatchInputMaxBytes bounds the total diff/content bytes recorded for a
// single patch_apply event.
//
// The bound exists because the legacy protocol reports `add` and `delete` as
// whole-file contents rather than diffs, so a single generated file can be
// arbitrarily large. Unifying the limit at the persistence boundary is a
// separate change.
const codexPatchInputMaxBytes = 64 * 1024

// codexNormalizeLegacyChanges converts a legacy `patch_apply_*` changes map
// into the normalized change list.
//
// The legacy wire shape is map[path]FileChange, where FileChange is internally
// tagged on `type` and carries *different* payload fields per variant:
// add/delete carry whole-file `content`, while update carries `unified_diff`
// plus an optional `move_path`. Nothing in that shape is a plain unified diff
// for every case, which is why the normalized form keeps `diff` and `content`
// as alternatives instead of one field.
func codexNormalizeLegacyChanges(raw any) []any {
	changes, ok := raw.(map[string]any)
	if !ok || len(changes) == 0 {
		return nil
	}

	// Go randomizes map iteration; sort so a replayed event yields a stable
	// transcript instead of reshuffling the file list on every run.
	paths := make([]string, 0, len(changes))
	for path := range changes {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	out := make([]any, 0, len(paths))
	for _, path := range paths {
		entry, ok := changes[path].(map[string]any)
		if !ok {
			continue
		}
		norm := map[string]any{"path": path}
		if kind, _ := entry["type"].(string); kind != "" {
			norm["kind"] = kind
		}
		if diff, _ := entry["unified_diff"].(string); diff != "" {
			norm["diff"] = diff
		}
		// Presence, not non-emptiness: an added empty file has content "" and
		// should still render as an empty body rather than a missing one.
		if content, ok := entry["content"].(string); ok {
			norm["content"] = content
		}
		if movePath, _ := entry["move_path"].(string); movePath != "" {
			norm["move_path"] = movePath
		}
		out = append(out, norm)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// codexNormalizeRawChanges converts a v2 app-server `fileChange` item's
// changes array into the normalized change list.
//
// Unlike the legacy shape this is an ordered array of
// FileUpdateChange{path, kind, diff}, and `kind` is an *object*
// ({type, move_path?}) rather than a string — reading it as a string yields ""
// and silently loses the add/delete/update distinction.
//
// The `diff` field is misleadingly named: upstream's format_file_change_diff
// only produces a unified diff for `update`. For `add` and `delete` it returns
// the whole file's contents verbatim, and for a moved `update` it appends a
// trailing "\n\nMoved to: <path>" line. Feeding an added file's contents to a
// diff parser mislabels every line — and actively inverts the meaning of any
// line that happens to begin with '+' or '-' — so the payload is routed by
// `kind`, not by field name.
func codexNormalizeRawChanges(raw any) []any {
	changes, ok := raw.([]any)
	if !ok || len(changes) == 0 {
		return nil
	}
	out := make([]any, 0, len(changes))
	for _, change := range changes {
		entry, ok := change.(map[string]any)
		if !ok {
			continue
		}
		norm := map[string]any{}
		if path, _ := entry["path"].(string); path != "" {
			norm["path"] = path
		}
		kind := ""
		movePath := ""
		switch k := entry["kind"].(type) {
		case map[string]any:
			kind, _ = k["type"].(string)
			movePath, _ = k["move_path"].(string)
		case string:
			// Tolerate a flattened form so a future protocol tweak degrades
			// to a labeled change instead of an unlabelled one.
			kind = k
		}
		if kind != "" {
			norm["kind"] = kind
		}
		if movePath != "" {
			norm["move_path"] = movePath
		}

		body, bodyPresent := entry["diff"].(string)
		switch kind {
		case "add", "delete":
			// Whole-file contents, despite arriving under `diff`. Keyed on the
			// field being present rather than non-empty so an empty added file
			// still renders as an empty body instead of a missing one.
			if bodyPresent {
				norm["content"] = body
			} else if content, ok := entry["content"].(string); ok {
				norm["content"] = content
			}
		default:
			if body != "" {
				norm["diff"] = stripCodexMovedToSuffix(body, movePath)
			} else if content, ok := entry["content"].(string); ok {
				norm["content"] = content
			}
		}
		if len(norm) == 0 {
			continue
		}
		out = append(out, norm)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stripCodexMovedToSuffix removes the trailing "Moved to: <path>" line that
// upstream appends to a moved file's unified diff. The destination is already
// carried as move_path, and leaving the sentence in would render as two stray
// context lines at the end of the diff.
func stripCodexMovedToSuffix(diff, movePath string) string {
	if movePath == "" {
		return diff
	}
	suffix := "\n\nMoved to: " + movePath
	return strings.TrimSuffix(diff, suffix)
}

// codexPatchInput wraps normalized changes into the tool_use input, applying
// the size bound. It returns nil when nothing could be normalized so a
// malformed or unrecognized event degrades to the previous payload-less
// behavior rather than breaking the transcript.
func codexPatchInput(changes []any) map[string]any {
	if len(changes) == 0 {
		return nil
	}
	// Measure before anything rewrites the bodies, so the figure reported to the
	// reader is the size of the patch they actually produced.
	originalBytes := codexPatchBodyBytes(changes)

	// Redaction must run BEFORE the size budget, never after it. Several rules
	// only match a credential as a whole — the PEM rule needs both the BEGIN
	// and the END marker — so cutting a body at the budget can strand the
	// opening half of a private key in text that no pattern will match again.
	// The daemon and the server each redact further down the path, and neither
	// can recover a secret that truncation has already made unrecognizable.
	safe, ok := RedactInputMap(map[string]any{"changes": changes})["changes"].([]any)
	if !ok {
		safe = changes
	}

	input := map[string]any{"changes": safe}
	// Budget the redacted bodies, since those are what gets stored. Redaction
	// usually shrinks a body — a whole key collapses to a short placeholder —
	// so this is also the measurement that decides whether trimming is needed
	// at all.
	if codexPatchBodyBytes(safe) > codexPatchInputMaxBytes {
		applyCodexPatchBudget(safe)
		input["truncated"] = true
		input["original_bytes"] = originalBytes
	}
	return input
}

// maxRedactInputDepth bounds the walk in redactInputValue. Tool inputs are
// decoded from provider-supplied JSON, so nesting depth is attacker-influenced;
// without a bound a pathologically nested payload would recurse until the
// stack blows. Below the bound the value is DROPPED rather than passed through
// — returning the raw value there would hand back an unscrubbed string, which
// is exactly what redaction exists to prevent.
const maxRedactInputDepth = 32

const redactInputDepthPlaceholder = "[REDACTED DEPTH LIMIT]"

// RedactInputMap returns a copy of m with every string value passed through
// redact.Redact, including strings nested inside maps and slices. It is the
// one tool-input scrubber: this package runs it over Codex patch payloads and
// the daemon runs it over every tool_use input before the payload leaves the
// process.
//
// The nested walk is load-bearing, not defensive tidying: Codex records a file
// edit as changes[]{path, diff, content}, and a top-level-only pass would leave
// a credential inside a patch body — or the full contents of a deleted .env —
// untouched on its way to the database and the WebSocket broadcast. Composites
// are copied rather than scrubbed in place: the caller still holds the
// original map, so mutating through the shared reference would be a surprise
// at a distance.
func RedactInputMap(m map[string]any) map[string]any {
	return redactMapValues(m, 0)
}

func redactMapValues(m map[string]any, depth int) map[string]any {
	if m == nil {
		return nil
	}
	if depth >= maxRedactInputDepth {
		return map[string]any{"_": redactInputDepthPlaceholder}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = redactInputValue(v, depth+1)
	}
	return out
}

func redactInputValue(v any, depth int) any {
	if depth >= maxRedactInputDepth {
		return redactInputDepthPlaceholder
	}
	switch t := v.(type) {
	case string:
		return redact.Redact(t)
	case map[string]any:
		return redactMapValues(t, depth)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactInputValue(e, depth+1)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = redact.Redact(e)
		}
		return out
	default:
		return v
	}
}

// codexPatchBodyBytes totals the diff/content payload carried by a change list.
func codexPatchBodyBytes(changes []any) int {
	total := 0
	for _, change := range changes {
		entry, ok := change.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"diff", "content"} {
			if body, _ := entry[key].(string); body != "" {
				total += len(body)
			}
		}
	}
	return total
}

// applyCodexPatchBudget trims diff/content bodies in place until the payload
// fits codexPatchInputMaxBytes. Paths and kinds are never dropped: they are
// small and are the part a reviewer needs even when the body is gone.
func applyCodexPatchBudget(changes []any) {
	remaining := codexPatchInputMaxBytes
	for _, change := range changes {
		entry, ok := change.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"diff", "content"} {
			body, _ := entry[key].(string)
			if body == "" {
				continue
			}
			if len(body) <= remaining {
				remaining -= len(body)
				continue
			}
			if kept := truncateUTF8(body, remaining); kept != "" {
				entry[key] = kept
			} else {
				delete(entry, key)
			}
			entry["truncated"] = true
			remaining = 0
		}
	}
}

// truncateUTF8 cuts s to at most limit bytes without leaving a split rune, so
// the result still marshals as valid JSON.
func truncateUTF8(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	return strings.ToValidUTF8(s[:limit], "")
}

// codexNormalizePatchStatus maps both protocols' status spellings onto one
// snake_case vocabulary: the legacy enum is already snake_case
// (completed/failed/declined) while the v2 enum is camelCase and adds
// inProgress.
func codexNormalizePatchStatus(status string) string {
	switch status {
	case "":
		return ""
	case "inProgress", "in_progress":
		return "in_progress"
	default:
		return status
	}
}

// codexPatchResultOutput renders the tool_result line for a finished patch.
// It always produces a non-empty string when anything is known, because an
// empty output renders as an unexpandable blank row in the transcript.
func codexPatchResultOutput(status string, changes []any, stdout, stderr string) string {
	segments := make([]string, 0, 3)

	if headline := codexPatchHeadline(status, len(changes)); headline != "" {
		segments = append(segments, headline)
	}
	if out := strings.TrimSpace(stdout); out != "" {
		segments = append(segments, out)
	}
	if err := strings.TrimSpace(stderr); err != "" {
		segments = append(segments, err)
	}
	return strings.Join(segments, "\n")
}

func codexPatchHeadline(status string, fileCount int) string {
	switch {
	case status == "" && fileCount == 0:
		return ""
	case status == "":
		return fmt.Sprintf("%s changed", pluralizeFiles(fileCount))
	case fileCount == 0:
		return status
	default:
		return fmt.Sprintf("%s (%s)", status, pluralizeFiles(fileCount))
	}
}

func pluralizeFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

func (c *codexClient) handleEvent(msg map[string]any) {
	msgType, _ := msg["type"].(string)

	switch msgType {
	case "task_started":
		c.turnStarted = true
		if c.onMessage != nil {
			c.onMessage(
				providerMessage{
					Type:      providerMessageStatus,
					Status:    "running",
					SessionID: c.getThreadID(),
				},
			)
		}
	case "agent_message":
		text, _ := msg["message"].(string)
		if text != "" && c.onMessage != nil {
			c.onMessage(providerMessage{Type: providerMessageText, Content: text})
		}
	case "exec_command_begin":
		callID, _ := msg["call_id"].(string)
		command, _ := msg["command"].(string)
		if c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:   providerMessageToolUse,
				Tool:   "exec_command",
				CallID: callID,
				Input:  map[string]any{"command": command},
			})
		}
	case "exec_command_end":
		callID, _ := msg["call_id"].(string)
		output, _ := msg["output"].(string)
		if c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:   providerMessageToolResult,
				Tool:   "exec_command",
				CallID: callID,
				Output: output,
			})
		}
	case "patch_apply_begin":
		callID, _ := msg["call_id"].(string)
		if c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:   providerMessageToolUse,
				Tool:   "patch_apply",
				CallID: callID,
				Input:  codexPatchInput(codexNormalizeLegacyChanges(msg["changes"])),
			})
		}
	case "patch_apply_end":
		callID, _ := msg["call_id"].(string)
		stdout, _ := msg["stdout"].(string)
		stderr, _ := msg["stderr"].(string)
		status, _ := msg["status"].(string)
		if status == "" {
			// `status` postdates `success`; fall back so older builds still
			// report an outcome rather than a bare file count.
			if success, ok := msg["success"].(bool); ok {
				if success {
					status = "completed"
				} else {
					status = "failed"
				}
			}
		}
		changes := codexNormalizeLegacyChanges(msg["changes"])
		if c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:   providerMessageToolResult,
				Tool:   "patch_apply",
				CallID: callID,
				Output: codexPatchResultOutput(
					codexNormalizePatchStatus(status),
					changes,
					stdout,
					stderr,
				),
			})
		}
	case "task_complete":
		// Extract usage from legacy task_complete if present.
		c.extractUsageFromMap(msg)
		if c.onTurnDone != nil {
			c.onTurnDone(false)
		}
	case "turn_aborted":
		if c.onTurnDone != nil {
			c.onTurnDone(true)
		}
	}
}

func (c *codexClient) handleRawNotification(method string, params map[string]any) {
	// Ignore notifications from threads other than the one we are tracking.
	// Codex multiplexes subagent threads (e.g. memory consolidation) on the
	// same stdio pipe; only our thread should drive turn lifecycle and output.
	//
	// The v2 app-server-protocol schema guarantees a top-level threadId on
	// every notification. handleNotification performs this guard before the
	// stateful current-turn gate; retain it here as defense in depth for direct
	// callers. If a future codex revision introduces notifications without
	// threadId, they fall through — re-audit this guard when bumping codex.
	if c.isNotificationFromOtherThread(params) {
		return
	}

	switch method {
	case "turn/started":
		c.turnStarted = true
		if turnID := extractNestedString(params, "turn", "id"); turnID != "" {
			c.setTurnID(turnID)
		}
		if c.onMessage != nil {
			c.onMessage(
				providerMessage{
					Type:      providerMessageStatus,
					Status:    "running",
					SessionID: c.getThreadID(),
				},
			)
		}

	case "turn/completed":
		turnID := extractNestedString(params, "turn", "id")
		status := extractNestedString(params, "turn", "status")
		threadID, _ := params["threadId"].(string)
		c.cfg.Logger.Info(
			"codex turn/completed received",
			"thread_id",
			threadID,
			"turn_id",
			turnID,
			"status",
			status,
		)
		aborted := status == "cancelled" || status == "canceled" ||
			status == "aborted" || status == "interrupted"

		// Capture the error message from failed turns so callers can surface
		// a real reason instead of falling back to "empty output".
		if status == "failed" {
			errMsg := extractNestedString(params, "turn", "error", "message")
			if errMsg == "" {
				errMsg = "codex turn failed"
			}
			c.setTurnError(errMsg)
		}

		if c.completedTurnIDs == nil {
			c.completedTurnIDs = map[string]bool{}
		}
		if turnID != "" {
			if c.completedTurnIDs[turnID] {
				return
			}
			c.completedTurnIDs[turnID] = true
		}

		// Extract usage from turn/completed if present (e.g. params.turn.usage).
		if turn, ok := params["turn"].(map[string]any); ok {
			c.extractUsageFromMap(turn)
		}

		if c.onTurnDone != nil {
			c.onTurnDone(aborted)
		}

	case "error":
		// Top-level protocol error. Retrying notifications (willRetry=true) are
		// transient reconnect attempts; only capture terminal errors so we
		// don't stomp on a real failure later with a retry placeholder.
		willRetry, _ := params["willRetry"].(bool)
		errMsg := extractNestedString(params, "error", "message")
		if errMsg == "" {
			errMsg = extractNestedString(params, "message")
		}
		if errMsg != "" {
			errMsg = sanitizeAgentDiagnostic(errMsg)
			c.cfg.Logger.Warn("codex error notification", "message", errMsg, "will_retry", willRetry)
			if c.onSemanticActivity != nil {
				if willRetry {
					c.onSemanticActivity("error:retry")
				} else {
					c.onSemanticActivity("error:terminal")
				}
			}
			if !willRetry {
				// The error is terminal evidence, but turn/completed remains the
				// authoritative raw v2 lifecycle boundary.
				c.setTurnError(errMsg)
			}
		}

	case "thread/status/changed":
		// Thread status is a projection. In particular, idle can precede the
		// authoritative turn/completed notification.

	default:
		if strings.HasPrefix(method, "item/") {
			c.handleItemNotification(method, params)
		}
	}
}

func (c *codexClient) isNotificationFromOtherThread(params map[string]any) bool {
	threadID, ok := params["threadId"].(string)
	selectedThreadID := c.getThreadID()
	return ok && selectedThreadID != "" && threadID != selectedThreadID
}

func (c *codexClient) handleItemNotification(method string, params map[string]any) {
	item, _ := params["item"].(map[string]any)
	itemType, _ := item["type"].(string)
	itemID, _ := item["id"].(string)
	delta, hasDelta := codexItemDelta(method, params)
	if hasDelta {
		itemID = delta.ItemKey
	}
	if isCodexItemProgressActivity(method) && c.onSemanticActivity != nil {
		c.onSemanticActivity(describeCodexItemProgressActivity(method, itemType, itemID))
	}
	if hasDelta && c.onMessage != nil {
		c.onMessage(delta)
	}
	if item == nil {
		return
	}

	switch {
	case method == "item/started" && itemType == "commandExecution":
		if c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageToolUse,
				Tool:    "exec_command",
				CallID:  itemID,
				ItemKey: itemID,
				Input:   codexCommandInput(item),
			})
		}

	case method == "item/completed" && itemType == "commandExecution":
		output, _ := item["aggregatedOutput"].(string)
		if c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:       providerMessageToolResult,
				Tool:       "exec_command",
				CallID:     itemID,
				ItemKey:    itemID,
				Input:      codexCommandInput(item),
				Output:     output,
				FilePath:   codexItemSavedPath(item),
				Status:     codexToolStatus(item),
				ExitCode:   codexOptionalInt64(item["exitCode"]),
				DurationMs: codexOptionalInt64(item["durationMs"]),
			})
		}

	case method == "item/started" && itemType == "fileChange":
		if c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageToolUse,
				Tool:    "patch_apply",
				CallID:  itemID,
				ItemKey: itemID,
				Input:   codexPatchInput(codexNormalizeRawChanges(item["changes"])),
			})
		}

	case method == "item/completed" && itemType == "fileChange":
		status, _ := item["status"].(string)
		changes := codexNormalizeRawChanges(item["changes"])
		if c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:     providerMessageToolResult,
				Tool:     "patch_apply",
				CallID:   itemID,
				ItemKey:  itemID,
				Input:    codexPatchInput(changes),
				Output:   codexPatchResultOutput(codexNormalizePatchStatus(status), changes, "", ""),
				FilePath: codexItemSavedPath(item),
				Status:   codexNormalizePatchStatus(status),
			})
		}

	case method == "item/started" && itemType == "mcpToolCall":
		server, _ := item["server"].(string)
		tool, _ := item["tool"].(string)
		name := codexMCPToolName(server, tool)
		if itemID != "" && name != "" && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageToolUse,
				Tool:    name,
				CallID:  itemID,
				ItemKey: itemID,
				Input:   codexMCPArguments(item["arguments"]),
			})
		}

	case method == "item/started" && itemType == "webSearch":
		if itemID != "" && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageToolUse,
				Tool:    "web_search",
				CallID:  itemID,
				ItemKey: itemID,
				Input:   codexWebSearchInput(item),
			})
		}

	case method == "item/started" && itemType == "imageGeneration":
		if itemID != "" && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageToolUse,
				Tool:    "image_generation",
				CallID:  itemID,
				ItemKey: itemID,
				Input:   codexImageGenerationInput(item),
			})
		}

	case method == "item/completed" && itemType == "imageGeneration":
		if itemID != "" && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:     providerMessageToolResult,
				Tool:     "image_generation",
				CallID:   itemID,
				ItemKey:  itemID,
				Input:    codexImageGenerationInput(item),
				Output:   codexImageGenerationResult(item),
				FilePath: codexItemSavedPath(item),
				Status:   codexToolStatus(item),
			})
		}

	case method == "item/started" && itemType == "sleep":
		if itemID != "" && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageToolUse,
				Tool:    "clock.sleep",
				CallID:  itemID,
				ItemKey: itemID,
				Input:   codexSleepInput(item),
			})
		}

	case method == "item/completed" && itemType == "agentMessage":
		text, _ := item["text"].(string)
		phase, _ := item["phase"].(string)
		if text != "" && c.onMessage != nil {
			c.onMessage(
				providerMessage{
					Type:        providerMessageText,
					ItemKey:     itemID,
					Content:     text,
					FinalAnswer: phase == "final_answer",
				},
			)
		}
		if phase == "final_answer" {
			// A final answer that arrives before turn/started is still the
			// deliverable. It does not prove terminal state; only turn/completed
			// closes the raw v2 turn.
			if text != "" && c.onFinalAnswer != nil {
				c.onFinalAnswer(text)
			}
		}

	case method == "item/completed" && itemType == "reasoning":
		parts := codexReasoningParts(item)
		if itemID != "" && len(parts) > 0 && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageCompletedItem,
				ItemKey: itemID,
				Role:    RoleAgent,
				Parts:   parts,
			})
		}

	case method == "item/completed" && itemType == "plan":
		text, _ := item["text"].(string)
		if itemID != "" && text != "" && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageCompletedItem,
				ItemKey: itemID,
				Role:    RoleAgent,
				Parts:   []Part{ReasoningPart{Text: text}},
			})
		}

	case method == "item/completed" && itemType == "webSearch":
		if itemID != "" && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageToolResult,
				Tool:    "web_search",
				CallID:  itemID,
				ItemKey: itemID,
				Input:   codexWebSearchInput(item),
				Output:  codexWebSearchResult(item),
				Status:  "completed",
			})
		}

	case method == "item/completed" && itemType == "sleep":
		if itemID != "" && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:       providerMessageToolResult,
				Tool:       "clock.sleep",
				CallID:     itemID,
				ItemKey:    itemID,
				Input:      codexSleepInput(item),
				Status:     "completed",
				DurationMs: codexOptionalInt64(item["durationMs"]),
			})
		}

	case method == "item/completed" && itemType == "collabAgentToolCall":
		// Codex delegating to its own sub-agents. It is a tool call like any
		// other — it can carry a prompt, a model, and the threads it addressed
		// — so it belongs in the transcript rather than on the ignorable list,
		// otherwise the reason an agent paused is invisible to the reader.
		parts := codexCollabAgentToolCallParts(itemID, item)
		if itemID != "" && len(parts) > 0 && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageCompletedItem,
				ItemKey: itemID,
				Role:    RoleTool,
				Parts:   parts,
			})
		}

	case method == "item/completed" && itemType == "mcpToolCall":
		parts := codexMCPToolCallParts(itemID, item)
		if itemID != "" && len(parts) > 0 && c.onMessage != nil {
			c.onMessage(providerMessage{
				Type:    providerMessageCompletedItem,
				ItemKey: itemID,
				Role:    RoleTool,
				Parts:   parts,
			})
		}

	case method == "item/completed" && codexIgnorableCompletedItemType(itemType):
		// These are provider lifecycle markers without durable transcript
		// content. Keep the allowlist explicit so a new content-bearing item
		// cannot disappear silently.

	case method == "item/completed":
		message := fmt.Sprintf("unsupported completed Codex item type %q", itemType)
		c.cfg.Logger.Error(message, "item_id", itemID)
		c.setTurnError(message)
	}
}

func codexIgnorableCompletedItemType(itemType string) bool {
	switch itemType {
	case "contextCompaction":
		return true
	case "imageView":
		// Codex emits this display-only receipt after an image tool result.
		// Its path is daemon-local and the image content already traveled
		// through the tool result, so there is nothing durable to persist.
		return true
	case "subAgentActivity":
		// A sub-agent thread changed state. Observed kinds on codex-cli
		// 0.148.0: started, interacted, interrupted. The payload is
		// agentPath + agentThreadId and nothing else — no prompt, no output,
		// no result — so there is no durable transcript content to lose. The
		// work those threads actually did arrives as its own items; this is
		// the bookkeeping around them.
		return true
	case "userMessage":
		// Codex echoes input back as a completed item, notably when a turn
		// starts from a resumed session. The Conversation already owns the
		// user's Message; replaying the echo into the Run transcript would
		// duplicate it, and failing closed would kill an otherwise healthy
		// turn.
		return true
	default:
		return false
	}
}

func codexCommandInput(item map[string]any) map[string]any {
	input := map[string]any{}
	if command, ok := item["command"].(string); ok && command != "" {
		input["command"] = command
	}
	if cwd, ok := item["cwd"].(string); ok && cwd != "" {
		input["cwd"] = cwd
	}
	return input
}

func codexSleepInput(item map[string]any) map[string]any {
	input := map[string]any{}
	if duration := codexOptionalInt64(item["durationMs"]); duration != nil {
		input["durationMs"] = *duration
	}
	return input
}

func codexToolStatus(item map[string]any) string {
	if status := codexNormalizedToolStatus(item["status"]); status != "" {
		return status
	}
	if exitCode := codexOptionalInt64(item["exitCode"]); exitCode != nil && *exitCode != 0 {
		return "failed"
	}
	return "completed"
}

func codexOptionalInt64(value any) *int64 {
	var result int64
	switch value := value.(type) {
	case float64:
		result = int64(value)
		if float64(result) != value {
			return nil
		}
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return nil
		}
		result = parsed
	case int64:
		result = value
	case int:
		result = int64(value)
	default:
		return nil
	}
	return &result
}

func codexImageGenerationInput(item map[string]any) map[string]any {
	input := map[string]any{}
	if prompt, ok := item["revisedPrompt"].(string); ok && prompt != "" {
		input["revisedPrompt"] = prompt
	}
	if transparent, ok := item["transparentBackground"].(bool); ok {
		input["transparentBackground"] = transparent
	}
	return input
}

func codexItemSavedPath(item map[string]any) string {
	path, _ := item["savedPath"].(string)
	return path
}

func codexImageGenerationResult(item map[string]any) string {
	// Keep image bytes and daemon-local savedPath out of the text transcript.
	// Media publication remains the artifact flow's responsibility.
	output := map[string]any{"status": codexToolStatus(item)}
	if failure, ok := item["failure"].(map[string]any); ok {
		safe := map[string]any{}
		for _, key := range []string{"type", "limitId"} {
			if value, ok := failure[key].(string); ok {
				safe[key] = value
			}
		}
		if resetsAt := codexOptionalInt64(failure["resetsAt"]); resetsAt != nil {
			safe["resetsAt"] = *resetsAt
		}
		if len(safe) > 0 {
			output["failure"] = safe
		}
	}
	raw, _ := json.Marshal(output)
	return string(raw)
}

func codexWebSearchInput(item map[string]any) map[string]any {
	input := map[string]any{}
	if query, ok := item["query"].(string); ok && query != "" {
		input["query"] = query
	}
	action, _ := item["action"].(map[string]any)
	if safeAction := codexWebSearchAction(action); len(safeAction) > 0 {
		input["action"] = safeAction
	}
	return input
}

func codexWebSearchAction(action map[string]any) map[string]any {
	if len(action) == 0 {
		return nil
	}
	safe := map[string]any{}
	if actionType, ok := action["type"].(string); ok && actionType != "" {
		safe["type"] = actionType
	}
	for _, key := range []string{"query", "url", "pattern"} {
		if value, ok := action[key].(string); ok && value != "" {
			safe[key] = value
		}
	}
	if queries, ok := action["queries"].([]any); ok && len(queries) > 0 {
		safe["queries"] = queries
	}
	return safe
}

func codexWebSearchResult(item map[string]any) string {
	results, _ := item["results"].([]any)
	output := map[string]any{
		"resultCount": len(results),
		"results":     results,
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return ""
	}
	return string(raw)
}

func codexReasoningParts(item map[string]any) []Part {
	parts := []Part{}
	for _, field := range []string{"summary", "content"} {
		entries, _ := item[field].([]any)
		for _, entry := range entries {
			text, _ := entry.(string)
			if text != "" {
				parts = append(parts, ReasoningPart{Text: text})
			}
		}
	}
	return parts
}

type codexMCPResultOutput struct {
	Content           []any `json:"content,omitempty"`
	StructuredContent any   `json:"structuredContent,omitempty"`
}

// codexCollabAgentToolCallParts renders one sub-agent coordination call.
//
// Shape observed on codex-cli 0.148.0: tool, status, senderThreadId, plus
// receiverThreadIds, prompt, model, reasoningEffort and agentsStates, which
// are null or empty for a bare `wait` and populated when the call actually
// hands work to another thread. Only the non-empty ones are carried, so a
// wait renders as a wait instead of a wall of nulls.
func codexCollabAgentToolCallParts(itemID string, item map[string]any) []Part {
	tool, _ := item["tool"].(string)
	if itemID == "" || tool == "" {
		return []Part{}
	}
	name := "collab_agent." + tool

	input := map[string]any{}
	if sender, ok := item["senderThreadId"].(string); ok && sender != "" {
		input["senderThreadId"] = sender
	}
	if receivers, ok := item["receiverThreadIds"].([]any); ok && len(receivers) > 0 {
		input["receiverThreadIds"] = receivers
	}
	if prompt, ok := item["prompt"].(string); ok && prompt != "" {
		input["prompt"] = prompt
	}
	if model, ok := item["model"].(string); ok && model != "" {
		input["model"] = model
	}
	if effort, ok := item["reasoningEffort"].(string); ok && effort != "" {
		input["reasoningEffort"] = effort
	}

	parts := []Part{
		ToolCallPart{CallID: itemID, Name: name, Input: input},
		ToolResultPart{
			CallID:     itemID,
			Name:       name,
			Output:     codexCollabAgentStates(item["agentsStates"]),
			Status:     ToolResultStatus(codexNormalizedToolStatus(item["status"])),
			DurationMs: codexOptionalInt64(item["durationMs"]),
		},
	}
	if message := extractNestedString(item, "error", "message"); message != "" {
		parts = append(parts, ErrorPart{Message: message})
	}
	return parts
}

// codexCollabAgentStates keeps the per-thread state map when the provider
// sent one, and renders an empty map as no output rather than "{}".
func codexCollabAgentStates(value any) string {
	states, ok := value.(map[string]any)
	if !ok || len(states) == 0 {
		return ""
	}
	encoded, err := json.Marshal(states)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func codexMCPToolCallParts(itemID string, item map[string]any) []Part {
	server, _ := item["server"].(string)
	tool, _ := item["tool"].(string)
	name := codexMCPToolName(server, tool)
	if itemID == "" || name == "" {
		return []Part{}
	}

	parts := []Part{
		ToolCallPart{
			CallID: itemID,
			Name:   name,
			Input:  codexMCPArguments(item["arguments"]),
		},
		ToolResultPart{
			CallID:     itemID,
			Name:       name,
			Output:     codexMCPResult(item["result"]),
			FilePath:   codexItemSavedPath(item),
			Status:     ToolResultStatus(codexNormalizedToolStatus(item["status"])),
			DurationMs: codexOptionalInt64(item["durationMs"]),
		},
	}
	if message := extractNestedString(item, "error", "message"); message != "" {
		parts = append(parts, ErrorPart{Message: message})
	}
	return parts
}

func codexNormalizedToolStatus(value any) string {
	status, _ := value.(string)
	switch status {
	case "inProgress":
		return string(ToolResultStatusInProgress)
	case "completed", "failed", "cancelled", "declined":
		return status
	default:
		return ""
	}
}

func codexMCPToolName(server, tool string) string {
	if server == "" || tool == "" {
		return ""
	}
	return server + "/" + tool
}

func codexMCPArguments(value any) map[string]any {
	if arguments, ok := value.(map[string]any); ok {
		return arguments
	}
	if value == nil {
		return map[string]any{}
	}
	return map[string]any{"value": value}
}

func codexMCPResult(value any) string {
	result, ok := value.(map[string]any)
	if !ok {
		return ""
	}

	output := codexMCPResultOutput{}
	if content, ok := result["content"].([]any); ok && len(content) > 0 {
		output.Content = toolTranscriptContent(content)
	}
	if structured := result["structuredContent"]; codexNonEmptyJSONValue(structured) {
		output.StructuredContent = structured
	}
	if len(output.Content) == 0 && output.StructuredContent == nil {
		return ""
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return ""
	}
	return string(raw)
}

func codexNonEmptyJSONValue(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case string:
		return value != ""
	case []any:
		return len(value) > 0
	case map[string]any:
		return len(value) > 0
	default:
		return true
	}
}

func codexItemDelta(method string, params map[string]any) (providerMessage, bool) {
	itemID, _ := params["itemId"].(string)
	if itemID == "" {
		return providerMessage{}, false
	}
	var delta string
	var channel DeltaChannel
	operation := DeltaOperationAppend
	switch method {
	case "item/agentMessage/delta":
		channel = DeltaChannelText
		delta, _ = params["delta"].(string)
	case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
		channel = DeltaChannelReasoning
		delta, _ = params["delta"].(string)
	case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
		channel = DeltaChannelTool
		delta, _ = params["delta"].(string)
	case "item/mcpToolCall/progress":
		channel = DeltaChannelTool
		operation = DeltaOperationReplace
		delta, _ = params["message"].(string)
	default:
		return providerMessage{}, false
	}
	if delta == "" {
		return providerMessage{}, false
	}
	return providerMessage{
		Type:      providerMessageItemDelta,
		ItemKey:   itemID,
		Channel:   channel,
		Operation: operation,
		Content:   delta,
	}, true
}

func isCodexItemProgressActivity(method string) bool {
	return strings.HasPrefix(method, "item/")
}

func describeCodexItemProgressActivity(method, itemType, itemID string) string {
	if itemType == "" {
		itemType = "unknown"
	}
	if itemID == "" {
		return fmt.Sprintf("%s:%s", method, itemType)
	}
	return fmt.Sprintf("%s:%s:%s", method, itemType, itemID)
}

// ── Helpers ──

func extractThreadID(result json.RawMessage) string {
	var r struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return ""
	}
	return r.Thread.ID
}

func extractNestedString(m map[string]any, keys ...string) string {
	current := any(m)
	for _, key := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = obj[key]
	}
	s, _ := current.(string)
	return s
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
