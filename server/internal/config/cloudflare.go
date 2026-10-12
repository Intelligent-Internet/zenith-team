package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"

	"boardly.dev/boardly/server/pkg/redact"
)

type CloudflareConfig struct {
	Enabled     bool
	APIToken    string
	AccountID   string
	NamespaceID string
	BaseDomain  string
}

func (CloudflareConfig) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "config.CloudflareConfig")
}
func (CloudflareConfig) GoString() string     { return redact.TypeString("config.CloudflareConfig") }
func (CloudflareConfig) LogValue() slog.Value { return redact.TypeLogValue("config.CloudflareConfig") }

func loadCloudflareConfig(lookup LookupEnv) (CloudflareConfig, error) {
	configuration := CloudflareConfig{
		APIToken:    strings.TrimSpace(valueOrDefault(lookup, "CLOUDFLARE_API_TOKEN", "")),
		AccountID:   strings.TrimSpace(valueOrDefault(lookup, "CLOUDFLARE_ACCOUNT_ID", "")),
		NamespaceID: strings.TrimSpace(valueOrDefault(lookup, "CLOUDFLARE_KV_NAMESPACE_ID", "")),
		BaseDomain:  strings.ToLower(strings.TrimSpace(valueOrDefault(lookup, "CLOUDFLARE_BASE_DOMAIN", ""))),
	}
	if configuration.APIToken == "" && configuration.AccountID == "" && configuration.NamespaceID == "" &&
		configuration.BaseDomain == "" {
		return configuration, nil
	}
	identifier := regexp.MustCompile(`^[0-9a-f]{32}$`)
	if configuration.APIToken == "" || strings.ContainsAny(configuration.APIToken, "\r\n") ||
		!identifier.MatchString(configuration.AccountID) ||
		!identifier.MatchString(configuration.NamespaceID) {
		return CloudflareConfig{}, errors.New(
			"Cloudflare routing requires an API token, account ID, and KV namespace ID",
		)
	}
	label := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	if len(configuration.BaseDomain) > 189 || !strings.Contains(configuration.BaseDomain, ".") ||
		net.ParseIP(configuration.BaseDomain) != nil {
		return CloudflareConfig{}, errors.New("CLOUDFLARE_BASE_DOMAIN must be a DNS hostname")
	}
	for _, part := range strings.Split(configuration.BaseDomain, ".") {
		if !label.MatchString(part) {
			return CloudflareConfig{}, errors.New("CLOUDFLARE_BASE_DOMAIN must be a DNS hostname")
		}
	}
	configuration.Enabled = true
	return configuration, nil
}
