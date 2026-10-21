package approval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	conversationdomain "github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

const (
	lifecycleBatchSize     = 100
	continuationRetryDelay = 30 * time.Second
)

// DefaultLifecycleWorker expires pending human-input requests and reconciles
// their durable continuation receipts. Every claim is SKIP LOCKED and every
// Run insert shares the transaction that marks its owning receipt delivered.
//
// A continuation pass takes its locks in the canonical order every Company
// write uses — Company, then member, then Conversation, then Task — with the
// receipt claimed between Company and member. The Company share lock comes
// first because account deletion holds its Companies FOR UPDATE and then
// abandons their receipts, and a Task mutation holds its Company FOR UPDATE
// and then locks the Conversation; a worker that claimed or locked either one
// first and only then waited for the Company would close a cycle with them.
type DefaultLifecycleWorker struct {
	store          *db.Store
	bus            *events.Bus
	inboxPresenter inbox.Presenter
}

// NewLifecycleWorker builds the worker. inboxPresenter is optional: it only
// raises the desktop toast for an abandoned-continuation Inbox item, whose
// durable row is written either way.
func NewLifecycleWorker(
	store *db.Store,
	bus *events.Bus,
	inboxPresenter inbox.Presenter,
) (*DefaultLifecycleWorker, error) {
	if store == nil || bus == nil {
		return nil, ErrApprovalConfiguration
	}
	return &DefaultLifecycleWorker{store: store, bus: bus, inboxPresenter: inboxPresenter}, nil
}

// Tick is one reconciliation pass. The composition root owns the cadence and
// the loop; a transient failure is logged and retried next pass, and the
// durable rows stay pending because an infrastructure error rolls back its
// own transaction.
func (w *DefaultLifecycleWorker) Tick(ctx context.Context) {
	if err := w.Reconcile(ctx); err != nil && ctx.Err() == nil {
		slog.Error("human-input lifecycle reconciliation failed", "error", err)
	}
}

// Reconcile expires one bounded batch of each request type, then drains one
// bounded batch of each continuation type. A drain stops at the first pass
// that claims nothing, including when another worker holds the receipt it
// found; the next tick resumes it.
func (w *DefaultLifecycleWorker) Reconcile(ctx context.Context) error {
	if err := w.expireApprovals(ctx); err != nil {
		return err
	}
	if err := w.expireQuestions(ctx); err != nil {
		return err
	}
	for range lifecycleBatchSize {
		delivered, err := w.reconcileApprovalContinuation(ctx)
		if err != nil {
			return err
		}
		if !delivered {
			break
		}
	}
	for range lifecycleBatchSize {
		delivered, err := w.reconcileQuestionContinuation(ctx)
		if err != nil {
			return err
		}
		if !delivered {
			break
		}
	}
	return nil
}

func (w *DefaultLifecycleWorker) expireApprovals(ctx context.Context) error {
	var pending []events.Event
	err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		rows, err := q.ExpireDueApprovals(ctx, lifecycleBatchSize)
		if err != nil {
			return fmt.Errorf("expire Approvals: %w", err)
		}
		pending = make([]events.Event, 0, len(rows)*2)
		for _, row := range rows {
			if err := inbox.CloseEntityInTx(
				ctx,
				q,
				&pending,
				row.CompanyID,
				inbox.Entity{Type: protocol.EntityApproval, ID: row.ID.String()},
				inbox.Actor{Kind: "system"},
			); err != nil {
				return fmt.Errorf("close expired Approval Inbox items: %w", err)
			}
			value, err := approvalProjectionForSystem(ctx, q, row)
			if err != nil {
				return err
			}
			if err := stageApprovalEvent(
				ctx, q, &pending,
				EventApprovalExpired,
				events.SystemActor(),
				value,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, group := range events.GroupByTopic(pending) {
		w.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return nil
}

func (w *DefaultLifecycleWorker) expireQuestions(ctx context.Context) error {
	var pending []events.Event
	err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		rows, err := q.ExpireDueQuestions(ctx, lifecycleBatchSize)
		if err != nil {
			return fmt.Errorf("expire Questions: %w", err)
		}
		pending = make([]events.Event, 0, len(rows)*2)
		for _, row := range rows {
			if err := inbox.CloseEntityInTx(
				ctx,
				q,
				&pending,
				row.CompanyID,
				inbox.Entity{Type: protocol.EntityQuestion, ID: row.ID.String()},
				inbox.Actor{Kind: "system"},
			); err != nil {
				return fmt.Errorf("close expired Question Inbox items: %w", err)
			}
			actorID := uuid.Nil
			if !row.TaskID.Valid {
				actorID = row.OriginUserID.UUID
			}
			projection, err := q.GetQuestionProjection(ctx, generated.GetQuestionProjectionParams{
				CompanyID: row.CompanyID, QuestionID: row.ID, ActorUserID: actorID,
			})
			if err != nil {
				return fmt.Errorf("load expired Question projection: %w", err)
			}
			value, err := questionFromProjection(
				projection.Question,
				projection.AskingAgentName,
				projection.ResolvedByDisplayName,
				projection.ResolvedByAvatarUrl,
			)
			if err != nil {
				return fmt.Errorf("decode expired Question projection: %w", err)
			}
			if err := stageQuestionEvent(
				ctx, q, &pending,
				EventQuestionExpired,
				events.SystemActor(),
				value,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, group := range events.GroupByTopic(pending) {
		w.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return nil
}

func (w *DefaultLifecycleWorker) reconcileApprovalContinuation(ctx context.Context) (bool, error) {
	var queued *generated.Run
	var pending []events.Event
	var abandoned *abandonedContinuation
	var notice inbox.CreateResult
	claimed := false
	// WithinRawTx rather than WithinTx: the Run insert runs behind a savepoint
	// (see enqueueContinuation), and taking one needs the transaction handle.
	err := w.store.WithinRawTx(ctx, pgx.TxOptions{}, func(
		tx generated.DBTX,
		q *generated.Queries,
	) error {
		companyID, err := lockDueContinuationCompanyInTx(ctx, q, q.PeekDueApprovalContinuationCompany)
		if err != nil || companyID == uuid.Nil {
			return err
		}
		row, err := q.ClaimDueApprovalContinuation(ctx, companyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim Approval continuation: %w", err)
		}
		claimed = true
		owner, err := lockContinuationOwnerInTx(ctx, q, row.CompanyID, row.ConversationID)
		if err != nil {
			return err
		}
		run, disposition, reason, err := w.enqueueContinuation(
			ctx,
			tx,
			q,
			&pending,
			row.CompanyID,
			row.ConversationID,
			row.RequestedByAgentID,
			row.TaskID,
			row.OriginRunID,
			row.ContinuationPrompt.String,
		)
		if err != nil {
			return err
		}
		switch disposition {
		case continuationEnqueued:
			// The Run insert above and this stamp share the claim's
			// transaction, so no reader ever sees a continuation_run_id
			// without the Run it names, or that Run without the receipt.
			delivered, err := q.DeliverApprovalContinuation(
				ctx,
				generated.DeliverApprovalContinuationParams{
					ContinuationRunID: run.ID, ApprovalID: row.ID,
				},
			)
			if err != nil {
				return fmt.Errorf("deliver Approval continuation receipt: %w", err)
			}
			if _, err := stageApprovalContinuationEvent(ctx, q, &pending, delivered); err != nil {
				return err
			}
			queued = &run
		case continuationDeferred:
			if _, err := q.DeferApprovalContinuation(ctx, generated.DeferApprovalContinuationParams{
				NextAttemptAt: pgtype.Timestamptz{
					Time:  time.Now().UTC().Add(continuationRetryDelay),
					Valid: true,
				},
				ApprovalID: row.ID,
			}); err != nil {
				return fmt.Errorf("defer Approval continuation: %w", err)
			}
		case continuationAbandoned:
			settled, err := q.AbandonApprovalContinuation(
				ctx,
				generated.AbandonApprovalContinuationParams{
					Reason: string(reason), ApprovalID: row.ID,
				},
			)
			if err != nil {
				return fmt.Errorf("abandon Approval continuation: %w", err)
			}
			value, err := stageApprovalContinuationEvent(ctx, q, &pending, settled)
			if err != nil {
				return err
			}
			abandoned = &abandonedContinuation{
				entity:    inbox.Entity{Type: protocol.EntityApproval, ID: row.ID.String()},
				companyID: row.CompanyID, conversationID: row.ConversationID, taskID: row.TaskID,
				originRunID: row.OriginRunID, agentID: row.RequestedByAgentID,
				agentName: value.RequestedByAgent.DisplayName, requestTitle: row.Title,
				reason: reason, ownerUserID: owner,
			}
			notice, err = noticeAbandonedContinuationInTx(ctx, q, &pending, *abandoned)
			if err != nil {
				return err
			}
			queued, err = conversationdomain.AbandonChannelContinuationInTx(
				ctx,
				q,
				&pending,
				row.OriginRunID,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	w.publishContinuation(ctx, pending, abandoned, notice, queued)
	return claimed, nil
}

func (w *DefaultLifecycleWorker) reconcileQuestionContinuation(ctx context.Context) (bool, error) {
	var queued *generated.Run
	var pending []events.Event
	var abandoned *abandonedContinuation
	var notice inbox.CreateResult
	claimed := false
	// Raw transaction for the same reason as the Approval reconciler: the Run
	// insert needs a savepoint under it. The lock order is the Approval one.
	err := w.store.WithinRawTx(ctx, pgx.TxOptions{}, func(
		tx generated.DBTX,
		q *generated.Queries,
	) error {
		companyID, err := lockDueContinuationCompanyInTx(ctx, q, q.PeekDueQuestionContinuationCompany)
		if err != nil || companyID == uuid.Nil {
			return err
		}
		row, err := q.ClaimDueQuestionContinuation(ctx, companyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim Question continuation: %w", err)
		}
		claimed = true
		owner, err := lockContinuationOwnerInTx(ctx, q, row.CompanyID, row.ConversationID)
		if err != nil {
			return err
		}
		run, disposition, reason, err := w.enqueueContinuation(
			ctx,
			tx,
			q,
			&pending,
			row.CompanyID,
			row.ConversationID,
			row.AskedByAgentID,
			row.TaskID,
			row.OriginRunID,
			row.ContinuationPrompt.String,
		)
		if err != nil {
			return err
		}
		switch disposition {
		case continuationEnqueued:
			// Same transaction as the Run insert, for the same reason.
			delivered, err := q.DeliverQuestionContinuation(
				ctx,
				generated.DeliverQuestionContinuationParams{
					ContinuationRunID: run.ID, QuestionID: row.ID,
				},
			)
			if err != nil {
				return fmt.Errorf("deliver Question continuation receipt: %w", err)
			}
			if _, err := stageQuestionContinuationEvent(ctx, q, &pending, delivered); err != nil {
				return err
			}
			queued = &run
		case continuationDeferred:
			if _, err := q.DeferQuestionContinuation(ctx, generated.DeferQuestionContinuationParams{
				NextAttemptAt: pgtype.Timestamptz{
					Time:  time.Now().UTC().Add(continuationRetryDelay),
					Valid: true,
				},
				QuestionID: row.ID,
			}); err != nil {
				return fmt.Errorf("defer Question continuation: %w", err)
			}
		case continuationAbandoned:
			settled, err := q.AbandonQuestionContinuation(
				ctx,
				generated.AbandonQuestionContinuationParams{
					Reason: string(reason), QuestionID: row.ID,
				},
			)
			if err != nil {
				return fmt.Errorf("abandon Question continuation: %w", err)
			}
			value, err := stageQuestionContinuationEvent(ctx, q, &pending, settled)
			if err != nil {
				return err
			}
			abandoned = &abandonedContinuation{
				entity:    inbox.Entity{Type: protocol.EntityQuestion, ID: row.ID.String()},
				companyID: row.CompanyID, conversationID: row.ConversationID, taskID: row.TaskID,
				originRunID: row.OriginRunID, agentID: row.AskedByAgentID,
				agentName: value.AskedByAgent.DisplayName, requestTitle: row.Title,
				reason: reason, ownerUserID: owner,
			}
			notice, err = noticeAbandonedContinuationInTx(ctx, q, &pending, *abandoned)
			if err != nil {
				return err
			}
			queued, err = conversationdomain.AbandonChannelContinuationInTx(
				ctx,
				q,
				&pending,
				row.OriginRunID,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	w.publishContinuation(ctx, pending, abandoned, notice, queued)
	return claimed, nil
}

// publishContinuation announces one committed continuation pass: the log line
// for an abandonment, the staged frames, the owner's toast and the daemon
// wake. It runs only after commit, so nothing it says can be rolled back.
func (w *DefaultLifecycleWorker) publishContinuation(
	ctx context.Context,
	pending []events.Event,
	abandoned *abandonedContinuation,
	notice inbox.CreateResult,
	queued *generated.Run,
) {
	if abandoned != nil {
		abandoned.log(notice.Created)
	}
	for _, group := range events.GroupByTopic(pending) {
		w.bus.Publish(ctx, group.Topic, group.Events...)
	}
	inbox.PresentCreated(w.inboxPresenter, notice)
	if queued != nil {
		execution.PublishRuntimeWork(ctx, w.bus, *queued)
	}
}

// lockDueContinuationCompanyInTx takes the share lock on the Company of the
// oldest due receipt, which peek names without locking anything, and returns
// that Company for the claim to stay inside. uuid.Nil means there is nothing
// to claim: no receipt is due, or its Company began deleting after the peek.
func lockDueContinuationCompanyInTx(
	ctx context.Context,
	q *generated.Queries,
	peek func(context.Context) (uuid.UUID, error),
) (uuid.UUID, error) {
	companyID, err := peek(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("find due continuation Company: %w", err)
	}
	if _, err := q.LockCompanyForMembershipRead(ctx, companyID); errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	} else if err != nil {
		return uuid.Nil, fmt.Errorf("lock continuation Company: %w", err)
	}
	return companyID, nil
}

// lockContinuationOwnerInTx names the member a continuation runs for — a
// Task's current assignee, a direct chat's creator (ADR 0014) — and takes
// their membership share lock, still ahead of the Conversation lock. Under
// the Company share lock the caller holds, no Task mutation can move the
// assignee before commit. uuid.Nil means there is nobody an abandonment could
// be reported to: a channel tag (its channel shows the tag blocked), a Task
// or chat that is gone, or an owner who is no longer an active member.
func lockContinuationOwnerInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, conversationID uuid.UUID,
) (uuid.UUID, error) {
	owner, err := continuationOwnerInTx(ctx, q, companyID, conversationID)
	if err != nil || owner == uuid.Nil {
		return uuid.Nil, err
	}
	eligible, err := inbox.LockRecipientInTx(ctx, q, companyID, owner)
	if err != nil {
		return uuid.Nil, fmt.Errorf("lock continuation owner: %w", err)
	}
	if !eligible {
		return uuid.Nil, nil
	}
	return owner, nil
}

type continuationDisposition uint8

const (
	continuationEnqueued continuationDisposition = iota
	continuationDeferred
	continuationAbandoned
)

// continuationAbandonReason is why a continuation will never run. The receipt
// records it in continuation_abandon_reason and the worker logs it; when the
// owner can do something about it, their Inbox item explains it.
type continuationAbandonReason string

const (
	// The Conversation is gone, archived, or no longer the asking Agent's.
	abandonConversationUnavailable continuationAbandonReason = "conversation_unavailable"
	// The Task is gone, or it moved to another Agent or into a member's hands.
	abandonTaskUnavailable continuationAbandonReason = "task_unavailable"
	// The Task admits no Run now. A member closed it (done or cancelled); its
	// Agent declared it failed; it waits on a prerequisite; or its Project is
	// archived.
	abandonTaskClosed            continuationAbandonReason = "task_closed"
	abandonTaskFailed            continuationAbandonReason = "task_failed"
	abandonTaskDependencyBlocked continuationAbandonReason = "task_dependency_blocked"
	abandonProjectArchived       continuationAbandonReason = "project_archived"
	// Nobody is left to run it for: a direct chat without its creator, or a
	// channel tag without the member who made it.
	abandonOwnerUnavailable continuationAbandonReason = "owner_unavailable"
	abandonAgentArchived    continuationAbandonReason = "agent_archived"
	// The owner has not set the Agent up on a computer of theirs (ADR 0014).
	abandonAgentNotBound           continuationAbandonReason = "agent_not_bound"
	abandonTaskAssigneeRequired    continuationAbandonReason = "task_assignee_required"
	abandonChannelInvocationDenied continuationAbandonReason = "channel_invocation_denied"
	// The owner no longer holds project.tasks.execute on the Task chat's
	// Project (ADR 0017): the enqueue kernel refused the Run they authorize.
	abandonProjectAccessRevoked continuationAbandonReason = execution.RunReasonProjectAccessRevoked
)

// notifiesOwner reports whether the owner is told. Only a cause they can fix
// and then retry is worth a run_failed item. A handoff, an archived
// Conversation or a closed Task is itself a member's decision, and after a
// handoff the new assignee never saw the request; the other causes leave
// nobody to tell.
func (reason continuationAbandonReason) notifiesOwner() bool {
	switch reason {
	case abandonAgentNotBound, abandonAgentArchived, abandonTaskAssigneeRequired,
		abandonTaskFailed, abandonTaskDependencyBlocked, abandonProjectArchived,
		abandonProjectAccessRevoked:
		return true
	default:
		return false
	}
}

// enqueueContinuation materializes the Run a terminal Approval or Question
// asked for. The identity comes from the database-generated UUID like every other Run's —
// nothing named it in advance — so a deferred or abandoned continuation leaves
// the owning row's continuation_run_id NULL instead of pointing at a Run that
// was never written. The caller stamps the returned Run's id onto that row in
// this same transaction, through the Deliver…Continuation statement.
//
// The insert runs behind a savepoint because one of its failures is an
// expected answer rather than a fault: the one-pending-per-(task, agent) unique
// index rejects it whenever that agent already has work queued, and the right
// response is to write a later retry time onto the receipt this pass claimed.
// A failed statement aborts the whole transaction, so with no marker to roll
// back to, that retry write fails with 25P02 and takes the claim, the attempt
// counter and the retry time down with it — leaving the receipt immediately due
// again. Reconcile drains oldest-first and returns on the first error, so a
// single such receipt would then block every continuation behind it.
//
// An abandoned continuation also returns why, for the receipt to record and
// the owner's Inbox item to explain.
func (w *DefaultLifecycleWorker) enqueueContinuation(
	ctx context.Context,
	tx generated.DBTX,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, conversationID, agentID uuid.UUID,
	taskID uuid.NullUUID,
	originRunID uuid.UUID,
	prompt string,
) (generated.Run, continuationDisposition, continuationAbandonReason, error) {
	conversation, err := q.LockConversationForSystemRunQueueMutation(
		ctx,
		generated.LockConversationForSystemRunQueueMutationParams{
			ConversationID: conversationID,
			CompanyID:      companyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.Run{}, continuationAbandoned, abandonConversationUnavailable, nil
	}
	if err != nil {
		return generated.Run{}, 0, "", fmt.Errorf("lock continuation Conversation: %w", err)
	}
	if conversation.ArchivedAt.Valid || !continuationConversationMatches(
		conversation,
		agentID,
		taskID,
	) {
		return generated.Run{}, continuationAbandoned, abandonConversationUnavailable, nil
	}
	if _, err := q.GetConversationCurrentRun(
		ctx,
		generated.GetConversationCurrentRunParams{
			ConversationID: conversationID,
			CompanyID:      companyID,
		},
	); err == nil {
		return generated.Run{}, continuationDeferred, "", nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return generated.Run{}, 0, "", fmt.Errorf("check continuation Conversation current Run: %w", err)
	}

	priority := int32(0)
	var continuationTask generated.Task
	if taskID.Valid {
		continuationTask, err = q.GetTaskForCompanyByIDForUpdate(
			ctx,
			generated.GetTaskForCompanyByIDForUpdateParams{
				TaskID: taskID.UUID, CompanyID: companyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return generated.Run{}, continuationAbandoned, abandonTaskUnavailable, nil
		}
		if err != nil {
			return generated.Run{}, 0, "", fmt.Errorf("lock continuation Task: %w", err)
		}
		taskAgent, err := q.GetTaskRunAgent(
			ctx,
			generated.GetTaskRunAgentParams{CompanyID: companyID, TaskID: taskID.UUID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return generated.Run{}, continuationAbandoned, abandonTaskUnavailable, nil
		}
		if err != nil {
			return generated.Run{}, 0, "", fmt.Errorf("resolve continuation Task Agent: %w", err)
		}
		origin, err := q.GetRun(ctx, originRunID)
		if err != nil {
			return generated.Run{}, 0, "", fmt.Errorf("read Task continuation origin: %w", err)
		}
		if continuationTask.ConversationID != conversationID || taskAgent.ID != agentID ||
			continuationTask.ExecutionGeneration != origin.TaskExecutionGeneration.Int64 ||
			continuationTask.AssigneeUserID != origin.ExecutionAuthorizedByUserID.UUID {
			return generated.Run{}, continuationAbandoned, abandonTaskUnavailable, nil
		}
		priority = execution.PriorityToInt(continuationTask.Priority)
	}
	// The Conversation's owner authorizes the continuation (ADR 0014): a
	// Task's current assignee, a direct chat's creator. The Run executes on
	// the owner's own binding of the Agent, so the member who decided is
	// recorded on the receipt but never becomes the authorizer. A direct
	// chat whose creator is gone has nobody to run it for.
	//
	// A channel_agent Conversation is shared by every member who tags the
	// Agent in that channel, and its creator is the Agent's creator, so the
	// owner of this work is the member whose tag started it: the origin
	// Run's authorizer, on whose computer it ran. The continuation resumes
	// that same invocation there.
	authorizer := uuid.Nil
	var invocationID uuid.NullUUID
	switch {
	case taskID.Valid:
		authorizer = continuationTask.AssigneeUserID
	case conversation.Kind == "channel_agent":
		origin, originErr := q.GetRun(ctx, originRunID)
		if errors.Is(originErr, pgx.ErrNoRows) {
			return generated.Run{}, continuationAbandoned, abandonOwnerUnavailable, nil
		}
		if originErr != nil {
			return generated.Run{}, 0, "", fmt.Errorf("load continuation origin Run: %w", originErr)
		}
		if !origin.InvocationID.Valid || !origin.ExecutionAuthorizedByUserID.Valid {
			return generated.Run{}, continuationAbandoned, abandonOwnerUnavailable, nil
		}
		invocationID = origin.InvocationID
		authorizer = origin.ExecutionAuthorizedByUserID.UUID
	case conversation.CreatedByUserID.Valid:
		authorizer = conversation.CreatedByUserID.UUID
	}
	if authorizer == uuid.Nil {
		return generated.Run{}, continuationAbandoned, abandonOwnerUnavailable, nil
	}
	// The receipt's prompt is the words this Run starts on, so it travels as
	// TriggerPrompt and the enqueue kernel freezes it under `prompt` — the same
	// seam a user message uses. Composing that key by hand here would be a
	// second author of one fact.
	//
	// The Task lock above stays outside the savepoint deliberately: it
	// serializes this whole pass against a concurrent Task write, and rolling
	// back a failed insert must not release it.
	var run generated.Run
	var attemptPending []events.Event
	err = w.store.TryWithinSavepoint(ctx, tx, func(q *generated.Queries) error {
		if taskID.Valid {
			allowed, prepareErr := task.PrepareContinuationRunInTx(
				ctx,
				q,
				&attemptPending,
				continuationTask,
			)
			if prepareErr != nil {
				return prepareErr
			}
			if !allowed {
				return errContinuationAbandoned
			}
		}
		var enqueueErr error
		run, enqueueErr = execution.EnqueueRunInTx(ctx, q, &attemptPending, execution.RunTrigger{
			CompanyID: companyID, ConversationID: conversationID, InvocationID: invocationID,
			AgentID: agentID, TaskID: taskID, Priority: priority,
			TriggerPrompt: prompt, Source: execution.RunSourceHumanInput,
			AuthorizedByUserID: authorizer,
		})
		if enqueueErr != nil {
			return enqueueErr
		}
		// The colleagues' saved Task Messages ride into the transcript with
		// this Run, exactly as the kernel froze them into its snapshot.
		_, enqueueErr = conversationdomain.MaterializeQueuedTaskMessagesInTx(ctx, q, &attemptPending, run)
		return enqueueErr
	})
	if errors.Is(err, execution.ErrDuplicatePendingRun) ||
		errors.Is(err, execution.ErrTaskMergeInProgress) {
		return generated.Run{}, continuationDeferred, "", nil
	}
	// An archived agent, or an Agent the owner has not set up on a runtime
	// of theirs, is refused by a zero-row SELECT before anything is
	// written, so it never aborted the transaction and the rollback to the
	// savepoint discards nothing. The receipt records which refusal it was
	// and the owner is told; they continue by setting the Agent up and
	// asking again. A Task whose status admits no continuation is closed or
	// failed, and the two are told apart because only the failed one is
	// worth telling its owner about.
	if err != nil {
		reason, abandoned := classifyContinuationRefusal(err, task.Status(continuationTask.Status))
		if !abandoned {
			return generated.Run{}, 0, "", fmt.Errorf("enqueue human-input continuation Run: %w", err)
		}
		return generated.Run{}, continuationAbandoned, reason, nil
	}
	*pending = append(*pending, attemptPending...)
	return run, continuationEnqueued, "", nil
}

var errContinuationAbandoned = errors.New("continuation target is no longer executable")

// classifyContinuationRefusal names the reason a continuation is abandoned
// for an enqueue refusal: the Task closed or failed (told apart by its
// status), a blocked dependency, an archived Project or Agent, an Agent not
// set up on the member's computer, a Run the assignee did not authorize, a
// channel tag no longer valid, or the assignee's lost project.tasks.execute
// (ADR 0017). Any other error is not a refusal (false).
func classifyContinuationRefusal(err error, taskStatus task.Status) (continuationAbandonReason, bool) {
	switch {
	case errors.Is(err, errContinuationAbandoned):
		if taskStatus == task.StatusFailed {
			return abandonTaskFailed, true
		}
		return abandonTaskClosed, true
	case errors.Is(err, task.ErrDependencyBlocked):
		return abandonTaskDependencyBlocked, true
	case errors.Is(err, task.ErrProjectArchived):
		return abandonProjectArchived, true
	case errors.Is(err, execution.ErrRunAgentArchived):
		return abandonAgentArchived, true
	case errors.Is(err, execution.ErrRunAgentNotBound):
		return abandonAgentNotBound, true
	case errors.Is(err, execution.ErrRunTaskAssigneeRequired):
		return abandonTaskAssigneeRequired, true
	case errors.Is(err, execution.ErrChannelInvocationDenied):
		return abandonChannelInvocationDenied, true
	case errors.Is(err, auth.ErrProjectPermissionRequired):
		return abandonProjectAccessRevoked, true
	}
	return "", false
}

func continuationConversationMatches(
	conversation generated.Conversation,
	agentID uuid.UUID,
	taskID uuid.NullUUID,
) bool {
	if taskID.Valid {
		return conversation.Kind == "task" &&
			conversation.TaskID.Valid &&
			conversation.TaskID.UUID == taskID.UUID
	}
	return (conversation.Kind == "direct" || conversation.Kind == "channel_agent") &&
		conversation.AgentID.Valid &&
		conversation.AgentID.UUID == agentID
}

// stageApprovalContinuationEvent re-sends the Approval once its receipt has
// settled — a Run stamped on it, or none ever — at the revision that write
// advanced, so a client holding the decided Approval learns it. The frame
// refreshes a resolution the Activity sink already recorded, so it is kept
// out of that sink rather than recorded twice.
func stageApprovalContinuationEvent(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	row generated.Approval,
) (Approval, error) {
	value, err := approvalProjectionForSystem(ctx, q, row)
	if err != nil {
		return Approval{}, err
	}
	event := approvalEvent(EventApprovalResolved, events.SystemActor(), value)
	event.ActivityRecorded = true
	if err := stageApprovalFrames(ctx, q, pending, event); err != nil {
		return Approval{}, err
	}
	return value, nil
}

// stageQuestionContinuationEvent is stageApprovalContinuationEvent for a
// Question.
func stageQuestionContinuationEvent(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	row generated.Question,
) (Question, error) {
	value, err := questionProjectionForSystem(ctx, q, row)
	if err != nil {
		return Question{}, err
	}
	event := questionEvent(EventQuestionResolved, events.SystemActor(), value)
	event.ActivityRecorded = true
	if err := stageApprovalFrames(ctx, q, pending, event); err != nil {
		return Question{}, err
	}
	return value, nil
}

// abandonedContinuation is a continuation that will never run: the request
// it followed, where it would have run, why it will not, and the member it
// would have run for (uuid.Nil when there is nobody to tell).
type abandonedContinuation struct {
	entity         inbox.Entity
	companyID      uuid.UUID
	conversationID uuid.UUID
	taskID         uuid.NullUUID
	originRunID    uuid.UUID
	agentID        uuid.UUID
	agentName      string
	requestTitle   string
	reason         continuationAbandonReason
	ownerUserID    uuid.UUID
}

// noticeAbandonedContinuationInTx tells the owner why the Agent stopped and
// what to do next, in the transaction that abandons the continuation, when
// there is an owner and the reason is one they can act on. The caller took
// the owner's Company and membership locks before the Conversation's, so the
// ones CreateInTx takes are already held.
func noticeAbandonedContinuationInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	notice abandonedContinuation,
) (inbox.CreateResult, error) {
	if notice.ownerUserID == uuid.Nil || !notice.reason.notifiesOwner() {
		return inbox.CreateResult{}, nil
	}
	result, err := inbox.CreateInTx(ctx, q, pending, abandonedContinuationInboxInput(notice))
	if err != nil {
		return inbox.CreateResult{}, fmt.Errorf("create abandoned continuation Inbox item: %w", err)
	}
	return result, nil
}

// log records a committed abandonment for operators.
func (notice abandonedContinuation) log(ownerNotified bool) {
	attributes := []any{
		"entity_type", string(notice.entity.Type),
		"entity_id", notice.entity.ID,
		"company_id", notice.companyID.String(),
		"conversation_id", notice.conversationID.String(),
		"agent_id", notice.agentID.String(),
		"reason", string(notice.reason),
		"owner_notified", ownerNotified,
	}
	if notice.ownerUserID != uuid.Nil {
		attributes = append(attributes, "owner_user_id", notice.ownerUserID.String())
	}
	slog.Info("human-input continuation abandoned", attributes...)
}

// continuationOwnerInTx names the member a continuation runs for, or uuid.Nil
// when there is nobody to tell. It reads without locking; see
// lockContinuationOwnerInTx.
func continuationOwnerInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, conversationID uuid.UUID,
) (uuid.UUID, error) {
	task, err := q.GetTaskByConversation(ctx, generated.GetTaskByConversationParams{
		ConversationID: conversationID, CompanyID: companyID,
	})
	if err == nil {
		return task.AssigneeUserID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("read abandoned continuation Task: %w", err)
	}
	route, err := q.GetConversationRoute(ctx, generated.GetConversationRouteParams{
		ConversationID: conversationID, CompanyID: companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("read abandoned continuation Conversation: %w", err)
	}
	if route.Kind != "direct" {
		return uuid.Nil, nil
	}
	return route.CreatedByUserID.UUID, nil
}

// abandonedContinuationInboxInput renders the owner's item. It is a
// run_failed item because that is what happened to them: the Agent's next
// turn failed and Zenith will not retry it. So it looks, groups and opens
// like one — the deep link lands on the Run that asked. Names and titles are
// bounded so the body always fits the Inbox's limits; a notice the Inbox
// rejected would roll back the abandonment and wedge the queue behind it.
//
// It only names a step the member can take. A Task goes on from its chat,
// never "run the task again": a Task waiting on a request stays in
// progress, where the Run button is not offered (nor in review, nor
// failed), while the server accepts a reply in its chat once the cause is
// fixed. An archived Agent comes back only when someone allowed to
// restores it.
func abandonedContinuationInboxInput(notice abandonedContinuation) inbox.CreateInput {
	agent := noticeLabel(notice.agentName, 64)
	request := "“" + noticeLabel(notice.requestTitle, 120) + "”"
	again := "ask again in the chat"
	if notice.taskID.Valid {
		again = "reply in the task's chat"
	}
	var body string
	switch notice.reason {
	case abandonAgentNotBound:
		body = agent + " is not set up on your computer, so it could not continue after " + request +
			". Set " + agent + " up on your computer, then " + again + "."
	case abandonAgentArchived:
		body = agent + " is archived, so it could not continue after " + request +
			". Once " + agent + " is restored, " + again + "."
	case abandonTaskFailed:
		body = "This task failed, so " + agent + " could not continue after " + request +
			". Reply in its chat to run " + agent + " again."
	case abandonTaskDependencyBlocked:
		body = "This task is waiting on a task it depends on, so " + agent +
			" could not continue after " + request + ". When that task is done, reply in this task's chat."
	case abandonProjectArchived:
		body = "This task's project is archived, so " + agent + " could not continue after " + request + "."
	case abandonProjectAccessRevoked:
		body = "Your access to this work no longer includes running tasks, so " + agent +
			" could not continue after " + request + ". Ask someone who manages this work to let you run tasks, then " +
			again + "."
	default:
		body = agent + " could not continue after " + request + ". To go on, " + again + "."
	}
	deepLink := requestChatDeepLink(notice.agentID, notice.conversationID, notice.taskID) +
		"&runId=" + notice.originRunID.String()
	return inbox.CreateInput{
		CompanyID:       notice.companyID,
		RecipientUserID: notice.ownerUserID,
		ConversationID:  &notice.conversationID,
		Type:            inbox.TypeRunFailed,
		Entity:          notice.entity,
		Title:           agent + " could not continue",
		Body:            body,
		DeepLink:        &deepLink,
		DedupeKey:       string(notice.entity.Type) + ":" + notice.entity.ID + ":continuation_abandoned",
		Actor:           inbox.Actor{Kind: "system"},
	}
}

// noticeLabel is one line of display text cut to maximum runes.
func noticeLabel(value string, maximum int) string {
	label := []rune(strings.Join(strings.Fields(value), " "))
	if len(label) > maximum {
		return string(label[:maximum-1]) + "…"
	}
	return string(label)
}
