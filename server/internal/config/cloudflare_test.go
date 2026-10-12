package config

import "testing"

func TestLoadCloudflareRequiresCompleteRoutingConfiguration(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"CLOUDFLARE_API_TOKEN":       "test-token",
		"CLOUDFLARE_ACCOUNT_ID":      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"CLOUDFLARE_KV_NAMESPACE_ID": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"CLOUDFLARE_BASE_DOMAIN":     "apps.example.com",
	}
	if got, err := loadCloudflareConfig(
		configuredLookup(values),
	); err != nil || !got.Enabled ||
		got.BaseDomain != "apps.example.com" {
		t.Fatalf("loadCloudflareConfig() = %+v, %v", got, err)
	}
	for key := range values {
		partial := make(map[string]string)
		for other, value := range values {
			if other != key {
				partial[other] = value
			}
		}
		if _, err := loadCloudflareConfig(configuredLookup(partial)); err == nil {
			t.Errorf("missing %s accepted", key)
		}
	}
	for _, domain := range []string{"https://apps.example.com", "*.example.com", "example.com/path", "localhost", "127.0.0.1"} {
		values["CLOUDFLARE_BASE_DOMAIN"] = domain
		if _, err := loadCloudflareConfig(configuredLookup(values)); err == nil {
			t.Errorf("invalid base domain %q accepted", domain)
		}
	}
}
