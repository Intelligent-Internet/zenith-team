package company

import (
	"strings"
	"testing"

	provideragent "boardly.dev/boardly/server/pkg/agent"
)

func TestInitializationModelUsesNewestAdvertisedLunaAndLowestReasoning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		models               []provideragent.RuntimeModel
		wantModel, wantLevel string
		wantError            bool
	}{
		{name: "missing Luna", models: []provideragent.RuntimeModel{{ID: "gpt-6", Label: "GPT 6"}}, wantError: true},
		{name: "exact Luna", models: []provideragent.RuntimeModel{{ID: "gpt-6-luna", Label: "Luna"}}, wantModel: "gpt-6-luna"},
		{name: "none first", models: []provideragent.RuntimeModel{{ID: "gpt-6-luna", Label: "Luna", Reasoning: &provideragent.RuntimeModelReasoning{Levels: []provideragent.RuntimeReasoningLevel{{Value: "high"}, {Value: "low"}, {Value: "none"}}}}}, wantModel: "gpt-6-luna", wantLevel: "none"},
		{name: "unknown levels use the runtime default", models: []provideragent.RuntimeModel{{ID: "gpt-6-luna", Reasoning: &provideragent.RuntimeModelReasoning{Levels: []provideragent.RuntimeReasoningLevel{{Value: "auto"}}}}}, wantModel: "gpt-6-luna"},
		{name: "low fallback", models: []provideragent.RuntimeModel{{ID: "gpt-6-luna", Label: "Luna", Reasoning: &provideragent.RuntimeModelReasoning{Levels: []provideragent.RuntimeReasoningLevel{{Value: "high"}, {Value: "low"}}}}}, wantModel: "gpt-6-luna", wantLevel: "low"},
		{name: "no fuzzy alias", models: []provideragent.RuntimeModel{{ID: "not-luna", Label: "Not Luna"}}, wantError: true},
		{name: "no lookalike", models: []provideragent.RuntimeModel{{ID: "gpt-6-lunar"}, {ID: "gpt-luna"}, {ID: "gpt-6-luna-preview"}}, wantError: true},
		{name: "earlier generation", models: []provideragent.RuntimeModel{{ID: "gpt-6.1-sol"}, {ID: "gpt-5.6-luna", Label: "GPT-5.6-Luna"}}, wantModel: "gpt-5.6-luna"},
		{name: "bare alias", models: []provideragent.RuntimeModel{{ID: "luna"}}, wantModel: "luna"},
		{name: "newest Luna", models: []provideragent.RuntimeModel{{ID: "luna"}, {ID: "gpt-5.6-luna"}, {ID: "gpt-6-luna"}, {ID: "gpt-5.10-luna"}}, wantModel: "gpt-6-luna"},
		{name: "minor version order", models: []provideragent.RuntimeModel{{ID: "gpt-5.10-luna"}, {ID: "gpt-5.9-luna"}}, wantModel: "gpt-5.10-luna"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model, level, err := initializationModel(tt.models)
			if (err != nil) != tt.wantError || model != tt.wantModel || level != tt.wantLevel {
				t.Fatalf("got = %q/%q/%v, want %q/%q/error=%v", model, level, err, tt.wantModel, tt.wantLevel, tt.wantError)
			}
		})
	}
}

func TestInitializationRejectsMissingOrUnboundedResearch(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "Sorry, I could not read your site.", "# Business context\nNo sources or unknowns", "## Business context\n## Sources\n## Open questions", "# Business context\n" + strings.Repeat("x", 65536) + "\n## Sources\n## Open questions"} {
		if _, ok := initializationContext(content); ok {
			t.Fatalf("accepted incomplete context %q", content)
		}
	}
	const want = "# Business context\nFounder: a shop.\n## Sources\nWebsite unavailable.\n## Open questions\nPricing TBD."
	for _, content := range []string{want, "Here is the context:\n\n```markdown\n" + want + "\n```\n"} {
		got, ok := initializationContext(content)
		if !ok || got != want {
			t.Fatalf("initializationContext(%q) = %q/%v, want %q", content, got, ok, want)
		}
	}
}
