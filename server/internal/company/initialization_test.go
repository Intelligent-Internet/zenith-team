package company

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

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

func TestInitializationJSONContract(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(Initialization{Phase: "research", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"phase":"research","status":"running","pagesWritten":0}` {
		t.Fatalf("empty progress = %s, want pagesWritten always present and optional fields omitted", raw)
	}
	run, conversation, page := uuid.New(), uuid.New(), uuid.New()
	raw, err = json.Marshal(Initialization{Phase: "ready", Status: "completed", RunID: &run, ConversationID: &conversation, PagesWritten: 6, OverviewPageID: &page})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"pagesWritten":6`, `"overviewPageId":"` + page.String() + `"`, `"runId"`, `"conversationId"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("%s lacks %s", raw, want)
		}
	}
}
