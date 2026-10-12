package storage

import (
	"context"
	"fmt"

	"boardly.dev/boardly/server/internal/config"
)

// NewFromConfig constructs the process-wide S3-compatible object store.
// AWS S3 and Silo use the same adapter with different endpoint credentials.
func NewFromConfig(ctx context.Context, configuration config.StorageConfig) (*S3, error) {
	store, err := NewS3(ctx, S3Config{
		Bucket:          configuration.Bucket,
		Region:          configuration.Region,
		Endpoint:        configuration.Endpoint,
		AccessKeyID:     configuration.AccessKeyID,
		SecretAccessKey: configuration.SecretAccessKey,
		SessionToken:    configuration.SessionToken,
		UsePathStyle:    configuration.UsePathStyle,
	})
	if err != nil {
		return nil, fmt.Errorf("construct S3-compatible object storage: %w", err)
	}
	return store, nil
}
