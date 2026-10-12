package runfailure

import (
	"strings"
	"testing"
	"unicode/utf8"

	"boardly.dev/boardly/server/pkg/protocol"
)

func TestDetailKeepsTheProviderSentence(t *testing.T) {
	t.Parallel()

	raw := "You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage " +
		"to purchase more credits or try again at Sep 22nd, 2026 11:27 AM."
	if got := Detail(raw); got != raw {
		t.Fatalf("Detail(short provider text) = %q, want it unchanged", got)
	}
}

func TestDetailNormalizesShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "   ", want: ""},
		{name: "capitalizes and terminates", raw: "runtime went offline", want: "Runtime went offline."},
		{
			name: "collapses whitespace and control characters",
			raw:  "stream\n\tdisconnected \x00 mid-response",
			want: "Stream disconnected mid-response.",
		},
		{
			name: "drops the redaction marker",
			raw:  "provider refused [truncated:diagnostic]",
			want: "Provider refused.",
		},
		{
			name: "reads a JSON message field",
			raw:  `{"error":{"type":"overloaded_error","message":"Overloaded"}}`,
			want: "Overloaded.",
		},
		{
			name: "reads a top-level message",
			raw:  `{"message":"Not logged in","code":401}`,
			want: "Not logged in.",
		},
		{
			name: "reads an envelope behind a CLI prefix",
			raw:  `API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			want: "Overloaded.",
		},
		{name: "keeps existing terminal punctuation", raw: "Rate limited!", want: "Rate limited!"},
		{name: "keeps a trailing ellipsis", raw: "Something happened…", want: "Something happened…"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Detail(test.raw); got != test.want {
				t.Fatalf("Detail(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestDetailIsBounded(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"words":              strings.Repeat("word ", 200),
		"spaceless cyrillic": strings.Repeat("я", 300),
		"spaceless emoji":    strings.Repeat("🤖", 300),
		"em dash at the cut": strings.Repeat("a", 278) + "—" + strings.Repeat("b", 50),
	} {
		got := Detail(raw)
		if n := len([]rune(got)); n > detailMaxRunes {
			t.Errorf("%s: Detail returned %d runes, want at most %d", name, n, detailMaxRunes)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: Detail returned invalid UTF-8: %q", name, got)
		}
		if !strings.HasSuffix(got, "…") {
			t.Errorf("%s: truncated detail should end with an ellipsis: %q", name, got)
		}
		if strings.Contains(got, "  ") {
			t.Errorf("%s: truncated detail has doubled spaces: %q", name, got)
		}
	}
}

func TestIsPreparationCode(t *testing.T) {
	t.Parallel()

	if !IsPreparationCode("run.preparation_failed.workspace") || IsPreparationCode("Overloaded") {
		t.Fatal("IsPreparationCode does not recognise the daemon's diagnostic prefix")
	}
}

// TestDetailExplainsPreparationPhases covers the bounded codes the daemon
// sends instead of a raw cause when a run fails before the agent starts.
func TestDetailExplainsPreparationPhases(t *testing.T) {
	t.Parallel()

	for _, phase := range PreparationPhases() {
		code := PreparationCode(phase)
		if code != "run.preparation_failed."+string(phase) || !IsPreparationCode(code) {
			t.Errorf("phase %q: code = %q", phase, code)
		}
		got := Detail(code)
		if got == "" || strings.Contains(got, "preparation_failed") || strings.Contains(got, "_") {
			t.Errorf("phase %q: Detail = %q, want a plain sentence", phase, got)
		}
	}
	if got := PreparationCode(""); got != "run.preparation_failed" {
		t.Errorf("PreparationCode(no phase) = %q", got)
	}
	if got := Detail("run.preparation_failed"); got == "" || strings.Contains(got, "_") {
		t.Errorf("bare preparation code: Detail = %q, want a plain sentence", got)
	}
	if got := Detail("run.preparation_failed.something_new"); got == "" || strings.Contains(got, "_") {
		t.Errorf("unknown phase: Detail = %q, want the generic preparation sentence", got)
	}
}

// DetailLabel says whose words a detail is: the provider's, when the
// failure is the provider's own and the detail is its sentence; otherwise
// a plain "Details".
func TestDetailLabel(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		reason   Reason
		provider string
		raw      string
		want     string
	}{
		{ReasonAgentProviderQuotaLimit, "codex", "You've hit your usage limit.", "Codex said"},
		{ReasonAgentProviderQuotaLimit, "", "You've hit your usage limit.", "The provider said"},
		{ReasonAgentProviderAuthOrAccess, "codex", PreparationCode(PreparationPhaseProviderAvailability), "Details"},
		{ReasonRuntimeOffline, "claude", "the machine went away", "Details"},
	} {
		if got := DetailLabel(test.reason, test.provider, test.raw); got != test.want {
			t.Errorf("DetailLabel(%q, %q, %q) = %q, want %q", test.reason, test.provider, test.raw, got, test.want)
		}
	}
	for _, provider := range protocol.SupportedProviders {
		if got := DetailLabel(ReasonAgentProviderQuotaLimit, provider, "x"); got == "The provider said" {
			t.Errorf("provider %q has no name in DetailLabel; add it to providerLabels", provider)
		}
	}
}
