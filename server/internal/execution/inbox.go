package execution

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

type runInboxTarget struct {
	runID            uuid.UUID
	companyID        uuid.UUID
	recipientUserID  uuid.UUID
	conversationKind string
	agentName        string
	workTitle        string
	// The failed run's provider, classifier and raw failure text, read only
	// for a failed status so the body can say why rather than just that.
	provider      string
	failureReason string
	failureText   string
	// The request a completed Run stopped to wait for, set only when the
	// recipient does not hold that request's own item: the notice then says
	// the Run is paused instead of that it is ready.
	awaiting protocol.EntityType
}

type runInboxRecipientKey struct {
	companyID       uuid.UUID
	recipientUserID uuid.UUID
}

// lockRunInboxTargets resolves each Run's personal recipient, then takes the
// Company/member locks in a deterministic order. Terminal paths call it before
// their Conversation locks so a user mutation and a daemon transition cannot
// deadlock by acquiring the same graph in opposite directions.
func lockRunInboxTargets(
	ctx context.Context,
	queries *db.Queries,
	runs []db.Run,
) (map[uuid.UUID]runInboxTarget, error) {
	targets := make([]runInboxTarget, 0, len(runs))
	for _, run := range runs {
		recipientUserID, err := queries.ResolveRunInboxRecipient(ctx, run.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve terminal Run Inbox recipient: %w", err)
		}
		route, err := queries.GetConversationRoute(ctx, db.GetConversationRouteParams{
			ConversationID: run.ConversationID,
			CompanyID:      run.CompanyID,
		})
		if err != nil {
			return nil, fmt.Errorf("resolve terminal Run Inbox route: %w", err)
		}
		targets = append(targets, runInboxTarget{
			runID:            run.ID,
			companyID:        run.CompanyID,
			recipientUserID:  recipientUserID,
			conversationKind: route.Kind,
		})
	}

	sort.Slice(targets, func(i, j int) bool {
		left := targets[i]
		right := targets[j]
		if left.companyID != right.companyID {
			return left.companyID.String() < right.companyID.String()
		}
		if left.recipientUserID != right.recipientUserID {
			return left.recipientUserID.String() < right.recipientUserID.String()
		}
		return left.runID.String() < right.runID.String()
	})

	eligibility := make(map[runInboxRecipientKey]bool, len(targets))
	locked := make(map[uuid.UUID]runInboxTarget, len(targets))
	for _, target := range targets {
		key := runInboxRecipientKey{
			companyID:       target.companyID,
			recipientUserID: target.recipientUserID,
		}
		eligible, seen := eligibility[key]
		if !seen {
			var err error
			eligible, err = inbox.LockRecipientInTx(
				ctx,
				queries,
				target.companyID,
				target.recipientUserID,
			)
			if err != nil {
				return nil, err
			}
			eligibility[key] = eligible
		}
		if eligible {
			locked[target.runID] = target
		}
	}
	return locked, nil
}

func createRunTerminalInboxItem(
	ctx context.Context,
	queries *db.Queries,
	pending *[]events.Event,
	run db.Run,
	target runInboxTarget,
	research companyResearchOutcome,
) (inbox.CreateResult, error) {
	if research.research {
		// A retried attempt is not the end; the final attempt notifies.
		if research.retrying {
			return inbox.CreateResult{}, nil
		}
		return createCompanyResearchInboxItem(ctx, queries, pending, run, target, research)
	}
	if run.Status == "completed" {
		waits, err := queries.ListRunHumanInputWaits(ctx, db.ListRunHumanInputWaitsParams{
			RecipientUserID: target.recipientUserID,
			RunID:           run.ID,
		})
		if err != nil {
			return inbox.CreateResult{}, fmt.Errorf("read terminal Run human input: %w", err)
		}
		awaiting, notify := runInboxAwaiting(waits)
		if !notify {
			return inbox.CreateResult{}, nil
		}
		target.awaiting = awaiting
	}
	agent, err := queries.GetRunAgentBrief(
		ctx,
		db.GetRunAgentBriefParams{AgentID: run.AgentID, RuntimeID: run.RuntimeID},
	)
	if err != nil {
		return inbox.CreateResult{}, fmt.Errorf("read terminal Run Inbox agent: %w", err)
	}
	target.agentName = agent.Name
	workTitle, readable, err := runInboxWorkTitle(ctx, queries, run, target.recipientUserID)
	if err != nil || !readable {
		return inbox.CreateResult{}, err
	}
	target.workTitle = workTitle
	if run.Status == "failed" {
		target.failureReason = run.FailureReason.String
		target.failureText = run.Error.String
		runtime, err := queries.GetRunRuntime(ctx, db.GetRunRuntimeParams{
			CompanyID: run.CompanyID,
			ID:        run.RuntimeID,
		})
		if err != nil {
			return inbox.CreateResult{}, fmt.Errorf("read terminal Run Inbox runtime: %w", err)
		}
		target.provider = runtime.Provider
	}
	itemType, title, body := runInboxContent(run.Status, target)
	deepLink := runInboxDeepLink(target.conversationKind, run)
	actorID := run.AgentID
	result, err := inbox.CreateInTx(ctx, queries, pending, inbox.CreateInput{
		CompanyID:       run.CompanyID,
		RecipientUserID: target.recipientUserID,
		ConversationID:  &run.ConversationID,
		Type:            itemType,
		Entity: inbox.Entity{
			Type: protocol.EntityRun,
			ID:   run.ID.String(),
		},
		Title:     title,
		Body:      body,
		DeepLink:  &deepLink,
		DedupeKey: fmt.Sprintf("run:%s:%s", run.ID, run.Status),
		Actor:     inbox.Actor{Kind: "agent", ID: &actorID},
	})
	if err != nil {
		return inbox.CreateResult{}, fmt.Errorf("create terminal Run Inbox item: %w", err)
	}
	return result, nil
}

// runInboxWorkTitle names the work a Run notice is about, as the recipient
// reads it: the Task's board label and title, or the chat's title for a Run
// with no Task. readable is false when the recipient can no longer read that
// Task or chat (ADR 0017): they get no notice, which would name work they
// cannot see.
func runInboxWorkTitle(
	ctx context.Context,
	queries *db.Queries,
	run db.Run,
	recipientUserID uuid.UUID,
) (string, bool, error) {
	if run.TaskID.Valid {
		task, err := queries.GetTaskForActiveMember(ctx, db.GetTaskForActiveMemberParams{
			ActorUserID: recipientUserID,
			TaskID:      run.TaskID.UUID,
			CompanyID:   run.CompanyID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("read Run Inbox task: %w", err)
		}
		// The label the board shows, not the key: a Company Plan key is a
		// slug the app never displays, and an older key can even be the
		// label of a different task.
		company, err := queries.GetCompanyByID(ctx, run.CompanyID)
		if err != nil {
			return "", false, fmt.Errorf("read Run Inbox company: %w", err)
		}
		return protocol.TaskLabel(company.TaskPrefix, task.Number) + " · " + task.Title, true, nil
	}
	conversation, err := queries.ResolveConversationStateForActiveUser(
		ctx,
		db.ResolveConversationStateForActiveUserParams{
			ActorUserID:    recipientUserID,
			ConversationID: run.ConversationID,
		},
	)
	// Likewise for a chat the recipient no longer reads, such as the Task
	// chat of a deleted Task in a Project they left.
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read Run Inbox conversation: %w", err)
	}
	return conversation.Title.String, true, nil
}

// createProjectAccessRevokedInboxItem tells the member a Run was stopped
// because they may no longer run tasks in its Project (ADR 0017). It is the
// run_failed shape an abandoned continuation uses for agent_not_bound: what
// happened to them is that the Agent's work for them ended and Zenith will
// not run it again. It is sent only while they still read the Run's Task or
// chat, so someone who lost read is told nothing about a Project now hidden
// from them; the Inbox's own Project check agrees. The item names the Agent
// and the Task they still read, never the Project.
func createProjectAccessRevokedInboxItem(
	ctx context.Context,
	queries *db.Queries,
	pending *[]events.Event,
	run db.Run,
	recipientUserID uuid.UUID,
) (inbox.CreateResult, error) {
	workTitle, readable, err := runInboxWorkTitle(ctx, queries, run, recipientUserID)
	if err != nil || !readable {
		return inbox.CreateResult{}, err
	}
	agent, err := queries.GetRunAgentBrief(
		ctx,
		db.GetRunAgentBriefParams{AgentID: run.AgentID, RuntimeID: run.RuntimeID},
	)
	if err != nil {
		return inbox.CreateResult{}, fmt.Errorf("read stopped Run Inbox agent: %w", err)
	}
	route, err := queries.GetConversationRoute(ctx, db.GetConversationRouteParams{
		ConversationID: run.ConversationID,
		CompanyID:      run.CompanyID,
	})
	if err != nil {
		return inbox.CreateResult{}, fmt.Errorf("read stopped Run Inbox route: %w", err)
	}
	result, err := inbox.CreateInTx(ctx, queries, pending,
		projectAccessRevokedInboxInput(run, recipientUserID, agent.Name, workTitle, route.Kind))
	if err != nil {
		return inbox.CreateResult{}, fmt.Errorf("create stopped Run Inbox item: %w", err)
	}
	return result, nil
}

// projectAccessRevokedInboxInput is the stopped-Run notice itself. It is
// built only from the Agent's name and the work title the recipient reads,
// so it has no way to name the Project.
func projectAccessRevokedInboxInput(
	run db.Run,
	recipientUserID uuid.UUID,
	agentName, workTitle, conversationKind string,
) inbox.CreateInput {
	agentName = runInboxLabel(agentName, 128)
	if strings.TrimSpace(workTitle) == "" {
		workTitle = agentName
	}
	deepLink := runInboxDeepLink(conversationKind, run)
	return inbox.CreateInput{
		CompanyID:       run.CompanyID,
		RecipientUserID: recipientUserID,
		ConversationID:  &run.ConversationID,
		Type:            inbox.TypeRunFailed,
		Entity:          inbox.Entity{Type: protocol.EntityRun, ID: run.ID.String()},
		Title:           runInboxLabel("Run stopped: "+workTitle, 160),
		Body: agentName + " stopped: your access to this work no longer includes running tasks. " +
			"Ask someone who manages this work to let you run tasks.",
		DeepLink:  &deepLink,
		DedupeKey: fmt.Sprintf("run:%s:%s", run.ID, RunReasonProjectAccessRevoked),
		Actor:     inbox.Actor{Kind: "system"},
	}
}

// runInboxAwaiting decides the notice for a completed Run from the Questions
// and Approvals it still waits on. A Run that stopped for a person has not
// finished its work, so it is never "ready to review". When the recipient
// holds every waiting request's own item, that item already asks them, and
// the continuation Run that carries the answer back sends the notice: none
// now. Otherwise nothing else tells them, so the notice says what the Run is
// waiting for.
func runInboxAwaiting(waits []db.ListRunHumanInputWaitsRow) (protocol.EntityType, bool) {
	for _, wait := range waits {
		if !wait.RecipientNotified {
			return protocol.EntityType(wait.EntityType), true
		}
	}
	return "", len(waits) == 0
}

func runInboxContent(status string, target runInboxTarget) (inbox.Type, string, string) {
	agentName := runInboxLabel(target.agentName, 128)
	workTitle := target.workTitle
	if strings.TrimSpace(workTitle) == "" {
		workTitle = agentName
	}
	itemType := inbox.TypeRunCompleted
	prefix := "Reply ready: "
	body := agentName + " finished its reply. Open the conversation to review it."
	if target.conversationKind == "task" {
		prefix = "Ready to review: "
		body = agentName + " finished its task run. Open the task to review the result."
	}
	if target.conversationKind == "schedule" {
		prefix = "Schedule finished: "
		body = agentName + " finished the scheduled run. Open the conversation to review the result."
	}
	if status == "completed" && target.awaiting != "" {
		turn, place := "its reply", "conversation"
		if target.conversationKind == "task" {
			turn, place = "its task run", "task"
		}
		if target.conversationKind == "schedule" {
			turn = "the scheduled run"
		}
		prefix = "Paused for approval: "
		wait := "to ask for an approval and continues once the request is decided"
		if target.awaiting == protocol.EntityQuestion {
			prefix = "Paused for an answer: "
			wait = "to ask a question and continues once it is answered"
		}
		body = agentName + " paused " + turn + " " + wait + ". Open the " + place + " to follow it."
	}
	if status == "failed" {
		itemType = inbox.TypeRunFailed
		prefix = "Reply failed: "
		body = agentName + " could not finish its reply. " + runInboxFailureWhy(target) +
			"Open the conversation to review the failure and retry."
		if target.conversationKind == "task" {
			prefix = "Run failed: "
			body = agentName + " could not finish its task run. " + runInboxFailureWhy(target) +
				"Open the task to review the failure and retry."
		}
		if target.conversationKind == "schedule" {
			prefix = "Schedule failed: "
			body = agentName + " could not finish the scheduled run. " + runInboxFailureWhy(target) +
				"Open the conversation to review the failure and retry."
		}
	}
	return itemType, runInboxLabel(prefix+workTitle, 160), body
}

// runInboxFailureWhy is the middle of a failed-run body: the provider-aware
// sentence for the reason, then the provider's own words when there are any.
// Both are bounded by pkg/runfailure, so the body stays inside the 2048-byte
// notification cap even with the longest agent name. A run with no classifier
// (a sweeper wrote it before classification existed) keeps the plain body.
func runInboxFailureWhy(target runInboxTarget) string {
	if target.failureReason == "" {
		return ""
	}
	why := runfailure.MessageFor(runfailure.Reason(target.failureReason), target.provider) + " "
	detail := runfailure.Detail(target.failureText)
	if detail == "" {
		return why
	}
	label := runfailure.DetailLabel(
		runfailure.Reason(target.failureReason), target.provider, target.failureText,
	)
	return why + label + ": " + detail + " "
}

// Notification copy is a compact label, never a prompt or provider response.
func runInboxLabel(value string, maximum int) string {
	label := []rune(strings.Join(strings.Fields(value), " "))
	if len(label) > maximum {
		return string(label[:maximum-1]) + "…"
	}
	return string(label)
}

func runInboxDeepLink(conversationKind string, run db.Run) string {
	query := url.Values{
		"agentId":          []string{run.AgentID.String()},
		"conversationId":   []string{run.ConversationID.String()},
		"conversationKind": []string{conversationKind},
		"runId":            []string{run.ID.String()},
	}
	if run.TaskID.Valid {
		query.Set("taskId", run.TaskID.UUID.String())
	}
	return "/app?" + query.Encode()
}
