//go:build integration

package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestScheduleExpiryUsesFiringAgeAndPreservesCronWaiting(t *testing.T) {
	pool, store, f := startSchedulePostgres(t)
	ctx := t.Context()
	svc := newScheduleTestService(t, store)
	principal := companyPrincipal(f.UserA, f.CompanyA)
	for _, test := range []struct {
		name, trigger, status, age string
		expire                     bool
	}{
		{"cron after ten hours", "cron", "queued", "10 hours", false},
		{"cron deferred after eight days", "cron", "deferred", "8 days", false},
		{"once six days", "once", "queued", "6 days", false},
		{"once eight days", "once", "queued", "8 days", true},
		{"once deferred eight days", "once", "deferred", "8 days", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			created := createDueSchedule(t, ctx, pool, svc, principal, f.AgentA)
			firingID, parentID := seedActiveScheduleRun(
				t,
				ctx,
				pool,
				svc,
				principal,
				created.Schedule,
				"failed",
			)
			if test.trigger == "cron" {
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`UPDATE schedules SET trigger_type='cron',run_at=NULL,cron_expression='0 9 * * *',timezone='UTC' WHERE id=$1`,
					created.Schedule.ID,
				)
			}
			// The retry is fresh; only the original receipt is old.
			child, err := store.Queries().
				CreateRetryRun(ctx, generated.CreateRetryRunParams{ParentRunID: parentID})
			if err != nil {
				t.Fatal(err)
			}
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE schedule_firings SET created_at=now()-$2::interval WHERE id=$1`,
				firingID,
				test.age,
			)
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE runs SET status=$2,fire_at=now()+interval '1 hour' WHERE id=$1`,
				child.ID,
				test.status,
			)
			rows, err := store.Queries().
				ExpireStaleQueuedRuns(ctx, generated.ExpireStaleQueuedRunsParams{RunIds: []uuid.UUID{child.ID}, TtlSecs: 7200, OnceWaitSecs: 604800})
			if err != nil {
				t.Fatal(err)
			}
			if (len(rows) == 1) != test.expire {
				t.Fatalf("expired %d, want %v", len(rows), test.expire)
			}
			history, err := svc.ListFirings(ctx, principal, created.Schedule.ID, FiringListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(history.Firings) != 1 || history.Firings[0].RunID == nil ||
				*history.Firings[0].RunID != child.ID ||
				history.Firings[0].Attempt == nil ||
				*history.Firings[0].Attempt != child.Attempt {
				t.Fatalf("history lost latest retry: %+v", history)
			}
			if test.expire && (rows[0].FailureReason.String != "queued_expired" || rows[0].FireAt.Valid) {
				t.Fatalf("wrong expiry result: %+v", rows[0])
			}
		})
	}
}

func TestSchedulePauseFencesRetryAndClaimFailure(t *testing.T) {
	pool, store, f := startSchedulePostgres(t)
	ctx := t.Context()
	svc := newScheduleTestService(t, store)
	principal := companyPrincipal(f.UserA, f.CompanyA)
	for _, status := range []string{"running", "dispatched"} {
		t.Run(status, func(t *testing.T) {
			created := createDueSchedule(t, ctx, pool, svc, principal, f.AgentA)
			_, runID := seedActiveScheduleRun(t, ctx, pool, svc, principal, created.Schedule, status)
			current, err := svc.Get(ctx, principal, created.Schedule.ID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := svc.Pause(
				ctx,
				principal,
				current.ID,
				RevisionInput{ExpectedRevision: current.Revision},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.RunEffect != "running_run_continues" {
				t.Fatalf("effect = %s", result.RunEffect)
			}
			if status == "dispatched" {
				before, err := store.Queries().GetRun(ctx, runID)
				if err != nil {
					t.Fatal(err)
				}
				run, err := store.Queries().
					RequeueRunAfterClaimFailure(ctx, generated.RequeueRunAfterClaimFailureParams{ID: runID, RuntimeID: before.RuntimeID, UpdatedAt: before.UpdatedAt})
				if err != nil {
					t.Fatal(err)
				}
				if run.Status != "cancelled" {
					t.Fatalf("invalid occurrence returned to %s", run.Status)
				}
			} else {
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`UPDATE runs SET status='failed',failure_reason='runtime_offline',finished_at=now() WHERE id=$1`,
					runID,
				)
				_, err := store.Queries().
					CreateRetryRun(ctx, generated.CreateRetryRunParams{ParentRunID: runID})
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("stopped occurrence retried: %v", err)
				}
			}
		})
	}
}

func TestScheduleUpdateRejectsOneTimePreset(t *testing.T) {
	pool, store, f := startSchedulePostgres(t)
	ctx := t.Context()
	svc := newScheduleTestService(t, store)
	principal := companyPrincipal(f.UserA, f.CompanyA)
	created := createDueSchedule(t, ctx, pool, svc, principal, f.AgentA)
	next := time.Now().Truncate(time.Minute).Add(time.Hour)
	input := previewInputFromResponse(created.Schedule)
	input.Trigger = Trigger{Type: TriggerTypeOnce, RunAt: &next}
	preview, err := svc.Preview(ctx, principal, input)
	if err != nil {
		t.Fatal(err)
	}
	preset := "daily_brief_v1"
	_, err = svc.Update(
		ctx,
		principal,
		created.Schedule.ID,
		UpdateInput{
			ExpectedRevision: created.Schedule.Revision,
			Trigger:          &input.Trigger,
			PresetKey:        NullableTextChange{Present: true, Value: &preset},
			ReviewDigest:     preview.ReviewDigest,
		},
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("one-time preset update = %v, want invalid input", err)
	}
}

func TestFiringWorkerContainsPanicAndSkipsLockedCompany(t *testing.T) {
	for _, brokenMode := range []string{"panic", "company lock"} {
		t.Run(brokenMode, func(t *testing.T) {
			pool, store, f := startSchedulePostgres(t)
			ctx := t.Context()
			svc := newScheduleTestService(t, store)
			broken := createDueSchedule(t, ctx, pool, svc, companyPrincipal(f.UserA, f.CompanyA), f.AgentA)
			healthy := createDueSchedule(t, ctx, pool, svc, companyPrincipal(f.UserB, f.CompanyB), f.AgentB)
			materializer := MaterializerFunc(
				func(ctx context.Context, q *generated.Queries, pending *[]events.Event, input MaterializeInput) (MaterializeResult, error) {
					if brokenMode == "panic" && input.ScheduleID == broken.Schedule.ID {
						panic("isolated failure")
					}
					return materializedScheduleRun(ctx, q, pending, input)
				},
			)
			if brokenMode == "company lock" {
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				if _, err := tx.Exec(
					ctx,
					`SELECT id FROM companies WHERE id=$1 FOR UPDATE`,
					f.CompanyA,
				); err != nil {
					t.Fatal(err)
				}
			}
			worker := reliabilityWorker(t, store, materializer)
			var issues []FiringIssue
			worker.options.OnIssue = func(issue FiringIssue) { issues = append(issues, issue) }
			deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if _, err := worker.FireOnce(deadline); err != nil {
				t.Fatal(err)
			}
			if countScheduleFirings(t, ctx, pool, healthy.Schedule.ID) != 1 {
				t.Fatal("healthy Company did not fire")
			}
			if countScheduleFirings(t, ctx, pool, broken.Schedule.ID) != 0 {
				t.Fatal("broken occurrence committed")
			}
			if len(issues) == 0 || issues[0].ScheduleID != broken.Schedule.ID ||
				issues[0].CompanyID != f.CompanyA ||
				issues[0].Path != "sweep" {
				t.Fatalf("missing diagnostic identity: %+v", issues)
			}
			if brokenMode == "panic" && len(issues[0].Stack) == 0 {
				t.Fatal("panic stack missing")
			}
		})
	}
}

func TestFiringEscalationRequiresHealthyProgressAndKeepsAttentionPrivate(t *testing.T) {
	pool, store, f := startSchedulePostgres(t)
	ctx := t.Context()
	svc := newScheduleTestService(t, store)
	principal := companyPrincipal(f.UserA, f.CompanyA)
	broken := createDueSchedule(t, ctx, pool, svc, principal, f.AgentA)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE schedules SET next_occurrence_at=now()-interval '20 minutes' WHERE id=$1`,
		broken.Schedule.ID,
	)
	worker := reliabilityWorker(
		t,
		store,
		MaterializerFunc(
			func(ctx context.Context, q *generated.Queries, pending *[]events.Event, input MaterializeInput) (MaterializeResult, error) {
				if input.ScheduleID == broken.Schedule.ID {
					return MaterializeResult{}, errors.New("row-local failure")
				}
				return materializedScheduleRun(ctx, q, pending, input)
			},
		),
	)
	for range 6 {
		if _, err := worker.FireOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	current, err := svc.Get(ctx, principal, broken.Schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusActive {
		t.Fatal("an outage without healthy progress paused a Schedule")
	}
	createDueSchedule(t, ctx, pool, svc, companyPrincipal(f.UserB, f.CompanyB), f.AgentB)
	if _, err := worker.FireOnce(ctx); err != nil {
		t.Fatal(err)
	}
	current, err = svc.Get(ctx, principal, broken.Schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusPaused || current.PauseReason == nil || *current.PauseReason != ReasonFiringFailed {
		t.Fatalf("not escalated: %+v", current)
	}
	var recipients int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE conversation_id=$1 AND recipient_user_id=$2`, current.ConversationID, f.UserA).
		Scan(&recipients); err != nil {
		t.Fatal(err)
	}
	if recipients != 1 {
		t.Fatalf("attention notices = %d", recipients)
	}
	var others int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE conversation_id=$1 AND recipient_user_id<>$2`, current.ConversationID, f.UserA).
		Scan(&others); err != nil {
		t.Fatal(err)
	}
	if others != 0 {
		t.Fatal("private Schedule leaked an attention notice")
	}
}

// A claim can only inspect a stopped occurrence; it must never take it.
func TestScheduleClaimCannotTakeCancelledOccurrence(t *testing.T) {
	pool, store, f := startSchedulePostgres(t)
	ctx := t.Context()
	svc := newScheduleTestService(t, store)
	principal := companyPrincipal(f.UserA, f.CompanyA)
	created := createDueSchedule(t, ctx, pool, svc, principal, f.AgentA)
	firingID, runID := seedActiveScheduleRun(t, ctx, pool, svc, principal, created.Schedule, "queued")
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE schedule_firings SET disposition='cancelled',reason='schedule_paused' WHERE id=$1`,
		firingID,
	)
	claim := generated.SelectClaimableRunParams{AgentID: f.AgentA, RuntimeID: f.RuntimeA, TaskLaneOpen: true}
	if _, err := store.Queries().SelectClaimableRun(ctx, claim); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("claim crossed stopped receipt: %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE schedule_firings SET disposition='materialized',reason=NULL WHERE id=$1`,
		firingID,
	)
	claimed, err := store.Queries().SelectClaimableRun(ctx, claim)
	if err != nil || claimed.ID != runID {
		t.Fatalf("active receipt not claimable: %v", err)
	}
}

func TestScheduleEditRecordsUnfiredDueTimeAndStopsWaitingWork(t *testing.T) {
	pool, store, f := startSchedulePostgres(t)
	ctx := t.Context()
	svc := newScheduleTestService(t, store)
	principal := companyPrincipal(f.UserA, f.CompanyA)
	created := createDueSchedule(t, ctx, pool, svc, principal, f.AgentA)
	_, runID := seedActiveScheduleRun(t, ctx, pool, svc, principal, created.Schedule, "deferred")
	before, err := svc.Get(ctx, principal, created.Schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := previewInputFromResponse(before)
	input.Name = "Reviewed replacement"
	next := time.Now().UTC().Truncate(time.Minute).Add(time.Hour)
	input.Trigger = Trigger{Type: TriggerTypeOnce, RunAt: &next}
	preview, err := svc.Preview(ctx, principal, input)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.Update(ctx, principal, before.ID, UpdateInput{
		ExpectedRevision: before.Revision,
		Name:             &input.Name,
		Trigger:          &input.Trigger,
		ReviewDigest:     preview.ReviewDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RunEffect != "waiting_run_cancelled" || updated.Schedule.NextOccurrenceAt == nil ||
		!updated.Schedule.NextOccurrenceAt.Equal(next) {
		t.Fatalf("edit result = %+v", updated)
	}
	run, err := store.Queries().GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "cancelled" || run.FireAt.Valid {
		t.Fatalf("old work survived edit: %+v", run)
	}
	history, err := svc.ListFirings(ctx, principal, before.ID, FiringListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Firings) != 2 {
		t.Fatalf("history = %+v, want stopped manual and skipped scheduled receipts", history)
	}
	var stopped, skipped bool
	for _, firing := range history.Firings {
		stopped = stopped || firing.Disposition == FiringDispositionCancelled
		skipped = skipped ||
			firing.Disposition == FiringDispositionSkipped && firing.Reason != nil && *firing.Reason == ReasonDefinitionUpdate &&
				firing.OccurrenceAt.Equal(*before.NextOccurrenceAt)
	}
	if !stopped || !skipped {
		t.Fatalf("missing edit audit: %+v", history)
	}
}

func TestCompletedLegacyPresetDoesNotBlockRecurringReplacement(t *testing.T) {
	pool, store, f := startSchedulePostgres(t)
	ctx := t.Context()
	svc := newScheduleTestService(t, store)
	principal := companyPrincipal(f.UserA, f.CompanyA)
	legacy := createDueSchedule(t, ctx, pool, svc, principal, f.AgentA)
	worker := reliabilityWorker(t, store, MaterializerFunc(materializedScheduleRun))
	if _, err := worker.FireSchedule(ctx, legacy.Schedule.ID, "sweep"); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE schedules SET preset_key='daily_brief_v1' WHERE id=$1`,
		legacy.Schedule.ID,
	)
	input := CreateInput{
		RequestKey:   uuid.New(),
		AgentID:      f.AgentA,
		Name:         "Daily brief",
		Instructions: "Summarize the reviewed sources.",
		Trigger: Trigger{
			Type:       TriggerTypeCron,
			Expression: stringPointer("0 9 * * *"),
			TimeZone:   stringPointer("UTC"),
		},
		Status:       StatusActive,
		PresetKey:    stringPointer("daily_brief_v1"),
		ConnectorIDs: []uuid.UUID{},
	}
	input.ReviewDigest = reviewScheduleInput(t, svc, principal, input)
	created, err := svc.Create(ctx, principal, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Schedule.ID == legacy.Schedule.ID {
		t.Fatal("replacement overwrote legacy schedule")
	}
	old, err := svc.Get(ctx, principal, legacy.Schedule.ID)
	if err != nil || old.Status != StatusCompleted {
		t.Fatalf("legacy preset changed: %+v, %v", old, err)
	}
}
