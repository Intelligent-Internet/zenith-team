package email

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
)

const maxIdempotencyKeyLength = 256

var (
	ErrInvalidMessage = errors.New("invalid email message")
	variableName      = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// TemplateMessage is provider-neutral input for a published email template.
// Provider-specific template syntax belongs in the provider adapter.
type TemplateMessage struct {
	To             string
	TemplateID     string
	Variables      map[string]string
	IdempotencyKey string
}

// Delivery is the provider-neutral result of accepting an email for delivery.
type Delivery struct {
	ID string
}

// TemplateProvider is the narrow boundary implemented by email providers such
// as Resend. Auth and other Boardly domains must not depend on provider APIs.
type TemplateProvider interface {
	SendTemplate(context.Context, TemplateMessage) (Delivery, error)
}

// Service owns the validation every provider-neutral message passes before it
// reaches a provider.
type Service interface {
	SendTemplate(context.Context, TemplateMessage) (Delivery, error)
}

var _ Service = (*DefaultService)(nil)

// DefaultService validates provider-neutral messages before dispatching them.
type DefaultService struct {
	provider TemplateProvider
}

func NewService(provider TemplateProvider) (*DefaultService, error) {
	if provider == nil {
		return nil, fmt.Errorf("%w: provider is required", ErrInvalidMessage)
	}
	return &DefaultService{provider: provider}, nil
}

func (s *DefaultService) SendTemplate(ctx context.Context, message TemplateMessage) (Delivery, error) {
	message.To = strings.TrimSpace(message.To)
	message.TemplateID = strings.TrimSpace(message.TemplateID)
	message.IdempotencyKey = strings.TrimSpace(message.IdempotencyKey)
	if err := validateTemplateMessage(message); err != nil {
		return Delivery{}, err
	}

	message.Variables = cloneVariables(message.Variables)
	return s.provider.SendTemplate(ctx, message)
}

func validateTemplateMessage(message TemplateMessage) error {
	address, err := mail.ParseAddress(message.To)
	if err != nil || address.Address != message.To {
		return fmt.Errorf("%w: recipient must be an email address", ErrInvalidMessage)
	}
	if message.TemplateID == "" {
		return fmt.Errorf("%w: template ID is required", ErrInvalidMessage)
	}
	if len(message.Variables) == 0 {
		return fmt.Errorf("%w: template variables are required", ErrInvalidMessage)
	}
	for name := range message.Variables {
		if !variableName.MatchString(name) {
			return fmt.Errorf("%w: invalid template variable name", ErrInvalidMessage)
		}
	}
	if message.IdempotencyKey == "" || len(message.IdempotencyKey) > maxIdempotencyKeyLength {
		return fmt.Errorf("%w: invalid idempotency key", ErrInvalidMessage)
	}
	return nil
}

func cloneVariables(variables map[string]string) map[string]string {
	cloned := make(map[string]string, len(variables))
	for name, value := range variables {
		cloned[name] = value
	}
	return cloned
}
