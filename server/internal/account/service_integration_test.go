//go:build integration

package account

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestDeleteCoordinatesAuthoredRetryAndRunQueueCleanup(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	var cancelledEvents []execution.RunSnapshot
	if err := bus.Subscribe(
		execution.Topic,
		func(_ context.Context, event events.Event) error {
			if event.Name != execution.EventRunCancelled {
				return nil
			}
			payload, err := events.PayloadOf[execution.RunSnapshot](event)
			if err != nil {
				return err
			}
			cancelledEvents = append(cancelledEvents, payload)
			return nil
		},
	); err != nil {
		t.Fatalf("subscribe run.cancelled: %v", err)
	}

	successorUserID := dbtest.NewUUID(t)
	sessionID := dbtest.NewUUID(t)
	triggerMessageID := dbtest.NewUUID(t)
	deletingRequestKey := dbtest.NewUUID(t)
	successorRequestKey := dbtest.NewUUID(t)
	attachmentID := dbtest.NewUUID(t)

	// Revoke the deleting user's membership and runtime ownership first. The
	// successor keeps a machine of their own rather than inheriting this one,
	// because a runtime is private to its registrant (ADR 0014). The account
	// transaction must still discover this Company from the authored queue
	// item and retry lineage left behind by the earlier revocation.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name)
		VALUES ($1, 'account-successor@example.com', 'Account Successor')`, successorUserID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, successorUserID)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members
		SET status = 'revoked', revoked_at = clock_timestamp(),
		    revoked_by_user_id = $2,
		    updated_at = clock_timestamp(), version = version + 1
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runtimes
		SET owner_user_id = NULL, updated_at = clock_timestamp()
		WHERE id = $1`, fixture.RuntimeA)
	// The Task goes to the colleague, who has the Agent set up on a machine
	// of their own: they are the assignee, so theirs is the queued prompt
	// promotion may materialize (ADR 0014).
	successorRuntimeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (
			id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at
		) VALUES ($1, $2, 'daemon-successor', 'Successor laptop', 'codex', 'online', $3, now())`,
		successorRuntimeID, fixture.CompanyA, successorUserID)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA,
		successorUserID, successorRuntimeID, "codex")
	// The new assignee works on the Task's Project: their queued prompt is
	// promoted only while they may run Tasks there (ADR 0017).
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, successorUserID)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks SET assignee_user_id = $2, updated_at = clock_timestamp(), version = version + 1
		WHERE id = $1`, fixture.TaskA, successorUserID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO auth_sessions (
			id, user_id, token_prefix, token_hash, expires_at, client_metadata
		)
		VALUES ($1, $2, 'acct', $3, clock_timestamp() + interval '1 hour', '{}'::jsonb)`,
		sessionID, fixture.UserA, bytes.Repeat([]byte{0x11}, 32))
	// The Run row goes in first and the user's words after it: runs.id is a
	// bigserial that needs nothing from the stream, and the item is what
	// carries run_id. That item is the whole of the authorship claim now — the
	// lineage query finds this root by an EXISTS over it.
	//
	// lane is spelled out because this Run carries a task_id: the column
	// defaults to 'direct', the lane the capacity gate exempts, so a task Run
	// seeded without it sits in the wrong lane. CreateRun stamps it from the
	// task_id it inserts, and CreateRetryRun copies it from the parent.
	var retryRootID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, attempt
		, run_type)
		VALUES ($1, $2, $3, $4, $5, 'failed', 1, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA,
		fixture.AgentA,
		fixture.TaskA,
		fixture.ConversationA,
		fixture.RuntimeA,
	).Scan(&retryRootID); err != nil {
		t.Fatalf("seed authored retry root Run: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items (
			id, company_id, conversation_id, run_id, seq, type,
			author_kind, author_id, content
		)
		VALUES ($1, $2, $3, $4, 2, 'user_message', 'user', $5, 'retry this')`,
		triggerMessageID, fixture.CompanyA, fixture.ConversationA, retryRootID, fixture.UserA)
	// The retry child writes no item of its own; it inherits the deleting
	// user's authorship through parent_run_id alone, and the parent's lane
	// with it.
	var retryChildID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, attempt, parent_run_id, fire_at
		, run_type)
		VALUES ($1, $2, $3, $4, $5, 'deferred', 2, $6,
		        clock_timestamp() + interval '1 hour', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA,
		fixture.AgentA,
		fixture.TaskA,
		fixture.ConversationA,
		fixture.RuntimeA,
		retryRootID,
	).Scan(&retryChildID); err != nil {
		t.Fatalf("seed retry child Run: %v", err)
	}
	if _, err := store.Queries().CreateRunToken(ctx, generated.CreateRunTokenParams{
		RunID:     retryChildID,
		TokenHash: bytes.Repeat([]byte{0x22}, 32),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("create retry Run token: %v", err)
	}

	deletingItem, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID:      fixture.CompanyA,
		ConversationID: fixture.ConversationA,
		ActorUserID:    fixture.UserA,
		RequestKey:     deletingRequestKey,
		Content:        "remove my queued prompt",
	})
	if err != nil {
		t.Fatalf("insert deleting user's queue item: %v", err)
	}
	deletingQueueID := deletingItem.ID
	successorItem, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID:      fixture.CompanyA,
		ConversationID: fixture.ConversationA,
		ActorUserID:    successorUserID,
		RequestKey:     successorRequestKey,
		Content:        "promote the eligible successor",
	})
	if err != nil {
		t.Fatalf("insert successor queue item: %v", err)
	}
	successorQueueID := successorItem.ID
	successorMessageID := successorItem.MessageID
	dbtest.MustExec(
		t, ctx, pool, `
		INSERT INTO attachments (
			id, company_id, uploaded_by_user_id, request_key, filename,
			content_type, size_bytes, status, version
		)
		VALUES ($1, $2, $3, $4, 'queued.txt', 'text/plain', 6, 'ready', 2)`,
		attachmentID,
		fixture.CompanyA,
		fixture.UserA,
		dbtest.NewUUID(t),
	)
	if _, err := store.Queries().ReserveRunQueueAttachment(
		ctx,
		generated.ReserveRunQueueAttachmentParams{
			AttachmentID: attachmentID,
			CompanyID:    fixture.CompanyA,
			ActorUserID:  fixture.UserA,
			QueueItemID:  deletingItem.ID,
			Position:     0,
		},
	); err != nil {
		t.Fatalf("reserve deleting user's queue Attachment: %v", err)
	}

	notifier := recordRuntimeWakeups(t, bus)
	service, err := NewAccountService(ServiceOptions{
		Store: store,
		Bus:   bus,
		SessionInvalidator: auth.SessionInvalidatorFunc(
			func(context.Context, []auth.SessionRevocation) {},
		),
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check:    task.CheckMessageRunInTx,
			Prepare:  task.PrepareMessageRunInTx,
			NextStep: task.RunNextStepInTx,
		},
		TaskTerminalEffects: execution.TaskTerminalEffectWriterFunc(task.ApplyRunTerminalInTx),
	})
	if err != nil {
		t.Fatalf("construct account service: %v", err)
	}
	if _, err := service.Delete(ctx, auth.UserPrincipal{
		UserID:    fixture.UserA,
		SessionID: sessionID,
		AuthKind:  auth.TokenKindSession,
	}, Confirmation); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	retryChild, err := store.Queries().GetRun(ctx, retryChildID)
	if err != nil {
		t.Fatalf("read retry child: %v", err)
	}
	if retryChild.Status != "cancelled" ||
		!retryChild.Error.Valid || retryChild.Error.String != "account_deleted" {
		t.Fatalf("retry child = %+v, want cancelled for account deletion", retryChild)
	}
	var tokenCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM run_tokens WHERE run_id = $1`, retryChildID).
		Scan(&tokenCount); err != nil {
		t.Fatalf("count retry Run tokens: %v", err)
	}
	if tokenCount != 0 {
		t.Fatalf("retry Run token count = %d, want 0", tokenCount)
	}

	var deletingState, deletingReason, successorState string
	if err := pool.QueryRow(ctx, `
		SELECT state, removal_reason
		FROM conversation_run_queue_items
		WHERE id = $1`, deletingQueueID).Scan(&deletingState, &deletingReason); err != nil {
		t.Fatalf("read deleting user's queue item: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT state FROM conversation_run_queue_items WHERE id = $1`, successorQueueID).
		Scan(&successorState); err != nil {
		t.Fatalf("read successor queue item: %v", err)
	}
	// The departing member's own queued prompt is removed with its reason,
	// and the new assignee's is promoted onto their own machine.
	if deletingState != "removed" || deletingReason != "authorization_revoked" ||
		successorState != "materialized" {
		t.Fatalf(
			"queue states = deleting(%q, %q), successor(%q); want removed, authorization_revoked, materialized",
			deletingState,
			deletingReason,
			successorState,
		)
	}
	var activeReservationCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM conversation_run_queue_attachments
		WHERE queue_item_id = $1 AND released_at IS NULL`, deletingQueueID).
		Scan(&activeReservationCount); err != nil {
		t.Fatalf("count deleting user's active queue reservations: %v", err)
	}
	if activeReservationCount != 0 {
		t.Fatalf("active deleting-user queue reservations = %d, want 0", activeReservationCount)
	}

	// The queue item no longer reserves a Run identity — only the item id — so
	// the promoted Run is reached through the item it wrote, which is the edge
	// that says whose words this Run is executing.
	var promotedRunID uuid.UUID
	var successorAuthorID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT run_id, author_id FROM conversation_items WHERE id = $1`, successorMessageID).
		Scan(&promotedRunID, &successorAuthorID); err != nil {
		t.Fatalf("read successor Message: %v", err)
	}
	if successorAuthorID != successorUserID {
		t.Fatalf("successor Message author = %s, want %s", successorAuthorID, successorUserID)
	}
	promoted, err := store.Queries().GetRun(ctx, promotedRunID)
	if err != nil || promoted.Status != "queued" || promoted.ConversationID != fixture.ConversationA ||
		promoted.RuntimeID != successorRuntimeID ||
		promoted.ExecutionAuthorizedByUserID.UUID != successorUserID {
		t.Fatalf("promoted successor Run = %+v, %v; want it queued on their own machine", promoted, err)
	}
	var taskStatus string
	var taskGeneration int64
	if err := pool.QueryRow(
		ctx, `
		SELECT status, execution_generation FROM tasks WHERE id = $1`, fixture.TaskA,
	).Scan(&taskStatus, &taskGeneration); err != nil {
		t.Fatalf("read Task after successor promotion: %v", err)
	}
	if taskStatus != "in_progress" || !promoted.TaskExecutionGeneration.Valid ||
		taskGeneration != promoted.TaskExecutionGeneration.Int64 {
		t.Fatalf(
			"Task after successor promotion = status %q generation %d, promoted Run = %+v",
			taskStatus,
			taskGeneration,
			promoted,
		)
	}
	// A promoted run and a cancelled one each say their runtime may have
	// work; both arrive as the same fact, which is the point.
	wantWakeups := []string{
		promotedRunID.String(),
		retryChildID.String(),
	}
	if len(notifier.runs) != 2 {
		t.Fatalf("runtime wakeups = %v, want %v", notifier.runs, wantWakeups)
	}
	for _, want := range wantWakeups {
		if !slices.Contains(notifier.runs, want) {
			t.Fatalf("runtime wakeups = %v, missing %s", notifier.runs, want)
		}
	}
	if len(cancelledEvents) != 1 {
		t.Fatalf("run.cancelled events = %d, want 1", len(cancelledEvents))
	}
	payload := cancelledEvents[0]
	// A task conversation's run is the company's, and the payload says so.
	if payload.ID != retryChildID || payload.Status != "cancelled" ||
		payload.CompanyID != fixture.CompanyA || payload.ConversationKind != "task" {
		t.Fatalf("run.cancelled event = %#v", cancelledEvents[0])
	}
}

func TestDeleteRevokesMembershipAndOwnsHumanInputContinuations(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	var closedInboxEvents []events.Event
	if err := bus.Subscribe(inbox.Topic, func(_ context.Context, event events.Event) error {
		if event.Name == inbox.EventItemUpdated {
			closedInboxEvents = append(closedInboxEvents, event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe Inbox events: %v", err)
	}
	var approvalEvents []events.Event
	if err := bus.Subscribe(approval.Topic, func(_ context.Context, event events.Event) error {
		if event.Name == approval.EventApprovalResolved {
			approvalEvents = append(approvalEvents, event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe Approval events: %v", err)
	}
	var questionEvents []events.Event
	if err := bus.Subscribe(approval.TopicQuestion, func(_ context.Context, event events.Event) error {
		if event.Name == approval.EventQuestionResolved {
			questionEvents = append(questionEvents, event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe Question events: %v", err)
	}
	var memberRemovedEvents []events.Event
	if err := bus.Subscribe(company.TopicMember, func(_ context.Context, event events.Event) error {
		if event.Name == company.EventMemberRemoved {
			memberRemovedEvents = append(memberRemovedEvents, event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe member.removed events: %v", err)
	}
	var accessRevokedEvents []events.Event
	if err := bus.Subscribe(company.TopicAuth, func(_ context.Context, event events.Event) error {
		if event.Name == company.EventAuthCompanyAccessRevoked {
			accessRevokedEvents = append(accessRevokedEvents, event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe auth.company_access_revoked events: %v", err)
	}
	var bindingEvents []events.Event
	if err := bus.Subscribe(agent.Topic, func(_ context.Context, event events.Event) error {
		if event.Name == agent.EventAgentBindingChanged {
			bindingEvents = append(bindingEvents, event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe agent.binding_changed events: %v", err)
	}
	sessionID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO auth_sessions (
			id, user_id, token_prefix, token_hash, expires_at, client_metadata
		)
		VALUES ($1, $2, 'acct', $3, clock_timestamp() + interval '1 hour', '{}'::jsonb)`,
		sessionID, fixture.UserA, bytes.Repeat([]byte{0x44}, 32))

	// A delivered human-input continuation is a fresh Run root and owns no
	// user_message. This one belongs to a shared Task Conversation, so its
	// durable Approval receipt is the only authorship edge account deletion can
	// follow.
	var continuationRunID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, task_execution_generation
		, run_type)
		VALUES ($1, $2, $3, $4, $5, 'queued', 0, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`, fixture.CompanyA, fixture.AgentA, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA).Scan(&continuationRunID); err != nil {
		t.Fatalf("seed delivered continuation Run: %v", err)
	}
	approvalID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO approvals (
			id, company_id, type, status, rule_key, title, preview_markdown,
			requested_by_agent_id, origin_run_id, request_key,
			task_id, conversation_id, origin_user_id, canonical_action,
			request_sha256, action_sha256, expires_at, decided_by_user_id,
			continuation_run_id, continuation_prompt, continuation_status,
			continuation_next_attempt_at, continuation_attempts
		)
		VALUES (
			$1, $2, 'company_plan_apply', 'rejected', 'company_plan_apply',
			'Apply Company Plan', 'preview', $3, $4, $5, $6, $7, NULL,
			'{}'::jsonb, decode(repeat('00', 32), 'hex'),
			decode(repeat('11', 32), 'hex'), clock_timestamp() + interval '1 hour', $8,
			$9, 'The request was rejected.', 'delivered',
			clock_timestamp(), 1
		)`, approvalID, fixture.CompanyA, fixture.AgentA, fixture.RunA,
		dbtest.NewUUID(t), fixture.TaskA, fixture.ConversationA, fixture.UserA, continuationRunID)

	// A terminal Task request attributes its still-pending continuation to the
	// member who supplied the human input, even though the shared Conversation
	// itself does not belong to that member.
	questionID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO questions (
			id, company_id, status, title, asked_by_agent_id, origin_run_id,
			request_key, task_id, conversation_id, origin_user_id, prompts,
			request_sha256, expires_at, answers, resolved_by_user_id,
			continuation_prompt, continuation_status,
			continuation_next_attempt_at
		)
		VALUES (
			$1, $2, 'answered', 'Choose a target', $3, $4, $5, $6, $7,
			NULL, '[]'::jsonb, decode(repeat('22', 32), 'hex'),
			clock_timestamp() + interval '1 hour', '[]'::jsonb, $8,
			'Use the reviewed answer.', 'pending', clock_timestamp()
		)`, questionID, fixture.CompanyA, fixture.AgentA, fixture.RunA,
		dbtest.NewUUID(t), fixture.TaskA, fixture.ConversationA, fixture.UserA)

	// An unresolved direct request has no continuation receipt yet. This is the
	// deleting user's only live footprint in Company B: if Company discovery
	// misses it, ExpireDue can open a receipt later and the lifecycle worker will
	// materialize private work for a deleted user.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (
			company_id, user_id, role, status, revoked_at, revoked_by_user_id
		)
		VALUES ($1, $2, 'member', 'revoked', clock_timestamp(), $3)`,
		fixture.CompanyB, fixture.UserA, fixture.UserB)
	directConversationID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (
			id, company_id, kind, agent_id, created_by_user_id, title
		)
		VALUES ($1, $2, 'direct', $3, $4, 'Historical direct request')`,
		directConversationID, fixture.CompanyB, fixture.AgentB, fixture.UserA)
	var directOriginRunID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, status
		, run_type)
		VALUES ($1, $2, $3, $4, 'completed', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`, fixture.CompanyB, fixture.AgentB,
		directConversationID, fixture.RuntimeB).Scan(&directOriginRunID); err != nil {
		t.Fatalf("seed direct Approval origin Run: %v", err)
	}
	pendingApprovalID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO approvals (
			id, company_id, type, rule_key, title, preview_markdown,
			requested_by_agent_id, origin_run_id, request_key,
			conversation_id, origin_user_id, canonical_action,
			request_sha256, action_sha256, expires_at
		)
		VALUES (
			$1, $2, 'company_plan_apply', 'company_plan_apply',
			'Apply Company Plan', 'preview', $3, $4, $5, $6, $7,
			'{}'::jsonb, decode(repeat('33', 32), 'hex'),
			decode(repeat('44', 32), 'hex'), clock_timestamp() + interval '1 hour'
			)`, pendingApprovalID, fixture.CompanyB, fixture.AgentB, directOriginRunID,
		dbtest.NewUUID(t), directConversationID, fixture.UserA)
	pendingApprovalInboxID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO inbox_items (
			id, company_id, recipient_user_id, conversation_id, kind,
			entity_type, entity_id, title, body, dedupe_key
		)
		VALUES (
			$1, $2, $3, $4, 'approval_request', 'approval', $5,
			'Approval required', 'Review the request.', $6
		)`, pendingApprovalInboxID, fixture.CompanyB, fixture.UserA,
		directConversationID, pendingApprovalID.String(), "approval:"+pendingApprovalID.String())

	service := newAccountIntegrationService(t, store, bus)
	if _, err := service.Delete(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, SessionID: sessionID, AuthKind: auth.TokenKindSession,
	}, Confirmation); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	var runStatus, runError string
	if err := pool.QueryRow(ctx, `SELECT status, error FROM runs WHERE id = $1`, continuationRunID).
		Scan(&runStatus, &runError); err != nil {
		t.Fatalf("read delivered continuation Run: %v", err)
	}
	if runStatus != "cancelled" || runError != "account_deleted" {
		t.Fatalf("delivered continuation Run = %q/%q, want cancelled/account_deleted", runStatus, runError)
	}
	var questionStatus, abandonReason string
	var questionRunID pgtype.Int8
	if err := pool.QueryRow(ctx, `
		SELECT continuation_status, continuation_abandon_reason, continuation_run_id
		FROM questions WHERE id = $1`, questionID).
		Scan(&questionStatus, &abandonReason, &questionRunID); err != nil {
		t.Fatalf("read pending continuation receipt: %v", err)
	}
	if questionStatus != "abandoned" || abandonReason != "account_deleted" || questionRunID.Valid {
		t.Fatalf(
			"pending continuation = %q/%q/%v, want abandoned/account_deleted/NULL",
			questionStatus,
			abandonReason,
			questionRunID,
		)
	}
	var approvalStatus, approvalContinuationStatus, approvalAbandonReason string
	if err := pool.QueryRow(ctx, `
		SELECT status, continuation_status, continuation_abandon_reason
		FROM approvals WHERE id = $1`, pendingApprovalID).Scan(
		&approvalStatus,
		&approvalContinuationStatus,
		&approvalAbandonReason,
	); err != nil {
		t.Fatalf("read unresolved direct Approval: %v", err)
	}
	if approvalStatus != "cancelled" || approvalContinuationStatus != "abandoned" ||
		approvalAbandonReason != "account_deleted" {
		t.Fatalf(
			"unresolved direct Approval = %q/%q/%q, want cancelled/abandoned/account_deleted",
			approvalStatus,
			approvalContinuationStatus,
			approvalAbandonReason,
		)
	}
	var inboxReadAt, inboxArchivedAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `
		SELECT read_at, archived_at FROM inbox_items WHERE id = $1`, pendingApprovalInboxID).
		Scan(&inboxReadAt, &inboxArchivedAt); err != nil {
		t.Fatalf("read direct Approval Inbox item: %v", err)
	}
	if !inboxReadAt.Valid || !inboxArchivedAt.Valid {
		t.Fatalf(
			"direct Approval Inbox item = read %v archived %v, want both closed",
			inboxReadAt,
			inboxArchivedAt,
		)
	}
	if len(closedInboxEvents) != 1 || closedInboxEvents[0].CompanyID != fixture.CompanyB {
		t.Fatalf("closed Inbox events = %+v, want one Company B update", closedInboxEvents)
	}
	if len(approvalEvents) != 1 {
		t.Fatalf("approval.resolved events = %+v, want one", approvalEvents)
	}
	cancelledApproval, err := events.PayloadOf[approval.Approval](approvalEvents[0])
	if err != nil {
		t.Fatalf("decode cancelled Approval event: %v", err)
	}
	if approvalEvents[0].Actor != events.SystemActor() || cancelledApproval.ID != pendingApprovalID ||
		cancelledApproval.CompanyID != fixture.CompanyB ||
		cancelledApproval.Status != approval.ApprovalStatusCancelled ||
		cancelledApproval.TaskID != nil || cancelledApproval.Revision != 2 ||
		approvalEvents[0].RunID != directOriginRunID ||
		approvalEvents[0].ConversationID != directConversationID {
		t.Fatalf("cancelled Approval event = %+v / %#v", approvalEvents[0], cancelledApproval)
	}
	if len(questionEvents) != 1 {
		t.Fatalf("question.resolved events = %+v, want one", questionEvents)
	}
	abandonedQuestion, err := events.PayloadOf[approval.Question](questionEvents[0])
	if err != nil {
		t.Fatalf("decode abandoned Question event: %v", err)
	}
	if questionEvents[0].Actor != events.SystemActor() || abandonedQuestion.ID != questionID ||
		abandonedQuestion.CompanyID != fixture.CompanyA ||
		abandonedQuestion.Status != approval.QuestionStatusAnswered ||
		abandonedQuestion.TaskID == nil || *abandonedQuestion.TaskID != fixture.TaskA ||
		abandonedQuestion.ContinuationRunID != nil || abandonedQuestion.Revision != 2 ||
		questionEvents[0].RunID != fixture.RunA ||
		questionEvents[0].TaskID != fixture.TaskA ||
		questionEvents[0].ConversationID != fixture.ConversationA {
		t.Fatalf("abandoned Question event = %+v / %#v", questionEvents[0], abandonedQuestion)
	}
	var membershipStatus string
	var revokedBy uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		SELECT status, revoked_by_user_id
		FROM company_members
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA).
		Scan(&membershipStatus, &revokedBy); err != nil {
		t.Fatalf("read deleted account membership: %v", err)
	}
	if membershipStatus != "revoked" || !revokedBy.Valid || revokedBy.UUID != fixture.UserA {
		t.Fatalf("deleted account membership = %q/%v, want revoked by self", membershipStatus, revokedBy)
	}
	if len(memberRemovedEvents) != 1 {
		t.Fatalf("member.removed events = %+v, want one", memberRemovedEvents)
	}
	removedMembership, err := events.PayloadOf[company.MemberRemovalReceipt](memberRemovedEvents[0])
	if err != nil {
		t.Fatalf("decode member.removed event: %v", err)
	}
	if memberRemovedEvents[0].Actor != events.SystemActor() ||
		removedMembership.CompanyID != fixture.CompanyA ||
		removedMembership.UserID != fixture.UserA || removedMembership.Revision != 2 {
		t.Fatalf("member.removed event = %+v / %#v", memberRemovedEvents[0], removedMembership)
	}
	// The deleted account had AgentA set up on its own machine: the
	// revocation drops that binding in the same commit and says so once, for
	// Company A (ADR 0014). Nobody else's setup is touched, because a
	// binding only ever names a machine its own member registered. Company B
	// is in the deletion set only through the pending Approval; the account
	// has no binding there, so no frame for B.
	if len(bindingEvents) != 1 {
		t.Fatalf("agent.binding_changed events = %+v, want one for Company A", bindingEvents)
	}
	bindingChange, err := events.PayloadOf[agent.BindingChange](bindingEvents[0])
	if err != nil {
		t.Fatalf("decode agent.binding_changed event: %v", err)
	}
	if bindingEvents[0].CompanyID != fixture.CompanyA ||
		bindingChange.CompanyID != fixture.CompanyA || bindingChange.UserID != fixture.UserA ||
		bindingChange.AgentID != fixture.AgentA || bindingChange.Binding != nil {
		t.Fatalf(
			"agent.binding_changed event = %+v / %#v, want AgentA unbound for the deleted account",
			bindingEvents[0],
			bindingChange,
		)
	}
	if len(accessRevokedEvents) != 1 {
		t.Fatalf("auth.company_access_revoked events = %+v, want one", accessRevokedEvents)
	}
	revokedAccess, err := events.PayloadOf[company.AccessRevokedReceipt](accessRevokedEvents[0])
	if err != nil {
		t.Fatalf("decode auth.company_access_revoked event: %v", err)
	}
	if accessRevokedEvents[0].Actor != events.SystemActor() ||
		revokedAccess.CompanyID != fixture.CompanyA || revokedAccess.UserID != fixture.UserA ||
		revokedAccess.Reason != company.AccessReasonMembershipRemoved {
		t.Fatalf(
			"auth.company_access_revoked event = %+v / %#v",
			accessRevokedEvents[0],
			revokedAccess,
		)
	}
}

func TestDeleteFencesSendThatResolvedMembershipBeforeDeletion(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	sessionID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO auth_sessions (
			id, user_id, token_prefix, token_hash, expires_at, client_metadata
		)
		VALUES ($1, $2, 'acct', $3, clock_timestamp() + interval '1 hour', '{}'::jsonb)`,
		sessionID, fixture.UserA, bytes.Repeat([]byte{0x55}, 32))

	companyHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Company holder: %v", err)
	}
	defer func() { _ = companyHolder.Rollback(context.Background()) }()
	if _, err := companyHolder.Exec(
		ctx,
		`SELECT id FROM companies WHERE id = $1 FOR UPDATE`,
		fixture.CompanyA,
	); err != nil {
		t.Fatalf("hold Company lock: %v", err)
	}

	accountService := newAccountIntegrationService(t, store, bus)
	deleteResult := make(chan error, 1)
	go func() {
		_, deleteErr := accountService.Delete(ctx, auth.UserPrincipal{
			UserID: fixture.UserA, SessionID: sessionID, AuthKind: auth.TokenKindSession,
		}, Confirmation)
		deleteResult <- deleteErr
	}()
	waitForBlockedAccountQuery(t, ctx, pool, "LockAccountCompaniesForDeletion")

	conversationService, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: store,
		Bus:   bus,
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check:    task.CheckMessageRunInTx,
			Prepare:  task.PrepareMessageRunInTx,
			NextStep: task.RunNextStepInTx,
		},
	})
	if err != nil {
		t.Fatalf("construct Conversation service: %v", err)
	}
	requestKey := dbtest.NewUUID(t)
	sendResult := make(chan error, 1)
	go func() {
		_, sendErr := conversationService.Send(
			ctx,
			auth.UserPrincipal{
				UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
			},
			conversation.MessageTarget{
				Kind: conversation.TargetConversation, ID: fixture.DirectConversationA,
			},
			conversation.SendInput{RequestKey: requestKey, Content: "Do not enqueue after deletion."},
		)
		sendResult <- sendErr
	}()
	waitForBlockedAccountQuery(t, ctx, pool, "LockCompanyForMembershipMutation")

	if err := companyHolder.Rollback(ctx); err != nil {
		t.Fatalf("release Company lock: %v", err)
	}
	select {
	case err := <-deleteResult:
		if err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Delete() did not resume after Company lock release")
	}
	select {
	case err := <-sendResult:
		if !errors.Is(err, conversation.ErrNotFound) {
			t.Fatalf("Send() error = %v, want Conversation not found after deletion", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Send() did not resume after account deletion")
	}

	var messageCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM conversation_items WHERE request_key = $1`, requestKey).
		Scan(&messageCount); err != nil {
		t.Fatalf("count post-deletion Messages: %v", err)
	}
	if messageCount != 0 {
		t.Fatalf("post-deletion Message count = %d, want 0", messageCount)
	}
}

func TestDeleteUserLockAllowsCompanyFirstInboxCreationToFinish(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	sessionID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO auth_sessions (
			id, user_id, token_prefix, token_hash, expires_at, client_metadata
		)
		VALUES ($1, $2, 'acct', $3, clock_timestamp() + interval '1 hour', '{}'::jsonb)`,
		sessionID, fixture.UserA, bytes.Repeat([]byte{0x66}, 32))

	var originRunID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, status
		, run_type)
		VALUES ($1, $2, $3, $4, 'running', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`, fixture.CompanyA, fixture.AgentA,
		fixture.DirectConversationA, fixture.RuntimeA).Scan(&originRunID); err != nil {
		t.Fatalf("seed direct origin Run: %v", err)
	}

	producerTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Approval producer transaction: %v", err)
	}
	defer func() { _ = producerTx.Rollback(context.Background()) }()
	if _, err := producerTx.Exec(ctx, `SET LOCAL statement_timeout = '3s'`); err != nil {
		t.Fatalf("bound Approval producer lock wait: %v", err)
	}
	producer := generated.New(producerTx)
	if _, err := producer.LockCompanyForMembershipMutation(ctx, fixture.CompanyA); err != nil {
		t.Fatalf("lock Approval Company: %v", err)
	}
	if _, err := producer.GetCompanyMemberForUpdate(
		ctx,
		generated.GetCompanyMemberForUpdateParams{
			CompanyID: fixture.CompanyA,
			UserID:    fixture.UserA,
		},
	); err != nil {
		t.Fatalf("lock Approval actor membership: %v", err)
	}
	if _, err := producer.LockHumanInputOriginConversation(
		ctx,
		generated.LockHumanInputOriginConversationParams{
			ConversationID:   fixture.DirectConversationA,
			CompanyID:        fixture.CompanyA,
			ConversationKind: "direct",
			AgentID:          fixture.AgentA,
			OriginUserID:     uuid.NullUUID{UUID: fixture.UserA, Valid: true},
		},
	); err != nil {
		t.Fatalf("lock Approval origin Conversation: %v", err)
	}
	if _, err := producer.LockLiveHumanInputOriginRun(
		ctx,
		generated.LockLiveHumanInputOriginRunParams{
			RunID:          originRunID,
			CompanyID:      fixture.CompanyA,
			AgentID:        fixture.AgentA,
			ConversationID: fixture.DirectConversationA,
		},
	); err != nil {
		t.Fatalf("lock Approval origin Run: %v", err)
	}
	approvalRow, err := producer.CreateApproval(ctx, generated.CreateApprovalParams{
		CompanyID:          fixture.CompanyA,
		Type:               string(approval.ApprovalTypeCompanyPlanApply),
		RuleKey:            string(approval.ApprovalTypeCompanyPlanApply),
		Title:              "Apply Company Plan",
		PreviewMarkdown:    "Review this plan.",
		RequestedByAgentID: fixture.AgentA,
		OriginRunID:        originRunID,
		RequestKey:         dbtest.NewUUID(t),
		ConversationID:     fixture.DirectConversationA,
		OriginUserID:       uuid.NullUUID{UUID: fixture.UserA, Valid: true},
		CanonicalAction:    []byte(`{}`),
		RequestSha256:      bytes.Repeat([]byte{0x77}, 32),
		ActionSha256:       bytes.Repeat([]byte{0x88}, 32),
		ExpiresAt: pgtype.Timestamptz{
			Time: time.Now().UTC().Add(time.Hour), Valid: true,
		},
	})
	if err != nil {
		t.Fatalf("create pending Approval: %v", err)
	}

	service := newAccountIntegrationService(t, store, bus)
	deleteResult := make(chan error, 1)
	go func() {
		_, deleteErr := service.Delete(ctx, auth.UserPrincipal{
			UserID: fixture.UserA, SessionID: sessionID, AuthKind: auth.TokenKindSession,
		}, Confirmation)
		deleteResult <- deleteErr
	}()
	waitForBlockedAccountQuery(t, ctx, pool, "LockAccountCompaniesForDeletion")

	var pending []events.Event
	created, err := inbox.CreateInTx(ctx, producer, &pending, inbox.CreateInput{
		CompanyID:       fixture.CompanyA,
		RecipientUserID: fixture.UserA,
		ConversationID:  &fixture.DirectConversationA,
		Type:            inbox.TypeApprovalRequest,
		Entity: inbox.Entity{
			Type: protocol.EntityApproval,
			ID:   approvalRow.ID.String(),
		},
		Title:     "Approval required",
		Body:      "Review the proposed Company Plan.",
		DedupeKey: "approval:" + approvalRow.ID.String() + ":requested",
		Actor:     inbox.Actor{Kind: "agent", ID: &fixture.AgentA},
	})
	if err != nil {
		t.Fatalf("create Approval Inbox item while deletion waits on Company: %v", err)
	}
	if !created.Created {
		t.Fatal("Approval Inbox item was not created")
	}
	if err := producerTx.Commit(ctx); err != nil {
		t.Fatalf("commit Approval producer: %v", err)
	}
	select {
	case err := <-deleteResult:
		if err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Delete() did not resume after Approval producer commit")
	}
}

func TestDeleteAndCompanyCreateFenceTheFounderAccount(t *testing.T) {
	t.Run("deletion wins", func(t *testing.T) {
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
		bus, err := events.NewBus(events.BusOptions{})
		if err != nil {
			t.Fatalf("construct event bus: %v", err)
		}
		sessionID := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO auth_sessions (
				id, user_id, token_prefix, token_hash, expires_at, client_metadata
			)
			VALUES ($1, $2, 'acct', $3, clock_timestamp() + interval '1 hour', '{}'::jsonb)`,
			sessionID, fixture.UserA, bytes.Repeat([]byte{0x91}, 32))

		companyHolder, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin Company holder: %v", err)
		}
		defer func() { _ = companyHolder.Rollback(context.Background()) }()
		if _, err := companyHolder.Exec(
			ctx,
			`SELECT id FROM companies WHERE id = $1 FOR UPDATE`,
			fixture.CompanyA,
		); err != nil {
			t.Fatalf("hold existing Company lock: %v", err)
		}

		accountService := newAccountIntegrationService(t, store, bus)
		deleteResult := make(chan error, 1)
		go func() {
			_, deleteErr := accountService.Delete(ctx, auth.UserPrincipal{
				UserID: fixture.UserA, SessionID: sessionID, AuthKind: auth.TokenKindSession,
			}, Confirmation)
			deleteResult <- deleteErr
		}()
		waitForBlockedAccountQuery(t, ctx, pool, "LockAccountCompaniesForDeletion")

		companyService, err := company.NewCompanyService(company.ServiceOptions{Store: store, Bus: bus})
		if err != nil {
			t.Fatalf("construct Company service: %v", err)
		}
		input := company.CreateCompanyInput{
			Name:            "Deletion Loses No Company",
			Slug:            "deletion-wins-company-create",
			TaskPrefix:      "DWC",
			BusinessContext: "Account deletion must win this race.",
		}
		createResult := make(chan error, 1)
		go func() {
			_, createErr := companyService.Create(
				ctx,
				auth.UserPrincipal{UserID: fixture.UserA},
				input,
			)
			createResult <- createErr
		}()
		waitForBlockedAccountQuery(t, ctx, pool, "LockEnabledUserForMembershipMutation")

		if err := companyHolder.Rollback(ctx); err != nil {
			t.Fatalf("release existing Company lock: %v", err)
		}
		select {
		case err := <-deleteResult:
			if err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Delete() did not resume after Company lock release")
		}
		select {
		case err := <-createResult:
			if !errors.Is(err, company.ErrUnauthenticated) {
				t.Fatalf("Create() error = %v, want ErrUnauthenticated", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Create() did not recheck the founder after deletion")
		}
		assertNoCompanyCreateFootprint(t, ctx, pool, fixture.UserA, input.Slug)
	})

	t.Run("creation wins", func(t *testing.T) {
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
		bus, err := events.NewBus(events.BusOptions{})
		if err != nil {
			t.Fatalf("construct event bus: %v", err)
		}
		sessionID := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO auth_sessions (
				id, user_id, token_prefix, token_hash, expires_at, client_metadata
			)
			VALUES ($1, $2, 'acct', $3, clock_timestamp() + interval '1 hour', '{}'::jsonb)`,
			sessionID, fixture.UserA, bytes.Repeat([]byte{0x92}, 32))

		input := company.CreateCompanyInput{
			Name:            "Creation Wins Company",
			Slug:            "creation-wins-account-delete",
			TaskPrefix:      "CWA",
			BusinessContext: "The deletion must discover this committed Company.",
		}
		slugHolder, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin Company slug holder: %v", err)
		}
		defer func() { _ = slugHolder.Rollback(context.Background()) }()
		if _, err := slugHolder.Exec(ctx, `
			INSERT INTO companies (name, slug, task_prefix, business_context)
			VALUES ('Slug holder', $1, 'HLD', 'Hold the unique index')`, input.Slug); err != nil {
			t.Fatalf("hold Company slug: %v", err)
		}

		companyService, err := company.NewCompanyService(company.ServiceOptions{Store: store, Bus: bus})
		if err != nil {
			t.Fatalf("construct Company service: %v", err)
		}
		type createOutcome struct {
			value company.Response
			err   error
		}
		createResult := make(chan createOutcome, 1)
		go func() {
			value, createErr := companyService.Create(
				ctx,
				auth.UserPrincipal{UserID: fixture.UserA},
				input,
			)
			createResult <- createOutcome{value: value, err: createErr}
		}()
		waitForBlockedAccountQuery(t, ctx, pool, "CreateCompany")

		accountService := newAccountIntegrationService(t, store, bus)
		deleteResult := make(chan error, 1)
		go func() {
			_, deleteErr := accountService.Delete(ctx, auth.UserPrincipal{
				UserID: fixture.UserA, SessionID: sessionID, AuthKind: auth.TokenKindSession,
			}, Confirmation)
			deleteResult <- deleteErr
		}()
		waitForBlockedAccountQuery(t, ctx, pool, "LockAccountDeletionAuthority")

		if err := slugHolder.Rollback(ctx); err != nil {
			t.Fatalf("release Company slug: %v", err)
		}
		var created company.Response
		select {
		case outcome := <-createResult:
			if outcome.err != nil {
				t.Fatalf("Create() error = %v", outcome.err)
			}
			created = outcome.value
		case <-time.After(10 * time.Second):
			t.Fatal("Create() did not resume after Company slug release")
		}
		select {
		case err := <-deleteResult:
			if err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Delete() did not resume after Company creation")
		}

		var companyID uuid.UUID
		var membershipStatus string
		if err := pool.QueryRow(ctx, `
			SELECT companies.id, company_members.status
			FROM companies
			JOIN company_members ON company_members.company_id = companies.id
			WHERE companies.slug = $1
			  AND company_members.user_id = $2`, input.Slug, fixture.UserA).
			Scan(&companyID, &membershipStatus); err != nil {
			t.Fatalf("read created Company after account deletion: %v", err)
		}
		if companyID != created.ID || membershipStatus != "revoked" {
			t.Fatalf(
				"created Company membership = %s/%q, want %s/revoked",
				companyID,
				membershipStatus,
				created.ID,
			)
		}
	})
}

func assertNoCompanyCreateFootprint(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	userID uuid.UUID,
	slug string,
) {
	t.Helper()
	var companyCount, activeMembershipCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM companies WHERE slug = $1`, slug).Scan(&companyCount); err != nil {
		t.Fatalf("count post-deletion Companies: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM company_members
		JOIN companies ON companies.id = company_members.company_id
		WHERE companies.slug = $1
		  AND company_members.user_id = $2
		  AND company_members.status = 'active'`, slug, userID).Scan(&activeMembershipCount); err != nil {
		t.Fatalf("count post-deletion active founder memberships: %v", err)
	}
	if companyCount != 0 || activeMembershipCount != 0 {
		t.Fatalf(
			"post-deletion Company footprint = companies %d active memberships %d, want 0/0",
			companyCount,
			activeMembershipCount,
		)
	}
}

func TestDeleteCancelsCurrentTaskWhenNoSuccessorPromotes(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	sessionID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO auth_sessions (
			id, user_id, token_prefix, token_hash, expires_at, client_metadata
		)
		VALUES ($1, $2, 'acct', $3, clock_timestamp() + interval '1 hour', '{}'::jsonb)`,
		sessionID, fixture.UserA, bytes.Repeat([]byte{0x33}, 32))
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET status = 'in_progress', execution_generation = 7
		WHERE id = $1`, fixture.TaskA)
	var runID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, task_execution_generation
		, run_type)
		VALUES ($1, $2, $3, $4, $5, 'running', 7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`, fixture.CompanyA, fixture.AgentA, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA).Scan(&runID); err != nil {
		t.Fatalf("seed current Task Run: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items (
			id, company_id, conversation_id, run_id, seq, type,
			author_kind, author_id, content
		)
		VALUES ($1, $2, $3, $4, 2, 'user_message', 'user', $5, 'cancel with account')`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.ConversationA, runID, fixture.UserA)

	var taskUpdated []events.Event
	if err := bus.Subscribe(task.Topic, func(_ context.Context, event events.Event) error {
		if event.Name == task.EventUpdated {
			taskUpdated = append(taskUpdated, event)
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe Task events: %v", err)
	}
	service, err := NewAccountService(ServiceOptions{
		Store: store,
		Bus:   bus,
		SessionInvalidator: auth.SessionInvalidatorFunc(
			func(context.Context, []auth.SessionRevocation) {},
		),
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check:    task.CheckMessageRunInTx,
			Prepare:  task.PrepareMessageRunInTx,
			NextStep: task.RunNextStepInTx,
		},
		TaskTerminalEffects: execution.TaskTerminalEffectWriterFunc(task.ApplyRunTerminalInTx),
	})
	if err != nil {
		t.Fatalf("construct account service: %v", err)
	}
	conversationHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Conversation holder: %v", err)
	}
	defer func() { _ = conversationHolder.Rollback(context.Background()) }()
	if _, err := generated.New(conversationHolder).LockConversationForSystemRunQueueMutation(
		ctx,
		generated.LockConversationForSystemRunQueueMutationParams{
			ConversationID: fixture.ConversationA,
			CompanyID:      fixture.CompanyA,
		},
	); err != nil {
		t.Fatalf("hold Task Conversation lock: %v", err)
	}
	deleteResult := make(chan error, 1)
	go func() {
		_, deleteErr := service.Delete(ctx, auth.UserPrincipal{
			UserID: fixture.UserA, SessionID: sessionID, AuthKind: auth.TokenKindSession,
		}, Confirmation)
		deleteResult <- deleteErr
	}()
	waitForAccountConversationLock(t, ctx, pool)
	if _, err := conversationHolder.Exec(ctx, `SET LOCAL statement_timeout = '2s'`); err != nil {
		t.Fatalf("set Run lock probe timeout: %v", err)
	}
	if _, err := conversationHolder.Exec(
		ctx,
		`UPDATE runs SET updated_at = clock_timestamp() WHERE id = $1`,
		runID,
	); err != nil {
		t.Fatalf("account deletion locked Run before Conversation: %v", err)
	}
	if err := conversationHolder.Rollback(ctx); err != nil {
		t.Fatalf("release Task Conversation lock: %v", err)
	}
	select {
	case err := <-deleteResult:
		if err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Delete() did not resume after Conversation lock release")
	}

	var taskStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM tasks WHERE id = $1`, fixture.TaskA).
		Scan(&taskStatus); err != nil {
		t.Fatalf("read cancelled Task: %v", err)
	}
	if taskStatus != "cancelled" {
		t.Fatalf("Task status = %q, want cancelled", taskStatus)
	}
	if len(taskUpdated) != 1 || taskUpdated[0].RunID != runID ||
		taskUpdated[0].TaskID != fixture.TaskA ||
		taskUpdated[0].ConversationID != fixture.ConversationA ||
		taskUpdated[0].Actor != events.SystemActor() {
		t.Fatalf("account-cancelled Task events = %+v", taskUpdated)
	}
}

func newAccountIntegrationService(
	t *testing.T,
	store *db.Store,
	bus *events.Bus,
) *DefaultAccountService {
	t.Helper()
	service, err := NewAccountService(ServiceOptions{
		Store: store,
		Bus:   bus,
		SessionInvalidator: auth.SessionInvalidatorFunc(
			func(context.Context, []auth.SessionRevocation) {},
		),
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check:    task.CheckMessageRunInTx,
			Prepare:  task.PrepareMessageRunInTx,
			NextStep: task.RunNextStepInTx,
		},
		TaskTerminalEffects: execution.TaskTerminalEffectWriterFunc(task.ApplyRunTerminalInTx),
	})
	if err != nil {
		t.Fatalf("construct account service: %v", err)
	}
	return service
}

func waitForBlockedAccountQuery(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	queryName string,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE datname = current_database()
				  AND pid <> pg_backend_pid()
				  AND wait_event_type = 'Lock'
				  AND query LIKE '%' || $1 || '%'
			)`, queryName).Scan(&waiting); err != nil {
			t.Fatalf("observe %s lock wait: %v", queryName, err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s lock: %v", queryName, ctx.Err())
		case <-timeout.C:
			t.Fatalf("%s never waited for its lock", queryName)
		case <-ticker.C:
		}
	}
}

func waitForAccountConversationLock(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE datname = current_database()
				  AND pid <> pg_backend_pid()
				  AND wait_event_type = 'Lock'
				  AND query LIKE '%LockConversationForSystemRunQueueMutation%'
			)`).Scan(&waiting); err != nil {
			t.Fatalf("observe account Conversation lock wait: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for account Conversation lock: %v", ctx.Err())
		case <-timeout.C:
			t.Fatal("account deletion never waited for the Conversation lock")
		case <-ticker.C:
		}
	}
}

// runtimeWakeups records the work-available facts the domain publishes, which
// is what used to be a direct call on a notifier the service held.
type runtimeWakeups struct {
	runs             []string
	companiesChanged []string
}

func recordRuntimeWakeups(t *testing.T, bus *events.Bus) *runtimeWakeups {
	t.Helper()
	recorded := &runtimeWakeups{}
	if err := bus.Subscribe(execution.TopicRuntime,
		func(_ context.Context, event events.Event) error {
			if event.Name != execution.EventRuntimeWorkAvailable {
				return nil
			}
			payload, err := events.PayloadOf[execution.RuntimeWork](event)
			if err != nil {
				return err
			}
			recorded.runs = append(recorded.runs, payload.RunID)
			return nil
		}); err != nil {
		t.Fatalf("subscribe runtime.work_available: %v", err)
	}
	if err := bus.Subscribe(company.Topic,
		func(_ context.Context, event events.Event) error {
			if event.Name != company.EventCompanyMembershipChanged {
				return nil
			}
			payload, err := events.PayloadOf[company.Membership](event)
			if err != nil {
				return err
			}
			recorded.companiesChanged = append(recorded.companiesChanged, payload.UserID)
			return nil
		}); err != nil {
		t.Fatalf("subscribe company.membership_changed: %v", err)
	}
	return recorded
}
