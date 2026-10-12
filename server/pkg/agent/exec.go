package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"

	"boardly.dev/boardly/server/pkg/redact"
)

// This file holds the subprocess plumbing shared by both adapters: child env
// assembly, custom-arg filtering, the bounded stderr tail, and the stream
// scanner. Process-group control lives in exec_unix.go and exec_windows.go.
// Everything protocol-specific
// lives in the per-provider files (claude_provider*.go, codex_provider*.go).

func sendProviderMessage(
	ctx context.Context,
	ch chan<- providerMessage,
	msg providerMessage,
) bool {
	if msg.Type == providerMessageItemDelta {
		select {
		case ch <- msg:
		default:
		}
		return true
	}
	if providerMessageNeedsProcessingAcknowledgement(msg.Type) {
		msg.processed = newAcknowledgement()
	}
	// Completed normalized items and tool lifecycle observations must not run
	// ahead of the consumer. Session/log messages keep ordinary channel
	// backpressure but need no second processing acknowledgement.
	select {
	case ch <- msg:
	case <-ctx.Done():
		return false
	}
	if msg.processed == nil {
		return true
	}
	select {
	case <-msg.processed.done:
		return true
	case <-ctx.Done():
		return false
	}
}

func providerMessageNeedsProcessingAcknowledgement(messageType providerMessageType) bool {
	switch messageType {
	case providerMessageText,
		providerMessageThinking,
		providerMessageToolUse,
		providerMessageToolResult,
		providerMessageError,
		providerMessageCompletedItem:
		return true
	default:
		return false
	}
}

func buildEnv(extra map[string]string) []string {
	return mergeEnv(os.Environ(), extra)
}

func mergeEnv(base []string, extra map[string]string) []string {
	env := make([]string, 0, len(base)+len(extra))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		// BOARDLY_* in the daemon's own environment is not run context. Drop
		// the inherited namespace for every backend and append only the values
		// the daemon explicitly assembled for this run below.
		if isFilteredChildEnvKey(key) || strings.HasPrefix(strings.ToUpper(key), "BOARDLY_") {
			continue
		}
		env = append(env, entry)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// isFilteredChildEnvKey reports whether an inherited env var is an internal
// Claude Code runtime/session marker that must NOT leak into the spawned child
// (otherwise the child mistakes itself for a nested or resumed session, or
// inherits the parent's exec path / transport).
//
// It must NOT strip the user-facing CLAUDE_CODE_* configuration namespace
// (CLAUDE_CODE_GIT_BASH_PATH, CLAUDE_CODE_USE_BEDROCK, CLAUDE_CODE_USE_VERTEX,
// CLAUDE_CODE_MAX_OUTPUT_TOKENS, CLAUDE_CODE_TMPDIR, ...): users set those
// deliberately and the child needs them. Blanket-stripping the whole prefix is
// what broke Windows — CLAUDE_CODE_GIT_BASH_PATH was silently removed, so Claude
// Code could not find bash.exe and exited immediately. Strip internal markers by
// exact name and let every other CLAUDE_CODE_* var through.
//
// The denylist holds only undocumented, per-process runtime markers. Anything in
// the public env-vars reference (https://code.claude.com/docs/en/env-vars) is
// user config and stays out of this list — including CLAUDE_CODE_TMPDIR, a
// documented temp-dir override under which Claude Code creates its own
// per-session subdir, so inheriting it is harmless.
func isFilteredChildEnvKey(key string) bool {
	switch key {
	case "CLAUDECODE", // "1" when running inside Claude Code
		"CLAUDE_CODE_ENTRYPOINT", // entrypoint marker (cli/sdk-cli/...)
		"CLAUDE_CODE_EXECPATH",   // path to the running CLI binary
		"CLAUDE_CODE_SESSION_ID", // per-session identifier
		"CLAUDE_CODE_SSE_PORT":   // IDE-extension transport port
		return true
	}
	// CLAUDECODE_* (no underscore between CLAUDE and CODE) is wholly internal;
	// keep stripping it. The user-facing config namespace is CLAUDE_CODE_*.
	return strings.HasPrefix(key, "CLAUDECODE_")
}

// blockedArgMode specifies whether a blocked arg takes a value or is standalone.
type blockedArgMode int

const (
	blockedWithValue     blockedArgMode = iota // flag takes a value (next arg or =value)
	blockedStandalone                          // flag is boolean, no value
	blockedOptionalValue                       // flag may take the next non-flag arg or =value
)

// filterCustomArgs removes protocol-critical flags from user-configured custom
// args to prevent breaking daemon↔agent communication. Each backend defines its
// own blocked set (the flags it hardcodes). This is intentionally narrow — we
// only block args that would break the communication protocol, not every
// possible dangerous flag. Workspace members are trusted to configure agents
// sensibly, same as with custom_env.
//
// Shell quoting is stripped from each arg before processing: users commonly
// type custom_args in config fields using shell syntax (e.g.
// --deny-tool='write'). Since the daemon spawns processes directly without a
// shell, those quotes would otherwise be passed literally to the child process,
// which typically rejects them as unrecognized flag values.
func filterCustomArgs(args []string, blocked map[string]blockedArgMode, logger *slog.Logger) []string {
	if len(args) == 0 {
		return args
	}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		raw := args[i]
		arg := unshellQuoteArg(raw)
		flag := arg
		hasInlineValue := false
		if idx := strings.Index(arg, "="); idx > 0 {
			flag = arg[:idx]
			hasInlineValue = true
		}
		mode, isBlocked := blocked[flag]
		if isBlocked {
			logger.Warn("custom_args: blocked protocol-critical flag, skipping", "flag", flag)
			if mode == blockedWithValue && !hasInlineValue {
				// The next arg is the value for this flag — skip it too.
				i++
			} else if mode == blockedOptionalValue && !hasInlineValue && i+1 < len(args) &&
				!strings.HasPrefix(unshellQuoteArg(args[i+1]), "-") {
				// Optional values are consumed only when the next token is not
				// another flag, so a boolean form cannot swallow an unrelated
				// option.
				i++
			}
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

// unshellQuoteArg strips a single layer of shell-style single or double quotes
// from an argument. It handles two forms:
//
//   - --flag='value' or --flag="value" → --flag=value
//   - 'standalone' or "standalone"     → standalone
//
// Only flag-style args (`-x=…`, `--flag=…`) get inline value unquoting. Plain
// assignment syntax like `model="o3"` is left alone because the quotes may be
// semantic for the child process (for example Codex `-c model="o3"`). Only
// matching outer quotes are stripped; no escape processing is done.
func unshellQuoteArg(arg string) string {
	if strings.HasPrefix(arg, "-") {
		if idx := strings.Index(arg, "="); idx > 0 {
			value := arg[idx+1:]
			if unquoted, ok := stripSurroundingQuotes(value); ok {
				return arg[:idx+1] + unquoted
			}
			return arg
		}
	}
	if unquoted, ok := stripSurroundingQuotes(arg); ok {
		return unquoted
	}
	return arg
}

// stripSurroundingQuotes removes a matching outer pair of single or double
// quotes from s and returns (unquoted, true). Returns (s, false) if s does not
// start and end with the same quote character.
func stripSurroundingQuotes(s string) (string, bool) {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1], true
		}
	}
	return s, false
}

// hasManagedMcpConfig preserves the API's three-state MCP semantics. Only SQL
// NULL / JSON null mean "inherit the runtime configuration"; any object,
// including an explicitly empty one, is a managed set and enables strict mode.
func hasManagedMcpConfig(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// ── Stream scanner ──

// agentStreamMaxLineBytes bounds a single line read from an agent CLI's
// event stream. Agent transports are line-delimited JSON, and one line can
// carry a whole conversation: Codex app-server serializes an entire thread
// into the single `thread/resume` response, and providers that embed a large
// tool result in one event grow the same way. Crossing this bound makes
// scanner.Scan() return false with bufio.ErrTooLong, which the backends can
// only report as a transport failure — the session itself is fine, we just
// cannot read it.
//
// 32 MiB was picked after a previous 10 MiB bound broke `thread/resume` for
// long Codex threads. It is headroom, not a guarantee: a thread can still
// outgrow any fixed cap, so the recovery path matters more than the number.
const agentStreamMaxLineBytes = 32 * 1024 * 1024

// agentStreamInitialBufferBytes is the scanner's starting allocation. Lines
// above it still grow up to agentStreamMaxLineBytes; this only decides how
// much is reserved before the first grow, so ordinary events never realloc.
const agentStreamInitialBufferBytes = 1024 * 1024

// newAgentStreamScanner returns a bufio.Scanner sized for agent event
// streams. Every backend reading a line-delimited agent transport must go
// through this constructor: the bound used to be copy-pasted per backend and
// silently drifted, which is how an earlier overflow fix reached only one of
// them.
func newAgentStreamScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, agentStreamInitialBufferBytes), agentStreamMaxLineBytes)
	return scanner
}

// ── Bounded stderr tail ──

// agentStderrTailBytes bounds the stderr tail captured for inclusion in
// error messages when an agent CLI exits before emitting a structured
// error (e.g. V8 abort, Bun panic, OOM). Large enough to contain typical
// CLI error lines, small enough to stay sensible inside a run-level
// Result.Error string.
const agentStderrTailBytes = 2048

var (
	agentAuthorizationHeaderRe = regexp.MustCompile(`(?im)(authorization\s*:\s*)[^\r\n]+`)
	agentJSONSecretRe          = regexp.MustCompile(
		`(?i)("(?:token|auth|authorization|api[_-]?key|secret|password)"\s*:\s*)"(?:\\.|[^"\\])*"`,
	)
	agentDiagnosticSecretRe = regexp.MustCompile(
		`(?i)(authorization|auth|api[_-]?key|token|secret|password)(\s*[:=]\s*)([^\s,;]+)`,
	)
)

// sanitizeAgentDiagnostic removes terminal control characters, common secret
// shapes, and local home-directory details before a child-process diagnostic is
// persisted in Result.Error. stderr is still forwarded to the local daemon log;
// this helper protects the run row and user-visible failure message.
func sanitizeAgentDiagnostic(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
	value = agentAuthorizationHeaderRe.ReplaceAllString(value, `$1[REDACTED]`)
	value = agentJSONSecretRe.ReplaceAllString(value, `$1"[REDACTED]"`)
	value = agentDiagnosticSecretRe.ReplaceAllString(value, `$1$2[REDACTED]`)
	return redact.Diagnostic(value)
}

// stderrTail forwards writes to an inner writer (typically the daemon's
// log) while also retaining a bounded tail of the bytes written. Consumers
// call Tail() to include that context in error messages when the agent
// process exits before it emits a structured error — otherwise all the
// user sees is "exit status N", with the real reason stuck in daemon logs.
//
// All backends that supervise a child CLI process should wire their
// cmd.Stderr through this type, and on failure include Tail() in
// Result.Error via withAgentStderr. That makes root-causing CLI crashes
// possible without having to crawl the daemon host's log files.
type stderrTail struct {
	inner io.Writer
	max   int

	mu    sync.Mutex
	buf   []byte
	total int64
}

func newStderrTail(inner io.Writer, tailBytes int) *stderrTail {
	if tailBytes <= 0 {
		tailBytes = agentStderrTailBytes
	}
	return &stderrTail{inner: inner, max: tailBytes}
}

func (s *stderrTail) Write(p []byte) (int, error) {
	if _, err := s.inner.Write(p); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.total += int64(len(p))
	s.buf = append(s.buf, p...)
	if len(s.buf) > s.max {
		s.buf = s.buf[len(s.buf)-s.max:]
	}
	s.mu.Unlock()
	return len(p), nil
}

func (s *stderrTail) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// Tail returns the captured stderr as valid UTF-8 with leading/trailing
// whitespace trimmed. A byte-bounded tail can start or end partway through a
// multi-byte rune, so invalid fragments are discarded before the diagnostic is
// persisted. The inner writer still receives every original byte verbatim.
// Empty string means nothing was written or no valid non-whitespace text was
// captured.
func (s *stderrTail) Tail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(strings.ToValidUTF8(string(s.buf), ""))
}

// withAgentStderr appends a stderr tail hint to an error message when
// non-empty, otherwise returns msg unchanged. The tail is prefixed with a
// short label so the composed string stays readable even when the original
// msg is already verbose.
func withAgentStderr(msg, label, tail string) string {
	msg = sanitizeAgentDiagnostic(msg)
	tail = sanitizeAgentDiagnostic(tail)
	if tail == "" {
		return msg
	}
	return msg + "; " + label + " stderr: " + tail
}

// extractVersionLine pulls the version line out of a `<cli> --version` capture,
// discarding leading shell noise. npm-installed CLI shims can emit `chcp`-style
// output like `Active code page: 65001` before the real version reaches
// stdout, and the raw concatenation was being persisted as the runtime
// version.
//
// The heuristic: return the first non-empty line that contains a semver-shaped
// token (matches versionRe). Full version strings like "2.1.5 (Claude Code)"
// or "codex-cli 0.118.0" survive unchanged because the whole matching line is
// returned. If no line carries a semver token, fall back to the trimmed raw
// output so unusual version formats aren't silently dropped to empty.
func extractVersionLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if versionRe.MatchString(line) {
			return line
		}
	}
	return strings.TrimSpace(raw)
}
