package conversation

import (
	"bytes"
	"context"
	"errors"
	"slices"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *DefaultConversationService) CreateHumanDirect(
	ctx context.Context,
	p auth.UserPrincipal,
	input CreateHumanDirectInput,
) (HumanDirectResult, error) {
	peerIDs := input.PeerUserIDs
	if len(peerIDs)+len(input.AgentIDs) == 0 || !validDiscussionIDs(peerIDs, 49) ||
		!validDiscussionIDs(input.AgentIDs, 20) || slices.Contains(peerIDs, p.UserID) {
		return HumanDirectResult{}, ErrDiscussionInvalid
	}
	participants := append([]uuid.UUID{p.UserID}, peerIDs...)
	slices.SortFunc(participants, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	agentIDs := append([]uuid.UUID{}, input.AgentIDs...)
	slices.SortFunc(agentIDs, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	for range 3 {
		retry := false
		var result HumanDirectResult
		var pending []events.Event
		err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			role, err := lockDiscussionMembership(ctx, q, p)
			if err != nil {
				return err
			}
			if !role.Can(auth.PermissionConversationsDiscuss) {
				return ErrDiscussionForbidden
			}
			id, err := q.GetHumanDirectParticipants(
				ctx,
				generated.GetHumanDirectParticipantsParams{
					CompanyID:      p.CompanyID,
					ParticipantIds: participants,
					AgentIds:       agentIDs,
				},
			)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			exists := err == nil
			eligible := map[uuid.UUID]bool{p.UserID: true}
			var eligibleErr error
			for _, userID := range participants {
				if userID == p.UserID {
					continue
				}
				err := lockDiscussionTargets(ctx, q, p.CompanyID, []uuid.UUID{userID})
				if err != nil && !errors.Is(err, ErrDiscussionInvalid) {
					return err
				}
				if err != nil {
					eligibleErr = err
				} else {
					eligible[userID] = true
				}
			}
			agents, err := q.LockDiscussionEligibleAgents(
				ctx,
				generated.LockDiscussionEligibleAgentsParams{
					CompanyID: p.CompanyID,
					AgentIds:  agentIDs,
				},
			)
			if err != nil {
				return err
			}
			if !exists && len(agents) != len(agentIDs) {
				return ErrDiscussionInvalid
			}
			if !exists {
				if eligibleErr != nil {
					return eligibleErr
				}
				row, err := q.InsertDiscussionConversation(
					ctx,
					generated.InsertDiscussionConversationParams{
						CompanyID:   p.CompanyID,
						ActorUserID: p.UserID,
						Kind:        "human_direct",
					},
				)
				if err != nil {
					return err
				}
				id = row.ID
				if err = q.InsertHumanDirectParticipants(
					ctx,
					generated.InsertHumanDirectParticipantsParams{
						CompanyID:      p.CompanyID,
						ConversationID: id,
						ParticipantIds: participants,
						AgentIds:       agentIDs,
					},
				); err != nil {
					return err
				}
				for _, userID := range participants {
					if err = grantDiscussionHuman(
						ctx,
						q,
						discussionGrant{
							CompanyID:      p.CompanyID,
							ConversationID: id,
							UserID:         userID,
							ActorUserID:    p.UserID,
							SourceType:     "human_direct",
						},
					); err != nil {
						return err
					}
				}
				for _, agent := range agents {
					if _, err = q.GrantDiscussionAgent(ctx, generated.GrantDiscussionAgentParams{
						CompanyID: p.CompanyID, ConversationID: id, AgentID: agent.ID,
						ActorUserID: uuid.NullUUID{UUID: p.UserID, Valid: true},
					}); err != nil {
						return err
					}
				}
				if err = activity.RecordInTx(
					ctx,
					q,
					&pending,
					events.UserActor(p.UserID.String()),
					EventConversationCreated,
					nil,
					Topic,
					id,
				); err != nil {
					return err
				}
				pending = append(pending, discussionChangedEvent(row))
				result.Created = true
			} else {
				if err = lockDiscussion(ctx, q, p, id); err != nil {
					return err
				}
				identity, err := q.GetHumanDirectIdentity(
					ctx,
					generated.GetHumanDirectIdentityParams{
						CompanyID:      p.CompanyID,
						ConversationID: id,
					},
				)
				if err != nil {
					return err
				}
				// Invitation may have expanded this group after the initial lookup.
				if identity.MembershipExpanded {
					retry = true
					return nil
				}
				if eligibleErr == nil || len(participants) > 2 || len(agentIDs) > 0 {
					row, err := q.LockDiscussionConversation(
						ctx,
						generated.LockDiscussionConversationParams{
							CompanyID:      p.CompanyID,
							ConversationID: id,
						},
					)
					if err != nil {
						return err
					}
					changed := false
					for _, userID := range participants {
						if !eligible[userID] {
							continue
						}
						previous, err := q.GetDiscussionMember(
							ctx,
							generated.GetDiscussionMemberParams{
								ConversationID: id,
								UserID:         userID,
							},
						)
						if err != nil {
							return err
						}
						if previous.Status == "active" {
							continue
						}
						if err = grantDiscussionHuman(
							ctx,
							q,
							discussionGrant{
								CompanyID:      p.CompanyID,
								ConversationID: id,
								UserID:         userID,
								ActorUserID:    p.UserID,
								SourceType:     "human_direct",
								HeadSeq:        row.HeadSeq,
							},
						); err != nil {
							return err
						}
						changed = true
					}
					for _, agent := range agents {
						previous, err := q.GetDiscussionAgent(
							ctx,
							generated.GetDiscussionAgentParams{
								ConversationID: id,
								AgentID:        agent.ID,
							},
						)
						if err != nil {
							return err
						}
						if previous.Status == "active" {
							continue
						}
						if _, err = q.GrantDiscussionAgent(
							ctx,
							generated.GrantDiscussionAgentParams{
								CompanyID:      p.CompanyID,
								ConversationID: id,
								AgentID:        agent.ID,
								ActorUserID: uuid.NullUUID{
									UUID:  p.UserID,
									Valid: true,
								},
							},
						); err != nil {
							return err
						}
						changed = true
					}
					if changed {
						if _, err = finishDiscussionChange(
							ctx,
							q,
							&pending,
							discussionChange{
								CompanyID:      p.CompanyID,
								ConversationID: id,
								Actor:          events.UserActor(p.UserID.String()),
							},
						); err != nil {
							return err
						}
					}
				}
			}
			row, err := currentDiscussion(ctx, q, p, id, false)
			if err != nil {
				return err
			}
			result.Conversation = discussionSummary(row, p.UserID)
			return nil
		})
		if retry || discussionConflict(err) {
			continue
		}
		if err != nil {
			return HumanDirectResult{}, err
		}
		publishDiscussionEvents(ctx, s.bus, pending)
		return result, nil
	}
	return HumanDirectResult{}, ErrDiscussionUnavailable
}

type discussionGrant struct {
	CompanyID, ConversationID, UserID, ActorUserID uuid.UUID
	SourceType                                     string
	HeadSeq                                        int64
}

func grantDiscussionHuman(
	ctx context.Context,
	q *generated.Queries,
	input discussionGrant,
) error {
	existing, err := q.GetDiscussionMember(
		ctx,
		generated.GetDiscussionMemberParams{ConversationID: input.ConversationID, UserID: input.UserID},
	)
	if err == nil && existing.Status == "active" {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	row, err := q.GrantDiscussionMember(
		ctx,
		generated.GrantDiscussionMemberParams{
			CompanyID:      input.CompanyID,
			ConversationID: input.ConversationID,
			UserID:         input.UserID,
			SourceType:     input.SourceType,
			JoinedSeq:      input.HeadSeq,
			ActorUserID:    uuid.NullUUID{UUID: input.ActorUserID, Valid: input.ActorUserID != uuid.Nil},
		},
	)
	if err != nil {
		return err
	}
	return q.ResetDiscussionUserState(
		ctx,
		generated.ResetDiscussionUserStateParams{
			CompanyID:       input.CompanyID,
			ConversationID:  input.ConversationID,
			UserID:          input.UserID,
			GrantGeneration: row.Generation,
		},
	)
}
