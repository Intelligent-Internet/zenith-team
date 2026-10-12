package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDiscoverRuntimeModelsCodexUsesBundledCatalogArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	t.Parallel()

	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	executablePath := filepath.Join(dir, "codex-fixture")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\n" +
		"printf '%s' '{\"models\":[{\"slug\":\"gpt-visible\",\"display_name\":\"GPT Visible\",\"visibility\":\"list\",\"default_reasoning_level\":\"medium\",\"supported_reasoning_levels\":[{\"effort\":\"low\",\"description\":\"Quick\"},{\"effort\":\"medium\",\"description\":\"Balanced\"}]},{\"slug\":\"gpt-hidden\",\"visibility\":\"hide\"}]}'\n"
	if err := os.WriteFile(executablePath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	models, err := DiscoverRuntimeModels(context.Background(), "codex", executablePath)
	if err != nil {
		t.Fatalf("DiscoverRuntimeModels() error = %v", err)
	}
	rawArgs, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	if got, want := strings.Fields(
		string(rawArgs),
	), []string{
		"debug",
		"models",
		"--bundled",
	}; !equalStrings(
		got,
		want,
	) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if len(models) != 1 || models[0].ID != "gpt-visible" || models[0].Label != "GPT Visible" {
		t.Fatalf("models = %#v", models)
	}
	if !models[0].Default {
		t.Fatal("first visible model should be marked default")
	}
	if models[0].Reasoning == nil || models[0].Reasoning.DefaultLevel != "medium" ||
		len(models[0].Reasoning.Levels) != 2 {
		t.Fatalf("reasoning = %#v", models[0].Reasoning)
	}
}

func TestDiscoverRuntimeModelsClaudeReturnsMaintainedAliasesWithoutExecuting(t *testing.T) {
	t.Parallel()

	models, err := DiscoverRuntimeModels(context.Background(), "claude", filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("DiscoverRuntimeModels() error = %v", err)
	}
	if len(models) == 0 {
		t.Fatal("Claude aliases are empty")
	}
	for _, model := range models {
		if !strings.HasPrefix(model.ID, "claude-") {
			t.Fatalf("unsafe Claude alias %q", model.ID)
		}
		if model.Reasoning == nil || len(model.Reasoning.Levels) == 0 {
			t.Fatalf("model %q has no maintained reasoning catalog", model.ID)
		}
	}
}

func TestParseCodexRuntimeModelsRejectsOversizedCatalog(t *testing.T) {
	t.Parallel()

	models := make([]string, runtimeModelCatalogMaxModels+1)
	for i := range models {
		models[i] = `{"slug":"gpt"}`
	}
	_, err := parseCodexRuntimeModels([]byte(`{"models":[` + strings.Join(models, ",") + `]}`))
	if err == nil {
		t.Fatal("parseCodexRuntimeModels() accepted too many models")
	}
}

func TestParseCodexRuntimeModelsRejectsDuplicateReasoningLevels(t *testing.T) {
	t.Parallel()

	raw := []byte(
		`{"models":[{"slug":"gpt-visible","display_name":"GPT Visible","visibility":"list","default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"high"},{"effort":"high"}]}]}`,
	)
	if _, err := parseCodexRuntimeModels(raw); err == nil {
		t.Fatal("parseCodexRuntimeModels() accepted duplicate reasoning levels")
	}
}

func TestNormalizeRuntimeModelsRejectsMultipleDefaults(t *testing.T) {
	t.Parallel()

	models := []RuntimeModel{
		{ID: "first", Label: "First", Default: true},
		{ID: "second", Label: "Second", Default: true},
	}
	if _, err := NormalizeRuntimeModels(models); err == nil {
		t.Fatal("NormalizeRuntimeModels() accepted multiple defaults")
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
