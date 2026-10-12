//go:build integration

package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const mergeIntegrationGeneration int64 = 7

type mergeIntegrationHarness struct {
	ctx           context.Context
	pool          *pgxpool.Pool
	store         *db.Store
	bus           *events.Bus
	mergeService  *DefaultMergeService
	taskService   *DefaultTaskService
	fixture       dbtest.Fixture
	principal     auth.UserPrincipal
	workerAgentID uuid.UUID
	sourceRunID   uuid.UUID
	jobID         uuid.UUID
}

func TestMergeServiceManualClaimFencesExpectedRevisionAndExecutionGeneration(t *testing.T) {
	h := newMergeIntegrationHarness(t)
	h.disableProactiveMode(t)
	current, err := h.taskService.Get(h.ctx, h.principal, h.fixture.TaskA, nil)
	if err != nil {
		t.Fatalf("Get(Task before manual merge) error = %v", err)
	}
	input := ManualMergeClaimInput{
		TaskID:                      h.fixture.TaskA,
		RunID:                       h.sourceRunID,
		ExpectedRevision:            current.Revision,
		ExpectedExecutionGeneration: mergeIntegrationGeneration,
	}

	staleRevision := input
	staleRevision.ExpectedRevision--
	if _, err := h.mergeService.ClaimManual(
		h.ctx, h.principal, h.fixture.CompanyA, h.fixture.RuntimeA, h.fixture.DaemonA, staleRevision,
	); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("ClaimManual(stale revision) error = %v, want %v", err, ErrMergeConflict)
	}
	staleGeneration := input
	staleGeneration.ExpectedExecutionGeneration--
	if _, err := h.mergeService.ClaimManual(
		h.ctx, h.principal, h.fixture.CompanyA, h.fixture.RuntimeA, h.fixture.DaemonA, staleGeneration,
	); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("ClaimManual(stale generation) error = %v, want %v", err, ErrMergeConflict)
	}

	claim, err := h.mergeService.ClaimManual(
		h.ctx, h.principal, h.fixture.CompanyA, h.fixture.RuntimeA, h.fixture.DaemonA, input,
	)
	if err != nil {
		t.Fatalf("ClaimManual() error = %v", err)
	}
	if claim.TaskID != input.TaskID || claim.RunID != input.RunID ||
		claim.TaskExecutionGeneration != input.ExpectedExecutionGeneration || claim.Attempt != 1 {
		t.Fatalf("ClaimManual() = %+v", claim)
	}
	replayed, err := h.mergeService.ClaimManual(
		h.ctx,
		h.principal,
		h.fixture.CompanyA,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		input,
	)
	if err != nil {
		t.Fatalf("ClaimManual(exact live replay) error = %v", err)
	}
	if replayed.JobID != claim.JobID || replayed.Attempt != claim.Attempt {
		t.Fatalf(
			"ClaimManual(exact live replay) = %+v, want job %s attempt %d",
			replayed,
			claim.JobID,
			claim.Attempt,
		)
	}

	var (
		requestType string
		jobStatus   string
		actorUserID uuid.UUID
	)
	if err := h.pool.QueryRow(
		h.ctx, `
		SELECT request_type, status, authority_user_id
		FROM task_merge_jobs
		WHERE id=$1`, claim.JobID,
	).Scan(&requestType, &jobStatus, &actorUserID); err != nil {
		t.Fatalf("read manual Task merge claim: %v", err)
	}
	if requestType != "manual" || jobStatus != "executing" || actorUserID != h.principal.UserID {
		t.Fatalf(
			"manual Task merge claim = type %q status %q actor %s",
			requestType,
			jobStatus,
			actorUserID,
		)
	}
	if _, _, err := h.enqueueFreshTaskRun(); !errors.Is(err, execution.ErrTaskMergeInProgress) {
		t.Fatalf(
			"enqueue while manual merge executes error = %v, want %v",
			err,
			execution.ErrTaskMergeInProgress,
		)
	}

	// The merged result moves the Task to done, which resolves its dependents:
	// each one gets its own task.updated frame at an advanced revision.
	dependentID := seedDependentTask(t, h.ctx, h.pool, h.fixture, "merge_dependent", h.fixture.TaskA)
	var dependentUpdates []Response
	if err := h.bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		if event.Name == EventUpdated && event.TaskID == dependentID {
			dependentUpdates = append(dependentUpdates, event.Payload.(Response))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.mergeService.RecordResult(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		claim.JobID,
		MergeResultInput{
			Attempt: claim.Attempt, Status: MergeResultMerged, MergedCommit: strings.Repeat("c", 40),
		},
	); err != nil {
		t.Fatalf("RecordResult(manual merged) error = %v", err)
	}
	var taskStatus string
	if err := h.pool.QueryRow(
		h.ctx, `SELECT status FROM tasks WHERE id=$1`, h.fixture.TaskA,
	).Scan(&taskStatus); err != nil {
		t.Fatalf("read manually merged Task: %v", err)
	}
	if taskStatus != string(StatusDone) {
		t.Fatalf("manually merged Task status = %q, want %q", taskStatus, StatusDone)
	}
	if len(dependentUpdates) != 1 || !dependentUpdates[0].DependenciesResolved ||
		dependentUpdates[0].Revision != 2 {
		t.Fatalf("dependent updates after merge = %+v, want one resolved at revision 2", dependentUpdates)
	}
}

func TestMergeServiceManualClaimRequiresLatestCompletedRun(t *testing.T) {
	h := newMergeIntegrationHarness(t)
	h.disableProactiveMode(t)
	current, err := h.taskService.Get(h.ctx, h.principal, h.fixture.TaskA, nil)
	if err != nil {
		t.Fatalf("Get(Task before manual merge) error = %v", err)
	}
	dbtest.MustExec(t, h.ctx, h.pool, `
		UPDATE runs
		SET updated_at=clock_timestamp() - interval '1 second'
		WHERE id=$1`, h.sourceRunID)
	var newerRunID uuid.UUID
	if err := h.pool.QueryRow(
		h.ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, context, task_execution_generation, updated_at
		, run_type) VALUES ($1,$2,$3,$4,$5,'completed',$6,$7,clock_timestamp(), (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		h.fixture.CompanyA,
		h.workerAgentID,
		h.fixture.TaskA,
		h.fixture.ConversationA,
		h.fixture.RuntimeA,
		[]byte(`{"executionProfile":{"task":{"key":"ENG-1"}}}`),
		mergeIntegrationGeneration,
	).Scan(&newerRunID); err != nil {
		t.Fatalf("insert newer completed Task Run: %v", err)
	}

	input := ManualMergeClaimInput{
		TaskID:                      h.fixture.TaskA,
		RunID:                       h.sourceRunID,
		ExpectedRevision:            current.Revision,
		ExpectedExecutionGeneration: mergeIntegrationGeneration,
	}
	if _, err := h.mergeService.ClaimManual(
		h.ctx, h.principal, h.fixture.CompanyA, h.fixture.RuntimeA, h.fixture.DaemonA, input,
	); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("ClaimManual(older completed Run) error = %v, want %v", err, ErrMergeConflict)
	}

	input.RunID = newerRunID
	claim, err := h.mergeService.ClaimManual(
		h.ctx, h.principal, h.fixture.CompanyA, h.fixture.RuntimeA, h.fixture.DaemonA, input,
	)
	if err != nil {
		t.Fatalf("ClaimManual(latest completed Run) error = %v", err)
	}
	if claim.RunID != newerRunID || claim.JobID != h.jobID {
		t.Fatalf("ClaimManual(latest completed Run) = %+v", claim)
	}
}

func TestProactiveMergeWaitsForTaskHumanInputContinuation(t *testing.T) {
	for _, requestType := range []string{"Approval", "Question"} {
		t.Run(requestType, func(t *testing.T) {
			h := newMergeIntegrationHarness(t)

			claimTx, err := h.pool.BeginTx(h.ctx, pgx.TxOptions{})
			if err != nil {
				t.Fatalf("begin pre-request claim transaction: %v", err)
			}
			t.Cleanup(func() { _ = claimTx.Rollback(context.WithoutCancel(h.ctx)) })
			claimQueries := generated.New(claimTx)
			candidate, err := claimQueries.GetNextTaskMergeJobClaimCandidate(
				h.ctx,
				mergeClaimCandidateParams(h.principal.UserID, h.fixture.RuntimeA, h.fixture.DaemonA),
			)
			if err != nil {
				t.Fatalf("read pre-request merge candidate: %v", err)
			}
			requestID := seedPendingMergeHumanInput(t, h, requestType)
			if err := lockMergeAuthority(
				h.ctx,
				claimQueries,
				h.principal.UserID,
				candidate.CompanyID,
			); err != nil {
				t.Fatalf("lock merge authority after human-input request: %v", err)
			}
			if _, err := claimQueries.GetTaskForCompanyByIDForUpdate(
				h.ctx,
				generated.GetTaskForCompanyByIDForUpdateParams{
					CompanyID: candidate.CompanyID,
					TaskID:    candidate.TaskID,
				},
			); err != nil {
				t.Fatalf("lock Task after human-input request: %v", err)
			}
			_, err = claimQueries.ClaimTaskMergeJob(
				h.ctx,
				mergeClaimParams(candidate.JobID, h.principal.UserID, h.fixture.RuntimeA, h.fixture.DaemonA),
			)
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("claim after pending %s error = %v, want %v", requestType, err, pgx.ErrNoRows)
			}
			if err := claimTx.Rollback(h.ctx); err != nil {
				t.Fatalf("roll back pre-request claim transaction: %v", err)
			}

			assertProactiveMergeBlockedByHumanInput(t, h, requestType+" decision")
			resolveMergeHumanInput(t, h, requestType, requestID)
			assertProactiveMergeBlockedByHumanInput(t, h, requestType+" continuation")

			deliverMergeHumanInput(t, h, requestType, requestID)
			dbtest.MustExec(
				t,
				h.ctx,
				h.pool,
				`DELETE FROM task_merge_jobs WHERE task_id=$1`,
				h.fixture.TaskA,
			)
			job, err := h.store.Queries().InsertProactiveTaskMergeJobForRun(
				h.ctx,
				generated.InsertProactiveTaskMergeJobForRunParams{
					ActorRoles: auth.RolesWith(auth.PermissionCompanyProactiveModeManage),
					RunID:      h.sourceRunID,
				},
			)
			if err != nil {
				t.Fatalf("insert merge job after delivered %s continuation: %v", requestType, err)
			}
			claim, err := h.mergeService.Claim(
				h.ctx,
				h.principal,
				h.fixture.RuntimeA,
				h.fixture.DaemonA,
			)
			if err != nil {
				t.Fatalf("claim merge after delivered %s continuation: %v", requestType, err)
			}
			if claim == nil || claim.JobID != job.ID {
				t.Fatalf(
					"claim after delivered %s continuation = %+v, want job %s",
					requestType,
					claim,
					job.ID,
				)
			}
		})
	}
}

func TestMergeServiceCompletesClaimAfterProactiveModeIsDisabledAndReplaysExactly(t *testing.T) {
	h := newMergeIntegrationHarness(t)
	claim := h.claim(t)
	if claim.JobID != h.jobID {
		t.Fatalf("Claim().JobID = %s, want %s", claim.JobID, h.jobID)
	}
	if claim.CompanyID != h.fixture.CompanyA {
		t.Fatalf("Claim().CompanyID = %s, want %s", claim.CompanyID, h.fixture.CompanyA)
	}
	if claim.TaskID != h.fixture.TaskA {
		t.Fatalf("Claim().TaskID = %s, want %s", claim.TaskID, h.fixture.TaskA)
	}
	if claim.RunID != h.sourceRunID {
		t.Fatalf("Claim().RunID = %d, want %d", claim.RunID, h.sourceRunID)
	}
	if claim.Attempt != 1 {
		t.Fatalf("Claim().Attempt = %d, want 1", claim.Attempt)
	}
	if claim.ConversationID != h.fixture.ConversationA {
		t.Fatalf(
			"Claim().ConversationID = %s, want %s",
			claim.ConversationID,
			h.fixture.ConversationA,
		)
	}
	if !json.Valid(claim.Context) {
		t.Fatalf("Claim().Context = %q, want valid JSON", claim.Context)
	}

	var recorded []events.Event
	if err := h.bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		recorded = append(recorded, event)
		return nil
	}); err != nil {
		t.Fatalf("subscribe to Task events: %v", err)
	}
	h.disableProactiveMode(t)

	result := MergeResultInput{
		Attempt:      claim.Attempt,
		Status:       MergeResultMerged,
		MergedCommit: strings.Repeat("a", 40),
	}
	if err := h.mergeService.RecordResult(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		result,
	); err != nil {
		t.Fatalf("RecordResult(merged after disable) error = %v", err)
	}

	var (
		jobStatus      string
		resultCode     pgtype.Text
		mergedCommit   pgtype.Text
		leaseExpiresAt pgtype.Timestamptz
		jobCompletedAt pgtype.Timestamptz
	)
	if err := h.pool.QueryRow(
		h.ctx, `
		SELECT status, result_code, merged_commit, lease_expires_at, completed_at
		FROM task_merge_jobs
		WHERE id=$1`, h.jobID,
	).Scan(&jobStatus, &resultCode, &mergedCommit, &leaseExpiresAt, &jobCompletedAt); err != nil {
		t.Fatalf("read completed Task merge job: %v", err)
	}
	if jobStatus != "succeeded" || resultCode.Valid ||
		!mergedCommit.Valid || mergedCommit.String != result.MergedCommit ||
		leaseExpiresAt.Valid || !jobCompletedAt.Valid {
		t.Fatalf(
			"completed Task merge job = status %q code %v commit %v lease %v completed %v",
			jobStatus,
			resultCode,
			mergedCommit,
			leaseExpiresAt,
			jobCompletedAt,
		)
	}

	var (
		taskStatus      string
		taskGeneration  int64
		taskCompletedAt pgtype.Timestamptz
	)
	if err := h.pool.QueryRow(
		h.ctx, `
		SELECT status, execution_generation, completed_at
		FROM tasks
		WHERE id=$1`, h.fixture.TaskA,
	).Scan(&taskStatus, &taskGeneration, &taskCompletedAt); err != nil {
		t.Fatalf("read merged Task: %v", err)
	}
	if taskStatus != string(StatusDone) || taskGeneration != mergeIntegrationGeneration ||
		!taskCompletedAt.Valid {
		t.Fatalf(
			"merged Task = status %q generation %d completed %v",
			taskStatus,
			taskGeneration,
			taskCompletedAt,
		)
	}

	var (
		completionSource string
		sourceAgentID    uuid.UUID
		recipientAgentID uuid.UUID
	)
	if err := h.pool.QueryRow(
		h.ctx, `
		SELECT completion_source, source_agent_id, recipient_agent_id
		FROM task_report_events
		WHERE task_id=$1 AND batch_id IS NULL AND superseded_at IS NULL`, h.fixture.TaskA,
	).Scan(&completionSource, &sourceAgentID, &recipientAgentID); err != nil {
		t.Fatalf("read merged Task outcome report: %v", err)
	}
	reportCount := countMergeIntegrationRows(t, h.ctx, h.pool, `
		SELECT count(*)
		FROM task_report_events
		WHERE task_id=$1 AND batch_id IS NULL AND superseded_at IS NULL`, h.fixture.TaskA)
	if reportCount != 1 || completionSource != reportSourceReviewAccepted ||
		sourceAgentID != h.workerAgentID || recipientAgentID != h.fixture.AgentA {
		t.Fatalf(
			"merged Task reports = count %d source %q Agent %s recipient %s",
			reportCount,
			completionSource,
			sourceAgentID,
			recipientAgentID,
		)
	}
	if countMergeIntegrationEvents(recorded, EventUpdated) != 1 ||
		countMergeIntegrationEvents(recorded, EventReportReady) != 1 {
		t.Fatalf("RecordResult Task events = %+v", recorded)
	}

	if err := h.mergeService.RecordResult(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		result,
	); err != nil {
		t.Fatalf("RecordResult(exact replay) error = %v", err)
	}
	if countMergeIntegrationRows(t, h.ctx, h.pool, `
		SELECT count(*) FROM task_report_events WHERE task_id=$1`, h.fixture.TaskA) != 1 {
		t.Fatal("exact merge-result replay duplicated the Task outcome report")
	}
	if countMergeIntegrationEvents(recorded, EventUpdated) != 1 ||
		countMergeIntegrationEvents(recorded, EventReportReady) != 1 {
		t.Fatalf("exact merge-result replay republished Task events = %+v", recorded)
	}

	conflicting := result
	conflicting.MergedCommit = strings.Repeat("b", 40)
	if err := h.mergeService.RecordResult(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		conflicting,
	); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("RecordResult(conflicting replay) error = %v, want %v", err, ErrMergeConflict)
	}
}

// Merging a Task's work into its Project runs that Task (ADR 0017): a
// daemon whose member lost project.tasks.execute there claims neither the
// Proactive merge job nor a manual acceptance, and gets both back once the
// grant returns.
func TestMergeClaimNeedsTheDaemonMembersExecutePermission(t *testing.T) {
	h := newMergeIntegrationHarness(t)
	dbtest.MustExec(t, h.ctx, h.pool, `
		DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2 AND permission = 'project.tasks.execute'`,
		h.fixture.ProjectA, h.principal.UserID)
	claim, err := h.mergeService.Claim(h.ctx, h.principal, h.fixture.RuntimeA, h.fixture.DaemonA)
	if err != nil || claim != nil {
		t.Fatalf("Claim() without project.tasks.execute = %+v, %v; want nil, nil", claim, err)
	}
	current, err := h.taskService.Get(h.ctx, h.principal, h.fixture.TaskA, nil)
	if err != nil {
		t.Fatalf("Get(Task) = %v", err)
	}
	manual := ManualMergeClaimInput{
		TaskID: h.fixture.TaskA, RunID: h.sourceRunID,
		ExpectedRevision: current.Revision, ExpectedExecutionGeneration: mergeIntegrationGeneration,
	}
	// A manual merge without the permission is told so, never that the
	// review is stale, even when its revision is stale too: the permission
	// is asked first.
	stale := manual
	stale.ExpectedRevision--
	for name, input := range map[string]ManualMergeClaimInput{"current": manual, "stale revision": stale} {
		if _, err := h.mergeService.ClaimManual(
			h.ctx, h.principal, h.fixture.CompanyA, h.fixture.RuntimeA, h.fixture.DaemonA, input,
		); !errors.Is(err, auth.ErrProjectPermissionRequired) || errors.Is(err, ErrMergeConflict) {
			t.Fatalf("ClaimManual(%s) without project.tasks.execute = %v, want %v",
				name, err, auth.ErrProjectPermissionRequired)
		}
	}
	if count := countMergeIntegrationRows(t, h.ctx, h.pool, `
		SELECT count(*) FROM task_merge_jobs WHERE task_id = $1 AND request_type = 'manual'`,
		h.fixture.TaskA); count != 0 {
		t.Fatalf("manual merge jobs after the refusal = %d, want 0", count)
	}

	// A member who cannot read the Project hears that the Task is not
	// there, as a read of it answers, never the permission or the review.
	dbtest.MustExec(t, h.ctx, h.pool, `
		DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		h.fixture.ProjectA, h.principal.UserID)
	if _, err := h.mergeService.ClaimManual(
		h.ctx, h.principal, h.fixture.CompanyA, h.fixture.RuntimeA, h.fixture.DaemonA, manual,
	); !errors.Is(err, ErrMergeClaimNotFound) {
		t.Fatalf("ClaimManual() without project.read = %v, want %v", err, ErrMergeClaimNotFound)
	}

	dbtest.GrantProject(t, h.ctx, h.pool, h.fixture.CompanyA, h.fixture.ProjectA, h.principal.UserID)
	// With the permission back, a stale revision is the conflict it is.
	if _, err := h.mergeService.ClaimManual(
		h.ctx, h.principal, h.fixture.CompanyA, h.fixture.RuntimeA, h.fixture.DaemonA, stale,
	); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("ClaimManual(stale revision) with project.tasks.execute = %v, want %v", err, ErrMergeConflict)
	}
	if claim := h.claim(t); claim.JobID != h.jobID {
		t.Fatalf("Claim() after the grant returns = %+v, want job %s", claim, h.jobID)
	}
}

func TestMergeServiceRejectsWrongRuntimeDaemonAndMemberAuthority(t *testing.T) {
	h := newMergeIntegrationHarness(t)
	wrongMember := auth.UserPrincipal{
		UserID: h.fixture.UserB, CompanyID: h.fixture.CompanyA, Role: auth.CompanyRoleMember,
	}

	claim, err := h.mergeService.Claim(
		h.ctx,
		h.principal,
		h.fixture.RuntimeB,
		h.fixture.DaemonB,
	)
	if err != nil || claim != nil {
		t.Fatalf("Claim(wrong Runtime) = %+v, %v; want nil, nil", claim, err)
	}
	claim, err = h.mergeService.Claim(
		h.ctx,
		wrongMember,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
	)
	if err != nil || claim != nil {
		t.Fatalf("Claim(wrong member) = %+v, %v; want nil, nil", claim, err)
	}
	claim, err = h.mergeService.Claim(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		"wrong-daemon",
	)
	if err != nil || claim != nil {
		t.Fatalf("Claim(wrong daemon) = %+v, %v; want nil, nil", claim, err)
	}
	claim = h.claim(t)

	result := MergeResultInput{
		Attempt: claim.Attempt, Status: MergeResultBlocked, ResultCode: "conflict",
	}
	resultTests := []struct {
		name      string
		principal auth.UserPrincipal
		runtimeID uuid.UUID
		daemonID  string
	}{
		{
			name: "wrong Runtime", principal: h.principal,
			runtimeID: h.fixture.RuntimeB, daemonID: h.fixture.DaemonB,
		},
		{
			name: "wrong daemon", principal: h.principal,
			runtimeID: h.fixture.RuntimeA, daemonID: "wrong-daemon",
		},
		{
			name: "wrong member", principal: wrongMember,
			runtimeID: h.fixture.RuntimeA, daemonID: h.fixture.DaemonA,
		},
	}
	for _, test := range resultTests {
		t.Run(test.name, func(t *testing.T) {
			err := h.mergeService.RecordResult(
				h.ctx,
				test.principal,
				test.runtimeID,
				test.daemonID,
				h.jobID,
				result,
			)
			if !errors.Is(err, ErrMergeClaimNotFound) {
				t.Fatalf("RecordResult() error = %v, want %v", err, ErrMergeClaimNotFound)
			}
		})
	}

	var jobStatus string
	if err := h.pool.QueryRow(h.ctx, `SELECT status FROM task_merge_jobs WHERE id=$1`, h.jobID).
		Scan(&jobStatus); err != nil {
		t.Fatalf("read unauthorized merge-result job: %v", err)
	}
	if jobStatus != "executing" {
		t.Fatalf("job status after rejected results = %q, want executing", jobStatus)
	}
	if err := h.mergeService.RecordResult(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		result,
	); err != nil {
		t.Fatalf("RecordResult(valid authority) error = %v", err)
	}
}

func TestTaskMutationRejectsLiveExecutingMergeClaim(t *testing.T) {
	h := newMergeIntegrationHarness(t)
	h.claim(t)

	current, err := h.taskService.Get(h.ctx, h.principal, h.fixture.TaskA, nil)
	if err != nil {
		t.Fatalf("Get(Task before mutation) error = %v", err)
	}
	todo := StatusTodo
	updated, err := h.taskService.Update(h.ctx, h.principal, h.fixture.TaskA, UpdateInput{
		ExpectedRevision: current.Revision,
		Status:           &todo,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Update(Task with live merge) = %+v, %v; want %v", updated, err, ErrConflict)
	}

	var (
		taskStatus   string
		taskRevision int64
		jobStatus    string
		lease        pgtype.Timestamptz
		leaseLive    bool
	)
	if err := h.pool.QueryRow(
		h.ctx, `
		SELECT tasks.status, tasks.version, task_merge_jobs.status,
		       task_merge_jobs.lease_expires_at,
		       task_merge_jobs.lease_expires_at > clock_timestamp()
		FROM tasks
		JOIN task_merge_jobs ON task_merge_jobs.task_id=tasks.id
		WHERE tasks.id=$1 AND task_merge_jobs.id=$2`, h.fixture.TaskA, h.jobID,
	).Scan(&taskStatus, &taskRevision, &jobStatus, &lease, &leaseLive); err != nil {
		t.Fatalf("read live merge mutation fence: %v", err)
	}
	if taskStatus != string(StatusInReview) || taskRevision != current.Revision ||
		jobStatus != "executing" || !lease.Valid || !leaseLive {
		t.Fatalf(
			"live merge mutation state = Task %q rev %d, job %q lease %v",
			taskStatus,
			taskRevision,
			jobStatus,
			lease,
		)
	}
}

func TestExpiredMergeClaimIsReclaimedAndFencedByAttempt(t *testing.T) {
	h := newMergeIntegrationHarness(t)
	firstClaim := h.claim(t)
	h.disableProactiveMode(t)
	dbtest.MustExec(t, h.ctx, h.pool, `
		UPDATE task_merge_jobs
		SET lease_expires_at=clock_timestamp() - interval '1 second'
		WHERE id=$1`, h.jobID)
	expiredResult := MergeResultInput{
		Attempt: firstClaim.Attempt, Status: MergeResultBlocked, ResultCode: "conflict",
	}
	if err := h.mergeService.RecordResult(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		expiredResult,
	); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("RecordResult(expired attempt) error = %v, want %v", err, ErrMergeConflict)
	}
	if err := h.mergeService.RenewLease(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		firstClaim.Attempt,
	); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("RenewLease(expired attempt) error = %v, want %v", err, ErrMergeConflict)
	}

	secondClaim := h.claim(t)
	if secondClaim.Attempt != firstClaim.Attempt+1 {
		t.Fatalf(
			"reclaimed Attempt = %d, want %d",
			secondClaim.Attempt,
			firstClaim.Attempt+1,
		)
	}
	if err := h.mergeService.RenewLease(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		secondClaim.Attempt,
	); err != nil {
		t.Fatalf("RenewLease(reclaimed claim) error = %v", err)
	}
	if err := h.mergeService.RenewLease(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		firstClaim.Attempt,
	); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("RenewLease(stale attempt) error = %v, want %v", err, ErrMergeConflict)
	}

	staleResult := MergeResultInput{
		Attempt: firstClaim.Attempt, Status: MergeResultBlocked, ResultCode: "conflict",
	}
	if err := h.mergeService.RecordResult(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		staleResult,
	); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("RecordResult(stale attempt) error = %v, want %v", err, ErrMergeConflict)
	}

	if _, _, err := h.enqueueFreshTaskRun(); !errors.Is(err, execution.ErrTaskMergeInProgress) {
		t.Fatalf(
			"enqueue while reclaimed merge executes error = %v, want %v",
			err,
			execution.ErrTaskMergeInProgress,
		)
	}

	result := MergeResultInput{
		Attempt: secondClaim.Attempt, Status: MergeResultBlocked, ResultCode: "conflict",
	}
	if err := h.mergeService.RecordResult(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		result,
	); err != nil {
		t.Fatalf("RecordResult(reclaimed attempt) error = %v", err)
	}
	if err := h.mergeService.RecordResult(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		result,
	); err != nil {
		t.Fatalf("RecordResult(reclaimed attempt replay) error = %v", err)
	}
	if err := h.mergeService.RecordResult(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
		h.jobID,
		staleResult,
	); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("RecordResult(stale terminal replay) error = %v, want %v", err, ErrMergeConflict)
	}

	queued, pending, err := h.enqueueFreshTaskRun()
	if err != nil {
		t.Fatalf("enqueue after terminal merge result: %v", err)
	}
	assertFreshMergeIntegrationRun(t, queued, pending, h.sourceRunID)
}

func TestFreshTaskRunSupersedesPendingMergeJob(t *testing.T) {
	h := newMergeIntegrationHarness(t)

	queued, pending, err := h.enqueueFreshTaskRun()
	if err != nil {
		t.Fatalf("enqueue fresh Task Run: %v", err)
	}
	assertFreshMergeIntegrationRun(t, queued, pending, h.sourceRunID)

	var jobStatus string
	var resultCode pgtype.Text
	if err := h.pool.QueryRow(
		h.ctx, `
		SELECT status, result_code
		FROM task_merge_jobs
		WHERE id=$1`, h.jobID,
	).Scan(&jobStatus, &resultCode); err != nil {
		t.Fatalf("read superseded pending Task merge: %v", err)
	}
	if jobStatus != "blocked" {
		t.Fatalf("superseded pending job status = %q, want blocked", jobStatus)
	}
	if !resultCode.Valid {
		t.Fatal("superseded pending job result code is NULL")
	}
	if resultCode.String != "superseded" {
		t.Fatalf("superseded pending job result code = %q, want superseded", resultCode.String)
	}
}

func TestMergeClaimRechecksCandidateAfterConcurrentTaskRun(t *testing.T) {
	h := newMergeIntegrationHarness(t)

	claimTx, err := h.pool.BeginTx(h.ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin claim transaction: %v", err)
	}
	t.Cleanup(func() { _ = claimTx.Rollback(context.WithoutCancel(h.ctx)) })
	claimQueries := generated.New(claimTx)
	candidate, err := claimQueries.GetNextTaskMergeJobClaimCandidate(
		h.ctx,
		mergeClaimCandidateParams(h.principal.UserID, h.fixture.RuntimeA, h.fixture.DaemonA),
	)
	if err != nil {
		t.Fatalf("read pre-mutation merge candidate: %v", err)
	}
	if candidate.JobID != h.jobID {
		t.Fatalf("pre-mutation candidate = %s, want %s", candidate.JobID, h.jobID)
	}

	queued, pending, err := h.enqueueFreshTaskRun()
	if err != nil {
		t.Fatalf("enqueue concurrent fresh Task Run: %v", err)
	}
	assertFreshMergeIntegrationRun(t, queued, pending, h.sourceRunID)

	if err := lockMergeAuthority(
		h.ctx,
		claimQueries,
		h.principal.UserID,
		candidate.CompanyID,
	); err != nil {
		t.Fatalf("lock stale candidate authority: %v", err)
	}
	if _, err := claimQueries.GetTaskForCompanyByIDForUpdate(
		h.ctx,
		generated.GetTaskForCompanyByIDForUpdateParams{
			CompanyID: candidate.CompanyID,
			TaskID:    candidate.TaskID,
		},
	); err != nil {
		t.Fatalf("lock stale candidate Task: %v", err)
	}
	_, err = claimQueries.ClaimTaskMergeJob(
		h.ctx,
		mergeClaimParams(candidate.JobID, h.principal.UserID, h.fixture.RuntimeA, h.fixture.DaemonA),
	)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("claim stale candidate error = %v, want %v", err, pgx.ErrNoRows)
	}
}

func assertFreshMergeIntegrationRun(
	t *testing.T,
	queued generated.Run,
	pending []events.Event,
	previousRunID uuid.UUID,
) {
	t.Helper()
	if queued.ID == previousRunID || queued.ID == uuid.Nil {
		t.Fatalf("fresh Run ID = %d, want greater than %d", queued.ID, previousRunID)
	}
	if queued.Status != "queued" {
		t.Fatalf("fresh Run status = %q, want queued", queued.Status)
	}
	if !queued.TaskExecutionGeneration.Valid {
		t.Fatal("fresh Run Task execution generation is NULL")
	}
	if queued.TaskExecutionGeneration.Int64 != mergeIntegrationGeneration+1 {
		t.Fatalf(
			"fresh Run Task execution generation = %d, want %d",
			queued.TaskExecutionGeneration.Int64,
			mergeIntegrationGeneration+1,
		)
	}
	if len(pending) != 3 || pending[1].Name != "activity.created" || pending[2].Name != canvas.EventChanged {
		t.Fatalf("fresh Run events = %+v, want Run, Activity, and starter Overview events", pending)
	}
	if pending[0].RunID != queued.ID {
		t.Fatalf("fresh Run event RunID = %d, want %d", pending[0].RunID, queued.ID)
	}
	if pending[0].Name != execution.EventRunQueued {
		t.Fatalf("fresh Run event = %q, want %q", pending[0].Name, execution.EventRunQueued)
	}
}

func seedPendingMergeHumanInput(
	t *testing.T,
	h *mergeIntegrationHarness,
	requestType string,
) uuid.UUID {
	t.Helper()
	requestID := dbtest.NewUUID(t)
	requestKey := dbtest.NewUUID(t)
	hash := make([]byte, 32)
	switch requestType {
	case "Approval":
		dbtest.MustExec(
			t, h.ctx, h.pool, `
			INSERT INTO approvals (
				id, company_id, rule_key, title, preview_markdown,
				requested_by_agent_id, origin_run_id, request_key, task_id,
				conversation_id, canonical_action, request_sha256, action_sha256,
				expires_at
			) VALUES ($1,$2,'publish','Publish','Review',$3,$4,$5,$6,$7,$8,$9,$9,
			          clock_timestamp() + interval '1 hour')`,
			requestID,
			h.fixture.CompanyA,
			h.workerAgentID,
			h.sourceRunID,
			requestKey,
			h.fixture.TaskA,
			h.fixture.ConversationA,
			json.RawMessage(`{}`),
			hash,
		)
	case "Question":
		dbtest.MustExec(
			t, h.ctx, h.pool, `
			INSERT INTO questions (
				id, company_id, title, asked_by_agent_id, origin_run_id,
				request_key, task_id, conversation_id, prompts, request_sha256,
				expires_at
			) VALUES ($1,$2,'Deployment target',$3,$4,$5,$6,$7,$8,$9,
			          clock_timestamp() + interval '1 hour')`,
			requestID,
			h.fixture.CompanyA,
			h.workerAgentID,
			h.sourceRunID,
			requestKey,
			h.fixture.TaskA,
			h.fixture.ConversationA,
			json.RawMessage(`[]`),
			hash,
		)
	default:
		t.Fatalf("unsupported human-input request type %q", requestType)
	}
	return requestID
}

func resolveMergeHumanInput(
	t *testing.T,
	h *mergeIntegrationHarness,
	requestType string,
	requestID uuid.UUID,
) {
	t.Helper()
	switch requestType {
	case "Approval":
		dbtest.MustExec(t, h.ctx, h.pool, `
			UPDATE approvals
			SET status='rejected', decided_by_user_id=$2,
			    continuation_prompt='Continue after the decision.',
			    continuation_status='pending',
			    continuation_next_attempt_at=clock_timestamp()
			WHERE id=$1`, requestID, h.principal.UserID)
	case "Question":
		dbtest.MustExec(t, h.ctx, h.pool, `
			UPDATE questions
			SET status='answered', answers='[]'::jsonb, resolved_by_user_id=$2,
			    continuation_prompt='Continue after the answer.',
			    continuation_status='pending',
			    continuation_next_attempt_at=clock_timestamp()
			WHERE id=$1`, requestID, h.principal.UserID)
	default:
		t.Fatalf("unsupported human-input request type %q", requestType)
	}
}

func deliverMergeHumanInput(
	t *testing.T,
	h *mergeIntegrationHarness,
	requestType string,
	requestID uuid.UUID,
) {
	t.Helper()
	switch requestType {
	case "Approval":
		dbtest.MustExec(t, h.ctx, h.pool, `
			UPDATE approvals
			SET continuation_status='delivered', continuation_run_id=$2
			WHERE id=$1`, requestID, h.sourceRunID)
	case "Question":
		dbtest.MustExec(t, h.ctx, h.pool, `
			UPDATE questions
			SET continuation_status='delivered', continuation_run_id=$2
			WHERE id=$1`, requestID, h.sourceRunID)
	default:
		t.Fatalf("unsupported human-input request type %q", requestType)
	}
}

func assertProactiveMergeBlockedByHumanInput(
	t *testing.T,
	h *mergeIntegrationHarness,
	phase string,
) {
	t.Helper()
	dbtest.MustExec(t, h.ctx, h.pool, `DELETE FROM task_merge_jobs WHERE task_id=$1`, h.fixture.TaskA)
	if _, err := h.store.Queries().InsertProactiveTaskMergeJobForRun(
		h.ctx,
		generated.InsertProactiveTaskMergeJobForRunParams{
			ActorRoles: auth.RolesWith(auth.PermissionCompanyProactiveModeManage),
			RunID:      h.sourceRunID,
		},
	); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("InsertProactiveTaskMergeJobForRun(%s) error = %v, want %v", phase, err, pgx.ErrNoRows)
	}
	if _, err := h.store.Queries().
		InsertNextProactiveTaskMergeJob(h.ctx, auth.RolesWith(auth.PermissionCompanyProactiveModeManage)); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("InsertNextProactiveTaskMergeJob(%s) error = %v, want %v", phase, err, pgx.ErrNoRows)
	}
	dbtest.MustExec(
		t, h.ctx, h.pool, `
		INSERT INTO task_merge_jobs (
			company_id, task_id, run_id, task_execution_generation, authority_user_id
		) VALUES ($1,$2,$3,$4,$5)`,
		h.fixture.CompanyA,
		h.fixture.TaskA,
		h.sourceRunID,
		mergeIntegrationGeneration,
		h.principal.UserID,
	)
	claim, err := h.mergeService.Claim(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
	)
	if err != nil || claim != nil {
		t.Fatalf("Claim(%s) = %+v, %v; want nil, nil", phase, claim, err)
	}
}

func newMergeIntegrationHarness(t *testing.T) *mergeIntegrationHarness {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	workerAgentID := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(t, ctx, pool, workerAgentID, fixture.CompanyA,
		"Engineering Agent", "codex", &fixture.AgentA, fixture.UserA, fixture.RuntimeA)
	dbtest.MustExec(t, ctx, pool, `
				UPDATE tasks
		SET executed_by_agent_id=$2,
		    status='in_progress',
		    completed_at=NULL,
		    execution_generation=$3
		WHERE id=$1`, fixture.TaskA, workerAgentID, mergeIntegrationGeneration)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE companies
		SET proactive_mode_enabled=true,
		    proactive_mode_enabled_by_user_id=$2,
		    proactive_mode_conversation_id=$3,
		    proactive_mode_version=proactive_mode_version+1
		WHERE id=$1`, fixture.CompanyA, fixture.UserA, fixture.DirectConversationA)

	var sourceRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, context, task_execution_generation, updated_at
		, run_type) VALUES ($1,$2,$3,$4,$5,'completed',$6,$7,clock_timestamp(), (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA,
		workerAgentID,
		fixture.TaskA,
		fixture.ConversationA,
		fixture.RuntimeA,
		[]byte(`{"executionProfile":{"task":{"key":"ENG-1"}}}`),
		mergeIntegrationGeneration,
	).Scan(&sourceRunID); err != nil {
		t.Fatalf("insert completed Task Run: %v", err)
	}
	run, err := store.Queries().GetRun(ctx, sourceRunID)
	if err != nil {
		t.Fatalf("read completed Task Run: %v", err)
	}
	var staged []events.Event
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		return ApplyRunTerminalInTx(ctx, queries, &staged, run)
	}); err != nil {
		t.Fatalf("apply completed Run Task effect: %v", err)
	}
	if len(staged) != 2 || staged[1].Name != "activity.created" || staged[0].Name != EventUpdated {
		t.Fatalf("completed Run Task events = %+v", staged)
	}
	var (
		jobID              uuid.UUID
		jobStatus          string
		jobRunID           uuid.UUID
		jobTaskGeneration  int64
		jobAuthorityUserID uuid.UUID
	)
	if err := pool.QueryRow(
		ctx, `
		SELECT id, status, run_id, task_execution_generation, authority_user_id
		FROM task_merge_jobs
		WHERE run_id=$1`, sourceRunID,
	).Scan(
		&jobID,
		&jobStatus,
		&jobRunID,
		&jobTaskGeneration,
		&jobAuthorityUserID,
	); err != nil {
		t.Fatalf("read pending Task merge job: %v", err)
	}
	if jobStatus != "pending" || jobRunID != sourceRunID ||
		jobTaskGeneration != mergeIntegrationGeneration ||
		jobAuthorityUserID != fixture.UserA {
		t.Fatalf(
			"pending Task merge job = status %q Run %d generation %d automation user %s",
			jobStatus,
			jobRunID,
			jobTaskGeneration,
			jobAuthorityUserID,
		)
	}

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	mergeService, err := NewMergeService(store, bus)
	if err != nil {
		t.Fatalf("construct Task merge service: %v", err)
	}
	taskService, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatalf("construct Task service: %v", err)
	}
	return &mergeIntegrationHarness{
		ctx: ctx, pool: pool, store: store, bus: bus,
		mergeService: mergeService, taskService: taskService, fixture: fixture,
		principal: auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		},
		workerAgentID: workerAgentID,
		sourceRunID:   sourceRunID,
		jobID:         jobID,
	}
}

func (h *mergeIntegrationHarness) claim(t *testing.T) *MergeClaim {
	t.Helper()
	claim, err := h.mergeService.Claim(
		h.ctx,
		h.principal,
		h.fixture.RuntimeA,
		h.fixture.DaemonA,
	)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claim == nil {
		t.Fatal("Claim() = nil")
	}
	return claim
}

func (h *mergeIntegrationHarness) enqueueFreshTaskRun() (
	generated.Run,
	[]events.Event,
	error,
) {
	var queued generated.Run
	var pending []events.Event
	err := h.store.WithinTx(h.ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if _, err := queries.LockConversationForActiveUserMutation(
			h.ctx,
			generated.LockConversationForActiveUserMutationParams{
				ActorUserID:    h.principal.UserID,
				ConversationID: h.fixture.ConversationA,
				CompanyID:      h.fixture.CompanyA,
			},
		); err != nil {
			return err
		}
		var enqueueErr error
		queued, enqueueErr = execution.EnqueueRunInTx(
			h.ctx,
			queries,
			&pending,
			execution.RunTrigger{
				CompanyID:          h.fixture.CompanyA,
				ConversationID:     h.fixture.ConversationA,
				AgentID:            h.workerAgentID,
				TaskID:             uuid.NullUUID{UUID: h.fixture.TaskA, Valid: true},
				TriggerPrompt:      "Continue with the reviewed implementation.",
				Source:             execution.RunSourceMessage,
				ActorUserID:        h.principal.UserID,
				AuthorizedByUserID: h.principal.UserID,
			},
		)
		return enqueueErr
	})
	return queued, pending, err
}

func (h *mergeIntegrationHarness) disableProactiveMode(t *testing.T) {
	t.Helper()
	dbtest.MustExec(t, h.ctx, h.pool, `
		UPDATE companies
		SET proactive_mode_enabled=false,
		    proactive_mode_enabled_by_user_id=NULL,
		    proactive_mode_conversation_id=NULL,
		    proactive_mode_version=proactive_mode_version+1
		WHERE id=$1`, h.fixture.CompanyA)
}

func countMergeIntegrationEvents(recorded []events.Event, name string) int {
	count := 0
	for _, event := range recorded {
		if event.Name == name {
			count++
		}
	}
	return count
}

func countMergeIntegrationRows(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	query string,
	args ...any,
) int64 {
	t.Helper()
	var count int64
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count merge integration rows: %v", err)
	}
	return count
}
