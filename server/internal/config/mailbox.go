package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var mailboxDomainPattern = regexp.MustCompile(
	`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`,
)

type MailboxConfig struct {
	Enabled             bool
	ResendAPIKey        SecretString
	ResendWebhookSecret SecretString
	Domain              string
	RequestsPerSecond   int
	ProviderTimeout     time.Duration
}

func (c MailboxConfig) String() string {
	return c.safeString()
}

func (c MailboxConfig) GoString() string {
	return c.safeString()
}

func (c MailboxConfig) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, c.safeString())
}

func (c MailboxConfig) safeString() string {
	return fmt.Sprintf(
		"MailboxConfig{Enabled:%t ResendAPIKey:[REDACTED] ResendWebhookSecret:[REDACTED] Domain:%q RequestsPerSecond:%d ProviderTimeout:%s}",
		c.Enabled,
		c.Domain,
		c.RequestsPerSecond,
		c.ProviderTimeout,
	)
}

func loadMailboxConfig(lookup LookupEnv) (MailboxConfig, error) {
	apiKey := trimmedLookup(lookup, "MAILBOX_RESEND_API_KEY")
	webhookSecret := trimmedLookup(lookup, "MAILBOX_RESEND_WEBHOOK_SECRET")
	domain := trimmedLookup(lookup, "MAILBOX_DOMAIN")
	enabled := apiKey != "" || webhookSecret != "" || domain != "" ||
		hasConfiguredValue(lookup, "MAILBOX_REQUESTS_PER_SECOND") ||
		hasConfiguredValue(lookup, "MAILBOX_PROVIDER_TIMEOUT_SECONDS")
	if !enabled {
		return MailboxConfig{}, nil
	}
	if apiKey == "" || len(apiKey) > 4096 || strings.TrimSpace(apiKey) != apiKey {
		return MailboxConfig{}, errors.New("MAILBOX_RESEND_API_KEY is required when Mailbox is enabled")
	}
	if webhookSecret != "" && !validMailboxWebhookSecret(webhookSecret) {
		return MailboxConfig{}, errors.New(
			"MAILBOX_RESEND_WEBHOOK_SECRET must be a valid Resend whsec_ signing secret",
		)
	}
	if len(domain) > 253 || domain != strings.ToLower(domain) || !mailboxDomainPattern.MatchString(domain) {
		return MailboxConfig{}, errors.New("MAILBOX_DOMAIN must be a canonical lowercase DNS name")
	}
	requestsPerSecond, err := positiveInt(lookup, "MAILBOX_REQUESTS_PER_SECOND", 4)
	if err != nil {
		return MailboxConfig{}, err
	}
	if requestsPerSecond > 5 {
		return MailboxConfig{}, errors.New("MAILBOX_REQUESTS_PER_SECOND must not exceed 5")
	}
	providerTimeout, err := boundedSeconds(
		lookup,
		"MAILBOX_PROVIDER_TIMEOUT_SECONDS",
		10*time.Second,
		time.Second,
		30*time.Second,
	)
	if err != nil {
		return MailboxConfig{}, err
	}
	return MailboxConfig{
		Enabled: true, ResendAPIKey: newSecretString(apiKey),
		ResendWebhookSecret: newSecretString(webhookSecret), Domain: domain,
		RequestsPerSecond: requestsPerSecond, ProviderTimeout: providerTimeout,
	}, nil
}

func validMailboxWebhookSecret(value string) bool {
	if !strings.HasPrefix(value, "whsec_") {
		return false
	}
	secret, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "whsec_"))
	return err == nil && len(secret) >= 16
}
