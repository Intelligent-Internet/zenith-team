package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSocialPlatformsRequireExplicitReleaseAndCredentials(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"SOCIAL_ENABLED_PLATFORMS":       "x,instagram",
		"SOCIAL_TOKEN_ENCRYPTION_KEY":    base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"SOCIAL_CALLBACK_ORIGIN":         "http://localhost:8788",
		"SOCIAL_X_CLIENT_ID":             "id",
		"SOCIAL_X_CLIENT_SECRET":         "private",
		"SOCIAL_INSTAGRAM_CLIENT_ID":     "ig",
		"SOCIAL_INSTAGRAM_CLIENT_SECRET": "private",
	}
	lookup := func(key string) (string, bool) { v, ok := values[key]; return v, ok }
	config, err := loadSocialConfig(lookup, "local")
	if err != nil || !config.Platforms["instagram"].Enabled {
		t.Fatalf("config = %+v, %v", config, err)
	}
	values["SOCIAL_CALLBACK_ORIGIN"] = "http://untrusted.example"
	if _, err := loadSocialConfig(lookup, "production"); err == nil {
		t.Fatal("insecure callback accepted")
	}
	values["SOCIAL_CALLBACK_ORIGIN"] = "https://api.example"
	delete(values, "SOCIAL_TOKEN_ENCRYPTION_KEY")
	if _, err := loadSocialConfig(lookup, "production"); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("missing key error = %v", err)
	}
}
