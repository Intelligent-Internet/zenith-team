package config

import (
	"fmt"
	"log/slog"

	"boardly.dev/boardly/server/pkg/redact"
)

func (StorageConfig) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "config.StorageConfig")
}

func (StorageConfig) GoString() string {
	return redact.TypeString("config.StorageConfig")
}

func (StorageConfig) LogValue() slog.Value {
	return redact.TypeLogValue("config.StorageConfig")
}
