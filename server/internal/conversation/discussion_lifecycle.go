package conversation

import (
	"context"

	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type DiscussionUserRevocation struct{ CompanyID, UserID, ActorUserID uuid.UUID }

// RevokeDiscussionMembersInTx follows the caller's exclusive Company fence.
// Account deletion has already locked all Companies after its enabled-user row.
// Conversations are locked in UUID order before any grant, so send either commits
// first or observes the revoked membership; no user-row lock follows Company.
func RevokeDiscussionMembersInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input DiscussionUserRevocation,
) error {
	rows, err := q.LockDiscussionsForUserRevocation(
		ctx,
		generated.LockDiscussionsForUserRevocationParams{CompanyID: input.CompanyID, UserID: input.UserID},
	)
	if err != nil {
		return err
	}
	actorID := uuid.NullUUID{UUID: input.ActorUserID, Valid: input.ActorUserID != uuid.Nil}
	for _, row := range rows {
		revoked, err := q.RevokeDiscussionMember(
			ctx,
			generated.RevokeDiscussionMemberParams{
				ConversationID: row.ID,
				UserID:         input.UserID,
				ActorUserID:    actorID,
			},
		)
		if err != nil {
			return err
		}
		*pending = append(*pending, discussionRevokeEvent(revoked))
		if row.Kind == "channel" {
			count, err := q.CountActiveDiscussionMembers(ctx, row.ID)
			if err != nil {
				return err
			}
			if count == 0 && !row.ArchivedAt.Valid && !row.IsGeneral {
				if err = q.SetChannelMetadata(
					ctx,
					generated.SetChannelMetadataParams{
						CompanyID:      input.CompanyID,
						ConversationID: row.ID,
						Archived:       pgtype.Bool{Bool: true, Valid: true},
					},
				); err != nil {
					return err
				}
			}
			var actor *uuid.UUID
			if actorID.Valid {
				actor = &input.ActorUserID
			}
			if err = recordChannelSystem(
				ctx,
				q,
				pending,
				discussionSystemChange{
					CompanyID:      input.CompanyID,
					ConversationID: row.ID,
					ActorUserID:    actor,
					SubjectUserID:  &input.UserID,
					Action:         "member_removed",
				},
			); err != nil {
				return err
			}
		} else {
			if _, err = finishDiscussionChange(
				ctx,
				q,
				pending,
				discussionChange{
					CompanyID:      input.CompanyID,
					ConversationID: row.ID,
					Actor:          events.SystemActor(),
				},
			); err != nil {
				return err
			}
		}
	}
	return nil
}

type DiscussionAgentRevocation struct{ CompanyID, AgentID uuid.UUID }

// RevokeDiscussionAgentsInTx runs while the Agent owner holds its Company and
// Agent locks. A restore never resurrects these grants; re-add advances generation.
func RevokeDiscussionAgentsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input DiscussionAgentRevocation,
) error {
	rows, err := q.LockDiscussionsForAgentRevocation(
		ctx,
		generated.LockDiscussionsForAgentRevocationParams{CompanyID: input.CompanyID, AgentID: input.AgentID},
	)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err = q.RevokeDiscussionAgent(
			ctx,
			generated.RevokeDiscussionAgentParams{ConversationID: row.ID, AgentID: input.AgentID},
		); err != nil {
			return err
		}
		if row.Kind == "human_direct" {
			if _, err = finishDiscussionChange(ctx, q, pending, discussionChange{
				CompanyID: input.CompanyID, ConversationID: row.ID, Actor: events.SystemActor(),
			}); err != nil {
				return err
			}
			continue
		}
		if err = recordChannelSystem(
			ctx,
			q,
			pending,
			discussionSystemChange{
				CompanyID:      input.CompanyID,
				ConversationID: row.ID,
				SubjectAgentID: &input.AgentID,
				Action:         "agent_removed",
			},
		); err != nil {
			return err
		}
	}
	return nil
}
