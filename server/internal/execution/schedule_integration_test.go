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

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
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

func TestSupersedeScheduleRunInTxRejectsNonqueuedRuns(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)

	for _, status := range []string{
		"dispatched", "waiting_local_directory", "running", "deferred",
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
	if linked.ConnectorSelectionMode != "exact" || selected != connectorID || revision != 4 || digest != "reviewed-digest" {
		t.Fatalf("linked mode %q, selection %s revision %d digest %q", linked.ConnectorSelectionMode, selected, revision, digest)
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
