//go:build integration

package schedule

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	agentdomain "boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	companydomain "boardly.dev/boardly/server/internal/company"
	conversationdomain "boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestFiringWorkerFencesCreatorMembershipBeforeScheduleAndConversation(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	created := createDueSchedule(t, ctx, pool, service, principal, fixture.AgentA)
	materializer := &recordingMaterializer{}
	worker, err := NewFiringWorker(
		FiringWorkerDependencies{
			Store: store, Bus: newScheduleTestBus(t), Materializer: materializer,
			Superseder:  QueuedRunSupersederFunc(supersedeQueuedScheduleRun),
			Invalidator: QueuedRunInvalidatorFunc(invalidateQueuedScheduleRun),
		},
		FiringWorkerOptions{BatchSize: 1},
	)
	if err != nil {
		t.Fatalf("NewFiringWorker() error = %v", err)
	}

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Company lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	var companyID uuid.UUID
	if err := holder.QueryRow(ctx, `
		SELECT id FROM companies WHERE id=$1 FOR UPDATE`, fixture.CompanyA).Scan(&companyID); err != nil {
		t.Fatalf("lock Company: %v", err)
	}
	if _, err := holder.Exec(ctx, `
		UPDATE company_members
		SET status='revoked', revoked_at=clock_timestamp(), revoked_by_user_id=$2,
		    updated_at=clock_timestamp(), version=version+1
		WHERE company_id=$1 AND user_id=$2`, fixture.CompanyA, fixture.UserA); err != nil {
		t.Fatalf("stage creator membership revocation: %v", err)
	}

	type fireResult struct {
		count int
		err   error
	}
	done := make(chan fireResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		count, fireErr := worker.FireOnce(ctx)
		done <- fireResult{count: count, err: fireErr}
	}()
	waitForScheduleProducerFence(t, ctx, pool, finished)

	checker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Schedule lock checker: %v", err)
	}
	var checkedScheduleID, checkedConversationID uuid.UUID
	checkErr := checker.QueryRow(ctx, `
		SELECT id FROM schedules WHERE id=$1 FOR UPDATE NOWAIT`, created.Schedule.ID).
		Scan(&checkedScheduleID)
	if checkErr == nil {
		checkErr = checker.QueryRow(
			ctx, `
			SELECT id FROM conversations WHERE id=$1 FOR UPDATE NOWAIT`,
			created.Schedule.ConversationID,
		).Scan(&checkedConversationID)
	}
	_ = checker.Rollback(context.Background())
	if checkErr != nil {
		t.Fatalf("Firing worker locked Schedule or Conversation before fresh membership: %v", checkErr)
	}
	if checkedScheduleID != created.Schedule.ID ||
		checkedConversationID != created.Schedule.ConversationID {
		t.Fatalf("checked Schedule graph = (%s, %s), want (%s, %s)",
			checkedScheduleID, checkedConversationID,
			created.Schedule.ID, created.Schedule.ConversationID)
	}

	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("commit creator membership revocation: %v", err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.count != 1 {
			t.Fatalf("FireOnce() = %d, %v; want one invalid Schedule", result.count, result.err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for FireOnce(): %v", ctx.Err())
	}
	if len(materializer.snapshot()) != 0 {
		t.Fatalf("revoked creator reached Materializer: %+v", materializer.snapshot())
	}
	if got := countScheduleFirings(t, ctx, pool, created.Schedule.ID); got != 0 {
		t.Fatalf("Schedule firings after creator revocation = %d, want 0", got)
	}
	var status string
	var pauseReason *string
	if err := pool.QueryRow(ctx, `
		SELECT status, pause_reason FROM schedules WHERE id=$1`, created.Schedule.ID).
		Scan(&status, &pauseReason); err != nil {
		t.Fatalf("read invalid Schedule: %v", err)
	}
	if status != string(StatusPaused) || pauseReason == nil || *pauseReason != ReasonCreatorChanged {
		t.Fatalf("invalid Schedule = status %q reason %v, want paused/%q",
			status, pauseReason, ReasonCreatorChanged)
	}
}

// A Schedule whose creator is no longer an enabled, active member pauses
// instead of firing: their machine is the only one it could run on
// (ADR 0014).
func TestFiringWorkerPausesAnInactiveCreatorWithoutMaterializing(t *testing.T) {
	processingModes := []struct {
		name      string
		reconcile bool
	}{
		{name: "drift reconciliation", reconcile: true},
		{name: "due firing"},
	}
	creatorStates := []struct {
		name     string
		mutation string
	}{
		{
			name: "revoked membership",
			mutation: `UPDATE company_members
				SET status='revoked', revoked_at=clock_timestamp(),
				    revoked_by_user_id=$2, updated_at=clock_timestamp(), version=version+1
				WHERE company_id=$1 AND user_id=$2`,
		},
		{
			name: "disabled account",
			mutation: `UPDATE users
				SET disabled_at=clock_timestamp()
				WHERE id=$2
				  AND EXISTS (
				      SELECT 1 FROM company_members
				      WHERE company_id=$1 AND user_id=$2
				  )`,
		},
	}
	for _, mode := range processingModes {
		for _, ownerState := range creatorStates {
			t.Run(mode.name+"/"+ownerState.name, func(t *testing.T) {
				pool, store, fixture := startSchedulePostgres(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				service := newScheduleTestService(t, store)
				principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
				created := createDueSchedule(t, ctx, pool, service, principal, fixture.AgentA)
				// The Schedule fires on its creator's own machine (ADR 0014),
				// so it is the creator leaving or being disabled that stops it.
				dbtest.MustExec(t, ctx, pool, ownerState.mutation, fixture.CompanyA, fixture.UserA)

				materializer := &recordingMaterializer{}
				worker, err := NewFiringWorker(
					FiringWorkerDependencies{
						Store: store, Bus: newScheduleTestBus(t), Materializer: materializer,
						Superseder:  QueuedRunSupersederFunc(supersedeQueuedScheduleRun),
						Invalidator: QueuedRunInvalidatorFunc(invalidateQueuedScheduleRun),
					},
					FiringWorkerOptions{BatchSize: 1},
				)
				if err != nil {
					t.Fatalf("NewFiringWorker() error = %v", err)
				}
				var count int
				if mode.reconcile {
					count, err = worker.ReconcileOnce(ctx)
				} else {
					count, err = worker.FireOnce(ctx)
				}
				if err != nil || count != 1 {
					t.Fatalf("process inactive Runtime owner = %d, %v; want 1, nil", count, err)
				}
				if len(materializer.snapshot()) != 0 {
					t.Fatalf(
						"inactive creator reached Materializer: %+v",
						materializer.snapshot(),
					)
				}
				// Read the row directly: the creator can no longer read their
				// own Schedule through the service, which is the point.
				var status, pauseReason string
				if err := pool.QueryRow(ctx, `
					SELECT status, COALESCE(pause_reason, '') FROM schedules WHERE id = $1`,
					created.Schedule.ID,
				).Scan(&status, &pauseReason); err != nil {
					t.Fatalf("read the paused Schedule: %v", err)
				}
				if status != string(StatusPaused) || pauseReason != string(ReasonCreatorChanged) {
					t.Fatalf(
						"inactive-creator Schedule = %q/%q, want paused/%q",
						status,
						pauseReason,
						ReasonCreatorChanged,
					)
				}
				if got := countScheduleFirings(t, ctx, pool, created.Schedule.ID); got != 0 {
					t.Fatalf("Schedule firings after an inactive creator = %d, want 0", got)
				}
			})
		}
	}
}

// scheduleCreator is a member who is not the fixture owner: an admin with
// her own computer and her own setup of the fixture Agent, who created a due
// Schedule and has its Run queued. It carries the database it lives in.
type scheduleCreator struct {
	pool      *pgxpool.Pool
	store     *db.Store
	fixture   dbtest.Fixture
	principal auth.UserPrincipal
	runtimeID uuid.UUID
	schedule  Response
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
	service := newScheduleTestService(t, store)
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
	creator.schedule = createDueSchedule(t, ctx, pool, service, creator.principal, fixture.AgentA).Schedule
	creator.firingID, creator.runID = seedActiveScheduleRun(
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
// nothing (D31). Removing the member and unbinding the Agent cancel the
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
		wantDisposition  FiringDisposition
		wantFiringReason string
	}{
		{
			name: "the owner removes the creator from the Company",
			lose: func(t *testing.T, ctx context.Context, creator scheduleCreator) {
				t.Helper()
				members, err := companydomain.NewCompanyService(companydomain.ServiceOptions{
					Store: creator.store, Bus: newScheduleTestBus(t),
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
				owner := companyPrincipal(creator.fixture.UserA, creator.fixture.CompanyA)
				if err := members.RemoveMember(ctx, owner, companydomain.MembershipMutation{
					UserID: creator.principal.UserID, ExpectedRevision: revision,
				}); err != nil {
					t.Fatalf("RemoveMember(creator) error = %v", err)
				}
			},
			wantReason:      ReasonCreatorChanged,
			wantRunReason:   "runtime_owner_inactive",
			wantDisposition: FiringDispositionMaterialized,
		},
		{
			name: "the creator unbinds the Agent",
			lose: func(t *testing.T, ctx context.Context, creator scheduleCreator) {
				t.Helper()
				bindings, err := agentdomain.NewBindingService(agentdomain.BindingServiceOptions{
					Store: creator.store, Bus: newScheduleTestBus(t),
				})
				if err != nil {
					t.Fatalf("NewBindingService() error = %v", err)
				}
				if err := bindings.Unbind(ctx, creator.principal, creator.fixture.AgentA); err != nil {
					t.Fatalf("Unbind() error = %v", err)
				}
			},
			wantReason:      ReasonAgentUnavailable,
			wantRunReason:   execution.RunReasonAgentUnbound,
			wantDisposition: FiringDispositionMaterialized,
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
			wantReason:       ReasonCreatorChanged,
			wantRunReason:    execution.CancellationScheduleInvalidated,
			wantDisposition:  FiringDispositionNeedsAttention,
			wantFiringReason: ReasonCreatorChanged,
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
			wantReason:       ReasonAgentUnavailable,
			wantRunReason:    execution.CancellationScheduleInvalidated,
			wantDisposition:  FiringDispositionNeedsAttention,
			wantFiringReason: ReasonAgentUnavailable,
		},
		{
			name: "the creator's runtime is removed while the Run is still queued",
			lose: func(t *testing.T, ctx context.Context, creator scheduleCreator) {
				t.Helper()
				dbtest.MustExec(t, ctx, creator.pool, `
					UPDATE runtimes SET removed_at = now() WHERE id = $1`, creator.runtimeID)
			},
			wantReason:       ReasonAgentUnavailable,
			wantRunReason:    execution.CancellationScheduleInvalidated,
			wantDisposition:  FiringDispositionNeedsAttention,
			wantFiringReason: ReasonAgentUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, store, fixture := startSchedulePostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			creator := seedScheduleCreatorWithQueuedRun(t, ctx, pool, store, fixture)
			tc.lose(t, ctx, creator)

			materializer := &recordingMaterializer{}
			worker, err := NewFiringWorker(
				FiringWorkerDependencies{
					Store: store, Bus: newScheduleTestBus(t), Materializer: materializer,
					Superseder:  QueuedRunSupersederFunc(supersedeQueuedScheduleRun),
					Invalidator: QueuedRunInvalidatorFunc(invalidateQueuedScheduleRun),
				},
				FiringWorkerOptions{BatchSize: 1},
			)
			if err != nil {
				t.Fatalf("NewFiringWorker() error = %v", err)
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
			if status != string(StatusPaused) || pauseReason != tc.wantReason {
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
			if len(materializer.snapshot()) != 0 {
				t.Fatalf("a paused Schedule reached the Materializer: %+v", materializer.snapshot())
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

func TestFiringWorkerMaterializesOneOccurrenceAcrossReplicas(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	connectorID := seedScheduleConnector(t, ctx, pool, fixture.CompanyA, "codex", "gmail")
	service := newScheduleTestService(t, store)
	runAt := time.Now().UTC().Truncate(time.Minute).Add(10 * time.Minute)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	createInput := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		Name: "One shot", Instructions: "Summarize Gmail.", Status: StatusActive,
		Trigger: Trigger{Type: TriggerTypeOnce, RunAt: &runAt}, ConnectorIDs: []uuid.UUID{connectorID},
	}
	createInput.ReviewDigest = reviewScheduleInput(t, service, principal, createInput)
	created, err := service.Create(ctx, principal, createInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		WITH due AS (
			SELECT date_trunc('minute', clock_timestamp()-interval '1 minute') AS value
		)
		UPDATE schedules SET run_at=due.value, next_occurrence_at=due.value
		FROM due WHERE id=$1`, created.Schedule.ID)

	materializer := &recordingMaterializer{}
	bus := newScheduleTestBus(t)
	workerA, err := NewFiringWorker(
		FiringWorkerDependencies{
			Store: store, Bus: bus, Materializer: materializer,
			Superseder:  QueuedRunSupersederFunc(supersedeQueuedScheduleRun),
			Invalidator: QueuedRunInvalidatorFunc(invalidateQueuedScheduleRun),
		},
		FiringWorkerOptions{BatchSize: 1},
	)
	if err != nil {
		t.Fatalf("NewFiringWorker(A) error = %v", err)
	}
	workerB, err := NewFiringWorker(
		FiringWorkerDependencies{
			Store: store, Bus: bus, Materializer: materializer,
			Superseder:  QueuedRunSupersederFunc(supersedeQueuedScheduleRun),
			Invalidator: QueuedRunInvalidatorFunc(invalidateQueuedScheduleRun),
		},
		FiringWorkerOptions{BatchSize: 1},
	)
	if err != nil {
		t.Fatalf("NewFiringWorker(B) error = %v", err)
	}

	results := make(chan int, 2)
	errorsChannel := make(chan error, 2)
	var workers sync.WaitGroup
	for _, worker := range []*FiringWorker{workerA, workerB} {
		workers.Add(1)
		go func(candidate *FiringWorker) {
			defer workers.Done()
			count, fireErr := candidate.FireOnce(ctx)
			results <- count
			errorsChannel <- fireErr
		}(worker)
	}
	workers.Wait()
	close(results)
	close(errorsChannel)
	for fireErr := range errorsChannel {
		if fireErr != nil {
			t.Fatalf("FireOnce() error = %v", fireErr)
		}
	}
	total := 0
	for count := range results {
		total += count
	}
	if total != 1 {
		t.Fatalf("replica materialization count = %d, want 1", total)
	}
	inputs := materializer.snapshot()
	if len(inputs) != 1 || inputs[0].ScheduleID != created.Schedule.ID ||
		!inputs[0].ForceFreshSession || inputs[0].Provider != "codex" {
		t.Fatalf("Materializer inputs = %+v", inputs)
	}
	// The linked Run carries the Schedule's reviewed sources as its exact scope.
	var mode string
	var selected []uuid.UUID
	var revisions []int64
	if err := pool.QueryRow(ctx, `
		SELECT runs.connector_selection_mode,
			array_agg(selection.connector_id ORDER BY selection.connector_id),
			array_agg(selection.policy_revision ORDER BY selection.connector_id)
		FROM schedule_firings AS firing
		JOIN runs ON runs.id = firing.run_id
		JOIN run_connector_selections AS selection ON selection.run_id = runs.id
		WHERE firing.schedule_id = $1
		GROUP BY runs.connector_selection_mode`, created.Schedule.ID).Scan(&mode, &selected, &revisions); err != nil {
		t.Fatalf("read the Run's Connector scope: %v", err)
	}
	if mode != "exact" || len(selected) != 1 || selected[0] != connectorID || revisions[0] != 1 {
		t.Fatalf("Run Connector scope = %s %v %v", mode, selected, revisions)
	}

	stored, err := service.Get(ctx, companyPrincipal(fixture.UserA, fixture.CompanyA), created.Schedule.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status != StatusCompleted || stored.NextOccurrenceAt != nil || stored.LastOccurrenceAt == nil {
		t.Fatalf("completed one-shot Schedule = %+v", stored)
	}
	var firingCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schedule_firings WHERE schedule_id=$1`, created.Schedule.ID).
		Scan(&firingCount); err != nil || firingCount != 1 {
		t.Fatalf("firing count = %d, %v", firingCount, err)
	}
}

// driftScheduleAgentProvider rebuilds the fixture Agent on the other
// provider: the binding goes, the template's provider changes, and the
// member sets it up again on a Claude machine of their own. A Schedule
// frozen against the Codex template now names a provider the Agent no longer
// has — the only shape provider drift can still take, since a binding's
// composite foreign keys make a cross-provider row unrepresentable
// (ADR 0014).
func driftScheduleAgentProvider(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
) {
	t.Helper()
	dbtest.MustExec(t, ctx, pool,
		`DELETE FROM agent_runtime_bindings WHERE company_id = $1 AND agent_id = $2`,
		fixture.CompanyA, fixture.AgentA)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE agents SET provider = 'claude' WHERE id = $1`, fixture.AgentA)
	claudeRuntime := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (
			id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at
		) VALUES ($1, $2, 'daemon-a', 'Claude on daemon-a', 'claude', 'online', $3, now())`,
		claudeRuntime, fixture.CompanyA, fixture.UserA)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA,
		fixture.UserA, claudeRuntime, "claude")
}

func TestFiringWorkerPausesProviderDriftWithoutMaterializing(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	runAt := time.Now().UTC().Truncate(time.Minute).Add(10 * time.Minute)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	createInput := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		Name: "Provider drift", Instructions: "Run.", Status: StatusActive,
		Trigger: Trigger{Type: TriggerTypeOnce, RunAt: &runAt}, ConnectorIDs: []uuid.UUID{},
	}
	createInput.ReviewDigest = reviewScheduleInput(t, service, principal, createInput)
	created, err := service.Create(ctx, principal, createInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	driftScheduleAgentProvider(t, ctx, pool, fixture)
	materializer := &recordingMaterializer{}
	bus := newScheduleTestBus(t)
	var hints []events.Event
	if err := bus.Subscribe(EventChanged, func(ctx context.Context, event events.Event) error {
		hints = append(hints, event)
		stored, err := service.Get(ctx, principal, created.Schedule.ID)
		if err != nil || stored.Status != StatusPaused {
			t.Errorf("drift event preceded commit: %+v, %v", stored, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewFiringWorker(
		FiringWorkerDependencies{
			Store: store, Bus: bus, Materializer: materializer,
			Superseder:  QueuedRunSupersederFunc(supersedeQueuedScheduleRun),
			Invalidator: QueuedRunInvalidatorFunc(invalidateQueuedScheduleRun),
		},
		FiringWorkerOptions{BatchSize: 1},
	)
	if err != nil {
		t.Fatalf("NewFiringWorker() error = %v", err)
	}
	if count, err := worker.ReconcileOnce(ctx); err != nil || count != 1 {
		t.Fatalf("ReconcileOnce() = %d, %v", count, err)
	}
	if len(materializer.snapshot()) != 0 {
		t.Fatalf("provider drift reached Materializer: %+v", materializer.snapshot())
	}
	stored, err := service.Get(ctx, companyPrincipal(fixture.UserA, fixture.CompanyA), created.Schedule.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status != StatusPaused || stored.PauseReason == nil || *stored.PauseReason != ReasonProviderChanged {
		t.Fatalf("provider-drift Schedule = %+v", stored)
	}
	if len(hints) != 1 {
		t.Fatalf("provider drift emitted %d hints", len(hints))
	}
}

func TestFiringWorkerCancelsQueuedRunWhenProviderDrifts(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	created := createDueSchedule(t, ctx, pool, service, principal, fixture.AgentA)
	firingID, runID := seedActiveScheduleRun(t, ctx, pool, service, principal, created.Schedule, "queued")
	driftScheduleAgentProvider(t, ctx, pool, fixture)
	worker, err := NewFiringWorker(
		FiringWorkerDependencies{
			Store: store, Bus: newScheduleTestBus(t), Materializer: &recordingMaterializer{},
			Superseder:  QueuedRunSupersederFunc(supersedeQueuedScheduleRun),
			Invalidator: QueuedRunInvalidatorFunc(invalidateQueuedScheduleRun),
		},
		FiringWorkerOptions{BatchSize: 1},
	)
	if err != nil {
		t.Fatalf("NewFiringWorker() error = %v", err)
	}
	if count, err := worker.ReconcileOnce(ctx); err != nil || count != 1 {
		t.Fatalf("ReconcileOnce() = %d, %v", count, err)
	}
	var runStatus, cancellationReason, disposition, firingReason string
	if err := pool.QueryRow(
		ctx,
		`SELECT status, error FROM runs WHERE id=$1`,
		runID,
	).Scan(&runStatus, &cancellationReason); err != nil {
		t.Fatalf("read invalidated Schedule Run: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT disposition, reason FROM schedule_firings WHERE id=$1`,
		firingID,
	).Scan(&disposition, &firingReason); err != nil {
		t.Fatalf("read invalidated Schedule firing: %v", err)
	}
	if runStatus != "cancelled" || cancellationReason != execution.CancellationScheduleInvalidated ||
		disposition != "needs_attention" || firingReason != ReasonProviderChanged {
		t.Fatalf(
			"drifted Run/firing = %q/%q and %q/%q",
			runStatus,
			cancellationReason,
			disposition,
			firingReason,
		)
	}
}

func TestFiringWorkerSupersedesQueuedRunAndMaterializesLatestOccurrence(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	created := createDueSchedule(t, ctx, pool, service, principal, fixture.AgentA)
	previousFiringID, previousRunID := seedActiveScheduleRun(
		t, ctx, pool, service, principal, created.Schedule, "queued",
	)
	materializer := &recordingMaterializer{}
	worker, err := NewFiringWorker(
		FiringWorkerDependencies{
			Store: store, Bus: newScheduleTestBus(t), Materializer: materializer,
			Superseder:  QueuedRunSupersederFunc(supersedeQueuedScheduleRun),
			Invalidator: QueuedRunInvalidatorFunc(invalidateQueuedScheduleRun),
		},
		FiringWorkerOptions{BatchSize: 1},
	)
	if err != nil {
		t.Fatalf("NewFiringWorker() error = %v", err)
	}
	if count, err := worker.FireOnce(ctx); err != nil || count != 1 {
		t.Fatalf("FireOnce() = %d, %v", count, err)
	}
	inputs := materializer.snapshot()
	if len(inputs) != 1 || inputs[0].ScheduleID != created.Schedule.ID {
		t.Fatalf("Materializer inputs = %+v", inputs)
	}
	var runStatus, cancellationReason, firingDisposition string
	if err := pool.QueryRow(
		ctx,
		`SELECT status, error FROM runs WHERE id=$1`,
		previousRunID,
	).Scan(&runStatus, &cancellationReason); err != nil {
		t.Fatalf("read superseded Run: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT disposition FROM schedule_firings WHERE id=$1`, previousFiringID).
		Scan(&firingDisposition); err != nil {
		t.Fatalf("read superseded firing: %v", err)
	}
	if runStatus != "cancelled" || cancellationReason != execution.CancellationScheduleSuperseded ||
		firingDisposition != "superseded" {
		t.Fatalf("superseded state = Run %q/%q, firing %q",
			runStatus, cancellationReason, firingDisposition)
	}
}

func TestFiringWorkerSkipsOccurrenceWhileRunIsRunning(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	created := createDueSchedule(t, ctx, pool, service, principal, fixture.AgentA)
	activeFiringID, _ := seedActiveScheduleRun(
		t, ctx, pool, service, principal, created.Schedule, "running",
	)
	materializer := &recordingMaterializer{}
	worker, err := NewFiringWorker(
		FiringWorkerDependencies{
			Store: store, Bus: newScheduleTestBus(t), Materializer: materializer,
			Superseder:  QueuedRunSupersederFunc(supersedeQueuedScheduleRun),
			Invalidator: QueuedRunInvalidatorFunc(invalidateQueuedScheduleRun),
		},
		FiringWorkerOptions{BatchSize: 1},
	)
	if err != nil {
		t.Fatalf("NewFiringWorker() error = %v", err)
	}
	if count, err := worker.FireOnce(ctx); err != nil || count != 1 {
		t.Fatalf("FireOnce() = %d, %v", count, err)
	}
	if len(materializer.snapshot()) != 0 {
		t.Fatalf("running overlap reached Materializer: %+v", materializer.snapshot())
	}
	var disposition, reason string
	if err := pool.QueryRow(ctx, `
		SELECT disposition, reason FROM schedule_firings
		WHERE schedule_id=$1 AND id<>$2`, created.Schedule.ID, activeFiringID).
		Scan(&disposition, &reason); err != nil {
		t.Fatalf("read skipped firing: %v", err)
	}
	if disposition != "skipped" || reason != ReasonRunActive {
		t.Fatalf("current firing = %q/%q, want skipped/%s", disposition, reason, ReasonRunActive)
	}
}

func TestServiceRunNowRejectsAnActiveScheduleRun(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	created := createDueSchedule(t, ctx, pool, service, principal, fixture.AgentA)
	seedActiveScheduleRun(t, ctx, pool, service, principal, created.Schedule, "queued")
	_, err := service.RunNow(ctx, principal, created.Schedule.ID, RunNowInput{
		RequestKey: dbtest.NewUUID(t),
	})
	if !errors.Is(err, ErrRunActive) {
		t.Fatalf("RunNow(active Run) error = %v, want %v", err, ErrRunActive)
	}
	if count := countScheduleFirings(t, ctx, pool, created.Schedule.ID); count != 1 {
		t.Fatalf("RunNow(active Run) firing count = %d, want unchanged 1", count)
	}
}

func TestServiceRunNowRejectsPausedScheduleWithoutCreatingFiring(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	created := createDueSchedule(t, ctx, pool, service, principal, fixture.AgentA)
	paused, err := service.Pause(ctx, principal, created.Schedule.ID, RevisionInput{
		ExpectedRevision: created.Schedule.Revision,
	})
	if err != nil || paused.Schedule.Status != StatusPaused {
		t.Fatalf("Pause() = %+v, %v", paused, err)
	}
	_, err = service.RunNow(ctx, principal, created.Schedule.ID, RunNowInput{
		RequestKey: dbtest.NewUUID(t),
	})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("RunNow(paused) error = %v, want %v", err, ErrInvalidState)
	}
	if count := countScheduleFirings(t, ctx, pool, created.Schedule.ID); count != 0 {
		t.Fatalf("RunNow(paused) firing count = %d, want 0", count)
	}
}

func TestFiringWorkerPausesMaterializationFailureWithoutStarvingLaterSchedule(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	createDueSchedule(t, ctx, pool, service, principal, fixture.AgentA)
	createDueSchedule(t, ctx, pool, service, principal, fixture.AgentA)
	materializer := &failFirstMaterializer{}
	worker, err := NewFiringWorker(
		FiringWorkerDependencies{
			Store: store, Bus: newScheduleTestBus(t), Materializer: materializer,
			Superseder:  QueuedRunSupersederFunc(supersedeQueuedScheduleRun),
			Invalidator: QueuedRunInvalidatorFunc(invalidateQueuedScheduleRun),
		},
		FiringWorkerOptions{BatchSize: 2},
	)
	if err != nil {
		t.Fatalf("NewFiringWorker() error = %v", err)
	}
	if count, err := worker.FireOnce(ctx); err != nil || count != 2 {
		t.Fatalf("FireOnce() = %d, %v, want 2, nil", count, err)
	}
	var paused, completed, needsAttention, materialized int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status='paused'),
			count(*) FILTER (WHERE status='completed')
		FROM schedules`).Scan(&paused, &completed); err != nil {
		t.Fatalf("read Schedule materialization outcomes: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE disposition='needs_attention' AND reason='materialization_blocked'),
			count(*) FILTER (WHERE disposition='materialized')
		FROM schedule_firings`).Scan(&needsAttention, &materialized); err != nil {
		t.Fatalf("read firing materialization outcomes: %v", err)
	}
	if paused != 1 || completed != 1 || needsAttention != 1 || materialized != 1 {
		t.Fatalf(
			"Schedule/firing outcomes = paused %d completed %d needs_attention %d materialized %d",
			paused,
			completed,
			needsAttention,
			materialized,
		)
	}
}

type recordingMaterializer struct {
	mu     sync.Mutex
	inputs []MaterializeInput
}

type failFirstMaterializer struct {
	calls int
}

func (m *failFirstMaterializer) MaterializeScheduleRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input MaterializeInput,
) (MaterializeResult, error) {
	m.calls++
	if m.calls == 1 {
		return MaterializeResult{}, errors.New("materializer unavailable")
	}
	return materializedScheduleRun(ctx, queries, pending, input)
}

func (m *recordingMaterializer) MaterializeScheduleRunInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input MaterializeInput,
) (MaterializeResult, error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, input)
	m.mu.Unlock()
	return materializedScheduleRun(ctx, queries, pending, input)
}

func (m *recordingMaterializer) snapshot() []MaterializeInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MaterializeInput(nil), m.inputs...)
}

func invalidateQueuedScheduleRun(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input InvalidateQueuedRunInput,
) error {
	return execution.InvalidateScheduleRunInTx(
		ctx,
		queries,
		pending,
		execution.ScheduleRunCancellationInput{
			FiringID: input.FiringID, RunID: input.RunID, ScheduleID: input.ScheduleID,
			CompanyID: input.CompanyID, ConversationID: input.ConversationID,
		},
	)
}

func supersedeQueuedScheduleRun(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input SupersedeQueuedRunInput,
) error {
	return execution.SupersedeScheduleRunInTx(
		ctx,
		queries,
		pending,
		execution.SupersedeScheduleRunInput{
			FiringID: input.FiringID, RunID: input.RunID, ScheduleID: input.ScheduleID,
			CompanyID: input.CompanyID, ConversationID: input.ConversationID,
		},
	)
}

func newScheduleTestService(t *testing.T, store *db.Store) *DefaultScheduleService {
	t.Helper()
	service, err := NewScheduleService(ServiceOptions{
		Store: store, Bus: newScheduleTestBus(t),
		ConversationUpdater: ConversationUpdaterFunc(func(
			ctx context.Context,
			queries *generated.Queries,
			pending *[]events.Event,
			input UpdateConversationInput,
		) error {
			return conversationdomain.UpdateScheduleConversationInTx(
				ctx,
				queries, pending,
				conversationdomain.UpdateScheduleConversationInput{
					ID: input.ID, ScheduleID: input.ScheduleID, CompanyID: input.CompanyID,
					CreatorUserID: input.CreatorUserID, AgentID: input.AgentID, Title: input.Title,
				},
			)
		}),
		Materializer: MaterializerFunc(materializedScheduleRun),
	})
	if err != nil {
		t.Fatalf("NewScheduleService() error = %v", err)
	}
	return service
}

func countScheduleFirings(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	scheduleID uuid.UUID,
) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schedule_firings WHERE schedule_id=$1`, scheduleID).
		Scan(&count); err != nil {
		t.Fatalf("count Schedule firings: %v", err)
	}
	return count
}

func createDueSchedule(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	service *DefaultScheduleService,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
) CreateResult {
	t.Helper()
	runAt := time.Now().UTC().Truncate(time.Minute).Add(10 * time.Minute)
	input := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: agentID,
		Name: "Overlap", Instructions: "Run latest.", Status: StatusActive,
		Trigger: Trigger{Type: TriggerTypeOnce, RunAt: &runAt}, ConnectorIDs: []uuid.UUID{},
	}
	input.ReviewDigest = reviewScheduleInput(t, service, principal, input)
	created, err := service.Create(ctx, principal, input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		WITH due AS (
			SELECT date_trunc('minute', clock_timestamp()-interval '1 minute') AS value
		)
		UPDATE schedules SET run_at=due.value, next_occurrence_at=due.value
		FROM due WHERE id=$1`, created.Schedule.ID)
	return created
}

func seedActiveScheduleRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	service *DefaultScheduleService,
	principal auth.UserPrincipal,
	schedule Response,
	status string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	result, err := service.RunNow(ctx, principal, schedule.ID, RunNowInput{
		RequestKey: dbtest.NewUUID(t),
	})
	if err != nil {
		t.Fatalf("RunNow(seed active Run) error = %v", err)
	}
	if result.Firing.RunID == nil {
		t.Fatalf("RunNow(seed active Run) = %+v, want Run id", result)
	}
	dbtest.MustExec(t, ctx, pool, `
		UPDATE schedule_firings
		SET occurrence_at=clock_timestamp()-interval '2 minutes'
		WHERE id=$1`, result.Firing.ID)
	if status != "queued" {
		dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status=$2 WHERE id=$1`, *result.Firing.RunID, status)
	}
	return result.Firing.ID, *result.Firing.RunID
}
