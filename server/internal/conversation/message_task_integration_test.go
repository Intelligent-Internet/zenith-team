//go:build integration

package conversation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestTaskMessageRunFromTargetScopedRouteReopensFailedTaskAndKeepsReplayExact(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: store,
		Bus:   bus,
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check:    task.CheckMessageRunInTx,
			Prepare:  task.PrepareMessageRunInTx,
			NextStep: task.RunNextStepInTx,
		},
	})
	if err != nil {
		t.Fatalf("construct Conversation service: %v", err)
	}
	// /tasks/:taskId/messages is target-scoped rather than company-scoped, so
	// auth has not populated CompanyID or Role on the principal. Conversation
	// resolves and locks the active membership before crossing into Task.
	principal := auth.UserPrincipal{UserID: fixture.UserA}
	input := conversation.SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Start this Task.",
	}
	target := conversation.MessageTarget{Kind: conversation.TargetTask, ID: fixture.TaskA}

	created, err := service.Send(ctx, principal, target, input)
	if err != nil {
		t.Fatalf("send Task Message: %v", err)
	}
	if !created.Created || created.RunID == nil {
		t.Fatalf("Task Message receipt = %+v", created)
	}
	var status string
	var generation, revision int64
	if err := pool.QueryRow(
		ctx, `
		SELECT status, execution_generation, version
		FROM tasks
		WHERE id = $1`, fixture.TaskA,
	).Scan(&status, &generation, &revision); err != nil {
		t.Fatalf("read Task after Message Run: %v", err)
	}
	if status != "in_progress" || generation != 1 || revision != 2 {
		t.Fatalf(
			"Task after Message Run = status %q, generation %d, revision %d",
			status,
			generation,
			revision,
		)
	}
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status = 'completed' WHERE id = $1`, *created.RunID)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status = 'failed', completed_at = clock_timestamp(), version = version + 1
		WHERE id = $1`, fixture.TaskA)
	managerID := dbtest.NewUUID(t)
	reportingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reporting hierarchy transaction: %v", err)
	}
	defer func() { _ = reportingTx.Rollback(context.Background()) }()
	if _, err := reportingTx.Exec(
		ctx, `
		INSERT INTO agents (
			id, company_id, name, title, model, provider, reports_to_agent_id
		) VALUES ($1, $2, 'Manager', 'CEO', 'test', 'codex', $3)`,
		managerID, fixture.CompanyA, fixture.AgentA,
	); err != nil {
		t.Fatalf("insert reporting manager: %v", err)
	}
	if _, err := reportingTx.Exec(
		ctx, `
		UPDATE agents SET reports_to_agent_id = $1 WHERE id = $2`,
		managerID, fixture.AgentA,
	); err != nil {
		t.Fatalf("assign Task Agent reporting manager: %v", err)
	}
	if _, err := reportingTx.Exec(
		ctx, `
		UPDATE agents SET reports_to_agent_id = NULL WHERE id = $1`, managerID,
	); err != nil {
		t.Fatalf("promote reporting manager to root: %v", err)
	}
	if err := reportingTx.Commit(ctx); err != nil {
		t.Fatalf("commit reporting hierarchy: %v", err)
	}
	var failedRevision int64
	var failedCompletedAt time.Time
	if err := pool.QueryRow(
		ctx, `
		SELECT version, completed_at FROM tasks WHERE id = $1`, fixture.TaskA,
	).Scan(&failedRevision, &failedCompletedAt); err != nil {
		t.Fatalf("read failed Task outcome: %v", err)
	}
	failureReport, err := store.Queries().InsertTaskReportEvent(
		ctx,
		generated.InsertTaskReportEventParams{
			CompanyID: fixture.CompanyA, TaskID: fixture.TaskA,
			TaskVersion: failedRevision, TaskKey: "task_a", TaskTitle: "Task A",
			SourceAgentID: fixture.AgentA, CompletionSource: "agent_business_failure",
			CompletedAt:   pgtype.Timestamptz{Time: failedCompletedAt, Valid: true},
			ResultSummary: "Stripe setup is incomplete.",
			SourceRunID:   uuid.NullUUID{UUID: *created.RunID, Valid: true},
			RequestKey: uuid.NullUUID{
				UUID: dbtest.NewUUID(t), Valid: true,
			},
		},
	)
	if err != nil {
		t.Fatalf("insert failed Task report: %v", err)
	}

	replay, err := service.Send(ctx, principal, target, input)
	if err != nil {
		t.Fatalf("replay terminal Task Message: %v", err)
	}
	if replay.Created || replay.RunID == nil || *replay.RunID != *created.RunID {
		t.Fatalf("terminal Task Message replay = %+v", replay)
	}
	continued, err := service.Send(ctx, principal, target, conversation.SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Stripe is ready; continue payment work.",
	})
	if err != nil {
		t.Fatalf("continue failed Task Message: %v", err)
	}
	if !continued.Created || continued.RunID == nil || *continued.RunID == *created.RunID {
		t.Fatalf("continued failed Task Message receipt = %+v", continued)
	}
	var finalStatus string
	var finalGeneration, finalRevision int64
	if err := pool.QueryRow(
		ctx, `
		SELECT status, execution_generation, version
		FROM tasks
		WHERE id = $1`, fixture.TaskA,
	).Scan(&finalStatus, &finalGeneration, &finalRevision); err != nil {
		t.Fatalf("read resumed failed Task: %v", err)
	}
	if finalStatus != "in_progress" || finalGeneration != generation+1 ||
		finalRevision != failedRevision+1 {
		t.Fatalf(
			"resumed failed Task = status %q, generation %d, revision %d",
			finalStatus,
			finalGeneration,
			finalRevision,
		)
	}
	var failureReportSuperseded bool
	if err := pool.QueryRow(
		ctx, `
		SELECT superseded_at IS NOT NULL
		FROM task_report_events
		WHERE id = $1`, failureReport.ID,
	).Scan(&failureReportSuperseded); err != nil {
		t.Fatalf("read resumed Task failure report: %v", err)
	}
	if !failureReportSuperseded {
		t.Fatal("resumed Task left its failure report pending")
	}
}

func TestBlockedTaskMessageRunWaitsForResolvedDependencies(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE companies SET next_task_number = 2 WHERE id = $1`, fixture.CompanyA)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	taskService, err := task.NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatalf("construct Task service: %v", err)
	}
	service, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: store,
		Bus:   bus,
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check:    task.CheckMessageRunInTx,
			Prepare:  task.PrepareMessageRunInTx,
			NextStep: task.RunNextStepInTx,
		},
	})
	if err != nil {
		t.Fatalf("construct Conversation service: %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	prerequisite, err := taskService.Create(ctx, principal, task.CreateInput{
		ProjectID: fixture.ProjectA,
		Key:       "message_prerequisite",

		MilestoneType: task.MilestoneTypeProduct,
		Title:         "Message prerequisite",
		Status:        task.StatusTodo,
		Priority:      task.PriorityMedium,
		Assignee:      task.ActorRef{Kind: task.ActorUser, ID: fixture.UserA},
	})
	if err != nil {
		t.Fatalf("create prerequisite Task: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status = 'blocked' WHERE id = $1`, fixture.TaskA)
	if _, err := store.Queries().InsertTaskDependency(
		ctx,
		generated.InsertTaskDependencyParams{
			CompanyID:          fixture.CompanyA,
			DependentTaskID:    fixture.TaskA,
			PrerequisiteTaskID: prerequisite.Task.ID,
			CreatedByUserID:    uuid.NullUUID{UUID: fixture.UserA, Valid: true},
		},
	); err != nil {
		t.Fatalf("insert unresolved Task dependency: %v", err)
	}
	target := conversation.MessageTarget{Kind: conversation.TargetTask, ID: fixture.TaskA}
	if _, err := service.Send(ctx, principal, target, conversation.SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Bypass the prerequisite.",
	}); !errors.Is(err, conversation.ErrConflict) {
		t.Fatalf("blocked Task Message error = %v, want %v", err, conversation.ErrConflict)
	}
	done := task.StatusDone
	if _, err := taskService.Update(ctx, principal, prerequisite.Task.ID, task.UpdateInput{
		ExpectedRevision: prerequisite.Task.Revision,
		Status:           &done,
	}); err != nil {
		t.Fatalf("complete prerequisite Task: %v", err)
	}
	created, err := service.Send(ctx, principal, target, conversation.SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Continue after the prerequisite.",
	})
	if err != nil {
		t.Fatalf("send resolved blocked Task Message: %v", err)
	}
	if created.RunID == nil {
		t.Fatalf("resolved blocked Task Message receipt = %+v", created)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM tasks WHERE id = $1`, fixture.TaskA).
		Scan(&status); err != nil {
		t.Fatalf("read resumed Task: %v", err)
	}
	if status != "in_progress" {
		t.Fatalf("resumed Task status = %q, want in_progress", status)
	}
}

// Every Run a Task chat starts is its assignee's, so it needs their
// project.tasks.execute on the Task's Project (ADR 0017), exactly like a
// start from the Task: an assignee who may read and write there but not run
// Tasks is refused, and neither the message nor a Run is written. So is a
// colleague's message, which would only wait for that assignee.
func TestTaskMessageRunNeedsTheAssigneesExecutePermission(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: store,
		Bus:   bus,
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check:    task.CheckMessageRunInTx,
			Prepare:  task.PrepareMessageRunInTx,
			NextStep: task.RunNextStepInTx,
		},
	})
	if err != nil {
		t.Fatalf("construct Conversation service: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM project_grants
		WHERE project_id = $1 AND user_id = $2
		  AND permission NOT IN ('project.read', 'project.tasks.write')`,
		fixture.ProjectA, fixture.UserA)
	count := func(statement string) int {
		t.Helper()
		var value int
		if err := pool.QueryRow(ctx, statement, fixture.TaskA).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	const countRuns = `SELECT count(*) FROM runs WHERE task_id = $1`
	const countItems = `SELECT count(*) FROM conversation_items
		WHERE conversation_id = (SELECT conversation_id FROM tasks WHERE id = $1)`
	runsBefore, itemsBefore := count(countRuns), count(countItems)

	_, err = service.Send(
		ctx,
		auth.UserPrincipal{UserID: fixture.UserA},
		conversation.MessageTarget{Kind: conversation.TargetTask, ID: fixture.TaskA},
		conversation.SendInput{RequestKey: dbtest.NewUUID(t), Content: "Start this Task."},
	)
	var refusal *execution.ExecutePermissionError
	if !errors.As(err, &refusal) || !errors.Is(err, auth.ErrProjectPermissionRequired) ||
		refusal.AssigneeID != fixture.UserA || refusal.AgentName == "" {
		t.Fatalf("Send() error = %v (%+v), want the execute refusal for the assignee", err, refusal)
	}
	if runs, items := count(countRuns), count(countItems); runs != runsBefore || items != itemsBefore {
		t.Fatalf("a refused send wrote %d Run(s) and %d item(s)", runs-runsBefore, items-itemsBefore)
	}

	// A colleague who may write there is refused the same way: their
	// message would only wait for a Run the assignee cannot start, so it is
	// neither saved nor queued. The Task chat gives everyone this reason.
	colleague := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
		colleague, "colleague@example.com")
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA, colleague)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, colleague, "project.tasks.write")
	const countQueued = `SELECT count(*) FROM conversation_run_queue_items
		WHERE conversation_id = (SELECT conversation_id FROM tasks WHERE id = $1)`
	queuedBefore := count(countQueued)

	_, err = service.Send(
		ctx,
		auth.UserPrincipal{UserID: colleague, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember},
		conversation.MessageTarget{Kind: conversation.TargetTask, ID: fixture.TaskA},
		conversation.SendInput{RequestKey: dbtest.NewUUID(t), Content: "Please also check the docs."},
	)
	refusal = nil
	if !errors.As(err, &refusal) || !errors.Is(err, auth.ErrProjectPermissionRequired) ||
		refusal.AssigneeID != fixture.UserA {
		t.Fatalf("Send(colleague) error = %v (%+v), want the execute refusal for the assignee", err, refusal)
	}
	if runs, items, queued := count(countRuns), count(countItems), count(countQueued); runs != runsBefore ||
		items != itemsBefore || queued != queuedBefore {
		t.Fatalf("a colleague's refused send wrote %d Run(s), %d item(s) and %d queued message(s)",
			runs-runsBefore, items-itemsBefore, queued-queuedBefore)
	}
}

// A Task chat follows its Project (ADR 0017): someone who cannot read the
// Project cannot find the chat, a reader sees its messages but may not post
// without project.tasks.write, and a reader who may write still has their
// message saved for the assignee (ADR 0014).
func TestTaskChatReadsFollowTheProjectAndPostingNeedsWrite(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: store,
		Bus:   bus,
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check:    task.CheckMessageRunInTx,
			Prepare:  task.PrepareMessageRunInTx,
			NextStep: task.RunNextStepInTx,
		},
	})
	if err != nil {
		t.Fatalf("construct Conversation service: %v", err)
	}
	member := func(email string) auth.UserPrincipal {
		t.Helper()
		id := dbtest.NewUUID(t)
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
			id,
			email,
		)
		dbtest.MustExec(t, ctx, pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
			fixture.CompanyA, id)
		return auth.UserPrincipal{UserID: id, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	}
	outsider, reader, writer := member(
		"outsider@example.com",
	), member(
		"reader@example.com",
	), member(
		"writer@example.com",
	)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, reader.UserID, "project.read")
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, writer.UserID, "project.tasks.write")

	chat := conversation.MessageTarget{Kind: conversation.TargetConversation, ID: fixture.ConversationA}
	byTask := conversation.MessageTarget{Kind: conversation.TargetTask, ID: fixture.TaskA}
	for _, target := range []conversation.MessageTarget{chat, byTask} {
		if _, err := service.ListMessages(
			ctx,
			outsider,
			target,
			conversation.MessageListOptions{Limit: 10},
		); !errors.Is(
			err,
			conversation.ErrNotFound,
		) {
			t.Errorf("ListMessages(outsider, %s) error = %v, want ErrNotFound", target.Kind, err)
		}
		if _, err := service.ListMessages(
			ctx,
			reader,
			target,
			conversation.MessageListOptions{Limit: 10},
		); err != nil {
			t.Errorf("ListMessages(reader, %s) error = %v", target.Kind, err)
		}
	}
	if _, err := service.FetchState(
		ctx,
		outsider,
		fixture.ConversationA,
	); !errors.Is(
		err,
		conversation.ErrNotFound,
	) {
		t.Errorf("FetchState(outsider) error = %v, want ErrNotFound", err)
	}

	count := func() (items, queued int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM conversation_items WHERE conversation_id = $1),
			       (SELECT count(*) FROM conversation_run_queue_items WHERE conversation_id = $1)`,
			fixture.ConversationA).Scan(&items, &queued); err != nil {
			t.Fatal(err)
		}
		return items, queued
	}
	itemsBefore, queuedBefore := count()
	send := func(principal auth.UserPrincipal) error {
		_, err := service.Send(ctx, principal, byTask,
			conversation.SendInput{RequestKey: dbtest.NewUUID(t), Content: "Please also check the docs."})
		return err
	}
	if err := send(outsider); !errors.Is(err, conversation.ErrNotFound) {
		t.Errorf("Send(outsider) error = %v, want ErrNotFound", err)
	}
	if err := send(reader); !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Errorf("Send(reader without project.tasks.write) error = %v, want ErrProjectPermissionRequired", err)
	}
	if items, queued := count(); items != itemsBefore || queued != queuedBefore {
		t.Fatalf(
			"refused sends wrote %d item(s) and %d queued message(s)",
			items-itemsBefore,
			queued-queuedBefore,
		)
	}
	if err := send(writer); err != nil {
		t.Fatalf("Send(writer) error = %v", err)
	}
	if _, queued := count(); queued != queuedBefore+1 {
		t.Fatalf("the writer's message queued %d time(s), want once for the assignee", queued-queuedBefore)
	}

	// Changing the queue is posting too: a reader who may not write sees
	// the writer's queued message but may not delete or reorder it.
	var queuedID uuid.UUID
	var queuedVersion int64
	if err := pool.QueryRow(ctx, `
		SELECT id, version FROM conversation_run_queue_items
		WHERE conversation_id = $1 AND initiated_by_user_id = $2 AND state = 'queued'`,
		fixture.ConversationA, writer.UserID).Scan(&queuedID, &queuedVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteRunQueueItem(
		ctx,
		reader,
		fixture.ConversationA,
		queuedID,
		conversation.DeleteRunQueueItemInput{
			ExpectedRevision: queuedVersion,
		},
	); !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Errorf("DeleteRunQueueItem(reader) error = %v, want ErrProjectPermissionRequired", err)
	}
	if _, err := service.ReorderRunQueue(ctx, reader, fixture.ConversationA, conversation.RunQueueOrderInput{
		Items: []conversation.RunQueueOrderItem{{ID: queuedID, ExpectedRevision: queuedVersion}},
	}); !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Errorf("ReorderRunQueue(reader) error = %v, want ErrProjectPermissionRequired", err)
	}
	if _, err := service.DeleteRunQueueItem(
		ctx,
		outsider,
		fixture.ConversationA,
		queuedID,
		conversation.DeleteRunQueueItemInput{
			ExpectedRevision: queuedVersion,
		},
	); !errors.Is(err, conversation.ErrNotFound) {
		t.Errorf("DeleteRunQueueItem(outsider) error = %v, want ErrNotFound", err)
	}

	// The writer then stops reading the Project by a path that leaves the
	// queued message behind. The assignee's next message must still start a
	// Run: the message is skipped, never written under a non-reader's name,
	// and never blocks the Task.
	dbtest.MustExec(t, ctx, pool, `DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		fixture.ProjectA, writer.UserID)
	sent, err := service.Send(ctx, auth.UserPrincipal{UserID: fixture.UserA}, byTask,
		conversation.SendInput{RequestKey: dbtest.NewUUID(t), Content: "Start with the docs."})
	if err != nil || sent.RunID == nil {
		t.Fatalf("Send(assignee) after the colleague lost the Project = %+v, %v; want a started Run", sent, err)
	}
	var carried int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM conversation_items
		WHERE conversation_id = $1 AND author_id = $2`,
		fixture.ConversationA, writer.UserID).Scan(&carried); err != nil {
		t.Fatal(err)
	}
	if carried != 0 {
		t.Fatalf("%d message(s) written under the name of someone who no longer reads the chat", carried)
	}
}
