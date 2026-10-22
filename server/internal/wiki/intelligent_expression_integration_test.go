//go:build integration

package wiki

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestUISharedExpressionFixturesInClient(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-intelligent-expressions.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("client expression contract failed (install frontend dependencies first): %v\n%s", err, output)
	}
}

func TestUIEvaluationPoolClientLifecycle(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-evaluation-pool.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf(
			"client evaluation pool lifecycle failed (install frontend dependencies first): %v\n%s",
			err,
			output,
		)
	}
}

func TestUICompiledExpressionCache(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-compiled-expression-cache.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compiled expression cache failed (install frontend dependencies first): %v\n%s", err, output)
	}
}
