package email

import (
	"context"
	"errors"
	"strings"
)

var ErrInvalidPasswordResetConfig = errors.New("invalid password reset email configuration")

// PasswordResetService owns Zenith Team's password-reset template contract while
// the provider-neutral email Service remains reusable across email providers.
type PasswordResetService interface {
	SendPasswordReset(context.Context, string, string, string, string) error
}

var _ PasswordResetService = (*DefaultPasswordResetService)(nil)

type DefaultPasswordResetService struct {
	service    *DefaultService
	templateID string
}

func NewPasswordResetService(service *DefaultService, templateID string) (*DefaultPasswordResetService, error) {
	templateID = strings.TrimSpace(templateID)
	if service == nil || templateID == "" {
		return nil, ErrInvalidPasswordResetConfig
	}
	return &DefaultPasswordResetService{service: service, templateID: templateID}, nil
}

func (s *DefaultPasswordResetService) SendPasswordReset(
	ctx context.Context,
	recipient string,
	displayName string,
	resetURL string,
	idempotencyKey string,
) error {
	_, err := s.service.SendTemplate(ctx, TemplateMessage{
		To: recipient, TemplateID: s.templateID, IdempotencyKey: idempotencyKey,
		Variables: map[string]string{"DISPLAY_NAME": displayName, "RESET_URL": resetURL},
	})
	return err
}
