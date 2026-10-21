package conversation

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// DiscussionPresenceInput combines the current client's activity heartbeat and
// a bounded Company roster read. Manual Away is shared by all devices.
type DiscussionPresenceInput struct {
	Active     bool
	ManualAway *bool
	UserIDs    []uuid.UUID
}
type DiscussionMemberPresence struct {
	UserID     uuid.UUID `json:"userId"`
	Online     bool      `json:"online"`
	ManualAway bool      `json:"manualAway"`
}

func (s *DefaultDiscussionService) Presence(
	ctx context.Context,
	p auth.UserPrincipal,
	input DiscussionPresenceInput,
) ([]DiscussionMemberPresence, error) {
	if len(input.UserIDs) > 50 {
		return nil, ErrDiscussionLimit
	}
	for _, id := range input.UserIDs {
		if id == uuid.Nil {
			return nil, ErrDiscussionInvalid
		}
	}
	result := []DiscussionMemberPresence{}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockCompanyForMembershipRead(ctx, p.CompanyID); err != nil {
			return discussionLookupError(err)
		}
		if _, err := q.GetCompanyMemberForUpdate(
			ctx,
			generated.GetCompanyMemberForUpdateParams{CompanyID: p.CompanyID, UserID: p.UserID},
		); err != nil {
			return discussionLookupError(err)
		}
		if _, err := lockDiscussionMembership(ctx, q, p); err != nil {
			return err
		}
		away := pgtype.Bool{}
		if input.ManualAway != nil {
			away = pgtype.Bool{Bool: *input.ManualAway, Valid: true}
		}
		if input.Active || input.ManualAway != nil {
			if err := q.TouchCompanyMemberPresence(
				ctx,
				generated.TouchCompanyMemberPresenceParams{
					CompanyID:  p.CompanyID,
					UserID:     p.UserID,
					Active:     input.Active,
					ManualAway: away,
				},
			); err != nil {
				return err
			}
		}

		rows, err := q.ListCompanyMemberPresence(
			ctx,
			generated.ListCompanyMemberPresenceParams{
				CompanyID: p.CompanyID,
				UserIds:   append(input.UserIDs, p.UserID),
			},
		)
		if err != nil {
			return err
		}
		for _, row := range rows {
			result = append(
				result,
				DiscussionMemberPresence{
					UserID:     row.UserID,
					Online:     row.Online,
					ManualAway: row.ManualAway,
				},
			)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update member presence: %w", err)
	}
	return result, nil
}
