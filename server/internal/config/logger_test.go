package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestNewLoggerCreatesLevelFilteredJSONLogger(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger, err := NewLogger(&output, "warn")
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}

	logger.Info("not emitted", "component", "server")
	logger.Warn("server warning", "component", "server")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode JSON log: %v\n%s", err, output.String())
	}
	if record["level"] != slog.LevelWarn.String() {
		t.Errorf("level = %v, want %q", record["level"], slog.LevelWarn.String())
	}
	if record["msg"] != "server warning" || record["component"] != "server" {
		t.Errorf("record = %#v", record)
	}
}

func TestNewLoggerRejectsUnknownLevel(t *testing.T) {
	t.Parallel()

	if _, err := NewLogger(&bytes.Buffer{}, "trace"); err == nil {
		t.Fatal("NewLogger() error = nil, want invalid level error")
	}
}
