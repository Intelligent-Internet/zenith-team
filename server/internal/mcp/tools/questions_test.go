package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type questionAPIStub struct {
	path       string
	requestKey string
	body       any
}

func (s *questionAPIStub) PostRunJSON(
	_ context.Context,
	path, requestKey string,
	source, destination any,
) error {
	s.path, s.requestKey, s.body = path, requestKey, source
	encoded, _ := json.Marshal(map[string]any{"id": "question-id", "status": "pending"})
	return json.Unmarshal(encoded, destination)
}

func TestAskHumanUsesRunProofEndpointAndStableRequestKey(t *testing.T) {
	t.Parallel()
	api := new(questionAPIStub)
	service := NewQuestionService(api, "company/id")
	input := AskHumanInput{
		Title: "Choose a launch date",
		Prompts: []QuestionPrompt{{
			ID: "launch_date", Prompt: "Which date?", SelectionMode: "single", Required: true,
			Options: []QuestionOption{{ID: "monday", Label: "Monday"}, {ID: "tuesday", Label: "Tuesday"}},
		}},
	}

	first, err := service.AskHuman(context.Background(), input)
	if err != nil {
		t.Fatalf("AskHuman() error = %v", err)
	}
	firstKey := api.requestKey
	second, err := service.AskHuman(context.Background(), input)
	if err != nil {
		t.Fatalf("AskHuman(replay) error = %v", err)
	}
	if api.path != "/companies/company%2Fid/questions" || firstKey == "" || api.requestKey != firstKey {
		t.Fatalf("request = path %q, keys %q / %q", api.path, firstKey, api.requestKey)
	}
	if first.QuestionID != "question-id" || first.QuestionStatus != "pending" ||
		!strings.Contains(first.Instruction, "without retrying") || second != first {
		t.Fatalf("AskHuman() results = %+v / %+v", first, second)
	}
}

func TestAskHumanOmitsFreeTextOptionsAndKeepsItsRequestKeyStable(t *testing.T) {
	t.Parallel()
	api := new(questionAPIStub)
	service := NewQuestionService(api, "company-id")
	input := AskHumanInput{
		Title: "Deployment domain",
		Prompts: []QuestionPrompt{{
			ID: "domain", Prompt: "What domain should be used?",
			SelectionMode: "single", Required: true,
		}},
	}

	if _, err := service.AskHuman(context.Background(), input); err != nil {
		t.Fatalf("AskHuman(nil options) error = %v", err)
	}
	firstKey := api.requestKey
	body, err := json.Marshal(api.body)
	if err != nil {
		t.Fatalf("marshal AskHuman() request body: %v", err)
	}
	if strings.Contains(string(body), `"options"`) {
		t.Fatalf("AskHuman() request body = %s, want free-text options omitted", body)
	}

	input.Prompts[0].Options = []QuestionOption{}
	if _, err := service.AskHuman(context.Background(), input); err != nil {
		t.Fatalf("AskHuman(empty options) error = %v", err)
	}
	if api.requestKey != firstKey {
		t.Fatalf("free-text request keys = %q / %q, want stable canonical key", firstKey, api.requestKey)
	}
}

func TestAskHumanCarriesExplicitStudioSetup(t *testing.T) {
	t.Parallel()
	api := new(questionAPIStub)
	service := NewQuestionService(api, "company-id")
	input := AskHumanInput{Title: "Enable Zenith Studio", Prompts: []QuestionPrompt{{
		ID: "studio", Prompt: "Connect to Zenith Studio", SelectionMode: "single", Required: true,
		ToolSetup: "zenith-studio",
	}}}
	result, err := service.AskHuman(context.Background(), input)
	if err != nil {
		t.Fatalf("AskHuman() error = %v", err)
	}
	for _, guidance := range []string{"not enabled", "not connected", "automatically", "Do not announce", "click Continue"} {
		if !strings.Contains(result.Instruction, guidance) {
			t.Fatalf("Studio setup instruction = %q, want guidance %q", result.Instruction, guidance)
		}
	}
	if strings.Contains(result.Instruction, "pending human response") {
		t.Fatalf(
			"Studio setup instruction = %q, want enable guidance instead of an answer request",
			result.Instruction,
		)
	}
	key := api.requestKey
	body, err := json.Marshal(api.body)
	if err != nil || !strings.Contains(string(body), `"toolSetup":"zenith-studio"`) {
		t.Fatalf("Studio setup request = %s, %v", body, err)
	}
	input.Prompts[0].ToolSetup = ""
	if _, err := service.AskHuman(context.Background(), input); err != nil {
		t.Fatalf("AskHuman(ordinary question) error = %v", err)
	}
	if api.requestKey == key {
		t.Fatal("setup action and ordinary text question share a request key")
	}
}
