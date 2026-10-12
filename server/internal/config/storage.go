package config

import (
	"errors"
	"strings"
)

func loadStorageConfig(lookup LookupEnv, environment string) (StorageConfig, error) {
	var defaults StorageConfig
	driver := strings.ToLower(valueOrDefault(lookup, "STORAGE_DRIVER", "s3"))
	switch driver {
	case "s3":
	case "local":
		if environment != "local" {
			return StorageConfig{}, errors.New("STORAGE_DRIVER=local requires APP_ENVIRONMENT=local")
		}
		// Match docker/compose.yml and its silo-init bucket. Local mode uses
		// the same S3 adapter; it changes only development configuration.
		defaults = StorageConfig{
			Bucket: "boardly-attachments", Region: "us-east-1", Endpoint: "http://localhost:9000",
			AccessKeyID: "boardly", SecretAccessKey: "boardly-development-secret", UsePathStyle: true,
		}
	default:
		return StorageConfig{}, errors.New("STORAGE_DRIVER must be s3 or local")
	}
	configuration := StorageConfig{
		Bucket:          valueOrDefault(lookup, "STORAGE_S3_BUCKET", defaults.Bucket),
		Region:          valueOrDefault(lookup, "STORAGE_S3_REGION", defaults.Region),
		Endpoint:        valueOrDefault(lookup, "STORAGE_S3_ENDPOINT", defaults.Endpoint),
		AccessKeyID:     valueOrDefault(lookup, "STORAGE_S3_ACCESS_KEY_ID", ""),
		SecretAccessKey: valueOrDefault(lookup, "STORAGE_S3_SECRET_ACCESS_KEY", ""),
		SessionToken:    valueOrDefault(lookup, "STORAGE_S3_SESSION_TOKEN", ""),
	}
	var err error
	configuration.UsePathStyle, err = boolean(lookup, "STORAGE_S3_USE_PATH_STYLE", defaults.UsePathStyle)
	if err != nil {
		return StorageConfig{}, err
	}
	if configuration.Bucket == "" || configuration.Region == "" {
		return StorageConfig{}, errors.New("STORAGE_S3_BUCKET and STORAGE_S3_REGION are required")
	}
	if (configuration.AccessKeyID == "") != (configuration.SecretAccessKey == "") {
		return StorageConfig{}, errors.New(
			"STORAGE_S3_ACCESS_KEY_ID and STORAGE_S3_SECRET_ACCESS_KEY must be configured together",
		)
	}
	if configuration.AccessKeyID == "" {
		configuration.AccessKeyID = defaults.AccessKeyID
		configuration.SecretAccessKey = defaults.SecretAccessKey
	}
	return configuration, nil
}
