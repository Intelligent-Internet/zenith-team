package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"

	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func (s *DefaultConversationService) ListMessages(
	ctx context.Context,
	principal auth.UserPrincipal,
	target MessageTarget,
	options MessageListOptions,
) (MessagePage, error) {
	if !validMessageTarget(target) {
		return MessagePage{}, ErrNotFound
	}
	normalized, err := normalizeMessageListOptions(options)
	if err != nil {
		return MessagePage{}, err
	}
	page, err := s.listMessages(ctx, principal, target, normalized)
	if err != nil {
		return MessagePage{}, fmt.Errorf("list Conversation Messages: %w", err)
	}
	if page.Items == nil {
		page.Items = []MessageView{}
	}
	for index := range page.Items {
		if page.Items[index].Attachments == nil {
			page.Items[index].Attachments = []attachment.Response{}
		}
	}
	return page, nil
}

func (s *DefaultConversationService) listMessages(
	ctx context.Context,
	principal auth.UserPrincipal,
	target MessageTarget,
	options MessageListOptions,
) (MessagePage, error) {
	q := s.store.Queries()
	resolved, err := resolveMessageTarget(ctx, q, principal.UserID, target)
	if err != nil {
		return MessagePage{}, err
	}
	watermark, err := conversationMessageWatermark(
		ctx,
		q,
		principal.UserID,
		resolved.ConversationID,
		resolved.CompanyID,
	)
	if err != nil {
		return MessagePage{}, fmt.Errorf("read Conversation Message watermark: %w", err)
	}
	// A cursor past the head is a caller that is ahead of this read — a
	// reconnect after its own send, a bound copied from a newer page — not
	// an invalid one, so it is clamped rather than refused. after_seq past
	// the head yields an empty page carrying the real head, which is how the
	// caller learns it is ahead; through_seq past the head reads from the
	// real head, exactly as the transcript read answers the same cursors.
	through := watermark
	if options.ThroughSeq != nil && *options.ThroughSeq < watermark {
		through = *options.ThroughSeq
	}
	var items []MessageView
	if options.FromNewest {
		items, err = newestMessageViews(
			ctx, q, principal.UserID, resolved.CompanyID, resolved.ConversationID,
			through, options.Limit,
		)
	} else {
		items, err = listMessageViews(
			ctx, q, principal.UserID, resolved.CompanyID, resolved.ConversationID,
			options.AfterSeq, through, options.Limit,
		)
	}
	if err != nil {
		return MessagePage{}, err
	}
	return MessagePage{
		CompanyID: resolved.CompanyID, ConversationID: resolved.ConversationID, Target: target,
		Items: items, EarliestAvailableSeq: 1, ThroughSeq: watermark,
	}, nil
}

func (s *DefaultConversationService) Send(
	ctx context.Context,
	principal auth.UserPrincipal,
	target MessageTarget,
	input SendInput,
) (SendResult, error) {
	if !validMessageTarget(target) {
		return SendResult{}, ErrNotFound
	}
	normalized, err := normalizeSendInput(input)
	if err != nil {
		return SendResult{}, err
	}
	result, queuedRun, err := s.sendMessage(ctx, principal, target, normalized)
	if err != nil {
		return SendResult{}, fmt.Errorf("send Conversation Message: %w", err)
	}
	if result.View.Attachments == nil {
		result.View.Attachments = []attachment.Response{}
	}
	// Post-commit: say the runtime may have claimable work, so the run is
	// claimed without waiting for the next poll. Whoever wakes daemons
	// subscribes to that; this domain no longer holds a handle to them.
	if queuedRun != nil {
		execution.PublishRuntimeWork(ctx, s.bus, *queuedRun)
	}
	return result, nil
}

// sendMessage owns visibility, sequence allocation, exact replay detection,
// ready Attachment linking, optional Run-queue capture, one Conversation
// summary bump, and wire-event writes in one commit. A READ COMMITTED
// PostgreSQL implementation must call LockConversationForActiveUserMutation
// and only then the applicable item insert as a separate statement in that
// transaction; combining the lock and insert can let a waiting statement
// retain a stale snapshot and allocate a duplicate seq.
//
// The Run row is written BEFORE the user's item, because runs.id is a
// database-generated UUID that needs nothing from the stream and the item is what carries
// run_id. That order puts the whole weight of enqueue idempotency on the
// item's request_key unique index, so the replay probe below must stay ahead
// of the enqueue: probing after it would create an orphan Run on every retry.
func (s *DefaultConversationService) sendMessage(
	ctx context.Context,
	principal auth.UserPrincipal,
	target MessageTarget,
	input SendInput,
) (SendResult, *generated.Run, error) {
	var result SendResult
	var queuedRun *generated.Run
	var queuedRunID *uuid.UUID
	var pending []events.Event
	err := s.store.WithinTx(ctx, conversationWriteTxOptions, func(tx *generated.Queries) error {
		pending = pending[:0]
		queuedRun = nil
		queuedRunID = nil
		resolved, err := resolveMessageTarget(ctx, tx, principal.UserID, target)
		if err != nil {
			return err
		}
		if resolved.DocumentThreadID != nil {
			return ErrNotFound
		}
		if (len(input.SkillReferences) > 0 || len(input.ConnectorReferences) > 0) &&
			resolved.ConversationKind != "direct" {
			return ErrInvalidInput
		}
		if err := lockActiveMembership(ctx, tx, resolved.CompanyID, principal.UserID); err != nil {
			return err
		}
		// A colleague's Task Message nudges the assignee (task_run_requested):
		// take the recipient's membership lock now, while Company and member
		// locks still precede the Conversation lock, so the Inbox write below
		// re-enters it instead of taking it out of order.
		if resolved.TaskID != nil && resolved.AssigneeUserID != nil &&
			*resolved.AssigneeUserID != principal.UserID {
			if _, err := inbox.LockRecipientInTx(
				ctx,
				tx,
				resolved.CompanyID,
				*resolved.AssigneeUserID,
			); err != nil {
				return fmt.Errorf("lock Task assignee for a queued Message: %w", err)
			}
		}
		// The lock still fences concurrent writers even though the enqueue no
		// longer reads the conversation row (the run's runtime comes from the
		// agent binding).
		err = lockConversation(
			ctx,
			tx,
			principal.UserID,
			resolved.ConversationID,
			resolved.CompanyID,
		)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return err
			}
			return fmt.Errorf("lock Conversation for Message send: %w", err)
		}
		if err := requireTaskChatWrite(ctx, tx, principal, resolved); err != nil {
			return err
		}
		taskPriority := int32(2) // direct-conversation runs queue at medium
		var taskRow *generated.Task
		if resolved.TaskID != nil {
			row, err := tx.GetTaskForCompanyByIDForUpdate(
				ctx,
				generated.GetTaskForCompanyByIDForUpdateParams{
					TaskID:    *resolved.TaskID,
					CompanyID: resolved.CompanyID,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("lock Message target Task: %w", err)
			}
			taskRow = &row
			resolved, err = resolveMessageTarget(ctx, tx, principal.UserID, target)
			if err != nil {
				return err
			}
			assignee := row.AssigneeUserID
			resolved.AssigneeUserID = &assignee
			taskPriority = execution.PriorityToInt(row.Priority)
		}
		message, messageReplayErr := messageSendReplay(
			ctx, tx, principal.UserID, input.RequestKey, resolved.ConversationID, resolved.CompanyID,
		)
		if messageReplayErr != nil && !errors.Is(messageReplayErr, ErrNotFound) {
			return messageReplayErr
		}
		queueReplay, queueReplayFound, err := loadRunQueueSendReplay(
			ctx, tx, principal, input, resolved,
		)
		if err != nil {
			return err
		}
		if messageReplayErr == nil {
			if queueReplayFound &&
				(queueReplay.State != "materialized" || queueReplay.MessageID != message.ID) {
				return ErrRunQueueConflict
			}
			var materializedQueue *generated.ConversationRunQueueItem
			if queueReplayFound {
				materializedQueue = &queueReplay
			}
			if err := matchMessageConnectors(
				ctx,
				tx,
				messageSkillScope{
					CompanyID:      resolved.CompanyID,
					ConversationID: resolved.ConversationID,
					UserID:         principal.UserID,
					RequestKey:     input.RequestKey,
				},
				input.ConnectorReferences,
			); err != nil {
				return err
			}
			if err := matchMessageSkills(
				ctx,
				tx,
				messageSkillScope{
					CompanyID:      resolved.CompanyID,
					ConversationID: resolved.ConversationID,
					UserID:         principal.UserID,
					RequestKey:     input.RequestKey,
				},
				input.SkillReferences,
			); err != nil {
				return err
			}
			return finishMessageReplay(
				ctx, tx, principal, target, input, resolved, message, materializedQueue, &result,
			)
		}
		if queueReplayFound {
			if err := matchMessageConnectors(
				ctx,
				tx,
				messageSkillScope{
					CompanyID:      resolved.CompanyID,
					ConversationID: resolved.ConversationID,
					UserID:         principal.UserID,
					RequestKey:     input.RequestKey,
				},
				input.ConnectorReferences,
			); err != nil {
				return err
			}
			if err := matchMessageSkills(
				ctx,
				tx,
				messageSkillScope{
					CompanyID:      resolved.CompanyID,
					ConversationID: resolved.ConversationID,
					UserID:         principal.UserID,
					RequestKey:     input.RequestKey,
				},
				input.SkillReferences,
			); err != nil {
				return err
			}
			if queueReplay.State == "materialized" {
				return ErrRunQueueConflict
			}
			result, err = finishRunQueueSendReplay(
				ctx, tx, principal.UserID, target, resolved, queueReplay,
			)
			return err
		}
		preparing, err := tx.IsConversationInitializationPending(
			ctx,
			generated.IsConversationInitializationPendingParams{
				CompanyID: resolved.CompanyID, ConversationID: resolved.ConversationID,
			},
		)
		if err != nil {
			return fmt.Errorf("check Conversation initialization: %w", err)
		}
		if preparing {
			return ErrInitializationPending
		}
		if err := saveMessageConnectors(ctx, tx, messageConnectorInput{
			Scope: messageSkillScope{
				CompanyID:      resolved.CompanyID,
				ConversationID: resolved.ConversationID,
				UserID:         principal.UserID,
				RequestKey:     input.RequestKey,
			},
			Content:    input.Content,
			References: input.ConnectorReferences,
			AgentIDs:   connectorTargetAgents(resolved.AgentID),
		}); err != nil {
			return err
		}
		if err := saveMessageSkills(
			ctx,
			tx,
			messageSkillScope{
				CompanyID:      resolved.CompanyID,
				ConversationID: resolved.ConversationID,
				UserID:         principal.UserID,
				RequestKey:     input.RequestKey,
			},
			input.Content,
			input.SkillReferences,
		); err != nil {
			return err
		}
		if len(input.ArtifactIDs) > 0 {
			if resolved.ConversationKind != "direct" {
				return ErrInvalidInput
			}
			content, err := artifactMessageContent(ctx, tx, principal.UserID, resolved.CompanyID, input)
			if err != nil {
				return err
			}
			input.Content = content
		}
		if taskRow != nil {
			allowed, err := s.taskMessageRuns.CheckTaskMessageRunInTx(ctx, tx, *taskRow)
			if err != nil {
				return fmt.Errorf("check Task Message Run: %w", err)
			}
			if !allowed {
				return ErrConflict
			}
			// Only the assignee starts the Agent, on their own machine
			// (ADR 0014). Anyone else's words are saved for them: a Run queue
			// item the assignee's next Run carries into the transcript, and a
			// nudge in the assignee's Inbox. A Task with no Agent takes the
			// no-Agent refusal below like any other send.
			if resolved.AgentID != nil && taskRow.AssigneeUserID != principal.UserID {
				result, err = queueTaskMessageForAssignee(
					ctx,
					tx,
					&pending,
					s.taskMessageRuns,
					principal,
					target,
					input,
					resolved,
					*taskRow,
				)
				return err
			}
		}
		queuedResult, handled, err := queueMessageSendIfRunning(
			ctx, tx, &pending, principal, target, input, resolved,
		)
		if err != nil {
			return err
		}
		if handled {
			result = queuedResult
			return nil
		}

		// A Conversation with no Agent cannot take a turn: conversation_items
		// .run_id is NOT NULL, so there is no such thing as words nobody was
		// asked to answer. A Task with no executing or assisting Agent, or a
		// direct chat whose Agent was deleted, refuses the send.
		if resolved.AgentID == nil {
			return ErrConflict
		}
		if taskRow != nil {
			allowed, err := s.taskMessageRuns.PrepareTaskMessageRunInTx(
				ctx,
				tx,
				&pending,
				principal.UserID,
				*taskRow,
			)
			if err != nil {
				return fmt.Errorf("prepare Task Message Run: %w", err)
			}
			if !allowed {
				return ErrConflict
			}
		}
		// The enqueue kernel inserts the queued Run and stages its run.queued
		// event inside this same transaction, so the item and its Run become
		// visible atomically. TriggerPrompt is the words about to be
		// committed: the item does not exist yet, so there is nothing to read
		// them back from. TriggerAttachmentIDs likewise name the files it is
		// about to carry, which a Task Run reads. A suppressed send carries
		// none: it writes no message, so its Attachments are never linked,
		// and linking is where they are checked.
		triggerAttachmentIDs := input.AttachmentIDs
		if input.SuppressUserMessage {
			triggerAttachmentIDs = nil
		}
		run, err := execution.EnqueueRunInTx(ctx, tx, &pending, execution.RunTrigger{
			CompanyID:            resolved.CompanyID,
			ConversationID:       resolved.ConversationID,
			AgentID:              *resolved.AgentID,
			TaskID:               util.NullUUIDFromPtr(resolved.TaskID),
			TriggerPrompt:        input.Content,
			SkillRequestKey:      input.RequestKey,
			TriggerAttachmentIDs: triggerAttachmentIDs,
			Priority:             taskPriority,
			Source:               execution.RunSourceMessage,
			ActorUserID:          principal.UserID,
			AuthorizedByUserID:   principal.UserID,
		})
		if err != nil {
			if errors.Is(err, execution.ErrRunAgentArchived) ||
				errors.Is(err, execution.ErrDuplicatePendingRun) ||
				errors.Is(err, execution.ErrTaskMergeInProgress) {
				return ErrConflict
			}
			// Not a conflict: the sender has not set this Agent up on a
			// runtime of theirs. The kernel refused before writing, and the
			// handler names the missing binding rather than a stale state.
			if errors.Is(err, execution.ErrRunAgentNotBound) {
				return err
			}
			return fmt.Errorf("enqueue Conversation Run: %w", err)
		}
		queuedRun = &run
		queuedRunID = &run.ID
		// The colleagues' saved Messages ride ahead of this one, exactly as
		// the kernel froze them into the Run's snapshot.
		if _, err := MaterializeQueuedTaskMessagesInTx(ctx, tx, &pending, run); err != nil {
			return err
		}

		beforeHistory, err := activity.ObserveInTx(ctx, tx, Topic, resolved.ConversationID)
		if err != nil {
			return err
		}
		if input.SuppressUserMessage {
			// The Run is enqueued and carries the prompt in its frozen context;
			// only the transcript entry is withheld. Attachments are ignored
			// here because there is no message for them to hang from — a
			// suppressed prompt is an instruction, not something a person sent.
			bumped, err := bumpConversationProjection(
				ctx, tx, resolved.ConversationID, resolved.CompanyID, &pending,
				events.UserActor(principal.UserID.String()), beforeHistory,
			)
			if err != nil {
				return err
			}
			summary, err := conversationListSummary(
				ctx, tx, principal.UserID, resolved.ConversationID, resolved.CompanyID,
			)
			if err != nil {
				return err
			}
			if err := stageInTx(ctx, tx, &pending, newConversationEvent(
				principal.UserID, resolved.TaskID, bumped,
				EventConversationUpdated, &summary,
			)); err != nil {
				return err
			}
			result = SendResult{
				CompanyID:        resolved.CompanyID,
				ConversationKind: Kind(resolved.ConversationKind),
				Target:           target,
				Disposition:      SendDispositionMessage,
				Suppressed:       true,
				RunID:            queuedRunID,
				Created:          true,
			}
			return nil
		}
		message, err = insertRequestedUserMessage(
			ctx,
			tx,
			run.ID,
			principal.UserID,
			input.RequestKey,
			input.Content,
			resolved.ConversationID,
			resolved.CompanyID,
			resolved.ProjectID,
		)
		if err != nil {
			return err
		}
		for index, attachmentID := range input.AttachmentIDs {
			if err := linkMessageAttachment(
				ctx, tx, int32(index), message.ID, attachmentID, principal.UserID, resolved.CompanyID,
			); err != nil {
				return err
			}
		}
		bumped, err := bumpConversationProjection(
			ctx,
			tx,
			resolved.ConversationID,
			resolved.CompanyID,
			&pending,
			events.UserActor(principal.UserID.String()),
			beforeHistory,
		)
		if err != nil {
			return err
		}
		summary, err := conversationListSummary(
			ctx,
			tx,
			principal.UserID,
			resolved.ConversationID,
			resolved.CompanyID,
		)
		if err != nil {
			return err
		}
		view, err := messageViewWithAttachments(ctx, tx, principal.UserID, resolved.CompanyID, message)
		if err != nil {
			return err
		}
		if err := stageInTx(
			ctx, tx, &pending,
			newMessageCreatedEvent(principal.UserID, bumped, view),
			newConversationEvent(
				principal.UserID,
				resolved.TaskID,
				bumped,
				EventConversationUpdated,
				&summary,
			),
		); err != nil {
			return err
		}
		result = SendResult{
			CompanyID:        resolved.CompanyID,
			ConversationKind: Kind(resolved.ConversationKind),
			Target:           target,
			Disposition:      SendDispositionMessage,
			View:             view,
			RunID:            queuedRunID,
			Created:          true,
		}
		return nil
	})
	if err != nil {
		return SendResult{}, nil, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, queuedRun, nil
}

// requireTaskChatWrite refuses a post into a Task chat from a reader who does
// not hold project.tasks.write on its Project (ADR 0017): reading the chat
// is project.read, writing into it is Task work. A colleague's message still
// queues for the assignee when they may write (ADR 0014). The caller holds
// the Conversation lock, which the Project lock must precede, so the check
// reads through project_permission_held without it; the insert statement
// checks the same permission again.
func requireTaskChatWrite(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	resolved resolvedConversationTarget,
) error {
	if Kind(resolved.ConversationKind) != KindTask || resolved.ProjectID == nil {
		return nil
	}
	return auth.RequireProjectPermissionHeld(
		ctx, q, resolved.CompanyID, *resolved.ProjectID, principal.UserID, auth.ProjectPermissionTasksWrite,
	)
}

func finishMessageReplay(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	target MessageTarget,
	input SendInput,
	resolved resolvedConversationTarget,
	message MessageResponse,
	materializedQueue *generated.ConversationRunQueueItem,
	result *SendResult,
) error {
	if !messageContentMatches(input, message.Content) {
		return ErrConflict
	}
	view, err := messageViewWithAttachments(ctx, q, principal.UserID, resolved.CompanyID, message)
	if err != nil {
		return err
	}
	if len(view.Attachments) != len(input.AttachmentIDs) {
		return ErrConflict
	}
	for index, attachmentID := range input.AttachmentIDs {
		if view.Attachments[index].ID != attachmentID {
			return ErrConflict
		}
	}
	var runID *uuid.UUID
	run, err := q.GetMessageTriggeredRunForReplay(
		ctx,
		generated.GetMessageTriggeredRunForReplayParams{
			MessageID: message.ID, ConversationID: resolved.ConversationID,
			CompanyID: resolved.CompanyID,
		},
	)
	if err == nil {
		value := run.ID
		runID = &value
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load Message send replay Run: %w", err)
	}
	// A materialized queue item that produced no Run is a torn promotion: the
	// item and its Run are written by one transaction, so the item existing
	// without one cannot be replayed as success. The old identity comparison
	// this replaced is gone with the reserved run id — the queue item and the
	// Run no longer share an identity, and the item's own run_id is the edge
	// GetMessageTriggeredRunForReplay already followed to get here.
	if materializedQueue != nil && runID == nil {
		return ErrRunQueueConflict
	}
	*result = SendResult{
		CompanyID: resolved.CompanyID, ConversationKind: Kind(resolved.ConversationKind), Target: target,
		Disposition: SendDispositionMessage, View: view, RunID: runID,
	}
	return nil
}
