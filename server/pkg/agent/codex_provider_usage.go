package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// extractUsageFromMap extracts token usage from a map that may contain
// "usage", "token_usage", or "tokens" fields. Handles various Codex formats.
func (c *codexClient) extractUsageFromMap(data map[string]any) {
	// Try common field names for usage data.
	var usageMap map[string]any
	for _, key := range []string{"usage", "token_usage", "tokens"} {
		if v, ok := data[key].(map[string]any); ok {
			usageMap = v
			break
		}
	}
	if usageMap == nil {
		return
	}

	c.usageMu.Lock()
	defer c.usageMu.Unlock()

	// Codex reports cached input as a prompt-token detail: cached_input_tokens
	// are included in input_tokens. Persist mutually-exclusive buckets so
	// dashboard cost math does not charge cached input twice.
	inputTokens := codexInt64(usageMap, "input_tokens", "input", "prompt_tokens")
	cacheReadTokens := codexInt64(usageMap, "cached_input_tokens", "cache_read_tokens", "cache_read_input_tokens")
	c.usage.InputTokens += codexUncachedInputTokens(inputTokens, cacheReadTokens)
	c.usage.OutputTokens += codexInt64(usageMap, "output_tokens", "output", "completion_tokens")
	reasoning := codexInt64(usageMap, "reasoning_output_tokens", "reasoning_tokens")
	if reasoning > 0 {
		value := reasoning
		if c.usage.ReasoningTokens != nil {
			value += *c.usage.ReasoningTokens
		}
		c.usage.ReasoningTokens = &value
	}
	c.usage.CacheReadTokens += cacheReadTokens
	c.usage.CacheWriteTokens += codexInt64(usageMap, "cache_write_tokens", "cache_creation_input_tokens")
	c.usage.RawJSON = usageFragment(map[string]int64{
		"input_tokens":       c.usage.InputTokens,
		"output_tokens":      c.usage.OutputTokens,
		"cache_read_tokens":  c.usage.CacheReadTokens,
		"cache_write_tokens": c.usage.CacheWriteTokens,
	})
}

func codexUncachedInputTokens(inputTokens, cachedInputTokens int64) int64 {
	uncached := inputTokens - cachedInputTokens
	if uncached < 0 {
		return 0
	}
	return uncached
}

// codexInt64 returns the first non-zero int64 value from the map for the given keys.
func codexInt64(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			if v != 0 {
				return int64(v)
			}
		case int64:
			if v != 0 {
				return v
			}
		}
	}
	return 0
}

// ── Codex session log scanner ──

// codexSessionUsage holds usage extracted from a Codex session JSONL file.
type codexSessionUsage struct {
	usage TokenUsage
	model string
}

// scanCodexSessionUsage extracts usage for threadID from its Codex rollout.
// Codex 0.144.4 embeds the app-server thread ID in both the rollout filename and
// the first session_meta item. Binding the scan to that ID prevents a
// concurrently-created rollout (for example a Codex subagent) from being billed
// to this run. A resumed rollout keeps its original date directory, so both flat
// and YYYY/MM/DD layouts are searched.
func scanCodexSessionUsage(startTime time.Time, codexHome, threadID string, resumed bool) *codexSessionUsage {
	root := codexSessionRoot(codexHome)
	if root == "" || strings.TrimSpace(threadID) == "" {
		return nil
	}

	type candidate struct {
		path    string
		modTime time.Time
	}
	var files []candidate
	for _, path := range findCodexSessionRollouts(root, threadID) {
		info, err := os.Stat(path)
		if err != nil || info.ModTime().Before(startTime) {
			continue
		}
		files = append(files, candidate{path: path, modTime: info.ModTime()})
	}
	if len(files) == 0 {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})

	// Multiple paths for one thread can transiently exist during layout migration.
	// They have the same owner, so prefer the latest deterministically without ever
	// crossing into a different thread's rollout.
	result := parseCodexSessionFileSince(files[len(files)-1].path, startTime, resumed)
	if result == nil || (result.usage.InputTokens == 0 && result.usage.OutputTokens == 0 &&
		result.usage.CacheReadTokens == 0 && result.usage.CacheWriteTokens == 0) {
		return nil
	}
	return result
}

// findCodexSessionRollouts returns uncompressed rollout files owned by threadID
// in the two layouts supported by Codex 0.14x. filepath.Glob traverses a linked
// sessions root, unlike WalkDir, which treats a symlink root as a single file and
// never visits its children. The normal path uses Codex's filename contract and
// validates session_meta.id when present. If a future Codex version changes the
// filename, the metadata pass preserves exact ownership instead of silently
// dropping usage. Filtering after globbing also keeps a provider-supplied thread
// ID out of the glob expression.
func findCodexSessionRollouts(root, threadID string) []string {
	threadID = strings.TrimSpace(threadID)
	if root == "" || threadID == "" {
		return nil
	}

	patterns := []string{
		filepath.Join(root, "rollout-*.jsonl"),
		filepath.Join(root, "*", "*", "*", "rollout-*.jsonl"),
	}
	seen := make(map[string]bool)
	var candidates []string
	for _, pattern := range patterns {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, path := range paths {
			if seen[path] {
				continue
			}
			seen[path] = true
			candidates = append(candidates, path)
		}
	}

	// Fast path for the current Codex contract. Reject a filename match if its
	// canonical session_meta explicitly names a different thread.
	suffix := "-" + threadID + ".jsonl"
	var matches []string
	for _, path := range candidates {
		if !strings.HasSuffix(filepath.Base(path), suffix) {
			continue
		}
		if metadataID, ok := readCodexRolloutThreadID(path); ok && metadataID != threadID {
			continue
		}
		matches = append(matches, path)
	}
	if len(matches) > 0 {
		return matches
	}

	// Compatibility path for a future filename format: the first session_meta
	// remains the canonical owner of a rollout in Codex's own resume reader.
	for _, path := range candidates {
		if metadataID, ok := readCodexRolloutThreadID(path); ok && metadataID == threadID {
			matches = append(matches, path)
		}
	}
	return matches
}

// readCodexRolloutThreadID reads only the head of a rollout. Codex writes the
// canonical session_meta first; the line cap prevents a malformed legacy file
// without metadata from turning ownership checks into a full multi-GB scan.
func readCodexRolloutThreadID(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	var evt struct {
		Type    string `json:"type"`
		Payload *struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for lineCount := 0; lineCount < 64 && scanner.Scan(); lineCount++ {
		line := scanner.Bytes()
		if !bytesContainsStr(line, "session_meta") {
			continue
		}
		if err := json.Unmarshal(
			line,
			&evt,
		); err == nil && evt.Type == "session_meta" && evt.Payload != nil &&
			evt.Payload.ID != "" {
			return evt.Payload.ID, true
		}
	}
	return "", false
}

// codexSessionRoot returns the Codex sessions directory. It prefers the
// explicit per-run codexHome the backend is running with (so usage is read
// from the same run-local sessions Codex actually wrote to), then the ambient
// CODEX_HOME, then ~/.codex.
func codexSessionRoot(codexHome string) string {
	if codexHome = strings.TrimSpace(codexHome); codexHome == "" {
		codexHome = os.Getenv("CODEX_HOME")
	}
	if codexHome != "" {
		dir := filepath.Join(codexHome, "sessions")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	dir := filepath.Join(home, ".codex", "sessions")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

type codexRawTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheReadInputTokens  int64 `json:"cache_read_input_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// codexSessionTokenCount represents a token_count event in Codex JSONL.
type codexSessionTokenCount struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   *struct {
		Type string `json:"type"`
		Info *struct {
			TotalTokenUsage *codexRawTokenUsage `json:"total_token_usage"`
			LastTokenUsage  *codexRawTokenUsage `json:"last_token_usage"`
			Model           string              `json:"model"`
		} `json:"info"`
		Model string `json:"model"`
	} `json:"payload"`
}

// parseCodexSessionFile extracts the final token_count from a Codex session file.
func parseCodexSessionFile(path string) *codexSessionUsage {
	return parseCodexSessionFileSince(path, time.Time{}, false)
}

// parseCodexSessionFileSince extracts usage accumulated after startTime. Codex
// reports total_token_usage cumulatively for the whole resumed session, so the
// last total before startTime is subtracted from the final total. Timestamp-less
// events in a resumed rollout are baseline-only until an explicit post-start
// timestamp establishes the boundary; fresh sessions retain the previous
// whole-file behavior because every event belongs to the new run.
func parseCodexSessionFileSince(path string, startTime time.Time, resumed bool) *codexSessionUsage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var result codexSessionUsage
	var previousTotal, accumulated, finalUsage codexRawTokenUsage
	previousTotalFound := false
	finalUsageFound := false
	afterStartBoundary := false

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()

		// Fast pre-filter.
		if !bytesContainsStr(line, "token_count") && !bytesContainsStr(line, "turn_context") {
			continue
		}

		var evt codexSessionTokenCount
		if err := json.Unmarshal(line, &evt); err != nil || evt.Payload == nil {
			continue
		}
		timestampAfterStart := !startTime.IsZero() && !evt.Timestamp.IsZero() && evt.Timestamp.After(startTime)
		if timestampAfterStart {
			afterStartBoundary = true
		}

		// Track model from turn_context events.
		if evt.Type == "turn_context" && evt.Payload.Model != "" {
			result.model = evt.Payload.Model
			continue
		}

		// Extract token usage from token_count events.
		if evt.Payload.Type == "token_count" && evt.Payload.Info != nil {
			afterStart := startTime.IsZero() || timestampAfterStart ||
				(evt.Timestamp.IsZero() && (!resumed || afterStartBoundary))
			if usage := evt.Payload.Info.TotalTokenUsage; usage != nil {
				current := normalizeCodexRawTokenUsage(*usage)
				if afterStart {
					delta := current
					if previousTotalFound {
						delta = subtractCodexRawTokenUsage(current, previousTotal)
					}
					accumulated = addCodexRawTokenUsage(accumulated, delta)
					finalUsage = accumulated
					finalUsageFound = true
				}
				previousTotal = current
				previousTotalFound = true
			} else if usage := evt.Payload.Info.LastTokenUsage; usage != nil && afterStart {
				// Preserve event order: a later last_token_usage is the same
				// fallback the old whole-file parser would have selected.
				finalUsage = normalizeCodexRawTokenUsage(*usage)
				finalUsageFound = true
			}
			if evt.Payload.Info.Model != "" {
				result.model = evt.Payload.Info.Model
			}
		}
	}

	if !finalUsageFound {
		return nil
	}
	cachedTokens := finalUsage.CachedInputTokens
	result.usage = TokenUsage{
		InputTokens:     codexUncachedInputTokens(finalUsage.InputTokens, cachedTokens),
		OutputTokens:    finalUsage.OutputTokens,
		CacheReadTokens: cachedTokens,
		ReasoningTokens: codexOptionalInt64(finalUsage.ReasoningOutputTokens),
		UsageBasis:      UsageBasisPerRun,
		RawJSON:         usageFragment(finalUsage),
	}
	return &result
}

func subtractCodexRawTokenUsage(total, baseline codexRawTokenUsage) codexRawTokenUsage {
	total = normalizeCodexRawTokenUsage(total)
	baseline = normalizeCodexRawTokenUsage(baseline)
	// Guard each counter independently. Treating one field's reset as a reset of
	// the entire snapshot would re-report still-monotonic fields and recreate the
	// over-counting this fallback is meant to prevent.
	return codexRawTokenUsage{
		InputTokens:       nonNegativeTokenDelta(total.InputTokens, baseline.InputTokens),
		OutputTokens:      nonNegativeTokenDelta(total.OutputTokens, baseline.OutputTokens),
		CachedInputTokens: nonNegativeTokenDelta(total.CachedInputTokens, baseline.CachedInputTokens),
		ReasoningOutputTokens: nonNegativeTokenDelta(
			total.ReasoningOutputTokens,
			baseline.ReasoningOutputTokens,
		),
	}
}

func normalizeCodexRawTokenUsage(usage codexRawTokenUsage) codexRawTokenUsage {
	if usage.CachedInputTokens == 0 {
		usage.CachedInputTokens = usage.CacheReadInputTokens
	}
	usage.CacheReadInputTokens = 0
	return usage
}

func addCodexRawTokenUsage(a, b codexRawTokenUsage) codexRawTokenUsage {
	return codexRawTokenUsage{
		InputTokens:           a.InputTokens + b.InputTokens,
		OutputTokens:          a.OutputTokens + b.OutputTokens,
		CachedInputTokens:     a.CachedInputTokens + b.CachedInputTokens,
		ReasoningOutputTokens: a.ReasoningOutputTokens + b.ReasoningOutputTokens,
	}
}

func nonNegativeTokenDelta(total, baseline int64) int64 {
	if total < baseline {
		// A counter reset means the final value already belongs to the new span.
		return total
	}
	return total - baseline
}

// bytesContainsStr checks if b contains the string s (without allocating).
func bytesContainsStr(b []byte, s string) bool {
	return strings.Contains(string(b), s)
}
