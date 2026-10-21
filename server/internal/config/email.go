package config

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type EmailVerificationConfig struct {
	Enabled                     bool
	Provider                    string
	ResendAPIKey                SecretString
	Secret                      SecretString
	TemplateID                  string
	InvitationTemplateID        string
	TTL                         time.Duration
	ResendCooldown              time.Duration
	ProviderTimeout             time.Duration
	PasswordResetTemplateID     string
	PasswordResetTTL            time.Duration
	PasswordResetResendCooldown time.Duration
}

func (c EmailVerificationConfig) String() string {
	return c.safeString()
}

func (c EmailVerificationConfig) GoString() string {
	return c.safeString()
}

func (c EmailVerificationConfig) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, c.safeString())
}

func (c EmailVerificationConfig) safeString() string {
	return fmt.Sprintf(
		"EmailVerificationConfig{Enabled:%t Provider:%q ResendAPIKey:[REDACTED] Secret:[REDACTED] TemplateID:%q TTL:%s ResendCooldown:%s ProviderTimeout:%s PasswordResetTemplateID:%q PasswordResetTTL:%s PasswordResetResendCooldown:%s}",
		c.Enabled,
		c.Provider,
		c.TemplateID,
		c.TTL,
		c.ResendCooldown,
		c.ProviderTimeout,
		c.PasswordResetTemplateID,
		c.PasswordResetTTL,
		c.PasswordResetResendCooldown,
	)
}

func loadEmailVerificationConfig(lookup LookupEnv) (EmailVerificationConfig, error) {
	provider := trimmedLookup(lookup, "AUTH_EMAIL_PROVIDER")
	apiKey := trimmedLookup(lookup, "AUTH_RESEND_API_KEY")
	secret := trimmedLookup(lookup, "AUTH_EMAIL_VERIFICATION_SECRET")
	templateID := trimmedLookup(lookup, "AUTH_EMAIL_VERIFICATION_TEMPLATE_ID")
	passwordResetTemplateID := trimmedLookup(lookup, "AUTH_PASSWORD_RESET_TEMPLATE_ID")
	invitationTemplateID := trimmedLookup(lookup, "AUTH_INVITATION_TEMPLATE_ID")
	enabled := invitationTemplateID != "" || provider != "" || apiKey != "" || secret != "" || templateID != "" ||
		hasConfiguredValue(lookup, "AUTH_EMAIL_VERIFICATION_TTL_SECONDS") ||
		hasConfiguredValue(lookup, "AUTH_EMAIL_VERIFICATION_RESEND_COOLDOWN_SECONDS") ||
		hasConfiguredValue(lookup, "AUTH_PASSWORD_RESET_TTL_SECONDS") ||
		hasConfiguredValue(lookup, "AUTH_PASSWORD_RESET_RESEND_COOLDOWN_SECONDS") ||
		passwordResetTemplateID != "" ||
		hasConfiguredValue(lookup, "AUTH_EMAIL_PROVIDER_TIMEOUT_SECONDS")
	if !enabled {
		return EmailVerificationConfig{}, nil
	}
	if provider != "resend" {
		return EmailVerificationConfig{}, errors.New(
			"AUTH_EMAIL_PROVIDER must be resend when email verification is enabled",
		)
	}
	if apiKey == "" {
		return EmailVerificationConfig{}, errors.New(
			"AUTH_RESEND_API_KEY is required when email verification is enabled",
		)
	}
	if len(secret) < 32 {
		return EmailVerificationConfig{}, errors.New(
			"AUTH_EMAIL_VERIFICATION_SECRET must contain at least 32 bytes",
		)
	}
	// Templates live in the operator's own Resend account; their HTML ships
	// in docs/branding/emails.
	if templateID == "" {
		return EmailVerificationConfig{}, errors.New(
			"AUTH_EMAIL_VERIFICATION_TEMPLATE_ID is required when email verification is enabled",
		)
	}
	if passwordResetTemplateID == "" {
		return EmailVerificationConfig{}, errors.New(
			"AUTH_PASSWORD_RESET_TEMPLATE_ID is required when email verification is enabled",
		)
	}
	ttl, err := boundedSeconds(
		lookup, "AUTH_EMAIL_VERIFICATION_TTL_SECONDS", 10*time.Minute, time.Minute, 30*time.Minute,
	)
	if err != nil {
		return EmailVerificationConfig{}, err
	}
	cooldown, err := boundedSeconds(
		lookup, "AUTH_EMAIL_VERIFICATION_RESEND_COOLDOWN_SECONDS", time.Minute, 10*time.Second, 10*time.Minute,
	)
	if err != nil {
		return EmailVerificationConfig{}, err
	}
	if cooldown >= ttl {
		return EmailVerificationConfig{}, errors.New(
			"AUTH_EMAIL_VERIFICATION_RESEND_COOLDOWN_SECONDS must be shorter than AUTH_EMAIL_VERIFICATION_TTL_SECONDS",
		)
	}
	providerTimeout, err := boundedSeconds(
		lookup, "AUTH_EMAIL_PROVIDER_TIMEOUT_SECONDS", 10*time.Second, time.Second, 30*time.Second,
	)
	if err != nil {
		return EmailVerificationConfig{}, err
	}
	passwordResetTTL, err := boundedSeconds(
		lookup, "AUTH_PASSWORD_RESET_TTL_SECONDS", 30*time.Minute, 5*time.Minute, 24*time.Hour,
	)
	if err != nil {
		return EmailVerificationConfig{}, err
	}
	passwordResetCooldown, err := boundedSeconds(
		lookup, "AUTH_PASSWORD_RESET_RESEND_COOLDOWN_SECONDS", time.Minute, 10*time.Second, 10*time.Minute,
	)
	if err != nil {
		return EmailVerificationConfig{}, err
	}
	if passwordResetCooldown >= passwordResetTTL {
		return EmailVerificationConfig{}, errors.New(
			"AUTH_PASSWORD_RESET_RESEND_COOLDOWN_SECONDS must be shorter than AUTH_PASSWORD_RESET_TTL_SECONDS",
		)
	}
	return EmailVerificationConfig{
		Enabled:                     true,
		Provider:                    provider,
		ResendAPIKey:                newSecretString(apiKey),
		Secret:                      newSecretString(secret),
		TemplateID:                  templateID,
		InvitationTemplateID:        invitationTemplateID,
		TTL:                         ttl,
		ResendCooldown:              cooldown,
		ProviderTimeout:             providerTimeout,
		PasswordResetTemplateID:     passwordResetTemplateID,
		PasswordResetTTL:            passwordResetTTL,
		PasswordResetResendCooldown: passwordResetCooldown,
	}, nil
}

func trimmedLookup(lookup LookupEnv, key string) string {
	value, _ := lookup(key)
	return strings.TrimSpace(value)
}

func hasConfiguredValue(lookup LookupEnv, key string) bool {
	value, configured := lookup(key)
	return configured && strings.TrimSpace(value) != ""
}
