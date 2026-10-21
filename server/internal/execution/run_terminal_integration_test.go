//go:build integration

package execution

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/telemetry"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

func TestCancelledRunAcknowledgesLateTerminalCallbackWithoutChangingHistory(t *testing.T) {
	t.Parallel()
	for _, callback := range []string{"complete", "fail"} {
		t.Run(callback, func(t *testing.T) {
			t.Parallel()
			h := newRunHarness(t)
			ctx := t.Context()
			runID := seedRunningTranscriptRun(t, h)
			head := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
				ItemKey: "late-answer",
				Role:    protocol.RunItemRoleAgent,
				Parts:   []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "done"}},
			})
			cancelled, err := h.svc.CancelRun(ctx, runID, "user stopped execution")
			if err != nil {
				t.Fatal(err)
			}
			readHistory := func() []activity.Entry {
				t.Helper()
				rows, _, err := activity.NewFeed(h.queries).
					List(ctx, h.fixture.CompanyA, activity.ListOptions{
						UserID:     h.fixture.UserA,
						EntityType: "run",
						EntityID:   runID.String(),
						Limit:      20,
					})
				if err != nil {
					t.Fatal(err)
				}
				return rows
			}
			before := readHistory()
			finish := func(proof TerminalTranscriptProof) (*generated.Run, error) {
				if callback == "complete" {
					return h.svc.CompleteRun(ctx, runID, proof, "", false, "")
				}
				return h.svc.FailRun(
					ctx,
					runID,
					proof,
					"provider disconnected",
					"",
					"provider_network",
					false,
					"",
				)
			}
			proof := terminalProof(head, head.ItemKey)
			for range 2 {
				got, err := finish(proof)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, cancelled) {
					t.Fatalf("late callback = %+v, want unchanged %+v", got, cancelled)
				}
			}
			wrong := proof
			wrong.ThroughSeq--
			if _, err := finish(wrong); !errors.Is(err, ErrTranscriptHeadMismatch) {
				t.Fatalf("mismatched proof error = %v, want ErrTranscriptHeadMismatch", err)
			}
			if got := readHistory(); !reflect.DeepEqual(got, before) {
				t.Fatalf("late callback changed history: got %+v, want %+v", got, before)
			}
			if h.recorder.topicCount(EventRunFailed) != 0 ||
				h.recorder.topicCount(EventRunCompleted) != 0 ||
				h.recorder.topicCount(EventRunQueued) != 0 {
				t.Fatal("late callback published a terminal transition or retry")
			}
			// The late callbacks persisted nothing, so the metrics saw
			// nothing beyond the cancellation itself.
			if got := h.observer.terminals(); len(got) != 0 {
				t.Fatalf("late callback observed terminal outcomes %+v, want none", got)
			}
		})
	}
}

// TestTerminalCallbacksObserveEachNewOutcomeOnce pins the run_terminal_total
// producer: a terminal callback that persists a NEW terminal row hands the
// registry exactly one observation carrying the bound runtime's provider and
// the outcome class, and the daemon's retry of the same callback (already
// finalized) hands it nothing.
func TestTerminalCallbacksObserveEachNewOutcomeOnce(t *testing.T) {
	t.Parallel()
	type callback struct {
		failureReason string // "" completes the Run; anything else fails it
		withFinalText bool
	}
	tests := []struct {
		name string
		callback
		want telemetry.RunTerminalObservation
	}{
		{
			name:     "completed with final text",
			callback: callback{withFinalText: true},
			want:     telemetry.RunTerminalObservation{Provider: "codex", Outcome: "message"},
		},
		{
			name: "completed without final text",
			want: telemetry.RunTerminalObservation{Provider: "codex", Outcome: "no_response"},
		},
		{
			name:     "failed inside the provider",
			callback: callback{failureReason: runfailure.ReasonAgentProviderAuthOrAccess.String()},
			want: telemetry.RunTerminalObservation{
				Provider: "codex", Outcome: "failed", FailureKind: "provider",
			},
		},
		{
			name:     "failed by the platform",
			callback: callback{failureReason: runfailure.ReasonTimeout.String()},
			want: telemetry.RunTerminalObservation{
				Provider: "codex", Outcome: "failed", FailureKind: "infrastructure",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newRunHarness(t)
			recordPromotedOutputs(h)
			runID := seedRunningTranscriptRun(t, h)
			proof := emptyTerminalProof()
			if test.withFinalText {
				head := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
					ItemKey: "answer",
					Role:    protocol.RunItemRoleAgent,
					Parts: []protocol.RunItemPart{{
						Type: protocol.RunItemPartTypeText, Text: "done",
					}},
				})
				proof = terminalProof(head, head.ItemKey)
			}
			// The daemon may send the same terminal callback twice; only the
			// first persists anything.
			for attempt := range 2 {
				var err error
				if test.failureReason == "" {
					_, err = h.svc.CompleteRun(t.Context(), runID, proof, "", false, "")
				} else {
					_, err = h.svc.FailRun(t.Context(), runID, proof, "provider said no", "",
						test.failureReason, false, "")
				}
				if err != nil {
					t.Fatalf("terminal callback attempt %d: %v", attempt+1, err)
				}
			}
			got := h.observer.terminals()
			if !reflect.DeepEqual(got, []telemetry.RunTerminalObservation{test.want}) {
				t.Fatalf("terminal observations = %+v, want exactly one %+v", got, test.want)
			}
		})
	}
}

// TestTerminalOutcomesReachTheMetricsEndpoint scrapes the real registry —
// the handler behind /metrics — after a completion and a failure, pinning
// the two samples the runbook alert and the dashboard's Run panels query.
func TestTerminalOutcomesReachTheMetricsEndpoint(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	recordPromotedOutputs(h)
	registry := telemetry.NewRegistry()
	h.svc.RunObserver = registry

	// One Conversation, two Runs in turn: the seed helper's direct
	// Conversation is unique per agent and member.
	conversationID := h.seedDirectConversation(t)
	completedID := h.seedQueuedRun(t, conversationID)
	dbtestUpdateRunStatus(t, h, completedID, "running")
	head := appendTerminalTestItem(t, h, completedID, TranscriptItemInput{
		ItemKey: "answer",
		Role:    protocol.RunItemRoleAgent,
		Parts:   []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "done"}},
	})
	proof := terminalProof(head, head.ItemKey)
	if _, err := h.svc.CompleteRun(t.Context(), completedID, proof, "", false, ""); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	failedID := h.seedQueuedRun(t, conversationID)
	dbtestUpdateRunStatus(t, h, failedID, "running")
	_, err := h.svc.FailRun(t.Context(), failedID, emptyTerminalProof(), "the machine timed out", "",
		runfailure.ReasonTimeout.String(), false, "")
	if err != nil {
		t.Fatalf("fail run: %v", err)
	}

	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, want := range []string{
		`zenith_team_run_terminal_total{failure_kind="none",outcome="message",provider="codex"} 1`,
		`zenith_team_run_terminal_total{failure_kind="infrastructure",outcome="failed",provider="codex"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestCompleteRunRequiresCommittedTranscriptProofAndCanonicalFinalItem(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)

	var promotedOutputs []string
	h.svc.SetRunQueuePromoter(RunQueuePromoterFunc(func(
		_ context.Context,
		_ *generated.Queries,
		_ *[]events.Event,
		_ generated.Run,
		output string,
	) (*uuid.UUID, error) {
		promotedOutputs = append(promotedOutputs, output)
		return nil, nil
	}))

	oldAnswer := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
		ItemKey: "answer-old",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeText,
			Text: "old",
		}},
	})
	answer := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
		ItemKey: "answer-final",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{
			{Type: protocol.RunItemPartTypeReasoning, Text: "do not publish"},
			{Type: protocol.RunItemPartTypeText, Text: "Hello, "},
			{Type: protocol.RunItemPartTypeText, Text: "world."},
		},
	})
	reasoning := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
		ItemKey: "reasoning-after-answer",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeReasoning,
			Text: "private reasoning",
		}},
	})
	head := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
		ItemKey: "tool-after-answer",
		Role:    protocol.RunItemRoleTool,
		Parts: []protocol.RunItemPart{{
			Type:   protocol.RunItemPartTypeToolResult,
			CallID: "call-1",
			Output: "done",
		}},
	})
	proof := terminalProof(head, answer.ItemKey)

	wrongSequence := proof
	wrongSequence.ThroughSeq--
	invalidFinalItems := []TerminalTranscriptProof{
		terminalProof(head, "missing"),
		terminalProof(head, oldAnswer.ItemKey),
		terminalProof(head, reasoning.ItemKey),
		terminalProof(head, head.ItemKey),
	}

	if _, err := h.svc.CompleteRun(
		ctx,
		runID,
		wrongSequence,
		"",
		false,
		"",
	); !errors.Is(
		err,
		ErrTranscriptHeadMismatch,
	) {
		t.Fatalf("CompleteRun(head mismatch) error = %v, want ErrTranscriptHeadMismatch", err)
	}
	assertTerminalTestRunStatus(t, h, runID, "running")
	for _, invalid := range invalidFinalItems {
		if _, err := h.svc.CompleteRun(
			ctx,
			runID,
			invalid,
			"",
			false,
			"",
		); !errors.Is(
			err,
			ErrTerminalFinalItemInvalid,
		) {
			t.Fatalf("CompleteRun(final item %q) error = %v, want ErrTerminalFinalItemInvalid",
				invalid.FinalItemKey, err)
		}
		assertTerminalTestRunStatus(t, h, runID, "running")
	}
	if len(promotedOutputs) != 0 {
		t.Fatalf("proof failures promoted outputs = %q, want none", promotedOutputs)
	}

	completed, err := h.svc.CompleteRun(ctx, runID, proof, "session-1", false, "")
	if err != nil {
		t.Fatalf("CompleteRun(valid proof) error = %v", err)
	}
	if completed.Status != "completed" {
		t.Fatalf("CompleteRun(valid proof) status = %q, want completed", completed.Status)
	}
	if len(promotedOutputs) != 1 || promotedOutputs[0] != "Hello, world." {
		t.Fatalf("promoted outputs = %q, want canonical transcript text", promotedOutputs)
	}

	// A terminal callback replay is successful only for the same committed
	// head and does not materialize the Conversation output twice.
	replayed, err := h.svc.CompleteRun(ctx, runID, proof, "session-1", false, "")
	if err != nil {
		t.Fatalf("CompleteRun(replay) error = %v", err)
	}
	if replayed.ID != completed.ID || len(promotedOutputs) != 1 {
		t.Fatalf("CompleteRun(replay) = %+v, outputs = %q", replayed, promotedOutputs)
	}
	if _, err := h.svc.CompleteRun(
		ctx,
		runID,
		wrongSequence,
		"session-1",
		false,
		"",
	); !errors.Is(
		err,
		ErrTranscriptHeadMismatch,
	) {
		t.Fatalf("CompleteRun(replay mismatch) error = %v, want ErrTranscriptHeadMismatch", err)
	}
}

func TestFailRunRequiresCommittedTranscriptHeadWithoutFinalItem(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)

	head := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
		ItemKey: "partial-error",
		Role:    protocol.RunItemRoleSystem,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeError,
			Text: "provider failed",
		}},
	})
	proof := terminalProof(head, "")
	wrong := proof
	wrong.ThroughSeq--

	if _, err := h.svc.FailRun(
		ctx,
		runID,
		wrong,
		"provider failed",
		"",
		string(runfailure.ReasonAgentUnknown),
		false,
		"",
	); !errors.Is(err, ErrTranscriptHeadMismatch) {
		t.Fatalf("FailRun(head mismatch) error = %v, want ErrTranscriptHeadMismatch", err)
	}
	assertTerminalTestRunStatus(t, h, runID, "running")

	failed, err := h.svc.FailRun(
		ctx,
		runID,
		proof,
		"provider failed",
		"",
		string(runfailure.ReasonAgentUnknown),
		false,
		"",
	)
	if err != nil {
		t.Fatalf("FailRun(valid proof) error = %v", err)
	}
	if failed.Status != "failed" {
		t.Fatalf("FailRun(valid proof) status = %q, want failed", failed.Status)
	}

	replayed, err := h.svc.FailRun(
		ctx,
		runID,
		proof,
		"provider failed",
		"",
		string(runfailure.ReasonAgentUnknown),
		false,
		"",
	)
	if err != nil {
		t.Fatalf("FailRun(replay) error = %v", err)
	}
	if replayed.ID != failed.ID {
		t.Fatalf("FailRun(replay) ID = %d, want %d", replayed.ID, failed.ID)
	}
	if _, err := h.svc.FailRun(
		ctx,
		runID,
		wrong,
		"provider failed",
		"",
		string(runfailure.ReasonAgentUnknown),
		false,
		"",
	); !errors.Is(err, ErrTranscriptHeadMismatch) {
		t.Fatalf("FailRun(replay mismatch) error = %v, want ErrTranscriptHeadMismatch", err)
	}
}

// TestCompleteRunWithoutFinalTextCompletesWithEmptyOutput proves the provider
// contract's "successful turn, no deliverable text" signal survives the whole
// terminal path: an empty finalItemKey completes the Run and hands the
// promoter an empty output, instead of rejecting the proof and letting the
// daemon downgrade a successful run to failed. Here the run committed tool
// work only — side effects happened, no agent text was produced.
func TestCompleteRunWithoutFinalTextCompletesWithEmptyOutput(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	promotedOutputs := recordPromotedOutputs(h)

	runID := seedRunningTranscriptRun(t, h)
	head := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
		ItemKey: "tool-only",
		Role:    protocol.RunItemRoleTool,
		Parts: []protocol.RunItemPart{{
			Type:   protocol.RunItemPartTypeToolResult,
			CallID: "call-1",
			Output: "side effect done",
		}},
	})
	completed, err := h.svc.CompleteRun(
		ctx, runID, terminalProof(head, ""), "session-1", false, "",
	)
	if err != nil {
		t.Fatalf("CompleteRun(tool-only, empty final item) error = %v", err)
	}
	if completed.Status != "completed" {
		t.Fatalf("CompleteRun(tool-only) status = %q, want completed", completed.Status)
	}
	if got := *promotedOutputs; len(got) != 1 || got[0] != "" {
		t.Fatalf("promoted outputs = %q, want one empty output", got)
	}
}

// TestCompleteRunWithEmptyTranscriptCompletes covers the extreme of the same
// signal: a successful run that committed no transcript items at all.
func TestCompleteRunWithEmptyTranscriptCompletes(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	promotedOutputs := recordPromotedOutputs(h)

	runID := seedRunningTranscriptRun(t, h)
	completed, err := h.svc.CompleteRun(ctx, runID, emptyTerminalProof(), "", false, "")
	if err != nil {
		t.Fatalf("CompleteRun(empty transcript, empty final item) error = %v", err)
	}
	if completed.Status != "completed" {
		t.Fatalf("CompleteRun(empty transcript) status = %q, want completed", completed.Status)
	}
	if got := *promotedOutputs; len(got) != 1 || got[0] != "" {
		t.Fatalf("promoted outputs = %q, want one empty output", got)
	}
}

func recordPromotedOutputs(h *runHarness) *[]string {
	var outputs []string
	h.svc.SetRunQueuePromoter(RunQueuePromoterFunc(func(
		_ context.Context,
		_ *generated.Queries,
		_ *[]events.Event,
		_ generated.Run,
		output string,
	) (*uuid.UUID, error) {
		outputs = append(outputs, output)
		return nil, nil
	}))
	return &outputs
}

func assertTerminalTestRunStatus(t *testing.T, h *runHarness, runID uuid.UUID, want string) {
	t.Helper()
	got, err := h.queries.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun(%d) error = %v", runID, err)
	}
	if got.Status != want {
		t.Fatalf("Run status = %q, want %q", got.Status, want)
	}
}

// 'deferred' must never be a terminal-by-neglect state: a deferred retry
// child whose runtime never polls again (daemon dead, machine retired) blocks
// its conversation forever, because only the daemon claim path promoted due
// deferred runs. The sweeper arm promotes them server-side; the ordinary
// queued TTL then applies, so the conversation always converges.
func TestPromoteDueDeferredRunsRecoversRunsWhoseRuntimeNeverPolls(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conversationID := h.seedDirectConversation(t)
	due := h.seedQueuedRun(t, conversationID)
	dbtestUpdateRunStatus(t, h, due, "deferred")
	if _, err := h.pool.Exec(
		ctx,
		`UPDATE runs SET fire_at = now() - interval '1 minute' WHERE id = $1`, due,
	); err != nil {
		t.Fatalf("arm due deferred run: %v", err)
	}

	notDue := h.seedQueuedRun(t, conversationID)
	dbtestUpdateRunStatus(t, h, notDue, "deferred")
	if _, err := h.pool.Exec(
		ctx,
		`UPDATE runs SET fire_at = now() + interval '1 hour' WHERE id = $1`, notDue,
	); err != nil {
		t.Fatalf("arm future deferred run: %v", err)
	}

	promoted, err := h.svc.PromoteDueDeferredRuns(ctx)
	if err != nil {
		t.Fatalf("PromoteDueDeferredRuns() error = %v", err)
	}
	if len(promoted) != 1 || promoted[0].ID != due {
		t.Fatalf("promoted %d runs (%v), want exactly the due one", len(promoted), promoted)
	}
	assertTerminalTestRunStatus(t, h, due, "queued")
	assertTerminalTestRunStatus(t, h, notDue, "deferred")
}

// A provider can report "the context window is full" as a SUCCESSFUL final
// answer (Claude Code's prompt_too_long escape; the Codex family has text
// shapes of its own). Recording that as a completion keeps the poisoned
// session pointer, so every follow-up resumes an exhausted session. The
// terminal boundary re-reads the output: an exhausted "completion" refuses to
// complete, and the daemon-facing caller converts it into a failed run with
// the context-overflow reason and a retired session.
func TestCompleteRunRefusesContextExhaustedOutput(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)

	head := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
		ItemKey: "exhausted-final",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeText,
			Text: "Prompt is too long and the conversation cannot be compacted further.",
		}},
	})
	proof := terminalProof(head, "exhausted-final")

	if _, err := h.svc.CompleteRun(
		ctx,
		runID,
		proof,
		"poisoned-session-1",
		false,
		"",
	); !errors.Is(
		err,
		ErrRunContextExhausted,
	) {
		t.Fatalf("CompleteRun(exhausted output) error = %v, want ErrRunContextExhausted", err)
	}
	assertTerminalTestRunStatus(t, h, runID, "running")

	failed, err := h.svc.FailRun(
		ctx, runID, proof,
		"provider reported the context window is exhausted",
		"", string(runfailure.ReasonAgentContextOverflow),
		false, "poisoned-session-1",
	)
	if err != nil {
		t.Fatalf("FailRun(context overflow) error = %v", err)
	}
	if failed.Status != "failed" {
		t.Fatalf("status = %q, want failed", failed.Status)
	}
	var reason, retired string
	if err := h.pool.QueryRow(
		ctx,
		`SELECT failure_reason, COALESCE(retired_session_id, '') FROM runs WHERE id = $1`, runID,
	).Scan(&reason, &retired); err != nil {
		t.Fatalf("read failed run: %v", err)
	}
	if reason != string(runfailure.ReasonAgentContextOverflow) || retired != "poisoned-session-1" {
		t.Fatalf(
			"failure_reason = %q retired_session = %q, want context overflow with the session retired",
			reason,
			retired,
		)
	}
}

// Bug R1: a refused terminal transition has to hand the daemon the server's
// OWN head. Without it the daemon can only re-send the proof it already has,
// which is refused again — the Run never reaches a terminal state and the
// answer it already committed is stranded. The 409 body is built from this
// error, so what it carries is what the daemon can reconcile against.
func TestTerminalHeadMismatchCarriesTheServerHeadToReconcileAgainst(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)

	// The daemon's view is one item behind: it never saw this append's
	// receipt, which is exactly how the two heads drift apart in production.
	stale := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
		ItemKey: "seen-by-daemon",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeText, Text: "first",
		}},
	})
	current := appendTerminalTestItem(t, h, runID, TranscriptItemInput{
		ItemKey: "acknowledgement-lost",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeText, Text: "second",
		}},
	})

	staleProof := terminalProof(stale, stale.ItemKey)
	_, err := h.svc.CompleteRun(ctx, runID, staleProof, "", false, "")
	var mismatch *TranscriptHeadMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("CompleteRun(stale proof) error = %v, want *TranscriptHeadMismatchError", err)
	}
	if !errors.Is(err, ErrTranscriptHeadMismatch) {
		t.Fatalf("CompleteRun(stale proof) does not satisfy ErrTranscriptHeadMismatch: %v", err)
	}
	if mismatch.RunID != runID ||
		mismatch.ProofThroughSeq != staleProof.ThroughSeq {
		t.Fatalf("mismatch echoes the wrong proof: %+v", mismatch)
	}
	// The reconciliation half: the server's head, not the daemon's.
	if mismatch.ServerThroughSeq != current.ThroughSeq {
		t.Fatalf(
			"mismatch server head = %d, want the committed head %d",
			mismatch.ServerThroughSeq, current.ThroughSeq,
		)
	}
	body := mismatch.Head()
	if body.RunID != runID || body.ThroughSeq != current.ThroughSeq {
		t.Fatalf("409 reconciliation body = %+v", body)
	}
	assertTerminalTestRunStatus(t, h, runID, "running")

	// And a daemon that adopts the reported head completes on the next try,
	// which is the whole point of reporting it.
	reconciled := TerminalTranscriptProof{
		ThroughSeq:   body.ThroughSeq,
		FinalItemKey: current.ItemKey,
	}
	h.svc.SetRunQueuePromoter(RunQueuePromoterFunc(func(
		context.Context, *generated.Queries, *[]events.Event, generated.Run, string,
	) (*uuid.UUID, error) {
		return nil, nil
	}))
	completed, err := h.svc.CompleteRun(ctx, runID, reconciled, "", false, "")
	if err != nil {
		t.Fatalf("CompleteRun(reconciled proof) error = %v", err)
	}
	if completed.Status != "completed" {
		t.Fatalf("CompleteRun(reconciled proof) status = %q, want completed", completed.Status)
	}
}
