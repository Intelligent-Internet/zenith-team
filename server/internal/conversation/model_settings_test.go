package conversation

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeModelSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, provider, model, thinking string
		invalid                         bool
	}{
		{"codex", "codex", " model ", " high ", false},
		{"provider defaults", "codex", "", "", false},
		{"claude", "claude", "sonnet", "max", false},
		{"foreign reasoning", "claude", "sonnet", "minimal", true},
		{"malformed reasoning", "codex", "model", "bad token", true},
		{"oversized model", "codex", strings.Repeat("a", 257), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := ModelSettingsInput{AgentID: uuid.New(), ExpectedRevision: 1}
			if tc.model != "" {
				in.Model = &tc.model
			}
			if tc.thinking != "" {
				in.ThinkingLevel = &tc.thinking
			}
			got, err := normalizeModelSettings(in, tc.provider)
			if (err != nil) != tc.invalid {
				t.Fatalf("error = %v, invalid = %t", err, tc.invalid)
			}
			if !tc.invalid && got.Model != nil && *got.Model != strings.TrimSpace(tc.model) {
				t.Fatalf("model = %q", *got.Model)
			}
		})
	}
}
