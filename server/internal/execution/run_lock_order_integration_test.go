//go:build integration

package execution

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type runSweepResult struct {
	batch FailedRunBatch
	err   error
}

type runCompletionResult struct {
	run *generated.Run
	err error
}

func TestCompleteRunLocksCompanyBeforeTerminalConversationAndRun(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)
	seededRun, err := h.queries.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("load seeded Run: %v", err)
	}

	holder, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Company lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := generated.New(holder).LockCompanyForMembershipMutation(
		ctx,
		h.fixture.CompanyA,
	); err != nil {
		t.Fatalf("lock Company: %v", err)
	}

	done := make(chan runCompletionResult, 1)
	go func() {
		run, err := h.svc.CompleteRun(ctx, runID, emptyTerminalProof(), "", false, "")
		done <- runCompletionResult{run: run, err: err}
	}()
	waitForCompanyLockWait(t, ctx, h, done)

	checker, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin terminal lock checker: %v", err)
	}
	if _, err := checker.Exec(
		ctx, `
		SELECT id FROM conversations WHERE id = $1 FOR NO KEY UPDATE NOWAIT`,
		seededRun.ConversationID,
	); err != nil {
		t.Fatalf("CompleteRun locked Conversation before Company: %v", err)
	}
	if _, err := checker.Exec(ctx, `SELECT id FROM runs WHERE id = $1 FOR UPDATE NOWAIT`, runID); err != nil {
		t.Fatalf("CompleteRun locked Run before Company: %v", err)
	}
	if err := checker.Rollback(context.Background()); err != nil {
		t.Fatalf("release terminal lock checker: %v", err)
	}

	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("release Company lock: %v", err)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("CompleteRun() error = %v", result.err)
	}
	if result.run == nil || result.run.Status != "completed" {
		t.Fatalf("CompleteRun() = %+v, want completed", result.run)
	}
}

func waitForCompanyLockWait(
	t *testing.T,
	ctx context.Context,
	h *runHarness,
	done <-chan runCompletionResult,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case result := <-done:
			t.Fatalf("CompleteRun bypassed held Company: run=%+v err=%v", result.run, result.err)
		default:
		}
		var waiting bool
		if err := h.pool.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1
			    FROM pg_stat_activity
			    WHERE datname = current_database()
			      AND state = 'active'
			      AND wait_event_type = 'Lock'
			      AND query LIKE '%LockCompanyForMembershipRead%'
			)`).Scan(&waiting); err != nil {
			t.Fatalf("observe blocked Run completion: %v", err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("CompleteRun did not reach the held Company lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFailRunsForOfflineRuntimesLocksConversationBeforeRun(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conversationID := h.seedDirectConversation(t)
	runID := h.seedQueuedRun(t, conversationID)
	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil {
		t.Fatalf("claim Run: %v", err)
	}
	if claimed == nil || claimed.ID != runID {
		t.Fatalf("claimed Run = %+v, want Run %d", claimed, runID)
	}
	if _, err := h.queries.SetRuntimeOffline(ctx, h.fixture.RuntimeA); err != nil {
		t.Fatalf("mark Runtime offline: %v", err)
	}

	holder, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Conversation lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := generated.New(holder).LockConversationForSystemRunQueueMutation(
		ctx,
		generated.LockConversationForSystemRunQueueMutationParams{
			ConversationID: conversationID,
			CompanyID:      h.fixture.CompanyA,
		},
	); err != nil {
		t.Fatalf("lock Conversation: %v", err)
	}

	done := make(chan runSweepResult, 1)
	go func() {
		batch, err := h.svc.FailRunsForOfflineRuntimes(ctx)
		done <- runSweepResult{batch: batch, err: err}
	}()

	waitForConversationLockWait(t, ctx, h, done)

	checker, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Run lock checker: %v", err)
	}
	var checkedRunID uuid.UUID
	checkErr := checker.QueryRow(
		ctx,
		`SELECT id FROM runs WHERE id = $1 FOR UPDATE NOWAIT`,
		runID,
	).Scan(&checkedRunID)
	_ = checker.Rollback(context.Background())
	if checkErr != nil {
		t.Fatalf("Run was locked before the canonical Conversation lock completed: %v", checkErr)
	}
	if checkedRunID != runID {
		t.Fatalf("checked Run id = %d, want %d", checkedRunID, runID)
	}

	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("release Conversation lock: %v", err)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("fail Runs for offline Runtimes: %v", result.err)
	}
	if len(result.batch.Runs) != 1 || result.batch.Runs[0].ID != runID {
		t.Fatalf("failed Runs = %+v, want Run %d", result.batch.Runs, runID)
	}
}

func waitForConversationLockWait(
	t *testing.T,
	ctx context.Context,
	h *runHarness,
	done <-chan runSweepResult,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case result := <-done:
			t.Fatalf("Run sweep bypassed held Conversation: batch=%+v err=%v", result.batch, result.err)
		default:
		}
		var waiting bool
		if err := h.pool.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1
			    FROM pg_stat_activity
			    WHERE datname = current_database()
			      AND state = 'active'
			      AND wait_event_type = 'Lock'
			      AND query LIKE '%LockConversationForSystemRunQueueMutation%'
			)`).Scan(&waiting); err != nil {
			t.Fatalf("observe blocked Run sweep: %v", err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Run sweep did not reach the held Conversation lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A channel tag's claim leaves the binding to SelectClaimableRun's fence and
// takes no lock on it. Bind and Unbind lock the member's binding and then
// their pending Runs; a claim that locked the Run and then waited for the
// binding could deadlock with them. Here a Bind holds the binding, and the
// claim must still go through at once.
func TestClaimOfAChannelTagTakesNoBindingLock(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedQueuedChannelTagRun(t, h)

	holder, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin binding lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := generated.New(holder).GetAgentRuntimeBindingForUserForUpdate(
		ctx,
		generated.GetAgentRuntimeBindingForUserForUpdateParams{
			CompanyID: h.fixture.CompanyA,
			AgentID:   h.fixture.AgentA,
			UserID:    h.fixture.UserA,
		},
	); err != nil {
		t.Fatalf("lock binding: %v", err)
	}

	claimCtx, claimCancel := context.WithTimeout(ctx, 10*time.Second)
	defer claimCancel()
	claimed, err := h.svc.ClaimRun(claimCtx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil {
		t.Fatalf("ClaimRun() while the binding is locked = %v, want no wait on the binding", err)
	}
	if claimed == nil || claimed.ID != runID || claimed.Status != "dispatched" {
		t.Fatalf("claimed Run = %+v, want Run %s dispatched", claimed, runID)
	}
}

// seedQueuedChannelTagRun writes an active tag of AgentA by the fixture owner
// in a channel, with its queued Run on the owner's RuntimeA.
func seedQueuedChannelTagRun(t *testing.T, h *runHarness) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	channelID := dbtest.SeedDiscussionChannel(
		t, ctx, h.pool, h.fixture.CompanyA, h.fixture.UserA, []uuid.UUID{h.fixture.AgentA},
	)
	var messageID uuid.UUID
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO conversation_items
			(company_id, conversation_id, seq, type, author_kind, author_id,
			 content, body, request_key, send_fingerprint)
		VALUES ($1, $2, 1, 'user_message', 'user', $3, '@Agent please',
			'{"markdown":"@Agent please"}'::jsonb, gen_random_uuid(),
			decode(repeat('11', 32), 'hex'))
		RETURNING id`, h.fixture.CompanyA, channelID, h.fixture.UserA).Scan(&messageID); err != nil {
		t.Fatalf("seed channel message: %v", err)
	}
	conversation, err := h.queries.EnsureChannelAgentConversation(
		ctx,
		generated.EnsureChannelAgentConversationParams{
			ChannelID: channelID, AgentID: h.fixture.AgentA, CompanyID: h.fixture.CompanyA,
		},
	)
	if err != nil {
		t.Fatalf("seed channel-Agent Conversation: %v", err)
	}
	invocation, err := h.queries.InsertChannelInvocation(ctx, generated.InsertChannelInvocationParams{
		ConversationID: conversation.ID, AgentID: h.fixture.AgentA, CallerUserID: h.fixture.UserA,
		MessageID: messageID, ChannelID: channelID, CompanyID: h.fixture.CompanyA,
	})
	if err != nil {
		t.Fatalf("seed channel tag: %v", err)
	}
	dbtest.MustExec(t, ctx, h.pool, `UPDATE channel_invocations SET status = 'active' WHERE id = $1`, invocation.ID)
	var runID uuid.UUID
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO runs
			(company_id, agent_id, conversation_id, runtime_id, run_type, status,
			 invocation_id, execution_authorized_by_user_id, context)
		VALUES ($1, $2, $3, $4, 'channel_agent', 'queued', $5, $6, '{}'::jsonb)
		RETURNING id`, h.fixture.CompanyA, h.fixture.AgentA, conversation.ID, h.fixture.RuntimeA,
		invocation.ID, h.fixture.UserA).Scan(&runID); err != nil {
		t.Fatalf("seed channel tag Run: %v", err)
	}
	return runID
}
