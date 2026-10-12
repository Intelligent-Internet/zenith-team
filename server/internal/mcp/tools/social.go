package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

type (
	ReadSocialInput    struct{}
	PublishSocialInput struct {
		QuestionID string `json:"questionId" jsonschema:"ID returned by ask_human for this exact socialPost preview. The human must have chosen publish. Publishes only the stored approved content; retries return the same receipt across Runs. Never create another review to retry an unknown outcome."`
	}
)

type SocialPost struct {
	ID         string `json:"id"`
	Platform   string `json:"platform"`
	TargetName string `json:"targetName"`
	Text       string `json:"text"`
	Status     string `json:"status"`
	ExternalID string `json:"externalId"`
	URL        string `json:"url"`
	ErrorCode  string `json:"errorCode"`
	CreatedAt  string `json:"createdAt"`
}

type ReadSocialResult struct {
	Connections []struct {
		ID          string `json:"id"`
		Platform    string `json:"platform"`
		DisplayName string `json:"displayName"`
		Username    string `json:"username"`
		Status      string `json:"status"`
		CanPublish  bool   `json:"canPublish"`
		Targets     []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"targets"`
	} `json:"connections"`
	Posts  []SocialPost  `json:"posts"`
	Images []SocialMedia `json:"images"`
	Videos []SocialMedia `json:"videos"`
}

type SocialMedia struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type socialAPI interface {
	FetchRunJSON(context.Context, string, any) error
	PostRunJSON(context.Context, string, string, any, any) error
}

type SocialToolService interface {
	ReadSocial(context.Context, ReadSocialInput) (ReadSocialResult, error)
	PublishSocial(context.Context, PublishSocialInput) (SocialPost, error)
}

var _ SocialToolService = (*DefaultSocialToolService)(nil)

type DefaultSocialToolService struct {
	api     socialAPI
	context RunContext
}

func NewSocialToolService(api socialAPI, run RunContext) (*DefaultSocialToolService, error) {
	if api == nil {
		return nil, errors.New("social API is unavailable")
	}
	if err := validateRunContext(run); err != nil {
		return nil, err
	}
	return &DefaultSocialToolService{api: api, context: run}, nil
}

func (s *DefaultSocialToolService) path() string {
	return fmt.Sprintf(
		"/companies/%s/conversations/%s/social",
		url.PathEscape(s.context.CompanyID),
		url.PathEscape(s.context.ConversationID),
	)
}

func (s *DefaultSocialToolService) ReadSocial(ctx context.Context, _ ReadSocialInput) (ReadSocialResult, error) {
	var result ReadSocialResult
	err := s.api.FetchRunJSON(ctx, s.path(), &result)
	return result, err
}

func (s *DefaultSocialToolService) PublishSocial(ctx context.Context, input PublishSocialInput) (SocialPost, error) {
	key, err := runRequestKey("social_publish", s.context, input)
	if err != nil {
		return SocialPost{}, err
	}
	var result SocialPost
	err = s.api.PostRunJSON(ctx, s.path()+"/posts", key, input, &result)
	return result, err
}
