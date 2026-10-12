package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testStripeConnectKey    = "rk_test_FakeConnect"
	testStripeCommerceKey   = "rk_test_FakeRuntime"
	testStripeReconcileKey  = "rk_test_FakeReconcile"
	liveStripeConnectKey    = "rk_live_FakeConnect"
	liveStripeCommerceKey   = "rk_live_FakeRuntime"
	liveStripeReconcileKey  = "rk_live_FakeReconcile"
	testStripeWebhookSecret = "whsec_FakeSandboxSecret"
	testStripePlatformID    = "acct_BoardlySandboxPlatform"
	testCommerceHMACKey     = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="
)

func TestLoadDisablesCommerceByDefault(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Commerce.Configured || cfg.Commerce.Enabled || cfg.Commerce.Sandbox.Enabled ||
		cfg.Commerce.Live.Enabled {
		t.Fatalf("Commerce must stay disabled until explicitly enabled: %#v", cfg.Commerce)
	}
	if cfg.Commerce.ProviderTimeout != 10*time.Second {
		t.Errorf("Commerce.ProviderTimeout = %s, want 10s", cfg.Commerce.ProviderTimeout)
	}
	if cfg.Commerce.ReturnURL != "" || cfg.Commerce.RefreshURL != "" {
		t.Errorf("disabled Commerce redirect URLs = %q / %q", cfg.Commerce.ReturnURL, cfg.Commerce.RefreshURL)
	}
}

func TestLoadConfiguresSandboxCommerceWithoutExposingSecrets(t *testing.T) {
	t.Parallel()

	values := validSandboxCommerceConfig()
	values["COMMERCE_PROVIDER_TIMEOUT_SECONDS"] = "12"
	cfg, err := Load(configuredLookup(values))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.Commerce.Configured || !cfg.Commerce.Enabled ||
		!cfg.Commerce.Sandbox.Enabled || cfg.Commerce.Live.Enabled {
		t.Fatalf("Commerce environment switches = %#v", cfg.Commerce)
	}
	if cfg.Commerce.Sandbox.ConnectKey.Reveal() != testStripeConnectKey ||
		cfg.Commerce.Sandbox.CommerceKey.Reveal() != testStripeCommerceKey ||
		cfg.Commerce.Sandbox.ReconcileKey.Reveal() != testStripeReconcileKey ||
		cfg.Commerce.Sandbox.PublishableKey != "pk_test_51BoardlyCommerceSandboxPublic" ||
		cfg.Commerce.Sandbox.WebhookSecret.Reveal() != testStripeWebhookSecret ||
		cfg.Commerce.Sandbox.PlatformAccountID != testStripePlatformID {
		t.Fatal("sandbox Stripe credentials were not retained at the integration boundary")
	}
	if cfg.Commerce.CustomerHMACKey.Reveal() != strings.Repeat("B", 32) {
		t.Fatal("Commerce customer HMAC key was not decoded at the integration boundary")
	}
	if cfg.Commerce.Terms.Version != commerceTermsVersion ||
		cfg.Commerce.Terms.SHA256 != commerceTermsDigest() ||
		cfg.Commerce.Terms.DocumentURL != "https://app.example.com/legal/commerce-addendum" ||
		cfg.Commerce.Terms.PrivacyURL != "https://app.example.com/privacy" {
		t.Errorf("Commerce terms = %#v", cfg.Commerce.Terms)
	}
	if cfg.Commerce.ReturnURL != "https://app.boardly.test/connect/stripe/return" ||
		cfg.Commerce.RefreshURL != "https://app.boardly.test/connect/stripe/refresh" {
		t.Errorf("Commerce redirect URLs = %q / %q", cfg.Commerce.ReturnURL, cfg.Commerce.RefreshURL)
	}
	if cfg.Commerce.ProviderTimeout != 12*time.Second {
		t.Errorf("Commerce.ProviderTimeout = %s, want 12s", cfg.Commerce.ProviderTimeout)
	}
	if len(cfg.Commerce.AllowedConnectedCountries) != 1 ||
		cfg.Commerce.AllowedConnectedCountries[0] != "GB" {
		t.Errorf("Commerce.AllowedConnectedCountries = %v, want [GB]", cfg.Commerce.AllowedConnectedCountries)
	}

	formatted := fmt.Sprintf(
		"%v %+v %#v %s %q",
		cfg.Commerce,
		cfg.Commerce,
		cfg.Commerce,
		cfg.Commerce,
		cfg.Commerce,
	)
	if strings.Contains(formatted, testStripeConnectKey) ||
		strings.Contains(formatted, testStripeCommerceKey) ||
		strings.Contains(formatted, testStripeReconcileKey) ||
		strings.Contains(
			formatted,
			testStripeWebhookSecret,
		) || strings.Contains(formatted, testStripePlatformID) ||
		strings.Contains(formatted, strings.Repeat("B", 32)) {
		t.Fatalf("formatted Commerce config leaked sandbox secret: %s", formatted)
	}
	formattedEnvironment := fmt.Sprintf(
		"%v %+v %#v",
		cfg.Commerce.Sandbox,
		cfg.Commerce.Sandbox,
		cfg.Commerce.Sandbox,
	)
	if strings.Contains(formattedEnvironment, testStripeConnectKey) ||
		strings.Contains(formattedEnvironment, testStripeCommerceKey) ||
		strings.Contains(formattedEnvironment, testStripeReconcileKey) ||
		strings.Contains(formattedEnvironment, testStripeWebhookSecret) ||
		strings.Contains(formattedEnvironment, testStripePlatformID) {
		t.Fatalf("formatted Commerce environment leaked sandbox secret: %s", formattedEnvironment)
	}
	formattedRoot := fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg)
	if strings.Contains(formattedRoot, testStripeConnectKey) ||
		strings.Contains(formattedRoot, testStripeCommerceKey) ||
		strings.Contains(formattedRoot, testStripeReconcileKey) ||
		strings.Contains(formattedRoot, testStripeWebhookSecret) ||
		strings.Contains(formattedRoot, testStripePlatformID) ||
		strings.Contains(formattedRoot, strings.Repeat("B", 32)) {
		t.Fatalf("formatted root config leaked Commerce provider authority: %s", formattedRoot)
	}
}

func TestLoadConfiguresSandboxCommerceForLocalDesktopLoopback(t *testing.T) {
	t.Parallel()

	for _, origin := range []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
	} {
		t.Run(origin, func(t *testing.T) {
			t.Parallel()

			values := validSandboxCommerceConfig()
			values["APP_ENVIRONMENT"] = "local"
			values["AUTH_WEB_ORIGINS"] = origin
			values["AUTH_WEB_APP_ORIGIN"] = origin

			cfg, err := Load(configuredLookup(values))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Commerce.ReturnURL != origin+"/connect/stripe/return" ||
				cfg.Commerce.RefreshURL != origin+"/connect/stripe/refresh" {
				t.Errorf(
					"Commerce redirect URLs = %q / %q",
					cfg.Commerce.ReturnURL,
					cfg.Commerce.RefreshURL,
				)
			}
			if cfg.Commerce.Terms.DocumentURL != "https://app.example.com/legal/commerce-addendum" ||
				cfg.Commerce.Terms.PrivacyURL != "https://app.example.com/privacy" {
				t.Errorf(
					"Commerce legal URLs = %q / %q",
					cfg.Commerce.Terms.DocumentURL,
					cfg.Commerce.Terms.PrivacyURL,
				)
			}
		})
	}
}

func TestCommerceTermsReleaseMatchesPublicLandingArtifact(t *testing.T) {
	t.Parallel()

	artifactPath := filepath.Join(
		"..", "..", "..", "frontend", "src", "features", "public-site", "content",
		"commerce-addendum.json",
	)
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", artifactPath, err)
	}
	var release struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(artifact, &release); err != nil {
		t.Fatalf("decode Commerce Addendum artifact: %v", err)
	}
	if release.Version != commerceTermsVersion {
		t.Fatalf("Commerce Addendum version = %q, want %q", release.Version, commerceTermsVersion)
	}
	if digest := sha256.Sum256(artifact); digest != commerceTermsDigest() {
		expectedDigest := commerceTermsDigest()
		t.Fatalf(
			"Commerce Addendum SHA-256 = %s, want %s",
			hex.EncodeToString(digest[:]),
			hex.EncodeToString(expectedDigest[:]),
		)
	}
}

func TestLoadConfiguresLiveCommerceInProduction(t *testing.T) {
	t.Parallel()

	values := validSandboxCommerceConfig()
	values["APP_ENVIRONMENT"] = "production"
	values["STRIPE_CONNECT_KEY"] = liveStripeConnectKey
	values["STRIPE_COMMERCE_KEY"] = liveStripeCommerceKey
	values["STRIPE_RECONCILE_KEY"] = liveStripeReconcileKey
	values["STRIPE_PUBLISHABLE_KEY"] = "pk_live_51BoardlyCommerceProductionPublic"
	values["STRIPE_WEBHOOK_SECRET"] = "whsec_FakeProductionSecret"
	values["STRIPE_PLATFORM_ACCOUNT_ID"] = "acct_BoardlyProductionPlatform"
	cfg, err := Load(configuredLookup(values))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Commerce.Enabled || cfg.Commerce.Sandbox.Enabled || !cfg.Commerce.Live.Enabled {
		t.Fatalf("Commerce environments = %#v", cfg.Commerce)
	}
	if cfg.Commerce.Live.ConnectKey.Reveal() != liveStripeConnectKey ||
		cfg.Commerce.Live.CommerceKey.Reveal() != liveStripeCommerceKey ||
		cfg.Commerce.Live.ReconcileKey.Reveal() != liveStripeReconcileKey {
		t.Fatal("live restricted keys were not retained at the integration boundary")
	}
}

func TestLoadRejectsLegacyStripeSecretKey(t *testing.T) {
	t.Parallel()

	values := validSandboxCommerceConfig()
	values["STRIPE_SECRET_KEY"] = "sk_test_legacyBoardlySecret"
	_, err := Load(configuredLookup(values))
	if err == nil || !strings.Contains(err.Error(), "STRIPE_SECRET_KEY") {
		t.Fatalf("Load() error = %v, want legacy key rejection", err)
	}
}

func TestLoadRejectsSandboxStripeKeysInProduction(t *testing.T) {
	t.Parallel()

	values := validSandboxCommerceConfig()
	values["APP_ENVIRONMENT"] = "production"
	_, err := Load(configuredLookup(values))
	if err == nil || !strings.Contains(err.Error(), "test-mode Stripe") {
		t.Fatalf("Load() error = %v, want production mode rejection", err)
	}
}

func TestLoadRejectsLiveStripeKeysOutsideProduction(t *testing.T) {
	t.Parallel()

	values := validSandboxCommerceConfig()
	values["APP_ENVIRONMENT"] = "local"
	values["STRIPE_CONNECT_KEY"] = liveStripeConnectKey
	values["STRIPE_COMMERCE_KEY"] = liveStripeCommerceKey
	values["STRIPE_RECONCILE_KEY"] = liveStripeReconcileKey
	values["STRIPE_PUBLISHABLE_KEY"] = "pk_live_51BoardlyCommerceProductionPublic"
	_, err := Load(configuredLookup(values))
	if err == nil || !strings.Contains(err.Error(), "live Stripe credentials") {
		t.Fatalf("Load() error = %v, want non-production live-mode rejection", err)
	}
}

func TestLoadRejectsPartialOrModeMismatchedCommerceConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{name: "sandbox enabled without Connect key", mutate: func(values map[string]string) {
			delete(values, "STRIPE_CONNECT_KEY")
		}},
		{name: "sandbox enabled without Commerce key", mutate: func(values map[string]string) {
			delete(values, "STRIPE_COMMERCE_KEY")
		}},
		{name: "sandbox enabled without Reconcile key", mutate: func(values map[string]string) {
			delete(values, "STRIPE_RECONCILE_KEY")
		}},
		{name: "sandbox enabled without publishable key", mutate: func(values map[string]string) {
			delete(values, "STRIPE_PUBLISHABLE_KEY")
		}},
		{name: "sandbox enabled without webhook secret", mutate: func(values map[string]string) {
			delete(values, "STRIPE_WEBHOOK_SECRET")
		}},
		{name: "sandbox enabled without platform account id", mutate: func(values map[string]string) {
			delete(values, "STRIPE_PLATFORM_ACCOUNT_ID")
		}},
		{name: "sandbox missing customer HMAC key", mutate: func(values map[string]string) {
			delete(values, "COMMERCE_CUSTOMER_HMAC_KEY")
		}},
		{name: "sandbox invalid customer HMAC key", mutate: func(values map[string]string) {
			values["COMMERCE_CUSTOMER_HMAC_KEY"] = "not-base64"
		}},
		{name: "sandbox reuses Commerce key for reconciliation", mutate: func(values map[string]string) {
			values["STRIPE_RECONCILE_KEY"] = values["STRIPE_COMMERCE_KEY"]
		}},
		{name: "sandbox uses live Connect key", mutate: func(values map[string]string) {
			values["STRIPE_CONNECT_KEY"] = liveStripeConnectKey
		}},
		{name: "sandbox uses secret instead of restricted Connect key", mutate: func(values map[string]string) {
			values["STRIPE_CONNECT_KEY"] = "sk_test_FakeSharedSecret"
		}},
		{name: "sandbox uses live publishable key", mutate: func(values map[string]string) {
			values["STRIPE_PUBLISHABLE_KEY"] = "pk_live_51BoardlyCommerceProductionPublic"
		}},
		{name: "sandbox webhook secret is invalid", mutate: func(values map[string]string) {
			values["STRIPE_WEBHOOK_SECRET"] = "not-a-webhook-secret"
		}},
		{name: "sandbox platform account id is invalid", mutate: func(values map[string]string) {
			values["STRIPE_PLATFORM_ACCOUNT_ID"] = "customer_not_an_account"
		}},
		{name: "connected country policy is missing", mutate: func(values map[string]string) {
			delete(values, "COMMERCE_ALLOWED_CONNECTED_COUNTRIES")
		}},
		{name: "connected country policy is not canonical", mutate: func(values map[string]string) {
			values["COMMERCE_ALLOWED_CONNECTED_COUNTRIES"] = "GB,gb"
		}},
		{name: "connected country policy contains duplicates", mutate: func(values map[string]string) {
			values["COMMERCE_ALLOWED_CONNECTED_COUNTRIES"] = "GB,GB"
		}},
		{name: "connected country policy is broader than release", mutate: func(values map[string]string) {
			values["COMMERCE_ALLOWED_CONNECTED_COUNTRIES"] = "GB,US"
		}},
		{name: "non-loopback web app origin is not HTTPS", mutate: func(values map[string]string) {
			values["AUTH_WEB_ORIGINS"] = "http://app.boardly.test"
			values["AUTH_WEB_APP_ORIGIN"] = "http://app.boardly.test"
		}},
		{name: "production loopback web app origin is not HTTPS", mutate: func(values map[string]string) {
			values["APP_ENVIRONMENT"] = "production"
			values["AUTH_WEB_ORIGINS"] = "http://localhost:5173"
			values["AUTH_WEB_APP_ORIGIN"] = "http://localhost:5173"
		}},
		{name: "web app origin is outside allowed origins", mutate: func(values map[string]string) {
			values["AUTH_WEB_ORIGINS"] = "https://other.boardly.test"
		}},
		{name: "provider timeout exceeds bound", mutate: func(values map[string]string) {
			values["COMMERCE_PROVIDER_TIMEOUT_SECONDS"] = "31"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			values := validSandboxCommerceConfig()
			test.mutate(values)
			if _, err := Load(configuredLookup(values)); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestLoadKeepsPortalAndWebhookConfiguredWhenCatalogKillSwitchIsOff(t *testing.T) {
	t.Parallel()

	values := validSandboxCommerceConfig()
	values["COMMERCE_ENABLED"] = "false"
	cfg, err := Load(configuredLookup(values))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Commerce.Configured || cfg.Commerce.Enabled || !cfg.Commerce.Sandbox.Enabled {
		t.Fatalf("Commerce kill-switch config = %#v", cfg.Commerce)
	}
}

func TestLoadRejectsCommerceValuesWithoutAnEnabledEnvironment(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "provider timeout", key: "COMMERCE_PROVIDER_TIMEOUT_SECONDS", value: "12"},
		{name: "Stripe publishable key", key: "STRIPE_PUBLISHABLE_KEY", value: "pk_test_example"},
		{name: "Stripe Connect key", key: "STRIPE_CONNECT_KEY", value: testStripeConnectKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Load(configuredLookup(map[string]string{
				"APP_DATABASE_URL": testDatabaseURL,
				test.key:           test.value,
			})); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

const testDatabaseURL = "postgres://boardly:boardly@localhost/boardly"

func validSandboxCommerceConfig() map[string]string {
	return map[string]string{
		"APP_DATABASE_URL":                     testDatabaseURL,
		"AUTH_WEB_ORIGINS":                     "https://app.boardly.test",
		"AUTH_WEB_APP_ORIGIN":                  "https://app.boardly.test",
		"COMMERCE_ENABLED":                     "true",
		"COMMERCE_ALLOWED_CONNECTED_COUNTRIES": "GB",
		"COMMERCE_CUSTOMER_HMAC_KEY":           testCommerceHMACKey,
		"STRIPE_CONNECT_KEY":                   testStripeConnectKey,
		"STRIPE_COMMERCE_KEY":                  testStripeCommerceKey,
		"STRIPE_RECONCILE_KEY":                 testStripeReconcileKey,
		"STRIPE_PUBLISHABLE_KEY":               "pk_test_51BoardlyCommerceSandboxPublic",
		"STRIPE_WEBHOOK_SECRET":                testStripeWebhookSecret,
		"STRIPE_PLATFORM_ACCOUNT_ID":           testStripePlatformID,
	}
}
