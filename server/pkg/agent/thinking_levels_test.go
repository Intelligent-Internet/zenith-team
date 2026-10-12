package agent

import (
	"strings"
	"testing"
)

func TestKnownThinkingLevelFollowsEachProviderVocabulary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		provider, level string
		want            bool
	}{
		{"claude", "", true},
		{"claude", "medium", true},
		{"claude", "xhigh", true},
		{"claude", "max", true},
		{"claude", "minimal", false},
		{"claude", "High", false},
		{"codex", "", true},
		{"codex", "minimal", true},
		{"codex", "xhigh", true},
		{"codex", "ultra", true},
		{"codex", "gpt-5_1", true},
		{"codex", strings.Repeat("a", 64), true},
		{"codex", strings.Repeat("a", 65), false},
		{"codex", "Extra High", false},
		{"codex", "-high", false},
		{"codex", "high--er", false},
		{"codex", "high-", false},
		{"hermes", "", true},
		{"hermes", "high", false},
	}
	for _, tc := range cases {
		if got := KnownThinkingLevel(tc.provider, tc.level); got != tc.want {
			t.Errorf("KnownThinkingLevel(%q, %q) = %t, want %t", tc.provider, tc.level, got, tc.want)
		}
	}
}

func TestThinkingLevelVocabularyIsACopy(t *testing.T) {
	t.Parallel()
	claude := ThinkingLevelVocabulary("claude")
	claude[0] = "tampered"
	if ThinkingLevelVocabulary("claude")[0] != "low" {
		t.Fatal("ThinkingLevelVocabulary shares its backing array with callers")
	}
	if ThinkingLevelVocabulary("unknown") != nil {
		t.Fatal("an unknown provider has no vocabulary")
	}
}
