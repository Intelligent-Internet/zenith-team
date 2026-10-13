package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type DiscussionService interface {
	UpdateReadState(context.Context, auth.UserPrincipal, uuid.UUID, DiscussionReadInput) (DiscussionUserState, error)
	Presence(context.Context, auth.UserPrincipal, DiscussionPresenceInput) ([]DiscussionMemberPresence, error)
	ListThread(
		context.Context,
		auth.UserPrincipal,
		DiscussionMessageTarget,
		DiscussionPageOptions,
	) (DiscussionThreadPage, error)
	MessageContext(context.Context, auth.UserPrincipal, DiscussionMessageTarget) (DiscussionContext, error)
	EditMessage(
		context.Context,
		auth.UserPrincipal,
		DiscussionMessageTarget,
		DiscussionEditInput,
	) (DiscussionMutation, error)
	DeleteMessage(context.Context, auth.UserPrincipal, DiscussionMessageTarget, int64) (DiscussionMutation, error)
	SetMessagePin(context.Context, auth.UserPrincipal, DiscussionMessageTarget, bool) (DiscussionMutation, error)
	SetReaction(
		context.Context,
		auth.UserPrincipal,
		DiscussionMessageTarget,
		DiscussionReactionInput,
	) (DiscussionMutation, error)
	ListPins(context.Context, auth.UserPrincipal, uuid.UUID, DiscussionPageOptions) (DiscussionPins, error)
	ListReactions(
		context.Context,
		auth.UserPrincipal,
		DiscussionMessageTarget,
		DiscussionPageOptions,
	) (DiscussionReactions, error)
	UpdateThreadState(
		context.Context,
		auth.UserPrincipal,
		DiscussionMessageTarget,
		DiscussionThreadStateInput,
	) (DiscussionThreadState, error)
	Send(context.Context, auth.UserPrincipal, uuid.UUID, DiscussionSendInput) (DiscussionReceipt, error)
	ListMessages(context.Context, auth.UserPrincipal, uuid.UUID, DiscussionPageOptions) (DiscussionPage, error)
	QueryStates(context.Context, auth.UserPrincipal, []uuid.UUID) ([]DiscussionState, error)
	UpdateState(context.Context, auth.UserPrincipal, uuid.UUID, DiscussionStateInput) (DiscussionUserState, error)
}

var _ DiscussionService = (*DefaultDiscussionService)(nil)

type DiscussionServiceOptions struct {
	Store *db.Store
	Bus   *events.Bus
}
type DefaultDiscussionService struct {
	store *db.Store
	bus   *events.Bus
}

func NewDiscussionService(options DiscussionServiceOptions) (*DefaultDiscussionService, error) {
	if options.Store == nil || options.Bus == nil {
		return nil, errors.New("discussion store and event bus are required")
	}
	return &DefaultDiscussionService{
		store: options.Store,
		bus:   options.Bus,
	}, nil
}

func (s *DefaultDiscussionService) Send(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input DiscussionSendInput,
) (DiscussionReceipt, error) {
	normalized, err := normalizeDiscussionSend(input)
	if err != nil {
		return DiscussionReceipt{}, err
	}
	var receipt DiscussionReceipt
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := lockDiscussionMembership(ctx, q, p); err != nil {
			return err
		}

		huddle, huddleErr := q.LockHuddleByChat(ctx, generated.LockHuddleByChatParams{CompanyID: p.CompanyID, ChatConversationID: id})
		if huddleErr != nil && !errors.Is(huddleErr, pgx.ErrNoRows) {
			return huddleErr
		}
		if huddleErr == nil {
			if huddle.State != "open" || !huddle.DeadlineAt.Time.After(time.Now()) {
				return ErrDiscussionForbidden
			}
			if _, err := q.LockHuddleChatWriter(ctx, generated.LockHuddleChatWriterParams{CompanyID: p.CompanyID, HuddleID: huddle.ID, UserID: p.UserID}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrDiscussionForbidden
				}
				return err
			}
			if len(input.AttachmentIDs) > 0 || input.ThreadRootID != nil ||
				len(input.Body.References) > 0 ||
				input.Whiteboard != nil {
				return ErrDiscussionInvalid
			}
		}

		// Lock possible recipients before the Conversation to preserve the
		// Company -> membership -> Conversation order used by revocation.
		roster, err := q.ListDiscussionMembers(
			ctx,
			generated.ListDiscussionMembersParams{CompanyID: p.CompanyID, ConversationID: id},
		)
		if err != nil {
			return err
		}
		lockedRecipients := make(map[uuid.UUID]bool, len(roster))
		for _, member := range roster {
			if _, err := inbox.LockRecipientInTx(ctx, q, p.CompanyID, member.UserID); err != nil {
				return err
			}
			lockedRecipients[member.UserID] = true
		}
		if err := lockDiscussion(ctx, q, p, id); err != nil {
			return err
		}

		currentRoster, err := q.ListDiscussionMembers(
			ctx,
			generated.ListDiscussionMembersParams{CompanyID: p.CompanyID, ConversationID: id},
		)
		if err != nil {
			return err
		}
		for _, member := range currentRoster {
			if member.Active && !lockedRecipients[member.UserID] {
				return ErrDiscussionRevisionConflict
			}
		}
		access, err := currentDiscussion(ctx, q, p, id, false)
		if err != nil {
			return err
		}
		var threadRoot *generated.ListDiscussionMessagesRow
		if input.ThreadRootID != nil {
			parent, err := getDiscussionItem(
				ctx,
				q,
				p,
				DiscussionMessageTarget{ConversationID: id, MessageID: *input.ThreadRootID},
			)
			if err != nil {
				return err
			}
			if parent.Type != "user_message" && parent.Type != "agent_text" {
				return ErrDiscussionNotFound
			}
			if parent.ThreadRootID.Valid {
				parent, err = getDiscussionItem(
					ctx,
					q,
					p,
					DiscussionMessageTarget{
						ConversationID: id,
						MessageID:      parent.ThreadRootID.UUID,
					},
				)
				if err != nil {
					return err
				}
			}
			threadRoot = &parent
			canonicalInput := input
			canonicalInput.ThreadRootID = &parent.ID
			normalized, err = normalizeDiscussionSend(canonicalInput)
			if err != nil {
				return err
			}
		}
		item, err := q.GetDiscussionSendReceipt(
			ctx,
			generated.GetDiscussionSendReceiptParams{
				CompanyID:      p.CompanyID,
				ConversationID: id,
				AuthorID:       p.UserID,
				RequestKey:     input.RequestKey,
			},
		)
		replay := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if replay {
			if !bytes.Equal(item.SendFingerprint, normalized.Fingerprint) {
				return ErrDiscussionRequestConflict
			}
		} else {
			if access.ArchivedAt.Valid || (access.Kind == "human_direct" && !access.PeerActive) {
				return ErrDiscussionReadOnly
			}
			if !auth.CompanyRole(access.CompanyRole).Can(auth.PermissionConversationsDiscuss) {
				return ErrDiscussionForbidden
			}
			previousHead := int64(0)
			if threadRoot != nil {
				previousHead, err = q.GetDiscussionThreadHead(
					ctx,
					generated.GetDiscussionThreadHeadParams{
						CompanyID:      p.CompanyID,
						ConversationID: id,
						RootID:         threadRoot.ID,
						RootSeq:        threadRoot.Seq,
					},
				)
				if err != nil {
					return err
				}
			}
			seq, err := q.AllocateDiscussionSequence(
				ctx,
				generated.AllocateDiscussionSequenceParams{CompanyID: p.CompanyID, ConversationID: id},
			)
			if err != nil {
				return err
			}
			item, err = q.InsertDiscussionMessage(
				ctx,
				generated.InsertDiscussionMessageParams{
					CompanyID:       p.CompanyID,
					ConversationID:  id,
					Seq:             seq,
					AuthorID:        p.UserID,
					RequestKey:      input.RequestKey,
					Body:            normalized.Body,
					Content:         normalized.Content,
					SendFingerprint: normalized.Fingerprint,
					ThreadRootID:    util.NullUUIDFromPtr(normalized.Input.ThreadRootID),
				},
			)
			if err != nil {
				return err
			}
			attachmentIDs := slices.Clone(input.AttachmentIDs)
			slices.SortFunc(attachmentIDs, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
			for _, attachmentID := range attachmentIDs {
				if _, err := q.LinkDiscussionAttachment(
					ctx,
					generated.LinkDiscussionAttachmentParams{
						CompanyID:    p.CompanyID,
						MessageID:    item.ID,
						AttachmentID: attachmentID,
						ActorUserID:  p.UserID,
						Position:     int32(slices.Index(input.AttachmentIDs, attachmentID)),
					},
				); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return ErrDiscussionInvalid
					}
					return err
				}
			}
			references, err := validateDiscussionReferences(
				ctx,
				q,
				p,
				id,
				normalized.Input.Body,
				normalized.Input.AttachmentIDs,
				nil,
			)
			if err != nil {
				return err
			}
			rootID := item.ID
			if err := admitChannelInvocations(ctx, q, &pending, channelAdmission{Principal: p, ChannelID: id, MessageID: item.ID, References: references}); err != nil {
				return err
			}
			if threadRoot == nil {
				previousHead = seq
			} else {
				rootID = threadRoot.ID
			}
			if err = q.AutoFollowDiscussionAuthor(
				ctx,
				generated.AutoFollowDiscussionAuthorParams{
					CompanyID:       p.CompanyID,
					ConversationID:  id,
					RootID:          rootID,
					UserID:          p.UserID,
					PreviousHeadSeq: previousHead,
					OptBackIn:       true,
				},
			); err != nil {
				return err
			}
			if threadRoot != nil && threadRoot.AuthorID.UUID != p.UserID {
				if err = q.AutoFollowDiscussionAuthor(
					ctx,
					generated.AutoFollowDiscussionAuthorParams{
						CompanyID:       p.CompanyID,
						ConversationID:  id,
						RootID:          rootID,
						UserID:          threadRoot.AuthorID.UUID,
						PreviousHeadSeq: previousHead,
					},
				); err != nil {
					return err
				}
			}
			artifactIDs := slices.Clone(input.ArtifactIDs)
			if normalized.Input.Whiteboard != nil {
				board, err := canvas.CreateWhiteboardInTx(
					ctx,
					q,
					p,
					id,
					normalized.Input.Whiteboard.Title,
				)
				if errors.Is(err, canvas.ErrBoardInvalid) {
					return ErrDiscussionInvalid
				}
				if err != nil {
					return err
				}
				artifactIDs = append(artifactIDs, board)
			}
			slices.SortFunc(artifactIDs, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
			for _, artifactID := range artifactIDs {
				if err := execution.ShareArtifactInTx(ctx, q, p, artifactID, id); err != nil {
					if errors.Is(err, execution.ErrDeliverableNotFound) ||
						errors.Is(err, execution.ErrDeliverableForbidden) {
						return ErrDiscussionForbidden
					}
					return err
				}
				if err := q.LinkDiscussionArtifact(
					ctx,
					generated.LinkDiscussionArtifactParams{
						CompanyID: p.CompanyID,
						MessageID: item.ID,
						OutputID:  artifactID,
					},
				); err != nil {
					return err
				}
			}
			if len(artifactIDs) > 0 {
				recipients, err := q.ListArtifactCompanyRecipients(ctx, p.CompanyID)
				if err != nil {
					return err
				}
				pending = append(
					pending,
					events.Event{
						EntityType: execution.TopicArtifact,
						Name:       execution.EventArtifactAccessChanged,
						CompanyID:  p.CompanyID,
						CreatedAt:  time.Now().UTC(),
						Actor:      events.UserActor(p.UserID.String()),
						Payload: execution.ArtifactAccessChanged{
							RecipientUserIDs: recipients,
						},
					},
				)
			}
			if normalized.Input.ThreadRootID != nil {
				if err := q.EnsureDirectThreadReadStates(ctx, generated.EnsureDirectThreadReadStatesParams{ConversationID: id, RootID: *normalized.Input.ThreadRootID}); err != nil {
					return err
				}
			}
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
			if err := createDiscussionMentionInboxItems(
				ctx,
				q,
				p,
				id,
				item.ID,
				access.Kind,
				normalized.Input.ThreadRootID,
				references,
				&pending,
				false,
			); err != nil {
				return err
			}
			if err := applyDiscussionUnreadDelta(ctx, q, discussionUnreadDelta{ConversationID: id, RootID: item.ThreadRootID, AuthorID: p.UserID, Seq: item.Seq, Delta: 1}); err != nil {
				return err
			}

			revealed, err := q.RevealDiscussionForPeerMessage(
				ctx,
				generated.RevealDiscussionForPeerMessageParams{
					ConversationID: id,
					AuthorID:       p.UserID,
					Seq:            seq,
				},
			)
			if err != nil {
				return err
			}
			for _, state := range revealed {
				pending = append(pending, discussionStateEvent(state))
			}
			access, err = currentDiscussion(ctx, q, p, id, false)
			if err != nil {
				return err
			}
		}
		message, err := discussionMessageInTx(
			ctx,
			q,
			p,
			DiscussionMessageTarget{ConversationID: id, MessageID: item.ID},
		)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return err
		}
		if len(encoded) > maxDiscussionItemBytes {
			return ErrDiscussionLimit
		}
		receipt = DiscussionReceipt{
			Message:              message,
			RequestKey:           input.RequestKey,
			Replayed:             replay,
			ConversationRevision: access.Version,
			HeadSeq:              access.HeadSeq,
		}
		return nil
	})
	if err != nil {
		return DiscussionReceipt{}, fmt.Errorf("send discussion message: %w", err)
	}
	publishDiscussionEvents(ctx, s.bus, pending)
	return receipt, nil
}

func (s *DefaultDiscussionService) QueryStates(
	ctx context.Context,
	p auth.UserPrincipal,
	ids []uuid.UUID,
) ([]DiscussionState, error) {
	if len(ids) == 0 || !validDiscussionIDs(ids, 50) {
		return nil, ErrDiscussionInvalid
	}
	result := make([]DiscussionState, 0, len(ids))
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			params := discussionParameters(p)
			params.ConversationIds = ids
			rows, err := q.ListDiscussionAccess(ctx, params)
			if err != nil {
				return err
			}
			byID := make(map[uuid.UUID]generated.ListDiscussionAccessRow, len(rows))
			for _, row := range rows {
				byID[row.ID] = row
			}
			for _, id := range ids {
				if row, ok := byID[id]; ok {
					result = append(result, discussionState(row))
				}
			}
			return nil
		},
	)
	return result, err
}

func (s *DefaultDiscussionService) UpdateState(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input DiscussionStateInput,
) (DiscussionUserState, error) {
	if input.ExpectedRevision < 0 || input.ExpectedRevision > maxDiscussionInteger ||
		(input.Pinned == nil && input.Hidden == nil && input.NotificationLevel == nil) ||
		(input.NotificationLevel != nil && *input.NotificationLevel != "all" && *input.NotificationLevel != "mentions" && *input.NotificationLevel != "muted") {
		return DiscussionUserState{}, ErrDiscussionInvalid
	}
	var result DiscussionUserState
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := lockDiscussionMembership(ctx, q, p); err != nil {
			return err
		}
		if err := lockDiscussion(ctx, q, p, id); err != nil {
			return err
		}
		access, err := currentDiscussion(ctx, q, p, id, false)
		if err != nil {
			return err
		}
		if access.StateRevision.Int64 != input.ExpectedRevision {
			return ErrDiscussionRevisionConflict
		}
		row, err := q.UpdateDiscussionUserState(
			ctx,
			generated.UpdateDiscussionUserStateParams{
				ConversationID:    id,
				UserID:            p.UserID,
				GrantGeneration:   access.GrantGeneration.Int64,
				ExpectedRevision:  input.ExpectedRevision,
				HeadSeq:           access.HeadSeq,
				Pinned:            discussionBool(input.Pinned),
				Hidden:            discussionBool(input.Hidden),
				NotificationLevel: pgtype.Text{String: discussionNotificationLevel(input.NotificationLevel), Valid: input.NotificationLevel != nil},
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDiscussionRevisionConflict
		}
		if err != nil {
			return err
		}
		result = DiscussionUserState{
			Revision:          row.Version,
			PinnedAt:          util.TimestampToTime(row.PinnedAt),
			HiddenAt:          util.TimestampToTime(row.HiddenAt),
			NotificationLevel: row.NotificationLevel,
		}
		pending = append(pending, discussionStateEvent(row))
		return nil
	})
	if err != nil {
		return DiscussionUserState{}, err
	}
	publishDiscussionEvents(ctx, s.bus, pending)
	return result, nil
}

func discussionBool(value *bool) pgtype.Bool {
	if value == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *value, Valid: true}
}

func discussionMessage(row generated.ListDiscussionMessagesRow) (DiscussionMessage, error) {
	result := DiscussionMessage{
		ID:             row.ID,
		ConversationID: row.ConversationID,
		Seq:            row.Seq,
		Revision:       row.Revision,
		Type:           row.Type,
		Author: DiscussionAuthor{
			Type:        row.AuthorKind,
			ID:          util.NullUUIDPtr(row.AuthorID),
			DisplayName: boundedDiscussionLabel(row.AuthorDisplayName),
			AvatarURL:   discussionOptionalString(row.AuthorAvatarUrl),
		},
		CreatedAt:       row.CreatedAt.Time.UTC(),
		Attachments:     []attachment.Response{},
		EditedAt:        util.TimestampToTime(row.EditedAt),
		DeletedAt:       util.TimestampToTime(row.DeletedAt),
		ThreadRootID:    util.NullUUIDPtr(row.ThreadRootID),
		ReactionSummary: []DiscussionReactionSummary{},
		Artifacts:       []DiscussionArtifact{},
	}
	if row.Body != nil {
		var body DiscussionBody
		if err := json.Unmarshal(row.Body, &body); err != nil {
			return DiscussionMessage{}, fmt.Errorf("decode stored discussion body: %w", err)
		}
		result.Body = &body
	}
	if row.Type == "discussion_system" {
		var system DiscussionSystem
		if err := json.Unmarshal(row.Metadata, &system); err != nil {
			return DiscussionMessage{}, err
		}
		result.System = &system
	}
	return result, nil
}

type discussionChange struct {
	CompanyID, ConversationID uuid.UUID
	Actor                     events.Actor
}

func finishDiscussionChange(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input discussionChange,
) (generated.Conversation, error) {
	if err := invalidateChannelInvocations(ctx, q, pending, input.ConversationID); err != nil {
		return generated.Conversation{}, err
	}
	before, err := activity.ObserveInTx(ctx, q, Topic, input.ConversationID)
	if err != nil {
		return generated.Conversation{}, err
	}
	row, err := q.BumpDiscussionConversation(
		ctx,
		generated.BumpDiscussionConversationParams{
			CompanyID:      input.CompanyID,
			ConversationID: input.ConversationID,
		},
	)
	if err != nil {
		return row, err
	}
	if err = activity.RecordInTx(
		ctx,
		q,
		pending,
		input.Actor,
		EventConversationUpdated,
		before,
		Topic,
		input.ConversationID,
	); err != nil {
		return row, err
	}
	*pending = append(*pending, discussionChangedEvent(row))
	return row, nil
}

type discussionSystemChange struct {
	CompanyID, ConversationID                  uuid.UUID
	ActorUserID, SubjectUserID, SubjectAgentID *uuid.UUID
	Action                                     string
}

func insertDiscussionSystem(
	ctx context.Context,
	q *generated.Queries,
	companyID, conversationID uuid.UUID,
	system DiscussionSystem,
) error {
	seq, err := q.AllocateDiscussionSequence(
		ctx,
		generated.AllocateDiscussionSequenceParams{
			CompanyID:      companyID,
			ConversationID: conversationID,
		},
	)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(system)
	if err != nil {
		return err
	}
	if err = q.InsertDiscussionSystemItem(
		ctx,
		generated.InsertDiscussionSystemItemParams{
			CompanyID:      companyID,
			ConversationID: conversationID,
			Seq:            seq,
			Metadata:       metadata,
		},
	); err != nil {
		return err
	}
	return nil
}

func recordChannelSystem(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input discussionSystemChange,
) error {
	err := insertDiscussionSystem(ctx, q, input.CompanyID, input.ConversationID, DiscussionSystem{
		Action: input.Action, ActorUserID: input.ActorUserID,
		SubjectUserID: input.SubjectUserID, SubjectAgentID: input.SubjectAgentID,
	})
	if err != nil {
		return err
	}
	if _, err = q.UpdateChannelConfiguration(
		ctx,
		generated.UpdateChannelConfigurationParams{
			CompanyID:      input.CompanyID,
			ConversationID: input.ConversationID,
		},
	); err != nil {
		return err
	}
	actor := events.SystemActor()
	if input.ActorUserID != nil {
		actor = events.UserActor(input.ActorUserID.String())
	}
	_, err = finishDiscussionChange(
		ctx,
		q,
		pending,
		discussionChange{CompanyID: input.CompanyID, ConversationID: input.ConversationID, Actor: actor},
	)
	return err
}

func publishDiscussionEvents(ctx context.Context, bus *events.Bus, pending []events.Event) {
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	for _, group := range events.GroupByTopic(pending) {
		bus.Publish(publishCtx, group.Topic, group.Events...)
		for _, event := range group.Events {
			if event.Name != execution.EventRunQueued {
				continue
			}
			snapshot, err := events.PayloadOf[execution.RunSnapshot](event)
			if err == nil && snapshot.ConversationKind == "channel_agent" {
				execution.PublishRuntimeWork(publishCtx, bus, generated.Run{ID: snapshot.ID, CompanyID: snapshot.CompanyID, ConversationID: snapshot.ConversationID, AgentID: snapshot.AgentID, RuntimeID: snapshot.RuntimeID})
			}
		}
	}
}

func discussionNotificationLevel(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
