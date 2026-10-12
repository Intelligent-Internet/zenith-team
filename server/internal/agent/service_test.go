package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
)

func TestServiceRequiresStoreAndBus(t *testing.T) {
	if _, err := NewAgentService(ServiceOptions{}); err == nil {
		t.Fatal("NewAgentService() error = nil without Store")
	}
	if _, err := NewAgentService(ServiceOptions{Store: &db.Store{}}); err == nil {
		t.Fatal("NewAgentService() error = nil without Bus")
	}
}

func TestNormalizeCreateInputAcceptsProviderDefaults(t *testing.T) {
	input := validCreateInput()
	input.AvatarKey = stringPointer(" fold-periwinkle ")
	input.AccentColor = stringPointer("#58b8f2")
	input.Name = "  Researcher  "
	input.Instructions = "\n  Investigate carefully.  \t"

	got, err := normalizeCreateInput(input)
	if err != nil {
		t.Fatalf("normalizeCreateInput() error = %v", err)
	}
	if got.Name != "Researcher" || got.Instructions != "Investigate carefully." {
		t.Fatalf("normalized input = %+v", got)
	}
	if got.AvatarKey == nil || *got.AvatarKey != "fold-periwinkle" ||
		got.AccentColor == nil || *got.AccentColor != "#58B8F2" {
		t.Fatalf("normalized appearance = %v / %v", got.AvatarKey, got.AccentColor)
	}
	if got.Model != nil || got.ThinkingLevel != nil {
		t.Fatalf("provider defaults = model %v thinking %v, want explicit nulls", got.Model, got.ThinkingLevel)
	}

	// A root agent carries no parent and is created bound (onboarding's
	// step); overrides normalize like any other.
	input.ReportsToAgentID = nil
	runtimeID := uuid.Must(uuid.NewV7())
	input.RuntimeID = &runtimeID
	model, thinking := " claude-sonnet ", " high "
	input.Model = &model
	input.ThinkingLevel = &thinking
	normalized, err := normalizeCreateInput(input)
	if err != nil {
		t.Fatalf("normalizeCreateInput(root) error = %v", err)
	}
	if normalized.ReportsToAgentID != nil || normalized.Model == nil || *normalized.Model != "claude-sonnet" ||
		normalized.ThinkingLevel == nil || *normalized.ThinkingLevel != "high" {
		t.Fatalf("normalized root input = %+v", normalized)
	}
}

func TestNormalizeCreateInputRejectsInvalidContractValues(t *testing.T) {
	empty := " \t\n "
	tooLongModel := strings.Repeat("m", maxModelRunes+1)
	tooLongThinking := strings.Repeat("t", maxThinkingLevelRunes+1)
	tests := map[string]func(*CreateInput){
		"nil runtime":             func(input *CreateInput) { input.RuntimeID = &uuid.Nil },
		"unknown provider":        func(input *CreateInput) { input.Provider = "gemini" },
		"empty provider":          func(input *CreateInput) { input.Provider = "" },
		"unbound root":            func(input *CreateInput) { input.ReportsToAgentID = nil; input.RuntimeID = nil },
		"zero-uuid parent":        func(input *CreateInput) { input.ReportsToAgentID = &uuid.Nil },
		"empty name":              func(input *CreateInput) { input.Name = empty },
		"long name":               func(input *CreateInput) { input.Name = strings.Repeat("😀", maxNameRunes+1) },
		"empty title":             func(input *CreateInput) { input.Title = empty },
		"long title":              func(input *CreateInput) { input.Title = strings.Repeat("😀", maxTitleRunes+1) },
		"long instructions":       func(input *CreateInput) { input.Instructions = strings.Repeat("😀", maxInstructionsRunes+1) },
		"byte-heavy instructions": func(input *CreateInput) { input.Instructions = strings.Repeat("😀", maxInstructionsBytes/4+1) },
		"invalid avatar key":      func(input *CreateInput) { input.AvatarKey = stringPointer("Sales Avatar") },
		"invalid accent color":    func(input *CreateInput) { input.AccentColor = stringPointer("blue") },
		"empty model override":    func(input *CreateInput) { input.Model = &empty },
		"long model override":     func(input *CreateInput) { input.Model = &tooLongModel },
		"empty thinking override": func(input *CreateInput) { input.ThinkingLevel = &empty },
		"long thinking override":  func(input *CreateInput) { input.ThinkingLevel = &tooLongThinking },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := validCreateInput()
			mutate(&input)
			if _, err := normalizeCreateInput(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizeCreateInput() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestNormalizeUpdateInputSupportsStrictPartialAndNullableChanges(t *testing.T) {
	name := "  Principal Researcher  "
	model := " gpt-5-codex "
	input := UpdateInput{
		ExpectedRevision: 1,
		Name:             &name,
		Model:            NullableTextChange{Present: true, Value: &model},
		ThinkingLevel:    NullableTextChange{Present: true, Value: nil},
	}

	got, err := normalizeUpdateInput(input)
	if err != nil {
		t.Fatalf("normalizeUpdateInput() error = %v", err)
	}
	if got.Name == nil || *got.Name != "Principal Researcher" ||
		got.Model.Value == nil || *got.Model.Value != "gpt-5-codex" ||
		!got.ThinkingLevel.Present || got.ThinkingLevel.Value != nil {
		t.Fatalf("normalized update = %+v", got)
	}
}

func TestNormalizeUpdateInputRejectsInvalidChanges(t *testing.T) {
	empty := " \n "
	longInstructions := strings.Repeat("😀", maxInstructionsBytes/4+1)
	nilParent := uuid.Nil
	// Two rules are not among these, because neither is declared here. The
	// revision bound is the `revision` binding rule on updateAgentRequest and
	// agentVersionRequest, exercised by the handler package's
	// TestRevisionBindingRejectsRevisionsOutsideTheRange. That an update
	// changes something is `required_without_all` on updateAgentRequest.Name,
	// exercised by the same package's "empty update" case in
	// TestAgentLifecycleRoutesRejectStrictInvalidBodies, which asserts both
	// the 422 and that the service was never called.
	tests := map[string]UpdateInput{
		"empty name":             {ExpectedRevision: 1, Name: &empty},
		"empty title":            {ExpectedRevision: 1, Title: &empty},
		"oversized instructions": {ExpectedRevision: 1, Instructions: &longInstructions},
		"nil parent":             {ExpectedRevision: 1, ReportsToAgentID: &nilParent},
		"empty model": {
			ExpectedRevision: 1,
			Model:            NullableTextChange{Present: true, Value: &empty},
		},
		"empty thinking": {
			ExpectedRevision: 1,
			ThinkingLevel:    NullableTextChange{Present: true, Value: &empty},
		},
		"hidden model value": {
			ExpectedRevision: 1,
			Name:             stringPointer("Name"),
			Model:            NullableTextChange{Value: stringPointer("model")},
		},
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeUpdateInput(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizeUpdateInput() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// The lifecycle frame reaches the whole Company and is kept as Company
// activity, so it is the template as a member who has not set the Agent up
// sees it: the acting member's binding, machine name and availability are
// theirs alone (ADR 0014) and never ride it.
func TestAgentLifecycleFrameCarriesNoMembersSetup(t *testing.T) {
	actor, runtimeID := uuid.New(), uuid.New()
	archivedAt := time.Date(2026, time.September, 25, 10, 31, 4, 0, time.UTC)
	for name, archived := range map[string]*time.Time{"active": nil, "archived": &archivedAt} {
		t.Run(name, func(t *testing.T) {
			agent := Response{
				ID: uuid.New(), CompanyID: uuid.New(), Name: "Lyra", Provider: "codex",
				Access: AgentAccess{Configure: true, Execute: true},
				Binding: &BindingRef{
					ID: uuid.New(),
					Runtime: RuntimeRef{
						ID: runtimeID, Name: "Codex (DuongPhung)", Status: "online", Provider: "codex",
						SignIn: &RuntimeSignIn{State: "signed_in", Detail: "Logged in using ChatGPT."},
					},
				},
				RuntimeAvailable: true, ActiveRunCount: 2, ArchivedAt: archived,
			}
			var pending []events.Event
			stageAgentLifecycle(&pending, actor, EventAgentCreated, agent)
			if len(pending) != 1 {
				t.Fatalf("staged %d events, want 1", len(pending))
			}
			frame, err := events.PayloadOf[Response](pending[0])
			if err != nil {
				t.Fatal(err)
			}
			wantReason := "not_bound"
			if archived != nil {
				wantReason = "agent_archived"
			}
			if frame.Binding != nil || frame.RuntimeAvailable || frame.Access.Execute ||
				frame.ActiveRunCount != 0 || frame.UnavailableReason == nil ||
				*frame.UnavailableReason != wantReason || !frame.Access.Configure || frame.Name != "Lyra" {
				t.Fatalf("lifecycle frame = %+v, want the template with no member's setup", frame)
			}
			encoded, err := json.Marshal(pending[0].Payload)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{runtimeID.String(), "DuongPhung", "Logged in using ChatGPT."} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("lifecycle frame names the acting member's machine (%q): %s", secret, encoded)
				}
			}
			if agent.Binding == nil || !agent.RuntimeAvailable {
				t.Fatal("staging the frame changed the caller's own projection")
			}
		})
	}
}

func stringPointer(value string) *string { return &value }

func validCreateInput() CreateInput {
	parent := uuid.Must(uuid.NewV7())
	return CreateInput{
		Name:             "Researcher",
		Title:            "Senior Researcher",
		Instructions:     "Investigate the assigned question.",
		ReportsToAgentID: &parent,
		Provider:         "claude",
		Model:            nil,
		ThinkingLevel:    nil,
	}
}
