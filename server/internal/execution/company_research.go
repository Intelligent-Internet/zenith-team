package execution

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

// companyResearchOverviewKey is the hub page the founder's research Run writes
// first; the notice opens it when it exists.
const companyResearchOverviewKey = "company-overview"

// companyResearchOutcome is what the terminal path learned about a Run that
// belongs to the Company's onboarding research (the receipt's research Run or
// one of its automatic-retry descendants). The zero value is any other Run.
type companyResearchOutcome struct {
	research bool
	// retrying is true while an automatic retry child exists: the attempt
	// failed but the research is still in progress, so nothing is final.
	retrying bool
	pages    int
	overview *uuid.UUID
}

// settleCompanyResearchInTx is the terminal hook for the onboarding research.
// In the same transaction as the Run's terminal write it marks the receipt
// complete when the Run is the research lineage's final attempt, so the
// completion never depends on a client being open. A retried failure is not
// final and changes nothing.
func settleCompanyResearchInTx(
	ctx context.Context,
	q *db.Queries,
	run db.Run,
	retrying bool,
) (companyResearchOutcome, error) {
	research, err := q.IsInitializationResearchRun(
		ctx,
		db.IsInitializationResearchRunParams{CompanyID: run.CompanyID, RunID: run.ID},
	)
	if err != nil {
		return companyResearchOutcome{}, fmt.Errorf("read company research lineage: %w", err)
	}
	if !research {
		return companyResearchOutcome{}, nil
	}
	outcome := companyResearchOutcome{research: true, retrying: retrying}
	if retrying {
		return outcome, nil
	}
	if _, err := q.CompleteCompanyInitializationForRun(
		ctx,
		db.CompleteCompanyInitializationForRunParams{CompanyID: run.CompanyID, RunID: run.ID},
	); err != nil {
		return companyResearchOutcome{}, fmt.Errorf("complete company initialization: %w", err)
	}
	outcome.pages, err = countResearchPages(ctx, q, run.CompanyID)
	if err != nil {
		return companyResearchOutcome{}, err
	}
	page, err := q.GetInitializationOverviewPage(ctx, db.GetInitializationOverviewPageParams{
		CompanyID: run.CompanyID, Key: companyResearchOverviewKey,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return companyResearchOutcome{}, fmt.Errorf("read company overview page: %w", err)
	default:
		outcome.overview = &page
	}
	return outcome, nil
}

// lockResearchInboxTargets resolves and locks the founder recipient of every
// research-lineage Run among runs, before the caller's Conversation locks, so a
// cancellation can notify the founder like any other terminal path.
func lockResearchInboxTargets(ctx context.Context, q *db.Queries, runs []db.Run) (map[uuid.UUID]runInboxTarget, error) {
	research := make([]db.Run, 0, 1)
	for _, run := range runs {
		isResearch, err := q.IsInitializationResearchRun(
			ctx,
			db.IsInitializationResearchRunParams{CompanyID: run.CompanyID, RunID: run.ID},
		)
		if err != nil {
			return nil, fmt.Errorf("read company research lineage: %w", err)
		}
		if isResearch {
			research = append(research, run)
		}
	}
	if len(research) == 0 {
		return nil, nil
	}
	return lockRunInboxTargets(ctx, q, research)
}

// settleCancelledResearchInTx is the terminal hook for a cancelled research
// Run. A cancellation never creates a retry child, so it is always the final
// attempt: the receipt completes and the founder is told the research could not
// finish. targets come from lockResearchInboxTargets.
func settleCancelledResearchInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	run db.Run,
	targets map[uuid.UUID]runInboxTarget,
) (inbox.CreateResult, error) {
	research, err := settleCompanyResearchInTx(ctx, q, run, false)
	if err != nil {
		return inbox.CreateResult{}, err
	}
	target, ok := targets[run.ID]
	if !research.research || !ok {
		return inbox.CreateResult{}, nil
	}
	return createRunTerminalInboxItem(ctx, q, pending, run, target, research)
}

func countResearchPages(ctx context.Context, q *db.Queries, companyID uuid.UUID) (int, error) {
	pages, err := q.CountInitializationWikiWrites(ctx, companyID)
	if err != nil {
		return 0, fmt.Errorf("count company research pages: %w", err)
	}
	return int(pages), nil
}

// createCompanyResearchInboxItem is the founder's notice that the research
// ended. It reuses run_completed/run_failed with copy and a Wiki deep link of
// its own, never the generic "Reply ready" text.
func createCompanyResearchInboxItem(
	ctx context.Context,
	queries *db.Queries,
	pending *[]events.Event,
	run db.Run,
	target runInboxTarget,
	outcome companyResearchOutcome,
) (inbox.CreateResult, error) {
	agent, err := queries.GetRunAgentBrief(
		ctx,
		db.GetRunAgentBriefParams{AgentID: run.AgentID, RuntimeID: run.RuntimeID},
	)
	if err != nil {
		return inbox.CreateResult{}, fmt.Errorf("read company research Inbox agent: %w", err)
	}
	target.agentName = agent.Name
	if run.Status == "failed" {
		target.failureReason = run.FailureReason.String
		target.failureText = run.Error.String
		runtime, err := queries.GetRunRuntime(
			ctx,
			db.GetRunRuntimeParams{CompanyID: run.CompanyID, ID: run.RuntimeID},
		)
		if err != nil {
			return inbox.CreateResult{}, fmt.Errorf("read company research Inbox runtime: %w", err)
		}
		target.provider = runtime.Provider
	}
	itemType, title, body, deepLink := companyResearchInboxContent(run.Status, target, outcome)
	actorID := run.AgentID
	result, err := inbox.CreateInTx(ctx, queries, pending, inbox.CreateInput{
		CompanyID:       run.CompanyID,
		RecipientUserID: target.recipientUserID,
		ConversationID:  &run.ConversationID,
		Type:            itemType,
		Entity:          inbox.Entity{Type: protocol.EntityRun, ID: run.ID.String()},
		Title:           title,
		Body:            body,
		DeepLink:        &deepLink,
		DedupeKey:       fmt.Sprintf("run:%s:%s", run.ID, run.Status),
		Actor:           inbox.Actor{Kind: "agent", ID: &actorID},
	})
	if err != nil {
		return inbox.CreateResult{}, fmt.Errorf("create company research Inbox item: %w", err)
	}
	return result, nil
}

// companyResearchInboxContent is the notice's type, title, body and deep link.
// Success opens the overview page, or the Wiki when the Run saved none;
// failure opens the Wiki, where the retry action lives. The links are the
// client's own routes: the Wiki is mounted at /wiki, not under /app.
func companyResearchInboxContent(
	status string,
	target runInboxTarget,
	outcome companyResearchOutcome,
) (inbox.Type, string, string, string) {
	agentName := runInboxLabel(target.agentName, 128)
	if status == "failed" || status == "cancelled" {
		return inbox.TypeRunFailed, "Company research could not finish",
			agentName + " could not finish researching your company. " + companyResearchFailureWhy(target) +
				"Open the Wiki to see what was saved and retry the research.",
			"/wiki"
	}
	deepLink := "/wiki"
	if outcome.overview != nil {
		deepLink += "/pages/" + outcome.overview.String()
	}
	body := agentName + " finished researching your company. Open the Wiki to review it."
	switch {
	case outcome.pages == 1:
		body = agentName + " wrote 1 page about your company. Open the Wiki to review it."
	case outcome.pages > 1:
		body = agentName + " wrote " + strconv.Itoa(
			outcome.pages,
		) + " pages about your company. Open the Wiki to review them."
	}
	return inbox.TypeRunCompleted, "Your company wiki is ready", body, deepLink
}

// companyResearchFailureWhy is the one plain sentence the founder gets for a
// failed research Run. A preparation failure on their computer is described by
// its phase alone, without the generic "could not classify" lead the Run
// taxonomy gives it; anything else keeps the ordinary failure sentence.
func companyResearchFailureWhy(target runInboxTarget) string {
	if runfailure.IsPreparationCode(target.failureText) {
		return runfailure.Detail(target.failureText) + " "
	}
	return runInboxFailureWhy(target)
}
