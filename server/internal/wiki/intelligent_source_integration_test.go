//go:build integration

package wiki

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestSourceReplacementClientTransaction(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-source-replacement.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf(
			"source replacement transaction failed (install frontend dependencies first): %v\n%s",
			err,
			output,
		)
	}
}

func TestDocumentCopyClientIdentities(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-document-copy.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf(
			"document copy identity check failed (install frontend dependencies first): %v\n%s",
			err,
			output,
		)
	}
}
