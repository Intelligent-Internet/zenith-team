//go:build integration

package task

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestApplyRunTerminalInTxUpdatesOnlyTheOwningTaskGeneration(t *testing.T) {
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
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status = 'in_progress', execution_generation = 7
		WHERE id = $1`, fixture.TaskA)
	var runID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, task_execution_generation
		, run_type)
		VALUES ($1, $2, $3, $4, $5, 'completed', 7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA,
		fixture.AgentA,
		fixture.TaskA,
		fixture.ConversationA,
		fixture.RuntimeA,
	).Scan(&runID); err != nil {
		t.Fatalf("insert terminal Run: %v", err)
	}
	run, err := store.Queries().GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("read terminal Run: %v", err)
	}

	var pending []events.Event
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		return ApplyRunTerminalInTx(ctx, queries, &pending, run)
	}); err != nil {
		t.Fatalf("apply terminal Run Task effect: %v", err)
	}
	if len(pending) != 2 || pending[1].Name != "activity.created" || pending[0].Name != EventUpdated ||
		pending[0].Actor.Type != events.ActorTypeSystem {
		t.Fatalf("terminal Task events = %+v", pending)
	}
	if pending[0].RunID != runID || pending[0].TaskID != fixture.TaskA ||
		pending[0].ConversationID != fixture.ConversationA ||
		pending[0].Actor != events.SystemActor() {
		t.Fatalf("terminal Task event envelope = %+v", pending[0])
	}
	value, err := events.PayloadOf[Response](pending[0])
	if err != nil {
		t.Fatalf("decode terminal Task event: %v", err)
	}
	if value.Status != StatusInReview ||
		value.Assignee.DisplayName == "" || !value.DependenciesResolved {
		t.Fatalf("terminal Task projection = %+v", value)
	}

	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status = 'in_progress', execution_generation = 8
		WHERE id = $1`, fixture.TaskA)
	pending = nil
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		return ApplyRunTerminalInTx(ctx, queries, &pending, run)
	}); err != nil {
		t.Fatalf("apply superseded terminal Run Task effect: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("superseded terminal Run staged Task events = %+v", pending)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM tasks WHERE id = $1`, fixture.TaskA).
		Scan(&status); err != nil {
		t.Fatalf("read superseded Task: %v", err)
	}
	if status != "in_progress" {
		t.Fatalf("superseded Task status = %q, want in_progress", status)
	}
}

func TestApplyRunTerminalInTxKeepsHumanInputWorkInProgress(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		humanWork    bool
		requestType  string
		runStatus    string
		status       string
		continuation string
		previousRun  bool
		want         Status
	}{
		{name: "human work awaiting answer", humanWork: true, requestType: "question", runStatus: "completed", status: "pending", want: StatusInProgress},
		{name: "pending question", requestType: "question", runStatus: "completed", status: "pending", want: StatusInProgress},
		{name: "pending approval", requestType: "approval", runStatus: "completed", status: "pending", want: StatusInProgress},
		{name: "answered question awaiting continuation", requestType: "question", runStatus: "completed", status: "answered", continuation: "pending", want: StatusInProgress},
		{name: "approved action awaiting continuation", requestType: "approval", runStatus: "completed", status: "approved", continuation: "pending", want: StatusInProgress},
		{name: "inline approval already applied", requestType: "approval", runStatus: "completed", status: "approved", continuation: "not_required", want: StatusInReview},
		{name: "question from an older run", requestType: "question", runStatus: "completed", status: "pending", previousRun: true, want: StatusInReview},
		{name: "failure with pending question", requestType: "question", runStatus: "failed", status: "pending", want: StatusError},
		{name: "cancellation with pending approval", requestType: "approval", runStatus: "cancelled", status: "pending", want: StatusCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
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
			if test.humanWork {
				dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET executed_by_agent_id=NULL WHERE id=$1`, fixture.TaskA)
			}
			dbtest.MustExec(t, ctx, pool, `
				UPDATE tasks SET status='in_progress', execution_generation=7
				WHERE id=$1`, fixture.TaskA)
			var runID uuid.UUID
			if err := pool.QueryRow(
				ctx, `
				INSERT INTO runs (company_id, agent_id, task_id, conversation_id,
				                  runtime_id, status, task_execution_generation, run_type)
				VALUES ($1,$2,$3,$4,$5,$6,7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 )) RETURNING id`,
				fixture.CompanyA, fixture.AgentA, fixture.TaskA,
				fixture.ConversationA, fixture.RuntimeA, test.runStatus,
			).Scan(&runID); err != nil {
				t.Fatalf("insert terminal Run: %v", err)
			}
			originRunID := runID
			if test.previousRun {
				if err := pool.QueryRow(
					ctx, `
					INSERT INTO runs (company_id, agent_id, task_id, conversation_id,
					                  runtime_id, status, task_execution_generation, run_type)
					VALUES ($1,$2,$3,$4,$5,'completed',6, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 )) RETURNING id`,
					fixture.CompanyA, fixture.AgentA, fixture.TaskA,
					fixture.ConversationA, fixture.RuntimeA,
				).Scan(&originRunID); err != nil {
					t.Fatalf("insert previous Run: %v", err)
				}
			}
			switch test.requestType {
			case "question":
				dbtest.MustExec(t, ctx, pool, `
					INSERT INTO questions (company_id, title, asked_by_agent_id,
					    origin_run_id, request_key, task_id, conversation_id, prompts,
					    request_sha256, expires_at, status, continuation_status)
					VALUES ($1,'Choose a direction',$2,$3,gen_random_uuid(),$4,$5,
					        '[]',decode(repeat('00',32),'hex'),now()+interval '1 hour',$6,NULLIF($7,''))`,
					fixture.CompanyA, fixture.AgentA, originRunID, fixture.TaskA,
					fixture.ConversationA, test.status, test.continuation)
			case "approval":
				dbtest.MustExec(t, ctx, pool, `
					INSERT INTO approvals (company_id, rule_key, title, preview_markdown,
					    requested_by_agent_id, origin_run_id, request_key, task_id,
					    conversation_id, canonical_action, request_sha256, action_sha256,
					    expires_at, status, continuation_status)
					VALUES ($1,'plan','Approve this step','Review',$2,$3,gen_random_uuid(),
					        $4,$5,'{}',decode(repeat('00',32),'hex'),decode(repeat('00',32),'hex'),
					        now()+interval '1 hour',$6,NULLIF($7,''))`,
					fixture.CompanyA, fixture.AgentA, originRunID, fixture.TaskA,
					fixture.ConversationA, test.status, test.continuation)
			}
			run, err := store.Queries().GetRun(ctx, runID)
			if err != nil {
				t.Fatalf("read terminal Run: %v", err)
			}
			var pending []events.Event
			if err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
				return ApplyRunTerminalInTx(ctx, queries, &pending, run)
			}); err != nil {
				t.Fatalf("apply terminal Run Task effect: %v", err)
			}
			var status string
			if err := pool.QueryRow(ctx, `SELECT status FROM tasks WHERE id=$1`, fixture.TaskA).
				Scan(&status); err != nil {
				t.Fatalf("read Task status: %v", err)
			}
			if status != string(test.want) {
				t.Fatalf("Task status = %q, want %q", status, test.want)
			}
			if test.want == StatusInProgress && len(pending) != 0 {
				t.Fatalf("Task events = %+v, want no review transition", pending)
			}
		})
	}
}
