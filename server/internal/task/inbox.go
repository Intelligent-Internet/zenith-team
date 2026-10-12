package task

import (
	"context"
	"fmt"
	"net/url"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// handoffOutcome is what a Task write's confirmed handoff did (ADR 0014).
// advanced: it moved the execution generation on, so no earlier Run of the
// Task can be reviewed or merged. runStopped: it cancelled a live Run.
type handoffOutcome struct {
	advanced   bool
	runStopped bool
}

// createTaskInboxItems writes the notices a Task write raises and closes the
// ones its handoff or change of assignee ended.
func createTaskInboxItems(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	previous *Response,
	current Response,
	actorUserID uuid.UUID,
	handoff handoffOutcome,
) ([]inbox.CreateResult, error) {
	results := make([]inbox.CreateResult, 0, 4)
	actor := inbox.Actor{Kind: "user", ID: &actorUserID}
	deepLink := taskInboxDeepLink(current.ID)

	// Nobody reviews a Run from before a handoff any more, so its "Ready to
	// review" notice closes, whoever holds it.
	if handoff.advanced {
		if err := inbox.CloseTaskRunReviewsInTx(
			ctx, tx, pending, current.CompanyID, current.ID, actor,
		); err != nil {
			return nil, fmt.Errorf("close Task Run review Inbox items for handoff: %w", err)
		}
	}

	// The new assignee is told who gave them the Task and how to start it on
	// their computer (ADR 0014); a member who assigned themself needs no
	// telling.
	assigned := current.Assignee.ID != actorUserID &&
		(previous == nil || !sameActor(previous.Assignee, current.Assignee))
	if assigned {
		// The same member-name read the task_run_requested copy uses.
		assigner, err := tx.GetRunTriggerAuthor(ctx, generated.GetRunTriggerAuthorParams{
			CompanyID: current.CompanyID, UserID: actorUserID,
		})
		if err != nil {
			return nil, fmt.Errorf("read Task assigner name: %w", err)
		}
		body := taskAssignedBody(current, assigner.DisplayName)
		result, err := inbox.CreateInTx(ctx, tx, pending, inbox.CreateInput{
			CompanyID:       current.CompanyID,
			RecipientUserID: current.Assignee.ID,
			ConversationID:  &current.ConversationID,
			Type:            inbox.TypeTaskAssigned,
			Entity:          inbox.Entity{Type: protocol.EntityTask, ID: current.ID.String()},
			Title:           "Task assigned",
			Body:            body,
			DeepLink:        &deepLink,
			DedupeKey: fmt.Sprintf(
				"task:%s:version:%d:assigned", current.ID, current.Revision,
			),
			Actor: actor,
		})
		if err != nil {
			return nil, fmt.Errorf("create Task assignment Inbox item: %w", err)
		}
		if result.Created {
			results = append(results, result)
		}
	}

	// Only the assignee starts a Task, so the notice that asked the previous
	// assignee to start it closes, whoever moved it.
	reassigned := previous != nil && !sameActor(previous.Assignee, current.Assignee) &&
		previous.Assignee.ID != uuid.Nil
	if reassigned {
		if err := inbox.CloseTaskAssignmentsInTx(
			ctx, tx, pending, current.CompanyID, current.ID, previous.Assignee.ID, actor,
		); err != nil {
			return nil, fmt.Errorf("close the previous assignee's Task assignment Inbox items: %w", err)
		}
	}

	// The previous assignee learns the Task left their computer, unless
	// they handed it over themself, and whether their run there was
	// stopped. Files they did not merge stay there; nothing moves them.
	handedOff := reassigned && previous.Assignee.ID != actorUserID
	if handedOff {
		body := fmt.Sprintf("“%s” was handed to %s.", current.Title, current.Assignee.DisplayName)
		if handoff.runStopped {
			body += " Its run on your computer was stopped."
		}
		result, err := inbox.CreateInTx(ctx, tx, pending, inbox.CreateInput{
			CompanyID:       current.CompanyID,
			RecipientUserID: previous.Assignee.ID,
			ConversationID:  &current.ConversationID,
			Type:            inbox.TypeTaskHandoff,
			Entity:          inbox.Entity{Type: protocol.EntityTask, ID: current.ID.String()},
			Title:           "Task handed off",
			Body:            body + " Files that were not merged stay on your computer.",
			DeepLink:        &deepLink,
			DedupeKey: fmt.Sprintf(
				"task:%s:version:%d:handoff", current.ID, current.Revision,
			),
			Actor: actor,
		})
		if err != nil {
			return nil, fmt.Errorf("create Task handoff Inbox item: %w", err)
		}
		if result.Created {
			results = append(results, result)
		}
	}

	// Moving the work to another Agent is a handoff too. The assignee keeps
	// the Task, but when someone else's move stopped their run, they learn
	// who stopped it and how to go on.
	agentMoved := handoff.runStopped && previous != nil &&
		sameActor(previous.Assignee, current.Assignee) && current.Assignee.ID != actorUserID
	if agentMoved {
		mover, err := tx.GetRunTriggerAuthor(ctx, generated.GetRunTriggerAuthorParams{
			CompanyID: current.CompanyID, UserID: actorUserID,
		})
		if err != nil {
			return nil, fmt.Errorf("read Task handoff actor name: %w", err)
		}
		result, err := inbox.CreateInTx(ctx, tx, pending, inbox.CreateInput{
			CompanyID:       current.CompanyID,
			RecipientUserID: current.Assignee.ID,
			ConversationID:  &current.ConversationID,
			Type:            inbox.TypeTaskHandoff,
			Entity:          inbox.Entity{Type: protocol.EntityTask, ID: current.ID.String()},
			Title:           "Task run stopped",
			Body:            agentMovedBody(current, mover.DisplayName),
			DeepLink:        &deepLink,
			DedupeKey: fmt.Sprintf(
				"task:%s:version:%d:handoff", current.ID, current.Revision,
			),
			Actor: actor,
		})
		if err != nil {
			return nil, fmt.Errorf("create Task Agent handoff Inbox item: %w", err)
		}
		if result.Created {
			results = append(results, result)
		}
	}

	blocked := current.Status == StatusBlocked &&
		(previous == nil || previous.Status != StatusBlocked)
	if blocked {
		recipients := make([]uuid.UUID, 0, 2)
		if current.Creator.Kind == ActorUser {
			recipients = append(recipients, current.Creator.ID)
		}
		if current.Assignee.Kind == ActorUser &&
			(len(recipients) == 0 || recipients[0] != current.Assignee.ID) {
			recipients = append(recipients, current.Assignee.ID)
		}
		for _, recipientID := range recipients {
			result, err := inbox.CreateInTx(ctx, tx, pending, inbox.CreateInput{
				CompanyID:       current.CompanyID,
				RecipientUserID: recipientID,
				ConversationID:  &current.ConversationID,
				Type:            inbox.TypeBlocker,
				Entity:          inbox.Entity{Type: protocol.EntityTask, ID: current.ID.String()},
				Title:           "Task blocked",
				Body:            fmt.Sprintf("“%s” is blocked.", current.Title),
				DeepLink:        &deepLink,
				DedupeKey: fmt.Sprintf(
					"task:%s:version:%d:blocked", current.ID, current.Revision,
				),
				Actor: actor,
			})
			if err != nil {
				return nil, fmt.Errorf("create blocked Task Inbox item: %w", err)
			}
			if result.Created {
				results = append(results, result)
			}
		}
	}
	return results, nil
}

// taskAssignedBody is the new assignee's task_assigned copy: who assigned
// the Task, then its runNextStep.
func taskAssignedBody(current Response, assignerName string) string {
	body := fmt.Sprintf("%s assigned you “%s”.", assignerName, current.Title)
	if step := runNextStep(current); step != "" {
		return body + " " + step
	}
	return body
}

// agentMovedBody is the assignee's task_handoff copy when someone else moved
// their Task to another Agent, or into their own hands, and so stopped its
// run: who did it, what stays, then runNextStep.
func agentMovedBody(current Response, moverName string) string {
	moved := fmt.Sprintf("%s moved “%s” to %s.", moverName, current.Title, current.ExecutedBy.DisplayName)
	if current.ExecutedBy.Kind != ActorAgent {
		moved = fmt.Sprintf("%s took “%s” off its Agent, so you do it yourself.", moverName, current.Title)
	}
	body := moved + " Its run on your computer was stopped. Files that were not merged stay on your computer."
	if step := runNextStep(current); step != "" {
		return body + " " + step
	}
	return body
}

// RunNextStepInTx is runNextStep for a notice another domain writes about
// the Task row it holds: the task_run_requested nudge a colleague's queued
// Message raises. It reads the Task's projection in the caller's
// transaction, so the step matches the status and the assignee's binding
// that the caller's write sees.
func RunNextStepInTx(ctx context.Context, q *generated.Queries, row generated.Task) (string, error) {
	projection, err := q.GetTaskProjectionForSystem(ctx, systemProjectionParams(row.CompanyID, row.ID))
	if err != nil {
		return "", fmt.Errorf("project Task for its next step: %w", err)
	}
	return runNextStep(responseFromSystemProjectionRow(projection)), nil
}

// runNextStep is the one sentence that tells a Task's assignee how to get
// its Agent running from where the Task stands. It never promises a start
// the server would refuse: an Agent they have not set up or a computer that
// cannot run it comes first, and a Task whose status the Run button is not
// offered for (in progress, in review, failed) is run again from its chat,
// as a Task message does. A closed Task, or work they do themself, has no
// next step and gets "".
func runNextStep(current Response) string {
	if allowed, _ := taskMessageRunStatusPolicy(current.Status); !allowed ||
		current.ExecutedBy.Kind != ActorAgent {
		return ""
	}
	agentName := current.ExecutedBy.DisplayName
	var unready string
	if current.Execution.UnreadyReason != nil {
		unready = *current.Execution.UnreadyReason
	}
	switch unready {
	case "not_bound":
		return fmt.Sprintf("Set %s up on your computer to run it.", agentName)
	case "agent_archived":
		return fmt.Sprintf("%s is archived, so it cannot run.", agentName)
	case "assignee_cannot_run":
		return fmt.Sprintf(
			"You can't run tasks in this work yet, so %s cannot run it. Ask someone who manages this work to let you run tasks.",
			agentName,
		)
	case "runtime_offline":
		return fmt.Sprintf("%s runs on your computer, which is offline right now.", agentName)
	case "runtime_signed_out":
		return fmt.Sprintf("%s runs on your computer, where the provider sign-in has expired.", agentName)
	case "runtime_unusable":
		return fmt.Sprintf("%s runs on your computer, which can't run it right now.", agentName)
	}
	if runnableTaskStatus(current.Status) {
		return fmt.Sprintf("Start it on your computer: %s runs there.", agentName)
	}
	return fmt.Sprintf("Reply in its chat to run %s on your computer.", agentName)
}

func taskInboxDeepLink(taskID uuid.UUID) string {
	query := url.Values{
		"desktop_search": []string{"1"},
		"taskId":         []string{taskID.String()},
	}
	return "/app?" + query.Encode()
}
