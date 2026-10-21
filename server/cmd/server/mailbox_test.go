package main

import (
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/config"
	"github.com/Intelligent-Internet/zenith-team/server/internal/handler"
)

func TestDisabledMailboxComponentsStayNilAtHandlerBoundary(t *testing.T) {
	t.Parallel()

	components, err := newMailboxComponents(config.MailboxConfig{}, 1, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("newMailboxComponents() error = %v", err)
	}
	options := handler.Handler{
		MailboxService:         components.service,
		MailboxDraftService:    components.draftService,
		MailboxWebhookVerifier: components.verifier,
	}
	if options.MailboxService != nil {
		t.Error("disabled Mailbox supplied a typed-nil MailboxService")
	}
	if options.MailboxDraftService != nil {
		t.Error("disabled Mailbox supplied a typed-nil MailboxDraftService")
	}
	if options.MailboxWebhookVerifier != nil {
		t.Error("disabled Mailbox supplied a typed-nil MailboxWebhookVerifier")
	}
}

func TestNewOptionalMailboxWebhookVerifierAllowsMissingSecret(t *testing.T) {
	t.Parallel()

	verifier, err := newOptionalMailboxWebhookVerifier(config.MailboxConfig{
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("newOptionalMailboxWebhookVerifier() error = %v", err)
	}
	if verifier != nil {
		t.Fatal("newOptionalMailboxWebhookVerifier() returned a verifier without a signing secret")
	}
	options := handler.Handler{MailboxWebhookVerifier: verifier}
	if options.MailboxWebhookVerifier != nil {
		t.Fatal("missing signing secret supplied a typed-nil webhook verifier")
	}
}
