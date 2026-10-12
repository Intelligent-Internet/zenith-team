package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoadAppliesSafeSingleNodeDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != ":8788" {
		t.Errorf("HTTPAddr = %q, want :8788", cfg.HTTPAddr)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 15s", cfg.ShutdownTimeout)
	}
	if cfg.Auth.AccessTTL != 15*time.Minute || cfg.Auth.RefreshTTL != 7*24*time.Hour || cfg.Auth.CookieSecure {
		t.Errorf("Auth defaults = %#v", cfg.Auth)
	}
	if cfg.Auth.DesktopHandoffCodeTTL != 60*time.Second {
		t.Errorf("Auth.DesktopHandoffCodeTTL = %s, want 60s", cfg.Auth.DesktopHandoffCodeTTL)
	}
	if cfg.Auth.EmailVerification.Enabled {
		t.Errorf(
			"email verification must stay disabled until provider secrets are configured: %#v",
			cfg.Auth.EmailVerification,
		)
	}
	if cfg.RedisURL != "" {
		t.Errorf("RedisURL = %q, want empty", cfg.RedisURL)
	}
	if cfg.APIReplicas != 1 {
		t.Errorf("APIReplicas = %d, want 1", cfg.APIReplicas)
	}
	if cfg.Mailbox.Enabled {
		t.Errorf("Mailbox must stay disabled until its Resend credentials are configured: %#v", cfg.Mailbox)
	}
	if !reflect.DeepEqual(cfg.WebOrigins, []string{"http://localhost:5173"}) {
		t.Errorf("WebOrigins = %#v, want localhost Vite origin", cfg.WebOrigins)
	}
	if cfg.Storage.Bucket != "boardly-test" || cfg.Storage.Region != "us-east-1" {
		t.Errorf("Storage defaults = %#v", cfg.Storage)
	}
	if cfg.Attachment.AbandonedReadyGrace != 24*time.Hour {
		t.Errorf("Attachment.AbandonedReadyGrace = %s, want 24h", cfg.Attachment.AbandonedReadyGrace)
	}
	if cfg.CloudRun.Enabled {
		t.Errorf("CloudRun must stay disabled without GCP_PROJECT_ID: %#v", cfg.CloudRun)
	}
	if cfg.DaemonIDRequired {
		t.Errorf("DaemonIDRequired = true, want the warn-only rollout stage by default")
	}
}

func TestLoadParsesDaemonIDRequired(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":       "postgres://boardly:boardly@localhost/boardly",
		"APP_DAEMON_ID_REQUIRED": "true",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.DaemonIDRequired {
		t.Fatalf("DaemonIDRequired = false, want true")
	}

	_, err = Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":       "postgres://boardly:boardly@localhost/boardly",
		"APP_DAEMON_ID_REQUIRED": "maybe",
	}))
	if err == nil || err.Error() != "APP_DAEMON_ID_REQUIRED must be a boolean" {
		t.Fatalf("Load() error = %v, want the boolean rejection", err)
	}
}

func TestLoadReadsReferenceCloudRunEnvironment(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":          "postgres://boardly:boardly@localhost/boardly",
		"GCP_PROJECT_ID":            "boardly-production",
		"GCP_REGION":                "asia-southeast1",
		"GCP_SOURCE_BUCKET":         "boardly-app-sources",
		"GCP_ARTIFACT_REGISTRY":     "asia-southeast1-docker.pkg.dev/boardly-production/user-apps",
		"CLOUD_RUN_MAX_INSTANCES":   "7",
		"CLOUD_RUN_MIN_INSTANCES":   "0",
		"CLOUD_RUN_MEMORY":          "1Gi",
		"CLOUD_RUN_CPU":             "2",
		"GCP_SERVICE_ACCOUNT_EMAIL": "boardly-build@my-production-project.iam.gserviceaccount.com",
		"CLOUD_RUN_SERVICE_ACCOUNT": "boardly-runtime@my-production-project.iam.gserviceaccount.com",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.CloudRun.Enabled || cfg.CloudRun.ProjectID != "boardly-production" ||
		cfg.CloudRun.Region != "asia-southeast1" ||
		cfg.CloudRun.SourceBucket != "boardly-app-sources" ||
		cfg.CloudRun.ArtifactRegistry !=
			"asia-southeast1-docker.pkg.dev/boardly-production/user-apps" ||
		cfg.CloudRun.BuildServiceAccount !=
			"boardly-build@my-production-project.iam.gserviceaccount.com" ||
		cfg.CloudRun.RuntimeServiceAccount !=
			"boardly-runtime@my-production-project.iam.gserviceaccount.com" ||
		cfg.CloudRun.MaxInstances != 7 || cfg.CloudRun.MinInstances != 0 ||
		cfg.CloudRun.Memory != "1Gi" || cfg.CloudRun.CPU != "2" {
		t.Fatalf("CloudRun config = %#v", cfg.CloudRun)
	}
}

func TestLoadDefaultsAndValidatesCloudRunEnvironment(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":          "postgres://boardly:boardly@localhost/boardly",
		"GCP_PROJECT_ID":            "my-project",
		"GCP_SERVICE_ACCOUNT_EMAIL": "boardly-build@my-project.iam.gserviceaccount.com",
		"CLOUD_RUN_SERVICE_ACCOUNT": "boardly-runtime@my-project.iam.gserviceaccount.com",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CloudRun.Region != "us-central1" ||
		cfg.CloudRun.SourceBucket != "my-project-app-sources" ||
		cfg.CloudRun.ArtifactRegistry != "us-central1-docker.pkg.dev/my-project/user-apps" ||
		cfg.CloudRun.MaxInstances != 10 || cfg.CloudRun.Memory != "512Mi" || cfg.CloudRun.CPU != "1" {
		t.Fatalf("CloudRun defaults = %#v", cfg.CloudRun)
	}

	validIdentity := map[string]string{
		"GCP_SERVICE_ACCOUNT_EMAIL": "boardly-build@my-project.iam.gserviceaccount.com",
		"CLOUD_RUN_SERVICE_ACCOUNT": "boardly-runtime@my-project.iam.gserviceaccount.com",
	}
	invalid := []map[string]string{
		{"GCP_PROJECT_ID": "INVALID_PROJECT"},
		{"GCP_PROJECT_ID": "my-project", "GCP_REGION": "https://region.invalid"},
		{"GCP_PROJECT_ID": "my-project", "GCP_SOURCE_BUCKET": "Bad Bucket"},
		{"GCP_PROJECT_ID": "my-project", "GCP_ARTIFACT_REGISTRY": "https://registry.invalid"},
		{"GCP_PROJECT_ID": "my-project", "CLOUD_RUN_MIN_INSTANCES": "11"},
		{"GCP_PROJECT_ID": "my-project", "CLOUD_RUN_CPU": "3"},
		{"GCP_PROJECT_ID": "my-project", "CLOUD_RUN_MEMORY": "secret"},
		{
			"GCP_PROJECT_ID":            "my-project",
			"GCP_SERVICE_ACCOUNT_EMAIL": " ",
			"CLOUD_RUN_SERVICE_ACCOUNT": validIdentity["CLOUD_RUN_SERVICE_ACCOUNT"],
		},
		{
			"GCP_PROJECT_ID":            "my-project",
			"GCP_SERVICE_ACCOUNT_EMAIL": validIdentity["GCP_SERVICE_ACCOUNT_EMAIL"],
			"CLOUD_RUN_SERVICE_ACCOUNT": " ",
		},
		{
			"GCP_PROJECT_ID":            "my-project",
			"GCP_SERVICE_ACCOUNT_EMAIL": "default",
		},
		{
			"GCP_PROJECT_ID":            "my-project",
			"CLOUD_RUN_SERVICE_ACCOUNT": "default",
		},
	}
	for index, values := range invalid {
		if values["GCP_PROJECT_ID"] != "INVALID_PROJECT" {
			if _, exists := values["GCP_SERVICE_ACCOUNT_EMAIL"]; !exists {
				values["GCP_SERVICE_ACCOUNT_EMAIL"] = validIdentity["GCP_SERVICE_ACCOUNT_EMAIL"]
			}
			if _, exists := values["CLOUD_RUN_SERVICE_ACCOUNT"]; !exists {
				values["CLOUD_RUN_SERVICE_ACCOUNT"] = validIdentity["CLOUD_RUN_SERVICE_ACCOUNT"]
			}
		}
		values["APP_DATABASE_URL"] = "postgres://boardly:boardly@localhost/boardly"
		if _, err := Load(configuredLookup(values)); err == nil {
			t.Fatalf("Load(invalid Cloud Run case %d) error = nil", index)
		}
	}
}

func TestLoadConfiguresDedicatedMailboxResendCredentials(t *testing.T) {
	t.Parallel()

	const apiKey = "sentinel-mailbox-resend-key"
	const webhookSecret = "whsec_bWFpbGJveCB3ZWJob29rIHRlc3Qgc2VjcmV0"
	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":                 "postgres://boardly:boardly@localhost/boardly",
		"MAILBOX_RESEND_API_KEY":           apiKey,
		"MAILBOX_RESEND_WEBHOOK_SECRET":    webhookSecret,
		"MAILBOX_DOMAIN":                   "mail.example.com",
		"MAILBOX_REQUESTS_PER_SECOND":      "3",
		"MAILBOX_PROVIDER_TIMEOUT_SECONDS": "12",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Mailbox.Enabled || cfg.Mailbox.Domain != "mail.example.com" ||
		cfg.Mailbox.RequestsPerSecond != 3 || cfg.Mailbox.ProviderTimeout != 12*time.Second {
		t.Fatalf("Mailbox config = %#v", cfg.Mailbox)
	}
	if cfg.Mailbox.ResendAPIKey.Reveal() != apiKey ||
		cfg.Mailbox.ResendWebhookSecret.Reveal() != webhookSecret {
		t.Fatal("Mailbox secrets were not retained at the integration seam")
	}
	formatted := fmt.Sprintf("%v %#v", cfg.Mailbox, cfg.Mailbox)
	if strings.Contains(formatted, apiKey) || strings.Contains(formatted, webhookSecret) {
		t.Fatalf("formatted Mailbox config leaked a secret: %s", formatted)
	}
}

func TestLoadAllowsMailboxWithoutWebhookSecret(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":       "postgres://boardly:boardly@localhost/boardly",
		"MAILBOX_RESEND_API_KEY": "mailbox-key",
		"MAILBOX_DOMAIN":         "mail.example.com",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Mailbox.Enabled {
		t.Fatal("Mailbox.Enabled = false")
	}
	if cfg.Mailbox.ResendWebhookSecret.Reveal() != "" {
		t.Fatal("Mailbox webhook secret must stay empty when it is not configured")
	}
}

func TestLoadRejectsPartialOrUnsafeMailboxConfiguration(t *testing.T) {
	t.Parallel()

	const webhookSecret = "whsec_bWFpbGJveCB3ZWJob29rIHRlc3Qgc2VjcmV0"
	tests := map[string]map[string]string{
		"missing domain": {
			"MAILBOX_RESEND_API_KEY": "mailbox-key",
		},
		"invalid webhook secret": {
			"MAILBOX_RESEND_API_KEY": "mailbox-key", "MAILBOX_RESEND_WEBHOOK_SECRET": "not-a-secret",
			"MAILBOX_DOMAIN": "mail.example.com",
		},
		"noncanonical domain": {
			"MAILBOX_RESEND_API_KEY": "mailbox-key", "MAILBOX_RESEND_WEBHOOK_SECRET": webhookSecret,
			"MAILBOX_DOMAIN": "Mail.Boardly.ii.inc",
		},
		"unsafe provider rate": {
			"MAILBOX_RESEND_API_KEY": "mailbox-key", "MAILBOX_RESEND_WEBHOOK_SECRET": webhookSecret,
			"MAILBOX_DOMAIN": "mail.example.com", "MAILBOX_REQUESTS_PER_SECOND": "6",
		},
	}
	for name, values := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			values["APP_DATABASE_URL"] = "postgres://boardly:boardly@localhost/boardly"
			if _, err := Load(configuredLookup(values)); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestProductionComposeLeavesOptionalEmailTuningsUnset(t *testing.T) {
	t.Parallel()

	compose, err := os.ReadFile("../../../deploy/docker-compose.production.yml")
	if err != nil {
		t.Fatalf("read production Compose configuration: %v", err)
	}
	configuration := string(compose)
	for _, key := range []string{
		"AUTH_EMAIL_VERIFICATION_TTL_SECONDS",
		"AUTH_EMAIL_VERIFICATION_RESEND_COOLDOWN_SECONDS",
		"AUTH_EMAIL_PROVIDER_TIMEOUT_SECONDS",
		"AUTH_PASSWORD_RESET_TTL_SECONDS",
		"AUTH_PASSWORD_RESET_RESEND_COOLDOWN_SECONDS",
	} {
		want := fmt.Sprintf("%s: ${%s:-}", key, key)
		if !strings.Contains(configuration, want) {
			t.Errorf(
				"production Compose mapping for %s must leave the value unset so backend defaults do not enable email authentication",
				key,
			)
		}
	}
}

func TestLoadConfiguresResendEmailVerificationWithoutExposingSecrets(t *testing.T) {
	t.Parallel()

	const apiKey = "sentinel-resend-api-key"
	const verificationSecret = "01234567890123456789012345678901"
	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":                    "postgres://boardly:boardly@localhost/boardly",
		"AUTH_EMAIL_PROVIDER":                 "resend",
		"AUTH_RESEND_API_KEY":                 apiKey,
		"AUTH_EMAIL_VERIFICATION_SECRET":      verificationSecret,
		"AUTH_EMAIL_VERIFICATION_TEMPLATE_ID": "published-template-id",
		"AUTH_WEB_APP_ORIGIN":                 "http://localhost:5173",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	email := cfg.Auth.EmailVerification
	if !email.Enabled || email.Provider != "resend" || email.TemplateID != "published-template-id" ||
		email.TTL != 10*time.Minute || email.ResendCooldown != time.Minute ||
		email.ProviderTimeout != 10*time.Second {
		t.Fatalf("email verification config = %#v", email)
	}
	if email.PasswordResetTemplateID != defaultPasswordResetTemplateID ||
		email.PasswordResetTTL != 30*time.Minute || email.PasswordResetResendCooldown != time.Minute {
		t.Fatalf("password reset email config = %#v", email)
	}
	if email.ResendAPIKey.Reveal() != apiKey || email.Secret.Reveal() != verificationSecret {
		t.Fatal("email verification secrets were not retained at the narrow integration boundary")
	}
	formatted := fmt.Sprintf("%v %#v", email, email)
	if strings.Contains(formatted, apiKey) || strings.Contains(formatted, verificationSecret) {
		t.Fatalf("formatted email config leaked a secret: %s", formatted)
	}
}

func TestLoadRejectsPartialOrUnsupportedEmailVerificationConfiguration(t *testing.T) {
	t.Parallel()

	for name, values := range map[string]map[string]string{
		"missing verification secret": {
			"AUTH_EMAIL_PROVIDER": "resend", "AUTH_RESEND_API_KEY": "resend-key",
		},
		"missing provider key": {
			"AUTH_EMAIL_PROVIDER": "resend", "AUTH_EMAIL_VERIFICATION_SECRET": "01234567890123456789012345678901",
		},
		"short verification secret": {
			"AUTH_EMAIL_PROVIDER": "resend", "AUTH_RESEND_API_KEY": "resend-key", "AUTH_EMAIL_VERIFICATION_SECRET": "too-short",
		},
		"unsupported provider": {
			"AUTH_EMAIL_PROVIDER": "other", "AUTH_RESEND_API_KEY": "resend-key", "AUTH_EMAIL_VERIFICATION_SECRET": "01234567890123456789012345678901",
		},
	} {
		values["APP_DATABASE_URL"] = "postgres://boardly:boardly@localhost/boardly"
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Load(configuredLookup(values)); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestLoadBoundsAttachmentAbandonedReadyGrace(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":                         "postgres://boardly:boardly@localhost/boardly",
		"ATTACHMENT_ABANDONED_READY_GRACE_SECONDS": "172800",
	}))
	if err != nil {
		t.Fatalf("Load(48h grace) error = %v", err)
	}
	if cfg.Attachment.AbandonedReadyGrace != 48*time.Hour {
		t.Fatalf("Attachment grace = %s, want 48h", cfg.Attachment.AbandonedReadyGrace)
	}
	for _, grace := range []string{"3540", "2595600"} {
		_, err := Load(configuredLookup(map[string]string{
			"APP_DATABASE_URL":                         "postgres://boardly:boardly@localhost/boardly",
			"ATTACHMENT_ABANDONED_READY_GRACE_SECONDS": grace,
		}))
		if err == nil || !strings.Contains(err.Error(), "ATTACHMENT_ABANDONED_READY_GRACE_SECONDS") {
			t.Fatalf("Load(grace=%s) error = %v", grace, err)
		}
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Parallel()

	_, err := Load(mapLookup(nil))
	if err == nil {
		t.Fatal("Load() error = nil, want missing database URL error")
	}
}

func TestLoadRequiresStrongCSRFSecret(t *testing.T) {
	t.Parallel()

	for name, values := range map[string]map[string]string{
		"missing": {
			"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
		},
		"too short": {
			"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
			"AUTH_CSRF_SECRET": "short",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Load(mapLookup(values)); err == nil {
				t.Fatal("Load() error = nil, want CSRF secret validation error")
			}
		})
	}
}

func TestLoadRejectsUnsafeLogLevelAndBrowserOrigins(t *testing.T) {
	t.Parallel()

	for name, overrides := range map[string]map[string]string{
		"unknown log level":            {"APP_LOG_LEVEL": "verbose"},
		"wildcard origin":              {"AUTH_WEB_ORIGINS": "*"},
		"null origin":                  {"AUTH_WEB_ORIGINS": "null"},
		"relative origin":              {"AUTH_WEB_ORIGINS": "app.boardly.test"},
		"origin with path":             {"AUTH_WEB_ORIGINS": "https://app.boardly.test/api"},
		"origin with query":            {"AUTH_WEB_ORIGINS": "https://app.boardly.test?token=secret"},
		"unknown desktop app host":     {"AUTH_WEB_ORIGINS": "boardly-app://attacker"},
		"desktop app origin with path": {"AUTH_WEB_ORIGINS": "boardly-app://renderer/"},
		"desktop app origin with port": {"AUTH_WEB_ORIGINS": "boardly-app://renderer:443"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			values := map[string]string{
				"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
			}
			for key, value := range overrides {
				values[key] = value
			}
			if _, err := Load(configuredLookup(values)); err == nil {
				t.Fatal("Load() error = nil, want configuration validation error")
			}
		})
	}
}

func TestLoadAcceptsAndDeduplicatesPackagedDesktopRendererOrigin(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
		"AUTH_WEB_ORIGINS": "https://app.boardly.test, boardly-app://renderer, boardly-app://renderer",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"https://app.boardly.test", "boardly-app://renderer"}
	if !reflect.DeepEqual(cfg.WebOrigins, want) {
		t.Errorf("WebOrigins = %#v, want %#v", cfg.WebOrigins, want)
	}
}

func TestLoadCanonicalizesAndDeduplicatesBrowserOrigins(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
		"AUTH_WEB_ORIGINS": "HTTPS://APP.BOARDLY.TEST/, https://app.boardly.test, http://localhost:5173/",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"https://app.boardly.test", "http://localhost:5173"}
	if !reflect.DeepEqual(cfg.WebOrigins, want) {
		t.Errorf("WebOrigins = %#v, want %#v", cfg.WebOrigins, want)
	}
}

func TestLoadDerivesSecureCookiesFromHTTPSOrigins(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
		"AUTH_WEB_ORIGINS": "https://app.boardly.test",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Auth.CookieSecure {
		t.Fatal("Auth.CookieSecure = false for HTTPS browser origin")
	}
}

func TestLoadRejectsInsecureProductionCookieConfigurations(t *testing.T) {
	t.Parallel()

	for name, overrides := range map[string]map[string]string{
		"remote HTTP origin": {
			"AUTH_WEB_ORIGINS": "http://app.boardly.test",
		},
		"secure cookie disabled for HTTPS": {
			"AUTH_WEB_ORIGINS":   "https://app.boardly.test",
			"AUTH_COOKIE_SECURE": "false",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			overrides["APP_DATABASE_URL"] = "postgres://boardly:boardly@localhost/boardly"
			if _, err := Load(configuredLookup(overrides)); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestLoadRequiresRedisForMultipleAPIReplicas(t *testing.T) {
	t.Parallel()

	_, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
		"APP_API_REPLICAS": "2",
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want Redis requirement error")
	}

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
		"APP_API_REPLICAS": "2",
		"APP_REDIS_URL":    "redis://localhost:6379/0",
	}))
	if err != nil {
		t.Fatalf("Load() with Redis error = %v", err)
	}
	if cfg.RedisURL != "redis://localhost:6379/0" {
		t.Errorf("RedisURL = %q, want configured URL", cfg.RedisURL)
	}
	if cfg.APIReplicas != 2 {
		t.Errorf("APIReplicas = %d, want 2", cfg.APIReplicas)
	}
}

func TestLoadUsesExplicitServerAndStorageSettings(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":             "postgres://boardly:boardly@localhost/boardly",
		"APP_HTTP_ADDR":                "127.0.0.1:9999",
		"APP_LOG_LEVEL":                "debug",
		"APP_SHUTDOWN_TIMEOUT_SECONDS": "30",
		"AUTH_WEB_ORIGINS":             "https://app.boardly.test, https://desktop.boardly.test",
		"STORAGE_S3_BUCKET":            "production-attachments",
		"STORAGE_S3_REGION":            "eu-west-1",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:9999" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %s", cfg.ShutdownTimeout)
	}
	if !reflect.DeepEqual(cfg.WebOrigins, []string{
		"https://app.boardly.test",
		"https://desktop.boardly.test",
	}) {
		t.Errorf("WebOrigins = %#v", cfg.WebOrigins)
	}
	if cfg.Storage.Bucket != "production-attachments" || cfg.Storage.Region != "eu-west-1" {
		t.Errorf("Storage = %#v", cfg.Storage)
	}
}

func TestLoadRequiresAndMapsS3Configuration(t *testing.T) {
	t.Parallel()

	_, err := Load(mapLookup(map[string]string{
		"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
		"AUTH_CSRF_SECRET": "01234567890123456789012345678901",
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want incomplete S3 configuration error")
	}

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":             "postgres://boardly:boardly@localhost/boardly",
		"STORAGE_S3_BUCKET":            "boardly-attachments",
		"STORAGE_S3_REGION":            "us-east-1",
		"STORAGE_S3_ENDPOINT":          "http://localhost:9000",
		"STORAGE_S3_ACCESS_KEY_ID":     "test-access",
		"STORAGE_S3_SECRET_ACCESS_KEY": "test-secret",
		"STORAGE_S3_USE_PATH_STYLE":    "true",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Storage.Bucket != "boardly-attachments" || cfg.Storage.Region != "us-east-1" {
		t.Errorf("S3 identity = %#v", cfg.Storage)
	}
	if cfg.Storage.Endpoint != "http://localhost:9000" || !cfg.Storage.UsePathStyle {
		t.Errorf("S3 endpoint settings = %#v", cfg.Storage)
	}
	if cfg.Storage.AccessKeyID != "test-access" || cfg.Storage.SecretAccessKey != "test-secret" {
		t.Error("S3 static credentials were not mapped")
	}
}

func TestLoadConfiguresDatabasePool(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":                         "postgres://boardly:boardly@localhost/boardly",
		"APP_DATABASE_MAX_CONNS":                   "40",
		"APP_DATABASE_MIN_CONNS":                   "8",
		"APP_DATABASE_MAX_CONN_LIFETIME_SECONDS":   "7200",
		"APP_DATABASE_MAX_CONN_IDLE_TIME_SECONDS":  "1200",
		"APP_DATABASE_HEALTH_CHECK_PERIOD_SECONDS": "45",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Database.MaxConns != 40 {
		t.Errorf("Database.MaxConns = %d, want 40", cfg.Database.MaxConns)
	}
	if cfg.Database.MinConns != 8 {
		t.Errorf("Database.MinConns = %d, want 8", cfg.Database.MinConns)
	}
	if cfg.Database.MaxConnLifetime != 2*time.Hour {
		t.Errorf("Database.MaxConnLifetime = %s, want 2h", cfg.Database.MaxConnLifetime)
	}
	if cfg.Database.MaxConnIdleTime != 20*time.Minute {
		t.Errorf("Database.MaxConnIdleTime = %s, want 20m", cfg.Database.MaxConnIdleTime)
	}
	if cfg.Database.HealthCheckPeriod != 45*time.Second {
		t.Errorf("Database.HealthCheckPeriod = %s, want 45s", cfg.Database.HealthCheckPeriod)
	}
}

func TestLoadRejectsDatabasePoolMinimumAboveMaximum(t *testing.T) {
	t.Parallel()

	_, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":       "postgres://boardly:boardly@localhost/boardly",
		"APP_DATABASE_MAX_CONNS": "4",
		"APP_DATABASE_MIN_CONNS": "5",
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want pool size validation error")
	}
}

func TestLoadGoogleOAuthDisabledDefaultsAndIndependentWebAppOrigin(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":             "postgres://boardly:boardly@localhost/boardly",
		"AUTH_WEB_APP_ORIGIN":          "http://localhost:5173",
		"GOOGLE_WEB_CLIENT_ID":         "",
		"GOOGLE_WEB_CLIENT_SECRET":     "",
		"GOOGLE_WEB_REDIRECT_URI":      "",
		"GOOGLE_DESKTOP_CLIENT_ID":     "",
		"GOOGLE_DESKTOP_CLIENT_SECRET": "",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Auth.WebAppOrigin != "http://localhost:5173" {
		t.Errorf("Auth.WebAppOrigin = %q", cfg.Auth.WebAppOrigin)
	}
	if cfg.Auth.Google.Enabled {
		t.Fatal("Auth.Google.Enabled = true without credentials")
	}
	if cfg.Auth.Google.FlowTTL != 10*time.Minute || cfg.Auth.Google.ProviderTimeout != 10*time.Second {
		t.Errorf("Auth.Google disabled defaults = %#v", cfg.Auth.Google)
	}
}

func TestLoadGoogleOAuthAllowsDesktopOnlyConfiguration(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":             "postgres://boardly:boardly@localhost/boardly",
		"GOOGLE_DESKTOP_CLIENT_ID":     "desktop-client.apps.googleusercontent.test",
		"GOOGLE_DESKTOP_CLIENT_SECRET": "sentinel-desktop-client-secret",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Auth.Google.Enabled ||
		cfg.Auth.Google.DesktopClientID != "desktop-client.apps.googleusercontent.test" ||
		cfg.Auth.Google.DesktopClientSecret != "sentinel-desktop-client-secret" {
		t.Fatalf("desktop-only Google OAuth config = %#v", cfg.Auth.Google)
	}
	if cfg.Auth.Google.WebClientID != "" || cfg.Auth.Google.WebClientSecret != "" ||
		cfg.Auth.Google.WebRedirectURI != "" {
		t.Fatalf("desktop-only config unexpectedly enabled Web OAuth: %#v", cfg.Auth.Google)
	}
}

func TestLoadGoogleOAuthAllowsWebOnlyConfiguration(t *testing.T) {
	t.Parallel()

	cfg, err := Load(configuredLookup(map[string]string{
		"APP_DATABASE_URL":         "postgres://boardly:boardly@localhost/boardly",
		"AUTH_WEB_ORIGINS":         "https://app.boardly.test",
		"AUTH_WEB_APP_ORIGIN":      "https://app.boardly.test",
		"GOOGLE_WEB_CLIENT_ID":     "web-client.apps.googleusercontent.test",
		"GOOGLE_WEB_CLIENT_SECRET": "sentinel-web-client-secret",
		"GOOGLE_WEB_REDIRECT_URI":  "https://api.boardly.test/api/v1/auth/google/web/callback",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Auth.Google.Enabled || cfg.Auth.Google.WebClientID != "web-client.apps.googleusercontent.test" ||
		cfg.Auth.Google.WebClientSecret != "sentinel-web-client-secret" {
		t.Fatalf("Web-only Google OAuth config = %#v", cfg.Auth.Google)
	}
	if cfg.Auth.Google.DesktopClientID != "" || cfg.Auth.Google.DesktopClientSecret != "" {
		t.Fatalf("Web-only config unexpectedly enabled Desktop OAuth: %#v", cfg.Auth.Google)
	}
}

func TestLoadGoogleOAuthRejectsPresentWhitespaceOnlyConfiguration(t *testing.T) {
	t.Parallel()

	for _, values := range []map[string]string{
		{
			"APP_DATABASE_URL":         "postgres://boardly:boardly@localhost/boardly",
			"GOOGLE_WEB_CLIENT_SECRET": "   ",
		},
		{
			"APP_DATABASE_URL":             "postgres://boardly:boardly@localhost/boardly",
			"GOOGLE_WEB_CLIENT_ID":         " ",
			"GOOGLE_WEB_CLIENT_SECRET":     "\t",
			"GOOGLE_WEB_REDIRECT_URI":      "\n",
			"GOOGLE_DESKTOP_CLIENT_ID":     "  ",
			"GOOGLE_DESKTOP_CLIENT_SECRET": "\r",
		},
	} {
		if _, err := Load(configuredLookup(values)); err == nil {
			t.Fatal("Load() error = nil for present whitespace-only Google configuration")
		}
	}
}

func TestLoadGoogleOAuthRejectsEveryIncompleteClientCredentialGroupWithoutLeakingSecrets(t *testing.T) {
	t.Parallel()

	credentials := []struct {
		key   string
		value string
	}{
		{key: "GOOGLE_WEB_CLIENT_ID", value: "web-client.apps.googleusercontent.test"},
		{key: "GOOGLE_WEB_CLIENT_SECRET", value: "sentinel-web-client-secret"},
		{key: "GOOGLE_WEB_REDIRECT_URI", value: "https://api.boardly.test/api/v1/auth/google/web/callback"},
		{key: "GOOGLE_DESKTOP_CLIENT_ID", value: "desktop-client.apps.googleusercontent.test"},
		{key: "GOOGLE_DESKTOP_CLIENT_SECRET", value: "sentinel-desktop-client-secret"},
	}
	for mask := 1; mask < (1<<len(credentials))-1; mask++ {
		if mask == 0b00111 || mask == 0b11000 {
			continue
		}

		t.Run(strconv.Itoa(mask), func(t *testing.T) {
			t.Parallel()
			values := map[string]string{
				"APP_DATABASE_URL":    "postgres://boardly:boardly@localhost/boardly",
				"AUTH_WEB_ORIGINS":    "https://app.boardly.test",
				"AUTH_WEB_APP_ORIGIN": "https://app.boardly.test",
			}
			for index, credential := range credentials {
				if mask&(1<<index) != 0 {
					values[credential.key] = credential.value
				}
			}
			_, err := Load(configuredLookup(values))
			if err == nil {
				t.Fatal("Load() error = nil")
			}
			if strings.Contains(err.Error(), "sentinel-web-client-secret") ||
				strings.Contains(err.Error(), "sentinel-desktop-client-secret") {
				t.Fatalf("Load() error leaked a provider secret: %v", err)
			}
		})
	}
}

func TestLoadGoogleOAuthRejectsMalformedCredentialsWithoutLeakingThem(t *testing.T) {
	t.Parallel()

	for _, malformed := range []string{
		" sentinel-malformed-secret",
		"sentinel-malformed-secret ",
		"sentinel\nmalformed-secret",
		string([]byte{0xff, 's', 'e', 'c', 'r', 'e', 't'}),
		strings.Repeat("s", 2049),
	} {
		t.Run(strconv.Itoa(len(malformed)), func(t *testing.T) {
			values := completeGoogleOAuthConfig()
			values["GOOGLE_WEB_CLIENT_SECRET"] = malformed
			_, err := Load(configuredLookup(values))
			if err == nil {
				t.Fatal("Load() error = nil")
			}
			if strings.Contains(err.Error(), malformed) ||
				strings.Contains(err.Error(), "sentinel-malformed-secret") {
				t.Fatalf("Load() error leaked malformed credential: %v", err)
			}
		})
	}
}

func TestLoadGoogleOAuthRequiresCanonicalMemberWebAppOriginAndSeparateClientIDs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		overrides map[string]string
	}{
		{name: "missing Web app origin", overrides: map[string]string{"AUTH_WEB_APP_ORIGIN": ""}},
		{name: "noncanonical trailing slash", overrides: map[string]string{"AUTH_WEB_APP_ORIGIN": "https://app.boardly.test/"}},
		{name: "noncanonical scheme case", overrides: map[string]string{"AUTH_WEB_APP_ORIGIN": "HTTPS://app.boardly.test"}},
		{name: "noncanonical host case", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://APP.BOARDLY.TEST", "AUTH_WEB_APP_ORIGIN": "https://APP.BOARDLY.TEST"}},
		{name: "explicit default port", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://app.boardly.test:443", "AUTH_WEB_APP_ORIGIN": "https://app.boardly.test:443"}},
		{name: "noncanonical leading-zero port", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://app.boardly.test:0443", "AUTH_WEB_APP_ORIGIN": "https://app.boardly.test:0443"}},
		{name: "out-of-range port", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://app.boardly.test:65536", "AUTH_WEB_APP_ORIGIN": "https://app.boardly.test:65536"}},
		{name: "origin path", overrides: map[string]string{"AUTH_WEB_APP_ORIGIN": "https://app.boardly.test/login"}},
		{name: "origin query", overrides: map[string]string{"AUTH_WEB_APP_ORIGIN": "https://app.boardly.test?next=/login"}},
		{name: "origin fragment", overrides: map[string]string{"AUTH_WEB_APP_ORIGIN": "https://app.boardly.test#login"}},
		{name: "origin userinfo", overrides: map[string]string{"AUTH_WEB_APP_ORIGIN": "https://user@app.boardly.test"}},
		{name: "Unicode host is not browser canonical", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://bücher.example", "AUTH_WEB_APP_ORIGIN": "https://bücher.example"}},
		{name: "expanded IPv6 is not browser canonical", overrides: map[string]string{"AUTH_WEB_ORIGINS": "http://[0:0:0:0:0:0:0:1]:5173", "AUTH_WEB_APP_ORIGIN": "http://[0:0:0:0:0:0:0:1]:5173"}},
		{name: "short legacy IPv4 is not browser canonical", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://127.1", "AUTH_WEB_APP_ORIGIN": "https://127.1"}},
		{name: "octal legacy IPv4 is not browser canonical", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://0177.0.0.1", "AUTH_WEB_APP_ORIGIN": "https://0177.0.0.1"}},
		{name: "integer legacy IPv4 is not browser canonical", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://2130706433", "AUTH_WEB_APP_ORIGIN": "https://2130706433"}},
		{name: "hex legacy IPv4 is not browser canonical", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://0x7f000001", "AUTH_WEB_APP_ORIGIN": "https://0x7f000001"}},
		{name: "trailing-dot host is not browser canonical", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://app.boardly.test.", "AUTH_WEB_APP_ORIGIN": "https://app.boardly.test."}},
		{name: "DNS host ending in decimal number is not browser canonical", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://example.127", "AUTH_WEB_APP_ORIGIN": "https://example.127"}},
		{name: "DNS host ending in octal number is not browser canonical", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://example.0177", "AUTH_WEB_APP_ORIGIN": "https://example.0177"}},
		{name: "DNS host ending in hex number is not browser canonical", overrides: map[string]string{"AUTH_WEB_ORIGINS": "https://example.0x7f", "AUTH_WEB_APP_ORIGIN": "https://example.0x7f"}},
		{name: "not in CORS allowlist", overrides: map[string]string{"AUTH_WEB_APP_ORIGIN": "https://other.boardly.test"}},
		{name: "same Web and Desktop ID", overrides: map[string]string{"GOOGLE_DESKTOP_CLIENT_ID": "web-client.apps.googleusercontent.test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := completeGoogleOAuthConfig()
			for key, value := range test.overrides {
				values[key] = value
			}
			if _, err := Load(configuredLookup(values)); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}

	cfg, err := Load(configuredLookup(completeGoogleOAuthConfig()))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Auth.Google.Enabled || cfg.Auth.WebAppOrigin != "https://app.boardly.test" ||
		cfg.Auth.Google.WebClientID != "web-client.apps.googleusercontent.test" ||
		cfg.Auth.Google.DesktopClientID != "desktop-client.apps.googleusercontent.test" {
		t.Fatalf("Google OAuth config = %#v / origin %q", cfg.Auth.Google, cfg.Auth.WebAppOrigin)
	}
}

func TestLoadGoogleOAuthValidatesCallbackSchemeShapeAndLocalDevelopmentPolicy(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		overrides map[string]string
		valid     bool
		allowHTTP bool
	}{
		{name: "HTTPS callback", valid: true},
		{name: "loopback HTTP development", overrides: map[string]string{
			"AUTH_WEB_ORIGINS":        "http://localhost:5173",
			"AUTH_WEB_APP_ORIGIN":     "http://localhost:5173",
			"GOOGLE_WEB_REDIRECT_URI": "http://127.0.0.1:8788/api/v1/auth/google/web/callback",
		}, valid: true, allowHTTP: true},
		{name: "canonical IPv6 loopback HTTP development", overrides: map[string]string{
			"AUTH_WEB_ORIGINS":        "http://[::1]:5173",
			"AUTH_WEB_APP_ORIGIN":     "http://[::1]:5173",
			"GOOGLE_WEB_REDIRECT_URI": "http://[::1]:8788/api/v1/auth/google/web/callback",
		}, valid: true, allowHTTP: true},
		{name: "secure cookie rejects loopback HTTP callback", overrides: map[string]string{
			"AUTH_WEB_ORIGINS":        "http://localhost:5173",
			"AUTH_WEB_APP_ORIGIN":     "http://localhost:5173",
			"GOOGLE_WEB_REDIRECT_URI": "http://127.0.0.1:8788/api/v1/auth/google/web/callback",
			"AUTH_COOKIE_SECURE":      "true",
		}},
		{name: "HTTPS app rejects HTTP callback", overrides: map[string]string{
			"GOOGLE_WEB_REDIRECT_URI": "http://127.0.0.1:8788/api/v1/auth/google/web/callback",
		}},
		{name: "remote HTTP callback", overrides: map[string]string{
			"GOOGLE_WEB_REDIRECT_URI": "http://api.boardly.test/api/v1/auth/google/web/callback",
		}},
		{name: "relative callback", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "/api/v1/auth/google/web/callback"}},
		{name: "wrong callback path", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://api.boardly.test/api/v1/auth/google/web/other"}},
		{name: "callback query", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://api.boardly.test/api/v1/auth/google/web/callback?code=sentinel"}},
		{name: "callback fragment", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://api.boardly.test/api/v1/auth/google/web/callback#fragment"}},
		{name: "callback userinfo", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://user@api.boardly.test/api/v1/auth/google/web/callback"}},
		{name: "callback noncanonical port", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://api.boardly.test:0443/api/v1/auth/google/web/callback"}},
		{name: "callback out-of-range port", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://api.boardly.test:65536/api/v1/auth/google/web/callback"}},
		{name: "callback over byte limit", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://" + strings.Repeat("a", 2050) + ".test/api/v1/auth/google/web/callback"}},
		{name: "callback Unicode host", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://bücher.example/api/v1/auth/google/web/callback"}},
		{name: "callback expanded IPv6 host", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://[0:0:0:0:0:0:0:1]/api/v1/auth/google/web/callback"}},
		{name: "callback short legacy IPv4", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://127.1/api/v1/auth/google/web/callback"}},
		{name: "callback octal legacy IPv4", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://0177.0.0.1/api/v1/auth/google/web/callback"}},
		{name: "callback integer legacy IPv4", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://2130706433/api/v1/auth/google/web/callback"}},
		{name: "callback hex legacy IPv4", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://0x7f000001/api/v1/auth/google/web/callback"}},
		{name: "callback trailing-dot host", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://api.boardly.test./api/v1/auth/google/web/callback"}},
		{name: "callback DNS host ending in decimal number", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://example.127/api/v1/auth/google/web/callback"}},
		{name: "callback DNS host ending in octal number", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://example.0177/api/v1/auth/google/web/callback"}},
		{name: "callback DNS host ending in hex number", overrides: map[string]string{"GOOGLE_WEB_REDIRECT_URI": "https://example.0x7f/api/v1/auth/google/web/callback"}},
		{name: "mixed HTTPS origin forces secure cookie", overrides: map[string]string{
			"AUTH_WEB_ORIGINS":        "http://localhost:5173,https://admin.boardly.test",
			"AUTH_WEB_APP_ORIGIN":     "http://localhost:5173",
			"GOOGLE_WEB_REDIRECT_URI": "http://127.0.0.1:8788/api/v1/auth/google/web/callback",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := completeGoogleOAuthConfig()
			for key, value := range test.overrides {
				values[key] = value
			}
			cfg, err := Load(configuredLookup(values))
			if !test.valid {
				if err == nil {
					t.Fatal("Load() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Auth.Google.AllowInsecureLoopbackWebCallback != test.allowHTTP {
				t.Errorf(
					"AllowInsecureLoopbackWebCallback = %v",
					cfg.Auth.Google.AllowInsecureLoopbackWebCallback,
				)
			}
		})
	}
}

func TestLoadGoogleOAuthBoundsFlowTTLAndProviderTimeout(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		key   string
		value string
		valid bool
	}{
		{key: "GOOGLE_OAUTH_FLOW_TTL_SECONDS", value: "60", valid: true},
		{key: "GOOGLE_OAUTH_FLOW_TTL_SECONDS", value: "900", valid: true},
		{key: "GOOGLE_OAUTH_FLOW_TTL_SECONDS", value: "59"},
		{key: "GOOGLE_OAUTH_FLOW_TTL_SECONDS", value: "901"},
		{key: "GOOGLE_OAUTH_PROVIDER_TIMEOUT_SECONDS", value: "1", valid: true},
		{key: "GOOGLE_OAUTH_PROVIDER_TIMEOUT_SECONDS", value: "30", valid: true},
		{key: "GOOGLE_OAUTH_PROVIDER_TIMEOUT_SECONDS", value: "0"},
		{key: "GOOGLE_OAUTH_PROVIDER_TIMEOUT_SECONDS", value: "31"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			t.Parallel()
			values := completeGoogleOAuthConfig()
			values[test.key] = test.value
			cfg, err := Load(configuredLookup(values))
			if !test.valid {
				if err == nil {
					t.Fatal("Load() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			seconds, err := strconv.Atoi(test.value)
			if err != nil {
				t.Fatalf("Atoi(%q): %v", test.value, err)
			}
			want := time.Duration(seconds) * time.Second
			if test.key == "GOOGLE_OAUTH_FLOW_TTL_SECONDS" && cfg.Auth.Google.FlowTTL != want {
				t.Errorf("FlowTTL = %s", cfg.Auth.Google.FlowTTL)
			}
			if test.key == "GOOGLE_OAUTH_PROVIDER_TIMEOUT_SECONDS" &&
				cfg.Auth.Google.ProviderTimeout != want {
				t.Errorf("ProviderTimeout = %s", cfg.Auth.Google.ProviderTimeout)
			}
		})
	}
}

func TestLoadBoundsDesktopHandoffCodeTTL(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		value string
		valid bool
	}{
		{value: "10", valid: true},
		{value: "300", valid: true},
		{value: "9"},
		{value: "301"},
	} {
		t.Run("AUTH_DESKTOP_HANDOFF_CODE_TTL_SECONDS="+test.value, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(configuredLookup(map[string]string{
				"APP_DATABASE_URL":                      "postgres://boardly:boardly@localhost/boardly",
				"AUTH_DESKTOP_HANDOFF_CODE_TTL_SECONDS": test.value,
			}))
			if !test.valid {
				if err == nil {
					t.Fatal("Load() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			seconds, err := strconv.Atoi(test.value)
			if err != nil {
				t.Fatalf("Atoi(%q): %v", test.value, err)
			}
			want := time.Duration(seconds) * time.Second
			if cfg.Auth.DesktopHandoffCodeTTL != want {
				t.Errorf("DesktopHandoffCodeTTL = %s, want %s", cfg.Auth.DesktopHandoffCodeTTL, want)
			}
		})
	}
}

func completeGoogleOAuthConfig() map[string]string {
	return map[string]string{
		"APP_DATABASE_URL":             "postgres://boardly:boardly@localhost/boardly",
		"AUTH_WEB_ORIGINS":             "https://app.boardly.test",
		"AUTH_WEB_APP_ORIGIN":          "https://app.boardly.test",
		"GOOGLE_WEB_CLIENT_ID":         "web-client.apps.googleusercontent.test",
		"GOOGLE_WEB_CLIENT_SECRET":     "sentinel-web-client-secret",
		"GOOGLE_WEB_REDIRECT_URI":      "https://api.boardly.test/api/v1/auth/google/web/callback",
		"GOOGLE_DESKTOP_CLIENT_ID":     "desktop-client.apps.googleusercontent.test",
		"GOOGLE_DESKTOP_CLIENT_SECRET": "sentinel-desktop-client-secret",
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func configuredLookup(values map[string]string) LookupEnv {
	merged := map[string]string{
		"AUTH_CSRF_SECRET":  "01234567890123456789012345678901",
		"STORAGE_S3_BUCKET": "boardly-test",
		"STORAGE_S3_REGION": "us-east-1",
	}
	for key, value := range values {
		merged[key] = value
	}
	return mapLookup(merged)
}

func TestStudioReceiptNamespaceIsExplicitAndStable(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		_, err := Load(
			configuredLookup(
				map[string]string{
					"APP_DATABASE_URL":             "postgres://localhost/test",
					"APP_STUDIO_RECEIPT_NAMESPACE": value,
				},
			),
		)
		if err == nil {
			t.Fatalf("accepted namespace %q", value)
		}
	}
	const namespace = "12345678-1234-1234-1234-123456789abc"
	cfg, err := Load(
		configuredLookup(
			map[string]string{
				"APP_DATABASE_URL":             "postgres://localhost/test",
				"APP_STUDIO_RECEIPT_NAMESPACE": namespace,
			},
		),
	)
	if err != nil || cfg.StudioReceiptNamespace.String() != namespace {
		t.Fatalf("namespace = %v, %v", cfg.StudioReceiptNamespace, err)
	}
}

func TestLoadStudioAPIOrigin(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, value, want string
		wantError         bool
	}{
		{name: "default", want: "https://api-agent.ii.inc"},
		{name: "loopback", value: "http://localhost:8000/", want: "http://localhost:8000"},
		{name: "canonical", value: "https://STUDIO.example:443/", want: "https://studio.example"},
		{name: "remote http", value: "http://studio.example", wantError: true},
		{name: "path", value: "https://studio.example/api", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(configuredLookup(map[string]string{
				"APP_DATABASE_URL":      "postgres://localhost/boardly",
				"ZENITH_STUDIO_API_URL": test.value,
			}))
			if (err != nil) != test.wantError {
				t.Fatalf("Load() error = %v, wantError %v", err, test.wantError)
			}
			if err == nil && cfg.StudioAPIOrigin != test.want {
				t.Fatalf("StudioAPIOrigin = %q, want %q", cfg.StudioAPIOrigin, test.want)
			}
		})
	}
}
