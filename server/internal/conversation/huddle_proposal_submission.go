package conversation

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// LockHuddleProposalInTx participates in a human-confirmed Task mutation.
// The caller locks Company and acting membership first and owns commit.
func LockHuddleProposalInTx(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, huddleID, proposalID uuid.UUID) (generated.HuddleWorkProposal, HuddleProposal, error) {
	h, role, err := lockHuddleAIActor(ctx, q, p, huddleID)
	if err != nil {
		return generated.HuddleWorkProposal{}, HuddleProposal{}, err
	}
	if h.StartedByUserID != p.UserID && !role.Can(auth.PermissionCompanyProfileUpdate) {
		return generated.HuddleWorkProposal{}, HuddleProposal{}, ErrHuddleForbidden
	}
	row, err := q.GetHuddleProposal(ctx, generated.GetHuddleProposalParams{CompanyID: p.CompanyID, HuddleID: huddleID, ID: proposalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, HuddleProposal{}, ErrHuddleNotFound
	}
	if err != nil {
		return row, HuddleProposal{}, err
	}
	return row, huddleProposalProjection(row, h, 0, false, true), nil
}

// RecordHuddleProposalSubmissionInTx saves the receipt beside the Task.
func RecordHuddleProposalSubmissionInTx(ctx context.Context, q *generated.Queries, in generated.SubmitHuddleProposalParams) (HuddleProposal, error) {
	row, err := q.SubmitHuddleProposal(ctx, in)
	if err != nil {
		return HuddleProposal{}, err
	}
	return huddleProposalProjection(row, generated.Huddle{}, 0, false, false), nil
}
