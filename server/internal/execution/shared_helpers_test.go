package execution

// Helpers shared by this package's unit tests and its `//go:build integration`
// suites. They live in an untagged file on purpose: declared inside a tagged
// file, the untagged build of this package would fail with `undefined:`.

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func terminalProof(receipt TranscriptItemReceipt, finalItemKey string) TerminalTranscriptProof {
	return TerminalTranscriptProof{
		ThroughSeq:   receipt.ThroughSeq,
		FinalItemKey: finalItemKey,
	}
}

func emptyTerminalProof() TerminalTranscriptProof {
	return TerminalTranscriptProof{}
}

func appendTerminalTestItem(
	t *testing.T,
	h *runHarness,
	runID uuid.UUID,
	input TranscriptItemInput,
) TranscriptItemReceipt {
	t.Helper()
	receipt, err := h.svc.AppendTranscriptItem(
		context.Background(), h.fixture.RuntimeA, runID, input,
	)
	if err != nil {
		t.Fatalf("AppendTranscriptItem(%q) error = %v", input.ItemKey, err)
	}
	return receipt
}
