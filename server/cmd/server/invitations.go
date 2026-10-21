package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"

	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/config"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/infra/email"
)

type invitationEmailSender struct {
	service    *email.DefaultService
	templateID string
}

func (s *invitationEmailSender) SendInvitation(ctx context.Context, message company.InvitationEmail, key string) error {
	_, err := s.service.SendTemplate(
		ctx,
		email.TemplateMessage{
			To:             message.Email,
			TemplateID:     s.templateID,
			IdempotencyKey: key,
			Variables: map[string]string{
				"COMPANY_NAME":   message.CompanyName,
				"INVITER_NAME":   message.InviterName,
				"INVITATION_URL": message.URL,
				"EXPIRES_AT":     message.ExpiresAt.UTC().Format("January 2, 2006"),
			},
		},
	)
	return err
}

func newInvitationService(
	store *db.Store,
	bus *events.Bus,
	configuration config.Config,
) (*company.DefaultInvitationService, error) {
	cfg := configuration.Auth.EmailVerification
	secret := cfg.Secret.Reveal()
	if secret == "" {
		secret = configuration.CSRFSecret
	}
	key := sha256.Sum256([]byte("zenith-invitation-delivery\x00" + secret))
	shareKey := sha256.Sum256([]byte("zenith-company-share-link\x00" + configuration.CSRFSecret))
	options := company.InvitationOptions{
		ShareLinkEncryptionKey: shareKey[:],
		Store:                  store,
		Bus:                    bus,
		WebAppOrigin:           configuration.Auth.WebAppOrigin,
		EncryptionKey:          key[:],
	}
	if cfg.Enabled && cfg.InvitationTemplateID != "" {
		provider, err := email.NewResendProvider(
			email.ResendOptions{
				APIKey:     cfg.ResendAPIKey.Reveal(),
				HTTPClient: &http.Client{Timeout: cfg.ProviderTimeout},
			},
		)
		if err != nil {
			return nil, err
		}
		sender, err := email.NewService(provider)
		if err != nil {
			return nil, err
		}
		options.Sender = &invitationEmailSender{service: sender, templateID: cfg.InvitationTemplateID}
	}
	service, err := company.NewInvitationService(options)
	if err != nil {
		return nil, fmt.Errorf("construct invitation service: %w", err)
	}
	return service, nil
}
