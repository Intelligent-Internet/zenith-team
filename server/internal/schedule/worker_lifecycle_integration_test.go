//go:build integration

package schedule_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	agentdomain "github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	companydomain "github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/schedule"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// scheduleCreator is a member who is not the fixture owner: an admin with
// her own computer and her own setup of the fixture Agent, who created a due
// Schedule and has its Run queued. It carries the database it lives in.
type scheduleCreator struct {
	pool      *pgxpool.Pool
	store     *db.Store
	fixture   dbtest.Fixture
	principal auth.UserPrincipal
	runtimeID uuid.UUID
	schedule  schedule.Response
	firingID  uuid.UUID
	runID     uuid.UUID
}

func seedScheduleCreatorWithQueuedRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *db.Store,
	fixture dbtest.Fixture,
) scheduleCreator {
	t.Helper()
	service := schedule.NewScheduleTestServiceForTest(t, store)
	creator := scheduleCreator{
		pool: pool, store: store, fixture: fixture,
		principal: auth.UserPrincipal{
			UserID: dbtest.NewUUID(t), SessionID: uuid.New(), AuthKind: auth.TokenKindBearer,
			CompanyID: fixture.CompanyA, Role: auth.CompanyRoleAdmin,
		},
		runtimeID: dbtest.NewUUID(t),
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name) VALUES ($1, 'mia@example.com', 'Mia')`,
		creator.principal.UserID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'admin')`,
		fixture.CompanyA, creator.principal.UserID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-mia', 'Mia''s Mac', 'codex', 'online', $3, now())`,
		creator.runtimeID, fixture.CompanyA, creator.principal.UserID)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA,
		creator.principal.UserID, creator.runtimeID, "codex")
	creator.schedule = schedule.CreateDueScheduleForTest(
		t,
		ctx,
		pool,
		service,
		creator.principal,
		fixture.AgentA,
	).Schedule
	creator.firingID, creator.runID = schedule.SeedActiveScheduleRunForTest(
		t, ctx, pool, service, creator.principal, creator.schedule, "queued",
	)
	var authorizer, runtimeID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT execution_authorized_by_user_id, runtime_id FROM runs WHERE id = $1`,
		creator.runID).Scan(&authorizer, &runtimeID); err != nil {
		t.Fatalf("read the queued Schedule Run: %v", err)
	}
	if authorizer != creator.principal.UserID || runtimeID != creator.runtimeID {
		t.Fatalf("queued Schedule Run = authorized by %s on %s, want the creator on her own runtime",
			authorizer, runtimeID)
	}
	return creator
}

// A Schedule fires only on its creator's own setup of the Agent (ADR 0014),
// so it stops within one worker poll once its creator leaves the Company
// (creator_changed) or no longer has the Agent set up on a runtime of theirs
// that is not removed (agent_unavailable), and the next due pass enqueues
// nothing. Removing the member and unbinding the Agent cancel the
// creator's queued Run themselves; when a queued Run outlives the change,
// the reconcile pass invalidates it and marks its firing.
func TestFiringWorkerStopsAScheduleItsCreatorCanNoLongerRun(t *testing.T) {
	cases := []struct {
		name string
		// lose takes the creator's access away, through the product path or
		// straight in the database.
		lose       func(t *testing.T, ctx context.Context, creator scheduleCreator)
		wantReason string
		// The queued Run ends with this reason; its firing then has this
		// disposition and reason.
		wantRunReason    string
		wantDisposition  schedule.FiringDisposition
		wantFiringReason string
	}{
		{
			name: "the owner removes the creator from the Company",
			lose: func(t *testing.T, ctx context.Context, creator scheduleCreator) {
				t.Helper()
				members, err := companydomain.NewCompanyService(companydomain.ServiceOptions{
					Store: creator.store, Bus: schedule.NewScheduleTestBusForTest(t),
				})
				if err != nil {
					t.Fatalf("NewCompanyService() error = %v", err)
				}
				var revision int64
				if err := creator.pool.QueryRow(ctx, `
					SELECT version FROM company_members WHERE company_id = $1 AND user_id = $2`,
					creator.fixture.CompanyA, creator.principal.UserID,
				).Scan(&revision); err != nil {
					t.Fatalf("read the creator's membership revision: %v", err)
				}
				owner := schedule.CompanyPrincipalForTest(
					creator.fixture.UserA,
					creator.fixture.CompanyA,
				)
				if err := members.RemoveMember(ctx, owner, companydomain.MembershipMutation{
					UserID: creator.principal.UserID, ExpectedRevision: revision,
				}); err != nil {
					t.Fatalf("RemoveMember(creator) error = %v", err)
				}
			},
			wantReason:      schedule.ReasonCreatorChanged,
			wantRunReason:   "runtime_owner_inactive",
			wantDisposition: schedule.FiringDispositionMaterialized,
		},
		{
			name: "the creator unbinds the Agent",
			lose: func(t *testing.T, ctx context.Context, creator scheduleCreator) {
				t.Helper()
				bindings, err := agentdomain.NewBindingService(agentdomain.BindingServiceOptions{
					Store: creator.store, Bus: schedule.NewScheduleTestBusForTest(t),
				})
				if err != nil {
					t.Fatalf("NewBindingService() error = %v", err)
				}
				if err := bindings.Unbind(ctx, creator.principal, creator.fixture.AgentA); err != nil {
					t.Fatalf("Unbind() error = %v", err)
				}
			},
			wantReason:      schedule.ReasonAgentUnavailable,
			wantRunReason:   execution.RunReasonAgentUnbound,
			wantDisposition: schedule.FiringDispositionMaterialized,
		},
		{
			name: "the creator's membership ends while the Run is still queued",
			lose: func(t *testing.T, ctx context.Context, creator scheduleCreator) {
				t.Helper()
				dbtest.MustExec(t, ctx, creator.pool, `
					UPDATE company_members
					SET status='revoked', revoked_at=clock_timestamp(), revoked_by_user_id=$3,
					    updated_at=clock_timestamp(), version=version+1
					WHERE company_id=$1 AND user_id=$2`,
					creator.fixture.CompanyA, creator.principal.UserID, creator.fixture.UserA)
			},
			wantReason:       schedule.ReasonCreatorChanged,
			wantRunReason:    execution.CancellationScheduleInvalidated,
			wantDisposition:  schedule.FiringDispositionNeedsAttention,
			wantFiringReason: schedule.ReasonCreatorChanged,
		},
		{
			name: "the creator's binding goes while the Run is still queued",
			lose: func(t *testing.T, ctx context.Context, creator scheduleCreator) {
				t.Helper()
				dbtest.MustExec(t, ctx, creator.pool, `
					DELETE FROM agent_runtime_bindings
					WHERE company_id = $1 AND agent_id = $2 AND user_id = $3`,
					creator.fixture.CompanyA, creator.fixture.AgentA, creator.principal.UserID)
			},
			wantReason:       schedule.ReasonAgentUnavailable,
			wantRunReason:    execution.CancellationScheduleInvalidated,
			wantDisposition:  schedule.FiringDispositionNeedsAttention,
			wantFiringReason: schedule.ReasonAgentUnavailable,
		},
		{
			name: "the creator's runtime is removed while the Run is still queued",
			lose: func(t *testing.T, ctx context.Context, creator scheduleCreator) {
				t.Helper()
				dbtest.MustExec(t, ctx, creator.pool, `
					UPDATE runtimes SET removed_at = now() WHERE id = $1`, creator.runtimeID)
			},
			wantReason:       schedule.ReasonAgentUnavailable,
			wantRunReason:    execution.CancellationScheduleInvalidated,
			wantDisposition:  schedule.FiringDispositionNeedsAttention,
			wantFiringReason: schedule.ReasonAgentUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, store, fixture := schedule.StartSchedulePostgresForTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			creator := seedScheduleCreatorWithQueuedRun(t, ctx, pool, store, fixture)
			tc.lose(t, ctx, creator)

			materializer := &schedule.RecordingMaterializerForTest{}
			worker, err := schedule.NewFiringWorker(
				schedule.FiringWorkerDependencies{
					Store:        store,
					Bus:          schedule.NewScheduleTestBusForTest(t),
					Materializer: materializer,
					Superseder: schedule.QueuedRunSupersederFunc(
						schedule.SupersedeQueuedScheduleRunForTest,
					),
					Invalidator: schedule.QueuedRunInvalidatorFunc(
						schedule.InvalidateQueuedScheduleRunForTest,
					),
				},
				schedule.FiringWorkerOptions{PassLimit: 1},
			)
			if err != nil {
				t.Fatalf("schedule.NewFiringWorker() error = %v", err)
			}
			if count, err := worker.ReconcileOnce(ctx); err != nil || count != 1 {
				t.Fatalf("ReconcileOnce() = %d, %v; want 1, nil", count, err)
			}
			var status, pauseReason string
			if err := pool.QueryRow(ctx, `
				SELECT status, COALESCE(pause_reason, '') FROM schedules WHERE id = $1`,
				creator.schedule.ID).Scan(&status, &pauseReason); err != nil {
				t.Fatalf("read the Schedule: %v", err)
			}
			if status != string(schedule.StatusPaused) || pauseReason != tc.wantReason {
				t.Fatalf("Schedule after one reconcile = %s/%s, want paused/%s",
					status, pauseReason, tc.wantReason)
			}
			var runStatus, runReason, disposition, firingReason string
			if err := pool.QueryRow(ctx, `
				SELECT status, COALESCE(error, '') FROM runs WHERE id = $1`,
				creator.runID).Scan(&runStatus, &runReason); err != nil {
				t.Fatalf("read the queued Run: %v", err)
			}
			if err := pool.QueryRow(ctx, `
				SELECT disposition, COALESCE(reason, '') FROM schedule_firings WHERE id = $1`,
				creator.firingID).Scan(&disposition, &firingReason); err != nil {
				t.Fatalf("read the queued Run's firing: %v", err)
			}
			if runStatus != "cancelled" || runReason != tc.wantRunReason ||
				disposition != string(tc.wantDisposition) || firingReason != tc.wantFiringReason {
				t.Fatalf("queued Run = %s/%s, firing = %s/%s; want cancelled/%s, %s/%s",
					runStatus, runReason, disposition, firingReason,
					tc.wantRunReason, tc.wantDisposition, tc.wantFiringReason)
			}

			// The Schedule is still past due, and the next pass enqueues
			// nothing for it.
			var due bool
			if err := pool.QueryRow(ctx, `
				SELECT next_occurrence_at <= clock_timestamp() FROM schedules WHERE id = $1`,
				creator.schedule.ID).Scan(&due); err != nil || !due {
				t.Fatalf("paused Schedule past due = %t, %v; want true, nil", due, err)
			}
			if count, err := worker.FireOnce(ctx); err != nil || count != 0 {
				t.Fatalf("FireOnce() after the pause = %d, %v; want 0, nil", count, err)
			}
			if materializer.CallsForTest() != 0 {
				t.Fatalf("a paused Schedule reached the Materializer: %+v", materializer.CallsForTest())
			}
			var runs, firings int
			if err := pool.QueryRow(ctx, `
				SELECT (SELECT count(*) FROM runs WHERE conversation_id = $1),
				       (SELECT count(*) FROM schedule_firings WHERE schedule_id = $2)`,
				creator.schedule.ConversationID, creator.schedule.ID,
			).Scan(&runs, &firings); err != nil {
				t.Fatalf("count the Schedule's Runs and firings: %v", err)
			}
			if runs != 1 || firings != 1 {
				t.Fatalf("Runs, firings after FireOnce = %d, %d; want 1, 1", runs, firings)
			}
		})
	}
}
