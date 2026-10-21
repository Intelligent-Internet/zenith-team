//go:build integration

package task

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func TestTaskHistoryKeepsBusinessChangesAndRollsBackFailedTerminalCapture(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status='in_progress' WHERE id=$1`, fixture.TaskA)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := newTestBus(t)
	if err := activity.RegisterListeners(bus, store.Queries()); err != nil {
		t.Fatal(err)
	}
	var announcements []events.Event
	if err := bus.SubscribeAll(func(_ context.Context, event events.Event) error {
		announcements = append(announcements, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tasks, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	title := "Business requirement"
	changed, err := tasks.Update(ctx, principal, fixture.TaskA, UpdateInput{ExpectedRevision: 1, Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	var run generated.Run
	var pending []events.Event
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		run,
			err = execution.EnqueueRunInTx(ctx,
			q,
			&pending,
			execution.RunTrigger{
				CompanyID:      fixture.CompanyA,
				ConversationID: fixture.ConversationA,
				AgentID:        fixture.AgentA,
				TaskID: uuid.NullUUID{
					UUID:  fixture.TaskA,
					Valid: true,
				},
				ActorUserID:        fixture.UserA,
				AuthorizedByUserID: fixture.UserA,
				Source:             execution.RunSourceMessage,
				TriggerPrompt:      "Do the work",
			})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, group := range events.GroupByTopic(pending) {
		bus.Publish(ctx, group.Topic, group.Events...)
	}
	runs := execution.NewRunService(execution.RunServiceOptions{
		Queries:             store.Queries(),
		Transactor:          store,
		Bus:                 bus,
		RouteResolver:       conversation.ResolveConversationRoute,
		TaskTerminalEffects: execution.TaskTerminalEffectWriterFunc(ApplyRunTerminalInTx),
	})
	runs.SetRunQueuePromoter(execution.RunQueuePromoterFunc(func(ctx context.Context,
		q *generated.Queries,
		pending *[]events.Event,
		run generated.Run,
		output string) (*uuid.UUID,
		error,
	) {
		result, err := conversation.FinalizeRunConversationInTx(ctx,
			q,
			pending,
			conversation.TaskMessageRunPreparerFuncs{
				Check:    CheckMessageRunInTx,
				Prepare:  PrepareMessageRunInTx,
				NextStep: RunNextStepInTx,
			},
			run,
			output)
		return result.RunID, err
	}))
	if _, err := runs.ClaimRun(ctx, fixture.AgentA, fixture.RuntimeA); err != nil {
		t.Fatal(err)
	}
	if err := runs.ExtendRunPrepareLease(ctx, run.ID, fixture.RuntimeA); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.StartRun(ctx, run.ID, fixture.RuntimeA); err != nil {
		t.Fatal(err)
	}
	receipt, err := runs.AppendTranscriptItem(ctx,
		fixture.RuntimeA,
		run.ID,
		execution.TranscriptItemInput{
			ItemKey: "task-result",
			Role:    protocol.RunItemRoleAgent,
			Parts: []protocol.RunItemPart{{
				Type: protocol.RunItemPartTypeText,
				Text: "Completed the work",
			}},
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Queries().TouchRuntimeHeartbeat(ctx, fixture.RuntimeA); err != nil {
		t.Fatal(err)
	}
	feed := activity.NewFeed(store.Queries())
	readTask := func() []activity.Entry {
		t.Helper()
		rows,
			_, err := feed.List(ctx,
			fixture.CompanyA,
			activity.ListOptions{
				UserID:     fixture.UserA,
				EntityType: "task",
				EntityID:   fixture.TaskA.String(),
				Limit:      20,
			})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	beforeTerminal := readTask()
	if len(beforeTerminal) != 1 || beforeTerminal[0].Action != "task.updated" {
		t.Fatalf("Task history after lease/transcript/heartbeat = %d rows", len(beforeTerminal))
	}
	proof := execution.TerminalTranscriptProof{ThroughSeq: receipt.ThroughSeq, FinalItemKey: receipt.ItemKey}
	// Fail the final Task capture after Run, Conversation and merge writes have staged.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`ALTER TABLE activity_events ADD CONSTRAINT reject_terminal_task_history CHECK (entity_type<>'task' OR revision<>3)`,
	)
	announcedBefore := len(announcements)
	var conversationRevision int64
	if err := pool.QueryRow(ctx,
		`SELECT version FROM conversations WHERE id=$1`,
		fixture.ConversationA).Scan(&conversationRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.CompleteRun(ctx, run.ID, proof, "", false, ""); err == nil {
		t.Fatal("completion succeeded without Task history")
	}
	var runStatus, taskStatus string
	var taskRevision, afterConversationRevision int64
	var mergeJobs int
	if err := pool.QueryRow(
		ctx,
		`SELECT (SELECT status FROM runs WHERE id=$1),(SELECT status FROM tasks WHERE id=$2),(SELECT version FROM tasks WHERE id=$2),(SELECT version FROM conversations WHERE id=$3),(SELECT count(*) FROM task_merge_jobs WHERE run_id=$1)`,
		run.ID,
		fixture.TaskA,
		fixture.ConversationA,
	).Scan(&runStatus,
		&taskStatus,
		&taskRevision,
		&afterConversationRevision,
		&mergeJobs); err != nil {
		t.Fatal(err)
	}
	if runStatus != "running" ||
		taskStatus != "in_progress" ||
		taskRevision != changed.Task.Revision ||
		afterConversationRevision != conversationRevision ||
		mergeJobs != 0 ||
		len(announcements) != announcedBefore ||
		!reflect.DeepEqual(readTask(),
			beforeTerminal) {
		t.Fatalf("terminal rollback = Run %s Task %s/%d Conversation %d merges %d announcements %d",
			runStatus,
			taskStatus,
			taskRevision,
			afterConversationRevision,
			mergeJobs,
			len(announcements)-announcedBefore)
	}
	dbtest.MustExec(t, ctx, pool, `ALTER TABLE activity_events DROP CONSTRAINT reject_terminal_task_history`)
	for range 2 {
		if _, err := runs.CompleteRun(ctx, run.ID, proof, "", false, ""); err != nil {
			t.Fatal(err)
		}
	}
	rows := readTask()
	if len(rows) != 2 ||
		rows[0].Action != "task.updated" ||
		rows[0].Actor.Kind != "system" ||
		rows[0].Revision == nil ||
		*rows[0].Revision != 3 ||
		rows[1].ID != beforeTerminal[0].ID {
		t.Fatalf("business Task history = %d rows", len(rows))
	}
	var details struct {
		Before map[string]any
		After  map[string]any
	}
	if err := json.Unmarshal(rows[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	if details.Before["status"] != "in_progress" || details.After["status"] != "in_review" {
		t.Fatalf("business transition snapshot = %s", rows[0].Details)
	}
	runRows,
		_, err := feed.List(ctx,
		fixture.CompanyA,
		activity.ListOptions{
			UserID:     fixture.UserA,
			EntityType: "run",
			EntityID:   run.ID.String(),
			Limit:      20,
		})
	if err != nil || len(runRows) != 4 {
		t.Fatalf("Run history after failed capture/replay = %d, %v", len(runRows), err)
	}
}
