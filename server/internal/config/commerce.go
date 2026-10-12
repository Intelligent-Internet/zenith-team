package config

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	commerceReturnPath           = "/connect/stripe/return"
	commerceRefreshPath          = "/connect/stripe/refresh"
	commercePublicSiteOrigin     = "https://app.example.com"
	commerceTermsPath            = "/legal/commerce-addendum"
	commercePrivacyPath          = "/privacy"
	commerceTermsVersion         = "2026-09-11.1"
	maxStripeCredentialBytes     = 4096
	maxStripePublishableKeyBytes = 260
)

var (
	stripePlatformIDPattern  = regexp.MustCompile(`^acct_[A-Za-z0-9]{8,255}$`)
	commerceReleaseCountries = [...]string{"GB"}
)

// CommerceTermsConfig pins the one legal document release the server may accept.
type CommerceTermsConfig struct {
	Version     string
	SHA256      [sha256.Size]byte
	DocumentURL string
	PrivacyURL  string
}

// CommerceEnvironmentConfig holds one mode-isolated Stripe platform boundary.
type CommerceEnvironmentConfig struct {
	Enabled           bool
	ConnectKey        SecretString
	CommerceKey       SecretString
	ReconcileKey      SecretString
	PublishableKey    string
	WebhookSecret     SecretString
	PlatformAccountID string
}

func (c CommerceEnvironmentConfig) String() string {
	return c.safeString()
}

func (c CommerceEnvironmentConfig) GoString() string {
	return c.safeString()
}

func (c CommerceEnvironmentConfig) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, c.safeString())
}

func (c CommerceEnvironmentConfig) safeString() string {
	const format = "CommerceEnvironmentConfig{Enabled:%t ConnectKey:[REDACTED] " +
		"CommerceKey:[REDACTED] ReconcileKey:[REDACTED] " +
		"PublishableKey:%q WebhookSecret:[REDACTED] PlatformAccountID:[REDACTED]}"
	return fmt.Sprintf(
		format,
		c.Enabled,
		c.PublishableKey,
	)
}

// CommerceConfig governs Company seller-commerce onboarding and provider access.
type CommerceConfig struct {
	Configured                bool
	Enabled                   bool
	CustomerHMACKey           SecretString
	Terms                     CommerceTermsConfig
	Sandbox                   CommerceEnvironmentConfig
	Live                      CommerceEnvironmentConfig
	AllowedConnectedCountries []string
	ProviderTimeout           time.Duration
	ReturnURL                 string
	RefreshURL                string
}

func (c CommerceConfig) String() string {
	return c.safeString()
}

func (c CommerceConfig) GoString() string {
	return c.safeString()
}

func (c CommerceConfig) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, c.safeString())
}

func (c CommerceConfig) safeString() string {
	const format = "CommerceConfig{Configured:%t Enabled:%t CustomerHMACKey:[REDACTED] TermsVersion:%q TermsSHA256:%q " +
		"TermsDocumentURL:%q PrivacyURL:%q Sandbox:%s Live:%s AllowedConnectedCountries:%v ProviderTimeout:%s " +
		"ReturnURL:%q RefreshURL:%q}"
	digest := ""
	if c.Terms.SHA256 != [sha256.Size]byte{} {
		digest = hex.EncodeToString(c.Terms.SHA256[:])
	}
	return fmt.Sprintf(
		format,
		c.Configured,
		c.Enabled,
		c.Terms.Version,
		digest,
		c.Terms.DocumentURL,
		c.Terms.PrivacyURL,
		c.Sandbox.safeString(),
		c.Live.safeString(),
		c.AllowedConnectedCountries,
		c.ProviderTimeout,
		c.ReturnURL,
		c.RefreshURL,
	)
}

func loadCommerceConfig(
	lookup LookupEnv,
	appEnvironment string,
	webAppOrigin string,
	webOrigins []string,
) (CommerceConfig, error) {
	enabled, err := boolean(lookup, "COMMERCE_ENABLED", false)
	if err != nil {
		return CommerceConfig{}, err
	}
	providerTimeout, err := boundedSeconds(
		lookup,
		"COMMERCE_PROVIDER_TIMEOUT_SECONDS",
		10*time.Second,
		time.Second,
		30*time.Second,
	)
	if err != nil {
		return CommerceConfig{}, err
	}
	if hasConfiguredValue(lookup, "STRIPE_SECRET_KEY") {
		return CommerceConfig{}, errors.New(
			"STRIPE_SECRET_KEY is unsupported; configure scoped STRIPE_CONNECT_KEY, STRIPE_COMMERCE_KEY, and STRIPE_RECONCILE_KEY",
		)
	}
	stripeConfigured := hasAnyConfiguredValue(lookup,
		"STRIPE_CONNECT_KEY",
		"STRIPE_COMMERCE_KEY",
		"STRIPE_RECONCILE_KEY",
		"STRIPE_PUBLISHABLE_KEY",
		"STRIPE_WEBHOOK_SECRET",
		"STRIPE_PLATFORM_ACCOUNT_ID",
	)
	if enabled && !stripeConfigured {
		return CommerceConfig{}, errors.New(
			"COMMERCE_ENABLED=true requires the complete Stripe Commerce configuration",
		)
	}
	stripeConfig, stripeMode, err := loadCommerceEnvironmentConfig(lookup, stripeConfigured)
	if err != nil {
		return CommerceConfig{}, err
	}
	if stripeConfigured && appEnvironment == "production" && stripeMode != "live" {
		return CommerceConfig{}, errors.New(
			"production Commerce requires live Stripe credentials; test-mode Stripe credentials are not allowed",
		)
	}
	if stripeConfigured && appEnvironment != "production" && stripeMode == "live" {
		return CommerceConfig{}, errors.New(
			"live Stripe credentials are allowed only when APP_ENVIRONMENT=production",
		)
	}
	if !stripeConfigured {
		if hasAnyConfiguredValue(lookup, commerceReleaseConfigurationKeys()...) {
			return CommerceConfig{}, errors.New(
				"Commerce release configuration requires Stripe Commerce credentials",
			)
		}
		return CommerceConfig{Enabled: enabled, ProviderTimeout: providerTimeout}, nil
	}
	hmacKey, err := loadCommerceCustomerHMACKey(lookup)
	if err != nil {
		return CommerceConfig{}, err
	}

	if webAppOrigin == "" {
		return CommerceConfig{}, errors.New(
			"AUTH_WEB_APP_ORIGIN is required when Commerce is configured",
		)
	}
	parsedOrigin, err := url.Parse(webAppOrigin)
	isHTTPSOrigin := err == nil && parsedOrigin.Scheme == "https"
	isLocalLoopbackOrigin := err == nil && appEnvironment == "local" &&
		parsedOrigin.Scheme == "http" && isLoopbackHostname(parsedOrigin.Hostname())
	if !isHTTPSOrigin && !isLocalLoopbackOrigin {
		return CommerceConfig{}, errors.New(
			"AUTH_WEB_APP_ORIGIN must be an exact canonical HTTPS origin or local loopback HTTP origin when Commerce is enabled",
		)
	}
	if !containsString(webOrigins, webAppOrigin) {
		return CommerceConfig{}, errors.New(
			"AUTH_WEB_APP_ORIGIN must appear in AUTH_WEB_ORIGINS when Commerce is enabled",
		)
	}
	terms := bundledCommerceTermsConfig()
	allowedConnectedCountries, err := loadCommerceAllowedConnectedCountries(lookup)
	if err != nil {
		return CommerceConfig{}, err
	}
	returnURL := webAppOrigin + commerceReturnPath
	refreshURL := webAppOrigin + commerceRefreshPath
	if len(returnURL) > 2048 || len(refreshURL) > 2048 {
		return CommerceConfig{}, errors.New("Commerce redirect URL exceeds 2048 bytes")
	}
	configuration := CommerceConfig{
		Configured:                true,
		Enabled:                   enabled,
		CustomerHMACKey:           hmacKey,
		Terms:                     terms,
		AllowedConnectedCountries: allowedConnectedCountries,
		ProviderTimeout:           providerTimeout,
		ReturnURL:                 returnURL,
		RefreshURL:                refreshURL,
	}
	if stripeMode == "live" {
		configuration.Live = stripeConfig
	} else {
		configuration.Sandbox = stripeConfig
	}
	return configuration, nil
}

func loadCommerceAllowedConnectedCountries(lookup LookupEnv) ([]string, error) {
	const key = "COMMERCE_ALLOWED_CONNECTED_COUNTRIES"
	raw := exactLookup(lookup, key)
	expected := strings.Join(commerceReleaseCountries[:], ",")
	if raw != expected {
		return nil, fmt.Errorf(
			"%s must exactly match the release-pinned country policy %q",
			key,
			expected,
		)
	}
	return append([]string(nil), commerceReleaseCountries[:]...), nil
}

func bundledCommerceTermsConfig() CommerceTermsConfig {
	return CommerceTermsConfig{
		Version:     commerceTermsVersion,
		SHA256:      commerceTermsDigest(),
		DocumentURL: commercePublicSiteOrigin + commerceTermsPath,
		PrivacyURL:  commercePublicSiteOrigin + commercePrivacyPath,
	}
}

func commerceTermsDigest() [sha256.Size]byte {
	return [sha256.Size]byte{
		0x9d, 0x90, 0x90, 0x1b, 0xcb, 0x3a, 0x28, 0x7d,
		0x10, 0x45, 0xc9, 0xce, 0xcd, 0x01, 0x1a, 0x5c,
		0xe4, 0x96, 0x04, 0x83, 0xf3, 0x78, 0x85, 0x74,
		0x1f, 0x4f, 0x12, 0x11, 0x74, 0x29, 0x53, 0xd1,
	}
}

func loadCommerceEnvironmentConfig(
	lookup LookupEnv,
	enabled bool,
) (CommerceEnvironmentConfig, string, error) {
	const prefix = "STRIPE_"
	connectKey := exactLookup(lookup, prefix+"CONNECT_KEY")
	commerceKey := exactLookup(lookup, prefix+"COMMERCE_KEY")
	reconcileKey := exactLookup(lookup, prefix+"RECONCILE_KEY")
	publishableKey := exactLookup(lookup, prefix+"PUBLISHABLE_KEY")
	webhookSecret := exactLookup(lookup, prefix+"WEBHOOK_SECRET")
	platformAccountID := exactLookup(lookup, prefix+"PLATFORM_ACCOUNT_ID")
	configuredCount := countConfiguredValues(
		[]string{
			connectKey,
			commerceKey,
			reconcileKey,
			publishableKey,
			webhookSecret,
			platformAccountID,
		},
	)
	if !enabled {
		if configuredCount != 0 {
			return CommerceEnvironmentConfig{}, "", fmt.Errorf(
				"all %s* values must be unset when the Commerce environment is disabled",
				prefix,
			)
		}
		return CommerceEnvironmentConfig{}, "", nil
	}
	if configuredCount != 6 {
		return CommerceEnvironmentConfig{}, "", fmt.Errorf(
			"%sCONNECT_KEY, %sCOMMERCE_KEY, %sRECONCILE_KEY, %sPUBLISHABLE_KEY, %sWEBHOOK_SECRET, and %sPLATFORM_ACCOUNT_ID must be configured together",
			prefix,
			prefix,
			prefix,
			prefix,
			prefix,
			prefix,
		)
	}
	if connectKey == commerceKey || connectKey == reconcileKey || commerceKey == reconcileKey {
		return CommerceEnvironmentConfig{}, "", errors.New(
			"STRIPE_CONNECT_KEY, STRIPE_COMMERCE_KEY, and STRIPE_RECONCILE_KEY must be distinct restricted keys",
		)
	}
	keyMode := ""
	for _, candidate := range [...]string{"test", "live"} {
		if validStripeRestrictedKey(connectKey, candidate) &&
			validStripeRestrictedKey(commerceKey, candidate) &&
			validStripeRestrictedKey(reconcileKey, candidate) &&
			validStripePublishableKey(publishableKey, candidate) {
			keyMode = candidate
			break
		}
	}
	if keyMode == "" {
		return CommerceEnvironmentConfig{}, "", fmt.Errorf(
			"%sCONNECT_KEY, %sCOMMERCE_KEY, %sRECONCILE_KEY, and %sPUBLISHABLE_KEY must be valid restricted credentials in the same Stripe mode",
			prefix,
			prefix,
			prefix,
			prefix,
		)
	}
	if !validStripeWebhookSecret(webhookSecret) {
		return CommerceEnvironmentConfig{}, "", fmt.Errorf("%sWEBHOOK_SECRET is invalid", prefix)
	}
	if !stripePlatformIDPattern.MatchString(platformAccountID) {
		return CommerceEnvironmentConfig{}, "", fmt.Errorf("%sPLATFORM_ACCOUNT_ID is invalid", prefix)
	}
	return CommerceEnvironmentConfig{
		Enabled:    true,
		ConnectKey: newSecretString(connectKey), CommerceKey: newSecretString(commerceKey),
		ReconcileKey: newSecretString(reconcileKey), PublishableKey: publishableKey,
		WebhookSecret: newSecretString(webhookSecret), PlatformAccountID: platformAccountID,
	}, keyMode, nil
}

func commerceReleaseConfigurationKeys() []string {
	return []string{
		"COMMERCE_ALLOWED_CONNECTED_COUNTRIES",
		"COMMERCE_PROVIDER_TIMEOUT_SECONDS",
		"COMMERCE_CUSTOMER_HMAC_KEY",
	}
}

func loadCommerceCustomerHMACKey(lookup LookupEnv) (SecretString, error) {
	const key = "COMMERCE_CUSTOMER_HMAC_KEY"
	raw := exactLookup(lookup, key)
	decoded, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size {
		return SecretString{}, fmt.Errorf(
			"%s must be standard-base64 encoding of exactly 32 random bytes",
			key,
		)
	}
	return newSecretString(string(decoded)), nil
}

func hasAnyConfiguredValue(lookup LookupEnv, keys ...string) bool {
	for _, key := range keys {
		if hasConfiguredValue(lookup, key) {
			return true
		}
	}
	return false
}

func exactLookup(lookup LookupEnv, key string) string {
	value, _ := lookup(key)
	return value
}

func validStripeRestrictedKey(value, mode string) bool {
	restrictedPrefix := "rk_" + mode + "_"
	hasExpectedPrefix := strings.HasPrefix(value, restrictedPrefix)
	hasMinimumSuffix := len(value) >= len(restrictedPrefix)+8
	return validBoundedCredential(value, maxStripeCredentialBytes) &&
		hasExpectedPrefix && hasMinimumSuffix
}

func validStripePublishableKey(value, mode string) bool {
	prefix := "pk_" + mode + "_"
	hasExpectedPrefix := strings.HasPrefix(value, prefix)
	hasMinimumSuffix := len(value) >= len(prefix)+8
	return validBoundedCredential(value, maxStripePublishableKeyBytes) &&
		hasExpectedPrefix && hasMinimumSuffix
}

func validStripeWebhookSecret(value string) bool {
	const prefix = "whsec_"
	hasExpectedPrefix := strings.HasPrefix(value, prefix)
	hasMinimumSuffix := len(value) >= len(prefix)+8
	return validBoundedCredential(value, maxStripeCredentialBytes) &&
		hasExpectedPrefix && hasMinimumSuffix
}

func validBoundedCredential(value string, maximum int) bool {
	invalidBounds := value == "" || len(value) > maximum
	invalidEncoding := !utf8.ValidString(value)
	hasSurroundingWhitespace := strings.TrimSpace(value) != value
	if invalidBounds || invalidEncoding || hasSurroundingWhitespace {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}
