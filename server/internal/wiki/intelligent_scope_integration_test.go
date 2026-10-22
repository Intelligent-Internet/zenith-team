//go:build integration

package wiki

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestUIScopeDefinitionMigrationInClient(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-intelligent-scope.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("client scope contract failed (install frontend dependencies first): %v\n%s", err, output)
	}
}

func TestUIClientVersionBoundaries(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-intelligent-versions.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("client version boundaries failed (install frontend dependencies first): %v\n%s", err, output)
	}
}

func TestUIScopeDefinitionCache(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-scope-definition-cache.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("scope definition cache failed (install frontend dependencies first): %v\n%s", err, output)
	}
}

func TestUIPreviewInteractionPolicy(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-preview-interactions.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("preview interaction policy failed (install frontend dependencies first): %v\n%s", err, output)
	}
}

func TestUIPerformanceMeasurements(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-ui-performance.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("UI performance measurements failed: %v\n%s", err, output)
	}
}

func TestUITaskSummaryInClient(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-task-summary.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Task summary: %v\n%s", err, output)
	}
}
