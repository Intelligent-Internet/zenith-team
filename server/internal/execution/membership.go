package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

// RuntimeOwnerRevocation identifies the Company-scoped execution authority.
type RuntimeOwnerRevocation struct {
	CompanyID uuid.UUID
	UserID    uuid.UUID
}

// CancelRuntimeOwnerRunsInTx revokes execution on machines whose owner just
// lost Company access. The caller holds the Company and membership locks.
func CancelRuntimeOwnerRunsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input RuntimeOwnerRevocation,
) ([]generated.Run, error) {
	candidates, err := q.ListActiveRunsForRuntimeOwner(
		ctx,
		generated.ListActiveRunsForRuntimeOwnerParams{CompanyID: input.CompanyID, OwnerUserID: input.UserID},
	)
	if err != nil {
		return nil, fmt.Errorf("list removed member runtime Runs: %w", err)
	}
	return cancelRunCandidatesInTx(ctx, q, pending, candidates, "runtime_owner_inactive")
}

// RunReasonProjectAccessRevoked is the reason a member's Runs on a Project
// end with when they lose project.read or project.tasks.execute there
// (ADR 0017): a grant downgrade or removal, or a Project made restricted.
const RunReasonProjectAccessRevoked = "project_access_revoked"

// ProjectAccessRevocation names one member who lost the right to read or
// run work in one Project.
type ProjectAccessRevocation struct {
	CompanyID uuid.UUID
	ProjectID uuid.UUID
	UserID    uuid.UUID
}

// CancelProjectAccessRevokedRunsInTx ends the member's queued, deferred,
// claimed and executing Runs on the Project's chats, with reason
// project_access_revoked: tokens deleted and run.cancelled staged, as every
// cancellation pass does. The Tasks stay assigned to the member.
//
// The member is told, one run_failed Inbox item per stopped Run, while they
// still read its Task or chat: they lost only project.tasks.execute, so the
// item names the Agent and the Task and says they can no longer run tasks in
// this work. Someone who lost project.read is told nothing, because the
// Project is hidden from them now. The items are written in this
// transaction; the caller presents the returned results after commit with
// inbox.PresentCreated.
//
// The caller holds the Company lock FOR UPDATE, the membership and Project
// locks (in that order), owns the transaction and applies the Task terminal
// transition to each returned Run. The member's Inbox lock is taken here,
// before the Conversation locks; it can come after a Project or Conversation
// lock the caller already holds because every change to a membership waits
// on that Company lock first, as FailRunForNoEligibleResolverInTx relies on.
func CancelProjectAccessRevokedRunsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input ProjectAccessRevocation,
) ([]generated.Run, []inbox.CreateResult, error) {
	candidates, err := q.ListActiveRunsForProjectAuthorizer(ctx, generated.ListActiveRunsForProjectAuthorizerParams{
		CompanyID: input.CompanyID,
		ProjectID: input.ProjectID,
		UserID:    input.UserID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list Project Runs for access revocation: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil, nil
	}
	notify, err := inbox.LockRecipientInTx(ctx, q, input.CompanyID, input.UserID)
	if err != nil {
		return nil, nil, err
	}
	cancelled, err := cancelRunCandidatesInTx(
		ctx,
		q,
		pending,
		candidates,
		RunReasonProjectAccessRevoked,
	)
	if err != nil || !notify {
		return cancelled, nil, err
	}
	var notices []inbox.CreateResult
	for _, run := range cancelled {
		notice, err := createProjectAccessRevokedInboxItem(ctx, q, pending, run, input.UserID)
		if err != nil {
			return nil, nil, err
		}
		if notice.Created {
			notices = append(notices, notice)
		}
	}
	return cancelled, notices, nil
}

// FailRunForNoEligibleResolverInTx fails the Run whose raise found no
// eligible resolver: a Task Run raised an Approval or Question that no reader
// of its Task chat's Project may decide (ADR 0017).
// The raise itself answers 409 no_eligible_resolver. The Run fails with
// failure_reason no_eligible_resolver, which is never retried: its tokens are
// deleted, run.failed is staged, and its authorizer — the member it worked
// for — gets the run_failed Inbox item with the reason's provider-aware
// sentence, unless they no longer read the Task. The failure writes no
// provider text into runs.error: nothing went wrong with the provider.
//
// The caller holds the Company lock (FOR UPDATE) and the raising member's
// membership lock — every route that takes a Run proof checks its session
// is the Run's authorizer (handler.runProofBearer) — so the
// recipient lock the Inbox takes here, after the Conversation lock the raise
// already holds, cannot wait on another transaction. The caller owns the
// transaction, applies the Task terminal transition to the returned Run and
// presents the returned Inbox result after commit. A nil Run means the Run
// had already ended.
func FailRunForNoEligibleResolverInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, runID uuid.UUID,
) (*generated.Run, inbox.CreateResult, error) {
	run, err := q.GetRunInCompany(ctx, generated.GetRunInCompanyParams{ID: runID, CompanyID: companyID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, inbox.CreateResult{}, nil
	}
	if err != nil {
		return nil, inbox.CreateResult{}, fmt.Errorf("read Run without an eligible resolver: %w", err)
	}
	targets, err := lockRunInboxTargets(ctx, q, []generated.Run{run})
	if err != nil {
		return nil, inbox.CreateResult{}, err
	}
	if err := lockTerminalRunConversation(ctx, q, run); err != nil {
		return nil, inbox.CreateResult{}, err
	}
	before, err := ObserveRunsInTx(ctx, q, []uuid.UUID{run.ID})
	if err != nil {
		return nil, inbox.CreateResult{}, err
	}
	failed, err := q.FailRun(ctx, generated.FailRunParams{
		ID:            run.ID,
		FailureReason: runfailure.ReasonNoEligibleResolver.String(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, inbox.CreateResult{}, nil
	}
	if err != nil {
		return nil, inbox.CreateResult{}, fmt.Errorf("fail Run without an eligible resolver: %w", err)
	}
	if err := recordRunChangeInTx(
		ctx,
		q,
		pending,
		events.SystemActor(),
		EventRunFailed,
		before[failed.ID],
		failed,
	); err != nil {
		return nil, inbox.CreateResult{}, err
	}
	if err := q.DeleteRunTokensByRun(ctx, failed.ID); err != nil {
		return nil, inbox.CreateResult{}, err
	}
	slog.Info(
		"run failed",
		"run_id", failed.ID,
		"conversation_id", failed.ConversationID.String(),
		"failure_reason", runfailure.ReasonNoEligibleResolver.String(),
	)
	target, ok := targets[failed.ID]
	if !ok {
		return &failed, inbox.CreateResult{}, nil
	}
	research, err := settleCompanyResearchInTx(ctx, q, failed, false)
	if err != nil {
		return nil, inbox.CreateResult{}, err
	}
	notice, err := createRunTerminalInboxItem(ctx, q, pending, failed, target, research)
	if err != nil {
		return nil, inbox.CreateResult{}, err
	}
	return &failed, notice, nil
}

// cancelRunCandidatesInTx is the cancellation pass the runtime revocations,
// binding changes and Task handoffs share. candidates arrive ordered by
// conversation then id; every conversation is locked before any Run is
// written, so the pass takes its locks in the same order as every other
// terminal transition.
func cancelRunCandidatesInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	candidates []generated.Run,
	reason string,
) ([]generated.Run, error) {
	researchTargets, err := lockResearchInboxTargets(ctx, q, candidates)
	if err != nil {
		return nil, err
	}
	for _, run := range candidates {
		if err := lockDocumentRunLifecycle(ctx, q, run); err != nil {
			return nil, err
		}
	}
	var last uuid.UUID
	for _, run := range candidates {
		if run.ConversationID == last {
			continue
		}
		if err := lockTerminalRunConversation(ctx, q, run); err != nil {
			return nil, err
		}
		last = run.ConversationID
	}
	var cancelled []generated.Run
	for _, candidate := range candidates {
		before, err := ObserveRunsInTx(ctx, q, []uuid.UUID{candidate.ID})
		if err != nil {
			return nil, err
		}
		run, err := q.CancelRun(
			ctx,
			generated.CancelRunParams{ID: candidate.ID, Reason: reason},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := recordRunChangeInTx(
			ctx,
			q,
			pending,
			events.SystemActor(),
			EventRunCancelled,
			before[run.ID],
			run,
		); err != nil {
			return nil, err
		}
		if err := q.DeleteRunTokensByRun(ctx, run.ID); err != nil {
			return nil, err
		}
		// The line a single cancel writes, with the pass's reason; logged in
		// the transaction like "run enqueued".
		slog.Info(
			"run cancelled",
			"run_id", run.ID,
			"conversation_id", run.ConversationID.String(),
			"reason", reason,
		)
		// A cancelled research Run is final: complete the receipt and tell
		// the founder it could not finish. The notice's realtime events ride
		// pending; this shared pass has no push presenter.
		if _, err := settleCancelledResearchInTx(ctx, q, pending, run, researchTargets); err != nil {
			return nil, err
		}
		// recordRunChangeInTx staged the run.cancelled frame beside the
		// Activity row; the pass adds no second one.
		cancelled = append(cancelled, run)
	}
	return cancelled, nil
}
