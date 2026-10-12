//go:build integration

package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestReportServiceDispatchesExactProactiveTargetAfterDeferredRunIsIdle(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE companies
		SET proactive_mode_enabled=true,
		    proactive_mode_enabled_by_user_id=$2,
		    proactive_mode_conversation_id=$3
		WHERE id=$1`, fixture.CompanyA, fixture.UserA, fixture.DirectConversationA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_report_events (
			company_id, task_id, task_version, task_key, task_title,
			source_agent_id, recipient_agent_id, completion_source,
			completed_at, result_summary
		) VALUES ($1,$2,1,'task_a','Task A',$3,$3,'manual',clock_timestamp(),'Done')`,
		fixture.CompanyA, fixture.TaskA, fixture.AgentA)
	var deferredRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, conversation_id, agent_id, runtime_id, status, fire_at
		, run_type) VALUES ($1,$2,$3,$4,'deferred',clock_timestamp() + interval '1 hour', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $2 ))
		RETURNING id`,
		fixture.CompanyA, fixture.DirectConversationA, fixture.AgentA, fixture.RuntimeA,
	).Scan(&deferredRunID); err != nil {
		t.Fatalf("insert deferred root-Agent Run: %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service, err := NewReportService(store, bus)
	if err != nil {
		t.Fatalf("construct Task report service: %v", err)
	}

	delivered, err := service.DispatchProactiveOnce(ctx)
	if err != nil || delivered {
		t.Fatalf("DispatchProactiveOnce(deferred busy) = %t, %v; want false, nil", delivered, err)
	}
	if got := countTaskReportRows(t, ctx, pool, `SELECT count(*) FROM task_report_batches`); got != 0 {
		t.Fatalf("proactive batches while deferred = %d, want 0", got)
	}
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status='completed', fire_at=NULL, updated_at=clock_timestamp()
		WHERE id=$1`, deferredRunID)

	delivered, err = service.DispatchProactiveOnce(ctx)
	if err != nil || !delivered {
		t.Fatalf("DispatchProactiveOnce(idle) = %t, %v; want true, nil", delivered, err)
	}
	var automationUserID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		SELECT automation_user_id
		FROM task_report_batches
		WHERE company_id=$1 AND delivery_type='proactive'`, fixture.CompanyA,
	).Scan(&automationUserID); err != nil {
		t.Fatalf("read proactive Task report batch: %v", err)
	}
	if automationUserID != fixture.UserA {
		t.Fatalf("proactive batch automation user = %s, want %s", automationUserID, fixture.UserA)
	}
	delivered, err = service.DispatchProactiveOnce(ctx)
	if err != nil || delivered {
		t.Fatalf("DispatchProactiveOnce(drained) = %t, %v; want false, nil", delivered, err)
	}
}

func TestReportServiceSendFencesFreshMembershipBeforeConversation(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_report_events (
			company_id, task_id, task_version, task_key, task_title,
			source_agent_id, recipient_agent_id, completion_source,
			completed_at, result_summary
		) VALUES ($1,$2,1,'task_a','Task A',$3,$3,'manual',clock_timestamp(),'Done')`,
		fixture.CompanyA, fixture.TaskA, fixture.AgentA)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service, err := NewReportService(store, bus)
	if err != nil {
		t.Fatalf("construct Task report service: %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}

	var runsBefore int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&runsBefore); err != nil {
		t.Fatalf("count initial Runs: %v", err)
	}
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Company lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	var lockedCompanyID uuid.UUID
	if err := holder.QueryRow(ctx, `
		SELECT id FROM companies WHERE id=$1 FOR UPDATE`, fixture.CompanyA).
		Scan(&lockedCompanyID); err != nil {
		t.Fatalf("lock Company: %v", err)
	}
	if lockedCompanyID != fixture.CompanyA {
		t.Fatalf("locked Company = %s, want %s", lockedCompanyID, fixture.CompanyA)
	}
	if _, err := holder.Exec(ctx, `
		UPDATE company_members
		SET status='revoked', revoked_at=clock_timestamp(), revoked_by_user_id=$2,
		    updated_at=clock_timestamp(), version=version+1
		WHERE company_id=$1 AND user_id=$2`, fixture.CompanyA, fixture.UserA); err != nil {
		t.Fatalf("stage membership revocation: %v", err)
	}

	type sendResult struct {
		result SendReportResult
		err    error
	}
	requestKey := dbtest.NewUUID(t)
	done := make(chan sendResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result, sendErr := service.Send(ctx, principal, fixture.DirectConversationA, SendReportInput{
			RequestKey: requestKey,
		})
		done <- sendResult{result: result, err: sendErr}
	}()
	waitForReportSendLock(t, ctx, pool, finished)

	checker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Conversation lock checker: %v", err)
	}
	var conversationID uuid.UUID
	checkErr := checker.QueryRow(
		ctx, `
		SELECT id FROM conversations WHERE id=$1 FOR UPDATE NOWAIT`,
		fixture.DirectConversationA,
	).Scan(&conversationID)
	_ = checker.Rollback(context.Background())
	if checkErr != nil {
		t.Fatalf("Task report Send locked Conversation before fresh Company membership: %v", checkErr)
	}
	if conversationID != fixture.DirectConversationA {
		t.Fatalf("checked Conversation = %s, want %s", conversationID, fixture.DirectConversationA)
	}

	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("commit membership revocation: %v", err)
	}
	select {
	case sent := <-done:
		if !errors.Is(sent.err, ErrNotFound) {
			t.Fatalf("Send() = %+v, %v; want ErrNotFound", sent.result, sent.err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for Send(): %v", ctx.Err())
	}
	var runsAfter int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&runsAfter); err != nil {
		t.Fatalf("count final Runs: %v", err)
	}
	if runsAfter != runsBefore {
		t.Fatalf("Runs after revoked Send = %d, want %d", runsAfter, runsBefore)
	}
	if got := countTaskReportRows(t, ctx, pool, `SELECT count(*) FROM task_report_batches`); got != 0 {
		t.Fatalf("Task report batches after revoked Send = %d, want 0", got)
	}
}

func waitForReportSendLock(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	finished <-chan struct{},
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case <-finished:
			t.Fatal("Task report Send bypassed the in-flight membership revocation")
		default:
		}
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE datname=current_database()
				  AND pid <> pg_backend_pid()
				  AND state='active'
				  AND wait_event_type='Lock'
				  AND query LIKE '%LockCompanyForMembershipRead%'
			)`).Scan(&waiting); err != nil {
			t.Fatalf("observe Task report Send lock wait: %v", err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Task report Send did not reach the Company membership fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReportServiceSendSerializesRecipientAgentIdleGate(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_report_events (
			company_id, task_id, task_version, task_key, task_title,
			source_agent_id, recipient_agent_id, completion_source,
			completed_at, result_summary
		) VALUES ($1,$2,1,'task_a','Task A',$3,$3,'manual',clock_timestamp(),'Done')`,
		fixture.CompanyA, fixture.TaskA, fixture.AgentA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	otherConversationID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (
			id, company_id, kind, agent_id, created_by_user_id, title
		) VALUES ($1, $2, 'direct', $3, $4, 'Competing direct chat')`,
		otherConversationID, fixture.CompanyA, fixture.AgentA, fixture.UserB)

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service, err := NewReportService(store, bus)
	if err != nil {
		t.Fatalf("construct Task report service: %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}

	// Model the commit point of an ordinary Message enqueue in another direct
	// Conversation. The Run insert takes the Agent FK's KEY SHARE lock, but the
	// row remains invisible until this transaction commits.
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin competing enqueue: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	var lockedConversationID uuid.UUID
	if err := holder.QueryRow(ctx, `
		SELECT id FROM conversations WHERE id=$1 FOR UPDATE`, otherConversationID).
		Scan(&lockedConversationID); err != nil {
		t.Fatalf("lock competing Conversation: %v", err)
	}
	if lockedConversationID != otherConversationID {
		t.Fatalf("locked competing Conversation = %s, want %s", lockedConversationID, otherConversationID)
	}
	var competingRunID uuid.UUID
	if err := holder.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, conversation_id, agent_id, runtime_id, status
		, run_type) VALUES ($1, $2, $3, $4, 'queued', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $2 ))
		RETURNING id`,
		fixture.CompanyA, otherConversationID, fixture.AgentA, fixture.RuntimeA,
	).Scan(&competingRunID); err != nil {
		t.Fatalf("insert competing Run: %v", err)
	}

	type sendResult struct {
		result SendReportResult
		err    error
	}
	requestKey := dbtest.NewUUID(t)
	done := make(chan sendResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result, sendErr := service.Send(
			ctx,
			principal,
			fixture.DirectConversationA,
			SendReportInput{RequestKey: requestKey},
		)
		done <- sendResult{result: result, err: sendErr}
	}()
	waitForTaskReportRecipientAgentLock(t, ctx, pool, finished)

	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("commit competing enqueue: %v", err)
	}
	select {
	case sent := <-done:
		if !errors.Is(sent.err, ErrAgentBusy) {
			t.Fatalf("Send() = %+v, %v; want ErrAgentBusy", sent.result, sent.err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for Send(): %v", ctx.Err())
	}
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT count(*) FROM task_report_batches`); got != 0 {
		t.Fatalf("Task report batches after competing enqueue = %d, want 0", got)
	}
	if got := countTaskReportRows(
		t, ctx, pool, `
		SELECT count(*) FROM runs
		WHERE agent_id=$1
		  AND status IN ('queued','dispatched','waiting_local_directory','running')`,
		fixture.AgentA,
	); got != 1 {
		t.Fatalf("active recipient Agent Runs = %d, want only competing Run %d", got, competingRunID)
	}
}

func waitForTaskReportRecipientAgentLock(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	finished <-chan struct{},
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case <-finished:
			t.Fatal("Task report Send bypassed the recipient Agent serialization fence")
		default:
		}
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE datname=current_database()
				  AND pid <> pg_backend_pid()
				  AND state='active'
				  AND wait_event_type='Lock'
				  AND query LIKE '%LockTaskReportRecipientAgent%'
			)`).Scan(&waiting); err != nil {
			t.Fatalf("observe Task report recipient Agent wait: %v", err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Task report Send did not reach the recipient Agent fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReportServiceQueuesDoneTaskSendsIdleGatedBatchAndSupersedesReopenedFailure(t *testing.T) {
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
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	engineeringAgentID := dbtest.NewUUID(t)
	otherCEOConversationID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
				INSERT INTO agents (
			id, company_id, name, title, reports_to_agent_id,
			model, provider
		) VALUES ($1, $2, 'Engineering Agent', 'Engineering Lead', $3,
		          'test', 'codex')`,
		engineeringAgentID, fixture.CompanyA, fixture.AgentA)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, engineeringAgentID,
		fixture.UserA, fixture.RuntimeA, "codex")
	dbtest.MustExec(t, ctx, pool, `
				UPDATE tasks
		SET executed_by_agent_id = $2,
		    status = 'todo',
		    completed_at = NULL
		WHERE id = $1`, fixture.TaskA, engineeringAgentID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (
			id, company_id, kind, agent_id, created_by_user_id, title
		) VALUES ($1, $2, 'direct', $3, $4, 'Other CEO chat')`,
		otherCEOConversationID, fixture.CompanyA, fixture.AgentA, fixture.UserB)

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	var taskEvents []events.Event
	if err := bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		taskEvents = append(taskEvents, event)
		return nil
	}); err != nil {
		t.Fatalf("subscribe to Task events: %v", err)
	}
	var runtimeEvents []events.Event
	if err := bus.Subscribe(execution.TopicRuntime, func(_ context.Context, event events.Event) error {
		runtimeEvents = append(runtimeEvents, event)
		return nil
	}); err != nil {
		t.Fatalf("subscribe to Runtime events: %v", err)
	}
	taskService, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatalf("construct Task service: %v", err)
	}

	inReview := StatusInReview
	reviewable, err := taskService.Update(ctx, principal, fixture.TaskA, UpdateInput{
		ExpectedRevision: 1,
		Status:           &inReview,
	})
	if err != nil || reviewable.Task.Status != StatusInReview {
		t.Fatalf("Update(in review) = %+v, %v", reviewable, err)
	}
	done := StatusDone
	updated, err := taskService.Update(ctx, principal, fixture.TaskA, UpdateInput{
		ExpectedRevision: reviewable.Task.Revision,
		Status:           &done,
	})
	if err != nil || updated.Task.Status != StatusDone {
		t.Fatalf("Update(done) = %+v, %v", updated, err)
	}
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT count(*) FROM task_report_events
		WHERE task_id=$1 AND completion_source='review_accepted'`, fixture.TaskA); got != 1 {
		t.Fatalf("review-accepted Task report events = %d, want 1", got)
	}
	// The report names its Task's Project (ADR 0017).
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT count(*) FROM task_report_events
		WHERE task_id=$1 AND project_id=$2`, fixture.TaskA, fixture.ProjectA); got != 1 {
		t.Fatalf("Task report events naming the Project = %d, want 1", got)
	}
	if got := countTaskEvents(taskEvents, fixture.CompanyA, EventReportReady); got != 1 {
		t.Fatalf("Task report ready events = %d, want 1", got)
	}

	reopened := StatusInProgress
	updated, err = taskService.Update(ctx, principal, fixture.TaskA, UpdateInput{
		ExpectedRevision: updated.Task.Revision,
		Status:           &reopened,
	})
	if err != nil || updated.Task.Status != StatusInProgress {
		t.Fatalf("Update(reopen) = %+v, %v", updated, err)
	}
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT count(*) FROM task_report_events
		WHERE task_id=$1 AND batch_id IS NULL AND superseded_at IS NULL`, fixture.TaskA); got != 0 {
		t.Fatalf("pending Task report events after reopen = %d, want 0", got)
	}
	if got := countTaskEvents(taskEvents, fixture.CompanyA, EventReportChanged); got != 1 {
		t.Fatalf("Task report changed events after reopen = %d, want 1", got)
	}

	doneAgain := StatusDone
	updated, err = taskService.Update(ctx, principal, fixture.TaskA, UpdateInput{
		ExpectedRevision: updated.Task.Revision,
		Status:           &doneAgain,
	})
	if err != nil || updated.Task.Status != StatusDone {
		t.Fatalf("Update(done again) = %+v, %v", updated, err)
	}

	reportService, err := NewReportService(store, bus)
	if err != nil {
		t.Fatalf("construct Task report service: %v", err)
	}
	preview, err := reportService.Preview(ctx, principal, fixture.DirectConversationA)
	if err != nil || preview.PendingCount != 1 || preview.AgentBusy ||
		preview.RecipientAgentID != fixture.AgentA || len(preview.Events) != 1 ||
		preview.Events[0].TaskID != fixture.TaskA || preview.Events[0].Outcome != StatusDone {
		t.Fatalf("Preview() = %+v, %v", preview, err)
	}

	var busyRunID uuid.UUID
	err = pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, conversation_id, agent_id, runtime_id, status
		, run_type) VALUES ($1, $2, $3, $4, 'running', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $2 ))
		RETURNING id`,
		fixture.CompanyA, otherCEOConversationID, fixture.AgentA, fixture.RuntimeA,
	).Scan(&busyRunID)
	if err != nil {
		t.Fatalf("insert busy CEO Run: %v", err)
	}
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT count(*) FROM runs
		WHERE conversation_id=$1 AND agent_id=$2
		  AND status IN ('queued','dispatched','waiting_local_directory','running')`,
		fixture.DirectConversationA, fixture.AgentA); got != 0 {
		t.Fatalf("active CEO Runs in target Conversation = %d, want 0", got)
	}
	busyPreview, err := reportService.Preview(ctx, principal, fixture.DirectConversationA)
	if err != nil || busyPreview.PendingCount != 1 || !busyPreview.AgentBusy || len(busyPreview.Events) != 1 {
		t.Fatalf("Preview(CEO busy in another Conversation) = %+v, %v", busyPreview, err)
	}
	input := SendReportInput{RequestKey: dbtest.NewUUID(t)}
	if _, err := reportService.Send(
		ctx, principal, fixture.DirectConversationA, input,
	); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("Send(busy) error = %v, want %v", err, ErrAgentBusy)
	}
	if got := countTaskReportRows(t, ctx, pool,
		`SELECT count(*) FROM task_report_events WHERE batch_id IS NULL AND superseded_at IS NULL`); got != 1 {
		t.Fatalf("pending Task report events after busy send = %d, want 1", got)
	}
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status='completed', updated_at=clock_timestamp() WHERE id=$1`, busyRunID)

	sent, err := reportService.Send(ctx, principal, fixture.DirectConversationA, input)
	if err != nil || !sent.Created || sent.BatchID == uuid.Nil || sent.MessageID == uuid.Nil ||
		sent.RunID == uuid.Nil || sent.EventCount != 1 {
		t.Fatalf("Send() = %+v, %v", sent, err)
	}
	if len(runtimeEvents) != 1 || runtimeEvents[0].RunID != sent.RunID ||
		runtimeEvents[0].Name != execution.EventRuntimeWorkAvailable {
		t.Fatalf("Runtime work publications = %+v, want Run %d", runtimeEvents, sent.RunID)
	}
	if got := countTaskEvents(taskEvents, fixture.CompanyA, EventReportChanged); got != 2 {
		t.Fatalf("Task report changed events after send = %d, want 2", got)
	}
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT count(*)
		FROM task_report_batches AS batch
		JOIN conversation_items AS item
		  ON item.id = batch.message_id
		 AND item.run_id = batch.run_id
		JOIN runs ON runs.id = batch.run_id
		WHERE batch.id = $1
		  AND item.type = 'user_message'
		  AND item.author_kind = 'user'
		  AND runs.status = 'queued'`, sent.BatchID); got != 1 {
		t.Fatalf("committed Task report batch graph = %d, want 1", got)
	}

	replayed, err := reportService.Send(ctx, principal, fixture.DirectConversationA, input)
	if err != nil || replayed.Created || replayed.BatchID != sent.BatchID || replayed.RunID != sent.RunID {
		t.Fatalf("Send(replay) = %+v, %v", replayed, err)
	}
	if got := countTaskReportRows(t, ctx, pool,
		`SELECT count(*) FROM task_report_batches WHERE company_id=$1`, fixture.CompanyA); got != 1 {
		t.Fatalf("Task report batches after replay = %d, want 1", got)
	}
	remaining, err := reportService.Preview(ctx, principal, fixture.DirectConversationA)
	if err != nil || remaining.PendingCount != 0 {
		t.Fatalf("Preview(after send) = %+v, %v", remaining, err)
	}

	var failedRevision int64
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status='failed', completed_at=clock_timestamp(),
		    updated_at=clock_timestamp(), version=version+1
		WHERE id=$1`, fixture.TaskA)
	if err := pool.QueryRow(ctx, `
		SELECT version FROM tasks WHERE id=$1`, fixture.TaskA).Scan(&failedRevision); err != nil {
		t.Fatalf("read failed Task revision: %v", err)
	}
	var failureRunID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id, status
		, run_type) VALUES ($1, $2, $3, $4, $5, 'completed', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`, fixture.CompanyA, engineeringAgentID, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA).Scan(&failureRunID)
	if err != nil {
		t.Fatalf("insert failed Task source Run: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_report_events (
			company_id, task_id, task_version, task_key, task_title,
			source_agent_id, recipient_agent_id, completion_source,
			completed_at, result_summary, source_run_id, request_key
		) VALUES ($1,$2,$3,'task_a','Task A',$4,$5,
		          'agent_business_failure',clock_timestamp(),
		          'Policy forbids delivery',$6,$7)`,
		fixture.CompanyA, fixture.TaskA, failedRevision, engineeringAgentID,
		fixture.AgentA, failureRunID, dbtest.NewUUID(t))
	todo := StatusTodo
	reopenedFailure, err := taskService.Update(ctx, principal, fixture.TaskA, UpdateInput{
		ExpectedRevision: failedRevision,
		Status:           &todo,
	})
	if err != nil || reopenedFailure.Task.Status != StatusTodo {
		t.Fatalf("Update(reopen failed) = %+v, %v", reopenedFailure, err)
	}
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT count(*) FROM task_report_events
		WHERE task_id=$1 AND batch_id IS NULL AND superseded_at IS NULL`, fixture.TaskA); got != 0 {
		t.Fatalf("pending failed Task reports after reopen = %d, want 0", got)
	}
	if got := countTaskEvents(taskEvents, fixture.CompanyA, EventReportChanged); got != 3 {
		t.Fatalf("Task report changed events after failed reopen = %d, want 3", got)
	}
}

func countTaskReportRows(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	query string,
	args ...any,
) int64 {
	t.Helper()
	var count int64
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count Task report rows: %v", err)
	}
	return count
}

func countTaskEvents(recorded []events.Event, companyID uuid.UUID, name string) int {
	count := 0
	for _, event := range recorded {
		if event.CompanyID == companyID && event.Name == name {
			count++
		}
	}
	return count
}
