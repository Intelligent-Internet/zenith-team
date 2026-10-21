package company

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// RevokeAccountMembershipsInTx revokes the account's active memberships and
// stages the canonical Company-scoped removal plus personal access-revoked
// facts. The caller must already hold every named Company row in id order, so
// this participant only takes the membership rows that follow those locks.
func RevokeAccountMembershipsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	userID uuid.UUID,
	companyIDs []uuid.UUID,
) error {
	if q == nil || pending == nil || !util.ValidUUID(userID) {
		return ErrInvalidInput
	}
	for _, companyID := range companyIDs {
		if !util.ValidUUID(companyID) {
			return ErrInvalidInput
		}
	}
	rows, err := q.RevokeActiveMembershipsForAccountDeletion(
		ctx,
		generated.RevokeActiveMembershipsForAccountDeletionParams{
			UserID:     userID,
			CompanyIds: companyIDs,
		},
	)
	if err != nil {
		return fmt.Errorf("revoke account Company memberships: %w", err)
	}
	// Project grants go with each membership (ADR 0017). Deleting them
	// updates Project rows, so every Company's grants go before any
	// Conversation lock below (company -> member -> project -> conversation).
	// The system acts; each project_grant row is addressed to the account.
	for _, row := range rows {
		if err := revokeProjectGrantsInTx(ctx, q, pending, projectGrantAuthor{
			companyID: row.CompanyID, actor: events.SystemActor(), recipient: userID,
		}, userID); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if err := conversation.RevokeDiscussionMembersInTx(
			ctx,
			q,
			pending,
			conversation.DiscussionUserRevocation{CompanyID: row.CompanyID, UserID: userID},
		); err != nil {
			return err
		}
		if err := stageMembershipEventsAs(
			pending,
			events.SystemActor(),
			memberResponse(generated.CompanyMember(row)),
			ActiveMemberResponse{},
			MembershipRemoved,
		); err != nil {
			return fmt.Errorf("stage account membership removal: %w", err)
		}
	}
	return nil
}
