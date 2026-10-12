package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type SocialPlatformConfig struct {
	Enabled      bool
	ClientID     string
	ClientSecret SecretString
}

type SocialConfig struct {
	CallbackOrigin     string
	TokenEncryptionKey SecretString
	GraphVersion       string
	Platforms          map[string]SocialPlatformConfig
}

func loadSocialConfig(lookup LookupEnv, environment string) (SocialConfig, error) {
	result := SocialConfig{
		CallbackOrigin:     strings.TrimRight(trimmedLookup(lookup, "SOCIAL_CALLBACK_ORIGIN"), "/"),
		TokenEncryptionKey: newSecretString(trimmedLookup(lookup, "SOCIAL_TOKEN_ENCRYPTION_KEY")),
		GraphVersion:       valueOrDefault(lookup, "SOCIAL_META_GRAPH_VERSION", "v23.0"),
		Platforms:          make(map[string]SocialPlatformConfig),
	}
	enabled := make(map[string]bool)
	for _, platform := range strings.Split(valueOrDefault(lookup, "SOCIAL_ENABLED_PLATFORMS", "x"), ",") {
		platform = strings.TrimSpace(platform)
		switch platform {
		case "x", "facebook", "instagram", "linkedin":
			enabled[platform] = true
		default:
			return result, errors.New("SOCIAL_ENABLED_PLATFORMS contains an unsupported platform")
		}
	}
	configured := false
	for _, platform := range []string{"x", "facebook", "instagram", "linkedin"} {
		prefix := "SOCIAL_" + strings.ToUpper(platform)
		item := SocialPlatformConfig{
			Enabled:      enabled[platform],
			ClientID:     trimmedLookup(lookup, prefix+"_CLIENT_ID"),
			ClientSecret: newSecretString(trimmedLookup(lookup, prefix+"_CLIENT_SECRET")),
		}
		if item.ClientID != "" || item.ClientSecret.Reveal() != "" {
			configured = true
			if item.ClientID == "" || item.ClientSecret.Reveal() == "" {
				return result, fmt.Errorf(
					"%s_CLIENT_ID and %s_CLIENT_SECRET must both be set",
					prefix,
					prefix,
				)
			}
		}
		result.Platforms[platform] = item
	}
	if !configured {
		return result, nil
	}
	key, err := base64.StdEncoding.DecodeString(result.TokenEncryptionKey.Reveal())
	if err != nil || len(key) != 32 {
		return result, errors.New("SOCIAL_TOKEN_ENCRYPTION_KEY must be a base64-encoded 32-byte key")
	}
	u, err := url.Parse(result.CallbackOrigin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return result, errors.New("SOCIAL_CALLBACK_ORIGIN must be an absolute origin without path or query")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && (environment != "local" || !loopback || u.Scheme != "http") {
		return result, errors.New("SOCIAL_CALLBACK_ORIGIN must use HTTPS outside local loopback development")
	}
	if !strings.HasPrefix(result.GraphVersion, "v") || strings.ContainsAny(result.GraphVersion, "/?# ") {
		return result, errors.New("SOCIAL_META_GRAPH_VERSION is invalid")
	}
	return result, nil
}
