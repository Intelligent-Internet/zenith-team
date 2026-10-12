package email

import (
	"context"
	"testing"
)

func TestPasswordResetServiceMapsTheProviderNeutralTemplateContract(t *testing.T) {
	t.Parallel()

	provider := &providerStub{}
	service, err := NewService(provider)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	reset, err := NewPasswordResetService(service, "password-reset-template")
	if err != nil {
		t.Fatalf("NewPasswordResetService() error = %v", err)
	}
	if err := reset.SendPasswordReset(
		context.Background(), "founder@boardly.test", "Boardly Founder",
		"https://app.boardly.test/login/reset?token=secret", "password-reset/flow-id",
	); err != nil {
		t.Fatalf("SendPasswordReset() error = %v", err)
	}
	message := provider.message
	if message.TemplateID != "password-reset-template" ||
		message.Variables["DISPLAY_NAME"] != "Boardly Founder" ||
		message.Variables["RESET_URL"] != "https://app.boardly.test/login/reset?token=secret" {
		t.Fatalf("password reset template message = %#v", message)
	}
}
