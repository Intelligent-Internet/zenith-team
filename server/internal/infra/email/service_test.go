package email

import (
	"context"
	"errors"
	"testing"
)

func TestServiceSendsTemplateThroughAbstractProvider(t *testing.T) {
	t.Parallel()

	provider := &providerStub{}
	service, err := NewService(provider)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	message := TemplateMessage{
		To:             "founder@zenith-team.test",
		TemplateID:     "2f1d6c8e-4b7a-4c51-9a3e-5d0e7c9b1a01",
		Variables:      map[string]string{"DISPLAY_NAME": "Zenith Team Founder", "OTP_CODE": "123456"},
		IdempotencyKey: "email-verification/flow-1/1",
	}

	delivery, err := service.SendTemplate(context.Background(), message)
	if err != nil {
		t.Fatalf("SendTemplate() error = %v", err)
	}
	if delivery.ID != "delivery-1" || provider.calls != 1 {
		t.Fatalf("SendTemplate() = %#v, calls = %d", delivery, provider.calls)
	}
	if provider.message.To != message.To || provider.message.TemplateID != message.TemplateID ||
		provider.message.Variables["OTP_CODE"] != "123456" {
		t.Fatalf("provider message = %#v", provider.message)
	}
	message.Variables["OTP_CODE"] = "leaked-mutation"
	if provider.message.Variables["OTP_CODE"] != "123456" {
		t.Fatal("provider did not receive a defensive variables copy")
	}
}

func TestServiceRejectsInvalidTemplateMessagesBeforeProvider(t *testing.T) {
	t.Parallel()

	provider := &providerStub{}
	service, err := NewService(provider)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	valid := TemplateMessage{
		To: "founder@zenith-team.test", TemplateID: "template-id",
		Variables: map[string]string{"OTP_CODE": "123456"}, IdempotencyKey: "verification/1",
	}
	for name, mutate := range map[string]func(*TemplateMessage){
		"recipient":       func(message *TemplateMessage) { message.To = "not-an-email" },
		"template":        func(message *TemplateMessage) { message.TemplateID = "" },
		"variables":       func(message *TemplateMessage) { message.Variables = nil },
		"variable key":    func(message *TemplateMessage) { message.Variables = map[string]string{"bad-key": "value"} },
		"idempotency key": func(message *TemplateMessage) { message.IdempotencyKey = "" },
	} {
		t.Run(name, func(t *testing.T) {
			message := valid
			message.Variables = map[string]string{"OTP_CODE": "123456"}
			mutate(&message)
			if _, err := service.SendTemplate(
				context.Background(),
				message,
			); !errors.Is(
				err,
				ErrInvalidMessage,
			) {
				t.Fatalf("SendTemplate() error = %v, want ErrInvalidMessage", err)
			}
		})
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
}

func TestVerificationServiceMapsZenithTeamTemplateVariables(t *testing.T) {
	t.Parallel()

	provider := &providerStub{}
	service, err := NewService(provider)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	verification, err := NewVerificationService(service, "2f1d6c8e-4b7a-4c51-9a3e-5d0e7c9b1a01")
	if err != nil {
		t.Fatalf("NewVerificationService() error = %v", err)
	}

	if err := verification.SendVerification(
		context.Background(),
		"founder@zenith-team.test",
		"Zenith Team Founder",
		"654321",
		"email-verification/flow-1/2",
	); err != nil {
		t.Fatalf("SendVerification() error = %v", err)
	}
	if got := provider.message.Variables; got["DISPLAY_NAME"] != "Zenith Team Founder" ||
		got["OTP_CODE"] != "654321" {
		t.Fatalf("template variables = %#v", got)
	}
}

type providerStub struct {
	calls   int
	message TemplateMessage
	err     error
}

func (p *providerStub) SendTemplate(_ context.Context, message TemplateMessage) (Delivery, error) {
	p.calls++
	p.message = message
	return Delivery{ID: "delivery-1"}, p.err
}
