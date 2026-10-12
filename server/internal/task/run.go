package task

import (
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/conversation"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// WAVE3: the run snapshot builders (agent/task/resource freeze, dependency
// snapshots, prompt template version) lived here; the rewritten run-enqueue
// kernel owns run creation and its frozen inputs.

type taskConversationListSummary = conversation.ListSummary

func taskFromRow(row generated.Task) Response {
	var completedAt *time.Time
	if row.CompletedAt.Valid {
		value := row.CompletedAt.Time.UTC()
		completedAt = &value
	}
	return Response{
		ScheduleGeneration:   row.TaskScheduleGeneration,
		AssignmentStatus:     row.AssignmentStatus,
		ScheduledAt:          calendarTime(row.ScheduledAt),
		ScheduleFiredAt:      calendarTime(row.ScheduleFiredAt),
		ScheduleID:           util.NullUUIDPtr(row.TaskScheduleID),
		ScheduleOccurrenceAt: calendarTime(row.ScheduleOccurrenceAt),
		ScheduleDisposition:  row.ScheduleDisposition,
		ID:                   row.ID,
		Key:                  row.Key,
		ProjectID:            row.ProjectID,
		CompanyID:            row.CompanyID,
		MilestoneType:        MilestoneType(row.MilestoneType),
		ConversationID:       row.ConversationID,
		Number:               row.Number,
		Title:                row.Title,
		Description:          row.Description,
		Status:               Status(row.Status),
		Priority:             Priority(row.Priority),
		Labels: append(
			[]string{},
			row.Labels...),
		StartDate:   dateFromPG(row.StartDate),
		DueDate:     dateFromPG(row.DueDate),
		Assignee:    ActorRef{Kind: ActorUser, ID: row.AssigneeUserID},
		ExecutedBy:  executedByFromPG(row.AssigneeUserID, row.ExecutedByAgentID),
		Creator:     actorFromPG(row.CreatorUserID, row.CreatorAgentID),
		CreatedAt:   row.CreatedAt.Time.UTC(),
		UpdatedAt:   row.UpdatedAt.Time.UTC(),
		CompletedAt: completedAt,
		Revision:    row.Version,

		ExecutionGeneration: row.ExecutionGeneration,
	}
}

func actorFromPG(userID, agentID uuid.NullUUID) ActorRef {
	if userID.Valid {
		return ActorRef{Kind: ActorUser, ID: userID.UUID}
	}
	return ActorRef{Kind: ActorAgent, ID: agentID.UUID}
}

// executedByFromPG is who does the work: the executing Agent when the row
// names one, else the assignee themself (ADR 0014).
func executedByFromPG(assigneeUserID uuid.UUID, executedByAgentID uuid.NullUUID) ActorRef {
	if executedByAgentID.Valid {
		return ActorRef{Kind: ActorAgent, ID: executedByAgentID.UUID}
	}
	return ActorRef{Kind: ActorUser, ID: assigneeUserID}
}

// newTaskEvent builds task.created / task.updated around the exact public
// Task response.
func newTaskEvent(actorID uuid.UUID, name string, value Response) events.Event {
	return newTaskEventForActor(events.UserActor(actorID.String()), name, value)
}

func newTaskEventForActor(actor events.Actor, name string, value Response) events.Event {
	value = framedResponse(value)
	return events.Event{
		EntityType:     Topic,
		Name:           name,
		CompanyID:      value.CompanyID,
		CreatedAt:      time.Now().UTC(),
		TaskID:         value.ID,
		ConversationID: value.ConversationID,
		Actor:          actor,
		Payload:        value,
	}
}

// newTaskMessageCreatedEvent announces a member message the Task domain
// wrote: the execution prompt, or the files a Task was created with.
func newTaskMessageCreatedEvent(
	actor events.Actor,
	companyID uuid.UUID,
	message conversation.MessageView,
) events.Event {
	// A Task chat has no single creator to narrow delivery to: it reaches
	// its Project's readers, whose audience stageTaskEvent decides (ADR 0017).
	return conversation.NewMemberMessageCreatedEventBy(
		actor, companyID, conversation.KindTask, nil, message,
	)
}

func newTaskConversationCreatedEvent(
	actorID uuid.UUID,
	taskID uuid.UUID,
	row generated.Conversation,
) events.Event {
	return conversation.NewMemberEvent(
		actorID, &taskID, conversation.ResponseFromRow(row),
		conversation.EventConversationCreated, nil,
	)
}

func newTaskConversationUpdatedEvent(
	actorID uuid.UUID,
	taskID uuid.UUID,
	row generated.Conversation,
	summary taskConversationListSummary,
) events.Event {
	return conversation.NewMemberEvent(
		actorID, &taskID, conversation.ResponseFromRow(row),
		conversation.EventConversationUpdated, &summary,
	)
}
