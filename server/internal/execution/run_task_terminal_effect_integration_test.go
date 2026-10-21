//go:build integration

package execution

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

type taskTerminalRecorder struct {
	runs  []generated.Run
	order *[]string
}

func (r *taskTerminalRecorder) ApplyRunTerminalInTx(
	_ context.Context,
	_ *generated.Queries,
	_ *[]events.Event,
	run generated.Run,
) error {
	r.runs = append(r.runs, run)
	if r.order != nil {
		*r.order = append(*r.order, "task")
	}
	return nil
}

func TestCompleteRunAppliesTaskEffectAfterPromotionOnce(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := seedRunningTaskTerminalRun(t, ctx, h)
	output, err := h.queries.InsertOutput(ctx, generated.InsertOutputParams{
		CompanyID: run.CompanyID, RunID: run.ID, PresentationNumber: 1,
		Title: "Retained result", Summary: "Completion evidence",
		PresentationFingerprint: "retained-completion", Type: "file", Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.SourceRunCompleted || output.SourceRunID.UUID != run.ID ||
		output.OnBehalfOfUserID.UUID != h.fixture.UserA {
		t.Fatalf("running delivery attribution = %+v", output)
	}

	var order []string
	recorder := &taskTerminalRecorder{order: &order}
	h.svc.TaskTerminalEffects = recorder
	h.svc.SetRunQueuePromoter(RunQueuePromoterFunc(func(
		context.Context,
		*generated.Queries,
		*[]events.Event,
		generated.Run,
		string,
	) (*uuid.UUID, error) {
		order = append(order, "promote")
		return nil, nil
	}))
	answer := appendTerminalTestItem(t, h, run.ID, TranscriptItemInput{
		ItemKey: "task-terminal-answer",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeText,
			Text: "done",
		}},
	})
	proof := terminalProof(answer, answer.ItemKey)

	if _, err := h.svc.CompleteRun(ctx, run.ID, proof, "", false, ""); err != nil {
		t.Fatalf("complete Task Run: %v", err)
	}
	if len(recorder.runs) != 1 || recorder.runs[0].ID != run.ID ||
		recorder.runs[0].Status != "completed" {
		t.Fatalf("completed Task effects = %+v", recorder.runs)
	}
	if len(order) != 2 || order[0] != "promote" || order[1] != "task" {
		t.Fatalf("terminal effect order = %v, want [promote task]", order)
	}
	if _, err := h.svc.CompleteRun(ctx, run.ID, proof, "", false, ""); err != nil {
		t.Fatalf("replay complete Task Run: %v", err)
	}
	if len(recorder.runs) != 1 || len(order) != 2 {
		t.Fatalf("completion replay repeated effects: runs=%+v order=%v", recorder.runs, order)
	}
	if _, err := h.pool.Exec(ctx, `DELETE FROM runs WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	var retained bool
	if err := h.pool.QueryRow(ctx, `SELECT run_id IS NULL AND source_run_id=$2
		AND on_behalf_of_user_id=$3 AND source_run_completed FROM outputs WHERE id=$1`,
		output.ID, run.ID, h.fixture.UserA).Scan(&retained); err != nil || !retained {
		t.Fatalf("retained completion and attribution = %v, %v, want true", retained, err)
	}
}

func TestFailRunDefersTaskEffectUntilRetryChainEnds(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	parent := seedRunningTaskTerminalRun(t, ctx, h)
	recorder := &taskTerminalRecorder{}
	h.svc.TaskTerminalEffects = recorder

	if _, err := h.svc.FailRun(
		ctx,
		parent.ID,
		emptyTerminalProof(),
		"runtime went offline",
		"",
		string(runfailure.ReasonRuntimeOffline),
		false,
		"",
	); err != nil {
		t.Fatalf("fail retryable Task Run: %v", err)
	}
	if len(recorder.runs) != 0 {
		t.Fatalf("retryable parent applied Task effects = %+v", recorder.runs)
	}
	child, err := h.queries.GetLatestRunForConversation(ctx, h.fixture.ConversationA)
	if err != nil {
		t.Fatalf("load Task retry child: %v", err)
	}
	if child.ParentRunID.UUID != parent.ID {
		t.Fatalf("Task retry child = %+v", child)
	}
	dbtestUpdateRunStatus(t, h, child.ID, "running")
	if _, err := h.svc.FailRun(
		ctx,
		child.ID,
		emptyTerminalProof(),
		"runtime went offline",
		"",
		string(runfailure.ReasonRuntimeOffline),
		false,
		"",
	); err != nil {
		t.Fatalf("fail exhausted Task retry: %v", err)
	}
	if len(recorder.runs) != 1 || recorder.runs[0].ID != child.ID ||
		recorder.runs[0].Status != "failed" {
		t.Fatalf("exhausted retry Task effects = %+v", recorder.runs)
	}
	if _, err := h.svc.FailRun(
		ctx,
		child.ID,
		emptyTerminalProof(),
		"runtime went offline",
		"",
		string(runfailure.ReasonRuntimeOffline),
		false,
		"",
	); err != nil {
		t.Fatalf("replay exhausted Task failure: %v", err)
	}
	if len(recorder.runs) != 1 {
		t.Fatalf("failure replay repeated Task effects = %+v", recorder.runs)
	}
}

func TestCancelRunLeavesTaskResumable(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := seedRunningTaskTerminalRun(t, ctx, h)
	recorder := &taskTerminalRecorder{}
	h.svc.TaskTerminalEffects = recorder

	if _, err := h.svc.CancelRun(ctx, run.ID, "stopped"); err != nil {
		t.Fatalf("cancel Task Run: %v", err)
	}
	if len(recorder.runs) != 0 {
		t.Fatalf("cancelled Run applied Task effects = %+v", recorder.runs)
	}
	var taskStatus string
	if err := h.pool.QueryRow(ctx, `SELECT status FROM tasks WHERE id = $1`, run.TaskID.UUID).
		Scan(&taskStatus); err != nil {
		t.Fatalf("read Task after Run cancellation: %v", err)
	}
	if taskStatus != "in_progress" {
		t.Fatalf("Task status after Run cancellation = %q, want in_progress", taskStatus)
	}
	if _, err := h.svc.CancelRun(ctx, run.ID, "stopped"); err != nil {
		t.Fatalf("replay cancel Task Run: %v", err)
	}
	if len(recorder.runs) != 0 {
		t.Fatalf("cancellation replay repeated Task effects = %+v", recorder.runs)
	}
}

func TestBulkFailureAppliesTaskEffectOnlyWithoutRetry(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	recorder := &taskTerminalRecorder{}
	h.svc.TaskTerminalEffects = recorder

	if _, err := h.pool.Exec(ctx, `
		UPDATE tasks
		SET status = 'in_progress', execution_generation = 7
		WHERE id = $1`, h.fixture.TaskA); err != nil {
		t.Fatalf("prepare Task: %v", err)
	}
	var retryableID, exhaustedID uuid.UUID
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id, execution_authorized_by_user_id,
			status, attempt, task_execution_generation
		, run_type)
		VALUES ($1, $2, $3, $4, $5, $6, 'running', 1, 7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`, h.fixture.CompanyA, h.fixture.AgentA, h.fixture.TaskA,
		h.fixture.ConversationA, h.fixture.RuntimeA, h.fixture.UserA).Scan(&retryableID); err != nil {
		t.Fatalf("insert retryable Task Run: %v", err)
	}
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id, execution_authorized_by_user_id,
			status, attempt, task_execution_generation
		, run_type)
		VALUES ($1, $2, $3, $4, $5, $6, 'running', 2, 7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`, h.fixture.CompanyA, h.fixture.AgentA, h.fixture.TaskA,
		h.fixture.ConversationA, h.fixture.RuntimeA, h.fixture.UserA).Scan(&exhaustedID); err != nil {
		t.Fatalf("insert exhausted Task Run: %v", err)
	}
	if _, err := h.pool.Exec(
		ctx,
		`UPDATE runtimes SET status = 'offline' WHERE id = $1`,
		h.fixture.RuntimeA,
	); err != nil {
		t.Fatalf("mark Runtime offline: %v", err)
	}
	batch, err := h.svc.FailRunsForOfflineRuntimes(ctx)
	if err != nil {
		t.Fatalf("fail Task Runs for offline Runtime: %v", err)
	}
	if len(batch.Runs) != 2 || batch.RetryCount != 1 {
		t.Fatalf("bulk Task failure = %+v", batch)
	}
	if len(recorder.runs) != 1 || recorder.runs[0].ID != exhaustedID {
		t.Fatalf("bulk Task effects = %+v, retryable Run was %d", recorder.runs, retryableID)
	}
}

func seedRunningTaskTerminalRun(
	t *testing.T,
	ctx context.Context,
	h *runHarness,
) generated.Run {
	t.Helper()
	if _, err := h.pool.Exec(ctx, `
		UPDATE tasks
		SET status = 'in_progress'
		WHERE id = $1`, h.fixture.TaskA); err != nil {
		t.Fatalf("prepare Task: %v", err)
	}
	var pending []events.Event
	run, err := EnqueueRunInTx(ctx, h.queries, &pending, RunTrigger{
		CompanyID:          h.fixture.CompanyA,
		ConversationID:     h.fixture.ConversationA,
		AgentID:            h.fixture.AgentA,
		TaskID:             util.NullUUIDFromPtr(&h.fixture.TaskA),
		Source:             RunSourceMessage,
		AuthorizedByUserID: h.fixture.UserA,
	})
	if err != nil {
		t.Fatalf("enqueue Task Run: %v", err)
	}
	dbtestUpdateRunStatus(t, h, run.ID, "running")
	run, err = h.queries.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("load running Task Run: %v", err)
	}
	return run
}
