//go:build integration

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/telemetry"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

func TestCancelRunRevokesTokens(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	conversation := h.seedDirectConversation(t)
	runID := h.seedQueuedRun(t, conversation)
	tokenHash := []byte("0123456789abcdef0123456789abcdef")
	if _, err := h.queries.CreateRunToken(ctx, generated.CreateRunTokenParams{
		RunID:     runID,
		TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("create token: %v", err)
	}

	cancelled, err := h.svc.CancelRun(ctx, runID, "user cancelled")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", cancelled.Status)
	}
	if _, err := h.queries.GetRunTokenByHash(ctx, tokenHash); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("token lookup after cancel = %v, want ErrNoRows", err)
	}
	if h.recorder.topicCount(EventRunCancelled) != 1 {
		t.Fatalf("run.cancelled events = %d, want 1", h.recorder.topicCount(EventRunCancelled))
	}

	// Cancelling an already-terminal run is an idempotent success.
	again, err := h.svc.CancelRun(ctx, runID, "user cancelled")
	if err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
	if again.Status != "cancelled" {
		t.Fatalf("idempotent cancel status = %q", again.Status)
	}
}

func TestClaimRunCancelsUnavailableExactConnectorSelection(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()
	conversationID := h.seedDirectConversation(t)
	runID := h.seedQueuedRun(t, conversationID)
	h.svc.ConnectorSnapshotter = ConnectorSnapshotterFunc(func(
		context.Context,
		*generated.Queries,
		uuid.UUID,
		uuid.UUID,
	) (generated.Run, error) {
		return generated.Run{}, ErrRunConnectorSnapshotUnavailable
	})

	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil {
		t.Fatalf("ClaimRun() error = %v", err)
	}
	if claimed != nil {
		t.Fatalf("ClaimRun() = %+v, want no dispatched Run", claimed)
	}
	stored, err := h.queries.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if stored.Status != "cancelled" || !stored.Error.Valid ||
		stored.Error.String != CancellationConnectorUnavailable {
		t.Fatalf("Run after rejected Connector freeze = %+v", stored)
	}
	if h.recorder.topicCount(EventRunDispatched) != 0 ||
		h.recorder.topicCount(EventRunCancelled) != 1 {
		t.Fatalf("Run events = dispatched %d, cancelled %d; want 0/1",
			h.recorder.topicCount(EventRunDispatched),
			h.recorder.topicCount(EventRunCancelled))
	}
}

func TestClaimRunCancelsARetryWhoseOwnConnectorFreezeFails(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()
	conversationID := h.seedDirectConversation(t)
	parentID := h.seedQueuedRun(t, conversationID)
	// The parent froze a snapshot before it failed.
	if _, err := h.pool.Exec(ctx, `
		UPDATE runs SET status = 'failed', connector_snapshot_frozen_at = clock_timestamp(),
			context = COALESCE(context, '{}'::jsonb) || '{"connectorSnapshot":{"schemaVersion":1,"connectors":[]}}'::jsonb
		WHERE id = $1`, parentID); err != nil {
		t.Fatal(err)
	}
	retry, err := h.queries.CreateRetryRun(ctx, generated.CreateRetryRunParams{ParentRunID: parentID})
	if err != nil {
		t.Fatalf("CreateRetryRun() error = %v", err)
	}
	// The copied context is not a freeze of the retry's own.
	if retry.ConnectorSnapshotFrozenAt.Valid {
		t.Fatalf("retry inherited the snapshot marker: %+v", retry.ConnectorSnapshotFrozenAt)
	}
	h.svc.ConnectorSnapshotter = ConnectorSnapshotterFunc(func(
		context.Context,
		*generated.Queries,
		uuid.UUID,
		uuid.UUID,
	) (generated.Run, error) {
		return generated.Run{}, ErrRunConnectorSnapshotUnavailable
	})
	if claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA); err != nil || claimed != nil {
		t.Fatalf("ClaimRun() = %+v, %v; want no dispatched Run", claimed, err)
	}
	stored, err := h.queries.GetRun(ctx, retry.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if stored.Status != "cancelled" || stored.Error.String != CancellationConnectorUnavailable {
		t.Fatalf("retry after its own rejected Connector freeze = %+v", stored)
	}
}

func TestClaimRunSerializesPerConversation(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	conversation := h.seedDirectConversation(t)
	first := h.seedQueuedRun(t, conversation)
	second := h.seedQueuedRun(t, conversation)

	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if claimed == nil || claimed.ID != first {
		t.Fatalf("first claim = %+v, want run %d", claimed, first)
	}
	if claimed.Status != "dispatched" || !claimed.PrepareLeaseExpiresAt.Valid {
		t.Fatalf(
			"claimed run status = %q lease valid = %v, want dispatched with lease",
			claimed.Status,
			claimed.PrepareLeaseExpiresAt.Valid,
		)
	}
	if h.recorder.topicCount(EventRunDispatched) != 1 {
		t.Fatalf("run.dispatch events = %d, want 1", h.recorder.topicCount(EventRunDispatched))
	}
	dispatchEvent, ok := h.recorder.lastEvent(EventRunDispatched)
	// A direct conversation's run is its creator's, not the company's, and
	// the payload carries the facts that say so.
	if !ok || dispatchEvent.Payload.ConversationKind == "task" ||
		dispatchEvent.Payload.ConversationCreatedBy == nil ||
		*dispatchEvent.Payload.ConversationCreatedBy != h.fixture.UserA ||
		dispatchEvent.Payload.CompanyID != h.fixture.CompanyA {
		t.Fatalf("run.dispatch routing = %+v, want the direct conversation's creator", dispatchEvent.Payload)
	}

	// The conversation serialization arm blocks the second run while the
	// first is active.
	blocked, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil {
		t.Fatalf("blocked claim: %v", err)
	}
	if blocked != nil {
		t.Fatalf("blocked claim = %+v, want nil while sibling is active", blocked)
	}

	// A terminal transition frees the slot.
	if _, err := h.queries.StartRun(ctx, generated.StartRunParams{
		ID: first, RuntimeID: h.fixture.RuntimeA,
	}); err != nil {
		t.Fatalf("start first: %v", err)
	}
	h.svc.SetRunQueuePromoter(RunQueuePromoterFunc(func(
		context.Context,
		*generated.Queries,
		*[]events.Event,
		generated.Run,
		string,
	) (*uuid.UUID, error) {
		return nil, nil
	}))
	answer := appendTerminalTestItem(t, h, first, TranscriptItemInput{
		ItemKey: "claim-serialization-answer",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeText,
			Text: "done",
		}},
	})
	if _, err := h.svc.CompleteRun(
		ctx, first, terminalProof(answer, answer.ItemKey), "", false, "",
	); err != nil {
		t.Fatalf("complete first: %v", err)
	}
	next, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil {
		t.Fatalf("post-terminal claim: %v", err)
	}
	if next == nil || next.ID != second {
		t.Fatalf("post-terminal claim = %+v, want run %d", next, second)
	}
}

func TestClaimRunUnavailableSnapshotDoesNotCancelNewerDispatch(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conversationID := h.seedDirectConversation(t)
	runID := h.seedQueuedRun(t, conversationID)

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

	var freezeCalls atomic.Int32
	h.svc.ConnectorSnapshotter = ConnectorSnapshotterFunc(func(
		ctx context.Context,
		queries *generated.Queries,
		claimedRunID uuid.UUID,
		runtimeID uuid.UUID,
	) (generated.Run, error) {
		if freezeCalls.Add(1) == 1 {
			return generated.Run{}, ErrRunConnectorSnapshotUnavailable
		}
		// The real freeze, as the composition root wires it from the agent
		// domain: the reviewed Connector contract for the claimed Run, with
		// discovery older than ten minutes treated as unavailable. Spelled
		// out here rather than imported, because internal/agent imports this
		// package (runtime removal retargets pending runs through it) and Go
		// counts a test file's import in the cycle.
		run, err := queries.FreezeRunConnectorSnapshot(ctx, generated.FreezeRunConnectorSnapshotParams{
			DiscoveryStaleAfterMilliseconds: (10 * time.Minute).Milliseconds(),
			RunID:                           claimedRunID,
			RuntimeID:                       runtimeID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return generated.Run{}, ErrRunConnectorSnapshotUnavailable
		}
		return run, err
	})

	type claimResult struct {
		run *generated.Run
		err error
	}
	firstDone := make(chan claimResult, 1)
	go func() {
		run, claimErr := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
		firstDone <- claimResult{run: run, err: claimErr}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case result := <-firstDone:
			t.Fatalf("unavailable claim bypassed held Conversation: run=%+v err=%v", result.run, result.err)
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
			t.Fatalf("observe blocked unavailable-snapshot cancellation: %v", err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unavailable-snapshot cancellation did not reach the held Conversation lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	newer, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil {
		t.Fatalf("newer ClaimRun() error = %v", err)
	}
	if newer == nil || newer.ID != runID || newer.Status != "dispatched" {
		t.Fatalf("newer ClaimRun() = %+v, want dispatched Run %d", newer, runID)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("release Conversation lock: %v", err)
	}

	first := <-firstDone
	if first.err != nil || first.run != nil {
		t.Fatalf("unavailable ClaimRun() = %+v, %v; want nil, nil", first.run, first.err)
	}
	stored, err := h.queries.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if stored.Status != "dispatched" {
		t.Fatalf("Run status = %q, want newer dispatch preserved", stored.Status)
	}
	if h.recorder.topicCount(EventRunDispatched) != 1 ||
		h.recorder.topicCount(EventRunCancelled) != 0 {
		t.Fatalf("Run events = dispatched %d, cancelled %d; want 1/0",
			h.recorder.topicCount(EventRunDispatched),
			h.recorder.topicCount(EventRunCancelled))
	}
}

func TestClaimRunsForRuntimesHonorsBatchCap(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	// Three claimable runs across three agents (the batch loop claims at
	// most one run per distinct (runtime, agent) pair per call, the
	// triedAgents dedup), capped at 2.
	agents := []uuid.UUID{h.fixture.AgentA}
	for range 2 {
		agentID := newPGUUID(t)
		dbtest.SeedBoundAgent(t, ctx, h.pool, agentID, h.fixture.CompanyA, "Worker", "codex",
			&h.fixture.AgentA, h.fixture.UserA, h.fixture.RuntimeA)
		agents = append(agents, agentID)
	}
	for _, agentID := range agents {
		conversation := newPGUUID(t)
		dbtest.MustExec(t, ctx, h.pool, `
			INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title)
			VALUES ($1, $2, 'direct', $3, $4, 'Direct')`,
			conversation, h.fixture.CompanyA, agentID, h.fixture.UserA)
		// runs.id is a bigserial since the one-stream migration: let the
		// sequence assign it rather than handing the column a uuid.
		dbtest.MustExec(t, ctx, h.pool, `
			INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id, status, run_type)
			VALUES ($1, $2, $3, $4, $5, 'queued', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))`,
			h.fixture.CompanyA, agentID, conversation, h.fixture.RuntimeA, h.fixture.UserA)
	}

	claimed, err := h.svc.ClaimRunsForRuntimes(ctx, []uuid.UUID{h.fixture.RuntimeA}, 2)
	if err != nil {
		t.Fatalf("batch claim: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("batch claimed %d runs, want cap 2", len(claimed))
	}
	for _, run := range claimed {
		if run.Status != "dispatched" || run.RuntimeID != h.fixture.RuntimeA {
			t.Fatalf("batch run %+v, want dispatched on RuntimeA", run)
		}
	}

	// The third run stays claimable on the next poll.
	rest, err := h.svc.ClaimRunsForRuntimes(ctx, []uuid.UUID{h.fixture.RuntimeA}, 32)
	if err != nil {
		t.Fatalf("second batch claim: %v", err)
	}
	if len(rest) != 1 {
		t.Fatalf("second batch claimed %d runs, want 1", len(rest))
	}
}

func TestClaimRunsForRuntimesReclaimsStaleDispatch(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	conversation := h.seedDirectConversation(t)
	h.seedQueuedRun(t, conversation)
	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimed == nil {
		t.Fatalf("claim: run=%v err=%v", claimed, err)
	}

	// Age the dispatch past the recovery window with an expired lease: the
	// lost claim response scenario.
	dbtest.MustExec(t, ctx, h.pool, `
			UPDATE runs SET created_at = now() - interval '6 minutes',
			                updated_at = now() - interval '5 minutes',
			                prepare_lease_expires_at = now() - interval '1 minute'
			WHERE id = $1`, claimed.ID)

	reclaimed, err := h.svc.ClaimRunsForRuntimes(ctx, []uuid.UUID{h.fixture.RuntimeA}, 1)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != claimed.ID {
		t.Fatalf("reclaimed = %+v, want the stale dispatched run", reclaimed)
	}
	if reclaimed[0].Status != "dispatched" ||
		!reclaimed[0].UpdatedAt.Time.After(claimed.UpdatedAt.Time) {
		t.Fatalf("reclaimed run = %+v, want a refreshed dispatch fence", reclaimed)
	}
}

func TestEnqueueRunOnePendingPerTaskAgent(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	// Occupy the (task, agent) pending slot, then a fresh assignment run for
	// the same pair must coalesce instead of queueing twice.
	var pending []events.Event
	if _, err := EnqueueRunInTx(ctx, h.queries, &pending, RunTrigger{
		CompanyID:          h.fixture.CompanyA,
		ConversationID:     h.fixture.ConversationA,
		AgentID:            h.fixture.AgentA,
		TaskID:             uuid.NullUUID{UUID: h.fixture.TaskA, Valid: true},
		TriggerPrompt:      "first",
		Priority:           2,
		Source:             RunSourceAssign,
		AuthorizedByUserID: h.fixture.UserA,
	}); err != nil {
		t.Fatalf("occupy pending slot: %v", err)
	}
	if len(pending) != 3 || pending[1].Name != "activity.created" || pending[0].Name != EventRunQueued ||
		pending[2].Name != "canvas.changed" {
		t.Fatalf("task run.queued events = %d, want Run, Activity and overview creation", len(pending))
	}
	for _, group := range events.GroupByTopic(pending) {
		h.bus.Publish(ctx, group.Topic, group.Events...)
	}
	taskQueued, ok := h.recorder.lastEvent(EventRunQueued)
	// A task conversation is the company's, so no single creator narrows it.
	if !ok || taskQueued.Payload.ConversationKind != "task" ||
		taskQueued.Payload.CompanyID != h.fixture.CompanyA {
		t.Fatalf("task run.queued routing = %+v, want the company", taskQueued.Payload)
	}
	_, err := EnqueueRunInTx(ctx, h.queries, &pending, RunTrigger{
		CompanyID:          h.fixture.CompanyA,
		ConversationID:     h.fixture.ConversationA,
		AgentID:            h.fixture.AgentA,
		TaskID:             uuid.NullUUID{UUID: h.fixture.TaskA, Valid: true},
		TriggerPrompt:      "second",
		Priority:           2,
		Source:             RunSourceAssign,
		AuthorizedByUserID: h.fixture.UserA,
	})
	if !errors.Is(err, ErrDuplicatePendingRun) {
		t.Fatalf("pending-slot enqueue error = %v, want ErrDuplicatePendingRun", err)
	}
}

// The Run row is written before the user's words, and the words then carry
// its run_id. This is the order runs.id being a bigserial buys, and it is
// what makes GetRunTriggerMessageContent — keyed by run id — the way back to
// the prompt now that runs.trigger_message_id is gone.
func TestEnqueueRunPrecedesItsUserItemAndFreezesThePrompt(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	conversation := h.seedDirectConversation(t)
	const prompt = "please summarize the design doc"

	var pending []events.Event
	first, err := EnqueueRunInTx(ctx, h.queries, &pending, RunTrigger{
		CompanyID:          h.fixture.CompanyA,
		ConversationID:     conversation,
		AgentID:            h.fixture.AgentA,
		TriggerPrompt:      prompt,
		Priority:           2,
		Source:             RunSourceMessage,
		AuthorizedByUserID: h.fixture.UserA,
	})
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if first.ID == uuid.Nil || first.Status != "queued" || first.RuntimeID != h.fixture.RuntimeA {
		t.Fatalf("first run = %+v, want queued on RuntimeA (codex)", first)
	}
	// The prompt is frozen onto the Run at enqueue, so a later read
	// cannot change what the provider was asked.
	var frozen struct {
		Prompt string `json:"prompt"`
		Agent  *struct {
			Name string `json:"name"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(first.Context, &frozen); err != nil {
		t.Fatalf("decode frozen run context: %v", err)
	}
	if frozen.Prompt != prompt || frozen.Agent == nil || frozen.Agent.Name != "Agent A" {
		t.Fatalf("frozen run context = %s, want the prompt and the agent brief", first.Context)
	}
	if len(pending) != 2 || pending[1].Name != "activity.created" || pending[0].Name != EventRunQueued {
		t.Fatalf("event events = %d, want Activity and Run creation", len(pending))
	}
	for _, group := range events.GroupByTopic(pending) {
		h.bus.Publish(ctx, group.Topic, group.Events...)
	}
	queued, ok := h.recorder.lastEvent(EventRunQueued)
	if !ok {
		t.Fatal("no run.queued reached a subscriber")
	}
	queuedView := queued.Payload
	// A direct conversation's run is its creator's, and the payload says so.
	if queuedView.ConversationKind == "task" ||
		queuedView.ConversationCreatedBy == nil ||
		*queuedView.ConversationCreatedBy != h.fixture.UserA {
		t.Fatalf("run.queued routing = %+v, want the direct conversation's creator", queuedView)
	}
	if queuedView.ID != first.ID || queuedView.AgentID != h.fixture.AgentA ||
		queuedView.RuntimeID != h.fixture.RuntimeA || queuedView.Status != "queued" ||
		queuedView.UpdatedAt.IsZero() {
		t.Fatalf("run.queued projection = %+v", queuedView)
	}

	// Before the item exists the Run has no words to reach: the trigger
	// lookup is keyed by run id and finds nothing.
	triggerParams := generated.GetRunTriggerMessageContentParams{
		RunID: first.ID, CompanyID: h.fixture.CompanyA, ConversationID: conversation,
	}
	if _, err := h.queries.GetRunTriggerMessageContent(ctx, triggerParams); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("trigger content before the item = %v, want pgx.ErrNoRows", err)
	}

	// The caller writes the item second, carrying the run id it just got.
	itemID := h.seedUserItem(t, conversation, first.ID, prompt)
	content, err := h.queries.GetRunTriggerMessageContent(ctx, triggerParams)
	if err != nil {
		t.Fatalf("GetRunTriggerMessageContent() error = %v", err)
	}
	if content != prompt {
		t.Fatalf("trigger content = %q, want %q", content, prompt)
	}

	// And the projection reads the Run's input back off that same item.
	view, err := h.svc.Get(
		ctx,
		auth.UserPrincipal{UserID: h.fixture.UserA, CompanyID: h.fixture.CompanyA},
		first.ID,
	)
	if err != nil {
		t.Fatalf("Get(first) error = %v", err)
	}
	if view.TriggerMessageID == nil || *view.TriggerMessageID != itemID {
		t.Fatalf("projected trigger item = %v, want %s", view.TriggerMessageID, itemID)
	}
}

func TestEnqueueRunRefusesArchivedAgentAndStampsTheBinding(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	conversation := h.seedDirectConversation(t)

	// Every agent is bound to a runtime, so the enqueue stamps that binding
	// even while the runtime is offline (the daemon claims it on next
	// online); the only enqueue-time refusal left is an archived agent.
	claudeAgent := newPGUUID(t)
	dbtest.SeedBoundAgent(t, ctx, h.pool, claudeAgent, h.fixture.CompanyA, "Claude A", "codex",
		&h.fixture.AgentA, h.fixture.UserA, h.fixture.RuntimeA)
	var pending []events.Event
	if run, err := EnqueueRunInTx(ctx, h.queries, &pending, RunTrigger{
		CompanyID:          h.fixture.CompanyA,
		ConversationID:     conversation,
		AgentID:            claudeAgent,
		TriggerPrompt:      "hi",
		Source:             RunSourceMessage,
		AuthorizedByUserID: h.fixture.UserA,
	}); err != nil || run.RuntimeID != h.fixture.RuntimeA {
		t.Fatalf("bound enqueue = %+v, %v; want the agent's runtime stamped", run, err)
	}

	// An archived agent refuses outright.
	dbtest.MustExec(t, ctx, h.pool,
		`UPDATE agents SET archived_at = now() WHERE id = $1`, claudeAgent)
	if _, err := EnqueueRunInTx(ctx, h.queries, &pending, RunTrigger{
		CompanyID:          h.fixture.CompanyA,
		ConversationID:     conversation,
		AgentID:            claudeAgent,
		TriggerPrompt:      "hi",
		Source:             RunSourceMessage,
		AuthorizedByUserID: h.fixture.UserA,
	}); !errors.Is(err, ErrRunAgentArchived) {
		t.Fatalf("archived enqueue error = %v, want ErrRunAgentArchived", err)
	}
}

func TestFailRunAutoRetryChain(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	conversation := h.seedDirectConversation(t)
	h.seedQueuedRun(t, conversation)
	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimed == nil {
		t.Fatalf("claim: run=%v err=%v", claimed, err)
	}
	if _, err := h.svc.StartRun(ctx, claimed.ID, h.fixture.RuntimeA); err != nil {
		t.Fatalf("start: %v", err)
	}

	failed, err := h.svc.FailRun(
		ctx,
		claimed.ID,
		emptyTerminalProof(),
		"runtime went offline",
		"",
		string(runfailure.ReasonRuntimeOffline),
		false,
		"",
	)
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if failed.Status != "failed" {
		t.Fatalf("status = %q, want failed", failed.Status)
	}

	// The retry child was created in the same transaction: attempt 2, fresh
	// queued row, parent link.
	child, err := h.queries.GetLatestRunForConversation(ctx, conversation)
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	if child.ID == claimed.ID || child.Status != "queued" || child.Attempt != 2 ||
		child.ParentRunID.UUID != claimed.ID {
		t.Fatalf("retry child = %+v, want queued attempt-2 child of the failed run", child)
	}
	// And it writes no 'user_message' item of its own: the user said it once,
	// on the parent, and the child reaches those words through parent_run_id.
	if _, err := h.queries.GetRunTriggerMessageContent(
		ctx,
		generated.GetRunTriggerMessageContentParams{
			RunID: child.ID, CompanyID: h.fixture.CompanyA, ConversationID: conversation,
		},
	); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("retry child trigger content error = %v, want pgx.ErrNoRows", err)
	}
	event, ok := h.recorder.lastEvent(EventRunFailed)
	if !ok {
		t.Fatal("no run.failed event published")
	}
	payload := event.Payload
	if !payload.RetryQueued {
		t.Fatal("run.failed retryQueued = false while the child covers the failure")
	}
	if payload.ID != failed.ID || payload.AgentID != h.fixture.AgentA ||
		payload.Agent.ID != h.fixture.AgentA || payload.Agent.Name != "Agent A" ||
		payload.RuntimeID != h.fixture.RuntimeA ||
		payload.MaxAttempts != MaxAttemptsFor(failed.FailureReason.String, failed.Attempt) ||
		payload.UpdatedAt.IsZero() {
		t.Fatalf("run.failed projection = %+v", payload)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal run.failed payload: %v", err)
	}
	if string(encoded) == "" || jsonContainsKey(encoded, "error") {
		t.Fatalf("run.failed safe payload leaks an error field: %s", encoded)
	}

	// Exhaust the budget: the child fails the same way and no third attempt
	// appears (attempt 2 == max_attempts 2).
	if _, err := h.queries.StartRun(ctx, generated.StartRunParams{
		ID: child.ID, RuntimeID: h.fixture.RuntimeA,
	}); err == nil {
		t.Fatal("child started straight from queued, want claim first")
	}
	reclaimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || reclaimed == nil || reclaimed.ID != child.ID {
		t.Fatalf("claim child: run=%v err=%v", reclaimed, err)
	}
	if _, err := h.svc.StartRun(ctx, child.ID, h.fixture.RuntimeA); err != nil {
		t.Fatalf("start child: %v", err)
	}
	terminal, err := h.svc.FailRun(
		ctx,
		child.ID,
		emptyTerminalProof(),
		"runtime went offline",
		"",
		string(runfailure.ReasonRuntimeOffline),
		false,
		"",
	)
	if err != nil {
		t.Fatalf("fail child: %v", err)
	}
	if terminal.Status != "failed" {
		t.Fatalf("child status = %q, want failed", terminal.Status)
	}
	latest, err := h.queries.GetLatestRunForConversation(ctx, conversation)
	if err != nil {
		t.Fatalf("load latest: %v", err)
	}
	if latest.ID != child.ID {
		t.Fatalf("a third attempt %d appeared past the budget", latest.ID)
	}
	event, ok = h.recorder.lastEvent(EventRunFailed)
	if !ok {
		t.Fatal("no terminal run.failed event")
	}
	payload = event.Payload
	if payload.RetryQueued {
		t.Fatal("terminal run.failed retryQueued = true")
	}
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal terminal run.failed payload: %v", err)
	}
	if jsonContainsKey(encoded, "error") {
		t.Fatalf("terminal run.failed safe payload leaks an error field: %s", encoded)
	}
}

func TestFailRunPreservesNonemptyDaemonReason(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	conversation := h.seedDirectConversation(t)
	h.seedQueuedRun(t, conversation)
	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimed == nil {
		t.Fatalf("claim: run=%v err=%v", claimed, err)
	}
	if _, err := h.svc.StartRun(ctx, claimed.ID, h.fixture.RuntimeA); err != nil {
		t.Fatalf("start: %v", err)
	}

	failed, err := h.svc.FailRun(
		ctx,
		claimed.ID,
		emptyTerminalProof(),
		"resolve skill bundles: context deadline exceeded",
		"",
		string(runfailure.ReasonAgentUnknown),
		false,
		"",
	)
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if !failed.FailureReason.Valid || failed.FailureReason.String != string(runfailure.ReasonAgentUnknown) {
		t.Fatalf(
			"failure reason = %q, want daemon reason %q",
			failed.FailureReason.String,
			runfailure.ReasonAgentUnknown,
		)
	}
}

func TestFailRunRedactsProviderErrorAtPersistenceBoundary(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	conversation := h.seedDirectConversation(t)
	h.seedQueuedRun(t, conversation)
	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimed == nil {
		t.Fatalf("claim: run=%v err=%v", claimed, err)
	}
	if _, err := h.svc.StartRun(ctx, claimed.ID, h.fixture.RuntimeA); err != nil {
		t.Fatalf("start: %v", err)
	}

	failed, err := h.svc.FailRun(
		ctx,
		claimed.ID,
		emptyTerminalProof(),
		"provider failed Authorization: Bearer super-secret-token password=hunter2",
		"",
		string(runfailure.ReasonAgentUnknown),
		false,
		"",
	)
	if err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	if !failed.Error.Valid {
		t.Fatal("failed Run has no error")
	}
	for _, secret := range []string{"super-secret-token", "hunter2"} {
		if strings.Contains(failed.Error.String, secret) {
			t.Fatalf("runs.error leaked %q: %q", secret, failed.Error.String)
		}
	}
}

func TestHeartbeatTouchAndRuntimeGone(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	if _, err := h.queries.SetRuntimeOffline(ctx, h.fixture.RuntimeA); err != nil {
		t.Fatalf("set offline: %v", err)
	}
	touched, err := h.queries.TouchRuntimeHeartbeat(ctx, h.fixture.RuntimeA)
	if err != nil {
		t.Fatalf("touch heartbeat: %v", err)
	}
	if touched.Status != "online" || !touched.LastSeenAt.Valid {
		t.Fatalf("touched runtime = %+v, want online with fresh last_seen_at", touched)
	}
	if time.Since(touched.LastSeenAt.Time) > time.Minute {
		t.Fatalf("last_seen_at = %v, want fresh", touched.LastSeenAt.Time)
	}
	// The same statement reports the status the row held before it, which is
	// how the heartbeat knows it was this write that brought the machine back.
	if touched.PreviousStatus != "offline" {
		t.Fatalf("previous status = %q, want the offline the touch replaced", touched.PreviousStatus)
	}
	if again, err := h.queries.TouchRuntimeHeartbeat(ctx, h.fixture.RuntimeA); err != nil ||
		again.PreviousStatus != "online" {
		t.Fatalf("second touch = %+v, error = %v; want online reported as the previous status",
			again, err)
	}

	// A deleted runtime answers pgx.ErrNoRows — the daemon's runtime-gone
	// signal (HTTP 404) to prune and re-register.
	if _, err := h.queries.GetRuntime(ctx, newPGUUID(t)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing runtime lookup = %v, want ErrNoRows", err)
	}
}

func TestPrepareLeaseExtendAndExpirySweep(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	conversation := h.seedDirectConversation(t)
	h.seedQueuedRun(t, conversation)
	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimed == nil {
		t.Fatalf("claim: run=%v err=%v", claimed, err)
	}

	if err := h.svc.ExtendRunPrepareLease(ctx, claimed.ID, h.fixture.RuntimeA); err != nil {
		t.Fatalf("extend lease: %v", err)
	}

	// A live lease excludes the dispatched row from the timeout sweep even
	// when the claim is old.
	dbtest.MustExec(t, ctx, h.pool,
		`UPDATE runs
		 SET created_at = now() - interval '11 minutes',
		     updated_at = now() - interval '10 minutes'
		 WHERE id = $1`, claimed.ID)
	batch, err := h.svc.FailStaleRuns(ctx, StaleRunOptions{
		DispatchTimeout: 300 * time.Second,
		RunningTimeout:  9000 * time.Second,
		RuntimeStale:    150 * time.Second,
	})
	if err != nil {
		t.Fatalf("fail stale runs (live lease): %v", err)
	}
	if len(batch.Runs) != 0 {
		t.Fatalf("sweep failed %d runs under a live lease, want 0", len(batch.Runs))
	}

	// An expired lease lets the dispatch timeout fire.
	dbtest.MustExec(t, ctx, h.pool,
		`UPDATE runs SET prepare_lease_expires_at = now() - interval '1 minute' WHERE id = $1`, claimed.ID)
	batch, err = h.svc.FailStaleRuns(ctx, StaleRunOptions{
		DispatchTimeout: 300 * time.Second,
		RunningTimeout:  9000 * time.Second,
		RuntimeStale:    150 * time.Second,
	})
	if err != nil {
		t.Fatalf("fail stale runs (expired lease): %v", err)
	}
	if len(batch.Runs) != 1 || batch.Runs[0].ID != claimed.ID {
		t.Fatalf("sweep failed %v, want exactly the leased run", batch.Runs)
	}
	if batch.Runs[0].FailureReason.String != "timeout" {
		t.Fatalf("failure_reason = %q, want timeout", batch.Runs[0].FailureReason.String)
	}

	// After expiry the extend CAS refuses: the run is no longer dispatched.
	if err := h.svc.ExtendRunPrepareLease(ctx, claimed.ID, h.fixture.RuntimeA); err == nil {
		t.Fatal("extend after terminal state succeeded, want failure")
	}
}

func TestRequeueRunAfterClaimFailureCAS(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	conversation := h.seedDirectConversation(t)
	h.seedQueuedRun(t, conversation)
	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimed == nil {
		t.Fatalf("claim: run=%v err=%v", claimed, err)
	}

	requeued, err := h.svc.RequeueRunAfterClaimFailure(ctx, *claimed)
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if requeued.Status != "queued" || !requeued.UpdatedAt.Time.After(claimed.UpdatedAt.Time) {
		t.Fatalf("requeued run = status %q updated_at %v, want queued with a moved fence",
			requeued.Status, requeued.UpdatedAt.Time)
	}

	// The CAS refuses a replay carrying the stale fence: the row is already
	// queued (and a newer reclaim would own a fresh updated_at).
	if _, err := h.svc.RequeueRunAfterClaimFailure(ctx, *claimed); err == nil {
		t.Fatal("stale requeue succeeded, want CAS failure")
	}
}

func TestStaleRuntimeSweepPipeline(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := context.Background()

	// A dispatched run on a runtime that stopped heartbeating.
	conversation := h.seedDirectConversation(t)
	h.seedQueuedRun(t, conversation)
	claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
	if err != nil || claimed == nil {
		t.Fatalf("claim: run=%v err=%v", claimed, err)
	}
	dbtest.MustExec(t, ctx, h.pool,
		`UPDATE runtimes SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`, h.fixture.RuntimeA)

	candidates, err := h.queries.SelectStaleOnlineRuntimes(ctx, 150)
	if err != nil {
		t.Fatalf("select stale runtimes: %v", err)
	}
	var ids []uuid.UUID
	sawRuntimeA := false
	for _, c := range candidates {
		ids = append(ids, c.ID)
		if c.ID == h.fixture.RuntimeA {
			sawRuntimeA = true
		}
	}
	if !sawRuntimeA {
		t.Fatalf("stale candidates %v missed RuntimeA", candidates)
	}
	marked, err := h.queries.MarkRuntimesOfflineByIDs(ctx, generated.MarkRuntimesOfflineByIDsParams{
		Ids:       ids,
		StaleSecs: 150,
	})
	if err != nil || len(marked) == 0 {
		t.Fatalf("mark offline: rows=%v err=%v", marked, err)
	}

	failedBatch, err := h.svc.FailRunsForOfflineRuntimes(ctx)
	if err != nil {
		t.Fatalf("fail runs for offline runtimes: %v", err)
	}
	if len(failedBatch.Runs) != 1 || failedBatch.Runs[0].ID != claimed.ID {
		t.Fatalf("offline-failed runs = %v, want the dispatched run", failedBatch.Runs)
	}
	if failedBatch.Runs[0].FailureReason.String != "runtime_offline" {
		t.Fatalf("failure_reason = %q, want runtime_offline", failedBatch.Runs[0].FailureReason.String)
	}

	// The shared pipeline publishes run.failed and creates the auto-retry
	// (runtime_offline is retryable, attempt 1 < 2).
	if failedBatch.RetryCount != 1 {
		t.Fatalf("retried = %d, want 1", failedBatch.RetryCount)
	}
	if h.recorder.topicCount(EventRunFailed) != 1 {
		t.Fatalf("run.failed events = %d, want 1", h.recorder.topicCount(EventRunFailed))
	}
	// The sweeper's terminal row is a platform failure the metrics see once;
	// the retry child is not terminal and is not counted.
	wantObserved := []telemetry.RunTerminalObservation{
		{Provider: "codex", Outcome: "failed", FailureKind: "infrastructure"},
	}
	if got := h.observer.terminals(); !reflect.DeepEqual(got, wantObserved) {
		t.Fatalf("terminal observations = %+v, want %+v", got, wantObserved)
	}
	child, err := h.queries.GetLatestRunForConversation(ctx, conversation)
	if err != nil || child.Status != "queued" || child.ParentRunID.UUID != claimed.ID {
		t.Fatalf("sweeper retry child = %+v err=%v, want queued child of the failed run", child, err)
	}
}
