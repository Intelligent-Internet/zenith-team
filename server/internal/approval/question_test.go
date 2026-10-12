package approval

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeQuestionAskInput(t *testing.T) {
	t.Parallel()

	help := "Used for the release pipeline."
	input := QuestionAskInput{
		Title: "  Choose a deployment target  ",
		Prompts: []QuestionPrompt{
			{
				ID: " environment ", Prompt: " Which environment? ", HelpText: &help,
				SelectionMode: QuestionSelectionSingle, Required: true,
				Options: []QuestionOption{
					{ID: " staging ", Label: " Staging "},
					{ID: " production ", Label: " Production "},
				},
			},
			{
				ID: "note", Prompt: "Anything else?", SelectionMode: QuestionSelectionSingle,
			},
		},
	}

	got, err := normalizeQuestionAskInput(input)
	if err != nil {
		t.Fatalf("normalizeQuestionAskInput() error = %v", err)
	}
	if got.Title != "Choose a deployment target" || got.Prompts[0].ID != "environment" ||
		got.Prompts[0].Prompt != "Which environment?" || got.Prompts[0].Options[0].ID != "staging" ||
		got.Prompts[0].Options[0].Label != "Staging" {
		t.Fatalf("normalizeQuestionAskInput() = %+v", got)
	}
	if got.Prompts[1].Options == nil {
		t.Fatal("free-text prompt options = nil, want canonical empty array")
	}
}

func TestNormalizeQuestionAskInputStudioSetup(t *testing.T) {
	t.Parallel()
	valid := func() QuestionAskInput {
		return QuestionAskInput{Title: "Enable Zenith Studio", Prompts: []QuestionPrompt{{
			ID: "studio", Prompt: "Connect to Zenith Studio", SelectionMode: QuestionSelectionSingle,
			Required: true, ToolSetup: QuestionToolSetupZenithStudio,
		}}}
	}
	got, err := normalizeQuestionAskInput(valid())
	if err != nil || got.Prompts[0].ToolSetup != QuestionToolSetupZenithStudio || got.Prompts[0].Options == nil {
		t.Fatalf("normalize Studio setup = %+v, %v", got, err)
	}
	for name, mutate := range map[string]func(*QuestionAskInput){
		"unknown tool": func(input *QuestionAskInput) { input.Prompts[0].ToolSetup = "unknown" },
		"multiple prompts": func(input *QuestionAskInput) {
			input.Prompts = append(input.Prompts, QuestionPrompt{
				ID: "other", Prompt: "Anything else?", SelectionMode: QuestionSelectionSingle,
			})
		},
		"optional":      func(input *QuestionAskInput) { input.Prompts[0].Required = false },
		"multi":         func(input *QuestionAskInput) { input.Prompts[0].SelectionMode = QuestionSelectionMulti },
		"custom answer": func(input *QuestionAskInput) { input.Prompts[0].AllowCustomAnswer = true },
		"choices": func(input *QuestionAskInput) {
			input.Prompts[0].Options = []QuestionOption{
				{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"},
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := valid()
			mutate(&input)
			if _, err := normalizeQuestionAskInput(input); !errors.Is(err, ErrQuestionInvalidInput) {
				t.Fatalf("normalize Studio setup error = %v, want invalid input", err)
			}
		})
	}
}

func TestNormalizeQuestionAskInputRejectsInvalidPrompts(t *testing.T) {
	t.Parallel()

	valid := func() QuestionAskInput {
		return QuestionAskInput{
			Title: "Choose a deployment target",
			Prompts: []QuestionPrompt{
				{
					ID:            "environment",
					Prompt:        "Which environment?",
					SelectionMode: QuestionSelectionSingle,
					Required:      true,
					Options: []QuestionOption{
						{ID: "staging", Label: "Staging"},
						{ID: "production", Label: "Production"},
					},
				},
			},
		}
	}

	tests := map[string]func(*QuestionAskInput){
		"no prompts": func(input *QuestionAskInput) { input.Prompts = nil },
		"more than ten prompts": func(input *QuestionAskInput) {
			input.Prompts = make([]QuestionPrompt, 11)
		},
		"duplicate prompt id": func(input *QuestionAskInput) {
			input.Prompts = append(input.Prompts, input.Prompts[0])
		},
		"empty prompt": func(input *QuestionAskInput) { input.Prompts[0].Prompt = " " },
		"unknown selection mode": func(input *QuestionAskInput) {
			input.Prompts[0].SelectionMode = "many"
		},
		"multi free text": func(input *QuestionAskInput) {
			input.Prompts[0].Options = nil
			input.Prompts[0].SelectionMode = QuestionSelectionMulti
		},
		"custom answer without options": func(input *QuestionAskInput) {
			input.Prompts[0].Options = nil
			input.Prompts[0].AllowCustomAnswer = true
		},
		"one option": func(input *QuestionAskInput) {
			input.Prompts[0].Options = input.Prompts[0].Options[:1]
		},
		"duplicate option id": func(input *QuestionAskInput) {
			input.Prompts[0].Options[1].ID = input.Prompts[0].Options[0].ID
		},
		"too many options": func(input *QuestionAskInput) {
			input.Prompts[0].Options = make([]QuestionOption, 21)
		},
		"oversized title": func(input *QuestionAskInput) {
			input.Title = strings.Repeat("x", 241)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := valid()
			mutate(&input)
			if _, err := normalizeQuestionAskInput(input); !errors.Is(err, ErrQuestionInvalidInput) {
				t.Fatalf("normalizeQuestionAskInput() error = %v, want ErrQuestionInvalidInput", err)
			}
		})
	}
}

func TestNormalizeQuestionAnswerInput(t *testing.T) {
	t.Parallel()

	prompts := []QuestionPrompt{
		{
			ID:                "environment",
			Prompt:            "Which environment?",
			SelectionMode:     QuestionSelectionSingle,
			Required:          true,
			AllowCustomAnswer: true,
			Options: []QuestionOption{
				{ID: "staging", Label: "Staging"},
				{ID: "production", Label: "Production"},
			},
		},
		{
			ID:            "regions",
			Prompt:        "Which regions?",
			SelectionMode: QuestionSelectionMulti,
			Options: []QuestionOption{
				{ID: "eu", Label: "Europe"},
				{ID: "us", Label: "United States"},
			},
		},
		{ID: "note", Prompt: "Anything else?", SelectionMode: QuestionSelectionSingle},
	}
	note := "  Deploy after the freeze  "
	customEnvironment := "  Customer-hosted  "
	input := QuestionAnswerInput{ExpectedRevision: 1, Answers: []QuestionAnswer{
		{PromptID: "environment", Text: &customEnvironment},
		{PromptID: "regions", OptionIDs: []string{"us", "eu"}},
		{PromptID: "note", Text: &note},
	}}

	got, err := normalizeQuestionAnswerInput(prompts, input)
	if err != nil {
		t.Fatalf("normalizeQuestionAnswerInput() error = %v", err)
	}
	if got.Answers[0].Text == nil || *got.Answers[0].Text != "Customer-hosted" || got.Answers[2].Text == nil ||
		*got.Answers[2].Text != "Deploy after the freeze" {
		t.Fatalf("normalizeQuestionAnswerInput() = %+v", got)
	}
}

func TestNormalizeQuestionAnswerInputRejectsInvalidAnswers(t *testing.T) {
	t.Parallel()

	prompts := []QuestionPrompt{
		{
			ID:            "environment",
			Prompt:        "Which environment?",
			SelectionMode: QuestionSelectionSingle,
			Required:      true,
			Options: []QuestionOption{
				{ID: "staging", Label: "Staging"},
				{ID: "production", Label: "Production"},
			},
		},
		{ID: "note", Prompt: "Anything else?", SelectionMode: QuestionSelectionSingle},
	}
	text := "explain"
	valid := func() QuestionAnswerInput {
		return QuestionAnswerInput{ExpectedRevision: 1, Answers: []QuestionAnswer{
			{PromptID: "environment", OptionIDs: []string{"staging"}},
		}}
	}
	tests := map[string]func(*QuestionAnswerInput){
		"missing required": func(input *QuestionAnswerInput) { input.Answers = nil },
		"unknown prompt":   func(input *QuestionAnswerInput) { input.Answers[0].PromptID = "missing" },
		"duplicate prompt": func(input *QuestionAnswerInput) {
			input.Answers = append(input.Answers, input.Answers[0])
		},
		"unknown option": func(input *QuestionAnswerInput) { input.Answers[0].OptionIDs[0] = "qa" },
		"multiple single options": func(input *QuestionAnswerInput) {
			input.Answers[0].OptionIDs = []string{"staging", "production"}
		},
		"option and text": func(input *QuestionAnswerInput) { input.Answers[0].Text = &text },
		"free text with option": func(input *QuestionAnswerInput) {
			input.Answers = append(
				input.Answers,
				QuestionAnswer{PromptID: "note", OptionIDs: []string{"x"}},
			)
		},
		"empty free text": func(input *QuestionAnswerInput) {
			empty := " "
			input.Answers = append(input.Answers, QuestionAnswer{PromptID: "note", Text: &empty})
		},
		// A revision outside 1..protocol.MaxJSONSafeInteger is not mutated in
		// here: it is the `revision` binding rule on questionAnswerRequest,
		// exercised by the handler package's
		// TestRevisionBindingRejectsRevisionsOutsideTheRange.
		"more answers than prompts": func(input *QuestionAnswerInput) {
			input.Answers = append(input.Answers,
				QuestionAnswer{PromptID: "note", Text: &text},
				QuestionAnswer{PromptID: "note", Text: &text},
			)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := valid()
			mutate(&input)
			if _, err := normalizeQuestionAnswerInput(
				prompts,
				input,
			); !errors.Is(
				err,
				ErrQuestionInvalidInput,
			) {
				t.Fatalf("normalizeQuestionAnswerInput() error = %v, want ErrQuestionInvalidInput", err)
			}
		})
	}
}

func TestNormalizeQuestionAnswerInputAcceptsMultiChoiceWithCustomAnswer(t *testing.T) {
	t.Parallel()
	custom := "Vietnam"
	prompts := []QuestionPrompt{{
		ID: "regions", Prompt: "Which regions?", SelectionMode: QuestionSelectionMulti,
		Required: true, AllowCustomAnswer: true,
		Options: []QuestionOption{{ID: "us", Label: "United States"}, {ID: "eu", Label: "Europe"}},
	}}
	got, err := normalizeQuestionAnswerInput(prompts, QuestionAnswerInput{
		ExpectedRevision: 1,
		Answers:          []QuestionAnswer{{PromptID: "regions", OptionIDs: []string{"us"}, Text: &custom}},
	})
	if err != nil || got.Answers[0].Text == nil || *got.Answers[0].Text != custom {
		t.Fatalf("normalizeQuestionAnswerInput() = %+v, %v", got, err)
	}
}

func TestNormalizeQuestionListOptionsDefaultsToPending(t *testing.T) {
	t.Parallel()

	got, err := normalizeQuestionListOptions(QuestionListOptions{})
	if err != nil {
		t.Fatalf("normalizeQuestionListOptions() error = %v", err)
	}
	if got.Limit != 50 || len(got.Statuses) != 1 || got.Statuses[0] != QuestionStatusPending {
		t.Fatalf("normalizeQuestionListOptions() = %+v", got)
	}
}

func TestNormalizeSocialReviewRequiresExplicitDecision(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		change    func(*QuestionAskInput)
		wantError bool
	}{
		{"canonical labels", func(in *QuestionAskInput) { in.Prompts[0].Options[0].Label = "Just save a draft" }, false},
		{"optional", func(in *QuestionAskInput) { in.Prompts[0].Required = false }, true},
		{"multi select", func(in *QuestionAskInput) { in.Prompts[0].SelectionMode = QuestionSelectionMulti }, true},
		{"unrelated choice", func(in *QuestionAskInput) { in.Prompts[0].Options[0].ID = "yes" }, true},
		{"tool setup", func(in *QuestionAskInput) { in.Prompts[0].ToolSetup = QuestionToolSetupZenithStudio }, true},
		{"bundled question", func(in *QuestionAskInput) { in.Prompts = append(in.Prompts, QuestionPrompt{ID: "other"}) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := QuestionAskInput{Title: "Review", Prompts: []QuestionPrompt{
				{
					ID:            "post",
					Prompt:        "Publish?",
					SelectionMode: QuestionSelectionSingle,
					Required:      true,
					SocialPost: &QuestionSocialPost{
						ConnectionID: "connection",
						TargetID:     "target",
					},
					Options: []QuestionOption{
						{ID: "publish", Label: "Publish"},
						{ID: "cancel", Label: "Cancel"},
					},
				},
			}}
			tc.change(&in)
			got, err := normalizeQuestionAskInput(in)
			if tc.wantError {
				if !errors.Is(err, ErrQuestionInvalidInput) {
					t.Fatalf("got %v, want invalid input", err)
				}
				return
			}
			if err != nil || got.Prompts[0].Options[0].Label != "Publish now" {
				t.Fatalf("got %+v, %v, want explicit publication label", got, err)
			}
		})
	}
}
