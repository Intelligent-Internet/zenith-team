package storage

import (
	"fmt"
	"log/slog"

	"boardly.dev/boardly/server/pkg/redact"
)

func (S3) Format(state fmt.State, _ rune) { redact.TypeFormat(state, "storage.S3") }
func (S3) GoString() string               { return redact.TypeString("storage.S3") }
func (S3) LogValue() slog.Value           { return redact.TypeLogValue("storage.S3") }

func (S3Config) Format(state fmt.State, _ rune) { redact.TypeFormat(state, "storage.S3Config") }
func (S3Config) GoString() string               { return redact.TypeString("storage.S3Config") }
func (S3Config) LogValue() slog.Value           { return redact.TypeLogValue("storage.S3Config") }

func (CloudflareRoutes) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "storage.CloudflareRoutes")
}
func (CloudflareRoutes) GoString() string     { return redact.TypeString("storage.CloudflareRoutes") }
func (CloudflareRoutes) LogValue() slog.Value { return redact.TypeLogValue("storage.CloudflareRoutes") }

func (CloudflareRoutesConfig) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "storage.CloudflareRoutesConfig")
}

func (CloudflareRoutesConfig) GoString() string {
	return redact.TypeString("storage.CloudflareRoutesConfig")
}

func (CloudflareRoutesConfig) LogValue() slog.Value {
	return redact.TypeLogValue("storage.CloudflareRoutesConfig")
}
