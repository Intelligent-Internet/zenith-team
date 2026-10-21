package conversation

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type GeneralChannelMembership struct{ CompanyID, UserID uuid.UUID }

// JoinGeneralChannelInTx follows the caller's exclusive Company membership fence.
// Creation and membership grants commit with the Company/join, never afterwards.
func JoinGeneralChannelInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input GeneralChannelMembership,
) error {
	row, err := q.GetGeneralChannelForUpdate(ctx, input.CompanyID)
	created := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !created {
		return err
	}
	users := []uuid.UUID{input.UserID}
	if created {
		row, err = q.CreateGeneralChannel(ctx, input.CompanyID)
		if err != nil {
			return err
		}
		if err = q.InsertChannelDetail(
			ctx,
			generated.InsertChannelDetailParams{
				CompanyID:         input.CompanyID,
				ConversationID:    row.ID,
				CreateFingerprint: make([]byte, 32),
			},
		); err != nil {
			return err
		}
		users, err = q.ListGeneralChannelEligibleUsers(ctx, input.CompanyID)
		if err != nil {
			return err
		}
	}
	changed := false
	for _, userID := range users {
		previous, err := q.GetDiscussionMember(
			ctx,
			generated.GetDiscussionMemberParams{ConversationID: row.ID, UserID: userID},
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && previous.Status == "active" {
			continue
		}
		if err = grantDiscussionHuman(
			ctx,
			q,
			discussionGrant{
				CompanyID:      input.CompanyID,
				ConversationID: row.ID,
				UserID:         userID,
				SourceType:     "channel",
				HeadSeq:        row.HeadSeq,
			},
		); err != nil {
			return err
		}
		changed = true
	}
	if created {
		if err := activity.RecordInTx(
			ctx,
			q,
			pending,
			events.SystemActor(),
			EventConversationCreated,
			nil,
			Topic,
			row.ID,
		); err != nil {
			return err
		}
		*pending = append(*pending, discussionChangedEvent(row))
		return nil
	}
	if !changed {
		return nil
	}
	return recordChannelSystem(
		ctx,
		q,
		pending,
		discussionSystemChange{
			CompanyID:      input.CompanyID,
			ConversationID: row.ID,
			SubjectUserID:  &input.UserID,
			Action:         "member_added",
		},
	)
}
