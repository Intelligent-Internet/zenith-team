package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const runQueueRemovalReasonUser = "user_deleted"

const runQueueRemovalReasonAuthorizationRevoked = "authorization_revoked"

// runQueueRemovalReasonProjectAccessRevoked marks the items of someone who
// may no longer post in a Project's Task chats (ADR 0017).
const runQueueRemovalReasonProjectAccessRevoked = "project_access_revoked"

type QueuePromotion struct {
	RunID        *uuid.UUID
	RemovedCount int
}

// FinalizeRunConversationInTx materializes the next queued user prompt after a
// Run reaches a terminal state, inside that terminal transaction, so a
// committed terminal state can never strand its queue successor.
//
// Chat replies remain in the Conversation transcript. Document runs also publish
// their final reply into the originating document comment thread.
func FinalizeRunConversationInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	taskMessageRuns TaskMessageRunPreparer,
	run generated.Run,
	finalOutput string,
	policy ...wiki.UIAuthoringPolicy,
) (QueuePromotion, error) {
	if run.InvocationID.Valid {
		return finalizeChannelInvocation(ctx, q, pending, run)
	}
	if q == nil || pending == nil || taskMessageRuns == nil {
		return QueuePromotion{}, ErrInvalidInput
	}
	var authoring wiki.UIAuthoringPolicy
	if len(policy) > 0 {
		authoring = policy[0]
	}
	if err := canvas.CompleteDocumentAgentInTx(
		ctx,
		q,
		pending,
		canvas.DocumentAgentCompletion{Run: run, Body: finalOutput, Authoring: authoring},
	); err != nil {
		return QueuePromotion{}, err
	}
	beforeHistory, err := activity.ObserveInTx(ctx, q, Topic, run.ConversationID)
	if err != nil {
		return QueuePromotion{}, err
	}
	promotion, err := PromoteRunQueueInTx(
		ctx,
		q,
		pending,
		taskMessageRuns,
		run.CompanyID,
		run.ConversationID,
	)
	if err != nil {
		return QueuePromotion{}, err
	}
	if err := BumpAndStageRunConversationUpdates(
		ctx,
		q,
		pending,
		string(events.ActorTypeSystem),
		nil,
		run.CompanyID,
		[]uuid.UUID{run.ConversationID},
		beforeHistory,
	); err != nil {
		return QueuePromotion{}, fmt.Errorf("stage terminal Run Conversation update: %w", err)
	}
	return promotion, nil
}

// LockAuthorizationRevocationRunQueuesInTx locks every Conversation whose
// active Run or queued prompt can be affected by revoking one user's Company
// authority. The caller supplies the plain-read Run candidates, while this
// domain adds its queued-prompt candidates; the union is deduplicated, sorted,
// and locked before the caller mutates any Run or queue row.
func LockAuthorizationRevocationRunQueuesInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	userID uuid.UUID,
	activeRunConversationIDs []uuid.UUID,
) ([]uuid.UUID, error) {
	if q == nil || !util.ValidUUID(companyID) || !util.ValidUUID(userID) {
		return nil, ErrInvalidInput
	}
	queuedConversationIDs, err := listQueuedRunQueueConversationIDsForUser(
		ctx,
		q,
		companyID,
		userID,
	)
	if err != nil {
		return nil, err
	}
	unique := make(map[uuid.UUID]struct{}, len(activeRunConversationIDs)+len(queuedConversationIDs))
	for _, conversationID := range activeRunConversationIDs {
		if !util.ValidUUID(conversationID) {
			return nil, ErrInvalidInput
		}
		unique[conversationID] = struct{}{}
	}
	for _, conversationID := range queuedConversationIDs {
		unique[conversationID] = struct{}{}
	}
	conversationIDs := make([]uuid.UUID, 0, len(unique))
	for conversationID := range unique {
		conversationIDs = append(conversationIDs, conversationID)
	}
	sortUUIDs(conversationIDs)
	if err := lockSystemRunQueueConversations(ctx, q, companyID, conversationIDs); err != nil {
		return nil, err
	}
	return conversationIDs, nil
}

// FinalizeAuthorizationRevocationRunQueuesInTx removes a user's deferred
// prompts after their Company authority is revoked, then promotes at most one
// eligible successor in every affected Conversation. The caller has already
// locked conversationIDs through LockAuthorizationRevocationRunQueuesInTx and
// then transitioned the affected Runs, preserving Conversation -> Run order.
func FinalizeAuthorizationRevocationRunQueuesInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	taskMessageRuns TaskMessageRunPreparer,
	companyID uuid.UUID,
	userID uuid.UUID,
	conversationIDs []uuid.UUID,
) ([]generated.Run, error) {
	if q == nil || pending == nil || taskMessageRuns == nil ||
		!util.ValidUUID(companyID) || !util.ValidUUID(userID) {
		return nil, ErrInvalidInput
	}
	for _, conversationID := range conversationIDs {
		if !util.ValidUUID(conversationID) {
			return nil, ErrInvalidInput
		}
	}
	observed := make([]*activity.Snapshot, 0, len(conversationIDs))
	for _, id := range conversationIDs {
		before, err := activity.ObserveInTx(ctx, q, Topic, id)
		if err != nil {
			return nil, err
		}
		observed = append(observed, before)
	}
	if err := removeQueuedRunQueueItemsForUser(
		ctx, q, pending, companyID, userID, nil, runQueueRemovalReasonAuthorizationRevoked,
	); err != nil {
		return nil, err
	}

	promotedRuns := make([]generated.Run, 0, len(conversationIDs))
	for _, conversationID := range conversationIDs {
		promotion, err := PromoteRunQueueInTx(
			ctx,
			q,
			pending,
			taskMessageRuns,
			companyID,
			conversationID,
		)
		if err != nil {
			return nil, fmt.Errorf("promote authorization-revoked Conversation Run queue: %w", err)
		}
		if promotion.RunID == nil {
			continue
		}
		promoted, err := q.GetRun(ctx, *promotion.RunID)
		if err != nil {
			return nil, fmt.Errorf("load authorization-revoked promoted Run: %w", err)
		}
		promotedRuns = append(promotedRuns, promoted)
	}
	if err := BumpAndStageRunConversationUpdates(
		ctx,
		q,
		pending,
		string(events.ActorTypeSystem),
		nil,
		companyID,
		conversationIDs,
		observed...,
	); err != nil {
		return nil, fmt.Errorf("stage authorization-revoked Conversation updates: %w", err)
	}
	return promotedRuns, nil
}

// RemoveRunQueueItemsForAuthorizationRevocationInTx removes every deferred
// prompt and saved Task Message queued by a user whose Company authority was
// revoked, closes the nudges about them and bumps the affected
// Conversations. The caller owns the Company and membership/user locks and
// calls this before it locks any Run, so Conversation locks come first.
// Removed rows remain as replay/audit tombstones while their Attachment
// reservations become reusable in the same commit. Nothing is promoted: a
// removal ends no Run, so no queue head is waiting on one.
func RemoveRunQueueItemsForAuthorizationRevocationInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID uuid.UUID,
	userID uuid.UUID,
) ([]uuid.UUID, error) {
	if q == nil || pending == nil || !util.ValidUUID(companyID) || !util.ValidUUID(userID) {
		return nil, ErrInvalidInput
	}
	conversationIDs, err := LockAuthorizationRevocationRunQueuesInTx(
		ctx,
		q,
		companyID,
		userID,
		nil,
	)
	if err != nil {
		return nil, err
	}
	if err := removeLockedRunQueueItemsInTx(
		ctx, q, pending, companyID, userID, conversationIDs, nil, runQueueRemovalReasonAuthorizationRevoked,
	); err != nil {
		return nil, err
	}
	return conversationIDs, nil
}

// RemoveProjectRunQueueItemsForAccessRevocationInTx removes the person's
// deferred prompts and saved Task Messages in one Project's Task chats after
// a Project access change took away their posting there (project.read or
// project.tasks.write, ADR 0017). Their saved Messages could never be
// written under their name, and a queue head nobody may post would hold the
// assignee's next Run. The caller holds the Company, member and Project
// locks and calls this before it locks any Run; the chats are locked here,
// in id order. Nothing is promoted: a removal ends no Run.
func RemoveProjectRunQueueItemsForAccessRevocationInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, projectID, userID uuid.UUID,
) error {
	if q == nil || pending == nil || !util.ValidUUID(companyID) ||
		!util.ValidUUID(projectID) || !util.ValidUUID(userID) {
		return ErrInvalidInput
	}
	conversationIDs, err := q.ListQueuedRunQueueConversationIDsForUser(
		ctx,
		generated.ListQueuedRunQueueConversationIDsForUserParams{
			CompanyID:   companyID,
			ActorUserID: userID,
			ProjectID:   uuid.NullUUID{UUID: projectID, Valid: true},
		},
	)
	if err != nil {
		return fmt.Errorf("list Project Conversation Run queues: %w", err)
	}
	if len(conversationIDs) == 0 {
		return nil
	}
	if err := lockSystemRunQueueConversations(ctx, q, companyID, conversationIDs); err != nil {
		return err
	}
	return removeLockedRunQueueItemsInTx(
		ctx, q, pending, companyID, userID, conversationIDs, conversationIDs,
		runQueueRemovalReasonProjectAccessRevoked,
	)
}

// removeLockedRunQueueItemsInTx removes the person's queued items (in every
// chat when scope is nil, otherwise in scope) and bumps the locked
// conversationIDs, staging their updates.
func removeLockedRunQueueItemsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, userID uuid.UUID,
	conversationIDs, scope []uuid.UUID,
	reason string,
) error {
	observed := make([]*activity.Snapshot, 0, len(conversationIDs))
	for _, id := range conversationIDs {
		before, err := activity.ObserveInTx(ctx, q, Topic, id)
		if err != nil {
			return err
		}
		observed = append(observed, before)
	}
	if err := removeQueuedRunQueueItemsForUser(
		ctx, q, pending, companyID, userID, scope, reason,
	); err != nil {
		return err
	}
	if err := BumpAndStageRunConversationUpdates(
		ctx,
		q,
		pending,
		string(events.ActorTypeSystem),
		nil,
		companyID,
		conversationIDs,
		observed...,
	); err != nil {
		return fmt.Errorf("stage revoked Conversation queue updates: %w", err)
	}
	return nil
}

func listQueuedRunQueueConversationIDsForUser(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	userID uuid.UUID,
) ([]uuid.UUID, error) {
	rows, err := q.ListQueuedRunQueueConversationIDsForUser(
		ctx,
		generated.ListQueuedRunQueueConversationIDsForUserParams{
			CompanyID:   companyID,
			ActorUserID: userID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list authorization-revoked Conversation Run queues: %w", err)
	}
	return rows, nil
}

func lockSystemRunQueueConversations(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	conversationIDs []uuid.UUID,
) error {
	for _, conversationID := range conversationIDs {
		_, err := q.LockConversationForSystemRunQueueMutation(
			ctx,
			generated.LockConversationForSystemRunQueueMutationParams{
				ConversationID: conversationID,
				CompanyID:      companyID,
			},
		)
		if err != nil {
			return fmt.Errorf("lock authorization-revoked Conversation Run queue: %w", err)
		}
	}
	return nil
}

// removeQueuedRunQueueItemsForUser removes the person's queued items, in
// every chat when conversationIDs is nil, otherwise only in those chats (the
// caller locked them), releases their Attachment reservations and closes
// the nudges about them.
func removeQueuedRunQueueItemsForUser(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID uuid.UUID,
	userID uuid.UUID,
	conversationIDs []uuid.UUID,
	reason string,
) error {
	removed, err := q.RemoveQueuedRunQueueItemsForUser(
		ctx,
		generated.RemoveQueuedRunQueueItemsForUserParams{
			RemovalReason:   reason,
			CompanyID:       companyID,
			ActorUserID:     userID,
			ConversationIds: conversationIDs,
		},
	)
	if err != nil {
		return fmt.Errorf("remove authorization-revoked Conversation Run queues: %w", err)
	}
	if len(removed) == 0 {
		return nil
	}
	queueItemIDs := make([]uuid.UUID, len(removed))
	for index, row := range removed {
		queueItemIDs[index] = row.ID
	}
	if err := q.ReleaseRunQueueItemAttachmentsBulk(ctx, queueItemIDs); err != nil {
		return fmt.Errorf("release authorization-revoked Run queue Attachments: %w", err)
	}
	for _, row := range removed {
		if err := closeQueuedMessageNudgeInTx(
			ctx, q, pending, companyID, row.MessageID, inbox.Actor{Kind: "system"},
		); err != nil {
			return err
		}
	}
	return nil
}

// PromoteRunQueueInTx materializes at most one authorized queue head. The
// caller already owns the Conversation lock and a terminal Run transition.
// Automatic retries remain ahead of user prompts because any nonterminal Run
// makes this helper a no-op. Invalid heads are retained as removed audit rows
// and the helper continues to the next item in the same transaction.
func PromoteRunQueueInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	taskMessageRuns TaskMessageRunPreparer,
	companyID uuid.UUID,
	conversationID uuid.UUID,
) (QueuePromotion, error) {
	if q == nil || pending == nil || taskMessageRuns == nil || !util.ValidUUID(companyID) ||
		!util.ValidUUID(conversationID) {
		return QueuePromotion{}, ErrInvalidInput
	}
	preparing, err := q.IsConversationInitializationPending(
		ctx,
		generated.IsConversationInitializationPendingParams{
			CompanyID: companyID, ConversationID: conversationID,
		},
	)
	if err != nil {
		return QueuePromotion{}, fmt.Errorf("check Run queue initialization guard: %w", err)
	}
	// Terminal Run transactions must still commit. The initialization owner
	// resumes this queue atomically if it completes a receipt itself.
	if preparing {
		return QueuePromotion{}, nil
	}
	if _, err := q.GetConversationCurrentRun(ctx, generated.GetConversationCurrentRunParams{
		ConversationID: conversationID,
		CompanyID:      companyID,
	}); err == nil {
		return QueuePromotion{}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return QueuePromotion{}, fmt.Errorf("check Run queue promotion guard: %w", err)
	}
	// A Task Conversation promotes the assignee's deferred prompts only: a
	// colleague's queued Message is skipped here and carried into the
	// transcript by the assignee's next Run instead (ADR 0014).
	promoter, err := runQueuePromoter(ctx, q, companyID, conversationID)
	if err != nil {
		return QueuePromotion{}, err
	}
	result := QueuePromotion{}
	for result.RemovedCount <= runQueueLimit {
		item, err := q.GetNextRunQueueItemForUpdate(ctx, generated.GetNextRunQueueItemForUpdateParams{
			ConversationID:    conversationID,
			CompanyID:         companyID,
			InitiatedByUserID: promoter,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return result, nil
		}
		if err != nil {
			return QueuePromotion{}, fmt.Errorf("lock Conversation Run queue head: %w", err)
		}
		// Only the item id is reserved: runs.id is a database-generated UUID the insert
		// below takes, so there is no second identity a re-promotion could
		// collide with.
		available, err := q.RunQueueReservedIDsAvailable(ctx, item.MessageID)
		if err != nil {
			return QueuePromotion{}, fmt.Errorf("check promoted Run queue identities: %w", err)
		}
		if !available {
			if err := systemRemoveRunQueueItem(
				ctx,
				q,
				pending,
				item,
				"reserved_identity_conflict",
			); err != nil {
				return QueuePromotion{}, err
			}
			result.RemovedCount++
			continue
		}
		initiatorID := item.InitiatedByUserID
		resolved, taskRow, err := resolveRunQueuePromotionTarget(ctx, q, taskMessageRuns, item)
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
			if err := systemRemoveRunQueueItem(
				ctx,
				q,
				pending,
				item,
				"authorization_or_target_changed",
			); err != nil {
				return QueuePromotion{}, err
			}
			result.RemovedCount++
			continue
		}
		if err != nil {
			return QueuePromotion{}, err
		}
		// Probe run availability BEFORE anything is written: an archived agent
		// or an Agent the original sender no longer has set up on a runtime of
		// theirs invalidates the head like any other stale target, and the
		// loop moves on to the next item in this same transaction rather than
		// aborting it.
		if err := execution.ProbeRunEnqueue(
			ctx, q, item.CompanyID, *resolved.AgentID, item.InitiatedByUserID,
		); err != nil {
			if errors.Is(err, execution.ErrRunAgentArchived) ||
				errors.Is(err, execution.ErrRunAgentNotBound) {
				if err := systemRemoveRunQueueItem(
					ctx,
					q,
					pending,
					item,
					"authorization_or_target_changed",
				); err != nil {
					return QueuePromotion{}, err
				}
				result.RemovedCount++
				continue
			}
			return QueuePromotion{}, fmt.Errorf("probe promoted Conversation Run target: %w", err)
		}
		// The Run goes in first and the reserved item after it, carrying the
		// run_id: runs.id is a database-generated UUID, so the identity flows Run -> item
		// and never the other way. The probe above still runs before either
		// write, so an unavailable target invalidates the head instead of
		// leaving a Run with no words behind it.
		//
		// Kernel failures past the probe are genuine transaction errors: the
		// promotion guard already proved no current Run exists, so the
		// duplicate-pending slot cannot legitimately fire.
		taskPriority := int32(2)
		if taskRow != nil {
			taskPriority = execution.PriorityToInt(taskRow.Priority)
			allowed, err := taskMessageRuns.PrepareTaskMessageRunInTx(
				ctx,
				q,
				pending,
				item.InitiatedByUserID,
				*taskRow,
			)
			if err != nil && !errors.Is(err, auth.ErrProjectPermissionRequired) {
				return QueuePromotion{}, fmt.Errorf("prepare promoted Task Message Run: %w", err)
			}
			if !allowed || err != nil {
				if err := systemRemoveRunQueueItem(
					ctx,
					q,
					pending,
					item,
					"authorization_or_target_changed",
				); err != nil {
					return QueuePromotion{}, err
				}
				result.RemovedCount++
				continue
			}
		}
		routing, err := resolveConversationRouting(ctx, q, item.CompanyID, item.ConversationID)
		if err != nil {
			return QueuePromotion{}, err
		}
		// A Task Run reads the files the promoted message is about to carry.
		var triggerAttachmentIDs []uuid.UUID
		if resolved.TaskID != nil {
			triggerAttachmentIDs, err = q.ListRunQueueAttachmentIDs(ctx, item.ID)
			if err != nil {
				return QueuePromotion{}, fmt.Errorf(
					"list promoted Conversation Run queue Attachments: %w",
					err,
				)
			}
		}
		run, err := execution.EnqueueRunInTx(ctx, q, pending, execution.RunTrigger{
			CompanyID:            item.CompanyID,
			ConversationID:       item.ConversationID,
			AgentID:              *resolved.AgentID,
			TaskID:               util.NullUUIDFromPtr(resolved.TaskID),
			TriggerPrompt:        item.Content,
			SkillRequestKey:      item.RequestKey,
			TriggerAttachmentIDs: triggerAttachmentIDs,
			Priority:             taskPriority,
			Source:               execution.RunSourcePromotion,
			ActorUserID:          item.InitiatedByUserID,
			AuthorizedByUserID:   item.InitiatedByUserID,
		})
		if err != nil {
			return QueuePromotion{}, fmt.Errorf("enqueue promoted Conversation Run: %w", err)
		}
		// The colleagues' saved Messages ride ahead of the promoted prompt,
		// exactly as the kernel froze them into the Run's snapshot.
		if _, err := MaterializeQueuedTaskMessagesInTx(ctx, q, pending, run); err != nil {
			return QueuePromotion{}, err
		}
		message, err := insertReservedUserMessage(
			ctx,
			q,
			run.ID,
			initiatorID,
			item.MessageID,
			item.RequestKey,
			item.Content,
			conversationID,
			companyID,
		)
		if err != nil {
			return QueuePromotion{}, err
		}
		_, err = q.MaterializeRunQueueAttachments(
			ctx,
			generated.MaterializeRunQueueAttachmentsParams{
				MessageID:   message.ID,
				QueueItemID: item.ID,
			},
		)
		if err != nil {
			return QueuePromotion{}, fmt.Errorf("materialize Conversation Run queue Attachments: %w", err)
		}
		if err := q.ReleaseRunQueueItemAttachments(ctx, item.ID); err != nil {
			return QueuePromotion{}, fmt.Errorf(
				"release promoted Conversation Run queue Attachments: %w",
				err,
			)
		}
		messageView, err := messageViewWithAttachments(
			ctx,
			q,
			initiatorID,
			companyID,
			message,
		)
		if err != nil {
			return QueuePromotion{}, err
		}
		_, err = q.MaterializeRunQueueItem(ctx, generated.MaterializeRunQueueItemParams{
			QueueItemID:     item.ID,
			ExpectedVersion: item.Version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return QueuePromotion{}, ErrRunQueueConflict
		}
		if err != nil {
			return QueuePromotion{}, fmt.Errorf("materialize Conversation Run queue item: %w", err)
		}
		// A handoff that made a colleague the assignee turned their saved
		// Message into their own prompt; a nudge still open about it closes
		// here.
		if err := closeQueuedMessageNudgeInTx(
			ctx, q, pending, item.CompanyID, item.MessageID, inbox.Actor{Kind: "system"},
		); err != nil {
			return QueuePromotion{}, err
		}
		if err := stageInTx(ctx, q, pending, NewMemberMessageCreatedEvent(
			initiatorID, item.CompanyID, routing.Kind, routing.CreatedByUserID, messageView,
		)); err != nil {
			return QueuePromotion{}, err
		}
		promotedRunID := run.ID
		result.RunID = &promotedRunID
		return result, nil
	}
	return QueuePromotion{}, errors.New("Conversation Run queue removal loop exceeded its limit")
}

func resolveRunQueuePromotionTarget(
	ctx context.Context,
	q *generated.Queries,
	taskMessageRuns TaskMessageRunPreparer,
	item generated.ConversationRunQueueItem,
) (resolvedConversationTarget, *generated.Task, error) {
	resolved, err := resolveMessageTarget(ctx, q, item.InitiatedByUserID, MessageTarget{
		Kind: TargetConversation,
		ID:   item.ConversationID,
	})
	if err != nil {
		return resolvedConversationTarget{}, nil, err
	}
	if resolved.CompanyID != item.CompanyID || resolved.AgentID == nil {
		return resolvedConversationTarget{}, nil, ErrConflict
	}
	if resolved.DocumentThreadID != nil {
		if resolved.ProjectID != nil {
			for _, permission := range []auth.ProjectPermission{auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite, auth.ProjectPermissionTasksExecute} {
				allowed, err := auth.HoldsProjectPermission(
					ctx,
					q,
					item.CompanyID,
					*resolved.ProjectID,
					item.InitiatedByUserID,
					permission,
				)
				if err != nil {
					return resolvedConversationTarget{}, nil, err
				}
				if !allowed {
					return resolvedConversationTarget{}, nil, ErrConflict
				}
			}
		}
		document, err := q.GetDocumentAgentContext(
			ctx,
			generated.GetDocumentAgentContextParams{
				CompanyID:      item.CompanyID,
				ConversationID: item.ConversationID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return resolvedConversationTarget{}, nil, ErrConflict
		}
		if err != nil {
			return resolvedConversationTarget{}, nil, err
		}
		access, err := auth.LockArtifactAccess(
			ctx,
			q,
			auth.UserPrincipal{CompanyID: item.CompanyID, UserID: item.InitiatedByUserID},
			document.PageID,
		)
		if err != nil {
			return resolvedConversationTarget{}, nil, err
		}
		if !access.Edit {
			return resolvedConversationTarget{}, nil, ErrConflict
		}

	}
	var taskRow *generated.Task
	if resolved.TaskID != nil {
		row, err := q.GetTaskForCompanyByIDForUpdate(ctx, generated.GetTaskForCompanyByIDForUpdateParams{
			TaskID:    *resolved.TaskID,
			CompanyID: resolved.CompanyID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return resolvedConversationTarget{}, nil, ErrNotFound
		}
		if err != nil {
			return resolvedConversationTarget{}, nil,
				fmt.Errorf("lock promoted Run queue Task: %w", err)
		}
		taskRow = &row
		resolved, err = resolveMessageTarget(
			ctx,
			q,
			item.InitiatedByUserID,
			MessageTarget{Kind: TargetTask, ID: row.ID},
		)
		if err != nil {
			return resolvedConversationTarget{}, nil, err
		}
		assignee := row.AssigneeUserID
		resolved.AssigneeUserID = &assignee
		if resolved.AgentID == nil {
			return resolvedConversationTarget{}, nil, ErrConflict
		}
		// The head was chosen among the assignee's items; a handoff since
		// then means this prompt would start a Run on the wrong machine.
		if item.InitiatedByUserID != row.AssigneeUserID {
			return resolvedConversationTarget{}, nil, ErrConflict
		}
		allowed, err := taskMessageRuns.CheckTaskMessageRunInTx(ctx, q, row)
		// An assignee who can no longer run Tasks in the Task's Project
		// (ADR 0017) cannot have the saved prompt started for them: the head
		// is a stale target like any other and is removed.
		if errors.Is(err, auth.ErrProjectPermissionRequired) {
			return resolvedConversationTarget{}, nil, ErrConflict
		}
		if err != nil {
			return resolvedConversationTarget{}, nil,
				fmt.Errorf("check promoted Task Message Run: %w", err)
		}
		if !allowed {
			return resolvedConversationTarget{}, nil, ErrConflict
		}
	}
	if _, err := q.GetActiveAgentForRunQueuePromotion(
		ctx,
		generated.GetActiveAgentForRunQueuePromotionParams{
			AgentID:   *resolved.AgentID,
			CompanyID: resolved.CompanyID,
		},
	); errors.Is(err, pgx.ErrNoRows) {
		return resolvedConversationTarget{}, nil, ErrConflict
	} else if err != nil {
		return resolvedConversationTarget{}, nil,
			fmt.Errorf("resolve promoted Run queue Agent: %w", err)
	}
	return resolved, taskRow, nil
}

// runQueuePromoter names whose items promotion may materialize: the Task's
// assignee for a Task Conversation, anyone (NULL) for a direct one. The
// caller holds the Conversation lock and a terminal Run transition; the Task
// row is locked again per item by resolveRunQueuePromotionTarget, which also
// rechecks the answer against the head it locked.
func runQueuePromoter(
	ctx context.Context,
	q *generated.Queries,
	companyID, conversationID uuid.UUID,
) (uuid.NullUUID, error) {
	task, err := q.GetTaskByConversation(ctx, generated.GetTaskByConversationParams{
		ConversationID: conversationID, CompanyID: companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.NullUUID{}, nil
	}
	if err != nil {
		return uuid.NullUUID{}, fmt.Errorf("resolve Run queue promoter: %w", err)
	}
	return uuid.NullUUID{UUID: task.AssigneeUserID, Valid: true}, nil
}

func systemRemoveRunQueueItem(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	item generated.ConversationRunQueueItem,
	reason string,
) error {
	if _, err := q.RemoveRunQueueItem(ctx, generated.RemoveRunQueueItemParams{
		RemovalReason:   reason,
		QueueItemID:     item.ID,
		ConversationID:  item.ConversationID,
		CompanyID:       item.CompanyID,
		ExpectedVersion: item.Version,
	}); err != nil {
		return fmt.Errorf("system-remove Conversation Run queue item: %w", err)
	}
	if err := q.ReleaseRunQueueItemAttachments(ctx, item.ID); err != nil {
		return fmt.Errorf("release system-removed Conversation Run queue Attachments: %w", err)
	}
	return closeQueuedMessageNudgeInTx(
		ctx, q, pending, item.CompanyID, item.MessageID, inbox.Actor{Kind: "system"},
	)
}

func (s *DefaultConversationService) FetchRunQueue(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (RunQueueProjection, error) {
	var projection RunQueueProjection
	err := s.store.WithinTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	}, func(q *generated.Queries) error {
		target, err := resolveMessageTarget(ctx, q, principal.UserID, MessageTarget{
			Kind: TargetConversation,
			ID:   conversationID,
		})
		if err != nil {
			return fmt.Errorf("resolve Conversation Run queue: %w", err)
		}
		projection, err = runQueueProjection(ctx, q, principal.UserID, target)
		return err
	})
	if err != nil {
		return RunQueueProjection{}, fmt.Errorf("get Conversation Run queue: %w", err)
	}
	return projection, nil
}

func (s *DefaultConversationService) ReorderRunQueue(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	input RunQueueOrderInput,
) (RunQueueProjection, error) {
	if !util.ValidUUID(conversationID) {
		return RunQueueProjection{}, ErrNotFound
	}
	normalized, err := normalizeRunQueueOrderInput(input)
	if err != nil {
		return RunQueueProjection{}, err
	}
	var projection RunQueueProjection
	var pending []events.Event
	err = s.store.WithinTx(ctx, conversationWriteTxOptions, func(tx *generated.Queries) error {
		resolved, err := lockRunQueueConversation(ctx, tx, principal, conversationID)
		if err != nil {
			return err
		}
		rows, err := tx.LockQueuedRunQueueItems(
			ctx,
			generated.LockQueuedRunQueueItemsParams{
				ConversationID: resolved.ConversationID,
				CompanyID:      resolved.CompanyID,
			},
		)
		if err != nil {
			return fmt.Errorf("lock Conversation Run queue order: %w", err)
		}
		current := make(map[uuid.UUID]int64, len(rows))
		for _, row := range rows {
			current[row.ID] = row.Version
		}
		// The order must be a permutation of the queue as it stands. An id
		// the queue never held, or a list that drops a queued item, is bad
		// input; an id that was queued but no longer is means the queue
		// changed under the write.
		var missing []uuid.UUID
		for _, item := range normalized.Items {
			if _, queued := current[item.ID]; !queued {
				missing = append(missing, item.ID)
			}
		}
		if len(missing) > 0 {
			held, err := runQueueItemsHeld(ctx, tx, resolved, missing)
			if err != nil {
				return err
			}
			if !held {
				return ErrInvalidInput
			}
			return ErrRunQueueConflict
		}
		if len(normalized.Items) < len(rows) {
			return ErrInvalidInput
		}
		for index, item := range normalized.Items {
			if current[item.ID] != item.ExpectedRevision {
				return ErrRunQueueConflict
			}
			_, err := tx.SetRunQueueItemPosition(
				ctx,
				generated.SetRunQueueItemPositionParams{
					Position:        int32(index + 1),
					QueueItemID:     item.ID,
					ConversationID:  resolved.ConversationID,
					CompanyID:       resolved.CompanyID,
					ExpectedVersion: item.ExpectedRevision,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRunQueueConflict
			}
			if err != nil {
				return fmt.Errorf("set Conversation Run queue item position: %w", err)
			}
		}
		if err := stageRunQueueConversationUpdate(ctx, tx, &pending, principal, resolved); err != nil {
			return err
		}
		projection, err = runQueueProjection(ctx, tx, principal.UserID, resolved)
		return err
	})
	if err != nil {
		return RunQueueProjection{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return projection, nil
}

func (s *DefaultConversationService) DeleteRunQueueItem(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	queueItemID uuid.UUID,
	input DeleteRunQueueItemInput,
) (RunQueueProjection, error) {
	if !util.ValidUUID(conversationID) || !util.ValidUUID(queueItemID) {
		return RunQueueProjection{}, ErrNotFound
	}
	var projection RunQueueProjection
	var pending []events.Event
	err := s.store.WithinTx(ctx, conversationWriteTxOptions, func(tx *generated.Queries) error {
		resolved, err := lockRunQueueConversation(ctx, tx, principal, conversationID)
		if err != nil {
			return err
		}
		removed, err := tx.RemoveRunQueueItem(ctx, generated.RemoveRunQueueItemParams{
			RemovalReason:   runQueueRemovalReasonUser,
			QueueItemID:     queueItemID,
			ConversationID:  resolved.ConversationID,
			CompanyID:       resolved.CompanyID,
			ExpectedVersion: input.ExpectedRevision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// No queued row matched: either the Conversation never held this
			// item (not found) or it did and the item moved on or was
			// revised since the caller read it (conflict).
			held, err := runQueueItemsHeld(ctx, tx, resolved, []uuid.UUID{queueItemID})
			if err != nil {
				return err
			}
			if !held {
				return ErrNotFound
			}
			return ErrRunQueueConflict
		}
		if err != nil {
			return fmt.Errorf("delete Conversation Run queue item: %w", err)
		}
		if err := tx.ReleaseRunQueueItemAttachments(ctx, queueItemID); err != nil {
			return fmt.Errorf("release Conversation Run queue Attachments: %w", err)
		}
		// The deleted Message will never exist, so the nudge about it closes.
		actorID := principal.UserID
		if err := closeQueuedMessageNudgeInTx(
			ctx, tx, &pending, resolved.CompanyID, removed.MessageID,
			inbox.Actor{Kind: "user", ID: &actorID},
		); err != nil {
			return err
		}
		if err := stageRunQueueConversationUpdate(ctx, tx, &pending, principal, resolved); err != nil {
			return err
		}
		projection, err = runQueueProjection(ctx, tx, principal.UserID, resolved)
		return err
	})
	if err != nil {
		return RunQueueProjection{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return projection, nil
}

// runQueueItemsHeld reports whether every id names an item this Conversation
// holds or once held. A removed or materialized item keeps its row, so an id
// with no row was never in this queue; ids must be distinct.
func runQueueItemsHeld(
	ctx context.Context,
	q *generated.Queries,
	resolved resolvedConversationTarget,
	ids []uuid.UUID,
) (bool, error) {
	known, err := q.ListRunQueueItemIDs(ctx, generated.ListRunQueueItemIDsParams{
		ConversationID: resolved.ConversationID,
		CompanyID:      resolved.CompanyID,
		Ids:            ids,
	})
	if err != nil {
		return false, fmt.Errorf("read Conversation Run queue items: %w", err)
	}
	return len(known) == len(ids), nil
}

func lockRunQueueConversation(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (resolvedConversationTarget, error) {
	resolved, err := resolveMessageTarget(ctx, q, principal.UserID, MessageTarget{
		Kind: TargetConversation,
		ID:   conversationID,
	})
	if err != nil {
		return resolvedConversationTarget{}, err
	}
	if err := lockActiveMembership(ctx, q, resolved.CompanyID, principal.UserID); err != nil {
		return resolvedConversationTarget{}, err
	}
	if err := lockConversation(
		ctx, q, principal.UserID, resolved.ConversationID, resolved.CompanyID,
	); err != nil {
		return resolvedConversationTarget{}, err
	}
	// Reordering or deleting queued work in a Task chat is posting there: a
	// reader without project.tasks.write sees the queue but may not change
	// it (ADR 0017).
	if err := requireTaskChatWrite(ctx, q, principal, resolved); err != nil {
		return resolvedConversationTarget{}, err
	}
	return resolved, nil
}

func stageRunQueueConversationUpdate(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	resolved resolvedConversationTarget,
) error {
	bumped, err := bumpConversationProjection(
		ctx,
		q,
		resolved.ConversationID,
		resolved.CompanyID,
		pending,
		events.UserActor(principal.UserID.String()),
	)
	if err != nil {
		return err
	}
	summary, err := conversationListSummary(
		ctx, q, principal.UserID, resolved.ConversationID, resolved.CompanyID,
	)
	if err != nil {
		return err
	}
	return stageInTx(ctx, q, pending, newConversationEvent(
		principal.UserID,
		resolved.TaskID,
		bumped,
		EventConversationUpdated,
		&summary,
	))
}

func runQueueProjection(
	ctx context.Context,
	q *generated.Queries,
	viewer uuid.UUID,
	resolved resolvedConversationTarget,
) (RunQueueProjection, error) {
	projection := RunQueueProjection{Items: []RunQueueItem{}, Limit: runQueueLimit}
	current, err := q.GetConversationCurrentRun(ctx, generated.GetConversationCurrentRunParams{
		ConversationID: resolved.ConversationID,
		CompanyID:      resolved.CompanyID,
	})
	if err == nil {
		projection.CurrentRun = &CurrentRun{
			RuntimeID:  execution.ViewerRuntimeID(current.RuntimeID, current.RuntimeOwnerUserID, viewer),
			WaitReason: current.WaitReason.String,
			ID:         current.ID,
			Status:     RunStatus(current.Status),
			QueuedAt:   current.CreatedAt.Time.UTC(),
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return RunQueueProjection{}, fmt.Errorf("get current Conversation Run: %w", err)
	}
	rows, err := q.ListQueuedRunQueueItems(ctx, generated.ListQueuedRunQueueItemsParams{
		ConversationID: resolved.ConversationID,
		CompanyID:      resolved.CompanyID,
	})
	if err != nil {
		return RunQueueProjection{}, fmt.Errorf("list Conversation Run queue items: %w", err)
	}
	if len(rows) == 0 {
		return projection, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for index, row := range rows {
		ids[index] = row.ID
	}
	attachmentRows, err := q.ListRunQueueItemAttachments(ctx, generated.ListRunQueueItemAttachmentsParams{
		QueueItemIds: ids, ViewerUserID: viewer,
	})
	if err != nil {
		return RunQueueProjection{}, fmt.Errorf("list Conversation Run queue Attachments: %w", err)
	}
	attachmentsByItem := make(map[uuid.UUID][]attachment.Response, len(rows))
	for _, row := range attachmentRows {
		value := runQueueAttachmentFromRow(row)
		queueItemID := row.QueueItemID
		attachmentsByItem[queueItemID] = append(attachmentsByItem[queueItemID], value)
	}
	initiators, err := runQueueInitiators(ctx, q, ids)
	if err != nil {
		return RunQueueProjection{}, err
	}
	projection.Items = make([]RunQueueItem, len(rows))
	for index, row := range rows {
		attachments := attachmentsByItem[row.ID]
		if attachments == nil {
			attachments = []attachment.Response{}
		}
		projection.Items[index] = RunQueueItem{
			ID:          row.ID,
			MessageID:   row.MessageID,
			Content:     row.Content,
			Attachments: attachments,
			InitiatedBy: initiators[row.ID],
			QueuedAt:    row.CreatedAt.Time.UTC(),
			Revision:    row.Version,
		}
	}
	return projection, nil
}

// runQueueInitiators names who wrote each queued prompt, keyed by queue
// item id.
func runQueueInitiators(
	ctx context.Context,
	q *generated.Queries,
	queueItemIDs []uuid.UUID,
) (map[uuid.UUID]ActorRef, error) {
	rows, err := q.ListRunQueueItemInitiators(ctx, queueItemIDs)
	if err != nil {
		return nil, fmt.Errorf("list Conversation Run queue item authors: %w", err)
	}
	initiators := make(map[uuid.UUID]ActorRef, len(rows))
	for _, row := range rows {
		initiators[row.QueueItemID] = actorRef(
			string(ActorUser), row.InitiatedByUserID, row.DisplayName, row.AvatarUrl,
		)
	}
	return initiators, nil
}

func loadRunQueueSendReplay(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	input SendInput,
	resolved resolvedConversationTarget,
) (generated.ConversationRunQueueItem, bool, error) {
	replay, err := q.GetRunQueueItemReplayForUpdate(
		ctx,
		generated.GetRunQueueItemReplayForUpdateParams{
			RequestKey: input.RequestKey, ConversationID: resolved.ConversationID,
			CompanyID: resolved.CompanyID, ActorUserID: principal.UserID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.ConversationRunQueueItem{}, false, nil
	}
	if err != nil {
		return generated.ConversationRunQueueItem{}, false,
			fmt.Errorf("look up Conversation Run queue replay: %w", err)
	}
	attachmentIDs, err := q.ListRunQueueAttachmentIDs(ctx, replay.ID)
	if err != nil {
		return generated.ConversationRunQueueItem{}, true,
			fmt.Errorf("list Conversation Run queue replay Attachments: %w", err)
	}
	if !messageContentMatches(input, replay.Content) || !sameUUIDSlice(attachmentIDs, input.AttachmentIDs) {
		return generated.ConversationRunQueueItem{}, true, ErrRunQueueConflict
	}
	return replay, true, nil
}

func finishRunQueueSendReplay(
	ctx context.Context,
	q *generated.Queries,
	viewer uuid.UUID,
	target MessageTarget,
	resolved resolvedConversationTarget,
	replay generated.ConversationRunQueueItem,
) (SendResult, error) {
	// No RunID on either disposition: a deferred prompt has no Run until it is
	// promoted, and a removed one never gets there. The queue item id used to
	// double as the reserved Run id; runs.id is a database-generated UUID now, so reporting
	// it as a Run id would name a row that does not exist.
	switch replay.State {
	case "queued":
		item, err := runQueueItemView(ctx, q, viewer, replay)
		if err != nil {
			return SendResult{}, err
		}
		return SendResult{
			CompanyID: resolved.CompanyID, ConversationKind: Kind(resolved.ConversationKind),
			Target: target, Disposition: SendDispositionQueued, QueueItem: &item,
		}, nil
	case "removed":
		return SendResult{
			CompanyID: resolved.CompanyID, ConversationKind: Kind(resolved.ConversationKind),
			Target: target, Disposition: SendDispositionRemoved,
		}, nil
	default:
		return SendResult{}, ErrRunQueueConflict
	}
}

// queueMessageSendIfRunning defers a prompt behind the Conversation's current
// Run: handled is false, with nothing written, when no Run is live.
func queueMessageSendIfRunning(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	target MessageTarget,
	input SendInput,
	resolved resolvedConversationTarget,
) (SendResult, bool, error) {
	if resolved.AgentID == nil {
		return SendResult{}, false, nil
	}
	if _, err := q.GetConversationCurrentRun(
		ctx,
		generated.GetConversationCurrentRunParams{
			ConversationID: resolved.ConversationID,
			CompanyID:      resolved.CompanyID,
		},
	); errors.Is(err, pgx.ErrNoRows) {
		return SendResult{}, false, nil
	} else if err != nil {
		return SendResult{}, true, fmt.Errorf("lock current Conversation Run: %w", err)
	}
	result, _, err := queueMessageSend(ctx, q, pending, principal, target, input, resolved)
	if err != nil {
		return SendResult{}, true, err
	}
	return result, true, nil
}

// queueMessageSend writes the Message as a Run queue item — the one shape a
// deferred prompt and a colleague's saved Task Message share — reserves its
// Attachments, bumps the Conversation, and answers the queued disposition.
func queueMessageSend(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	target MessageTarget,
	input SendInput,
	resolved resolvedConversationTarget,
) (SendResult, generated.ConversationRunQueueItem, error) {
	count, err := q.CountQueuedRunQueueItems(ctx, generated.CountQueuedRunQueueItemsParams{
		ConversationID: resolved.ConversationID,
		CompanyID:      resolved.CompanyID,
	})
	if err != nil {
		return SendResult{}, generated.ConversationRunQueueItem{},
			fmt.Errorf("count Conversation Run queue: %w", err)
	}
	if count >= runQueueLimit {
		return SendResult{}, generated.ConversationRunQueueItem{}, ErrRunQueueFull
	}
	row, err := q.InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID:      resolved.CompanyID,
		ConversationID: resolved.ConversationID,
		ActorUserID:    principal.UserID,
		RequestKey:     input.RequestKey,
		Content:        input.Content,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SendResult{}, generated.ConversationRunQueueItem{}, ErrRunQueueConflict
		}
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23505" {
			return SendResult{}, generated.ConversationRunQueueItem{}, ErrRunQueueConflict
		}
		return SendResult{}, generated.ConversationRunQueueItem{},
			mapConversationMutationError("insert Conversation Run queue item", err)
	}
	for index, attachmentID := range input.AttachmentIDs {
		_, err := q.ReserveRunQueueAttachment(ctx, generated.ReserveRunQueueAttachmentParams{
			QueueItemID:  row.ID,
			Position:     int32(index),
			AttachmentID: attachmentID,
			CompanyID:    resolved.CompanyID,
			ActorUserID:  principal.UserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return SendResult{}, generated.ConversationRunQueueItem{}, ErrRunQueueConflict
		}
		if err != nil {
			return SendResult{}, generated.ConversationRunQueueItem{},
				fmt.Errorf("reserve Conversation Run queue Attachment: %w", err)
		}
	}
	if err := stageRunQueueConversationUpdate(ctx, q, pending, principal, resolved); err != nil {
		return SendResult{}, generated.ConversationRunQueueItem{}, err
	}
	item, err := runQueueItemView(ctx, q, principal.UserID, row)
	if err != nil {
		return SendResult{}, generated.ConversationRunQueueItem{}, err
	}
	return SendResult{
		CompanyID:        resolved.CompanyID,
		ConversationKind: Kind(resolved.ConversationKind),
		Target:           target,
		Disposition:      SendDispositionQueued,
		QueueItem:        &item,
		Created:          true,
	}, row, nil
}

// runQueueItemView projects one queued prompt for the viewer, who wrote it or
// is replaying their own send; its files follow the queue list rule.
func runQueueItemView(
	ctx context.Context,
	q *generated.Queries,
	viewer uuid.UUID,
	row generated.ConversationRunQueueItem,
) (RunQueueItem, error) {
	attachmentRows, err := q.ListRunQueueItemAttachments(ctx, generated.ListRunQueueItemAttachmentsParams{
		QueueItemIds: []uuid.UUID{row.ID}, ViewerUserID: viewer,
	})
	if err != nil {
		return RunQueueItem{}, fmt.Errorf("list Conversation Run queue item Attachments: %w", err)
	}
	attachments := make([]attachment.Response, len(attachmentRows))
	for index, attachmentRow := range attachmentRows {
		attachments[index] = runQueueAttachmentFromRow(attachmentRow)
	}
	initiators, err := runQueueInitiators(ctx, q, []uuid.UUID{row.ID})
	if err != nil {
		return RunQueueItem{}, err
	}
	return RunQueueItem{
		ID:          row.ID,
		MessageID:   row.MessageID,
		Content:     row.Content,
		Attachments: attachments,
		InitiatedBy: initiators[row.ID],
		QueuedAt:    row.CreatedAt.Time.UTC(),
		Revision:    row.Version,
	}, nil
}

func sameUUIDSlice(left, right []uuid.UUID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func runQueueAttachmentFromRow(
	row generated.ListRunQueueItemAttachmentsRow,
) attachment.Response {
	return attachment.Response{
		ID:               row.ID,
		CompanyID:        row.CompanyID,
		UploadedByUserID: row.UploadedByUserID,
		Filename:         row.Filename,
		ContentType:      row.ContentType,
		SizeBytes:        row.SizeBytes,
		Status:           attachment.PublicStatusReady,
		CreatedAt:        row.CreatedAt.Time.UTC(),
		UpdatedAt:        row.UpdatedAt.Time.UTC(),
		Revision:         row.Version,
	}
}
