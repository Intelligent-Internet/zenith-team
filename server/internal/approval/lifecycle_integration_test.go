//go:build integration

package approval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/execution"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestLifecycleReconcilerDeliversContinuationExactlyOnce(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	if err := activity.RegisterListeners(bus, store.Queries()); err != nil {
		t.Fatalf("activity.RegisterListeners() error = %v", err)
	}
	questions, err := NewQuestionService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewQuestionService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
	asked, _, err := questions.Ask(ctx, principal, origin, uuid.Must(uuid.NewV7()), QuestionAskInput{
		Title: "Deployment target",
		Prompts: []QuestionPrompt{
			{
				ID:            "target",
				Prompt:        "Which target?",
				SelectionMode: QuestionSelectionSingle,
				Required:      true,
				Options: []QuestionOption{
					{ID: "staging", Label: "Staging"},
					{ID: "prod", Label: "Production"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, origin.RunID)
	answered, err := questions.Answer(ctx, principal, asked.ID, QuestionAnswerInput{
		ExpectedRevision: asked.Revision,
		Answers:          []QuestionAnswer{{PromptID: "target", OptionIDs: []string{"staging"}}},
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	// Answering opens the receipt but names no Run: the id is stamped by the
	// transaction that inserts it, which has not run yet.
	if answered.ContinuationRunID != nil {
		t.Fatalf("Answer() continuationRunId = %d, want nil until the Run exists",
			*answered.ContinuationRunID)
	}

	// The answer is in the company's history once. The re-sent frame below
	// refreshes that answer, so it must not be recorded a second time.
	answeredHistory := countActivity(t, ctx, pool, EventQuestionResolved, asked.ID)
	if answeredHistory != 1 {
		t.Fatalf("question.resolved Activity rows after the answer = %d, want 1", answeredHistory)
	}

	notifier := recordContinuationWakeups(t, bus)
	frames := recordTopic[Question](t, bus, TopicQuestion)
	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile(replay) error = %v", err)
	}
	if after := countActivity(t, ctx, pool, EventQuestionResolved, asked.ID); after != answeredHistory {
		t.Fatalf("question.resolved Activity rows after delivery = %d, want %d", after, answeredHistory)
	}

	var status string
	var attempts int
	var continuationRunID uuid.NullUUID
	var revision int64
	if err := pool.QueryRow(ctx, `
		SELECT continuation_status, continuation_attempts, continuation_run_id, version
		FROM questions WHERE id=$1`, asked.ID).
		Scan(&status, &attempts, &continuationRunID, &revision); err != nil {
		t.Fatalf("read continuation receipt: %v", err)
	}
	if status != "delivered" || attempts != 1 || !continuationRunID.Valid {
		t.Fatalf("continuation receipt = %q/%d/%v", status, attempts, continuationRunID)
	}
	if len(notifier.runs) != 1 || notifier.runs[0] != continuationRunID.UUID {
		t.Fatalf("notified Runs = %+v, want the stamped id %d", notifier.runs, continuationRunID.UUID)
	}
	// The stamp is a change a client holding the answered revision must hear
	// about, or it never learns which Run carried the answer.
	if revision != answered.Revision+1 {
		t.Fatalf("delivered Question revision = %d, want %d", revision, answered.Revision+1)
	}
	resolved := frames.named(EventQuestionResolved)
	if len(resolved) != 1 || resolved[0].Payload.Revision != revision ||
		resolved[0].Payload.ContinuationRunID == nil ||
		*resolved[0].Payload.ContinuationRunID != continuationRunID.UUID {
		t.Fatalf("delivery frames = %+v, want one question.resolved at revision %d naming Run %s",
			resolved, revision, continuationRunID.UUID)
	}

	var runCount int
	// parent_run_id is a bigint like the id it references; a continuation is a
	// fresh root Run, so it must stay NULL.
	var parentID uuid.NullUUID
	var runContext []byte
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE id=$1`, continuationRunID.UUID).
		Scan(&runCount); err != nil {
		t.Fatalf("count continuation Run: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT parent_run_id, context FROM runs WHERE id=$1`, continuationRunID.UUID).
		Scan(&parentID, &runContext); err != nil {
		t.Fatalf("read continuation Run: %v", err)
	}
	if runCount != 1 || parentID.Valid {
		t.Fatalf("continuation count=%d parent=%v", runCount, parentID)
	}
	var contextDocument struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(runContext, &contextDocument); err != nil || contextDocument.Prompt == "" {
		t.Fatalf("continuation context = %s, %v", runContext, err)
	}
}

func TestLifecycleReconcilerDefersWhileOriginRunIsStillCurrent(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, bus, questions := newQuestionLifecycleFixture(t, pool)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
	asked, _, err := questions.Ask(ctx, principal, origin, uuid.Must(uuid.NewV7()), QuestionAskInput{
		Title: "Deployment target",
		Prompts: []QuestionPrompt{{
			ID:            "target",
			Prompt:        "Which target?",
			SelectionMode: QuestionSelectionSingle,
			Required:      true,
			Options: []QuestionOption{
				{ID: "staging", Label: "Staging"},
				{ID: "prod", Label: "Production"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if _, err := questions.Answer(ctx, principal, asked.ID, QuestionAnswerInput{
		ExpectedRevision: asked.Revision,
		Answers:          []QuestionAnswer{{PromptID: "target", OptionIDs: []string{"staging"}}},
	}); err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	runsBefore := countCompanyRuns(t, ctx, pool, fixture.CompanyA)

	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var status string
	var attempts int
	var continuationRunID uuid.NullUUID
	var nextAttemptAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT continuation_status, continuation_attempts, continuation_run_id,
		       continuation_next_attempt_at
		FROM questions WHERE id=$1`, asked.ID).
		Scan(&status, &attempts, &continuationRunID, &nextAttemptAt); err != nil {
		t.Fatalf("read deferred continuation receipt: %v", err)
	}
	if status != "pending" || attempts != 1 || continuationRunID.Valid {
		t.Fatalf("deferred receipt = %q/%d/%v, want pending/1 and a NULL Run id",
			status, attempts, continuationRunID)
	}
	if !nextAttemptAt.After(time.Now().UTC()) {
		t.Fatalf("continuation_next_attempt_at = %s, want a time in the future", nextAttemptAt)
	}
	if after := countCompanyRuns(t, ctx, pool, fixture.CompanyA); after != runsBefore {
		t.Fatalf("Runs while origin %d is current = %d, want %d", origin.RunID, after, runsBefore)
	}
}

// A deferred continuation is the first case that used to leave an id behind:
// the reconciler declines, the receipt stays pending for a later attempt, and
// the Run it would have named was never inserted. continuation_run_id must be
// NULL — anything else is a promise the runs API cannot keep.
func TestLifecycleReconcilerLeavesContinuationRunNullWhenDeferred(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, bus, questions := newQuestionLifecycleFixture(t, pool)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedTaskRunningOrigin(t, ctx, pool, fixture)
	asked, _, err := questions.Ask(ctx, principal, origin, uuid.Must(uuid.NewV7()), QuestionAskInput{
		Title: "Deployment target",
		Prompts: []QuestionPrompt{
			{
				ID:            "target",
				Prompt:        "Which target?",
				SelectionMode: QuestionSelectionSingle,
				Required:      true,
				Options: []QuestionOption{
					{ID: "staging", Label: "Staging"},
					{ID: "prod", Label: "Production"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, origin.RunID)
	if _, err := questions.Answer(ctx, principal, asked.ID, QuestionAnswerInput{
		ExpectedRevision: asked.Revision,
		Answers:          []QuestionAnswer{{PromptID: "target", OptionIDs: []string{"staging"}}},
	}); err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	// A sibling Run already occupies the (task, agent) pending slot, so the
	// continuation loses the one-pending-per-pair index and is deferred.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id, status, run_type)
		VALUES ($1, $2, $3, $4, $5, 'queued', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA, fixture.ConversationA, fixture.RuntimeA)
	runsBefore := countCompanyRuns(t, ctx, pool, fixture.CompanyA)

	notifier := recordContinuationWakeups(t, bus)
	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var status string
	var attempts int
	var continuationRunID uuid.NullUUID
	var nextAttemptAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT continuation_status, continuation_attempts, continuation_run_id,
		       continuation_next_attempt_at
		FROM questions WHERE id=$1`, asked.ID).
		Scan(&status, &attempts, &continuationRunID, &nextAttemptAt); err != nil {
		t.Fatalf("read deferred continuation receipt: %v", err)
	}
	if status != "pending" || attempts != 1 || continuationRunID.Valid {
		t.Fatalf("deferred receipt = %q/%d/%v, want pending/1 and a NULL run id",
			status, attempts, continuationRunID)
	}
	// The deferral write shares the transaction with the insert that just
	// failed, so it commits only because that insert ran behind a savepoint.
	// Without one it raises 25P02, the claim rolls back with it, and the
	// receipt stays immediately due — the wedge this asserts against.
	if !nextAttemptAt.After(time.Now().UTC()) {
		t.Fatalf("continuation_next_attempt_at = %s, want a time in the future", nextAttemptAt)
	}
	if after := countCompanyRuns(t, ctx, pool, fixture.CompanyA); after != runsBefore {
		t.Fatalf("Runs after a deferred continuation = %d, want %d", after, runsBefore)
	}
	if len(notifier.runs) != 0 {
		t.Fatalf("deferred continuation notified %d Runs, want 0", len(notifier.runs))
	}

	// And the retry time is load-bearing: the next pass must not find this
	// receipt due, or one blocked continuation spins and starves the rest.
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile(second pass) error = %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT continuation_attempts FROM questions WHERE id=$1`, asked.ID).
		Scan(&attempts); err != nil {
		t.Fatalf("re-read deferred continuation receipt: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("continuation_attempts after a second pass = %d, want 1 — the receipt was reclaimed early",
			attempts)
	}
}

// The Approval reconciler is the Question reconciler's twin and defers on the
// same losing race, so it needs the same savepoint under its insert. Fixing one
// and not the other leaves half the human-input surface wedged.
func TestLifecycleReconcilerDefersApprovalContinuationWithoutPoisoningTheTransaction(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	approvals, err := NewApprovalService(store, bus, &planActionParticipantStub{}, nil)
	if err != nil {
		t.Fatalf("NewApprovalService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedTaskRunningOrigin(t, ctx, pool, fixture)
	requested, _, err := approvals.RequestCompanyPlanApply(
		ctx, principal, origin, uuid.Must(uuid.NewV7()),
		json.RawMessage(`{"artifactVersion":1,"desired":{"tasks":[]}}`),
	)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, origin.RunID)
	if _, err := approvals.Approve(ctx, principal, requested.ID, ApprovalDecisionInput{
		ExpectedRevision: requested.Revision,
	}); err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	// The agent already holds the one pending slot for this Task, so the
	// continuation insert loses the unique index and has to be deferred.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id, status, run_type)
		VALUES ($1, $2, $3, $4, $5, 'queued', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA, fixture.ConversationA, fixture.RuntimeA)
	runsBefore := countCompanyRuns(t, ctx, pool, fixture.CompanyA)

	notifier := recordContinuationWakeups(t, bus)
	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var status string
	var attempts int
	var continuationRunID uuid.NullUUID
	var nextAttemptAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT continuation_status, continuation_attempts, continuation_run_id,
		       continuation_next_attempt_at
		FROM approvals WHERE id=$1`, requested.ID).
		Scan(&status, &attempts, &continuationRunID, &nextAttemptAt); err != nil {
		t.Fatalf("read deferred Approval continuation receipt: %v", err)
	}
	if status != "pending" || attempts != 1 || continuationRunID.Valid {
		t.Fatalf("deferred Approval receipt = %q/%d/%v, want pending/1 and a NULL run id",
			status, attempts, continuationRunID)
	}
	if !nextAttemptAt.After(time.Now().UTC()) {
		t.Fatalf("continuation_next_attempt_at = %s, want a time in the future", nextAttemptAt)
	}
	if after := countCompanyRuns(t, ctx, pool, fixture.CompanyA); after != runsBefore {
		t.Fatalf("Runs after a deferred Approval continuation = %d, want %d", after, runsBefore)
	}
	if len(notifier.runs) != 0 {
		t.Fatalf("deferred Approval continuation notified %d Runs, want 0", len(notifier.runs))
	}

	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile(second pass) error = %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT continuation_attempts FROM approvals WHERE id=$1`, requested.ID).
		Scan(&attempts); err != nil {
		t.Fatalf("re-read deferred Approval continuation receipt: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("continuation_attempts after a second pass = %d, want 1 — the receipt was reclaimed early",
			attempts)
	}
}

// The second case: an archived agent abandons the receipt permanently. The
// Run will never exist, so the column must stay NULL forever rather than
// naming a burned sequence value.
func TestLifecycleReconcilerLeavesContinuationRunNullWhenAbandoned(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, bus, questions := newQuestionLifecycleFixture(t, pool)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
	asked, _, err := questions.Ask(ctx, principal, origin, uuid.Must(uuid.NewV7()), QuestionAskInput{
		Title: "Deployment target",
		Prompts: []QuestionPrompt{
			{
				ID:            "target",
				Prompt:        "Which target?",
				SelectionMode: QuestionSelectionSingle,
				Required:      true,
				Options: []QuestionOption{
					{ID: "staging", Label: "Staging"},
					{ID: "prod", Label: "Production"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, origin.RunID)
	if _, err := questions.Answer(ctx, principal, asked.ID, QuestionAnswerInput{
		ExpectedRevision: asked.Revision,
		Answers:          []QuestionAnswer{{PromptID: "target", OptionIDs: []string{"staging"}}},
	}); err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET archived_at=now() WHERE id=$1`, fixture.AgentA)
	runsBefore := countCompanyRuns(t, ctx, pool, fixture.CompanyA)

	notifier := recordContinuationWakeups(t, bus)
	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var status, reason string
	var continuationRunID uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		SELECT continuation_status, continuation_abandon_reason, continuation_run_id
		FROM questions WHERE id=$1`, asked.ID).
		Scan(&status, &reason, &continuationRunID); err != nil {
		t.Fatalf("read abandoned continuation receipt: %v", err)
	}
	if status != "abandoned" || reason != string(abandonAgentArchived) || continuationRunID.Valid {
		t.Fatalf("abandoned receipt = %q/%q/%v, want abandoned/%s and a NULL run id",
			status, reason, continuationRunID, abandonAgentArchived)
	}
	if after := countCompanyRuns(t, ctx, pool, fixture.CompanyA); after != runsBefore {
		t.Fatalf("Runs after an abandoned continuation = %d, want %d", after, runsBefore)
	}
	if len(notifier.runs) != 0 {
		t.Fatalf("abandoned continuation notified %d Runs, want 0", len(notifier.runs))
	}
	// An archived Agent can be restored, so the chat's creator is told what to
	// do once it is. Only a member allowed to configure it can restore it,
	// so the notice does not tell every creator to do that themself.
	notice := readAbandonedContinuationNotice(t, ctx, pool, fixture.UserA, "question", asked.ID)
	if notice.conversationID != fixture.DirectConversationA ||
		!strings.Contains(notice.body, "is archived") ||
		!strings.HasSuffix(notice.body, "Once Agent A is restored, ask again in the chat.") {
		t.Fatalf("archived Agent continuation notice = %+v", notice)
	}
}

func TestLifecycleReconcilerPreparesTaskContinuationForExecution(t *testing.T) {
	for _, humanWork := range []bool{false, true} {
		t.Run(map[bool]string{false: "agent work", true: "human work"}[humanWork], func(t *testing.T) {
			pool, _ := dbtest.StartPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatalf("MigrateUp() error = %v", err)
			}
			fixture := dbtest.SeedFixture(t, ctx, pool)
			store, bus, questions := newQuestionLifecycleFixture(t, pool)
			principal := auth.UserPrincipal{
				UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
			}
			origin := seedTaskRunningOrigin(t, ctx, pool, fixture)
			if humanWork {
				dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET executed_by_agent_id=NULL WHERE id=$1`, fixture.TaskA)
			}
			dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status='in_review' WHERE id=$1`, fixture.TaskA)
			asked, _, err := questions.Ask(ctx, principal, origin, uuid.Must(uuid.NewV7()), QuestionAskInput{
				Title: "Deployment target",
				Prompts: []QuestionPrompt{{
					ID:            "target",
					Prompt:        "Which target?",
					SelectionMode: QuestionSelectionSingle,
					Required:      true,
					Options: []QuestionOption{
						{ID: "staging", Label: "Staging"},
						{ID: "production", Label: "Production"},
					},
				}},
			})
			if err != nil {
				t.Fatalf("Ask() error = %v", err)
			}
			dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, origin.RunID)
			if _, err := questions.Answer(ctx, principal, asked.ID, QuestionAnswerInput{
				ExpectedRevision: asked.Revision,
				Answers:          []QuestionAnswer{{PromptID: "target", OptionIDs: []string{"staging"}}},
			}); err != nil {
				t.Fatalf("Answer() error = %v", err)
			}

			worker, err := NewLifecycleWorker(store, bus, nil)
			if err != nil {
				t.Fatalf("NewLifecycleWorker() error = %v", err)
			}
			if err := worker.Reconcile(ctx); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}

			var taskStatus, continuationStatus string
			if err := pool.QueryRow(ctx, `SELECT status FROM tasks WHERE id=$1`, fixture.TaskA).
				Scan(&taskStatus); err != nil {
				t.Fatalf("read Task status: %v", err)
			}
			if err := pool.QueryRow(ctx, `SELECT continuation_status FROM questions WHERE id=$1`, asked.ID).
				Scan(&continuationStatus); err != nil {
				t.Fatalf("read continuation status: %v", err)
			}
			if taskStatus != "in_progress" || continuationStatus != "delivered" {
				t.Fatalf("Task/continuation status = %q/%q, want in_progress/delivered",
					taskStatus, continuationStatus)
			}
		})
	}
}

func TestLifecycleReconcilerAbandonsReassignedTaskContinuation(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, bus, questions := newQuestionLifecycleFixture(t, pool)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedTaskRunningOrigin(t, ctx, pool, fixture)
	asked, _, err := questions.Ask(ctx, principal, origin, uuid.Must(uuid.NewV7()), QuestionAskInput{
		Title: "Deployment target",
		Prompts: []QuestionPrompt{{
			ID:            "target",
			Prompt:        "Which target?",
			SelectionMode: QuestionSelectionSingle,
			Required:      true,
			Options: []QuestionOption{
				{ID: "staging", Label: "Staging"},
				{ID: "production", Label: "Production"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, origin.RunID)
	if _, err := questions.Answer(ctx, principal, asked.ID, QuestionAnswerInput{
		ExpectedRevision: asked.Revision,
		Answers:          []QuestionAnswer{{PromptID: "target", OptionIDs: []string{"staging"}}},
	}); err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	// The Task is handed off: its member takes the work into their own hands,
	// so the Agent that asked the Question no longer executes it (ADR 0014).
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET executed_by_agent_id=NULL, assignee_user_id=$2,
		    execution_generation=execution_generation+1,
		    workspace_generation=workspace_generation+1, version=version+1
		WHERE id=$1`, fixture.TaskA, fixture.UserA)
	runsBefore := countCompanyRuns(t, ctx, pool, fixture.CompanyA)

	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var status, reason string
	var continuationRunID uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		SELECT continuation_status, continuation_abandon_reason, continuation_run_id
		FROM questions WHERE id=$1`, asked.ID).
		Scan(&status, &reason, &continuationRunID); err != nil {
		t.Fatalf("read abandoned continuation receipt: %v", err)
	}
	if status != "abandoned" || reason != string(abandonTaskUnavailable) || continuationRunID.Valid {
		t.Fatalf("reassigned receipt = %q/%q/%v, want abandoned/%s and a NULL Run id",
			status, reason, continuationRunID, abandonTaskUnavailable)
	}
	if after := countCompanyRuns(t, ctx, pool, fixture.CompanyA); after != runsBefore {
		t.Fatalf("Runs after Task reassignment = %d, want %d", after, runsBefore)
	}
	// The handoff was a member's own decision, and after one the new assignee
	// never saw the Question: a run_failed item about it would be noise.
	if count := countContinuationNotices(t, ctx, pool, "question", asked.ID); count != 0 {
		t.Fatalf("Inbox items after a handed-off continuation = %d, want 0", count)
	}
}

// The owner took the Agent off their computer between the question and the
// answer, so the continuation has nowhere to run (ADR 0014). Abandoning it is
// right; doing so in silence is not. The Task's assignee — the member the Run
// would have run for — is told why and what to do, and every client holding
// the answered Question learns it moved on.
func TestLifecycleReconcilerTellsTheTaskAssigneeWhenTheAgentIsNotSetUp(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, bus, questions := newQuestionLifecycleFixture(t, pool)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	if err := activity.RegisterListeners(bus, store.Queries()); err != nil {
		t.Fatalf("activity.RegisterListeners() error = %v", err)
	}
	origin := seedTaskRunningOrigin(t, ctx, pool, fixture)
	answered := askAndAnswerDeploymentTarget(t, ctx, pool, questions, principal, origin)
	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`,
		fixture.AgentA, fixture.UserA)
	runsBefore := countCompanyRuns(t, ctx, pool, fixture.CompanyA)
	answeredHistory := countActivity(t, ctx, pool, EventQuestionResolved, answered.ID)
	if answeredHistory != 1 {
		t.Fatalf("question.resolved Activity rows after the answer = %d, want 1", answeredHistory)
	}

	frames := recordTopic[Question](t, bus, TopicQuestion)
	toasts := &recordedToasts{}
	worker, err := NewLifecycleWorker(store, bus, toasts)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	assertAbandonedReceipt(t, ctx, pool, "questions", answered.ID, answered.Revision+1, abandonAgentNotBound)
	if after := countCompanyRuns(t, ctx, pool, fixture.CompanyA); after != runsBefore {
		t.Fatalf("Runs after an unbound continuation = %d, want %d", after, runsBefore)
	}
	resolved := frames.named(EventQuestionResolved)
	if len(resolved) != 1 || resolved[0].Payload.Revision != answered.Revision+1 ||
		resolved[0].Payload.ContinuationRunID != nil {
		t.Fatalf("abandonment frames = %+v, want one question.resolved at revision %d with no Run",
			resolved, answered.Revision+1)
	}
	notice := readAbandonedContinuationNotice(t, ctx, pool, fixture.UserA, "question", answered.ID)
	if notice.conversationID != fixture.ConversationA ||
		!strings.Contains(notice.body, "not set up on your computer") ||
		!strings.Contains(notice.body, "reply in the task's chat") ||
		!strings.Contains(notice.deepLink, "taskId="+fixture.TaskA.String()) ||
		!strings.Contains(notice.deepLink, "runId="+origin.RunID.String()) {
		t.Fatalf("abandoned continuation notice = %+v", notice)
	}
	// Like every other run_failed item, it raises one desktop toast after
	// commit, to the owner alone.
	if shown := toasts.all(); len(shown) != 1 || shown[0].Type != inbox.TypeRunFailed ||
		shown[0].RecipientID != fixture.UserA || shown[0].Body != notice.body {
		t.Fatalf("toasts = %+v, want one run_failed toast to the assignee", shown)
	}
	if after := countActivity(t, ctx, pool, EventQuestionResolved, answered.ID); after != answeredHistory {
		t.Fatalf("question.resolved Activity rows after abandonment = %d, want %d", after, answeredHistory)
	}
}

// The Task's assignee authorizes its continuation (ADR 0014), and every Task
// Run needs their project.tasks.execute on the Task's Project (ADR 0017).
// When they lose it between the question and the answer, the enqueue kernel
// refuses the Run: the continuation is abandoned with reason
// project_access_revoked, no Run is written, the Task is not moved, and the
// assignee — who still reads the Project — is told what to ask for.
func TestLifecycleReconcilerAbandonsAContinuationTheAssigneeMayNoLongerRun(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, bus, questions := newQuestionLifecycleFixture(t, pool)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedTaskRunningOrigin(t, ctx, pool, fixture)
	answered := askAndAnswerDeploymentTarget(t, ctx, pool, questions, principal, origin)
	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM project_grants
		WHERE company_id=$1 AND project_id=$2 AND user_id=$3 AND permission='project.tasks.execute'`,
		fixture.CompanyA, fixture.ProjectA, fixture.UserA)
	runsBefore := countCompanyRuns(t, ctx, pool, fixture.CompanyA)
	var taskVersion int64
	if err := pool.QueryRow(ctx, `SELECT version FROM tasks WHERE id=$1`, fixture.TaskA).Scan(&taskVersion); err != nil {
		t.Fatal(err)
	}

	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	assertAbandonedReceipt(t, ctx, pool, "questions", answered.ID, answered.Revision+1, abandonProjectAccessRevoked)
	if after := countCompanyRuns(t, ctx, pool, fixture.CompanyA); after != runsBefore {
		t.Fatalf("Runs after a continuation the assignee may not run = %d, want %d", after, runsBefore)
	}
	var versionAfter int64
	if err := pool.QueryRow(ctx, `SELECT version FROM tasks WHERE id=$1`, fixture.TaskA).Scan(&versionAfter); err != nil {
		t.Fatal(err)
	}
	if versionAfter != taskVersion {
		t.Fatalf("Task version after the refused continuation = %d, want %d (the savepoint discards the move)",
			versionAfter, taskVersion)
	}
	notice := readAbandonedContinuationNotice(t, ctx, pool, fixture.UserA, "question", answered.ID)
	if !strings.Contains(notice.body, "no longer includes running tasks") ||
		!strings.Contains(notice.body, "reply in the task's chat") ||
		!strings.Contains(notice.deepLink, "runId="+origin.RunID.String()) {
		t.Fatalf("abandoned continuation notice = %+v", notice)
	}
}

// A Task that no longer takes the continuation says why, and only when the
// assignee can do something about it. Closing the Task was their own
// decision, so a done or cancelled Task raises nothing; a failed Task, one
// waiting on a prerequisite, and one in an archived Project each get copy
// that is true of them — none of them is "run the task again", which the
// server refuses for a failed Task and for one waiting on another.
func TestLifecycleReconcilerExplainsOnlyATaskTheAssigneeCanStillMove(t *testing.T) {
	for _, test := range []struct {
		name   string
		setup  func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture dbtest.Fixture)
		reason continuationAbandonReason
		body   string
	}{
		{
			name: "done",
			setup: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture dbtest.Fixture) {
				dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status='done' WHERE id=$1`, fixture.TaskA)
			},
			reason: abandonTaskClosed,
		},
		{
			name: "cancelled",
			setup: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture dbtest.Fixture) {
				dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status='cancelled' WHERE id=$1`, fixture.TaskA)
			},
			reason: abandonTaskClosed,
		},
		{
			name: "failed",
			setup: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture dbtest.Fixture) {
				dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status='failed' WHERE id=$1`, fixture.TaskA)
			},
			reason: abandonTaskFailed,
			body: "This task failed, so Agent A could not continue after “Deployment target”. " +
				"Reply in its chat to run Agent A again.",
		},
		{
			name: "waiting on a prerequisite",
			setup: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture dbtest.Fixture) {
				conversationID, prerequisiteID := dbtest.NewUUID(t), dbtest.NewUUID(t)
				dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
					dbtest.MustExecTx(t, ctx, tx, `
						INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
						VALUES ($1, $2, 'task', $3, 'Prerequisite', (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
						conversationID, fixture.CompanyA, prerequisiteID)
					dbtest.MustExecTx(t, ctx, tx, `
						INSERT INTO tasks
							(id, company_id, conversation_id, project_id, milestone_type,
							 number, key, title, assignee_user_id, creator_user_id)
						VALUES ($1, $2, $3, $4, 'product', 2, 'prerequisite', 'Prerequisite', $5, $5)`,
						prerequisiteID, fixture.CompanyA, conversationID, fixture.ProjectA, fixture.UserA)
					dbtest.MustExecTx(t, ctx, tx, `
						INSERT INTO task_dependencies
							(company_id, project_id, dependent_task_id, prerequisite_task_id)
						VALUES ($1, $2, $3, $4)`,
						fixture.CompanyA, fixture.ProjectA, fixture.TaskA, prerequisiteID)
				})
			},
			reason: abandonTaskDependencyBlocked,
			body: "This task is waiting on a task it depends on, so Agent A could not continue after " +
				"“Deployment target”. When that task is done, reply in this task's chat.",
		},
		{
			name: "archived Project",
			setup: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture dbtest.Fixture) {
				dbtest.MustExec(t, ctx, pool, `UPDATE projects SET archived_at=now() WHERE id=$1`, fixture.ProjectA)
			},
			reason: abandonProjectArchived,
			body:   "This task's project is archived, so Agent A could not continue after “Deployment target”.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, _ := dbtest.StartPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatalf("MigrateUp() error = %v", err)
			}
			fixture := dbtest.SeedFixture(t, ctx, pool)
			store, bus, questions := newQuestionLifecycleFixture(t, pool)
			principal := auth.UserPrincipal{
				UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
			}
			origin := seedTaskRunningOrigin(t, ctx, pool, fixture)
			answered := askAndAnswerDeploymentTarget(t, ctx, pool, questions, principal, origin)
			test.setup(t, ctx, pool, fixture)
			runsBefore := countCompanyRuns(t, ctx, pool, fixture.CompanyA)

			worker, err := NewLifecycleWorker(store, bus, nil)
			if err != nil {
				t.Fatalf("NewLifecycleWorker() error = %v", err)
			}
			if err := worker.Reconcile(ctx); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}

			assertAbandonedReceipt(t, ctx, pool, "questions", answered.ID, answered.Revision+1, test.reason)
			if after := countCompanyRuns(t, ctx, pool, fixture.CompanyA); after != runsBefore {
				t.Fatalf("Runs after an abandoned continuation = %d, want %d", after, runsBefore)
			}
			if test.body == "" {
				if count := countContinuationNotices(
					t,
					ctx,
					pool,
					"question",
					answered.ID,
				); count != 0 {
					t.Fatalf("Inbox items after the member closed the Task = %d, want 0", count)
				}
				return
			}
			notice := readAbandonedContinuationNotice(t, ctx, pool, fixture.UserA, "question", answered.ID)
			if notice.body != test.body {
				t.Fatalf("notice body = %q, want %q", notice.body, test.body)
			}
		})
	}
}

// The same silence in a direct chat: its creator is the owner, and they are
// told to set the Agent up and ask again.
func TestLifecycleReconcilerTellsTheChatOwnerWhenTheAgentIsNotSetUp(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, bus, questions := newQuestionLifecycleFixture(t, pool)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
	answered := askAndAnswerDeploymentTarget(t, ctx, pool, questions, principal, origin)
	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`,
		fixture.AgentA, fixture.UserA)

	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	assertAbandonedReceipt(t, ctx, pool, "questions", answered.ID, answered.Revision+1, abandonAgentNotBound)
	notice := readAbandonedContinuationNotice(t, ctx, pool, fixture.UserA, "question", answered.ID)
	if notice.conversationID != fixture.DirectConversationA ||
		!strings.Contains(notice.body, "not set up on your computer") ||
		!strings.Contains(notice.body, "ask again") ||
		!strings.Contains(notice.deepLink, "conversationKind=direct") ||
		!strings.Contains(notice.deepLink, "runId="+origin.RunID.String()) {
		t.Fatalf("abandoned continuation notice = %+v", notice)
	}
}

// The Approval reconciler is the Question one's twin: an Approval whose owner
// has no binding is abandoned with the same reason, re-sent at a new
// revision, and its owner told.
func TestLifecycleReconcilerTellsTheOwnerWhenAnApprovalCannotContinue(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	approvals, err := NewApprovalService(store, bus, &planActionParticipantStub{}, nil)
	if err != nil {
		t.Fatalf("NewApprovalService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedTaskRunningOrigin(t, ctx, pool, fixture)
	requested, _, err := approvals.RequestCompanyPlanApply(
		ctx, principal, origin, uuid.Must(uuid.NewV7()),
		json.RawMessage(`{"artifactVersion":1,"desired":{"tasks":[]}}`),
	)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, origin.RunID)
	approved, err := approvals.Approve(ctx, principal, requested.ID, ApprovalDecisionInput{
		ExpectedRevision: requested.Revision,
	})
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`,
		fixture.AgentA, fixture.UserA)

	frames := recordTopic[Approval](t, bus, Topic)
	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	assertAbandonedReceipt(t, ctx, pool, "approvals", approved.ID, approved.Revision+1, abandonAgentNotBound)
	resolved := frames.named(EventApprovalResolved)
	if len(resolved) != 1 || resolved[0].Payload.Revision != approved.Revision+1 ||
		resolved[0].Payload.ContinuationRunID != nil {
		t.Fatalf("abandonment frames = %+v, want one approval.resolved at revision %d with no Run",
			resolved, approved.Revision+1)
	}
	notice := readAbandonedContinuationNotice(t, ctx, pool, fixture.UserA, "approval", approved.ID)
	if notice.conversationID != fixture.ConversationA ||
		!strings.Contains(notice.body, "not set up on your computer") ||
		!strings.Contains(notice.body, "reply in the task's chat") {
		t.Fatalf("abandoned Approval continuation notice = %+v", notice)
	}
}

// Delivering an Approval continuation stamps the Run on the Approval, so its
// revision moves and approval.resolved carries the Run id to every client.
func TestLifecycleReconcilerAnnouncesTheDeliveredApprovalContinuation(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	approvals, err := NewApprovalService(store, bus, &planActionParticipantStub{}, nil)
	if err != nil {
		t.Fatalf("NewApprovalService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedTaskRunningOrigin(t, ctx, pool, fixture)
	requested, _, err := approvals.RequestCompanyPlanApply(
		ctx, principal, origin, uuid.Must(uuid.NewV7()),
		json.RawMessage(`{"artifactVersion":1,"desired":{"tasks":[]}}`),
	)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, origin.RunID)
	approved, err := approvals.Approve(ctx, principal, requested.ID, ApprovalDecisionInput{
		ExpectedRevision: requested.Revision,
	})
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}

	frames := recordTopic[Approval](t, bus, Topic)
	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var status string
	var continuationRunID uuid.NullUUID
	var revision int64
	if err := pool.QueryRow(ctx, `
		SELECT continuation_status, continuation_run_id, version
		FROM approvals WHERE id=$1`, approved.ID).
		Scan(&status, &continuationRunID, &revision); err != nil {
		t.Fatalf("read delivered Approval receipt: %v", err)
	}
	if status != "delivered" || !continuationRunID.Valid || revision != approved.Revision+1 {
		t.Fatalf("delivered Approval receipt = %q/%v revision %d, want delivered with a Run at %d",
			status, continuationRunID, revision, approved.Revision+1)
	}
	resolved := frames.named(EventApprovalResolved)
	if len(resolved) != 1 || resolved[0].Payload.Revision != revision ||
		resolved[0].Payload.ContinuationRunID == nil ||
		*resolved[0].Payload.ContinuationRunID != continuationRunID.UUID {
		t.Fatalf("delivery frames = %+v, want one approval.resolved at revision %d naming Run %s",
			resolved, revision, continuationRunID.UUID)
	}
}

// The owner left the Company after answering: their bindings went with them,
// so the continuation cannot run, and there is nobody left to tell. The
// Inbox writes only to active members, so the pass skips the item rather
// than letting it refuse and roll the abandonment back — the receipt settles,
// the pass returns nil, and nothing stays due behind it.
func TestLifecycleReconcilerAbandonsQuietlyWhenTheOwnerHasLeft(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, bus, questions := newQuestionLifecycleFixture(t, pool)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
	answered := askAndAnswerDeploymentTarget(t, ctx, pool, questions, principal, origin)
	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`,
		fixture.AgentA, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members SET status='revoked', revoked_at=now()
		WHERE company_id=$1 AND user_id=$2`,
		fixture.CompanyA, fixture.UserA)

	frames := recordTopic[Question](t, bus, TopicQuestion)
	toasts := &recordedToasts{}
	worker, err := NewLifecycleWorker(store, bus, toasts)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile(second pass) error = %v", err)
	}

	assertAbandonedReceipt(t, ctx, pool, "questions", answered.ID, answered.Revision+1, abandonAgentNotBound)
	if count := countContinuationNotices(t, ctx, pool, "question", answered.ID); count != 0 {
		t.Fatalf("Inbox items to a former member = %d, want 0", count)
	}
	if shown := toasts.all(); len(shown) != 0 {
		t.Fatalf("toasts to a former member = %+v, want none", shown)
	}
	if resolved := frames.named(EventQuestionResolved); len(resolved) != 1 ||
		resolved[0].Payload.Revision != answered.Revision+1 {
		t.Fatalf("abandonment frames = %+v, want one question.resolved at revision %d",
			resolved, answered.Revision+1)
	}
}

// A channel tag's continuation belongs to the member whose tag started it,
// and its channel already shows the tag blocked when it cannot go on, so
// abandoning it raises no Inbox item: the tag itself is closed instead.
func TestLifecycleReconcilerClosesTheChannelTagWithoutAnInboxItem(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, bus, questions := newQuestionLifecycleFixture(t, pool)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET created_by_user_id=$2 WHERE id=$1`,
		fixture.AgentA, fixture.UserA)
	conversations, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: store, Bus: bus,
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check: func(context.Context, *generated.Queries, generated.Task) (bool, error) {
				return true, nil
			},
			Prepare: func(
				context.Context, *generated.Queries, *[]events.Event, uuid.UUID, generated.Task,
			) (bool, error) {
				return true, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewConversationService() error = %v", err)
	}
	discussion, err := conversation.NewDiscussionService(conversation.DiscussionServiceOptions{
		Store: store, Bus: bus,
	})
	if err != nil {
		t.Fatalf("NewDiscussionService() error = %v", err)
	}
	channel, err := conversations.CreateChannel(ctx, principal, conversation.CreateChannelInput{
		RequestKey: uuid.New(), Name: "Agent requests", AgentIDs: []uuid.UUID{fixture.AgentA},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	tag, err := json.Marshal(map[string]any{
		"type": "agent", "agentId": fixture.AgentA, "start": 0, "end": 6,
	})
	if err != nil {
		t.Fatalf("encode Agent tag: %v", err)
	}
	sent, err := discussion.Send(ctx, principal, channel.Conversation.ID, conversation.DiscussionSendInput{
		RequestKey: uuid.New(),
		Body: conversation.DiscussionBody{
			Version: 1, Markdown: "@Agent ", References: []json.RawMessage{tag},
		},
	})
	if err != nil {
		t.Fatalf("Send(tag) error = %v", err)
	}
	var invocationID, runID, agentConversationID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT channel_invocations.id, runs.id, runs.conversation_id
		FROM channel_invocations
		JOIN runs ON runs.invocation_id = channel_invocations.id
		WHERE channel_invocations.trigger_message_id=$1`, sent.Message.ID).
		Scan(&invocationID, &runID, &agentConversationID); err != nil {
		t.Fatalf("read the tag's invocation and Run: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='running', updated_at=now() WHERE id=$1`, runID)
	origin := execution.RunOrigin{
		RunID: runID, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		ConversationID: agentConversationID, ConversationKind: "channel_agent",
		OriginUserID: &fixture.UserA,
	}
	answered := askAndAnswerDeploymentTarget(t, ctx, pool, questions, principal, origin)
	// The Run ended to wait for the answer, which is what parks its tag.
	dbtest.MustExec(t, ctx, pool, `UPDATE channel_invocations SET status='waiting_input' WHERE id=$1`,
		invocationID)
	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`,
		fixture.AgentA, fixture.UserA)

	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	assertAbandonedReceipt(t, ctx, pool, "questions", answered.ID, answered.Revision+1, abandonAgentNotBound)
	if count := countContinuationNotices(t, ctx, pool, "question", answered.ID); count != 0 {
		t.Fatalf("Inbox items for a channel tag's continuation = %d, want 0", count)
	}
	var status, publication string
	if err := pool.QueryRow(ctx, `
		SELECT status, publication_status FROM channel_invocations WHERE id=$1`, invocationID).
		Scan(&status, &publication); err != nil {
		t.Fatalf("read the tag's invocation: %v", err)
	}
	if status != "cancelled" || publication != "blocked" {
		t.Fatalf("channel tag after an abandoned continuation = %s/%s, want cancelled/blocked",
			status, publication)
	}
}

// askAndAnswerDeploymentTarget raises the one-prompt Question every
// continuation test uses from origin, ends that Run, and answers it — the
// state in which the reconciler finds a due receipt.
func askAndAnswerDeploymentTarget(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	questions QuestionService,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
) Question {
	t.Helper()
	asked, _, err := questions.Ask(ctx, principal, origin, uuid.Must(uuid.NewV7()), QuestionAskInput{
		Title: "Deployment target",
		Prompts: []QuestionPrompt{{
			ID:            "target",
			Prompt:        "Which target?",
			SelectionMode: QuestionSelectionSingle,
			Required:      true,
			Options: []QuestionOption{
				{ID: "staging", Label: "Staging"},
				{ID: "production", Label: "Production"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, origin.RunID)
	answered, err := questions.Answer(ctx, principal, asked.ID, QuestionAnswerInput{
		ExpectedRevision: asked.Revision,
		Answers:          []QuestionAnswer{{PromptID: "target", OptionIDs: []string{"staging"}}},
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	return answered
}

// assertAbandonedReceipt pins an abandoned receipt: the reason names the
// cause, no Run is named, and the revision moved past the decision's.
func assertAbandonedReceipt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	table string,
	id uuid.UUID,
	wantRevision int64,
	wantReason continuationAbandonReason,
) {
	t.Helper()
	var status, reason string
	var continuationRunID uuid.NullUUID
	var revision int64
	if err := pool.QueryRow(ctx, `
		SELECT continuation_status, continuation_abandon_reason, continuation_run_id, version
		FROM `+table+` WHERE id=$1`, id).
		Scan(&status, &reason, &continuationRunID, &revision); err != nil {
		t.Fatalf("read abandoned %s receipt: %v", table, err)
	}
	if status != "abandoned" || reason != string(wantReason) || continuationRunID.Valid ||
		revision != wantRevision {
		t.Fatalf("abandoned %s receipt = %q/%q/%v revision %d, want abandoned/%s, no Run, revision %d",
			table, status, reason, continuationRunID, revision, wantReason, wantRevision)
	}
}

type abandonedContinuationNotice struct {
	conversationID uuid.UUID
	body           string
	deepLink       string
}

// readAbandonedContinuationNotice reads the one run_failed item an abandoned
// continuation raised for recipient about the request it continued.
func readAbandonedContinuationNotice(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	recipientUserID uuid.UUID,
	entityType string,
	entityID uuid.UUID,
) abandonedContinuationNotice {
	t.Helper()
	var count int
	var notice abandonedContinuationNotice
	if err := pool.QueryRow(ctx, `
		SELECT count(*) OVER (), conversation_id, body, deep_link
		FROM inbox_items
		WHERE recipient_user_id=$1 AND kind='run_failed'
		  AND entity_type=$2 AND entity_id=$3`,
		recipientUserID, entityType, entityID.String()).
		Scan(&count, &notice.conversationID, &notice.body, &notice.deepLink); err != nil {
		t.Fatalf("read abandoned continuation Inbox item: %v", err)
	}
	if count != 1 {
		t.Fatalf("abandoned continuation Inbox items = %d, want 1", count)
	}
	return notice
}

// countContinuationNotices counts the run_failed items any member received
// about one request's continuation.
func countContinuationNotices(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	entityType string,
	entityID uuid.UUID,
) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_items
		WHERE kind='run_failed' AND entity_type=$1 AND entity_id=$2`,
		entityType, entityID.String()).Scan(&count); err != nil {
		t.Fatalf("count continuation Inbox items: %v", err)
	}
	return count
}

// countActivity counts the Company history rows recording one action on one
// request.
func countActivity(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	action string,
	entityID uuid.UUID,
) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM activity_events WHERE action=$1 AND entity_id=$2`,
		action, entityID.String()).Scan(&count); err != nil {
		t.Fatalf("count %s Activity: %v", action, err)
	}
	return count
}

// recordedToasts is an inbox.Presenter that keeps what it was asked to show.
type recordedToasts struct {
	mu    sync.Mutex
	items []inbox.Notification
}

func (r *recordedToasts) Present(notification inbox.Notification) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, notification)
}

func (r *recordedToasts) all() []inbox.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]inbox.Notification(nil), r.items...)
}

func newQuestionLifecycleFixture(
	t *testing.T,
	pool *pgxpool.Pool,
) (*db.Store, *events.Bus, QuestionService) {
	t.Helper()
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	questions, err := NewQuestionService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewQuestionService() error = %v", err)
	}
	return store, bus, questions
}

func countCompanyRuns(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE company_id=$1`, companyID).
		Scan(&count); err != nil {
		t.Fatalf("count company Runs: %v", err)
	}
	return count
}

func TestLifecycleReconcilerExpiresAndClosesPendingQuestion(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	questions, err := NewQuestionService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewQuestionService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
	asked, _, err := questions.Ask(ctx, principal, origin, uuid.Must(uuid.NewV7()), QuestionAskInput{
		Title: "Need a note",
		Prompts: []QuestionPrompt{{
			ID: "note", Prompt: "What should I know?", SelectionMode: QuestionSelectionSingle,
			Required: true, Options: []QuestionOption{},
		}},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, origin.RunID)
	dbtest.MustExec(t, ctx, pool, `UPDATE questions SET expires_at=now()-interval '1 minute' WHERE id=$1`, asked.ID)

	worker, err := NewLifecycleWorker(store, bus, nil)
	if err != nil {
		t.Fatalf("NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	var status, continuationStatus string
	var openInbox int
	if err := pool.QueryRow(ctx, `
		SELECT status, continuation_status FROM questions WHERE id=$1`, asked.ID).
		Scan(&status, &continuationStatus); err != nil {
		t.Fatalf("read expired Question: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_items
		WHERE company_id=$1 AND entity_type='question' AND entity_id=$2
		  AND archived_at IS NULL`, fixture.CompanyA, asked.ID.String()).Scan(&openInbox); err != nil {
		t.Fatalf("count open Question Inbox: %v", err)
	}
	if status != "expired" || continuationStatus != "delivered" || openInbox != 0 {
		t.Fatalf("expired Question = %q/%q openInbox=%d", status, continuationStatus, openInbox)
	}
}

// continuationWakeups records the work-available facts a continuation enqueue
// publishes. It replaces a notifier the worker used to hold: the wakeup is a
// fact on the bus now, so the test reads it where a daemon would.
type continuationWakeups struct {
	runs []uuid.UUID
}

func recordContinuationWakeups(t *testing.T, bus *events.Bus) *continuationWakeups {
	t.Helper()
	recorded := &continuationWakeups{}
	if err := bus.Subscribe(execution.TopicRuntime,
		func(_ context.Context, event events.Event) error {
			if event.Name != execution.EventRuntimeWorkAvailable {
				return nil
			}
			work, err := events.PayloadOf[execution.RuntimeWork](event)
			if err != nil {
				return err
			}
			runID, err := uuid.Parse(work.RunID)
			if err != nil {
				return err
			}
			recorded.runs = append(recorded.runs, runID)
			return nil
		}); err != nil {
		t.Fatalf("subscribe runtime.work_available: %v", err)
	}
	return recorded
}

func TestStudioSetupQuestionContinuesWithNewCapabilities(t *testing.T) {
	for _, enabledBefore := range []bool{false, true} {
		name := "enable and connect"
		if enabledBefore {
			name = "reconnect enabled Agent"
		}
		t.Run(name, func(t *testing.T) {
			pool, _ := dbtest.StartPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatalf("MigrateUp() error = %v", err)
			}
			fixture := dbtest.SeedFixture(t, ctx, pool)
			store, bus, questions := newQuestionLifecycleFixture(t, pool)
			principal := auth.UserPrincipal{
				UserID:    fixture.UserA,
				CompanyID: fixture.CompanyA,
				Role:      auth.CompanyRoleOwner,
			}
			origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
			if enabledBefore {
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`UPDATE agents SET settings = jsonb_set(settings, '{capabilities}',
			'{"enabledTools":["zenith-studio"]}'::jsonb) WHERE id=$1`,
					fixture.AgentA,
				)
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`UPDATE runs SET studio_enabled=true WHERE id=$1`,
					origin.RunID,
				)
			}
			key := uuid.New()
			input := QuestionAskInput{Title: "Enable Zenith Studio", Prompts: []QuestionPrompt{
				{
					ID:            "studio",
					Prompt:        "Connect to Zenith Studio",
					SelectionMode: QuestionSelectionSingle,
					Required:      true,
					ToolSetup:     QuestionToolSetupZenithStudio,
				},
			}}
			asked, created, err := questions.Ask(ctx, principal, origin, key, input)
			if err != nil || !created {
				t.Fatalf("Ask() = %+v, %t, %v", asked, created, err)
			}
			stored, err := questions.Get(ctx, principal, asked.ID)
			if err != nil || stored.Prompts[0].ToolSetup != QuestionToolSetupZenithStudio ||
				stored.OriginRunID != origin.RunID {
				t.Fatalf("stored setup request = %+v, %v", stored, err)
			}
			replay, created, err := questions.Ask(ctx, principal, origin, key, input)
			if err != nil || created || replay.ID != asked.ID {
				t.Fatalf("Ask(replay) = %+v, %t, %v", replay, created, err)
			}
			input.Prompts[0].ToolSetup = ""
			if _, _, err := questions.Ask(
				ctx,
				principal,
				origin,
				key,
				input,
			); !errors.Is(
				err,
				ErrQuestionConflict,
			) {
				t.Fatalf("Ask(changed setup metadata) error = %v, want conflict", err)
			}
			// The continuation reads current settings; the origin retains its snapshot.
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE agents SET settings = jsonb_set(settings, '{capabilities}',
		'{"enabledTools":["zenith-studio"]}'::jsonb) WHERE id=$1`,
				fixture.AgentA,
			)
			ready := "Zenith Studio is enabled and connected. Continue my original request."
			if _, err := questions.Answer(ctx, principal, asked.ID, QuestionAnswerInput{
				ExpectedRevision: asked.Revision,
				Answers:          []QuestionAnswer{{PromptID: "studio", Text: &ready}},
			}); err != nil {
				t.Fatalf("Answer() error = %v", err)
			}
			worker, err := NewLifecycleWorker(store, bus, nil)
			if err != nil {
				t.Fatalf("NewLifecycleWorker() error = %v", err)
			}
			if err := worker.Reconcile(ctx); err != nil {
				t.Fatalf("Reconcile(live origin) error = %v", err)
			}
			waiting, err := questions.Get(ctx, principal, asked.ID)
			if err != nil || waiting.ContinuationRunID != nil {
				t.Fatalf("live origin continuation = %+v, %v", waiting, err)
			}
			dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, origin.RunID)
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE questions SET continuation_next_attempt_at=now() WHERE id=$1`,
				asked.ID,
			)
			if err := worker.Reconcile(ctx); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if err := worker.Reconcile(ctx); err != nil {
				t.Fatalf("Reconcile(replay) error = %v", err)
			}
			resolved, err := questions.Get(ctx, principal, asked.ID)
			if err != nil || resolved.ContinuationRunID == nil {
				t.Fatalf("resolved setup request = %+v, %v", resolved, err)
			}
			original, err := store.Queries().GetRun(ctx, origin.RunID)
			if err != nil || original.StudioEnabled != enabledBefore {
				t.Fatalf("origin Studio authority = %t, %v", original.StudioEnabled, err)
			}
			next, err := store.Queries().GetRun(ctx, *resolved.ContinuationRunID)
			if err != nil || !next.StudioEnabled || next.ParentRunID.Valid ||
				!strings.Contains(string(next.Context), ready) {
				t.Fatalf("continuation = %+v, %v", next, err)
			}
			var count int
			if err := pool.QueryRow(
				ctx,
				`SELECT count(*) FROM runs WHERE conversation_id=$1`,
				origin.ConversationID,
			).Scan(&count); err != nil || count != 2 {
				t.Fatalf("Run count = %d, %v, want origin plus one continuation", count, err)
			}
		})
	}
}
