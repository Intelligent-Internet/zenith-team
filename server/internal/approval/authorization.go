package approval

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// lockActiveHumanInputMember takes the canonical Company then membership
// locks. They are separate statements so a waiter gets a fresh READ COMMITTED
// snapshot of the membership after a concurrent account deletion commits.
func lockActiveHumanInputMember(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID uuid.UUID,
) (auth.CompanyRole, bool, error) {
	if _, err := q.LockCompanyForMembershipMutation(ctx, companyID); errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	} else if err != nil {
		return "", false, fmt.Errorf("lock Company for human-input mutation: %w", err)
	}
	membership, err := q.GetCompanyMemberForUpdate(
		ctx,
		generated.GetCompanyMemberForUpdateParams{
			CompanyID: companyID,
			UserID:    userID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lock human-input actor membership: %w", err)
	}
	if membership.Status != "active" {
		return "", false, nil
	}
	return auth.CompanyRole(membership.Role), true, nil
}

// lockLiveHumanInputOrigin extends the active-member fence through the exact
// Conversation and currently-running Run represented by the earlier token
// lookup. A terminal transition or account deletion that commits first makes
// the stale token fail here before a request row can be read or inserted.
func lockLiveHumanInputOrigin(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
) (auth.CompanyRole, error) {
	role, err := lockHumanInputOriginMember(ctx, q, principal, origin)
	if err != nil {
		return "", err
	}
	return role, lockHumanInputOriginChat(ctx, q, origin)
}

// lockHumanInputOriginMember is the first half of lockLiveHumanInputOrigin:
// the Company and member locks of the person the Run works for. A caller
// that must take Project locks takes them next, before
// lockHumanInputOriginChat, since a Project lock never follows a
// Conversation or Run lock.
func lockHumanInputOriginMember(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
) (auth.CompanyRole, error) {
	role, active, err := lockActiveHumanInputMember(
		ctx,
		q,
		principal.CompanyID,
		humanInputProjectionActor(principal, origin),
	)
	if err != nil {
		return "", err
	}
	if !active {
		return "", execution.ErrRunProofInvalid
	}
	return role, nil
}

// lockHumanInputOriginChat is the second half of lockLiveHumanInputOrigin:
// the origin Conversation and live Run locks, after the member locks.
func lockHumanInputOriginChat(
	ctx context.Context,
	q *generated.Queries,
	origin execution.RunOrigin,
) error {
	if origin.ConversationKind == "channel_agent" {
		if _, err := q.LockConversationForSystemRunQueueMutation(ctx, generated.LockConversationForSystemRunQueueMutationParams{CompanyID: origin.CompanyID, ConversationID: origin.ConversationID}); err != nil {
			return err
		}
	}
	if _, err := q.LockHumanInputOriginConversation(
		ctx,
		generated.LockHumanInputOriginConversationParams{
			ConversationID:   origin.ConversationID,
			CompanyID:        origin.CompanyID,
			ConversationKind: origin.ConversationKind,
			TaskID:           util.NullUUIDFromPtr(origin.TaskID),
			AgentID:          origin.AgentID,
			OriginUserID:     util.NullUUIDFromPtr(origin.OriginUserID),
		},
	); errors.Is(err, pgx.ErrNoRows) {
		return execution.ErrRunProofInvalid
	} else if err != nil {
		return fmt.Errorf("lock human-input origin Conversation: %w", err)
	}
	if _, err := q.LockLiveHumanInputOriginRun(
		ctx,
		generated.LockLiveHumanInputOriginRunParams{
			RunID:          origin.RunID,
			CompanyID:      origin.CompanyID,
			AgentID:        origin.AgentID,
			TaskID:         util.NullUUIDFromPtr(origin.TaskID),
			ConversationID: origin.ConversationID,
		},
	); errors.Is(err, pgx.ErrNoRows) {
		return execution.ErrRunProofInvalid
	} else if err != nil {
		return fmt.Errorf("lock live human-input origin Run: %w", err)
	}
	if origin.ConversationKind == "channel_agent" {
		run, err := q.GetRun(ctx, origin.RunID)
		if err != nil {
			return err
		}
		err = execution.AuthorizeChannelInvocationInTx(ctx, q, execution.RunTrigger{CompanyID: origin.CompanyID, ConversationID: origin.ConversationID, AgentID: origin.AgentID, InvocationID: run.InvocationID})
		if errors.Is(err, execution.ErrChannelInvocationDenied) || errors.Is(err, execution.ErrRunAgentNotBound) {
			return execution.ErrRunProofInvalid
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// companyPlanApproversInTx returns who may approve a Company Plan Approval a
// Task Run raised in its Task chat with a Project: the Project's readers
// holding project.approvals.resolve and project.tasks.write (ADR 0017).
// Approving runs the frozen apply as the decider in that Project alone, so
// these are the permissions it needs; the Company role plays no part. They
// are the Approval's recipients and the eligibility test for raising it; a
// reader holding only project.approvals.resolve may still reject it.
func companyPlanApproversInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID uuid.UUID,
) ([]uuid.UUID, error) {
	deciders, err := auth.ProjectDecidersInTx(ctx, q, companyID, projectID, auth.ProjectPermissionApprovalsResolve)
	if err != nil || len(deciders) == 0 {
		return deciders, err
	}
	writers, err := auth.ProjectDecidersInTx(ctx, q, companyID, projectID, auth.ProjectPermissionTasksWrite)
	if err != nil {
		return nil, err
	}
	return intersectSorted(deciders, writers), nil
}

// intersectSorted keeps the ids of want (sorted) that also appear in among.
func intersectSorted(want, among []uuid.UUID) []uuid.UUID {
	kept := make([]uuid.UUID, 0, len(want))
	for _, id := range want {
		if slices.Contains(among, id) {
			kept = append(kept, id)
		}
	}
	return kept
}

// endRunWithoutResolverInTx ends the raising Run when nobody may decide what
// it raised (ADR 0017): the Run fails with reason no_eligible_resolver, its
// Task takes the terminal transition, and its member gets the run_failed
// Inbox item, which the caller presents after commit. The caller commits
// this and then answers auth.ErrNoEligibleResolver.
func endRunWithoutResolverInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, runID uuid.UUID,
) (inbox.CreateResult, error) {
	run, notice, err := execution.FailRunForNoEligibleResolverInTx(ctx, q, pending, companyID, runID)
	if err != nil || run == nil {
		return notice, err
	}
	return notice, task.ApplyRunTerminalInTx(ctx, q, pending, *run)
}

func humanInputProjectionActor(principal auth.UserPrincipal, origin execution.RunOrigin) uuid.UUID {
	if origin.ConversationKind == "channel_agent" && origin.OriginUserID != nil {
		return *origin.OriginUserID
	}
	return principal.UserID
}
