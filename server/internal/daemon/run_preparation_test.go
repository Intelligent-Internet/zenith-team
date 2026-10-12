package daemon

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"boardly.dev/boardly/server/pkg/runfailure"
)

func TestRunPreparationDiagnosticEventUsesStablePhaseWithoutErrorText(t *testing.T) {
	t.Parallel()

	sensitive := errors.New("workspace /Users/founder/private token_secret")
	err := wrapRunPreparationError(runPreparationPhaseWorkspace, sensitive)
	if !errors.Is(err, sensitive) {
		t.Fatal("wrapped preparation error lost its cause")
	}

	event := runPreparationDiagnosticEvent(fmt.Errorf("prepare run: %w", err))
	if event != "run.preparation_failed.workspace" {
		t.Fatalf("diagnostic event = %q, want workspace phase", event)
	}
	if strings.Contains(event, "founder") || strings.Contains(event, "secret") {
		t.Fatalf("diagnostic event leaked error detail: %q", event)
	}

	got := runPreparationDiagnosticEvent(errors.New("unknown preparation error"))
	if got != "run.preparation_failed" {
		t.Fatalf("fallback diagnostic event = %q, want generic event", got)
	}
}

func TestRunPreparationFailureReasonUsesPrivateCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		err   error
		phase runPreparationPhase
		want  runfailure.Reason
	}{
		{
			name:  "preparation timeout",
			err:   errRunPrepareTimeout,
			phase: runPreparationPhaseWorkspace,
			want:  runfailure.ReasonTimeout,
		},
		{
			name:  "skill bundle unavailable",
			err:   errSkillBundleUnavailable,
			phase: runPreparationPhaseSkillSnapshot,
			want:  runfailure.ReasonSkillBundleUnavailable,
		},
		{
			name:  "invalid run context",
			err:   errConversationSnapshotInvalid,
			phase: runPreparationPhasePrepare,
			want:  runfailure.ReasonRunContextInvalid,
		},
		{
			name:  "provider network failure",
			err:   errors.New("connection refused"),
			phase: runPreparationPhaseProviderSpawn,
			want:  runfailure.ReasonAgentProviderNetwork,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := wrapRunPreparationError(test.phase, test.err)
			if got := runFailureReason(err); got != test.want.String() {
				t.Fatalf("runFailureReason() = %q, want %q", got, test.want)
			}
		})
	}
}
