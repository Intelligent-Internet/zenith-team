package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoadLocalStorageUsesSiloDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load(mapLookup(map[string]string{
		"APP_REDIS_URL":    "redis://localhost:6379/0",
		"APP_DATABASE_URL": "postgres://boardly:boardly@localhost/boardly",
		"AUTH_CSRF_SECRET": "01234567890123456789012345678901",
		"STORAGE_DRIVER":   "local",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := StorageConfig{
		Bucket: "boardly-attachments", Region: "us-east-1", Endpoint: "http://localhost:9000",
		AccessKeyID: "boardly", SecretAccessKey: "boardly-development-secret", UsePathStyle: true,
	}
	if !reflect.DeepEqual(cfg.Storage, want) {
		t.Fatal("local storage configuration does not match the Docker Silo defaults")
	}
}

func TestLoadLocalStorageAllowsExplicitS3Overrides(t *testing.T) {
	t.Parallel()

	cfg, err := Load(mapLookup(map[string]string{
		"APP_REDIS_URL":                "redis://localhost:6379/0",
		"APP_DATABASE_URL":             "postgres://boardly:boardly@localhost/boardly",
		"AUTH_CSRF_SECRET":             "01234567890123456789012345678901",
		"STORAGE_DRIVER":               "local",
		"STORAGE_S3_BUCKET":            "custom-attachments",
		"STORAGE_S3_REGION":            "eu-west-1",
		"STORAGE_S3_ENDPOINT":          "http://localhost:19000",
		"STORAGE_S3_ACCESS_KEY_ID":     "custom-access",
		"STORAGE_S3_SECRET_ACCESS_KEY": "custom-secret",
		"STORAGE_S3_SESSION_TOKEN":     "custom-token",
		"STORAGE_S3_USE_PATH_STYLE":    "false",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := StorageConfig{
		Bucket: "custom-attachments", Region: "eu-west-1", Endpoint: "http://localhost:19000",
		AccessKeyID: "custom-access", SecretAccessKey: "custom-secret", SessionToken: "custom-token",
	}
	if !reflect.DeepEqual(cfg.Storage, want) {
		t.Fatal("local storage configuration did not preserve explicit S3 overrides")
	}
}

func TestLoadRejectsInvalidStorageModeConfiguration(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		values map[string]string
		want   string
	}{
		"unknown driver": {
			values: map[string]string{"STORAGE_DRIVER": "filesystem"},
			want:   "STORAGE_DRIVER must be s3 or local",
		},
		"local production": {
			values: map[string]string{"STORAGE_DRIVER": "local", "APP_ENVIRONMENT": "production"},
			want:   "STORAGE_DRIVER=local requires APP_ENVIRONMENT=local",
		},
		"local staging": {
			values: map[string]string{"STORAGE_DRIVER": "local", "APP_ENVIRONMENT": "staging"},
			want:   "STORAGE_DRIVER=local requires APP_ENVIRONMENT=local",
		},
		"local access key without secret": {
			values: map[string]string{"STORAGE_DRIVER": "local", "STORAGE_S3_ACCESS_KEY_ID": "custom-access"},
			want:   "STORAGE_S3_ACCESS_KEY_ID and STORAGE_S3_SECRET_ACCESS_KEY must be configured together",
		},
		"local secret without access key": {
			values: map[string]string{"STORAGE_DRIVER": "local", "STORAGE_S3_SECRET_ACCESS_KEY": "custom-secret"},
			want:   "STORAGE_S3_ACCESS_KEY_ID and STORAGE_S3_SECRET_ACCESS_KEY must be configured together",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			test.values["APP_DATABASE_URL"] = "postgres://boardly:boardly@localhost/boardly"
			_, err := Load(configuredLookup(test.values))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
		})
	}
}
