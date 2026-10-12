// Package redact removes credentials from product content and additionally
// masks email addresses in diagnostics. Contact addresses remain readable in
// agent replies, tool input, and tool output.
//
// It is the one versioned redactor shared by every layer that handles
// agent-authored text: the daemon pipeline redacts local previews and
// cloud-bound semantic events first (exact Run capability tokens layered over
// the ruleset via Redactor), and the server applies the same
// ruleset again defensively before committing canonical Run events, terminal
// Result.Output messages, and Agent/MCP-posted product Messages.
//
// Redaction is deterministic and idempotent — Redact(Redact(x)) equals
// Redact(x) — so stacked application across the daemon and every
// server-before-commit seam never double-mangles content, and exact replays
// of a redacted payload stay exact. Placeholders carry the rule id (for
// example "[redacted:aws-key]"). Version identifies the ruleset; any rule
// change bumps it.
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// rulesetVersion identifies the exact shared ruleset below. Every producer
// and every server-before-commit seam applies the same version, so double
// application is always a no-op. Any change to the rules — identifiers,
// patterns, placeholders, or order — must bump this constant and update the
// fingerprint pinned in redact_test.go in the same commit.
const (
	rulesetVersion     = 4
	maxDiagnosticBytes = 32 << 10
)

// Version returns the version of the shared ruleset. Producers that want to
// attribute redaction record this value next to redacted payloads.
func Version() int { return rulesetVersion }

// placeholder builds the deterministic replacement carrying one rule id.
// Placeholder texts are closed under the ruleset: redacting a placeholder
// (or any concatenation of placeholders and safe text) never changes it,
// which is what makes Redact idempotent.
func placeholder(id string) string { return "[redacted:" + id + "]" }

type rule struct {
	id          string
	pattern     *regexp.Regexp
	replacement string
}

func newRule(id, pattern string) rule {
	return rule{id: id, pattern: regexp.MustCompile(pattern), replacement: placeholder(id)}
}

func newTemplateRule(id, pattern, replacement string) rule {
	return rule{id: id, pattern: regexp.MustCompile(pattern), replacement: replacement}
}

// kvKeyPattern captures one same-line key whose name carries a secret-ish
// keyword, its optional closing quote, and the =/: separator.
const kvKeyPattern = `(?i)([A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|apikey|access[_-]?key)[A-Za-z0-9_.-]*["'` + "`" + `]?[ \t]*[:=][ \t]*)`

// jsonSensitiveKeyPattern is intentionally narrower than kvKeyPattern: token
// counters such as inputTokens and outputTokens are ordinary telemetry, while
// a singular token or a password/secret/key-bearing field owns credential
// material and is replaced as a whole regardless of the value's shape.
var jsonSensitiveKeyPattern = regexp.MustCompile(
	`(?i)(?:password|passwd|secret|api[_-]?key|apikey|access[_-]?key|authorization|set[_-]?cookie|private[_-]?key|credential|token$)`,
)

// credentialRules is the leading part of the versioned ruleset that matches
// only credential formats: armored keys, exact token shapes, and the bearer
// and URL-userinfo carriers. None of them fires on ordinary prose, which is
// what lets Credentials run on human-authored text.
var credentialRules = []rule{
	// PEM-armored key and certificate blocks, including multi-line bodies.
	newRule("pem-block", `(?s)-----BEGIN [A-Z0-9 ]+-----.*?-----END [A-Z0-9 ]+-----`),
	// AWS access key IDs.
	newRule("aws-key", `\bAKIA[0-9A-Z]{16}\b`),
	// GitHub classic and installation tokens (ghp/gho/ghu/ghs/ghr).
	newRule("github-token", `\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,255}\b`),
	// GitHub fine-grained personal access tokens.
	newRule("github-pat", `\bgithub_pat_[A-Za-z0-9_]{20,255}\b`),
	// Slack bot/app/personal/refresh tokens.
	newRule("slack-token", `\bxox[abprs]-[A-Za-z0-9-]{8,}\b`),
	// Google API keys.
	newRule("google-api-key", `\bAIza[0-9A-Za-z_-]{35}\b`),
	// OpenAI/Anthropic-style secret keys (sk-, sk-proj-, sk-ant-…).
	newRule("sk-key", `\bsk-[A-Za-z0-9_-]{20,}\b`),
	// JWT triplets: two base64url segments starting with "ey" plus signature.
	newRule("jwt", `\bey[A-Za-z0-9_-]{8,}\.ey[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{8,}`),
	// Bearer authorization values on one line.
	newTemplateRule("bearer-token", `(?i)\bbearer[ \t]+[A-Za-z0-9._~+/=-]{8,}`,
		"Bearer "+placeholder("bearer-token")),
	// Credentials embedded in URL userinfo (scheme://user:password@host).
	newTemplateRule("url-credentials", `\b([A-Za-z][A-Za-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`,
		"${1}"+placeholder("url-credentials")+"@"),
}

// rules is the ordered versioned ruleset. Order is part of the version:
// multi-line armors first, then exact credential shapes, then carrier
// patterns (bearer/URL userinfo/key=value). Every replacement is
// deterministic, so replays and retries of the
// same input redact to the same bytes at every layer.
var rules = append(credentialRules[:len(credentialRules):len(credentialRules)],
	// Generic same-line key/value secrets (password/token/secret/api_key…).
	// Quoted values keep their quote style so the rewritten text stays a
	// fixed point; the unquoted form consumes a maximal run whose boundary
	// characters are outside the value alphabet for the same reason.
	newTemplateRule("kv-secret",
		kvKeyPattern+`"[^"\r\n]*"`, `${1}"`+placeholder("kv-secret")+`"`),
	newTemplateRule("kv-secret",
		kvKeyPattern+`'[^'\r\n]*'`, `${1}'`+placeholder("kv-secret")+`'`),
	newTemplateRule("kv-secret",
		kvKeyPattern+`[^\s"',;]+`, "${1}"+placeholder("kv-secret")),
)

// Contact addresses are product content, but diagnostics still mask them.
var diagnosticEmailRule = newRule("email", `\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)

// placeholderTexts is the closed set of replacement texts the ruleset and
// literal layering can introduce. Literal guards use it to refuse literals
// whose replacement could re-trigger a rewrite, keeping Redact idempotent.
var placeholderTexts = buildPlaceholderTexts()

func buildPlaceholderTexts() []string {
	texts := make([]string, 0, len(rules)+2)
	for _, rule := range rules {
		texts = append(texts, rule.replacement)
	}
	return append(texts, diagnosticEmailRule.replacement, placeholder(literalRuleID), placeholder("json-secret"))
}

// Redact applies the shared versioned ruleset while preserving email addresses.
// It is idempotent:
// Redact(Redact(x)) == Redact(x), so the daemon-first pass and every
// server-before-commit pass may stack safely.
func Redact(text string) string {
	for _, rule := range rules {
		text = rule.pattern.ReplaceAllString(text, rule.replacement)
	}
	return text
}

// Credentials removes only credential formats — PEM blocks, AWS, GitHub,
// Slack, Google and sk- keys, JWTs, bearer values and URL userinfo — from
// human-authored prose. Unlike Redact it keeps words such as "Password:" or
// "Tokenization:", which are ordinary content in a
// handbook. It shares the versioned patterns and placeholders, so it is
// idempotent and Redact(Credentials(x)) == Redact(x).
func Credentials(text string) string {
	for _, rule := range credentialRules {
		text = rule.pattern.ReplaceAllString(text, rule.replacement)
	}
	return text
}

// Diagnostic applies the shared rules, masks email addresses, and bounds one
// diagnostic so it fits comfortably inside daemon terminal request frames. The
// truncation marker is part of the bound and the result is valid UTF-8 and
// idempotent.
func Diagnostic(text string) string {
	text = JSONOrText(strings.ToValidUTF8(text, ""))
	text = diagnosticEmailRule.pattern.ReplaceAllString(text, diagnosticEmailRule.replacement)
	const marker = "\n[truncated:diagnostic]"
	if len(text) <= maxDiagnosticBytes {
		return text
	}
	limit := maxDiagnosticBytes - len(marker)
	for limit > 0 && !utf8.ValidString(text[:limit]) {
		limit--
	}
	return text[:limit] + marker
}

// JSON redacts every string value inside one JSON document while preserving
// structure, keys, numbers, and validity. Documents that fail to decode are
// returned unchanged so the caller's own validation still rejects them, and
// untouched documents keep their exact input bytes. Re-encoded output is
// deterministic (sorted object keys, no HTML escaping), so replays of the
// same input produce identical bytes.
func JSON(raw []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return raw
	}
	redacted, changed := redactJSONValue(value)
	if !changed {
		return raw
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(redacted); err != nil {
		return raw
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
}

// JSONOrText redacts a tool-result string with JSON key awareness when the
// whole value is valid JSON, and otherwise applies the ordinary text rules.
// Valid JSON is canonicalized so daemon and server defense passes produce the
// same stable bytes for transcript replay and digesting.
func JSONOrText(text string) string {
	raw := []byte(text)
	if !json.Valid(raw) {
		return Redact(text)
	}
	redacted := JSON(raw)
	decoder := json.NewDecoder(bytes.NewReader(redacted))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return Redact(text)
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return Redact(text)
	}
	return string(bytes.TrimSuffix(buffer.Bytes(), []byte("\n")))
}

func redactJSONValue(value any) (any, bool) {
	switch value := value.(type) {
	case string:
		redacted := Redact(value)
		return redacted, redacted != value
	case map[string]any:
		changed := false
		for key, item := range value {
			if jsonSensitiveKeyPattern.MatchString(key) {
				replacement := placeholder("json-secret")
				if text, ok := item.(string); !ok || text != replacement {
					value[key] = replacement
					changed = true
				}
				continue
			}
			redacted, itemChanged := redactJSONValue(item)
			if itemChanged {
				value[key] = redacted
				changed = true
			}
		}
		return value, changed
	case []any:
		changed := false
		for index, item := range value {
			redacted, itemChanged := redactJSONValue(item)
			if itemChanged {
				value[index] = redacted
				changed = true
			}
		}
		return value, changed
	default:
		return value, false
	}
}
