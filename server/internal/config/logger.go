package config

import (
	"errors"
	"io"
	"log/slog"
)

// NewLogger returns a JSON slog logger configured at level. Config validation uses
// the same closed set, while this boundary also fails closed for direct use.
func NewLogger(destination io.Writer, level string) (*slog.Logger, error) {
	if destination == nil {
		return nil, errors.New("log destination is required")
	}

	var minimum slog.Level
	switch level {
	case "debug":
		minimum = slog.LevelDebug
	case "info":
		minimum = slog.LevelInfo
	case "warn":
		minimum = slog.LevelWarn
	case "error":
		minimum = slog.LevelError
	default:
		return nil, errors.New("log level must be debug, info, warn, or error")
	}

	handler := slog.NewJSONHandler(destination, &slog.HandlerOptions{Level: minimum})
	return slog.New(handler), nil
}
