package conversation

import (
	"bytes"
	"context"
	"errors"
	"slices"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *DefaultConversationService) InviteHumanDirectMembers(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input InviteHumanDirectInput,
) (HumanDirectDetail, error) {
	if len(input.MemberUserIDs)+len(input.AgentIDs) == 0 ||
		!validDiscussionIDs(input.MemberUserIDs, 50) || !validDiscussionIDs(input.AgentIDs, 20) {
		return HumanDirectDetail{}, ErrDiscussionInvalid
	}
	var result HumanDirectDetail
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		role, err := lockDiscussionMembership(ctx, q, p)
		if err != nil {
			return err
		}
		if !role.Can(auth.PermissionConversationsDiscuss) {
			return ErrDiscussionForbidden
		}
		if err = lockDiscussionTargets(ctx, q, p.CompanyID, input.MemberUserIDs); err != nil {
			return err
		}
		agents, err := q.LockDiscussionEligibleAgents(ctx, generated.LockDiscussionEligibleAgentsParams{
			CompanyID: p.CompanyID,
			AgentIds:  append([]uuid.UUID{}, input.AgentIDs...),
		})
		if err != nil {
			return err
		}
		if len(agents) != len(input.AgentIDs) {
			return ErrDiscussionInvalid
		}
		if err = lockDiscussion(ctx, q, p, id); err != nil {
			return err
		}
		row, err := currentDiscussion(ctx, q, p, id, false)
		if err != nil {
			return err
		}
		if row.Kind != "human_direct" {
			return ErrDiscussionNotFound
		}
		if row.UserLowID.Valid {
			return ErrDiscussionForbidden
		}
		identity, err := q.GetHumanDirectIdentity(ctx, generated.GetHumanDirectIdentityParams{CompanyID: p.CompanyID, ConversationID: id})
		if err != nil {
			return err
		}
		humans := mergeDirectParticipants(identity.ParticipantIds, input.MemberUserIDs)
		agentIDs := mergeDirectParticipants(identity.AgentIds, input.AgentIDs)
		if len(humans) > 50 || len(agentIDs) > 20 {
			return ErrDiscussionLimit
		}
		if len(humans) != len(identity.ParticipantIds) || len(agentIDs) != len(identity.AgentIds) {
			if err = q.ExpandHumanDirectParticipants(ctx, generated.ExpandHumanDirectParticipantsParams{
				CompanyID: p.CompanyID, ConversationID: id,
				ParticipantIds: humans, AgentIds: agentIDs,
			}); err != nil {
				return err
			}
		}
		var addedUsers, addedAgents []uuid.UUID
		for _, userID := range input.MemberUserIDs {
			previous, err := q.GetDiscussionMember(ctx, generated.GetDiscussionMemberParams{ConversationID: id, UserID: userID})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil && previous.Status == "active" {
				continue
			}
			if _, err = grantDiscussionHuman(ctx, q, discussionGrant{
				CompanyID: p.CompanyID, ConversationID: id,
				UserID: userID, ActorUserID: p.UserID,
				SourceType: "human_direct", HeadSeq: row.HeadSeq,
			}); err != nil {
				return err
			}
			addedUsers = append(addedUsers, userID)
		}
		for _, agentID := range input.AgentIDs {
			previous, err := q.GetDiscussionAgent(ctx, generated.GetDiscussionAgentParams{ConversationID: id, AgentID: agentID})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil && previous.Status == "active" {
				continue
			}
			if _, err = q.GrantDiscussionAgent(ctx, generated.GrantDiscussionAgentParams{
				CompanyID: p.CompanyID, ConversationID: id, AgentID: agentID,
				ActorUserID: uuid.NullUUID{UUID: p.UserID, Valid: true},
			}); err != nil {
				return err
			}
			addedAgents = append(addedAgents, agentID)
		}
		if len(addedUsers)+len(addedAgents) > 0 {
			if err = insertDiscussionSystem(ctx, q, p.CompanyID, id, DiscussionSystem{
				Action: "members_added", ActorUserID: &p.UserID,
				SubjectUserIDs: addedUsers, SubjectAgentIDs: addedAgents,
			}); err != nil {
				return err
			}

			if _, err = finishDiscussionChange(ctx, q, &pending, discussionChange{CompanyID: p.CompanyID, ConversationID: id, Actor: events.UserActor(p.UserID.String())}); err != nil {
				return err
			}
		}
		row, err = currentDiscussion(ctx, q, p, id, false)
		if err != nil {
			return err
		}
		result.Conversation = discussionSummary(row, p.UserID)
		result.Members, result.Agents, err = discussionRoster(ctx, q, p.CompanyID, id)
		return err
	})
	if err != nil {
		return HumanDirectDetail{}, err
	}
	publishDiscussionEvents(ctx, s.bus, pending)
	return result, nil
}

func mergeDirectParticipants(existing, added []uuid.UUID) []uuid.UUID {
	ids := append(append([]uuid.UUID{}, existing...), added...)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(ids)
}
