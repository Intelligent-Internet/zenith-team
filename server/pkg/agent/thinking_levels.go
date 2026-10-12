package agent

import "strings"

// Thinking levels are a provider's vocabulary, not the Agent's. Claude Code
// takes one fixed effort set, so the API can name every accepted value.
// Codex reports its levels per model from the daemon-local catalog
// (`supported_reasoning_levels`), which gains tokens without a server
// release, so the API can only fence the shape of a level and leaves the
// exact per-model check to discovery and the daemon that runs the model.
var (
	claudeThinkingLevels = []string{"low", "medium", "high", "xhigh", "max"}
	codexThinkingLevels  = []string{"none", "minimal", "low", "medium", "high", "xhigh"}
)

// ThinkingLevelVocabulary lists the levels documented for a provider. For
// Codex it is the documented catalog, not an exhaustive accept-list: see
// KnownThinkingLevel. Unknown providers have no vocabulary.
func ThinkingLevelVocabulary(provider string) []string {
	switch provider {
	case "claude":
		return append([]string(nil), claudeThinkingLevels...)
	case "codex":
		return append([]string(nil), codexThinkingLevels...)
	default:
		return nil
	}
}

// KnownThinkingLevel reports whether a runtime of the given provider could
// accept the level. An empty level is the provider default and always
// passes. Claude Code checks the fixed set; Codex checks that the value is a
// well-formed catalog token (lowercase letters and digits with single
// separators, at most 64 bytes) because the exact set belongs to the model.
func KnownThinkingLevel(provider, level string) bool {
	if level == "" {
		return true
	}
	switch provider {
	case "claude":
		for _, known := range claudeThinkingLevels {
			if known == level {
				return true
			}
		}
		return false
	case "codex":
		return wellFormedThinkingToken(level)
	default:
		return false
	}
}

func wellFormedThinkingToken(level string) bool {
	if len(level) > 64 || strings.HasPrefix(level, "-") || strings.HasPrefix(level, "_") ||
		strings.HasSuffix(level, "-") || strings.HasSuffix(level, "_") {
		return false
	}
	previousSeparator := false
	for _, r := range level {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			previousSeparator = false
		case r == '-' || r == '_':
			if previousSeparator {
				return false
			}
			previousSeparator = true
		default:
			return false
		}
	}
	return true
}
