package conversation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func normalizeCreateChannel(input CreateChannelInput, creator uuid.UUID) (CreateChannelInput, []byte, error) {
	name, err := normalizeChannelName(input.Name)
	if err != nil {
		return input, nil, err
	}
	input.Name = name
	if input.RequestKey == uuid.Nil || !utf8.ValidString(input.Topic) || len(input.Topic) > 2048 ||
		strings.ContainsRune(input.Topic, 0) ||
		!validDiscussionIDs(input.MemberUserIDs, 49) ||
		!validDiscussionIDs(input.AgentIDs, 20) ||
		slices.Contains(input.MemberUserIDs, creator) {
		return input, nil, ErrDiscussionInvalid
	}
	input.MemberUserIDs = append([]uuid.UUID{}, input.MemberUserIDs...)
	input.AgentIDs = append([]uuid.UUID{}, input.AgentIDs...)
	compare := func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }
	slices.SortFunc(input.MemberUserIDs, compare)
	slices.SortFunc(input.AgentIDs, compare)
	canonical, _ := json.Marshal(struct {
		Version int         `json:"version"`
		Name    string      `json:"name"`
		Topic   string      `json:"topic"`
		Members []uuid.UUID `json:"members"`
		Agents  []uuid.UUID `json:"agents"`
	}{1, input.Name, input.Topic, input.MemberUserIDs, input.AgentIDs})
	digest := sha256.Sum256(canonical)
	return input, digest[:], nil
}

func (s *DefaultConversationService) CreateChannel(
	ctx context.Context,
	p auth.UserPrincipal,
	input CreateChannelInput,
) (ChannelReceipt, error) {
	input, fingerprint, err := normalizeCreateChannel(input, p.UserID)
	if err != nil {
		return ChannelReceipt{}, err
	}
	for range 3 {
		var result ChannelReceipt
		var pending []events.Event
		err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			role, err := lockDiscussionMembership(ctx, q, p)
			if err != nil {
				return err
			}
			if !role.Can(auth.PermissionChannelsManage) {
				return ErrDiscussionForbidden
			}
			existing, err := q.GetDiscussionCreationReceipt(
				ctx,
				generated.GetDiscussionCreationReceiptParams{
					CompanyID:   p.CompanyID,
					ActorUserID: p.UserID,
					RequestKey:  input.RequestKey,
				},
			)
			if err == nil {
				if existing.Kind != "channel" || !bytes.Equal(existing.CreateFingerprint, fingerprint) {
					return ErrDiscussionRequestConflict
				}
				if err = lockDiscussion(ctx, q, p, existing.ID); err != nil {
					return err
				}
				row, err := currentDiscussion(ctx, q, p, existing.ID, false)
				if err != nil {
					return err
				}
				result = ChannelReceipt{
					Conversation: discussionSummary(row, p.UserID),
					RequestKey:   input.RequestKey,
					Replayed:     true,
				}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err = lockDiscussionTargets(ctx, q, p.CompanyID, input.MemberUserIDs); err != nil {
				return err
			}
			agents, err := q.LockDiscussionEligibleAgents(
				ctx,
				generated.LockDiscussionEligibleAgentsParams{
					CompanyID: p.CompanyID,
					AgentIds:  input.AgentIDs,
				},
			)
			if err != nil {
				return err
			}
			if len(agents) != len(input.AgentIDs) {
				return ErrDiscussionInvalid
			}
			row, err := q.InsertDiscussionConversation(
				ctx,
				generated.InsertDiscussionConversationParams{
					CompanyID:   p.CompanyID,
					ActorUserID: p.UserID,
					Kind:        "channel",
					Title:       pgtype.Text{String: input.Name, Valid: true},
					RequestKey:  uuid.NullUUID{UUID: input.RequestKey, Valid: true},
				},
			)
			if err != nil {
				return err
			}
			if err = q.InsertChannelDetail(
				ctx,
				generated.InsertChannelDetailParams{
					CompanyID:         p.CompanyID,
					ConversationID:    row.ID,
					Topic:             input.Topic,
					CreateFingerprint: fingerprint,
				},
			); err != nil {
				return err
			}
			for _, userID := range append([]uuid.UUID{p.UserID}, input.MemberUserIDs...) {
				if err = grantDiscussionHuman(
					ctx,
					q,
					discussionGrant{
						CompanyID:      p.CompanyID,
						ConversationID: row.ID,
						UserID:         userID,
						ActorUserID:    p.UserID,
						SourceType:     "channel",
					},
				); err != nil {
					return err
				}
			}
			for _, agentID := range input.AgentIDs {
				if _, err = q.GrantDiscussionAgent(
					ctx,
					generated.GrantDiscussionAgentParams{
						CompanyID:      p.CompanyID,
						ConversationID: row.ID,
						AgentID:        agentID,
						ActorUserID:    uuid.NullUUID{UUID: p.UserID, Valid: true},
					},
				); err != nil {
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
				row.ID,
			); err != nil {
				return err
			}
			pending = append(pending, discussionChangedEvent(row))
			access, err := currentDiscussion(ctx, q, p, row.ID, false)
			if err != nil {
				return err
			}
			result = ChannelReceipt{
				Conversation: discussionSummary(access, p.UserID),
				RequestKey:   input.RequestKey,
			}
			return nil
		})
		if discussionConflict(err) {
			continue
		}
		if err != nil {
			return ChannelReceipt{}, err
		}
		publishDiscussionEvents(ctx, s.bus, pending)
		return result, nil
	}
	return ChannelReceipt{}, ErrDiscussionUnavailable
}

type channelMutation struct {
	ID               uuid.UUID
	ExpectedRevision int64
	Leave            bool
	UserTargets      []uuid.UUID
	AgentTargets     []uuid.UUID
}

func (s *DefaultConversationService) mutateChannel(
	ctx context.Context,
	p auth.UserPrincipal,
	input channelMutation,
	mutate func(*generated.Queries, generated.ListDiscussionAccessRow, *[]events.Event) error,
) (ChannelManagement, error) {
	if !discussionRevisionValid(input.ExpectedRevision) {
		return ChannelManagement{}, ErrDiscussionInvalid
	}
	var result ChannelManagement
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		role, err := lockDiscussionMembership(ctx, q, p)
		if err != nil {
			return err
		}
		// Target Company memberships and Agents precede the Conversation lock.
		if err = lockDiscussionTargets(ctx, q, p.CompanyID, input.UserTargets); err != nil {
			return err
		}
		if len(input.AgentTargets) > 0 {
			agents, err := q.LockDiscussionEligibleAgents(
				ctx,
				generated.LockDiscussionEligibleAgentsParams{
					CompanyID: p.CompanyID,
					AgentIds:  input.AgentTargets,
				},
			)
			if err != nil {
				return err
			}
			if len(agents) != len(input.AgentTargets) {
				return ErrDiscussionInvalid
			}
		}
		if err = lockDiscussion(ctx, q, p, input.ID); err != nil {
			return err
		}
		access, err := currentDiscussion(
			ctx,
			q,
			p,
			input.ID,
			!input.Leave && role.Can(auth.PermissionChannelsManage),
		)
		if err != nil {
			return err
		}
		if access.Kind != "channel" {
			return ErrDiscussionNotFound
		}
		if !input.Leave && !role.Can(auth.PermissionChannelsManage) {
			return ErrDiscussionForbidden
		}
		if access.ConfigurationRevision.Int64 != input.ExpectedRevision {
			return ErrDiscussionRevisionConflict
		}
		if err = mutate(q, access, &pending); err != nil {
			return err
		}
		if input.Leave {
			receipt, err := q.GetChannelMutationReceipt(
				ctx,
				generated.GetChannelMutationReceiptParams{
					CompanyID:      p.CompanyID,
					ConversationID: input.ID,
				},
			)
			if err != nil {
				return err
			}
			access.Title = receipt.Title
			access.ArchivedAt = receipt.ArchivedAt
			access.Topic = pgtype.Text{String: receipt.Topic, Valid: true}
			access.ConfigurationRevision = pgtype.Int8{Int64: receipt.ConfigurationRevision, Valid: true}
			result, err = channelManagement(ctx, q, access)
			return err
		}
		updated, err := currentDiscussion(ctx, q, p, input.ID, true)
		if err != nil {
			return err
		}
		result, err = channelManagement(ctx, q, updated)
		return err
	})
	if err != nil {
		return ChannelManagement{}, err
	}
	publishDiscussionEvents(ctx, s.bus, pending)
	return result, nil
}

func (s *DefaultConversationService) UpdateChannel(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input ChannelUpdateInput,
) (ChannelManagement, error) {
	if input.Name == nil && input.Topic == nil {
		return ChannelManagement{}, ErrDiscussionInvalid
	}
	if input.Name != nil {
		name, err := normalizeChannelName(*input.Name)
		if err != nil {
			return ChannelManagement{}, err
		}
		input.Name = &name
	}
	if input.Topic != nil &&
		(!utf8.ValidString(*input.Topic) || len(*input.Topic) > 2048 || strings.ContainsRune(*input.Topic, 0)) {
		return ChannelManagement{}, ErrDiscussionInvalid
	}
	return s.mutateChannel(
		ctx,
		p,
		channelMutation{ID: id, ExpectedRevision: input.ExpectedRevision},
		func(q *generated.Queries, row generated.ListDiscussionAccessRow, pending *[]events.Event) error {
			if row.ArchivedAt.Valid {
				return ErrDiscussionReadOnly
			}
			if row.IsGeneral && input.Name != nil && *input.Name != row.Title.String {
				return ErrDiscussionForbidden
			}
			if (input.Name == nil || *input.Name == row.Title.String) &&
				(input.Topic == nil || *input.Topic == row.Topic.String) {
				return nil
			}
			if err := q.SetChannelMetadata(
				ctx,
				generated.SetChannelMetadataParams{
					CompanyID:      p.CompanyID,
					ConversationID: id,
					Title:          util.PtrToText(input.Name),
				},
			); err != nil {
				return err
			}
			// recordChannelSystem advances configuration once; topic is written
			// separately without a second revision increment.
			if input.Topic != nil {
				if err := q.SetChannelTopic(
					ctx,
					generated.SetChannelTopicParams{
						CompanyID:      p.CompanyID,
						ConversationID: id,
						Topic:          *input.Topic,
					},
				); err != nil {
					return err
				}
			}
			return recordChannelSystem(
				ctx,
				q,
				pending,
				discussionSystemChange{
					CompanyID:      p.CompanyID,
					ConversationID: id,
					ActorUserID:    &p.UserID,
					Action:         "channel_updated",
				},
			)
		},
	)
}

func (s *DefaultConversationService) SetChannelMember(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input ChannelMemberInput,
) (ChannelManagement, error) {
	if input.UserID == uuid.Nil {
		return ChannelManagement{}, ErrDiscussionInvalid
	}
	mutation := channelMutation{
		ID:               id,
		ExpectedRevision: input.ExpectedRevision,
		Leave:            !input.Active && input.UserID == p.UserID,
	}
	if input.Active {
		mutation.UserTargets = []uuid.UUID{input.UserID}
	}
	return s.mutateChannel(
		ctx,
		p,
		mutation,
		func(q *generated.Queries, row generated.ListDiscussionAccessRow, pending *[]events.Event) error {
			return changeChannelMember(
				ctx,
				q,
				pending,
				channelMemberChange{
					CompanyID:      p.CompanyID,
					ConversationID: id,
					ActorUserID:    p.UserID,
					UserID:         input.UserID,
					Active:         input.Active,
					Leave:          mutation.Leave,
					Access:         row,
				},
			)
		},
	)
}

type channelMemberChange struct {
	CompanyID, ConversationID, ActorUserID, UserID uuid.UUID
	Active, Leave                                  bool
	Access                                         generated.ListDiscussionAccessRow
}

func changeChannelMember(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input channelMemberChange,
) error {
	if input.Access.IsGeneral {
		return ErrDiscussionForbidden
	}
	if input.Access.ArchivedAt.Valid && !input.Leave {
		return ErrDiscussionReadOnly
	}
	existing, err := q.GetDiscussionMember(
		ctx,
		generated.GetDiscussionMemberParams{ConversationID: input.ConversationID, UserID: input.UserID},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	active := err == nil && existing.Status == "active"
	if input.Leave && !active {
		return ErrDiscussionNotFound
	}
	if active == input.Active {
		return nil
	}
	action := "member_removed"
	if input.Active {
		if input.Access.MemberCount >= 50 {
			return ErrDiscussionLimit
		}
		if err = grantDiscussionHuman(
			ctx,
			q,
			discussionGrant{
				CompanyID:      input.CompanyID,
				ConversationID: input.ConversationID,
				UserID:         input.UserID,
				ActorUserID:    input.ActorUserID,
				SourceType:     "channel",
				HeadSeq:        input.Access.HeadSeq,
			},
		); err != nil {
			return err
		}
		action = "member_added"
	} else {
		revoked, err := q.RevokeDiscussionMember(
			ctx,
			generated.RevokeDiscussionMemberParams{
				ConversationID: input.ConversationID,
				UserID:         input.UserID,
				ActorUserID:    uuid.NullUUID{UUID: input.ActorUserID, Valid: true},
			},
		)
		if err != nil {
			return err
		}
		*pending = append(*pending, discussionRevokeEvent(revoked))
		if input.Access.MemberCount == 1 {
			if err = q.SetChannelMetadata(
				ctx,
				generated.SetChannelMetadataParams{
					CompanyID:      input.CompanyID,
					ConversationID: input.ConversationID,
					Archived:       pgtype.Bool{Bool: true, Valid: true},
				},
			); err != nil {
				return err
			}
		}
	}
	return recordChannelSystem(
		ctx,
		q,
		pending,
		discussionSystemChange{
			CompanyID:      input.CompanyID,
			ConversationID: input.ConversationID,
			ActorUserID:    &input.ActorUserID,
			SubjectUserID:  &input.UserID,
			Action:         action,
		},
	)
}

func (s *DefaultConversationService) LeaveChannel(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	expectedRevision int64,
) error {
	_, err := s.mutateChannel(
		ctx,
		p,
		channelMutation{ID: id, ExpectedRevision: expectedRevision, Leave: true},
		func(q *generated.Queries, row generated.ListDiscussionAccessRow, pending *[]events.Event) error {
			return changeChannelMember(
				ctx,
				q,
				pending,
				channelMemberChange{
					CompanyID:      p.CompanyID,
					ConversationID: id,
					ActorUserID:    p.UserID,
					UserID:         p.UserID,
					Leave:          true,
					Access:         row,
				},
			)
		},
	)
	return err
}

func (s *DefaultConversationService) SetChannelAgent(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input ChannelAgentInput,
) (ChannelManagement, error) {
	if input.AgentID == uuid.Nil {
		return ChannelManagement{}, ErrDiscussionInvalid
	}
	mutation := channelMutation{ID: id, ExpectedRevision: input.ExpectedRevision}
	if input.Active {
		mutation.AgentTargets = []uuid.UUID{input.AgentID}
	}
	return s.mutateChannel(
		ctx,
		p,
		mutation,
		func(q *generated.Queries, row generated.ListDiscussionAccessRow, pending *[]events.Event) error {
			if row.ArchivedAt.Valid {
				return ErrDiscussionReadOnly
			}
			existing, err := q.GetDiscussionAgent(
				ctx,
				generated.GetDiscussionAgentParams{ConversationID: id, AgentID: input.AgentID},
			)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if (err == nil && existing.Status == "active") == input.Active {
				return nil
			}
			action := "agent_removed"
			if input.Active {
				if row.AgentCount >= 20 {
					return ErrDiscussionLimit
				}
				_, err = q.GrantDiscussionAgent(
					ctx,
					generated.GrantDiscussionAgentParams{
						CompanyID:      p.CompanyID,
						ConversationID: id,
						AgentID:        input.AgentID,
						ActorUserID:    uuid.NullUUID{UUID: p.UserID, Valid: true},
					},
				)
				action = "agent_added"
			} else {
				_, err = q.RevokeDiscussionAgent(
					ctx,
					generated.RevokeDiscussionAgentParams{
						ConversationID: id,
						AgentID:        input.AgentID,
						ActorUserID:    uuid.NullUUID{UUID: p.UserID, Valid: true},
					},
				)
			}
			if err != nil {
				return err
			}
			return recordChannelSystem(
				ctx,
				q,
				pending,
				discussionSystemChange{
					CompanyID:      p.CompanyID,
					ConversationID: id,
					ActorUserID:    &p.UserID,
					SubjectAgentID: &input.AgentID,
					Action:         action,
				},
			)
		},
	)
}

func (s *DefaultConversationService) SetChannelArchived(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input ChannelArchiveInput,
) (ChannelManagement, error) {
	return s.mutateChannel(
		ctx,
		p,
		channelMutation{ID: id, ExpectedRevision: input.ExpectedRevision},
		func(q *generated.Queries, row generated.ListDiscussionAccessRow, pending *[]events.Event) error {
			if row.IsGeneral {
				return ErrDiscussionForbidden
			}
			if row.ArchivedAt.Valid == input.Archived {
				return nil
			}
			action := "channel_archived"
			if !input.Archived {
				action = "channel_restored"
				if row.MemberCount == 0 {
					if err := grantDiscussionHuman(
						ctx,
						q,
						discussionGrant{
							CompanyID:      p.CompanyID,
							ConversationID: id,
							UserID:         p.UserID,
							ActorUserID:    p.UserID,
							SourceType:     "channel",
							HeadSeq:        row.HeadSeq,
						},
					); err != nil {
						return err
					}
				}
			}
			if err := q.SetChannelMetadata(
				ctx,
				generated.SetChannelMetadataParams{
					CompanyID:      p.CompanyID,
					ConversationID: id,
					Archived:       pgtype.Bool{Bool: input.Archived, Valid: true},
				},
			); err != nil {
				return err
			}
			return recordChannelSystem(
				ctx,
				q,
				pending,
				discussionSystemChange{
					CompanyID:      p.CompanyID,
					ConversationID: id,
					ActorUserID:    &p.UserID,
					Action:         action,
				},
			)
		},
	)
}
