package storage

import (
	"context"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/config"
)

func TestNewFromConfigConstructsS3CompatibleStorage(t *testing.T) {
	t.Parallel()

	store, err := NewFromConfig(context.Background(), config.StorageConfig{
		Bucket:          "zenith-team-test",
		Region:          "us-east-1",
		Endpoint:        "http://localhost:9000",
		AccessKeyID:     "test-access-key",
		SecretAccessKey: "test-secret-key",
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("NewFromConfig() error = %v", err)
	}
	if store == nil || store.bucket != "zenith-team-test" {
		t.Fatalf("NewFromConfig() = %#v, want S3 bucket zenith-team-test", store)
	}
}

func TestNewFromConfigRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := NewFromConfig(context.Background(), config.StorageConfig{}); err == nil {
		t.Fatal("NewFromConfig() error = nil, want configuration error")
	}
}
