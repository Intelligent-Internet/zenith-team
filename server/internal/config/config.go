// Package config loads and validates process configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/util"
)

type LookupEnv func(string) (string, bool)

type StorageConfig struct {
	Bucket          string
	Region          string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	UsePathStyle    bool
}

type AttachmentConfig struct {
	AbandonedReadyGrace time.Duration
}

type LiveKitConfig struct {
	AIEnabled        bool
	AIWorkloadSecret string
	URL              string
	APIKey           string
	APISecret        string
}

type DatabaseConfig struct {
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

type AuthConfig struct {
	AccessTTL             time.Duration
	RefreshTTL            time.Duration
	CookieSecure          bool
	WebAppOrigin          string
	Google                GoogleOAuthConfig
	EmailVerification     EmailVerificationConfig
	DesktopHandoffCodeTTL time.Duration
}

type GoogleOAuthConfig struct {
	Enabled                          bool
	WebClientID                      string
	WebClientSecret                  string
	WebRedirectURI                   string
	DesktopClientID                  string
	DesktopClientSecret              string
	FlowTTL                          time.Duration
	ProviderTimeout                  time.Duration
	AllowInsecureLoopbackWebCallback bool
}

type Config struct {
	StudioAPIOrigin        string
	StudioReceiptNamespace uuid.UUID
	Name                   string
	Environment            string
	HTTPAddr               string
	DatabaseURL            string
	Database               DatabaseConfig
	LogLevel               string
	CSRFSecret             string
	ShutdownTimeout        time.Duration
	APIReplicas            int
	RedisURL               string
	RealtimeMetricsToken   string
	// DaemonIDRequired makes daemon-plane runtime routes refuse a request
	// that carries no X-Boardly-Daemon-ID (400 invalid_request). Off is the
	// warn-only rollout stage: a missing header is admitted and logged.
	DaemonIDRequired bool
	Auth             AuthConfig
	WebOrigins       []string
	// TrustedProxies is the CIDR set whose X-Forwarded-* headers HTTP and
	// realtime boundaries honor. Empty means "trust no proxy".
	TrustedProxies []netip.Prefix
	Storage        StorageConfig
	Attachment     AttachmentConfig
	LiveKit        LiveKitConfig
	CloudRun       CloudRunConfig
	Cloudflare     CloudflareConfig
	Mailbox        MailboxConfig
	Commerce       CommerceConfig
	Social         SocialConfig
}

func Load(lookup LookupEnv) (Config, error) {
	appName := valueOrDefault(lookup, "APP_NAME", "Zenith Team API")
	appEnvironment := strings.ToLower(valueOrDefault(lookup, "APP_ENVIRONMENT", "local"))
	switch appEnvironment {
	case "local", "staging", "production":
	default:
		return Config{}, errors.New("APP_ENVIRONMENT must be local, staging, or production")
	}
	databaseURL, _ := lookup("APP_DATABASE_URL")
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return Config{}, errors.New("APP_DATABASE_URL is required")
	}
	csrfSecret, ok := lookup("AUTH_CSRF_SECRET")
	if !ok || len([]byte(csrfSecret)) < 32 {
		return Config{}, errors.New("AUTH_CSRF_SECRET must contain at least 32 bytes")
	}
	logLevel := strings.ToLower(valueOrDefault(lookup, "APP_LOG_LEVEL", "info"))
	if logLevel != "debug" && logLevel != "info" && logLevel != "warn" && logLevel != "error" {
		return Config{}, errors.New("APP_LOG_LEVEL must be debug, info, warn, or error")
	}
	webOrigins, err := browserOrigins(lookup)
	if err != nil {
		return Config{}, err
	}

	apiReplicas, err := positiveInt(lookup, "APP_API_REPLICAS", 1)
	if err != nil {
		return Config{}, err
	}
	redisURL := valueOrDefault(lookup, "APP_REDIS_URL", "")
	if apiReplicas > 1 && redisURL == "" {
		return Config{}, errors.New("APP_REDIS_URL is required when APP_API_REPLICAS is greater than 1")
	}
	shutdownTimeout, err := positiveSeconds(lookup, "APP_SHUTDOWN_TIMEOUT_SECONDS", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	authAccessTTL, err := positiveSeconds(lookup, "AUTH_ACCESS_TOKEN_TTL_SECONDS", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	authRefreshTTL, err := positiveSeconds(lookup, "AUTH_REFRESH_TOKEN_TTL_SECONDS", 7*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	if authRefreshTTL <= authAccessTTL {
		return Config{}, errors.New("AUTH_REFRESH_TOKEN_TTL_SECONDS must exceed AUTH_ACCESS_TOKEN_TTL_SECONDS")
	}
	desktopHandoffCodeTTL, err := boundedSeconds(
		lookup,
		"AUTH_DESKTOP_HANDOFF_CODE_TTL_SECONDS",
		60*time.Second,
		10*time.Second,
		5*time.Minute,
	)
	if err != nil {
		return Config{}, err
	}
	requiresSecureCookie := false
	for _, origin := range webOrigins {
		parsed, _ := url.Parse(origin)
		requiresSecureCookie = requiresSecureCookie || parsed.Scheme == "https"
	}
	cookieSecure := requiresSecureCookie
	if rawCookieSecure, configured := lookup(
		"AUTH_COOKIE_SECURE",
	); configured &&
		strings.TrimSpace(rawCookieSecure) != "" {
		cookieSecure, err = strconv.ParseBool(strings.TrimSpace(rawCookieSecure))
		if err != nil {
			return Config{}, errors.New("AUTH_COOKIE_SECURE must be a boolean")
		}
	}
	if requiresSecureCookie && !cookieSecure {
		return Config{}, errors.New(
			"AUTH_COOKIE_SECURE cannot be false when an HTTPS browser origin is configured",
		)
	}
	webAppOrigin, googleOAuth, err := loadGoogleOAuthConfig(lookup, webOrigins, cookieSecure)
	if err != nil {
		return Config{}, err
	}
	emailVerification, err := loadEmailVerificationConfig(lookup)
	if err != nil {
		return Config{}, err
	}
	if emailVerification.Enabled && webAppOrigin == "" {
		return Config{}, errors.New("AUTH_WEB_APP_ORIGIN is required when email authentication is enabled")
	}
	if emailVerification.Enabled && !containsString(webOrigins, webAppOrigin) {
		return Config{}, errors.New(
			"AUTH_WEB_APP_ORIGIN must appear in AUTH_WEB_ORIGINS when email authentication is enabled",
		)
	}
	commerceConfig, err := loadCommerceConfig(
		lookup,
		appEnvironment,
		webAppOrigin,
		webOrigins,
	)
	if err != nil {
		return Config{}, err
	}
	socialConfig, err := loadSocialConfig(lookup, appEnvironment)
	if err != nil {
		return Config{}, err
	}
	mailboxConfig, err := loadMailboxConfig(lookup)
	if err != nil {
		return Config{}, err
	}
	cloudRunConfig, err := loadCloudRunConfig(lookup)
	if err != nil {
		return Config{}, err
	}
	cloudflareConfig, err := loadCloudflareConfig(lookup)
	if err != nil {
		return Config{}, err
	}
	databaseMaxConns, err := positiveInt32(lookup, "APP_DATABASE_MAX_CONNS", 25)
	if err != nil {
		return Config{}, err
	}
	databaseMinConns, err := positiveInt32(lookup, "APP_DATABASE_MIN_CONNS", 5)
	if err != nil {
		return Config{}, err
	}
	if databaseMinConns > databaseMaxConns {
		return Config{}, errors.New("APP_DATABASE_MIN_CONNS must not exceed APP_DATABASE_MAX_CONNS")
	}
	databaseMaxConnLifetime, err := positiveSeconds(lookup, "APP_DATABASE_MAX_CONN_LIFETIME_SECONDS", time.Hour)
	if err != nil {
		return Config{}, err
	}
	databaseMaxConnIdleTime, err := positiveSeconds(
		lookup,
		"APP_DATABASE_MAX_CONN_IDLE_TIME_SECONDS",
		30*time.Minute,
	)
	if err != nil {
		return Config{}, err
	}
	databaseHealthCheckPeriod, err := positiveSeconds(
		lookup,
		"APP_DATABASE_HEALTH_CHECK_PERIOD_SECONDS",
		time.Minute,
	)
	if err != nil {
		return Config{}, err
	}
	storageConfig, err := loadStorageConfig(lookup, appEnvironment)
	if err != nil {
		return Config{}, err
	}
	attachmentAbandonedReadyGrace, err := boundedSeconds(
		lookup,
		"ATTACHMENT_ABANDONED_READY_GRACE_SECONDS",
		24*time.Hour,
		time.Hour,
		30*24*time.Hour,
	)
	if err != nil {
		return Config{}, err
	}
	daemonIDRequired, err := boolean(lookup, "APP_DAEMON_ID_REQUIRED", false)
	if err != nil {
		return Config{}, err
	}
	studioOrigin, err := util.CanonicalStudioOrigin(
		valueOrDefault(lookup, "ZENITH_STUDIO_API_URL", util.DefaultStudioOrigin),
	)
	if err != nil {
		return Config{}, fmt.Errorf("ZENITH_STUDIO_API_URL: %w", err)
	}
	var studioNamespace uuid.UUID
	if raw, present := lookup("APP_STUDIO_RECEIPT_NAMESPACE"); present && strings.TrimSpace(raw) != "" {
		studioNamespace, err = uuid.Parse(strings.TrimSpace(raw))
		if err != nil || studioNamespace == uuid.Nil {
			return Config{}, errors.New("APP_STUDIO_RECEIPT_NAMESPACE must be a nonzero UUID")
		}
	}
	aiEnabled, err := boolean(lookup, "BOARDLY_HUDDLE_AI_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	aiSecret := valueOrDefault(lookup, "BOARDLY_HUDDLE_AI_WORKLOAD_SECRET", "")
	if aiEnabled && len(aiSecret) < 32 {
		return Config{}, errors.New("BOARDLY_HUDDLE_AI_WORKLOAD_SECRET must contain at least 32 bytes when AI is enabled")
	}
	liveKitConfig := LiveKitConfig{
		AIEnabled: aiEnabled, AIWorkloadSecret: aiSecret,
		URL:       valueOrDefault(lookup, "BOARDLY_LIVEKIT_URL", ""),
		APIKey:    valueOrDefault(lookup, "BOARDLY_LIVEKIT_API_KEY", ""),
		APISecret: valueOrDefault(lookup, "BOARDLY_LIVEKIT_API_SECRET", ""),
	}
	if aiEnabled && (liveKitConfig.URL == "" || liveKitConfig.APIKey == "" || liveKitConfig.APISecret == "") {
		return Config{}, errors.New("LiveKit configuration is required when huddle AI is enabled")
	}
	return Config{
		StudioReceiptNamespace: studioNamespace,
		StudioAPIOrigin:        studioOrigin,
		Name:                   appName,
		Environment:            appEnvironment,
		HTTPAddr:               valueOrDefault(lookup, "APP_HTTP_ADDR", ":8788"),
		DatabaseURL:            databaseURL,
		Database: DatabaseConfig{
			MaxConns:          databaseMaxConns,
			MinConns:          databaseMinConns,
			MaxConnLifetime:   databaseMaxConnLifetime,
			MaxConnIdleTime:   databaseMaxConnIdleTime,
			HealthCheckPeriod: databaseHealthCheckPeriod,
		},
		LogLevel:             logLevel,
		CSRFSecret:           csrfSecret,
		ShutdownTimeout:      shutdownTimeout,
		APIReplicas:          apiReplicas,
		RedisURL:             redisURL,
		RealtimeMetricsToken: valueOrDefault(lookup, "APP_REALTIME_METRICS_TOKEN", ""),
		DaemonIDRequired:     daemonIDRequired,
		Auth: AuthConfig{
			AccessTTL:             authAccessTTL,
			RefreshTTL:            authRefreshTTL,
			CookieSecure:          cookieSecure,
			WebAppOrigin:          webAppOrigin,
			Google:                googleOAuth,
			EmailVerification:     emailVerification,
			DesktopHandoffCodeTTL: desktopHandoffCodeTTL,
		},
		WebOrigins:     webOrigins,
		TrustedProxies: trustedProxies(lookup),
		Storage:        storageConfig,
		Attachment:     AttachmentConfig{AbandonedReadyGrace: attachmentAbandonedReadyGrace},
		LiveKit:        liveKitConfig,
		CloudRun:       cloudRunConfig,
		Cloudflare:     cloudflareConfig,
		Mailbox:        mailboxConfig,
		Commerce:       commerceConfig,
		Social:         socialConfig,
	}, nil
}

// googleWebCallbackPath must match the route the handler registers
// (googleOAuthWebCallbackPath in internal/handler/google_oauth.go).
const googleWebCallbackPath = "/api/v1/auth/google/web/callback"

func loadGoogleOAuthConfig(
	lookup LookupEnv,
	webOrigins []string,
	cookieSecure bool,
) (string, GoogleOAuthConfig, error) {
	flowTTL, err := boundedSeconds(
		lookup,
		"GOOGLE_OAUTH_FLOW_TTL_SECONDS",
		10*time.Minute,
		time.Minute,
		15*time.Minute,
	)
	if err != nil {
		return "", GoogleOAuthConfig{}, err
	}
	providerTimeout, err := boundedSeconds(
		lookup,
		"GOOGLE_OAUTH_PROVIDER_TIMEOUT_SECONDS",
		10*time.Second,
		time.Second,
		30*time.Second,
	)
	if err != nil {
		return "", GoogleOAuthConfig{}, err
	}

	webAppOrigin, err := configuredWebAppOrigin(lookup)
	if err != nil {
		return "", GoogleOAuthConfig{}, err
	}
	google := GoogleOAuthConfig{
		FlowTTL:         flowTTL,
		ProviderTimeout: providerTimeout,
	}
	credentialKeys := []string{
		"GOOGLE_WEB_CLIENT_ID",
		"GOOGLE_WEB_CLIENT_SECRET",
		"GOOGLE_WEB_REDIRECT_URI",
		"GOOGLE_DESKTOP_CLIENT_ID",
		"GOOGLE_DESKTOP_CLIENT_SECRET",
	}
	credentials := make([]string, len(credentialKeys))
	configuredCount := 0
	for index, key := range credentialKeys {
		value, _ := lookup(key)
		credentials[index] = value
		if value != "" {
			configuredCount++
		}
	}
	if configuredCount == 0 {
		return webAppOrigin, google, nil
	}
	webConfiguredCount := countConfiguredValues(credentials[:3])
	desktopConfiguredCount := countConfiguredValues(credentials[3:])
	if webConfiguredCount != 0 && webConfiguredCount != 3 {
		return "", GoogleOAuthConfig{}, errors.New(
			"all GOOGLE_WEB_* client credentials and redirect URI must be configured together",
		)
	}
	if desktopConfiguredCount != 0 && desktopConfiguredCount != 2 {
		return "", GoogleOAuthConfig{}, errors.New(
			"GOOGLE_DESKTOP_CLIENT_ID and GOOGLE_DESKTOP_CLIENT_SECRET must be configured together",
		)
	}
	webEnabled := webConfiguredCount == 3
	desktopEnabled := desktopConfiguredCount == 2
	if webEnabled {
		if webAppOrigin == "" {
			return "", GoogleOAuthConfig{}, errors.New(
				"AUTH_WEB_APP_ORIGIN is required when Google Web OAuth is enabled",
			)
		}
		if !containsString(webOrigins, webAppOrigin) {
			return "", GoogleOAuthConfig{}, errors.New(
				"AUTH_WEB_APP_ORIGIN must appear in AUTH_WEB_ORIGINS",
			)
		}
		if !validGoogleCredential(credentials[0]) || !validGoogleCredential(credentials[1]) {
			return "", GoogleOAuthConfig{}, errors.New("Google OAuth Web client configuration is invalid")
		}
		allowInsecureLoopback, err := validateGoogleWebCallback(credentials[2], webAppOrigin, cookieSecure)
		if err != nil {
			return "", GoogleOAuthConfig{}, err
		}
		google.WebClientID = credentials[0]
		google.WebClientSecret = credentials[1]
		google.WebRedirectURI = credentials[2]
		google.AllowInsecureLoopbackWebCallback = allowInsecureLoopback
	}
	if desktopEnabled && (!validGoogleCredential(credentials[3]) || !validGoogleCredential(credentials[4])) {
		return "", GoogleOAuthConfig{}, errors.New("Google OAuth Desktop client configuration is invalid")
	}
	if webEnabled && desktopEnabled && credentials[0] == credentials[3] {
		return "", GoogleOAuthConfig{}, errors.New("Google OAuth Web and Desktop client IDs must differ")
	}
	if desktopEnabled {
		google.DesktopClientID = credentials[3]
		google.DesktopClientSecret = credentials[4]
	}
	google.Enabled = true
	return webAppOrigin, google, nil
}

func countConfiguredValues(values []string) int {
	configured := 0
	for _, value := range values {
		if value != "" {
			configured++
		}
	}
	return configured
}

func configuredWebAppOrigin(lookup LookupEnv) (string, error) {
	raw, _ := lookup("AUTH_WEB_APP_ORIGIN")
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	canonical, err := canonicalBrowserOrigin(raw)
	if err != nil || canonical != raw {
		return "", errors.New("AUTH_WEB_APP_ORIGIN must be an exact canonical HTTP or HTTPS origin")
	}
	return canonical, nil
}

func validateGoogleWebCallback(raw, webAppOrigin string, cookieSecure bool) (bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path != googleWebCallbackPath {
		return false, errors.New("GOOGLE_WEB_REDIRECT_URI must be the exact absolute Google Web callback URI")
	}
	callbackOrigin, err := canonicalOriginFromURL(parsed)
	if err != nil || callbackOrigin+googleWebCallbackPath != raw {
		return false, errors.New("GOOGLE_WEB_REDIRECT_URI must be the exact absolute Google Web callback URI")
	}
	if parsed.Scheme == "https" {
		return false, nil
	}
	app, _ := url.Parse(webAppOrigin)
	if parsed.Scheme == "http" && app != nil && app.Scheme == "http" &&
		isLoopbackHostname(parsed.Hostname()) && isLoopbackHostname(app.Hostname()) && !cookieSecure {
		return true, nil
	}
	return false, errors.New("GOOGLE_WEB_REDIRECT_URI must use HTTPS outside loopback HTTP development")
}

func canonicalBrowserOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid browser origin")
	}
	canonical, err := canonicalOriginFromURL(parsed)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "http" && !isLoopbackHostname(parsed.Hostname()) {
		return "", errors.New("insecure non-loopback browser origin")
	}
	return canonical, nil
}

func canonicalOriginFromURL(parsed *url.URL) (string, error) {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", errors.New("invalid HTTP origin")
	}
	hostname, err := canonicalHTTPHostname(parsed.Hostname())
	if err != nil {
		return "", err
	}
	port := parsed.Port()
	if port != "" {
		numericPort, err := strconv.Atoi(port)
		if err != nil || numericPort < 1 || numericPort > 65535 {
			return "", errors.New("invalid HTTP origin port")
		}
		port = strconv.Itoa(numericPort)
	}
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return parsed.Scheme + "://" + host, nil
}

func canonicalHTTPHostname(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("invalid HTTP origin host")
	}
	for index := range len(raw) {
		if raw[index] >= utf8.RuneSelf {
			return "", errors.New("HTTP origin host must use browser-canonical ASCII")
		}
	}
	if ip := net.ParseIP(raw); ip != nil {
		return ip.String(), nil
	}
	if strings.Contains(raw, ":") || strings.HasSuffix(raw, ".") || looksLikeLegacyBrowserIPv4(raw) {
		return "", errors.New("invalid HTTP origin IP literal")
	}
	return strings.ToLower(raw), nil
}

func looksLikeLegacyBrowserIPv4(hostname string) bool {
	lastLabel := hostname
	if separator := strings.LastIndexByte(hostname, '.'); separator >= 0 {
		lastLabel = hostname[separator+1:]
	}
	if lastLabel == "" {
		return false
	}
	lower := strings.ToLower(lastLabel)
	if strings.HasPrefix(lower, "0x") {
		return len(lower) > 2 && strings.IndexFunc(lower[2:], func(character rune) bool {
			return !strings.ContainsRune("0123456789abcdef", character)
		}) < 0
	}
	return strings.IndexFunc(lastLabel, func(character rune) bool { return character < '0' || character > '9' }) < 0
}

func validGoogleCredential(value string) bool {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func browserOrigins(lookup LookupEnv) ([]string, error) {
	configured := commaSeparated(lookup, "AUTH_WEB_ORIGINS", []string{"http://localhost:5173"})
	origins := make([]string, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for _, raw := range configured {
		if raw == "boardly-app://renderer" {
			if _, duplicate := seen[raw]; !duplicate {
				seen[raw] = struct{}{}
				origins = append(origins, raw)
			}
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "HTTP" && parsed.Scheme != "HTTPS") ||
			(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("AUTH_WEB_ORIGINS contains invalid origin %q", raw)
		}

		scheme := strings.ToLower(parsed.Scheme)
		hostname, err := canonicalHTTPHostname(parsed.Hostname())
		if err != nil {
			return nil, fmt.Errorf("AUTH_WEB_ORIGINS contains invalid origin %q", raw)
		}
		if scheme == "http" && !isLoopbackHostname(hostname) {
			return nil, fmt.Errorf("AUTH_WEB_ORIGINS contains insecure non-loopback origin %q", raw)
		}
		port := parsed.Port()
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			port = ""
		}
		host := hostname
		if port != "" {
			host = net.JoinHostPort(hostname, port)
		} else if strings.Contains(hostname, ":") {
			host = "[" + hostname + "]"
		}
		origin := scheme + "://" + host
		if _, duplicate := seen[origin]; duplicate {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins, nil
}

func isLoopbackHostname(hostname string) bool {
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func boolean(lookup LookupEnv, key string, fallback bool) (bool, error) {
	raw := valueOrDefault(lookup, key, "")
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return value, nil
}

func positiveInt32(lookup LookupEnv, key string, fallback int32) (int32, error) {
	raw := valueOrDefault(lookup, key, "")
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive 32-bit integer", key)
	}
	return int32(value), nil
}

// positiveSeconds reads an env value as a positive integer count of seconds and
// returns it as a time.Duration. Empty/whitespace uses the fallback.
func positiveSeconds(lookup LookupEnv, key string, fallback time.Duration) (time.Duration, error) {
	raw := valueOrDefault(lookup, key, "")
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive integer number of seconds", key)
	}
	return time.Duration(value) * time.Second, nil
}

func boundedSeconds(lookup LookupEnv, key string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value, err := positiveSeconds(lookup, key, fallback)
	if err != nil {
		return 0, err
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf(
			"%s must be between %d and %d seconds",
			key,
			int64(minimum/time.Second),
			int64(maximum/time.Second),
		)
	}
	return value, nil
}

func commaSeparated(lookup LookupEnv, key string, fallback []string) []string {
	raw := valueOrDefault(lookup, key, "")
	if raw == "" {
		return fallback
	}
	values := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return fallback
	}
	return values
}

func positiveInt(lookup LookupEnv, key string, fallback int) (int, error) {
	raw := valueOrDefault(lookup, key, "")
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

func valueOrDefault(lookup LookupEnv, key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

// trustedProxies parses BOARDLY_TRUSTED_PROXIES as comma-separated CIDR
// prefixes. Invalid entries are dropped with a warning rather than failing
// startup; an empty variable returns nil ("trust no proxy").
func trustedProxies(lookup LookupEnv) []netip.Prefix {
	raw, _ := lookup("BOARDLY_TRUSTED_PROXIES")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var prefixes []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			slog.Warn("config: ignoring invalid trusted proxy CIDR", "value", value, "error", err)
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}
