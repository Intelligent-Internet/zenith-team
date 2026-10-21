package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

const AskHumanToolName = "ask_human"

type QuestionAPI interface {
	PostRunJSON(context.Context, string, string, any, any) error
}

type QuestionService interface {
	AskHuman(context.Context, AskHumanInput) (AskHumanResult, error)
}

var _ QuestionService = (*DefaultQuestionService)(nil)

type DefaultQuestionService struct {
	api       QuestionAPI
	companyID string
}

func NewQuestionService(api QuestionAPI, companyID string) *DefaultQuestionService {
	return &DefaultQuestionService{api: api, companyID: strings.TrimSpace(companyID)}
}

type QuestionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type QuestionPrompt struct {
	ID                string           `json:"id"`
	Prompt            string           `json:"prompt"`
	HelpText          *string          `json:"helpText,omitempty"`
	SelectionMode     string           `json:"selectionMode"`
	Required          bool             `json:"required"`
	AllowCustomAnswer bool             `json:"allowCustomAnswer"`
	Options           []QuestionOption `json:"options,omitempty"`
	ToolSetup         string           `json:"toolSetup,omitempty"`
	SocialPost        *SocialPostDraft `json:"socialPost,omitempty"`
}

type SocialPostDraft struct {
	ConnectionID  string   `json:"connectionId"`
	TargetID      string   `json:"targetId"`
	Text          string   `json:"text"`
	AttachmentIDs []string `json:"attachmentIds,omitempty"`
}

type AskHumanInput struct {
	Title   string           `json:"title"`
	Prompts []QuestionPrompt `json:"prompts"`
}

type questionSummary struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type AskHumanResult struct {
	QuestionID     string `json:"questionId"`
	QuestionStatus string `json:"questionStatus"`
	Instruction    string `json:"instruction"`
}

func (s *DefaultQuestionService) AskHuman(
	ctx context.Context,
	input AskHumanInput,
) (AskHumanResult, error) {
	if s == nil || s.api == nil || s.companyID == "" {
		return AskHumanResult{}, errors.New("Zenith Team human-question service is unavailable")
	}
	canonical, err := json.Marshal(input)
	if err != nil {
		return AskHumanResult{}, fmt.Errorf("encode human question: %w", err)
	}
	requestKey := uuid.NewSHA1(
		uuid.NameSpaceURL,
		append([]byte("zenith-team:human-question:"), canonical...),
	).String()
	path := fmt.Sprintf("/companies/%s/questions", url.PathEscape(s.companyID))
	// The endpoint answers with the bare Question for both first ask and
	// exact replay, so the tool reports what the question is, not how it got
	// there.
	var response questionSummary
	if err := s.api.PostRunJSON(ctx, path, requestKey, input, &response); err != nil {
		return AskHumanResult{}, err
	}
	instruction := "The question is pending human response. Briefly ask for the needed choice in the user's language. Do not narrate creating a card or mention Runs or Questions. End this run without retrying or asking the same question again."
	if len(input.Prompts) == 1 && input.Prompts[0].ToolSetup == "zenith-studio" {
		instruction = "Briefly tell the user, in their language, that Zenith Studio is not enabled or not connected, matching the actual state, and needs enabling or connecting. " +
			"Their original request resumes automatically once Studio is enabled and connected. " +
			"Do not announce creating a card, waiting for an answer, or starting another Run, and do not ask them to click Continue. " +
			"Do not mention Runs or Questions to the user. " +
			"End this run without retrying or asking the same question again."
	}
	return AskHumanResult{
		QuestionID:     response.ID,
		QuestionStatus: response.Status,
		Instruction:    instruction,
	}, nil
}
