package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

type CreateMailDraftDeliveryInput struct {
	To []string `json:"to"           jsonschema:"Primary recipients for one private email delivery."`
	CC []string `json:"cc,omitempty" jsonschema:"CC recipients for the same delivery."`
}

type CreateMailDraftInput struct {
	Subject    string                         `json:"subject"    jsonschema:"Plain-text email subject, without line breaks."`
	Text       string                         `json:"text"       jsonschema:"Plain-text email body."`
	Deliveries []CreateMailDraftDeliveryInput `json:"deliveries" jsonschema:"Separate deliveries. Put independent customers in separate entries so they cannot see each other."`
}

type CreateMailDraftsInput struct {
	Drafts []CreateMailDraftInput `json:"drafts" jsonschema:"One or more reviewable email drafts. Group only deliveries that share exactly the same subject and body."`
}

type CreateMailDraftsResult struct {
	GroupID        string `json:"groupId"`
	Drafts         int    `json:"drafts"`
	RecipientCount int    `json:"recipientCount"`
}

type mailAPI interface {
	PostRunJSON(context.Context, string, string, any, any) error
}

type createMailDraftsRequest struct {
	Drafts []CreateMailDraftInput `json:"drafts"`
}

type createMailDraftsResponse struct {
	ID             string `json:"id"`
	RecipientCount int    `json:"recipientCount"`
	Drafts         []struct {
		ID string `json:"id"`
	} `json:"drafts"`
}

// MailService owns the run-bound Agent Mail draft capability.
type MailService interface {
	CreateMailDrafts(context.Context, CreateMailDraftsInput) (CreateMailDraftsResult, error)
}

var _ MailService = (*DefaultMailService)(nil)

type DefaultMailService struct {
	api     mailAPI
	context RunContext
}

func NewMailService(api mailAPI, runContext RunContext) (*DefaultMailService, error) {
	if api == nil {
		return nil, errors.New("Zenith Team Agent Mail API is unavailable")
	}
	if err := validateRunContext(runContext); err != nil {
		return nil, errors.New("Zenith Team Agent Mail context is unavailable")
	}
	return &DefaultMailService{api: api, context: runContext}, nil
}

func (s *DefaultMailService) CreateMailDrafts(
	ctx context.Context,
	input CreateMailDraftsInput,
) (CreateMailDraftsResult, error) {
	if s == nil || s.api == nil {
		return CreateMailDraftsResult{}, errors.New("Zenith Team Agent Mail context is unavailable")
	}
	path := fmt.Sprintf(
		"/companies/%s/conversations/%s/mail-draft-groups",
		url.PathEscape(s.context.CompanyID), url.PathEscape(s.context.ConversationID),
	)
	request := createMailDraftsRequest(input)
	requestKey, err := runRequestKey("create_mail_drafts", s.context, request)
	if err != nil {
		return CreateMailDraftsResult{}, fmt.Errorf("create Agent Mail request key: %w", err)
	}
	var response createMailDraftsResponse
	if err := s.api.PostRunJSON(ctx, path, requestKey, request, &response); err != nil {
		return CreateMailDraftsResult{}, fmt.Errorf("create Agent Mail drafts: %w", err)
	}
	if response.ID == "" || len(response.Drafts) == 0 || response.RecipientCount < 1 {
		return CreateMailDraftsResult{}, errors.New("Zenith Team Agent Mail response is invalid")
	}
	return CreateMailDraftsResult{
		GroupID: response.ID, Drafts: len(response.Drafts),
		RecipientCount: response.RecipientCount,
	}, nil
}
