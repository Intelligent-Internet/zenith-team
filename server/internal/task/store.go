package task

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// This file holds the Task domain's sqlc calls. Every function takes the query
// set as a parameter rather than holding a pool, so the same function serves a
// plain read off Store.Queries() and a step inside someone else's transaction —
// the caller decides which by the handle it passes.

// bumpedConversation pairs the bumped Conversation projection row with the list
// summary computed inside the same transaction. The deferred
// conversation.updated event needs both, and they only agree when they come out
// of one call.
type bumpedConversation struct {
	row     generated.Conversation
	summary taskConversationListSummary
}

func latestTaskRunStatus(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	task Response,
) (string, bool, error) {
	row, err := q.GetLatestConversationRunForSummary(ctx, generated.GetLatestConversationRunForSummaryParams{
		ActorUserID:    principal.UserID,
		ConversationID: task.ConversationID, CompanyID: task.CompanyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read latest Task Run: %w", err)
	}
	return row.Status, true, nil
}

func listTasks(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	options ListOptions,
) (Page, error) {
	return listTaskProjections(ctx, q, principal, options)
}

func getTask(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
) (Response, error) {
	return taskProjectionForActiveMember(ctx, q, principal, taskID)
}

// authorizeTaskMutation takes the canonical Company lock and then the acting
// member's row lock — the first two steps of every Task mutation — and
// returns the role the locked row holds, so a permission read later in the
// transaction is the locked fact rather than the token's claim.
func authorizeTaskMutation(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
) (auth.CompanyRole, error) {
	if _, err := q.LockCompanyForMembershipMutation(
		ctx,
		principal.CompanyID,
	); errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		return "", ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("lock Company for Task mutation: %w", err)
	}
	membership, err := q.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
		CompanyID: principal.CompanyID, UserID: principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock acting Company member for Task mutation: %w", err)
	}
	return auth.CompanyRole(membership.Role), nil
}

// lockAssigneeMembership proves the member a Task is being assigned to is
// still an active member, under the same Company lock the caller holds.
// It runs after the acting member's own lock and before any Conversation
// lock, keeping Company → members ahead of everything else.
func lockAssigneeMembership(
	ctx context.Context,
	q *generated.Queries,
	companyID, assigneeUserID uuid.UUID,
) error {
	membership, err := q.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
		CompanyID: companyID, UserID: assigneeUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return ErrAssigneeInactive
	}
	if err != nil {
		return fmt.Errorf("lock Task assignee membership: %w", err)
	}
	return nil
}

func insertTaskWithConversation(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	input CreateInput,
	number int64,
	pending *[]events.Event,
) (Response, generated.Conversation, error) {
	// Creating a Task needs project.tasks.write on its Project (ADR 0017);
	// the statement checks it again for the creator.
	fence := personWriteFence(principal.UserID, auth.ProjectPermissionTasksWrite)
	fenceUserID, fencePermission, sets := fence.params()
	row, err := q.InsertTaskWithConversation(ctx, generated.InsertTaskWithConversationParams{
		FenceUserID:               fenceUserID,
		FencePermission:           fencePermission,
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
		ProjectID:                 input.ProjectID,
		Key:                       input.Key,
		Labels:                    input.Labels,
		StartDate:                 dateToPG(input.StartDate),
		DueDate:                   dateToPG(input.DueDate),
		MilestoneType:             string(input.MilestoneType),
		Number:                    number,
		Title:                     input.Title,
		Description:               input.Description,
		Status:                    string(input.Status),
		Priority:                  string(input.Priority),
		AssigneeUserID:            input.Assignee.ID,
		ExecutedByAgentID:         util.NullUUIDFromPtr(executedByAgentID(*input.ExecutedBy)),
		// A Task a Run creates keeps the member as its creator and also names
		// the Run's Agent. The member's own Task still runs with
		// the Company's root Agent (task_run_agents).
		CreatorUserID:  uuid.NullUUID{UUID: principal.UserID, Valid: true},
		CreatorAgentID: input.Run.creatorAgentID(),
		CompanyID:      principal.CompanyID,
	})
	if err != nil {
		err = explainFencedMiss(ctx, q, fence, principal.CompanyID, input.ProjectID, err)
		return Response{}, generated.Conversation{}, mapTaskMutationError("insert Task and Conversation", err)
	}
	taskRow := generated.Task(row)
	conversationRow := generated.Conversation{
		ID: row.ConversationID, CompanyID: row.CompanyID, Kind: "task",
		TaskID: uuid.NullUUID{UUID: row.ID, Valid: true}, Title: util.StrToText(row.Title),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Version: 1,
	}
	actor := input.Run.actor(principal.UserID)
	if err := activity.RecordInTx(ctx, q, pending, actor, EventCreated, nil, Topic, row.ID); err != nil {
		return Response{}, generated.Conversation{}, err
	}
	if err := activity.RecordInTx(
		ctx,
		q,
		pending,
		actor,
		conversation.EventConversationCreated,
		nil,
		conversation.Topic,
		row.ConversationID,
	); err != nil {
		return Response{}, generated.Conversation{}, err
	}
	return taskFromRow(taskRow), conversationRow, nil
}

// lockTaskForUpdate maps a missing row to ErrNotFound deliberately: visibility
// was already proven, so a miss means the Task vanished mid-flight.
func lockTaskForUpdate(ctx context.Context, q *generated.Queries, companyID, taskID uuid.UUID) (Response, error) {
	row, err := q.GetTaskForCompanyByIDForUpdate(ctx, generated.GetTaskForCompanyByIDForUpdateParams{
		TaskID: taskID, CompanyID: companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Response{}, ErrNotFound
	}
	if err != nil {
		return Response{}, fmt.Errorf("lock Task for update: %w", err)
	}
	return taskFromRow(row), nil
}

// updateTask writes the target state. advanceGeneration is true for a
// confirmed handoff only: it moves the Task's execution and workspace
// generations so the previous assignee's Runs, merges and workspace lose
// their hold on the Task (ADR 0014). fence is the person whose Project
// permission the statement checks again (ADR 0017); the zero value is a
// system writer.
func updateTask(
	ctx context.Context,
	q *generated.Queries,
	target Response,
	expectedVersion int64,
	pending *[]events.Event,
	actor events.Actor,
	advanceGeneration bool,
	fence projectWriteFence,
) (Response, error) {
	if target.Status == StatusDone {
		var reviewer uuid.UUID
		if actor.Type == events.ActorTypeUser {
			var err error
			reviewer, err = uuid.Parse(actor.ID)
			if err != nil {
				return Response{}, err
			}
		}

		state, err := q.GetTaskResultState(
			ctx,
			generated.GetTaskResultStateParams{CompanyID: target.CompanyID, ID: target.ID},
		)
		if err != nil {
			return Response{}, err
		}
		if state.Status != string(StatusDone) {
			if err = requireResultApplication(ctx, q, target, reviewer); err != nil {
				return Response{}, err
			}
		}
	}

	if target.Status != StatusDone && target.Status != StatusCancelled {
		project, err := q.GetTaskProjectForShare(
			ctx,
			generated.GetTaskProjectForShareParams{CompanyID: target.CompanyID, TaskID: target.ID},
		)
		if err != nil {
			return Response{}, fmt.Errorf("read Task Project: %w", err)
		}
		if project.ArchivedAt.Valid {
			return Response{}, ErrProjectArchived
		}
	}
	before, err := activity.ObserveInTx(ctx, q, Topic, target.ID)
	if err != nil {
		return Response{}, err
	}
	fenceUserID, fencePermission, sets := fence.params()
	row, err := q.UpdateTask(ctx, generated.UpdateTaskParams{
		FenceUserID:               fenceUserID,
		FencePermission:           fencePermission,
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
		MilestoneType:             string(target.MilestoneType),
		Title:                     target.Title,
		Description:               target.Description,
		Status:                    string(target.Status),
		Priority:                  string(target.Priority),
		AssigneeUserID:            target.Assignee.ID,
		ExecutedByAgentID:         util.NullUUIDFromPtr(executedByAgentID(target.ExecutedBy)),
		AdvanceGeneration:         advanceGeneration,
		TaskID:                    target.ID,
		CompanyID:                 target.CompanyID,
		ExpectedVersion:           expectedVersion,
		Labels:                    target.Labels,
		SetDates:                  true,
		StartDate:                 dateToPG(target.StartDate),
		DueDate:                   dateToPG(target.DueDate),
	})
	if err != nil {
		err = explainFencedMiss(ctx, q, fence, target.CompanyID, target.ProjectID, err)
		return Response{}, mapTaskMutationError("update Task", err)
	}
	if err := activity.RecordInTx(ctx, q, pending, actor, EventUpdated, before, Topic, row.ID); err != nil {
		return Response{}, err
	}
	return taskFromRow(row), nil
}

// stageTaskConversationCreated builds and stages the conversation.created
// event at the caller's position.
func stageTaskConversationCreated(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	actorID uuid.UUID,
	taskID uuid.UUID,
	created generated.Conversation,
) error {
	return stageTaskEvent(ctx, q, pending, newTaskConversationCreatedEvent(actorID, taskID, created))
}

// stageTaskConversationUpdated builds and stages the conversation.updated
// event at the caller's position.
func stageTaskConversationUpdated(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	actorID uuid.UUID,
	taskID uuid.UUID,
	bumped bumpedConversation,
) error {
	return stageTaskEvent(
		ctx,
		q,
		pending,
		newTaskConversationUpdatedEvent(actorID, taskID, bumped.row, bumped.summary),
	)
}

// stageTaskEvent is unexported on purpose: no other package may stage Task
// wire events through it. A staging error propagates so the transaction that
// would have described the fact aborts with it.
//
// It is also the one place every Task frame passes: a Response payload loses
// its reader's access here too, whichever constructor built it, and every
// frame about the Task or its chat takes the audience of the Task's Project
// (ADR 0017), decided in this transaction.
func stageTaskEvent(ctx context.Context, q *generated.Queries, pending *[]events.Event, event events.Event) error {
	return stageTaskEvents(ctx, q, pending, event)
}

// stageTaskEvents is stageTaskEvent for several frames at once: their
// audiences are decided in one pass, so frames of one Project read its
// access once.
func stageTaskEvents(ctx context.Context, q *generated.Queries, pending *[]events.Event, staged ...events.Event) error {
	for index, event := range staged {
		if value, ok := event.Payload.(Response); ok {
			staged[index].Payload = framedResponse(value)
		}
	}
	if err := auth.StampProjectAudiencesInTx(ctx, q, staged); err != nil {
		return err
	}
	*pending = append(*pending, staged...)
	return nil
}

// framedResponse is a Task as a realtime frame carries it. The frame reaches
// other people, so the permissions of whoever read the Task stay off it
// (ADR 0017); a client refetches the Task for its own.
func framedResponse(value Response) Response {
	value.Access = nil
	return value
}

// taskExecutionPromptContent is the words a start-execution Run is given. It
// is built before the Run row exists, because the Run freezes the same string
// into its context and the item that stores it is written afterwards.
func taskExecutionPromptContent(task Response) string {
	return task.Title + "\n---\n" + task.Description
}

func insertTaskExecutionPromptMessage(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	task Response,
	runID uuid.UUID,
	content string,
) (generated.ConversationItem, error) {
	message, err := q.InsertUserMessageAfterConversationLock(
		ctx,
		generated.InsertUserMessageAfterConversationLockParams{
			RunID:          runID,
			Content:        content,
			ActorUserID:    principal.UserID,
			ConversationID: task.ConversationID, CompanyID: task.CompanyID,
		},
	)
	if err != nil {
		return generated.ConversationItem{}, mapTaskMutationError(
			"insert Task execution prompt Message", err,
		)
	}
	return message, nil
}

// taskFilesMessageContent is the words the files a Task was created with
// travel under; the files themselves are the message's Attachments.
func taskFilesMessageContent(count int) string {
	if count == 1 {
		return "Attached 1 file for this Task."
	}
	return fmt.Sprintf("Attached %d files for this Task.", count)
}

// insertTaskFilesMessage writes the member message that carries the files a
// Task was created with. It belongs to the assignment Run, which froze the
// same files before this row existed; the message is what keeps them: it lists
// them as the Task's sources, holds them out of the abandoned-upload sweep
// and hands them to every later Run of the Task. The Conversation is new in
// this transaction, so no other writer can race the seq it takes.
func insertTaskFilesMessage(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	task Response,
	runID uuid.UUID,
	attachmentIDs []uuid.UUID,
) (conversation.MessageView, error) {
	message, err := q.InsertUserMessageAfterConversationLock(
		ctx,
		generated.InsertUserMessageAfterConversationLockParams{
			RunID:          runID,
			Content:        taskFilesMessageContent(len(attachmentIDs)),
			ActorUserID:    principal.UserID,
			ConversationID: task.ConversationID, CompanyID: task.CompanyID,
		},
	)
	if err != nil {
		return conversation.MessageView{}, mapTaskMutationError("insert Task files Message", err)
	}
	for position, attachmentID := range attachmentIDs {
		_, err := q.LinkMessageAttachment(ctx, generated.LinkMessageAttachmentParams{
			AttachmentID: attachmentID, CompanyID: task.CompanyID,
			ActorUserID: principal.UserID, Position: int32(position),
			MessageID: message.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return conversation.MessageView{}, ErrAttachmentUnavailable
		}
		if err != nil {
			return conversation.MessageView{}, fmt.Errorf("link Task file: %w", err)
		}
	}
	view, err := conversation.MessageViewWithAttachmentsFromRow(
		ctx, q, principal.UserID, task.CompanyID, message,
	)
	if err != nil {
		return conversation.MessageView{}, fmt.Errorf("project Task files Message: %w", err)
	}
	return view, nil
}

func taskConversationSummary(
	ctx context.Context,
	q *generated.Queries,
	actorID uuid.UUID,
	conversationRow generated.Conversation,
) (taskConversationListSummary, error) {
	params := generated.GetConversationListSummaryBaseParams{
		ActorUserID:    actorID,
		ConversationID: conversationRow.ID, CompanyID: conversationRow.CompanyID,
	}
	base, err := q.GetConversationListSummaryBase(ctx, params)
	if err != nil {
		return taskConversationListSummary{}, fmt.Errorf("read Task Conversation summary base: %w", err)
	}
	summary := taskConversationListSummary{
		UpdatedAt: base.UpdatedAt.Time.UTC(),
		Runs: conversation.RunsSummary{
			QueuedCount: base.QueuedRunCount, ActiveCount: base.ActiveRunCount,
		},
	}
	message, err := q.GetLatestConversationMessageForSummary(
		ctx,
		generated.GetLatestConversationMessageForSummaryParams(params),
	)
	if err == nil {
		// The preview travels with the summary. This is the fourth publisher
		// of this wire type, and clients cache-write what it carries, so
		// dropping content here blanks the row the conversation list just
		// rendered — the exact failure the shared constructor exists to stop.
		summary.LastMessage = &conversation.LastMessageSummary{
			Seq:              message.Seq,
			Content:          message.ContentPreview,
			ContentTruncated: message.ContentTruncated,
			CreatedAt:        message.CreatedAt.Time.UTC(),
			Type:             conversation.ItemType(message.Type),
			AuthorKind:       conversation.ActorKind(message.AuthorKind),
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return taskConversationListSummary{}, fmt.Errorf("read Task Conversation latest Message: %w", err)
	}
	latest, err := q.GetLatestConversationRunForSummary(
		ctx,
		generated.GetLatestConversationRunForSummaryParams(params),
	)
	if err == nil {
		summary.Runs.Latest = &conversation.LatestRunSummary{
			RunID: latest.RunID, AgentID: latest.AgentID,
			Status: conversation.RunStatus(latest.Status), CreatedAt: latest.CreatedAt.Time.UTC(),
			FailureReason: util.TextToPtr(latest.FailureReason),
			Attempt:       latest.Attempt,
			MaxAttempts:   execution.MaxAttemptsFor(latest.FailureReason.String, latest.Attempt),
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return taskConversationListSummary{}, fmt.Errorf("read Task Conversation latest Run: %w", err)
	}
	return summary, nil
}

func mapTaskMutationError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrConflict)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503", "23505", "23514":
			return fmt.Errorf("%s: %w", operation, ErrConflict)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
