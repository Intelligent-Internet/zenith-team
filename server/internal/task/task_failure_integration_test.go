//go:build integration

package task

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	agentdomain "github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestMarkFailedFromRunIsGenerationFencedAndExactlyReplayable(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	managerID := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(t, ctx, pool, managerID, fixture.CompanyA,
		"Manager", "codex", &fixture.AgentA, fixture.UserA, fixture.RuntimeA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE agents SET reports_to_agent_id=$1 WHERE id=$2`, managerID, fixture.AgentA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE agents SET reports_to_agent_id=NULL WHERE id=$1`, managerID)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status='in_progress', completed_at=NULL,
		    execution_generation=7, version=3, updated_at=clock_timestamp()
		WHERE id=$1`, fixture.TaskA)

	var runID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, task_execution_generation
		, run_type) VALUES ($1,$2,$3,$4,$5,'running',7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA,
	).Scan(&runID); err != nil {
		t.Fatalf("seed running Task Run: %v", err)
	}

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("events.NewBus() error = %v", err)
	}
	var failureTaskUpdated, memberTaskUpdated, reportReady int
	if err := bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		switch event.Name {
		case EventUpdated:
			switch event.RunID {
			case runID:
				failureTaskUpdated++
				if event.Actor != events.AgentActor(fixture.AgentA.String()) {
					t.Fatalf(
						"Run-proven task.updated actor = %+v, want Agent %s",
						event.Actor,
						fixture.AgentA,
					)
				}
				if event.TaskID != fixture.TaskA || event.ConversationID != fixture.ConversationA {
					t.Fatalf("Run-proven task.updated envelope = %+v", event)
				}
			case uuid.Nil:
				memberTaskUpdated++
				if event.Actor != events.UserActor(fixture.UserA.String()) {
					t.Fatalf(
						"member task.updated actor = %+v, want User %s",
						event.Actor,
						fixture.UserA,
					)
				}
			default:
				t.Fatalf("task.updated carried unexpected Run id: %+v", event)
			}
		case EventReportReady:
			reportReady++
			if event.Actor != events.AgentActor(fixture.AgentA.String()) {
				t.Fatalf("task.report_ready actor = %+v, want Agent %s", event.Actor, fixture.AgentA)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe to Task events: %v", err)
	}
	service, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewTaskService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA,
		SessionID: dbtest.NewUUID(t), AuthKind: auth.TokenKindBearer,
		Role: auth.CompanyRoleOwner,
	}
	taskID := fixture.TaskA
	origin := execution.RunOrigin{
		RunID: runID, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		TaskID: &taskID, ConversationID: fixture.ConversationA,
		ConversationKind: "task", OriginUserID: &fixture.UserA,
	}
	requestKey := dbtest.NewUUID(t)
	input := FailureInput{RequestKey: requestKey, Reason: "Supplier policy forbids delivery"}

	beforeFailure, err := service.Get(ctx, principal, fixture.TaskA, nil)
	if err != nil {
		t.Fatal(err)
	}
	history := func() []activity.Entry {
		t.Helper()
		rows,
			_, err := activity.NewFeed(store.Queries()).List(ctx,
			fixture.CompanyA,
			activity.ListOptions{
				UserID:     fixture.UserA,
				EntityType: "task",
				EntityID:   fixture.TaskA.String(),
				Limit:      10,
			})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	foreignOrigin := origin
	foreignOrigin.AgentID = fixture.AgentB
	if _, err := service.MarkFailed(ctx, principal, foreignOrigin, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign Run actor proof = %v", err)
	}
	dbtest.MustExec(t,
		ctx,
		pool,
		`ALTER TABLE activity_events ADD CONSTRAINT reject_failure_history CHECK (action <> 'task.updated')`)
	if _, err := service.MarkFailed(ctx, principal, origin, input); err == nil {
		t.Fatal("failure committed without history")
	}
	rolledBack, err := service.Get(ctx, principal, fixture.TaskA, nil)
	if err != nil || !reflect.DeepEqual(rolledBack, beforeFailure) {
		t.Fatalf("rolled-back Task = %+v, %v", rolledBack, err)
	}
	var reportCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM task_report_events WHERE task_id=$1`,
		fixture.TaskA).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if reportCount != 0 || len(history()) != 0 || failureTaskUpdated != 0 || reportReady != 0 {
		t.Fatalf("rolled-back reports/history/announcements = %d/%d/%d/%d",
			reportCount,
			len(history()),
			failureTaskUpdated,
			reportReady)
	}
	dbtest.MustExec(t, ctx, pool, `ALTER TABLE activity_events DROP CONSTRAINT reject_failure_history`)
	failed, err := service.MarkFailed(ctx, principal, origin, input)
	if err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}
	if failed.ID != fixture.TaskA || failed.Status != StatusFailed ||
		failed.CompletedAt == nil || failed.Revision != 4 {
		t.Fatalf("MarkFailed() = %+v", failed)
	}
	recorded := history()
	if len(recorded) != 1 ||
		recorded[0].Revision == nil ||
		*recorded[0].Revision != failed.Revision ||
		recorded[0].Actor.Kind != "agent" ||
		recorded[0].Actor.ID == nil ||
		*recorded[0].Actor.ID != fixture.AgentA.String() ||
		recorded[0].Entity.ID != fixture.TaskA.String() {
		t.Fatalf("verified Agent Activity = %+v", recorded)
	}
	var details struct {
		Before json.RawMessage
		After  json.RawMessage
	}
	if err := json.Unmarshal(recorded[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	assertTaskHistoryState(t, details.Before, beforeFailure)
	assertTaskHistoryState(t, details.After, failed)
	var reportReason, reportSource string
	var reportRunID uuid.UUID
	var reportRequestKey, reportAgentID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		SELECT result_summary, completion_source, source_run_id, request_key, source_agent_id
		FROM task_report_events
		WHERE task_id=$1 AND task_version=$2`, fixture.TaskA, failed.Revision,
	).Scan(&reportReason, &reportSource, &reportRunID, &reportRequestKey, &reportAgentID); err != nil {
		t.Fatalf("read failed Task report event: %v", err)
	}
	if reportReason != input.Reason || reportSource != reportSourceAgentBusinessFailure ||
		reportRunID != runID || reportRequestKey != requestKey || reportAgentID != fixture.AgentA {
		t.Fatalf(
			"failed Task report = reason %q source %q run %d key %s agent %s",
			reportReason, reportSource, reportRunID, reportRequestKey, reportAgentID,
		)
	}
	if failureTaskUpdated != 1 || memberTaskUpdated != 0 || reportReady != 1 {
		t.Fatalf(
			"published failure/member task.updated/task.report_ready = %d/%d/%d, want 1/0/1",
			failureTaskUpdated,
			memberTaskUpdated,
			reportReady,
		)
	}
	todo := StatusTodo
	if _, err := service.Update(ctx, principal, fixture.TaskA, UpdateInput{
		ExpectedRevision: failed.Revision,
		Status:           &todo,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Update(reopen while failure Run is active) error = %v, want %v", err, ErrConflict)
	}

	replayed, err := service.MarkFailed(ctx, principal, origin, input)
	if err != nil || replayed.Revision != failed.Revision || replayed.CompletedAt == nil ||
		!replayed.CompletedAt.Equal(*failed.CompletedAt) {
		t.Fatalf("MarkFailed(replay) = %+v, %v", replayed, err)
	}
	if failureTaskUpdated != 1 || memberTaskUpdated != 0 || reportReady != 1 {
		t.Fatalf(
			"events after replay = %d/%d/%d, want unchanged",
			failureTaskUpdated,
			memberTaskUpdated,
			reportReady,
		)
	}
	if got := history(); len(got) != 1 || got[0].ID != recorded[0].ID {
		t.Fatalf("failure replay history = %+v", got)
	}
	if _, err := service.MarkFailed(ctx, principal, origin, FailureInput{
		RequestKey: requestKey, Reason: "A different reason",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("MarkFailed(changed payload) error = %v, want %v", err, ErrConflict)
	}
	if _, err := service.MarkFailed(ctx, principal, origin, FailureInput{
		RequestKey: dbtest.NewUUID(t), Reason: input.Reason,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("MarkFailed(new key after terminal Task) error = %v, want %v", err, ErrConflict)
	}
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status='completed', updated_at=clock_timestamp() WHERE id=$1`, runID)
	reopened, err := service.Update(ctx, principal, fixture.TaskA, UpdateInput{
		ExpectedRevision: failed.Revision,
		Status:           &todo,
	})
	if err != nil || reopened.Task.Status != StatusTodo || reopened.Task.Revision != failed.Revision+1 {
		t.Fatalf("Update(reopen after failure Run terminal) = %+v, %v", reopened, err)
	}
	if failureTaskUpdated != 1 || memberTaskUpdated != 1 || reportReady != 1 {
		t.Fatalf(
			"events after member reopen = %d/%d/%d, want 1/1/1",
			failureTaskUpdated,
			memberTaskUpdated,
			reportReady,
		)
	}

	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status='in_progress', completed_at=NULL,
		    execution_generation=8, version=version+1, updated_at=clock_timestamp()
		WHERE id=$1`, fixture.TaskA)
	var terminalRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, task_execution_generation
		, run_type) VALUES ($1,$2,$3,$4,$5,'completed',8, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA,
	).Scan(&terminalRunID); err != nil {
		t.Fatalf("seed terminal Task Run: %v", err)
	}
	terminalOrigin := origin
	terminalOrigin.RunID = terminalRunID
	if _, err := service.MarkFailed(ctx, principal, terminalOrigin, FailureInput{
		RequestKey: dbtest.NewUUID(t), Reason: input.Reason,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("MarkFailed(terminal Run) error = %v, want %v", err, ErrConflict)
	}

	var staleRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, task_execution_generation
		, run_type) VALUES ($1,$2,$3,$4,$5,'running',7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA,
	).Scan(&staleRunID); err != nil {
		t.Fatalf("seed stale-generation Task Run: %v", err)
	}
	staleOrigin := origin
	staleOrigin.RunID = staleRunID
	if _, err := service.MarkFailed(ctx, principal, staleOrigin, FailureInput{
		RequestKey: dbtest.NewUUID(t), Reason: input.Reason,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("MarkFailed(stale generation) error = %v, want %v", err, ErrConflict)
	}
	agents, err := agentdomain.NewAgentService(agentdomain.ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	renamedAgent := "Renamed after the failure"
	if _, err := agents.Update(ctx,
		principal,
		fixture.AgentA,
		agentdomain.UpdateInput{
			ExpectedRevision: 1,
			Name:             &renamedAgent,
		}); err != nil {
		t.Fatal(err)
	}
	retained := history()
	if len(retained) != 2 ||
		retained[1].Actor.DisplayName == nil ||
		recorded[0].Actor.DisplayName == nil ||
		*retained[1].Actor.DisplayName != *recorded[0].Actor.DisplayName ||
		!reflect.DeepEqual(retained[1].Details,
			recorded[0].Details) {
		t.Fatal("Agent rename rewrote retained failure attribution")
	}
	// Physical removal is a retained-history fixture; the Agent API exposes
	// archival, not deletion. Attribution must not require a surviving actor.
	dbtest.MustExec(t, ctx, pool, `DELETE FROM tasks WHERE company_id=$1`, fixture.CompanyA)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM conversations WHERE company_id=$1`, fixture.CompanyA)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM agents WHERE id=$1`, fixture.AgentA)
	afterRemoval := history()
	if !reflect.DeepEqual(afterRemoval, retained) {
		t.Fatal("removing the actor or subject changed retained history")
	}
	if got := history(); len(got) != 2 {
		t.Fatalf("history after rejected proofs = %+v, want failure and User reopen", got)
	}
}
