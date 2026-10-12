package conversation

// A Task's assignee is the only member who can start its Agent, and the Run
// executes on their machine (ADR 0014). conversation_items.run_id is NOT
// NULL, so a colleague's words cannot enter the transcript until a Run
// exists to carry them. This file is the two halves of that rule:
//
//   - queueTaskMessageForAssignee saves a non-assignee's Message as a Run
//     queue item and nudges the assignee (task_run_requested);
//   - MaterializeQueuedTaskMessagesInTx writes those items into the
//     transcript, in queue order, right after the assignee's Run row exists
//     and before its trigger, so the seqs match what the enqueue kernel
//     froze into the Run's conversationSnapshot.
//
// Promotion (PromoteRunQueueInTx) never promotes a colleague's item: each
// of those is context for the assignee's next Run, not a Run of its own.
//
// The nudge is about one waiting Message. Every path that moves an item out
// of 'queued' closes it (closeQueuedMessageNudgeInTx), and a handoff closes
// the nudges it leaves addressed to the previous assignee
// (CloseQueuedTaskMessageNudgesInTx).

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// queueTaskMessageForAssignee saves a colleague's Message for the Task's
// assignee. The caller holds the Conversation and Task locks and has already
// run the Task admission policy; the Run queue item is what a replay of the
// same request key finds, exactly as for a deferred prompt.
func queueTaskMessageForAssignee(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	taskMessageRuns TaskMessageRunPreparer,
	principal auth.UserPrincipal,
	target MessageTarget,
	input SendInput,
	resolved resolvedConversationTarget,
	task generated.Task,
) (SendResult, error) {
	result, row, err := queueMessageSend(ctx, q, pending, principal, target, input, resolved)
	if err != nil {
		return SendResult{}, err
	}
	if err := stageTaskRunRequestedInbox(ctx, q, pending, taskMessageRuns, principal, task, row); err != nil {
		return SendResult{}, err
	}
	return result, nil
}

// stageTaskRunRequestedInbox nudges the assignee once per saved Message. The
// item's entity is the reserved message the words will become, so
// MaterializeQueuedTaskMessagesInTx closes it the moment the assignee's Run
// carries them into the transcript. The next step it names is the Task
// domain's, so it never points at a Start the Task's status or the
// assignee's binding would refuse.
func stageTaskRunRequestedInbox(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	taskMessageRuns TaskMessageRunPreparer,
	principal auth.UserPrincipal,
	task generated.Task,
	item generated.ConversationRunQueueItem,
) error {
	sender, err := q.GetRunTriggerAuthor(ctx, generated.GetRunTriggerAuthorParams{
		CompanyID: task.CompanyID, UserID: principal.UserID,
	})
	if err != nil {
		return fmt.Errorf("read queued Task Message sender: %w", err)
	}
	body := fmt.Sprintf("%s wrote in “%s”.", sender.DisplayName, task.Title)
	step, err := taskMessageRuns.TaskRunNextStepInTx(ctx, q, task)
	if err != nil {
		return fmt.Errorf("read queued Task Message next step: %w", err)
	}
	if step != "" {
		body += " " + step
	}
	deepLink := taskDeepLink(task.ID)
	actorID := principal.UserID
	if _, err := inbox.CreateInTx(ctx, q, pending, inbox.CreateInput{
		CompanyID:       task.CompanyID,
		RecipientUserID: task.AssigneeUserID,
		ConversationID:  &task.ConversationID,
		Type:            inbox.TypeTaskRunRequested,
		Entity:          inbox.Entity{Type: protocol.EntityMessage, ID: item.MessageID.String()},
		Title:           "Message waiting in a task",
		Body:            body,
		DeepLink:        &deepLink,
		DedupeKey:       fmt.Sprintf("task:%s:message:%s:run_requested", task.ID, item.MessageID),
		Actor:           inbox.Actor{Kind: string(protocol.ActorKindUser), ID: &actorID},
	}); err != nil {
		return fmt.Errorf("create Task run-requested Inbox item: %w", err)
	}
	return nil
}

// closeQueuedMessageNudgeInTx closes the task_run_requested nudge about a
// queued item's Message. A deferred prompt has no nudge, so the close finds
// nothing to change.
func closeQueuedMessageNudgeInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, messageID uuid.UUID,
	actor inbox.Actor,
) error {
	if err := inbox.CloseEntityInTx(ctx, q, pending, companyID,
		inbox.Entity{Type: protocol.EntityMessage, ID: messageID.String()}, actor,
	); err != nil {
		return fmt.Errorf("close queued Task Message nudge: %w", err)
	}
	return nil
}

// CloseQueuedTaskMessageNudgesInTx closes the nudges about every Message
// still queued in a Task Conversation. The Task domain calls it when a
// handoff changes the assignee: each nudge was addressed to whoever was the
// assignee when the Message was saved, and that member can no longer start
// the Run that carries it. The caller holds the Conversation and Task locks.
func CloseQueuedTaskMessageNudgesInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, conversationID uuid.UUID,
) error {
	if q == nil || pending == nil {
		return ErrInvalidInput
	}
	rows, err := q.ListQueuedRunQueueItems(ctx, generated.ListQueuedRunQueueItemsParams{
		ConversationID: conversationID, CompanyID: companyID,
	})
	if err != nil {
		return fmt.Errorf("list queued Task Messages for handoff: %w", err)
	}
	for _, row := range rows {
		if err := closeQueuedMessageNudgeInTx(
			ctx, q, pending, companyID, row.MessageID, inbox.Actor{Kind: "system"},
		); err != nil {
			return err
		}
	}
	return nil
}

// taskDeepLink mirrors the Task domain's Inbox deep link: the app opens the
// Task by id.
func taskDeepLink(taskID uuid.UUID) string {
	return "/app?desktop_search=1&taskId=" + taskID.String()
}

// MaterializeQueuedTaskMessagesInTx writes the colleagues' queued Messages
// of a Task Conversation into the transcript as items of run, in queue
// order, and closes their task_run_requested nudges. It must be called in
// the enqueue transaction, after execution.EnqueueRunInTx returned the Run
// and before the caller inserts the trigger Message, under the Conversation
// lock the caller already holds: the kernel froze these same rows into the
// Run's conversationSnapshot at the seqs this allocation now hands them. A
// direct or Schedule Run has nothing to carry and returns 0.
func MaterializeQueuedTaskMessagesInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	run generated.Run,
) (int, error) {
	if q == nil || pending == nil || run.ID == uuid.Nil {
		return 0, ErrInvalidInput
	}
	// Every Run the kernel enqueues names its authorizer; a Task Run without
	// one cannot exist since ADR 0014 and carries nothing.
	if !run.TaskID.Valid || !run.ExecutionAuthorizedByUserID.Valid {
		return 0, nil
	}
	rows, err := q.ListQueuedRunQueueItemsForTaskRun(ctx, generated.ListQueuedRunQueueItemsForTaskRunParams{
		ConversationID: run.ConversationID, CompanyID: run.CompanyID,
		AuthorizedByUserID: run.ExecutionAuthorizedByUserID.UUID,
	})
	if err != nil {
		return 0, fmt.Errorf("list queued Task Messages to carry: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	routing, err := resolveConversationRouting(ctx, q, run.CompanyID, run.ConversationID)
	if err != nil {
		return 0, err
	}
	materialized := 0
	for _, row := range rows {
		item := row.ConversationRunQueueItem
		message, err := insertReservedUserMessage(
			ctx, q, run.ID, item.InitiatedByUserID, item.MessageID, item.RequestKey, item.Content,
			run.ConversationID, run.CompanyID,
		)
		if err != nil {
			return 0, err
		}
		if _, err := q.MaterializeRunQueueAttachments(ctx, generated.MaterializeRunQueueAttachmentsParams{
			MessageID: message.ID, QueueItemID: item.ID,
		}); err != nil {
			return 0, fmt.Errorf("materialize carried Task Message Attachments: %w", err)
		}
		if err := q.ReleaseRunQueueItemAttachments(ctx, item.ID); err != nil {
			return 0, fmt.Errorf("release carried Task Message Attachments: %w", err)
		}
		if _, err := q.MaterializeRunQueueItem(ctx, generated.MaterializeRunQueueItemParams{
			QueueItemID: item.ID, ExpectedVersion: item.Version,
		}); errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrRunQueueConflict
		} else if err != nil {
			return 0, fmt.Errorf("materialize carried Task Message: %w", err)
		}
		view, err := messageViewWithAttachments(ctx, q, item.InitiatedByUserID, run.CompanyID, message)
		if err != nil {
			return 0, err
		}
		if err := stageInTx(ctx, q, pending, NewMemberMessageCreatedEvent(
			item.InitiatedByUserID, run.CompanyID, routing.Kind, routing.CreatedByUserID, view,
		)); err != nil {
			return 0, err
		}
		if err := closeQueuedMessageNudgeInTx(
			ctx, q, pending, run.CompanyID, item.MessageID, inbox.Actor{Kind: "system"},
		); err != nil {
			return 0, err
		}
		materialized++
	}
	return materialized, nil
}
