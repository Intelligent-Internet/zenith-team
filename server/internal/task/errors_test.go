package task

import (
	"errors"
	"fmt"
	"testing"
)

// Both handoff refusals are the one sentinel the 409 mapping matches, so a
// caller branches on the code and the boundary reads RunLive only to pick
// the sentence it shows.
func TestHandoffConfirmationErrorIsTheOneSentinel(t *testing.T) {
	t.Parallel()

	for _, runLive := range []bool{true, false} {
		err := fmt.Errorf("update Task: %w", &HandoffConfirmationError{RunLive: runLive})
		if !errors.Is(err, ErrHandoffConfirmationRequired) {
			t.Fatalf("RunLive=%v: errors.Is(%v, ErrHandoffConfirmationRequired) = false", runLive, err)
		}
		var refusal *HandoffConfirmationError
		if !errors.As(err, &refusal) || refusal.RunLive != runLive {
			t.Fatalf("RunLive=%v: errors.As = %+v", runLive, refusal)
		}
	}
	live := (&HandoffConfirmationError{RunLive: true}).Error()
	finished := (&HandoffConfirmationError{}).Error()
	if live == finished {
		t.Fatalf("both refusals read %q; the live one must say it cancels work", live)
	}
}
