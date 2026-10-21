package approval

import (
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

var (
	ErrQuestionInvalidInput  = errors.New("question: invalid input")
	ErrQuestionNotFound      = errors.New("question: not found")
	ErrQuestionForbidden     = errors.New("question: resolver forbidden")
	ErrQuestionConflict      = errors.New("question: conflict")
	ErrQuestionExpired       = errors.New("question: expired")
	ErrQuestionConfiguration = errors.New("question: service configuration is invalid")
)

// QuestionSocialPost freezes the destination, content and media shown for a
// publication decision. The social domain resolves all presentation fields.
type QuestionSocialPost struct {
	ConnectionID       string                `json:"connectionId"`
	TargetID           string                `json:"targetId"`
	Text               string                `json:"text"`
	AttachmentIDs      []string              `json:"attachmentIds"`
	ConnectionRevision int64                 `json:"connectionRevision"`
	Platform           string                `json:"platform"`
	TargetName         string                `json:"targetName"`
	Username           string                `json:"username"`
	AvatarURL          string                `json:"avatarUrl"`
	Media              []QuestionSocialMedia `json:"media"`
}

type QuestionSocialMedia struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type QuestionAskInput struct {
	Title   string
	Prompts []QuestionPrompt
}

type QuestionAnswerInput struct {
	ExpectedRevision int64
	Answers          []QuestionAnswer
}

type QuestionCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type QuestionListOptions struct {
	Statuses []QuestionStatus
	AgentID  *uuid.UUID
	TaskID   *uuid.UUID
	Limit    int32
	Before   *QuestionCursor
	// Decide keeps only the Questions the caller may answer (view=decide).
	Decide bool
}

type QuestionPage struct {
	Items []Question
	Next  *QuestionCursor
}

func normalizeQuestionAskInput(input QuestionAskInput) (QuestionAskInput, error) {
	input.Title = protocol.TrimContractWhitespace(input.Title)
	if !validQuestionText(input.Title, 240, 1024) || len(input.Prompts) < 1 || len(input.Prompts) > 10 {
		return QuestionAskInput{}, ErrQuestionInvalidInput
	}
	seenPrompts := make(map[string]struct{}, len(input.Prompts))
	for index := range input.Prompts {
		prompt := &input.Prompts[index]
		prompt.ID = protocol.TrimContractWhitespace(prompt.ID)
		prompt.Prompt = protocol.TrimContractWhitespace(prompt.Prompt)
		if !validQuestionIdentifier(prompt.ID) || !validQuestionText(prompt.Prompt, 2000, 8192) {
			return QuestionAskInput{}, ErrQuestionInvalidInput
		}
		if _, duplicate := seenPrompts[prompt.ID]; duplicate {
			return QuestionAskInput{}, ErrQuestionInvalidInput
		}
		seenPrompts[prompt.ID] = struct{}{}
		if prompt.SocialPost != nil {
			if len(input.Prompts) != 1 || !prompt.Required || prompt.ToolSetup != "" ||
				prompt.SelectionMode != QuestionSelectionSingle || len(prompt.Options) != 2 ||
				prompt.Options[0].ID != "publish" || prompt.Options[1].ID != "cancel" {
				return QuestionAskInput{}, ErrQuestionInvalidInput
			}
			// Model-supplied labels must never disguise a public posting decision.
			prompt.Options[0].Label = "Publish now"
			prompt.Options[1].Label = "Do not publish"
		}
		if prompt.ToolSetup != "" && (prompt.ToolSetup != QuestionToolSetupZenithStudio ||
			len(input.Prompts) != 1 || !prompt.Required || len(prompt.Options) != 0) {
			return QuestionAskInput{}, ErrQuestionInvalidInput
		}
		if prompt.HelpText != nil {
			value := protocol.TrimContractWhitespace(*prompt.HelpText)
			if !validOptionalQuestionText(value, 4000, 16*1024) {
				return QuestionAskInput{}, ErrQuestionInvalidInput
			}
			prompt.HelpText = &value
		}
		if prompt.SelectionMode != QuestionSelectionSingle && prompt.SelectionMode != QuestionSelectionMulti {
			return QuestionAskInput{}, ErrQuestionInvalidInput
		}
		if prompt.Options == nil {
			prompt.Options = []QuestionOption{}
		}
		if len(prompt.Options) == 0 {
			if prompt.SelectionMode != QuestionSelectionSingle || prompt.AllowCustomAnswer {
				return QuestionAskInput{}, ErrQuestionInvalidInput
			}
			continue
		}
		if len(prompt.Options) < 2 || len(prompt.Options) > 20 {
			return QuestionAskInput{}, ErrQuestionInvalidInput
		}
		seenOptions := make(map[string]struct{}, len(prompt.Options))
		for optionIndex := range prompt.Options {
			option := &prompt.Options[optionIndex]
			option.ID = protocol.TrimContractWhitespace(option.ID)
			option.Label = protocol.TrimContractWhitespace(option.Label)
			if !validQuestionIdentifier(option.ID) || !validQuestionText(option.Label, 240, 1024) {
				return QuestionAskInput{}, ErrQuestionInvalidInput
			}
			if _, duplicate := seenOptions[option.ID]; duplicate {
				return QuestionAskInput{}, ErrQuestionInvalidInput
			}
			seenOptions[option.ID] = struct{}{}
		}
	}
	return input, nil
}

// answerContradictsChoicePrompt decides whether an answer disagrees with the
// prompt it answers. A custom answer replaces the selected option for a
// single-select prompt and may accompany selected options for multi-select.
//
// It cannot be a binding rule. The prompt is read back from the stored
// Question row, not sent with the answer, so the rule is a relation between a
// row and a request body and no tag can see both.
func answerContradictsChoicePrompt(prompt QuestionPrompt, answer QuestionAnswer) bool {
	hasCustomAnswer := answer.Text != nil
	if hasCustomAnswer && !prompt.AllowCustomAnswer || len(answer.OptionIDs) > len(prompt.Options) {
		return true
	}
	if prompt.SelectionMode == QuestionSelectionSingle {
		return hasCustomAnswer && len(answer.OptionIDs) != 0 ||
			!hasCustomAnswer && len(answer.OptionIDs) != 1
	}
	return !hasCustomAnswer && len(answer.OptionIDs) == 0
}

func normalizeQuestionAnswerInput(
	prompts []QuestionPrompt,
	input QuestionAnswerInput,
) (QuestionAnswerInput, error) {
	if len(input.Answers) > len(prompts) {
		return QuestionAnswerInput{}, ErrQuestionInvalidInput
	}
	byID := make(map[string]QuestionPrompt, len(prompts))
	for _, prompt := range prompts {
		byID[prompt.ID] = prompt
	}
	seen := make(map[string]struct{}, len(input.Answers))
	for index := range input.Answers {
		answer := &input.Answers[index]
		answer.PromptID = protocol.TrimContractWhitespace(answer.PromptID)
		prompt, exists := byID[answer.PromptID]
		if !exists {
			return QuestionAnswerInput{}, ErrQuestionInvalidInput
		}
		if _, duplicate := seen[answer.PromptID]; duplicate {
			return QuestionAnswerInput{}, ErrQuestionInvalidInput
		}
		seen[answer.PromptID] = struct{}{}
		if answer.OptionIDs == nil {
			answer.OptionIDs = []string{}
		}
		if len(prompt.Options) == 0 {
			if len(answer.OptionIDs) != 0 || answer.Text == nil {
				return QuestionAnswerInput{}, ErrQuestionInvalidInput
			}
			value := protocol.TrimContractWhitespace(*answer.Text)
			if !validQuestionText(value, 16*1024, 64*1024) {
				return QuestionAnswerInput{}, ErrQuestionInvalidInput
			}
			answer.Text = &value
			continue
		}
		if answerContradictsChoicePrompt(prompt, *answer) {
			return QuestionAnswerInput{}, ErrQuestionInvalidInput
		}
		if answer.Text != nil {
			value := protocol.TrimContractWhitespace(*answer.Text)
			if !validQuestionText(value, 16*1024, 64*1024) {
				return QuestionAnswerInput{}, ErrQuestionInvalidInput
			}
			answer.Text = &value
		}
		allowed := make(map[string]struct{}, len(prompt.Options))
		for _, option := range prompt.Options {
			allowed[option.ID] = struct{}{}
		}
		seenOptions := make(map[string]struct{}, len(answer.OptionIDs))
		for optionIndex, optionID := range answer.OptionIDs {
			optionID = protocol.TrimContractWhitespace(optionID)
			if _, exists := allowed[optionID]; !exists {
				return QuestionAnswerInput{}, ErrQuestionInvalidInput
			}
			if _, duplicate := seenOptions[optionID]; duplicate {
				return QuestionAnswerInput{}, ErrQuestionInvalidInput
			}
			seenOptions[optionID] = struct{}{}
			answer.OptionIDs[optionIndex] = optionID
		}
	}
	for _, prompt := range prompts {
		if prompt.Required {
			if _, answered := seen[prompt.ID]; !answered {
				return QuestionAnswerInput{}, ErrQuestionInvalidInput
			}
		}
	}
	return input, nil
}

func validQuestionStatus(status QuestionStatus) bool {
	switch status {
	case QuestionStatusPending, QuestionStatusAnswered, QuestionStatusWithdrawn, QuestionStatusExpired:
		return true
	default:
		return false
	}
}

func validQuestionIdentifier(value string) bool {
	return validQuestionText(value, 64, 256)
}

func validQuestionText(value string, maxRunes, maxBytes int) bool {
	return value != "" && validOptionalQuestionText(value, maxRunes, maxBytes)
}

func validOptionalQuestionText(value string, maxRunes, maxBytes int) bool {
	return utf8.ValidString(value) && protocol.TrimContractWhitespace(value) == value &&
		utf8.RuneCountInString(value) <= maxRunes && len(value) <= maxBytes
}
