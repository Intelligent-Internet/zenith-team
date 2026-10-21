//go:build integration

package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestSupersedeScheduleRunInTxRejectsAnotherSchedule(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	input := seedScheduleRunCancellationInput(t, ctx, pool, queries, fixture, "queued")
	otherSchedule := insertCancellationTestSchedule(t, ctx, queries, fixture, "Other Schedule")
	input.ScheduleID = otherSchedule.ID

	pending := []events.Event{}
	err := SupersedeScheduleRunInTx(ctx, queries, &pending, input)
	if !errors.Is(err, ErrScheduleRunConflict) {
		t.Fatalf("SupersedeScheduleRunInTx(wrong Schedule) error = %v, want %v", err, ErrScheduleRunConflict)
	}
	if len(pending) != 0 {
		t.Fatalf("staged events = %d, want 0", len(pending))
	}
	if got := cancellationTestRunStatus(t, ctx, pool, input.RunID); got != "queued" {
		t.Fatalf("Run status = %q, want queued", got)
	}
}

func TestSupersedeScheduleRunInTxRejectsNonwaitingRuns(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)

	for _, status := range []string{
		"dispatched", "waiting_local_directory", "running",
		"completed", "failed", "cancelled",
	} {
		t.Run(status, func(t *testing.T) {
			input := seedScheduleRunCancellationInput(t, ctx, pool, queries, fixture, status)
			pending := []events.Event{}

			err := SupersedeScheduleRunInTx(ctx, queries, &pending, input)
			if !errors.Is(err, ErrScheduleRunConflict) {
				t.Fatalf(
					"SupersedeScheduleRunInTx(status %q) error = %v, want %v",
					status,
					err,
					ErrScheduleRunConflict,
				)
			}
			if len(pending) != 0 {
				t.Fatalf("staged events = %d, want 0", len(pending))
			}
			if got := cancellationTestRunStatus(t, ctx, pool, input.RunID); got != status {
				t.Fatalf("Run status = %q, want %q", got, status)
			}
		})
	}
}

func TestLinkRunToScheduleFiringCopiesTheExactConnectorScope(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	input := seedUnlinkedScheduleRunCancellationInput(t, ctx, pool, queries, fixture, "queued")

	// A Schedule Run is never linked without the exact scope.
	_, err := pool.Exec(ctx, `UPDATE runs SET schedule_firing_id = $2 WHERE id = $1`, input.RunID, input.FiringID)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23514" {
		t.Fatalf("link without the exact scope error = %v, want PostgreSQL SQLSTATE 23514", err)
	}

	var connectorID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO connector_catalog_items (company_id, provider, source, native_id, name, status, action, observed_at)
		VALUES ($1, 'codex', 'codex_app', 'github', 'GitHub', 'unavailable', 'none', now()) RETURNING id`,
		fixture.CompanyA).Scan(&connectorID); err != nil {
		t.Fatalf("insert Connector: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO schedule_sources (
			schedule_id, connector_id, company_id, provider, source, native_id,
			reviewed_contract_digest, policy_revision, position
		) VALUES ($1, $2, $3, 'codex', 'codex_app', 'github', 'reviewed-digest', 4, 0)`,
		input.ScheduleID, connectorID, fixture.CompanyA)
	linked, err := queries.LinkRunToScheduleFiring(ctx, generated.LinkRunToScheduleFiringParams{
		FiringID: input.FiringID, ScheduleID: input.ScheduleID,
		RunID: input.RunID, CompanyID: input.CompanyID, ConversationID: input.ConversationID,
	})
	if err != nil {
		t.Fatalf("LinkRunToScheduleFiring() error = %v", err)
	}
	var selected uuid.UUID
	var revision int64
	var digest string
	if err := pool.QueryRow(ctx, `
		SELECT connector_id, policy_revision, contract_digest FROM run_connector_selections WHERE run_id = $1`,
		input.RunID).Scan(&selected, &revision, &digest); err != nil {
		t.Fatalf("read the Run's exact Connector scope: %v", err)
	}
	if linked.ConnectorSelectionMode != "exact" || selected != connectorID || revision != 4 ||
		digest != "reviewed-digest" {
		t.Fatalf(
			"linked mode %q, selection %s revision %d digest %q",
			linked.ConnectorSelectionMode,
			selected,
			revision,
			digest,
		)
	}
}

func seedScheduleRunCancellationInput(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	queries *generated.Queries,
	fixture dbtest.Fixture,
	status string,
) ScheduleRunCancellationInput {
	t.Helper()
	input := seedUnlinkedScheduleRunCancellationInput(t, ctx, pool, queries, fixture, status)
	if _, err := queries.LinkRunToScheduleFiring(ctx, generated.LinkRunToScheduleFiringParams{
		FiringID: input.FiringID, ScheduleID: input.ScheduleID,
		RunID: input.RunID, CompanyID: input.CompanyID, ConversationID: input.ConversationID,
	}); err != nil {
		t.Fatalf("LinkRunToScheduleFiring() error = %v", err)
	}
	return input
}

func seedUnlinkedScheduleRunCancellationInput(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	queries *generated.Queries,
	fixture dbtest.Fixture,
	status string,
) ScheduleRunCancellationInput {
	t.Helper()
	schedule := insertCancellationTestSchedule(t, ctx, queries, fixture, "Cancellation Schedule")
	run, err := queries.CreateRun(ctx, generated.CreateRunParams{
		CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		ConversationID: schedule.ConversationID, RuntimeID: fixture.RuntimeA,
		Status: status, Attempt: 1,
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	var messageID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO conversation_items (
			company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
		) VALUES ($1,$2,$3,1,'user_message','user',$4,'Run this Schedule.')
		RETURNING id`, fixture.CompanyA, schedule.ConversationID, run.ID, fixture.UserA,
	).Scan(&messageID); err != nil {
		t.Fatalf("insert Schedule Message: %v", err)
	}
	firing, err := queries.InsertScheduleFiring(ctx, generated.InsertScheduleFiringParams{
		CompanyID: fixture.CompanyA, ScheduleID: schedule.ID,
		ScheduleVersion: schedule.Version, ConversationID: schedule.ConversationID,
		MessageID: uuid.NullUUID{UUID: messageID, Valid: true},
		RunID:     uuid.NullUUID{UUID: run.ID, Valid: true},
		OccurrenceAt: pgtype.Timestamptz{
			Time: time.Now().UTC(), Valid: true,
		},
		Trigger: "manual", RequestKey: uuid.NullUUID{UUID: dbtest.NewUUID(t), Valid: true},
		Disposition: "materialized",
	})
	if err != nil {
		t.Fatalf("InsertScheduleFiring() error = %v", err)
	}
	return ScheduleRunCancellationInput{
		RunID: run.ID, FiringID: firing.ID, ScheduleID: schedule.ID,
		CompanyID: fixture.CompanyA, ConversationID: schedule.ConversationID,
	}
}

func insertCancellationTestSchedule(
	t *testing.T,
	ctx context.Context,
	queries *generated.Queries,
	fixture dbtest.Fixture,
	name string,
) generated.InsertScheduleRow {
	t.Helper()
	runAt := time.Now().UTC().Add(time.Hour)
	schedule, err := queries.InsertSchedule(ctx, generated.InsertScheduleParams{
		ActorRoles: auth.RolesWith(auth.PermissionSchedulesManage),
		CompanyID:  fixture.CompanyA, CreatorUserID: fixture.UserA,
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		ReviewedProvider: "codex", Name: name, Instructions: "Run exactly once.",
		TriggerType: "once", RunAt: pgtype.Timestamptz{Time: runAt, Valid: true},
		Status: "draft",
	})
	if err != nil {
		t.Fatalf("InsertSchedule() error = %v", err)
	}
	return schedule
}

func cancellationTestRunStatus(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runID uuid.UUID,
) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM runs WHERE id=$1`, runID).Scan(&status); err != nil {
		t.Fatalf("read Run status: %v", err)
	}
	return status
}

func TestCancelPendingTaskScheduleRunsPreservesClaimedAndManualWork(t *testing.T) {
	h := newRunHarness(t)
	ctx := t.Context()
	for _, test := range []struct {
		name      string
		status    string
		scheduled bool
		want      string
	}{
		{"queued occurrence", "queued", true, "cancelled"},
		{"deferred occurrence", "deferred", true, "cancelled"},
		{"dispatched occurrence", "dispatched", true, "dispatched"},
		{"running occurrence", "running", true, "running"},
		{"preparing occurrence", "waiting_local_directory", true, "waiting_local_directory"},
		{"manual queued work", "queued", false, "queued"},
		{"manual deferred work", "deferred", false, "deferred"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var id uuid.UUID
			if err := h.pool.QueryRow(
				ctx,
				`
				INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id,
					execution_authorized_by_user_id, status, fire_at, run_type, task_execution_generation,
					task_scheduled)
				VALUES ($1, $2, $3, $4, $5, $6, $7,
					CASE WHEN $7 = 'deferred' THEN now() + interval '1 hour' END, 'task', 1,
					$8)
				RETURNING id`,
				h.fixture.CompanyA,
				h.fixture.AgentA,
				h.fixture.TaskA,
				h.fixture.ConversationA,
				h.fixture.RuntimeA,
				h.fixture.UserA,
				test.status,
				test.scheduled,
			).Scan(&id); err != nil {
				t.Fatal(err)
			}
			defer dbtest.MustExec(t, ctx, h.pool, `DELETE FROM runs WHERE id=$1`, id)
			var pending []events.Event
			var cancelled []generated.Run
			err := h.svc.runInTx(ctx, func(q *generated.Queries) error {
				var err error
				cancelled, err = CancelPendingTaskScheduleRunsInTx(
					ctx,
					q,
					&pending,
					TaskScheduleCancellation{
						CompanyID: h.fixture.CompanyA,
						TaskID:    h.fixture.TaskA,
						Reason:    CancellationScheduleEdited,
					},
				)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			got := mustRun(t, h, id)
			if got.Status != test.want {
				t.Fatalf("Run status = %s, want %s", got.Status, test.want)
			}
			wantCount := 0
			if test.want == "cancelled" {
				wantCount = 1
				if got.Error.String != CancellationScheduleEdited {
					t.Fatalf("Run cancellation reason = %q", got.Error.String)
				}
			}
			if len(cancelled) != wantCount {
				t.Fatalf("cancelled count = %d, want %d", len(cancelled), wantCount)
			}
			count := 0
			for _, event := range pending {
				if event.Name == EventRunCancelled && event.RunID == id {
					count++
				}
			}
			if count != wantCount {
				t.Fatalf("Run cancellation events = %d, want %d", count, wantCount)
			}
		})
	}
}

func TestTaskScheduleClaimUsesDatabaseDueTime(t *testing.T) {
	h := newRunHarness(t)
	ctx := t.Context()
	var scheduleID uuid.UUID
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO task_schedules (company_id, project_id, template_task_id, trigger, template_title, template_description)
		VALUES ($1, $2, $3, jsonb_build_object('type','once','runAt',now()+interval '1 hour'), 'Clock fence', '')
		RETURNING id`, h.fixture.CompanyA, h.fixture.ProjectA, h.fixture.TaskA).Scan(&scheduleID); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, h.pool, `UPDATE tasks SET task_schedule_id=$2,
		scheduled_at=now()+interval '1 hour', schedule_occurrence_at=now()+interval '1 hour',
		schedule_disposition='started', schedule_fired_at=now() WHERE id=$1`, h.fixture.TaskA, scheduleID)
	var run generated.Run
	var pending []events.Event
	err := h.svc.runInTx(ctx, func(q *generated.Queries) error {
		var err error
		run, err = EnqueueRunInTx(ctx, q, &pending, RunTrigger{
			CompanyID:          h.fixture.CompanyA,
			ConversationID:     h.fixture.ConversationA,
			AgentID:            h.fixture.AgentA,
			TaskID:             uuid.NullUUID{UUID: h.fixture.TaskA, Valid: true},
			Source:             RunSourceExecution,
			TriggerPrompt:      "clock fence",
			ActorUserID:        h.fixture.UserA,
			AuthorizedByUserID: h.fixture.UserA,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`UPDATE runs SET task_scheduled=true WHERE id=$1`,
		run.ID,
	)
	fence := claimCandidateParams()
	candidates, err := h.queries.ListQueuedClaimCandidatesByRuntimes(
		ctx,
		generated.ListQueuedClaimCandidatesByRuntimesParams{
			RuntimeIds:                []uuid.UUID{h.fixture.RuntimeA},
			ExecutePermission:         fence.ExecutePermission,
			ReadPermission:            fence.ReadPermission,
			CompanyDefaultPermissions: fence.CompanyDefaultPermissions,
		},
	)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("future Task candidates = %v, %v; want none", candidates, err)
	}
	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimed != nil {
		t.Fatalf("future Task claim = %v, %v; want none", claimed, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`UPDATE tasks SET scheduled_at=now()-interval '1 minute' WHERE id=$1`,
		h.fixture.TaskA,
	)
	dbtest.MustExec(t, ctx, h.pool, `UPDATE task_schedules SET generation=generation+1 WHERE id=$1`, scheduleID)
	candidates, err = h.queries.ListQueuedClaimCandidatesByRuntimes(
		ctx,
		generated.ListQueuedClaimCandidatesByRuntimesParams{
			RuntimeIds:                []uuid.UUID{h.fixture.RuntimeA},
			ExecutePermission:         fence.ExecutePermission,
			ReadPermission:            fence.ReadPermission,
			CompanyDefaultPermissions: fence.CompanyDefaultPermissions,
		},
	)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("obsolete occurrence candidates = %v, %v; want none", candidates, err)
	}
	claimed, err = h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimed != nil {
		t.Fatalf("obsolete occurrence claim = %v, %v; want none", claimed, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`UPDATE tasks SET task_schedule_generation=task_schedule_generation+1 WHERE id=$1`,
		h.fixture.TaskA,
	)
	claimed, err = h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimed == nil || claimed.ID != run.ID {
		t.Fatalf("due Task claim = %v, %v; want Run %s", claimed, err, run.ID)
	}
}
