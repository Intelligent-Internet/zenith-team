//go:build integration

package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/config"
)

func TestS3AgainstLocalStore(t *testing.T) {
	endpoint := os.Getenv("ZENITH_TEAM_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("ZENITH_TEAM_TEST_S3_ENDPOINT is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := NewS3(ctx, S3Config{
		Bucket: os.Getenv("ZENITH_TEAM_TEST_S3_BUCKET"), Region: "us-east-1", Endpoint: endpoint,
		AccessKeyID:     os.Getenv("ZENITH_TEAM_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("ZENITH_TEAM_TEST_S3_SECRET_ACCESS_KEY"), UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3() error = %v", err)
	}
	t.Run("explicit S3 settings", func(t *testing.T) {
		testLocalRoundTrip(t, ctx, store)
	})
	t.Run("local Silo preset", func(t *testing.T) {
		values := map[string]string{
			"APP_DATABASE_URL":             "postgres://localhost/unused",
			"AUTH_CSRF_SECRET":             "01234567890123456789012345678901",
			"STORAGE_DRIVER":               "local",
			"STORAGE_S3_ENDPOINT":          endpoint,
			"STORAGE_S3_BUCKET":            os.Getenv("ZENITH_TEAM_TEST_S3_BUCKET"),
			"STORAGE_S3_ACCESS_KEY_ID":     os.Getenv("ZENITH_TEAM_TEST_S3_ACCESS_KEY_ID"),
			"STORAGE_S3_SECRET_ACCESS_KEY": os.Getenv("ZENITH_TEAM_TEST_S3_SECRET_ACCESS_KEY"),
		}
		cfg, err := config.Load(func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		localStore, err := NewFromConfig(ctx, cfg.Storage)
		if err != nil {
			t.Fatalf("NewFromConfig() error = %v", err)
		}
		testLocalRoundTrip(t, ctx, localStore)
	})
}

func testLocalRoundTrip(t *testing.T, ctx context.Context, store *S3) {
	t.Helper()

	key := "integration/zenith-team-attachment"
	payload := []byte("silo attachment")
	_ = store.DeleteObject(ctx, key)
	t.Cleanup(func() { _ = store.DeleteObject(context.Background(), key) })
	if err := store.Upload(
		ctx,
		key,
		bytes.NewReader(payload),
		int64(len(payload)),
		"text/plain",
		"file.txt",
	); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	reader, err := store.GetReader(ctx, key)
	if err != nil {
		t.Fatalf("GetReader() error = %v", err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("read = %q, %v, close=%v", got, readErr, closeErr)
	}
	if err := store.DeleteObject(ctx, key); err != nil {
		t.Fatalf("DeleteObject() error = %v", err)
	}
	if _, err := store.GetReader(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetReader() after delete = %v, want ErrNotFound", err)
	}
}
