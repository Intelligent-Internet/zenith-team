package email

import (
	"context"
	"errors"
	"strings"
)

var ErrInvalidVerificationConfig = errors.New("invalid email verification configuration")

// VerificationService owns Zenith Team's verification-template contract while the
// underlying email Service remains reusable and provider-neutral.
type VerificationService interface {
	SendVerification(context.Context, string, string, string, string) error
}

var _ VerificationService = (*DefaultVerificationService)(nil)

type DefaultVerificationService struct {
	service    *DefaultService
	templateID string
}

func NewVerificationService(service *DefaultService, templateID string) (*DefaultVerificationService, error) {
	templateID = strings.TrimSpace(templateID)
	if service == nil || templateID == "" {
		return nil, ErrInvalidVerificationConfig
	}
	return &DefaultVerificationService{service: service, templateID: templateID}, nil
}

func (s *DefaultVerificationService) SendVerification(
	ctx context.Context,
	recipient string,
	displayName string,
	code string,
	idempotencyKey string,
) error {
	_, err := s.service.SendTemplate(ctx, TemplateMessage{
		To:         recipient,
		TemplateID: s.templateID,
		Variables: map[string]string{
			"DISPLAY_NAME": displayName,
			"OTP_CODE":     code,
		},
		IdempotencyKey: idempotencyKey,
	})
	return err
}
