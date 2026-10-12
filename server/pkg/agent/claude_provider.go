package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// claudeTerminateGraceNanos optionally overrides, in nanoseconds, how long a
// cancelled claude process group is given to exit after SIGTERM before it is
// SIGKILLed. Set via atomic store in tests; zero keeps the default.
var claudeTerminateGraceNanos atomic.Int64

func claudeTerminateGrace() time.Duration {
	if n := claudeTerminateGraceNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return 5 * time.Second
}

// claudeProvider implements Provider by spawning the Claude Code CLI
// with --output-format stream-json. Constructed by New("claude", cfg).
type claudeProvider struct {
	cfg Config
}

func (b *claudeProvider) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Execution, error) {
	if err := validateConnectorExecutionOptions(opts); err != nil {
		return nil, fmt.Errorf("validate claude connector scope: %w", err)
	}
	opts = normalizeConnectorExecutionOptions(opts)
	if opts.ToolPolicy == ToolPolicyConnectorOnly {
		if err := validateClaudeConnectorOnlySettings(opts.ClaudeSettingsPath); err != nil {
			return nil, fmt.Errorf("validate claude connector-only settings: %w", err)
		}
	}
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "claude"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("claude executable not found at %q: %w", execPath, err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	var mcpConfigPath, systemPromptPath string
	var mcpFileCleanup, systemPromptFileCleanup func()
	// Clean up private files if execution returns before the goroutine takes
	// ownership of them.
	defer func() {
		if mcpFileCleanup != nil {
			mcpFileCleanup()
		}
		if systemPromptFileCleanup != nil {
			systemPromptFileCleanup()
		}
	}()
	if opts.SystemPrompt != "" {
		path, err := writeClaudeSystemPromptToTemp(opts.SystemPrompt)
		if err != nil {
			cancel()
			return nil, err
		}
		systemPromptPath = path
		systemPromptFileCleanup = func() { cleanupClaudeSystemPromptTemp(systemPromptPath) }
	}

	args := buildClaudeArgs(opts, systemPromptPath, b.cfg.Logger)

	// If the caller provided an MCP config, write it to a temp file and pass
	// --mcp-config <path> so the agent uses a controlled set of MCP servers
	// instead of inheriting from the outer Claude Code session.
	if hasManagedMcpConfig(opts.McpConfig) {
		path, err := writeMcpConfigToTemp(opts.McpConfig)
		if err != nil {
			cancel()
			return nil, err
		}
		mcpConfigPath = path
		mcpFileCleanup = func() { cleanupMcpConfigTemp(mcpConfigPath) }
		args = append(args, "--mcp-config", mcpConfigPath)
	}

	launchPath, launchArgs := opts.Sandbox.Command(execPath, args)
	cmd := exec.CommandContext(runCtx, launchPath, launchArgs...)
	hideAgentWindow(cmd)
	// Run claude in its own process group so cancellation can reach the whole
	// tree — the claude CLI plus the MCP servers and tool subprocesses it
	// spawns — not just the direct child. The default CommandContext behavior
	// SIGKILLs only the leader, which orphans those descendants; on a resumed
	// stream-json session with no wall-clock timeout they then keep running and
	// burning model budget long after the run was cancelled, and under a
	// single-concurrency daemon starve every queued run. This mirrors the
	// same fix made for codex.
	configureProcessGroup(cmd)
	// Take over context cancellation: the default would SIGKILL only the leader
	// the instant runCtx is done. We instead drive a graceful group-wide
	// SIGTERM→SIGKILL from the cancellation goroutine below and close stdout
	// only after the tree has been signaled. Returning nil keeps os/exec from
	// racing us with its own kill; WaitDelay remains the hard backstop.
	cmd.Cancel = func() error { return nil }
	b.cfg.Logger.Info("agent command", "exec", execPath, "args", args, "sandboxed", opts.Sandbox != nil)
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)
	if opts.ToolPolicy == ToolPolicyConnectorOnly {
		cmd.Env = mergeEnv(cmd.Env, map[string]string{
			"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1",
			"CLAUDE_CODE_DISABLE_CLAUDE_MDS":  "1",
		})
	}
	if err := claudeRootSudoPreflight(args, cmd.Env); err != nil {
		cancel()
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claude stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claude stdin pipe: %w", err)
	}
	var closeStdinOnce sync.Once
	closeStdin := func() { closeStdinOnce.Do(func() { _ = stdin.Close() }) }
	// Capture stderr into both the daemon log (as before) and a bounded tail
	// buffer so we can include the last few KB in Result.Error when claude
	// exits unexpectedly. Without the tail, an exit-code-only failure looks
	// like "claude exited with error: exit status 3" — which is useless for
	// root-causing V8 aborts, Bun panics, or any other CLI-side crash.
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[claude:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		closeStdin()
		cancel()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	b.cfg.Logger.Info("claude started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	// cmd.Start() succeeded — transfer temp file ownership to the goroutine.
	mcpFileCleanup = nil
	systemPromptFileCleanup = nil

	eventStream := newExecutionEventStream()

	// procDone closes once cmd.Wait() returns, letting the cancellation handler
	// skip a process that already exited and avoid signaling a dead/reused pid.
	procDone := make(chan struct{})

	// writeClaudeInput runs in its own goroutine so it cannot deadlock
	// against the stdout reader. With --verbose --output-format stream-json
	// the CLI emits a startup banner before reading its first stdin frame;
	// if nothing is draining stdout while we write the prompt, claude blocks
	// writing stdout, never reads stdin, and our Write blocks until runCtx
	// fires. The field symptom is "write |1: The pipe has been ended."
	// surfacing exactly at the per-run timeout when the kill invalidates
	// the still-blocked pipe.
	//
	// Keep stdin open after the initial user message. Claude's stream-json
	// protocol can emit control_request events mid-run and expects matching
	// control_response frames on the same input stream; closing stdin here
	// leaves the child stuck waiting for a response until its own fallback
	// timeout.
	writeDone := make(chan error, 1)
	go func() {
		err := writeClaudeInput(stdin, prompt)
		if err != nil {
			closeStdin()
		}
		writeDone <- err
	}()

	go func() {
		defer cancel()
		defer eventStream.close()
		if mcpConfigPath != "" {
			defer cleanupMcpConfigTemp(mcpConfigPath)
		}
		if systemPromptPath != "" {
			defer cleanupClaudeSystemPromptTemp(systemPromptPath)
		}

		startTime := time.Now()
		var lastAssistantText string
		var finalResultText string
		sawResult := false
		resultIsError := false
		terminalReasonError := ""
		var sessionID string
		sawAsyncLaunch := false
		usage := make(map[string]TokenUsage)
		eventCount := 0
		invalidEventCount := 0
		assistantEventCount := 0
		toolUseCount := 0
		unreadableAssistantCount := 0
		var streamContractErr error
		pluginsVerified := len(opts.DisabledPlugins) == 0 || opts.ToolPolicy == ToolPolicyConnectorOnly
		itemSequence := 0

		// On cancellation / timeout, terminate claude (and every MCP server and
		// tool subprocess it spawned) BEFORE unblocking the scanner. EOF stdin
		// to nudge a clean exit, then SIGTERM the whole process group, give it a
		// grace period, and SIGKILL the group if any member is still alive.
		// SIGKILL is uncatchable, so once delivered no group member can write
		// again — only then is it safe to close the stdout read end as a
		// last-resort unblock for a scanner a wedged descendant still keeps
		// open. WaitDelay is the final backstop.
		go func() {
			select {
			case <-procDone:
				return // finished on its own; nothing to terminate
			case <-runCtx.Done():
			}
			closeStdin()
			if cmd.Process != nil {
				signalProcessGroup(cmd.Process, syscall.SIGTERM)
				// Escalate to a group SIGKILL unless the WHOLE process group has
				// exited within the grace window. This must key off the process
				// group, not procDone: procDone only means cmd.Wait() returned
				// for the leader, so a SIGTERM-ignoring descendant that does not
				// hold claude's stdout would let the leader exit, close procDone,
				// and skip the SIGKILL — leaking exactly the orphan this fix
				// targets. waitProcessGroupGone returns as soon as the group is
				// empty, so the graceful case adds no latency.
				if !waitProcessGroupGone(cmd.Process, claudeTerminateGrace()) {
					signalProcessGroup(cmd.Process, syscall.SIGKILL)
				}
			}
			_ = stdout.Close()
		}()

		scanner := newAgentStreamScanner(stdout)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var msg claudeSDKMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				invalidEventCount++
				if streamContractErr == nil {
					streamContractErr = fmt.Errorf("decode Claude stream frame: %w", err)
				}
				continue
			}
			eventCount++

			switch msg.Type {
			case "assistant":
				assistantEventCount++
				itemSequence++
				turn := b.handleAssistant(
					ctx,
					msg,
					eventStream.send,
					usage,
					fmt.Sprintf("claude-item-%d", itemSequence),
				)
				toolUseCount += turn.toolUses
				if !turn.understood {
					unreadableAssistantCount++
				}
				if streamContractErr == nil && turn.contractErr != nil {
					streamContractErr = turn.contractErr
				}
				lastAssistantText = turn.resolveFallback(lastAssistantText)
			case "user":
				itemSequence++
				turn := b.handleUser(
					ctx,
					msg,
					eventStream.send,
					fmt.Sprintf("claude-item-%d", itemSequence),
				)
				if turn.sawAsyncLaunch {
					sawAsyncLaunch = true
				}
				if streamContractErr == nil && turn.contractErr != nil {
					streamContractErr = turn.contractErr
				}
			case "system":
				if msg.Subtype == "init" && opts.ToolPolicy != ToolPolicyConnectorOnly {
					if err := checkClaudeDisabledPlugins(
						msg.Plugins,
						opts.DisabledPlugins,
					); err != nil {
						streamContractErr = err
						signalProcessGroup(cmd.Process, syscall.SIGKILL)
					} else {
						pluginsVerified = true
					}
				}
				if msg.SessionID != "" {
					sessionID = msg.SessionID
					eventStream.send(ctx, SessionReady{SessionID: sessionID})
				}
			case "result":
				sawResult = true
				finalResultText = msg.ResultText
				resultIsError = msg.IsError
				terminalReasonError = claudeTerminalReasonFailure(msg.TerminalReason, msg.ResultText)
				sessionID = msg.SessionID
				if resultUsage := claudeResultUsage(msg, opts.Model); len(resultUsage) > 0 {
					usage = resultUsage
				}
				closeStdin()
			case "log":
				if msg.Log != nil {
					b.cfg.Logger.Debug(
						"claude stream log",
						"level",
						msg.Log.Level,
						"message",
						sanitizeAgentDiagnostic(msg.Log.Message),
					)
				}
			case "control_request":
				b.handleControlRequest(msg, stdin, opts)
			}
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			// Scanner stopped consuming stdout. Close the pipe before Wait so a
			// child still writing a malformed/oversized event cannot deadlock on
			// the full OS pipe; the scanner error remains the primary failure.
			_ = stdout.Close()
		}

		closeStdin()

		// Wait for process exit, then release the cancellation handler.
		exitErr := cmd.Wait()
		close(procDone)
		duration := time.Since(startTime)
		// writeDone is buffered (cap 1) and the writer always sends — by the
		// time cmd has exited, the prompt write has either succeeded, hit a
		// broken pipe, or been unblocked by the kill that ended cmd.
		writeErr := <-writeDone

		if !pluginsVerified && streamContractErr == nil {
			streamContractErr = fmt.Errorf(
				"Claude did not report init plugin state; cannot verify disabled plugins",
			)
		}
		completionGuardError := ""
		if sawAsyncLaunch {
			completionGuardError = "claude launched an async background task; Zenith Team-managed runs require foreground execution"
		}
		finalStatus, finalOutput, finalError := finalizeStreamResult(
			"claude",
			timeout,
			runCtx.Err(),
			writeErr,
			exitErr,
			sessionID,
			streamTerminalState{
				lastAssistantText:   lastAssistantText,
				finalResultText:     finalResultText,
				sawResult:           sawResult,
				resultIsError:       resultIsError,
				scanErr:             scanErr,
				contractErr:         streamContractErr,
				terminalReasonError: terminalReasonError,
			},
			completionGuardError,
		)

		// cmd.Wait() has returned — os/exec's stderr copy goroutine has
		// observed every byte claude wrote to stderr before exiting, so
		// stderrBuf.Tail() is safe to sample now. Attach the tail to any
		// non-empty failure message; callers upstream surface this as the
		// run's error field, which is the only place users see it.
		stderrTail := stderrBuf.Tail()
		if finalError != "" {
			finalError = withAgentStderr(finalError, "claude", stderrTail)
		}
		finalError = sanitizeAgentDiagnostic(finalError)
		logStreamProtocolObservation(b.cfg.Logger, streamProtocolObservation{
			provider:                   "claude",
			cliVersion:                 b.cfg.CLIVersion,
			model:                      opts.Model,
			exitCode:                   streamProcessExitCode(exitErr),
			eventCount:                 eventCount,
			invalidEventCount:          invalidEventCount,
			assistantEventCount:        assistantEventCount,
			toolUseCount:               toolUseCount,
			sawResult:                  sawResult,
			resultIsError:              resultIsError,
			resultBytes:                len(finalResultText),
			lastAssistantBytes:         len(lastAssistantText),
			scannerError:               scanErr != nil,
			unreadableAssistantCount:   unreadableAssistantCount,
			anthropicBaseURLConfigured: strings.TrimSpace(b.cfg.Env["ANTHROPIC_BASE_URL"]) != "",
		})

		b.cfg.Logger.Info(
			"claude finished",
			"pid",
			cmd.Process.Pid,
			"status",
			finalStatus,
			"duration",
			duration.Round(time.Millisecond).String(),
		)

		// The account-binding 400 arrives in the result event (finalError);
		// "no conversation found" is printed to stderr. Check both.
		resumeRejected := resumeWasRejected(
			opts.ResumeSessionID,
			sessionID,
			finalStatus == "failed",
			finalError,
			stderrTail,
		)
		reportedSessionID := resolveSessionID(
			opts.ResumeSessionID,
			sessionID,
			finalStatus == "failed",
			finalError,
			stderrTail,
		)
		if resumeRejected {
			b.cfg.Logger.Info(
				"claude resume was rejected; dropping session id and signaling fresh-session retry",
				"requested_resume",
				opts.ResumeSessionID,
				"emitted_session",
				sessionID,
			)
		}

		if opts.RequireFinalAnswerProof && finalStatus == "completed" && sawResult && !resultIsError &&
			strings.TrimSpace(finalResultText) != "" {
			eventStream.send(ctx, CompletedItem{
				Key: "claude-terminal-answer", Role: RoleAgent,
				Parts: []Part{TextPart{Text: finalResultText, Final: true}},
			})
		}
		eventStream.send(ctx, Finished{Result: Result{
			Status:         finalStatus,
			Output:         finalOutput,
			Error:          finalError,
			DurationMs:     duration.Milliseconds(),
			SessionID:      reportedSessionID,
			Usage:          usage,
			ResumeRejected: resumeRejected,
		}})
	}()

	return eventStream.execution(), nil
}

func (b *claudeProvider) handleAssistant(
	ctx context.Context,
	msg claudeSDKMessage,
	emit executionEventEmitter,
	usage map[string]TokenUsage,
	fallbackKey string,
) assistantTurn {
	var content claudeMessageContent
	if err := json.Unmarshal(msg.Message, &content); err != nil {
		// Unreadable body: understood stays false so the caller drops any
		// fallback rather than let an older turn stand in for this one.
		return assistantTurn{contractErr: fmt.Errorf("decode assistant message: %w", err)}
	}
	if content.ContentText != nil {
		return assistantTurn{contractErr: errors.New("assistant message content must be an array")}
	}
	turn := assistantTurn{understood: true}
	var assistantText strings.Builder
	var parts []Part
	toolUseCount := 0

	// Accumulate token usage per model.
	if content.Usage != nil && content.Model != "" {
		u := usage[content.Model]
		u.InputTokens += content.Usage.InputTokens
		u.OutputTokens += content.Usage.OutputTokens
		u.CacheReadTokens += content.Usage.CacheReadInputTokens
		u.CacheWriteTokens += content.Usage.CacheCreationInputTokens
		u.RawJSON = usageFragment(claudeUsage{
			InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
			CacheReadInputTokens:     u.CacheReadTokens,
			CacheCreationInputTokens: u.CacheWriteTokens,
		})
		usage[content.Model] = u
	}

	for _, block := range content.Content {
		switch block.Type {
		case "text":
			if block.Text == "" && !block.textPresent {
				turn.understood = false
				turn.contractErr = errors.New("text block is missing text")
				continue
			}
			if block.Text != "" {
				assistantText.WriteString(block.Text)
				parts = append(parts, TextPart{Text: block.Text})
			}
		case "thinking":
			if block.Thinking == "" && block.Text == "" &&
				!block.thinkingPresent && !block.textPresent {
				turn.understood = false
				turn.contractErr = errors.New("thinking block is missing content")
				continue
			}
			thinking := block.Thinking
			if thinking == "" {
				thinking = block.Text
			}
			if thinking != "" {
				parts = append(parts, ReasoningPart{Text: thinking})
			}
		case "tool_use", "server_tool_use", "mcp_tool_use":
			toolUseCount++
			call, callErr := claudeToolCallPart(block)
			if callErr != nil {
				turn.understood = false
				turn.contractErr = callErr
				continue
			}
			parts = append(parts, call)
		case "redacted_thinking":
			// Claude deliberately withholds this block's contents. It is a known
			// no-visible-content block, not an unreadable answer: never persist
			// the provider's opaque encrypted data or invent user-facing text.
		default:
			if isClaudeToolResultType(block.Type) {
				toolUseCount++
				resultParts, resultErr := claudeToolResultParts(block)
				if resultErr != nil {
					turn.understood = false
					turn.contractErr = resultErr
					continue
				}
				parts = append(parts, resultParts...)
				continue
			}
			// A block type we do not render may be carrying the model's answer
			// in a shape we cannot read, so we must not claim this turn was
			// silent. Recognizing a new no-text block is a deliberate one-line
			// addition here, not an accident of falling through.
			turn.understood = false
			turn.contractErr = fmt.Errorf(
				"unsupported assistant content block type %q",
				block.Type,
			)
		}
	}
	turn.text = assistantText.String()
	turn.toolUses = toolUseCount
	if len(parts) > 0 && turn.contractErr == nil {
		key := content.ID
		if key == "" {
			key = fallbackKey
		}
		emit(ctx, CompletedItem{Key: key, Role: RoleAgent, Parts: parts})
	}
	return turn
}

func claudeToolCallPart(block claudeContentBlock) (ToolCallPart, error) {
	name := block.Name
	if block.Type == "mcp_tool_use" {
		if block.ServerName == "" || name == "" {
			return ToolCallPart{}, errors.New(
				"mcp_tool_use block is missing a stable id, server name, or tool name",
			)
		}
		name = block.ServerName + "/" + name
	} else if name == "" {
		name = block.ToolName
		if block.ServerName != "" && name != "" {
			name = block.ServerName + "/" + name
		}
	}
	if block.ID == "" || name == "" {
		return ToolCallPart{}, fmt.Errorf(
			"%s block is missing a stable id or tool name",
			block.Type,
		)
	}
	if len(bytes.TrimSpace(block.Input)) == 0 {
		return ToolCallPart{}, fmt.Errorf("%s block is missing an input object", block.Type)
	}
	var input map[string]any
	if err := json.Unmarshal(block.Input, &input); err != nil {
		return ToolCallPart{}, fmt.Errorf("decode %s input: %w", block.Type, err)
	}
	if input == nil {
		return ToolCallPart{}, fmt.Errorf("%s block input must be an object", block.Type)
	}
	return ToolCallPart{CallID: block.ID, Name: name, Input: input}, nil
}

func claudeToolResultParts(block claudeContentBlock) ([]Part, error) {
	if block.ToolUseID == "" {
		return nil, fmt.Errorf("%s block is missing a tool use id", block.Type)
	}
	content := bytes.TrimSpace(block.Content)
	if block.Type != "tool_result" && (len(content) == 0 || bytes.Equal(content, []byte("null"))) {
		return nil, fmt.Errorf("%s block is missing content", block.Type)
	}
	name := claudeToolResultName(block)
	status, failure := claudeToolResultStatusDetails(
		block.Type,
		block.Content,
		block.IsError,
		name,
	)
	parts := []Part{ToolResultPart{
		CallID: block.ToolUseID,
		Name:   name,
		Output: claudeToolResultOutput(block.Type, block.Content),
		Status: status,
	}}
	if failure != "" {
		parts = append(parts, ErrorPart{Message: failure})
	}
	return parts, nil
}

func isClaudeToolResultType(blockType string) bool {
	switch blockType {
	case "tool_result", "web_search_tool_result", "mcp_tool_result",
		"web_fetch_tool_result", "code_execution_tool_result",
		"bash_code_execution_tool_result", "text_editor_code_execution_tool_result",
		"tool_search_tool_result", "advisor_tool_result":
		return true
	default:
		return false
	}
}

func isClaudeToolUseType(blockType string) bool {
	switch blockType {
	case "tool_use", "server_tool_use", "mcp_tool_use":
		return true
	default:
		return false
	}
}

func claudeToolResultName(block claudeContentBlock) string {
	if block.Name != "" {
		return block.Name
	}
	switch block.Type {
	case "web_search_tool_result":
		return "web_search"
	case "web_fetch_tool_result":
		return "web_fetch"
	case "code_execution_tool_result":
		return "code_execution"
	case "bash_code_execution_tool_result":
		return "bash_code_execution"
	case "text_editor_code_execution_tool_result":
		return "text_editor_code_execution"
	case "advisor_tool_result":
		return "advisor"
	default:
		// Standard, MCP, and tool-search result blocks carry only the call id.
		// The corresponding completed call already owns the exact tool name.
		return ""
	}
}

type userTurn struct {
	sawAsyncLaunch bool
	contractErr    error
}

func (b *claudeProvider) handleUser(
	ctx context.Context,
	msg claudeSDKMessage,
	emit executionEventEmitter,
	fallbackKey string,
) userTurn {
	var content claudeMessageContent
	if err := json.Unmarshal(msg.Message, &content); err != nil {
		return userTurn{contractErr: fmt.Errorf("decode user message: %w", err)}
	}
	if content.ContentText != nil {
		return userTurn{}
	}

	turn := userTurn{}
	var parts []Part
	for _, block := range content.Content {
		if isClaudeToolUseType(block.Type) {
			call, err := claudeToolCallPart(block)
			if err != nil {
				turn.contractErr = err
				continue
			}
			parts = append(parts, call)
			continue
		}
		if isClaudeToolResultType(block.Type) {
			resultParts, err := claudeToolResultParts(block)
			if err != nil {
				turn.contractErr = err
				continue
			}
			parts = append(parts, resultParts...)
			if block.Type == "tool_result" && claudeToolResultHasAsyncLaunch(block.Content) {
				turn.sawAsyncLaunch = true
			}
			continue
		}
		if block.Type == "text" {
			if block.Text == "" && !block.textPresent {
				turn.contractErr = errors.New("user text block is missing text")
			}
			continue
		}
		turn.contractErr = fmt.Errorf("unsupported user content block type %q", block.Type)
	}
	if len(parts) > 0 && turn.contractErr == nil {
		key := content.ID
		if key == "" {
			key = fallbackKey
		}
		emit(ctx, CompletedItem{Key: key, Role: RoleTool, Parts: parts})
	}
	return turn
}

func (b *claudeProvider) handleControlRequest(
	msg claudeSDKMessage,
	stdin interface{ Write([]byte) (int, error) },
	opts ExecOptions,
) {
	var req claudeControlRequestPayload
	if err := json.Unmarshal(msg.Request, &req); err != nil {
		return
	}

	var inputMap map[string]any
	if req.Input != nil {
		_ = json.Unmarshal(req.Input, &inputMap)
	}
	if inputMap == nil {
		inputMap = map[string]any{}
	}
	allowed := opts.ToolPolicy != ToolPolicyConnectorOnly ||
		(req.Subtype == "tool_use" || req.Subtype == "can_use_tool") &&
			connectorOnlyMCPToolAllowed(opts, req.ToolName)
	if allowed && forceClaudeToolInputForeground(inputMap) {
		b.cfg.Logger.Info("claude: forced foreground tool execution",
			"request_id", msg.RequestID,
			"tool", req.ToolName,
		)
	}

	decision := map[string]any{"behavior": "deny", "message": "tool is outside the exact Connector scope"}
	if opts.ToolPolicy == ToolPolicyConnectorOnly {
		switch req.Subtype {
		case "elicitation", "mcp_elicitation":
			decision = map[string]any{"action": "decline"}
		case "request_user_dialog":
			decision = map[string]any{"behavior": "cancelled"}
		}
	}
	if allowed {
		decision = map[string]any{
			"behavior":     "allow",
			"updatedInput": inputMap,
		}
	}
	response := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": msg.RequestID,
			"response":   decision,
		},
	}

	data, err := json.Marshal(response)
	if err != nil {
		b.cfg.Logger.Warn("claude: failed to marshal control response", "error", err)
		return
	}
	data = append(data, '\n')
	if _, err := stdin.Write(data); err != nil {
		b.cfg.Logger.Warn("claude: failed to write control response", "error", err)
	}
}

func forceClaudeToolInputForeground(input map[string]any) bool {
	if runInBackground, ok := input["run_in_background"].(bool); ok && runInBackground {
		input["run_in_background"] = false
		return true
	}
	return false
}

// claudeBlockedArgs are flags controlled by the daemon, including alternative
// system-prompt flags that could override the managed runtime brief.
var claudeBlockedArgs = map[string]blockedArgMode{
	"-p":                          blockedStandalone, // non-interactive mode
	"--output-format":             blockedWithValue,  // stream-json protocol
	"--input-format":              blockedWithValue,  // stream-json protocol
	"--permission-mode":           blockedWithValue,  // bypassPermissions for autonomous operation
	"--mcp-config":                blockedWithValue,  // set by daemon from agent.mcp_config
	"--plugin-dir":                blockedWithValue,  // selected by the immutable run snapshot
	"--system-prompt":             blockedWithValue,
	"--system-prompt-file":        blockedWithValue,
	"--append-system-prompt":      blockedWithValue,
	"--append-system-prompt-file": blockedWithValue,
	// `--effort` is owned by the per-agent thinking_level picker so a
	// user-supplied custom_arg cannot silently outvote it. The daemon
	// injects --effort only when opts.ThinkingLevel is set; if a user
	// nevertheless writes it in custom_args we drop the duplicate and
	// log a warning rather than letting the CLI receive two conflicting
	// --effort values.
	"--effort": blockedWithValue,
}

func buildClaudeArgs(opts ExecOptions, systemPromptPath string, logger *slog.Logger) []string {
	if opts.ToolPolicy == ToolPolicyConnectorOnly {
		args := []string{
			"-p",
			"--output-format", "stream-json",
			"--input-format", "stream-json",
			"--verbose",
			"--permission-mode", "manual",
			"--permission-prompt-tool=stdio",
			"--tools=",
			"--strict-mcp-config",
			"--setting-sources=",
			"--disable-slash-commands",
			"--no-chrome",
			"--no-session-persistence",
			"--disallowedTools", "AskUserQuestion",
		}
		if opts.Model != "" {
			args = append(args, "--model", opts.Model)
		}
		if opts.ThinkingLevel != "" {
			args = append(args, "--effort", opts.ThinkingLevel)
		}
		if opts.MaxTurns > 0 {
			args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
		}
		if systemPromptPath != "" {
			args = append(args, "--append-system-prompt-file="+systemPromptPath)
		}
		if len(opts.ExtraArgs)+len(opts.CustomArgs) != 0 && logger != nil {
			logger.Warn(
				"custom_args: ignored for connector-only Claude execution",
				"count", len(opts.ExtraArgs)+len(opts.CustomArgs),
			)
		}
		for _, directory := range opts.PluginDirectories {
			args = append(args, "--plugin-dir="+directory)
		}
		if opts.ClaudeSettingsPath != "" {
			args = append(args, "--settings", opts.ClaudeSettingsPath)
		}
		return args
	}

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
		// AskUserQuestion is Claude Code's built-in interactive question tool.
		// The daemon runs Claude in non-interactive stream-json mode and has
		// no UI for the prompt to render in, so a call returns an empty
		// answer and the agent ends up "inferring" silently — the user
		// never sees the question. User-facing clarification belongs in a
		// conversation message instead.
		"--disallowedTools", "AskUserQuestion",
	}
	if hasManagedMcpConfig(opts.McpConfig) {
		// A saved agent-level config is authoritative, including an explicitly
		// empty object. With no managed config, omit strict mode so Claude can
		// inherit the user's local runtime MCP servers.
		args = append(args, "--strict-mcp-config")
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.ThinkingLevel != "" {
		// Slotted right after --model so the per-session effort runs
		// against the same model selection the args advertise; the CLI
		// itself accepts the flag in any order but this ordering makes
		// the launch line readable in `agent command` logs.
		args = append(args, "--effort", opts.ThinkingLevel)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}
	if systemPromptPath != "" {
		args = append(args, "--append-system-prompt-file="+systemPromptPath)
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	blockedArgs := claudeBlockedArgs
	if opts.ClaudeSettingsPath != "" {
		// The daemon-owned --settings file is the enforcement layer for disabled
		// inherited skills. Drop competing per-agent/default flags only while that
		// policy is active, then append the managed file last.
		blockedArgs = make(map[string]blockedArgMode, len(claudeBlockedArgs)+1)
		for key, mode := range claudeBlockedArgs {
			blockedArgs[key] = mode
		}
		blockedArgs["--settings"] = blockedWithValue
	}
	args = append(args, filterCustomArgs(opts.ExtraArgs, blockedArgs, logger)...)
	args = append(args, filterCustomArgs(opts.CustomArgs, blockedArgs, logger)...)
	for _, directory := range opts.PluginDirectories {
		args = append(args, "--plugin-dir="+directory)
	}
	if opts.ClaudeSkillsRoot != "" {
		args = append(args, "--add-dir", opts.ClaudeSkillsRoot)
	}
	if opts.ClaudeSettingsPath != "" {
		args = append(args, "--settings", opts.ClaudeSettingsPath)
	}
	return args
}

func writeClaudeInput(w io.Writer, prompt string) error {
	data, err := buildClaudeInput(prompt)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return nil
}

func buildClaudeInput(prompt string) ([]byte, error) {
	payload := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]string{
				{
					"type": "text",
					"text": prompt,
				},
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal claude input: %w", err)
	}
	return append(data, '\n'), nil
}

// resumeRejectedPhrases are the provider messages that positively identify a
// refused resume, as opposed to a failure that merely happened to occur during
// a resumed run.
//
// Matching must stay tight: a false positive makes the daemon throw away a
// recoverable session pointer and re-run the turn, so anything ambiguous
// belongs out of this list.
var resumeRejectedPhrases = []string{
	// Verified: claude prints this when the transcript named by --resume is
	// absent. Covered by TestResolveSessionID's existing stderr fixture.
	"no conversation found",
	// Reported verbatim against Claude Code 2.1.207 (zh-CN): "400 此 session
	// 已绑定另外的ai账号，请执行 /new 开启新 session". This is the
	// account-switch guardrail this signal exists for.
	"已绑定另外",
	// INFERRED, not yet observed: the en-US wording of the same guardrail has
	// not been captured. These are guesses derived from the zh-CN text and
	// should be corrected once a real English sample is available. A miss
	// here degrades to a terminal failure carrying the provider's raw error,
	// which is diagnosable — it does not silently mis-route the run.
	"bound to another account",
	"bound to a different account",
}

// resumeWasRejected reports whether a failed run's --resume was itself
// refused. It is the positive-evidence predicate behind Result.ResumeRejected:
// only a rejected resume can be cured by starting a fresh session.
//
// texts should include every place the provider may have written the reason —
// the final error string and the stderr tail — because the account-binding 400
// arrives in the stream-json result event while "no conversation found" is
// printed to stderr.
func resumeWasRejected(requestedResume, emitted string, failed bool, texts ...string) bool {
	if !failed || requestedResume == "" {
		return false
	}
	for _, text := range texts {
		lower := strings.ToLower(text)
		for _, phrase := range resumeRejectedPhrases {
			if strings.Contains(lower, phrase) {
				return true
			}
		}
	}
	// Claude answered with a different session than the one we asked to
	// continue: the requested transcript did not load.
	return emitted != "" && emitted != requestedResume
}

// resolveSessionID decides which session id to report on the Result. A session
// known to be dead must not be persisted as the resume pointer, so a rejected
// resume reports "" regardless of what claude echoed back.
//
// This is deliberately no longer the signal the daemon reads to decide *why*
// a run failed — Result.ResumeRejected carries that. An empty SessionID is
// also produced by any failure before the first stream message (a 401, a
// missing binary), which is why inferring "the resume was rejected" from it
// was wrong in both directions.
func resolveSessionID(requestedResume, emitted string, failed bool, texts ...string) string {
	if resumeWasRejected(requestedResume, emitted, failed, texts...) {
		return ""
	}
	return emitted
}

func claudeRootSudoPreflight(args, env []string) error {
	if !argsRequestBypassPermissions(args) || os.Geteuid() != 0 || envHasSandbox(env) {
		return nil
	}
	return fmt.Errorf(
		"Claude Code refuses bypassPermissions under root/sudo privileges. Run the Zenith Team daemon as a non-root user, or set IS_SANDBOX=1 if running in a genuine container/sandbox",
	)
}

func argsRequestBypassPermissions(args []string) bool {
	for i, arg := range args {
		if arg == "--dangerously-skip-permissions" {
			return true
		}
		if arg == "--permission-mode" && i+1 < len(args) && args[i+1] == "bypassPermissions" {
			return true
		}
	}
	return false
}

func envHasSandbox(env []string) bool {
	for i := len(env) - 1; i >= 0; i-- {
		key, value, ok := strings.Cut(env[i], "=")
		if key != "IS_SANDBOX" {
			continue
		}
		if !ok {
			return false
		}
		switch strings.ToLower(value) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	}
	return false
}

func writeClaudePrivateTempFile(directoryPattern, filename string, data []byte) (string, error) {
	dir, err := os.MkdirTemp("", directoryPattern)
	if err != nil {
		return "", fmt.Errorf("create private claude temp dir: %w", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("write private claude temp file: %w", err)
	}
	return path, nil
}

// writeMcpConfigToTemp writes MCP config JSON to a temporary file and returns
// its path. The caller is responsible for removing it via cleanupMcpConfigTemp.
func writeMcpConfigToTemp(raw json.RawMessage) (string, error) {
	return writeClaudePrivateTempFile("boardly-mcp-*", "mcp-config.json", raw)
}

func cleanupMcpConfigTemp(path string) {
	cleanupClaudePrivateTempFile(path, "boardly-mcp-")
}

func writeClaudeSystemPromptToTemp(prompt string) (string, error) {
	return writeClaudePrivateTempFile(
		"boardly-claude-prompt-*",
		"system-prompt.txt",
		[]byte(prompt),
	)
}

func cleanupClaudeSystemPromptTemp(path string) {
	cleanupClaudePrivateTempFile(path, "boardly-claude-prompt-")
}

func cleanupClaudePrivateTempFile(path, directoryPrefix string) {
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if strings.HasPrefix(filepath.Base(dir), directoryPrefix) {
		_ = os.RemoveAll(dir)
		return
	}
	_ = os.Remove(path)
}

func validateClaudeConnectorOnlySettings(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("managed settings path must be absolute and canonical")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat managed settings: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("managed settings must be a private regular file with owner-only permissions")
	}
	if info.Size() < 2 || info.Size() > 64<<10 {
		return errors.New("managed settings size is invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read managed settings: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("decode managed settings: %w", err)
	}
	for key := range root {
		if key != "disableAllHooks" && key != "enabledPlugins" && key != "permissions" {
			return fmt.Errorf("managed settings contain unexpected field %q", key)
		}
	}
	var disableAllHooks bool
	if json.Unmarshal(root["disableAllHooks"], &disableAllHooks) != nil || !disableAllHooks {
		return errors.New("managed settings must disable all hooks")
	}
	var enabledPlugins map[string]any
	if json.Unmarshal(root["enabledPlugins"], &enabledPlugins) != nil || len(enabledPlugins) != 0 {
		return errors.New("managed settings must disable inherited plugins")
	}
	var permissions map[string]json.RawMessage
	if json.Unmarshal(root["permissions"], &permissions) != nil {
		return errors.New("managed settings permissions must be an object")
	}
	for key := range permissions {
		if key != "ask" {
			return fmt.Errorf("managed settings permissions contain unexpected field %q", key)
		}
	}
	var ask []string
	if json.Unmarshal(permissions["ask"], &ask) != nil || len(ask) != 1 || ask[0] != "*" {
		return errors.New("managed settings permissions must route every tool through stdio")
	}
	return nil
}

// logWriter adapts a *slog.Logger to an io.Writer for capturing stderr.
type logWriter struct {
	logger *slog.Logger
	prefix string
}

func newLogWriter(logger *slog.Logger, prefix string) *logWriter {
	return &logWriter{logger: logger, prefix: prefix}
}

func (w *logWriter) Write(p []byte) (int, error) {
	text := strings.TrimSpace(string(p))
	if text != "" {
		w.logger.Debug(w.prefix + text)
	}
	return len(p), nil
}
