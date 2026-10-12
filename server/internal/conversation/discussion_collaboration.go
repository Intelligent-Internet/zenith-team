package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func discussionWritable(row generated.ListDiscussionAccessRow) error {
	if row.Kind == "huddle" || !discussionOpen(row) {
		return ErrDiscussionReadOnly
	}
	if !auth.CompanyRole(row.CompanyRole).Can(auth.PermissionConversationsDiscuss) {
		return ErrDiscussionForbidden
	}
	return nil
}

func getDiscussionItem(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
) (generated.ListDiscussionMessagesRow, error) {
	row, err := q.GetDiscussionMessage(
		ctx,
		generated.GetDiscussionMessageParams{
			CompanyID:      p.CompanyID,
			ConversationID: target.ConversationID,
			MessageID:      target.MessageID,
		},
	)
	return generated.ListDiscussionMessagesRow(row), discussionLookupError(err)
}

// Hydration is batched over a bounded window, never one query per row or avatar.
func hydrateDiscussionMessages(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	messages []DiscussionMessage,
) error {
	files := make([]*DiscussionMessage, len(messages))
	for i := range messages {
		files[i] = &messages[i]
	}
	if err := loadDiscussionFiles(ctx, q, p, files); err != nil {
		return err
	}
	if err := hydrateHuddleCards(ctx, q, p, messages); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	id := messages[0].ConversationID
	ids := make([]uuid.UUID, len(messages))
	byID := make(map[uuid.UUID]*DiscussionMessage, len(messages))
	for i := range messages {
		ids[i] = messages[i].ID
		byID[ids[i]] = &messages[i]
		messages[i].ReactionSummary = []DiscussionReactionSummary{}
		if messages[i].ThreadRootID == nil && messages[i].Type == "user_message" {
			messages[i].ThreadSummary = &DiscussionThreadSummary{RecentAuthors: []DiscussionAuthor{}}
		}
	}
	summaries, err := q.ListDiscussionThreadSummaries(
		ctx,
		generated.ListDiscussionThreadSummariesParams{
			CompanyID:      p.CompanyID,
			ConversationID: id,
			MessageIds:     ids,
		},
	)
	if err != nil {
		return err
	}
	for _, row := range summaries {
		summary := byID[row.ThreadRootID.UUID].ThreadSummary
		summary.ReplyCount = row.ReplyCount
		summary.LastReplyAt = util.TimestampToTime(row.LastReplyAt)
	}
	states, err := q.ListDiscussionThreadReadStates(ctx, generated.ListDiscussionThreadReadStatesParams{CompanyID: p.CompanyID, ConversationID: id, UserID: p.UserID, RootIds: ids})
	if err != nil {
		return err
	}
	for _, state := range states {
		if summary := byID[state.RootID].ThreadSummary; summary != nil {
			summary.UnreadCount = state.UnreadCount
			summary.ManualUnreadSeq = discussionInt(state.ManualUnreadSeq)
		}
	}
	authors, err := q.ListDiscussionThreadAuthors(
		ctx,
		generated.ListDiscussionThreadAuthorsParams{
			CompanyID:      p.CompanyID,
			ConversationID: id,
			MessageIds:     ids,
		},
	)
	if err != nil {
		return err
	}
	for _, row := range authors {
		summary := byID[row.ThreadRootID.UUID].ThreadSummary
		summary.RecentAuthors = append(
			summary.RecentAuthors,
			DiscussionAuthor{
				Type:        row.AuthorKind,
				ID:          util.NullUUIDPtr(row.AuthorID),
				DisplayName: boundedDiscussionLabel(row.DisplayName),
				AvatarURL:   discussionOptionalString(row.AvatarUrl),
			},
		)
	}
	reactions, err := q.ListDiscussionReactionSummaries(
		ctx,
		generated.ListDiscussionReactionSummariesParams{
			CompanyID:      p.CompanyID,
			ConversationID: id,
			MessageIds:     ids,
			ActorUserID:    p.UserID,
		},
	)
	if err != nil {
		return err
	}
	for _, row := range reactions {
		message := byID[row.MessageID]
		message.ReactionSummary = append(
			message.ReactionSummary,
			DiscussionReactionSummary{Emoji: row.Emoji, Count: row.Count, Reacted: row.Reacted},
		)
	}
	pins, err := q.ListDiscussionPinnedIDs(
		ctx,
		generated.ListDiscussionPinnedIDsParams{CompanyID: p.CompanyID, ConversationID: id, MessageIds: ids},
	)
	if err != nil {
		return err
	}
	for _, id := range pins {
		byID[id].Pinned = true
	}
	return nil
}

func discussionMessageInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
) (DiscussionMessage, error) {
	row, err := getDiscussionItem(ctx, q, p, target)
	if err != nil {
		return DiscussionMessage{}, err
	}
	message, err := discussionMessage(row)
	if err != nil {
		return message, err
	}
	messages := []DiscussionMessage{message}
	err = hydrateDiscussionMessages(ctx, q, p, messages)
	return messages[0], err
}

type discussionMessageMutation func(*generated.Queries, generated.ListDiscussionAccessRow, generated.ListDiscussionMessagesRow) (bool, error)

func (s *DefaultDiscussionService) mutateMessage(
	ctx context.Context,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
	mutate discussionMessageMutation,
	mentions []discussionReference,
) (DiscussionMutation, error) {
	var result DiscussionMutation
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := lockDiscussionMembership(ctx, q, p); err != nil {
			return err
		}
		ids := make([]uuid.UUID, 0, len(mentions))
		for _, reference := range mentions {
			if reference.Type == "user" {
				ids = append(ids, *reference.UserID)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
		for _, id := range ids {
			if _, err := inbox.LockRecipientInTx(ctx, q, p.CompanyID, id); err != nil {
				return err
			}
		}
		if err := lockDiscussion(ctx, q, p, target.ConversationID); err != nil {
			return err
		}
		access, err := currentDiscussion(ctx, q, p, target.ConversationID, false)
		if err != nil {
			return err
		}
		item, err := getDiscussionItem(ctx, q, p, target)
		if err != nil {
			return err
		}
		if err = discussionWritable(access); err != nil {
			return err
		}
		if item.Type != "user_message" && item.Type != "agent_text" {
			return ErrDiscussionForbidden
		}
		changed, err := mutate(q, access, item)
		if err != nil {
			return err
		}
		revision := access.Version
		if changed {
			if len(mentions) > 0 {
				var oldBody DiscussionBody
				if err := json.Unmarshal(item.Body, &oldBody); err != nil {
					return err
				}
				oldRefs, err := decodeDiscussionReferences(oldBody)
				if err != nil {
					return err
				}
				oldUsers := make(map[uuid.UUID]bool)
				for _, ref := range oldRefs {
					if ref.Type == "user" {
						oldUsers[*ref.UserID] = true
					}
				}
				var added []discussionReference
				for _, ref := range mentions {
					if ref.Type == "user" && !oldUsers[*ref.UserID] {
						added = append(added, ref)
					}
				}
				if len(added) > 0 {
					var rootID *uuid.UUID
					if item.ThreadRootID.Valid {
						rootID = &item.ThreadRootID.UUID
					}
					if err := createDiscussionMentionInboxItems(ctx, q, p, target.ConversationID, target.MessageID, access.Kind, rootID, added, &pending, true); err != nil {
						return err
					}
				}
			}
			row, err := finishDiscussionChange(
				ctx,
				q,
				&pending,
				discussionChange{
					CompanyID:      p.CompanyID,
					ConversationID: target.ConversationID,
					Actor:          events.UserActor(p.UserID.String()),
				},
			)
			if err != nil {
				return err
			}
			revision = row.Version
		}
		message, err := discussionMessageInTx(ctx, q, p, target)
		if err != nil {
			return err
		}
		result = DiscussionMutation{Message: message, ConversationRevision: revision, HeadSeq: access.HeadSeq}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("mutate discussion message: %w", err)
	}
	publishDiscussionEvents(ctx, s.bus, pending)
	return result, nil
}

func (s *DefaultDiscussionService) EditMessage(
	ctx context.Context,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
	input DiscussionEditInput,
) (DiscussionMutation, error) {
	if !discussionRevisionValid(input.ExpectedRevision) {
		return DiscussionMutation{}, ErrDiscussionInvalid
	}
	normalized, err := normalizeDiscussionSend(DiscussionSendInput{RequestKey: target.MessageID, Body: input.Body})
	if err != nil {
		return DiscussionMutation{}, err
	}
	mentions, err := decodeDiscussionReferences(normalized.Input.Body)
	if err != nil {
		return DiscussionMutation{}, err
	}
	return s.mutateMessage(
		ctx,
		p,
		target,
		func(q *generated.Queries, _ generated.ListDiscussionAccessRow, item generated.ListDiscussionMessagesRow) (bool, error) {
			if item.Type != "user_message" || item.AuthorID.UUID != p.UserID {
				return false, ErrDiscussionForbidden
			}
			if item.Revision != input.ExpectedRevision {
				return false, ErrDiscussionRevisionConflict
			}
			if item.DeletedAt.Valid {
				return false, ErrDiscussionForbidden
			}
			var previous DiscussionBody
			if err := json.Unmarshal(item.Body, &previous); err != nil {
				return false, err
			}
			if _, err := validateDiscussionReferences(ctx, q, p, target.ConversationID, normalized.Input.Body, nil, &previous); err != nil {
				return false, err
			}
			err := q.EditDiscussionMessage(
				ctx,
				generated.EditDiscussionMessageParams{
					CompanyID:      p.CompanyID,
					ConversationID: target.ConversationID,
					MessageID:      target.MessageID,
					Body:           normalized.Body,
					Content:        normalized.Content,
				},
			)
			return true, err
		},
		mentions,
	)
}

func (s *DefaultDiscussionService) DeleteMessage(
	ctx context.Context,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
	revision int64,
) (DiscussionMutation, error) {
	if !discussionRevisionValid(revision) {
		return DiscussionMutation{}, ErrDiscussionInvalid
	}
	return s.mutateMessage(
		ctx,
		p,
		target,
		func(q *generated.Queries, access generated.ListDiscussionAccessRow, item generated.ListDiscussionMessagesRow) (bool, error) {
			if item.Type != "user_message" {
				return false, ErrDiscussionForbidden
			}
			if item.AuthorID.UUID != p.UserID &&
				(access.Kind != "channel" || !auth.CompanyRole(access.CompanyRole).Can(auth.PermissionChannelsManage)) {
				return false, ErrDiscussionForbidden
			}
			if item.Revision != revision {
				return false, ErrDiscussionRevisionConflict
			}
			if item.DeletedAt.Valid {
				return false, ErrDiscussionForbidden
			}
			if _, err := q.UnpinDiscussionMessage(
				ctx,
				generated.UnpinDiscussionMessageParams{
					CompanyID:      p.CompanyID,
					ConversationID: target.ConversationID,
					MessageID:      target.MessageID,
				},
			); err != nil {
				return false, err
			}
			if err := q.ClearDiscussionReactions(
				ctx,
				generated.ClearDiscussionReactionsParams{
					CompanyID:      p.CompanyID,
					ConversationID: target.ConversationID,
					MessageID:      target.MessageID,
				},
			); err != nil {
				return false, err
			}
			err := q.DeleteDiscussionMessage(
				ctx,
				generated.DeleteDiscussionMessageParams{
					CompanyID:      p.CompanyID,
					ConversationID: target.ConversationID,
					MessageID:      target.MessageID,
				},
			)
			if err == nil {
				err = applyDiscussionUnreadDelta(ctx, q, discussionUnreadDelta{ConversationID: target.ConversationID, RootID: item.ThreadRootID, AuthorID: item.AuthorID.UUID, Seq: item.Seq, Delta: -1})
			}
			return true, err
		},
		nil,
	)
}

func (s *DefaultDiscussionService) SetMessagePin(
	ctx context.Context,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
	active bool,
) (DiscussionMutation, error) {
	return s.mutateMessage(
		ctx,
		p,
		target,
		func(q *generated.Queries, _ generated.ListDiscussionAccessRow, item generated.ListDiscussionMessagesRow) (bool, error) {
			if item.DeletedAt.Valid {
				return false, ErrDiscussionForbidden
			}
			var changed int64
			var err error
			if active {
				changed, err = q.PinDiscussionMessage(
					ctx,
					generated.PinDiscussionMessageParams{
						CompanyID:      p.CompanyID,
						ConversationID: target.ConversationID,
						MessageID:      target.MessageID,
						ActorUserID:    p.UserID,
					},
				)
			} else {
				changed, err = q.UnpinDiscussionMessage(
					ctx,
					generated.UnpinDiscussionMessageParams{
						CompanyID:      p.CompanyID,
						ConversationID: target.ConversationID,
						MessageID:      target.MessageID,
					},
				)
			}
			return changed > 0, err
		},
		nil,
	)
}

func (s *DefaultDiscussionService) SetReaction(
	ctx context.Context,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
	input DiscussionReactionInput,
) (DiscussionMutation, error) {
	emoji, err := canonicalDiscussionEmoji(input.Emoji)
	if err != nil {
		return DiscussionMutation{}, err
	}
	return s.mutateMessage(
		ctx,
		p,
		target,
		func(q *generated.Queries, _ generated.ListDiscussionAccessRow, item generated.ListDiscussionMessagesRow) (bool, error) {
			if item.DeletedAt.Valid {
				return false, ErrDiscussionForbidden
			}
			var changed int64
			var err error
			if input.Active {
				summaries, listErr := q.ListDiscussionReactionSummaries(
					ctx,
					generated.ListDiscussionReactionSummariesParams{
						CompanyID:      p.CompanyID,
						ConversationID: target.ConversationID,
						MessageIds:     []uuid.UUID{target.MessageID},
						ActorUserID:    p.UserID,
					},
				)
				if listErr != nil {
					return false, listErr
				}
				exists := false
				for _, row := range summaries {
					if row.Emoji == emoji {
						exists = true
					}
				}
				if len(summaries) >= 20 && !exists {
					return false, ErrDiscussionLimit
				}
				changed, err = q.AddDiscussionReaction(
					ctx,
					generated.AddDiscussionReactionParams{
						CompanyID:      p.CompanyID,
						ConversationID: target.ConversationID,
						MessageID:      target.MessageID,
						UserID:         p.UserID,
						Emoji:          emoji,
					},
				)
			} else {
				changed, err = q.RemoveDiscussionReaction(
					ctx,
					generated.RemoveDiscussionReactionParams{
						CompanyID:      p.CompanyID,
						ConversationID: target.ConversationID,
						MessageID:      target.MessageID,
						UserID:         p.UserID,
						Emoji:          emoji,
					},
				)
			}
			return changed > 0, err
		},
		nil,
	)
}

func threadStateInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
) (DiscussionThreadState, error) {
	count := int64(0)
	state := DiscussionThreadState{RootID: target.MessageID, Follow: "", UnreadCount: &count}
	member, err := q.GetDiscussionMember(ctx, generated.GetDiscussionMemberParams{ConversationID: target.ConversationID, UserID: p.UserID})
	if err != nil {
		return state, discussionLookupError(err)
	}
	state.ReadThroughSeq = member.JoinedSeq
	state.GrantGeneration = &member.Generation
	row, err := q.GetDiscussionThreadState(
		ctx,
		generated.GetDiscussionThreadStateParams{
			CompanyID:      p.CompanyID,
			ConversationID: target.ConversationID,
			RootID:         target.MessageID,
			UserID:         p.UserID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.Revision = row.Version
	state.GrantGeneration = &row.GrantGeneration
	state.Follow = row.Follow
	state.ReadThroughSeq = max(member.JoinedSeq, row.ReadSeq)
	state.ManualUnreadSeq = discussionInt(row.ManualUnreadSeq)
	state.UnreadCount = &row.UnreadCount
	return state, nil
}

func (s *DefaultDiscussionService) UpdateThreadState(
	ctx context.Context,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
	input DiscussionThreadStateInput,
) (DiscussionThreadState, error) {
	if input.ReadThroughSeq != nil || input.ManualUnreadSeq != nil {
		if input.Follow != "" {
			return DiscussionThreadState{}, ErrDiscussionInvalid
		}
		return s.updateThreadReadState(ctx, p, target, DiscussionReadInput{ReadThroughSeq: input.ReadThroughSeq, ManualUnreadSeq: input.ManualUnreadSeq, ExpectedRevision: &input.ExpectedRevision, GrantGeneration: input.GrantGeneration})
	}
	if input.ExpectedRevision < 0 || input.ExpectedRevision > maxDiscussionInteger ||
		(input.Follow != "following" && input.Follow != "unfollowed") {
		return DiscussionThreadState{}, ErrDiscussionInvalid
	}
	var result DiscussionThreadState
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := lockDiscussionMembership(ctx, q, p); err != nil {
			return err
		}
		if err := lockDiscussion(ctx, q, p, target.ConversationID); err != nil {
			return err
		}
		access, err := currentDiscussion(ctx, q, p, target.ConversationID, false)
		if err != nil {
			return err
		}
		root, err := getDiscussionItem(ctx, q, p, target)
		if err != nil {
			return err
		}
		if root.Type != "user_message" || root.ThreadRootID.Valid {
			return ErrDiscussionNotFound
		}
		state, err := threadStateInTx(ctx, q, p, target)
		if err != nil {
			return err
		}
		if state.Revision != input.ExpectedRevision {
			return ErrDiscussionRevisionConflict
		}
		if !access.GrantGeneration.Valid {
			if !access.IsGeneral && access.MemberCount >= 50 {
				return ErrDiscussionLimit
			}
			if _, err = grantDiscussionHuman(
				ctx,
				q,
				discussionGrant{
					CompanyID:      p.CompanyID,
					ConversationID: target.ConversationID,
					UserID:         p.UserID,
					ActorUserID:    p.UserID,
					SourceType:     "channel",
					HeadSeq:        access.HeadSeq,
				},
			); err != nil {
				return err
			}
			if err = recordChannelSystem(
				ctx,
				q,
				&pending,
				discussionSystemChange{
					CompanyID:      p.CompanyID,
					ConversationID: target.ConversationID,
					ActorUserID:    &p.UserID,
					Action:         "member_added",
					SubjectUserID:  &p.UserID,
				},
			); err != nil {
				return err
			}
		}
		if state.Follow == input.Follow {
			result = state
			return nil
		}
		member, err := q.GetDiscussionMember(
			ctx,
			generated.GetDiscussionMemberParams{ConversationID: target.ConversationID, UserID: p.UserID},
		)
		if err != nil {
			return err
		}
		row, err := q.SetDiscussionThreadState(
			ctx,
			generated.SetDiscussionThreadStateParams{
				CompanyID:       p.CompanyID,
				ConversationID:  target.ConversationID,
				RootID:          target.MessageID,
				UserID:          p.UserID,
				GrantGeneration: member.Generation,
				Follow:          input.Follow,
				ReadSeq:         member.JoinedSeq,
			},
		)
		if err != nil {
			return err
		}
		result = DiscussionThreadState{
			RootID:          target.MessageID,
			Revision:        row.Version,
			GrantGeneration: &row.GrantGeneration,
			Follow:          row.Follow,
		}
		personal, err := q.TouchDiscussionUserState(
			ctx,
			generated.TouchDiscussionUserStateParams{
				ConversationID: target.ConversationID,
				UserID:         p.UserID,
			},
		)
		if err != nil {
			return err
		}
		if err = refreshDiscussionUnread(ctx, q, p, target.ConversationID); err != nil {
			return err
		}
		result, err = threadStateInTx(ctx, q, p, target)
		if err != nil {
			return err
		}
		pending = append(pending, discussionStateEvent(personal))
		return nil
	})
	if err != nil {
		return result, err
	}
	publishDiscussionEvents(ctx, s.bus, pending)
	return result, nil
}
