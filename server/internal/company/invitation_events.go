package company

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// invitationAudience is the routing fact for the invitation surface: the
// active members whose role holds members.invite, read inside the mutation
// transaction so the audience is the one the committed change is for.
func invitationAudience(ctx context.Context, q *generated.Queries, companyID uuid.UUID) ([]uuid.UUID, error) {
	recipients, err := q.ListActiveCompanyMemberIDsByRoles(ctx, generated.ListActiveCompanyMemberIDsByRolesParams{
		CompanyID: companyID,
		Roles:     auth.RolesWith(auth.PermissionMembersInvite),
	})
	if err != nil {
		return nil, fmt.Errorf("list invitation audience: %w", err)
	}
	return recipients, nil
}

// stageInvitationChanged stages invitation.changed for the invitation surface.
func stageInvitationChanged(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	actor events.Actor,
	invitation InvitationResponse,
	companyID uuid.UUID,
) error {
	recipients, err := invitationAudience(ctx, q, companyID)
	if err != nil {
		return err
	}
	*pending = append(*pending, events.Event{
		EntityType: TopicInvitation,
		Name:       EventInvitationChanged,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      actor,
		Payload:    InvitationChanged{InvitationResponse: invitation, RecipientUserIDs: recipients},
	})
	return nil
}

// stageShareLinkChanged stages share_link.changed for the invitation surface.
// The URL never travels on the frame; a client that may see it refetches.
func stageShareLinkChanged(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	actor events.Actor,
	link ShareLinkResponse,
	companyID uuid.UUID,
) error {
	recipients, err := invitationAudience(ctx, q, companyID)
	if err != nil {
		return err
	}
	*pending = append(*pending, events.Event{
		EntityType: TopicInvitation,
		Name:       EventShareLinkChanged,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      actor,
		Payload: ShareLinkChanged{
			Role:             link.Role,
			ExpiresAt:        link.ExpiresAt,
			Revision:         link.Revision,
			Available:        link.Available,
			RecipientUserIDs: recipients,
		},
	})
	return nil
}
