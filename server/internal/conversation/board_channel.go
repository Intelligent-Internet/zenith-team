package conversation

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// A Board's chat is one channel per Board that the people who can read the
// Board share. Its people follow Board access: they join when they open the
// chat or are mentioned in it, and Sync removes them after an access change
// commits. Its Agents are the Agents invited to the Board. Until Sync runs,
// the readers view, ListDiscussionAccess and tag authority all check Board
// access live, so a lost Board is lost in its chat at once.
//
// Chat paths here never lock the Board, and Board writers never lock a chat
// or a Run: an Agent drawing on the Board holds its Run while it waits for
// the Board, so taking both orders would deadlock.

// BoardChannelService opens a Board's chat and keeps its people and Agents
// in step with the Board.
type BoardChannelService interface {
	// Channel reads the Board's chat and its people without joining it.
	Channel(context.Context, auth.UserPrincipal, uuid.UUID) (BoardChannel, error)
	// Join starts the Board's chat the first time, joins the caller, and
	// brings its people and Agents in step with the Board.
	Join(context.Context, auth.UserPrincipal, uuid.UUID) (BoardChannel, error)
	// Sync brings the Board's chat in step with the Board after its access
	// changed; a Board with no chat is left alone.
	Sync(ctx context.Context, companyID, boardID uuid.UUID) error
}

// BoardChannel is a Board's chat as its reader sees it: the chat, once
// started, and the people who can read the Board, to mention.
type BoardChannel struct {
	BoardID         uuid.UUID            `json:"boardId"`
	ConversationID  *uuid.UUID           `json:"conversationId"`
	Joined          bool                 `json:"joined"`
	People          []BoardChannelPerson `json:"people"`
	PeopleTruncated bool                 `json:"peopleTruncated"`
}

type BoardChannelPerson struct {
	UserID      uuid.UUID `json:"userId"`
	DisplayName string    `json:"displayName"`
	AvatarURL   *string   `json:"avatarUrl"`
	Joined      bool      `json:"joined"`
}

// boardChannelPeopleLimit bounds the people a Board's chat lists to mention.
const boardChannelPeopleLimit = 500

type BoardChannelServiceOptions struct {
	Store *db.Store
	Bus   *events.Bus
}

type DefaultBoardChannelService struct {
	store *db.Store
	bus   *events.Bus
}

var _ BoardChannelService = (*DefaultBoardChannelService)(nil)

func NewBoardChannelService(options BoardChannelServiceOptions) (*DefaultBoardChannelService, error) {
	if options.Store == nil || options.Bus == nil {
		return nil, errors.New("board channel store and event bus are required")
	}
	return &DefaultBoardChannelService{store: options.Store, bus: options.Bus}, nil
}

func (s *DefaultBoardChannelService) Channel(
	ctx context.Context,
	p auth.UserPrincipal,
	boardID uuid.UUID,
) (BoardChannel, error) {
	var result BoardChannel
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			if err := requireBoardChannelReader(ctx, q, p, boardID); err != nil {
				return err
			}
			var channel uuid.NullUUID
			id, err := q.GetBoardChannelID(
				ctx,
				generated.GetBoardChannelIDParams{CompanyID: p.CompanyID, BoardID: boardID},
			)
			if err == nil {
				channel = uuid.NullUUID{UUID: id, Valid: true}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			result, err = boardChannelView(ctx, q, p, boardID, channel)
			return err
		},
	)
	return result, err
}

func (s *DefaultBoardChannelService) Join(
	ctx context.Context,
	p auth.UserPrincipal,
	boardID uuid.UUID,
) (BoardChannel, error) {
	var result BoardChannel
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = pending[:0]
		role, err := lockDiscussionMembership(ctx, q, p)
		if err != nil {
			return err
		}
		if !role.Can(auth.PermissionWhiteboardsRead) {
			return ErrDiscussionNotFound
		}
		if err = requireBoardChannelReader(ctx, q, p, boardID); err != nil {
			return err
		}
		agents, err := lockBoardChannelAgents(ctx, q, p.CompanyID, boardID)
		if err != nil {
			return err
		}
		channel, created, err := lockOrStartBoardChannel(ctx, q, &pending, p, boardID)
		if err != nil {
			return err
		}
		// Access read before the chat lock may be stale; read it again now
		// that a concurrent Sync has to wait for this transaction.
		if err = requireBoardChannelReader(ctx, q, p, boardID); err != nil {
			return err
		}
		changed := created
		member, err := q.GetDiscussionMember(
			ctx,
			generated.GetDiscussionMemberParams{ConversationID: channel.ID, UserID: p.UserID},
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err != nil || member.Status != "active" {
			if _, err = grantDiscussionHuman(ctx, q, discussionGrant{
				CompanyID: p.CompanyID, ConversationID: channel.ID, UserID: p.UserID,
				SourceType: "channel", HeadSeq: channel.HeadSeq, NotificationLevel: "mentions",
			}); err != nil {
				return err
			}
			changed = true
		}
		reconciled, err := reconcileBoardChannelInTx(ctx, q, &pending, channel, boardID, agents)
		if err != nil {
			return err
		}
		if changed || reconciled {
			if err = finishBoardChannelChange(
				ctx,
				q,
				&pending,
				channel,
				events.UserActor(p.UserID.String()),
			); err != nil {
				return err
			}
		}
		result, err = boardChannelView(ctx, q, p, boardID, uuid.NullUUID{UUID: channel.ID, Valid: true})
		return err
	})
	if err != nil {
		return BoardChannel{}, err
	}
	publishDiscussionEvents(ctx, s.bus, pending)
	return result, nil
}

func (s *DefaultBoardChannelService) Sync(ctx context.Context, companyID, boardID uuid.UUID) error {
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = pending[:0]
		if _, err := q.LockCompanyForMembershipRead(ctx, companyID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		if _, err := q.GetBoardChannelID(
			ctx,
			generated.GetBoardChannelIDParams{CompanyID: companyID, BoardID: boardID},
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		agents, err := lockBoardChannelAgents(ctx, q, companyID, boardID)
		if err != nil {
			return err
		}
		channel, err := q.LockBoardChannel(
			ctx,
			generated.LockBoardChannelParams{CompanyID: companyID, BoardID: boardID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		changed, err := reconcileBoardChannelInTx(ctx, q, &pending, channel, boardID, agents)
		if err != nil || !changed {
			return err
		}
		return finishBoardChannelChange(ctx, q, &pending, channel, events.SystemActor())
	})
	if err != nil {
		return err
	}
	publishDiscussionEvents(ctx, s.bus, pending)
	return nil
}

// requireBoardChannelReader answers ErrDiscussionNotFound for someone who
// cannot read the Board.
func requireBoardChannelReader(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	boardID uuid.UUID,
) error {
	readers, err := q.BoardChannelReaders(ctx, generated.BoardChannelReadersParams{
		CompanyID: p.CompanyID, BoardID: boardID, UserIds: []uuid.UUID{p.UserID},
	})
	if err != nil {
		return err
	}
	if len(readers) == 0 {
		return ErrDiscussionNotFound
	}
	return nil
}

// lockBoardChannelAgents locks the Agents invited to the Board, as a
// channel's Agent changes lock them before the chat.
func lockBoardChannelAgents(
	ctx context.Context,
	q *generated.Queries,
	companyID, boardID uuid.UUID,
) ([]uuid.UUID, error) {
	granted, err := q.ListBoardChannelAgentGrants(
		ctx,
		generated.ListBoardChannelAgentGrantsParams{CompanyID: companyID, BoardID: boardID},
	)
	if err != nil || len(granted) == 0 {
		return nil, err
	}
	// A restored Agent can take a Board past the limit it invites to; the
	// chat keeps the first ones.
	if len(granted) > canvas.BoardAgentLimit {
		granted = granted[:canvas.BoardAgentLimit]
	}
	locked, err := q.LockDiscussionEligibleAgents(
		ctx,
		generated.LockDiscussionEligibleAgentsParams{CompanyID: companyID, AgentIds: granted},
	)
	if err != nil {
		return nil, err
	}
	agents := make([]uuid.UUID, 0, len(locked))
	for _, agent := range locked {
		agents = append(agents, agent.ID)
	}
	return agents, nil
}

// lockOrStartBoardChannel locks the Board's chat, starting it the first time.
func lockOrStartBoardChannel(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	p auth.UserPrincipal,
	boardID uuid.UUID,
) (generated.Conversation, bool, error) {
	key := generated.LockBoardChannelParams{CompanyID: p.CompanyID, BoardID: boardID}
	channel, err := q.LockBoardChannel(ctx, key)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return channel, false, err
	}
	channel, err = q.InsertBoardChannel(ctx, generated.InsertBoardChannelParams{
		CompanyID: p.CompanyID, BoardID: boardID, ActorUserID: p.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Someone else started it first, or the Board is gone.
		channel, err = q.LockBoardChannel(ctx, key)
		return channel, false, discussionLookupError(err)
	}
	if err != nil {
		return channel, false, err
	}
	if err = q.InsertChannelDetail(ctx, generated.InsertChannelDetailParams{
		CompanyID: p.CompanyID, ConversationID: channel.ID, CreateFingerprint: make([]byte, 32),
	}); err != nil {
		return channel, false, err
	}
	if err = activity.RecordInTx(
		ctx, q, pending, events.UserActor(p.UserID.String()), EventConversationCreated, nil, Topic, channel.ID,
	); err != nil {
		return channel, false, err
	}
	return channel, true, nil
}

// reconcileBoardChannelInTx removes the chat's people who can no longer read
// the Board and makes its Agents the Agents invited to the Board. It reports
// whether anything changed; the caller finishes the change.
func reconcileBoardChannelInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	channel generated.Conversation,
	boardID uuid.UUID,
	agents []uuid.UUID,
) (bool, error) {
	changed := false
	stale, err := q.ListBoardChannelStaleMembers(
		ctx,
		generated.ListBoardChannelStaleMembersParams{ConversationID: channel.ID, BoardID: boardID},
	)
	if err != nil {
		return false, err
	}
	for _, userID := range stale {
		revoked, err := q.RevokeDiscussionMember(
			ctx,
			generated.RevokeDiscussionMemberParams{ConversationID: channel.ID, UserID: userID},
		)
		if err != nil {
			return false, err
		}
		*pending = append(*pending, discussionRevokeEvent(revoked))
		changed = true
	}
	roster, err := q.ListDiscussionActiveAgents(ctx, channel.ID)
	if err != nil {
		return false, err
	}
	for _, agentID := range agents {
		if slices.Contains(roster, agentID) {
			continue
		}
		if _, err = q.GrantDiscussionAgent(ctx, generated.GrantDiscussionAgentParams{
			CompanyID: channel.CompanyID, ConversationID: channel.ID, AgentID: agentID,
		}); err != nil {
			return false, err
		}
		changed = true
	}
	for _, agentID := range roster {
		if slices.Contains(agents, agentID) {
			continue
		}
		if _, err = q.RevokeDiscussionAgent(
			ctx,
			generated.RevokeDiscussionAgentParams{ConversationID: channel.ID, AgentID: agentID},
		); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// finishBoardChannelChange records a change to the chat's people or Agents:
// clients refetch it, and tags whose caller or Agent left are cancelled.
func finishBoardChannelChange(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	channel generated.Conversation,
	actor events.Actor,
) error {
	if _, err := q.UpdateChannelConfiguration(
		ctx,
		generated.UpdateChannelConfigurationParams{CompanyID: channel.CompanyID, ConversationID: channel.ID},
	); err != nil {
		return err
	}
	_, err := finishDiscussionChange(ctx, q, pending, discussionChange{
		CompanyID: channel.CompanyID, ConversationID: channel.ID, Actor: actor,
	})
	return err
}

// joinBoardChannelMentionsInTx brings the people a message in a Board's chat
// mentions into the chat when they can read the Board, so the mention
// reaches them. It runs under the chat lock, before the mentions are checked;
// the caller locked these people before the chat.
func joinBoardChannelMentionsInTx(
	ctx context.Context,
	q *generated.Queries,
	channel generated.ListDiscussionAccessRow,
	userIDs []uuid.UUID,
) error {
	if len(userIDs) == 0 {
		return nil
	}
	readers, err := q.BoardChannelReaders(ctx, generated.BoardChannelReadersParams{
		CompanyID: channel.CompanyID, BoardID: channel.BoardID.UUID, UserIds: userIDs,
	})
	if err != nil {
		return err
	}
	joined := false
	for _, userID := range readers {
		member, err := q.GetDiscussionMember(
			ctx,
			generated.GetDiscussionMemberParams{ConversationID: channel.ID, UserID: userID},
		)
		if err == nil && member.Status == "active" {
			continue
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err = grantDiscussionHuman(ctx, q, discussionGrant{
			CompanyID: channel.CompanyID, ConversationID: channel.ID, UserID: userID,
			SourceType: "channel", HeadSeq: channel.HeadSeq, NotificationLevel: "mentions",
		}); err != nil {
			return err
		}
		joined = true
	}
	if !joined {
		return nil
	}
	_, err = q.UpdateChannelConfiguration(
		ctx,
		generated.UpdateChannelConfigurationParams{CompanyID: channel.CompanyID, ConversationID: channel.ID},
	)
	return err
}

// boardChannelMentions lists the people a message mentions by name.
func boardChannelMentions(body DiscussionBody) []uuid.UUID {
	references, err := decodeDiscussionReferences(body)
	if err != nil {
		return nil
	}
	var users []uuid.UUID
	for _, reference := range references {
		if reference.Type == "user" && reference.UserID != nil && !slices.Contains(users, *reference.UserID) {
			users = append(users, *reference.UserID)
		}
	}
	return users
}

func boardChannelView(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	boardID uuid.UUID,
	channel uuid.NullUUID,
) (BoardChannel, error) {
	result := BoardChannel{
		BoardID:        boardID,
		ConversationID: util.NullUUIDPtr(channel),
		People:         []BoardChannelPerson{},
	}
	rows, err := q.ListBoardChannelPeople(ctx, generated.ListBoardChannelPeopleParams{
		CompanyID:      p.CompanyID,
		BoardID:        boardID,
		ConversationID: channel,
		PageLimit:      boardChannelPeopleLimit + 1,
	})
	if err != nil {
		return result, err
	}
	if len(rows) > boardChannelPeopleLimit {
		rows, result.PeopleTruncated = rows[:boardChannelPeopleLimit], true
	}
	for _, row := range rows {
		result.People = append(result.People, BoardChannelPerson{
			UserID:      row.UserID,
			DisplayName: row.DisplayName,
			AvatarURL:   discussionOptionalString(row.AvatarUrl),
			Joined:      row.Joined,
		})
	}
	if channel.Valid {
		member, err := q.GetDiscussionMember(
			ctx,
			generated.GetDiscussionMemberParams{ConversationID: channel.UUID, UserID: p.UserID},
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return result, err
		}
		result.Joined = err == nil && member.Status == "active"
	}
	return result, nil
}
