package task

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func listTaskProjections(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	options ListOptions,
) (Page, error) {
	rows, err := q.ListTaskProjectionsForActiveMember(
		ctx,
		taskProjectionParams(principal, options, nil),
	)
	if err != nil {
		return Page{}, fmt.Errorf("list canonical Task projections: %w", err)
	}
	values := make([]Response, len(rows))
	for index, row := range rows {
		values[index] = responseFromProjectionRow(row)
	}
	return Page{Tasks: values}, nil
}

func taskProjectionForActiveMember(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
) (Response, error) {
	rows, err := q.ListTaskProjectionsForActiveMember(
		ctx,
		taskProjectionParams(principal, ListOptions{}, &taskID),
	)
	if err != nil {
		return Response{}, fmt.Errorf("read canonical Task projection: %w", err)
	}
	if len(rows) == 0 {
		return Response{}, ErrNotFound
	}
	return responseFromProjectionRow(rows[0]), nil
}

func taskProjectionParams(
	principal auth.UserPrincipal,
	options ListOptions,
	taskID *uuid.UUID,
) generated.ListTaskProjectionsForActiveMemberParams {
	sets := auth.ProjectPermissionHeldSets()
	params := generated.ListTaskProjectionsForActiveMemberParams{
		ActorUserID:               principal.UserID,
		CompanyID:                 principal.CompanyID,
		Statuses:                  []string{},
		Priorities:                []string{},
		ExecutePermission:         string(auth.ProjectPermissionTasksExecute),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
	}
	if options.ProjectID != nil {
		params.HasProjectID = true
		params.ProjectID = *options.ProjectID
	}
	if taskID != nil {
		params.HasTaskID = true
		params.TaskID = *taskID
	}
	if len(options.Statuses) > 0 {
		params.HasStatuses = true
		params.Statuses = make([]string, len(options.Statuses))
		for index, status := range options.Statuses {
			params.Statuses[index] = string(status)
		}
	}
	if len(options.Priorities) > 0 {
		params.HasPriorities = true
		params.Priorities = make([]string, len(options.Priorities))
		for index, priority := range options.Priorities {
			params.Priorities[index] = string(priority)
		}
	}
	if options.AssigneeUserID != nil {
		params.HasAssignee = true
		params.AssigneeID = *options.AssigneeUserID
	}
	params.AssistedTaskID = util.NullUUIDFromPtr(options.assistedTaskID)
	if options.ExecutedByAgentID != nil {
		params.HasExecutedBy = true
		params.ExecutedByAgentID = *options.ExecutedByAgentID
	}
	if options.DependenciesResolved != nil {
		params.HasDependenciesResolved = true
		params.DependenciesResolved = *options.DependenciesResolved
	}
	return params
}

// systemProjectionParams names one Task for GetTaskProjectionForSystem and
// binds what its readiness reads: whether the assignee holds
// project.tasks.execute on the Task's Project (assignee_cannot_run, ADR
// 0017), stated with the same sets every project_permission_held call binds.
func systemProjectionParams(companyID, taskID uuid.UUID) generated.GetTaskProjectionForSystemParams {
	sets := auth.ProjectPermissionHeldSets()
	return generated.GetTaskProjectionForSystemParams{
		CompanyID:                 companyID,
		TaskID:                    taskID,
		ExecutePermission:         string(auth.ProjectPermissionTasksExecute),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
	}
}

// StageChangesInTx stages what a cross-domain Task write owes once its rows
// are final: the canonical task.created/task.updated events on pending, and
// the Inbox items Create and Update raise for the same write — task_assigned
// when the assignee became a user, blocker when the Task entered blocked,
// task_handoff when a handoff stopped the assignee's run — the review
// notices a handoff closes, and the previous assignee's task_assigned
// notices a change of assignee closes.
// Company Plan reconciliation owns that transaction; Task still owns the
// event names, the payload shape and the Inbox rules, and one system
// projection read per Task serves both. The caller presents the returned
// items after commit with inbox.PresentCreated; the durable rows and their
// inbox.created events are staged here.
func StageChangesInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	changes []Change,
) ([]inbox.CreateResult, error) {
	if len(changes) == 0 {
		return nil, nil
	}
	var inboxResults []inbox.CreateResult
	for _, change := range changes {
		// The applier's own view is not used: a frame and an Inbox item carry
		// no reader's access, and until the Company Plan apply is scoped by
		// Project permission (ADR 0017, a later step) it may write Tasks in a
		// Project the applier cannot read.
		row, err := q.GetTaskProjectionForSystem(ctx, systemProjectionParams(principal.CompanyID, change.ID))
		if err != nil {
			return nil, fmt.Errorf("read Task change projection %s: %w", change.ID, err)
		}
		value := responseFromSystemProjectionRow(row)
		var name string
		var previous *Response
		switch change.Type {
		case ChangeTypeCreated:
			name = EventCreated
		case ChangeTypeUpdated:
			name = EventUpdated
			previous = &Response{Assignee: change.PreviousAssignee, Status: change.PreviousStatus}
		default:
			return nil, fmt.Errorf(
				"stage Task change %s: unknown change type %q",
				change.ID,
				change.Type,
			)
		}
		if err := stageTaskEvent(ctx, q, pending, newTaskEvent(principal.UserID, name, value)); err != nil {
			return nil, err
		}
		created, err := createTaskInboxItems(ctx, q, pending, previous, value, principal.UserID, handoffOutcome{
			advanced: change.Handoff, runStopped: change.RunStopped,
		})
		if err != nil {
			return nil, err
		}
		inboxResults = append(inboxResults, created...)
	}
	return inboxResults, nil
}

// responseFromProjectionRow maps a row read for a person: the read decided
// their access in the statement and returned the facts beside the Task, and
// auth.ProjectAccessOf states the effective set from them, the same merge a
// Project read uses.
func responseFromProjectionRow(row generated.ListTaskProjectionsForActiveMemberRow) Response {
	value := projectedResponse(
		row.Task, row.AssigneeDisplayName, row.AssigneeAvatarUrl, row.ExecutedByDisplayName,
		row.CreatorDisplayName, row.CreatorAvatarUrl, row.BlockedByCount, row.DependenciesResolved,
		row.UnreadyReason, row.RunAgentID, row.RunAgentName,
	)
	value.AssigneeOutsideProject = row.AssigneeOutsideProject
	access := auth.ProjectAccessOf(auth.ProjectAccessFacts{
		ProjectID:          row.Task.ProjectID,
		CompanyID:          row.Task.CompanyID,
		Visibility:         row.ProjectVisibility,
		Via:                row.AccessVia,
		Management:         row.AccessManagement,
		Revision:           row.ProjectAccessVersion,
		GrantedPermissions: row.GrantedPermissions,
		GroupPermissions:   row.GroupPermissions,
	})
	value.Access = &Access{Permissions: access.Permissions}
	return value
}

func responseFromSystemProjectionRow(row generated.GetTaskProjectionForSystemRow) Response {
	value := projectedResponse(
		row.Task, row.AssigneeDisplayName, row.AssigneeAvatarUrl, row.ExecutedByDisplayName,
		row.CreatorDisplayName, row.CreatorAvatarUrl, row.BlockedByCount, row.DependenciesResolved,
		row.UnreadyReason, row.RunAgentID, row.RunAgentName,
	)
	value.AssigneeOutsideProject = row.AssigneeOutsideProject
	return value
}

// projectedResponse is the one mapping both projection reads share. The
// executing Agent's name comes from the row; when the assignee does the
// work themself, executedBy is the assignee and takes their name and
// avatar. The empty unready reason the query answers when ready makes the
// Task ready and becomes an explicit null, so assigneeReady and
// unreadyReason never disagree.
func projectedResponse(
	row generated.Task,
	assigneeDisplayName string,
	assigneeAvatarURL pgtype.Text,
	executedByDisplayName string,
	creatorDisplayName string,
	creatorAvatarURL pgtype.Text,
	blockedByCount int64,
	dependenciesResolved bool,
	unreadyReason string,
	runAgentID uuid.NullUUID,
	runAgentName string,
) Response {
	value := taskFromRow(row)
	value.Assignee.DisplayName = assigneeDisplayName
	value.Assignee.AvatarURL = util.TextToPtr(assigneeAvatarURL)
	if value.ExecutedBy.Kind == ActorAgent {
		value.ExecutedBy.DisplayName = executedByDisplayName
	} else {
		value.ExecutedBy = value.Assignee
	}
	value.Creator.DisplayName = creatorDisplayName
	value.Creator.AvatarURL = util.TextToPtr(creatorAvatarURL)
	value.BlockedByCount = blockedByCount
	value.DependenciesResolved = dependenciesResolved
	value.Execution = Execution{AssigneeReady: unreadyReason == ""}
	if runAgentID.Valid {
		value.Execution.Agent = &ActorRef{Kind: ActorAgent, ID: runAgentID.UUID, DisplayName: runAgentName}
	}
	if unreadyReason != "" {
		reason := unreadyReason
		value.Execution.UnreadyReason = &reason
	}
	return value
}

// PublishReadinessChanged announces again the open Tasks a member is
// assigned and one of agentIDs executes, after that member's setup of those
// Agents, or the computer it names, changed. Whether such a Task can start
// is read live off that setup (ADR 0014), so no Task row moved: the frame
// keeps the Task's revision, and carries the projection every member reads,
// which names no computer. It reads committed state, so it runs after the
// change commits. A member who is no longer active reads no Tasks, and
// nothing is published for them.
func PublishReadinessChanged(
	ctx context.Context,
	q *generated.Queries,
	bus *events.Bus,
	companyID, assigneeID uuid.UUID,
	agentIDs []uuid.UUID,
) error {
	page, err := listTaskProjections(
		ctx, q,
		auth.UserPrincipal{UserID: assigneeID, CompanyID: companyID},
		ListOptions{AssigneeUserID: &assigneeID},
	)
	if err != nil {
		return fmt.Errorf("read Tasks whose readiness changed: %w", err)
	}
	var pending, frames []events.Event
	for _, value := range page.Tasks {
		if value.Status == StatusDone || value.Status == StatusCancelled ||
			value.Execution.Agent == nil || !slices.Contains(agentIDs, value.Execution.Agent.ID) {
			continue
		}
		frames = append(frames, newTaskEventForActor(events.SystemActor(), EventUpdated, value))
	}
	// Each frame reaches only its Project's readers (ADR 0017), and drops the
	// assignee's access as every Task frame does.
	if err := stageTaskEvents(ctx, q, &pending, frames...); err != nil {
		return fmt.Errorf("stage Task readiness frames: %w", err)
	}
	bus.Publish(ctx, Topic, pending...)
	return nil
}
