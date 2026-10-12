package conversation

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func (s *DefaultDiscussionService) ListThread(
	ctx context.Context,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
	options DiscussionPageOptions,
) (DiscussionThreadPage, error) {
	var result DiscussionThreadPage
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			access, err := currentDiscussion(ctx, q, p, target.ConversationID, false)
			if err != nil {
				return err
			}
			result.Root, err = discussionMessageInTx(ctx, q, p, target)
			if err != nil {
				return err
			}
			if result.Root.Type != "user_message" || result.Root.ThreadRootID != nil {
				return ErrDiscussionNotFound
			}
			result.DiscussionPage, err = readDiscussionPage(
				ctx,
				q,
				discussionReadInput{
					Principal: p,
					Access:    access,
					Options:   options,
					RootID:    &target.MessageID,
				},
			)
			if err != nil {
				return err
			}
			result.State, err = threadStateInTx(ctx, q, p, target)
			return err
		},
	)
	return result, err
}

func (s *DefaultDiscussionService) MessageContext(
	ctx context.Context,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
) (DiscussionContext, error) {
	result := DiscussionContext{TargetID: target.MessageID}
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			access, err := currentDiscussion(ctx, q, p, target.ConversationID, false)
			if err != nil {
				return err
			}
			item, err := getDiscussionItem(ctx, q, p, target)
			if err != nil {
				return err
			}
			rootID := util.NullUUIDPtr(item.ThreadRootID)
			input := discussionReadInput{
				Principal: p,
				Access:    access,
				RootID:    rootID,
				Options:   DiscussionPageOptions{Limit: 51},
			}
			args := generated.ListDiscussionMessagesParams{
				CompanyID:      p.CompanyID,
				ConversationID: target.ConversationID,
				ThreadRootID:   item.ThreadRootID,
				ThroughSeq:     item.Seq - 1,
				PageLimit:      25,
			}
			before, err := listDiscussionWindow(ctx, q, args, false)
			if err != nil {
				return err
			}
			args.AfterSeq = item.Seq
			args.ThroughSeq = access.HeadSeq
			after, err := listDiscussionWindow(ctx, q, args, true)
			if err != nil {
				return err
			}
			rows := append([]generated.ListDiscussionMessagesRow{item}, before...)
			rows = append(rows, after...)
			messages := make([]DiscussionMessage, 0, len(rows))
			for _, row := range rows {
				message, err := discussionMessage(row)
				if err != nil {
					return err
				}
				messages = append(messages, message)
			}
			if err = hydrateDiscussionMessages(ctx, q, p, messages); err != nil {
				return err
			}
			budget := maxDiscussionPageBytes - 8192
			if rootID != nil {
				root, err := discussionMessageInTx(
					ctx,
					q,
					p,
					DiscussionMessageTarget{
						ConversationID: target.ConversationID,
						MessageID:      *rootID,
					},
				)
				if err != nil {
					return err
				}
				result.Root = &root
				encoded, err := json.Marshal(root)
				if err != nil {
					return err
				}
				budget -= len(encoded)
			}
			// Include the target first, then nearest neighbors. Once one side hits
			// the byte budget, stop that side so the returned window has no holes.
			encoded, err := json.Marshal(messages[0])
			if err != nil {
				return err
			}
			used := len(encoded)
			selected := []DiscussionMessage{messages[0]}
			for _, bounds := range [][2]int{{1, 1 + len(before)}, {1 + len(before), len(messages)}} {
				for _, message := range messages[bounds[0]:bounds[1]] {
					encoded, err := json.Marshal(message)
					if err != nil {
						return err
					}
					if used+len(encoded)+1 > budget {
						break
					}
					selected = append(selected, message)
					used += len(encoded) + 1
				}
			}
			slices.SortFunc(selected, func(a, b DiscussionMessage) int {
				if a.Seq < b.Seq {
					return -1
				}
				if a.Seq > b.Seq {
					return 1
				}
				return 0
			})
			result.DiscussionPage, err = finishDiscussionPage(
				ctx,
				q,
				discussionPageFinish{
					Input:   input,
					Cursor:  newDiscussionCursor(input),
					Items:   selected,
					Limit:   51,
					Initial: true,
				},
			)
			return err
		},
	)
	return result, err
}

type discussionCollectionCursor struct {
	discussionCursor
	Time  time.Time `json:"time"`
	ID    uuid.UUID `json:"id"`
	Emoji string    `json:"emoji,omitempty"`
}

func (s *DefaultDiscussionService) ListPins(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	options DiscussionPageOptions,
) (DiscussionPins, error) {
	result := DiscussionPins{Items: []DiscussionPin{}}
	limit, err := discussionPageLimit(options.Limit)
	if err != nil {
		return result, err
	}
	err = s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			access, err := currentDiscussion(ctx, q, p, id, false)
			if err != nil {
				return err
			}
			cursor := discussionCollectionCursor{
				discussionCursor: newDiscussionCursor(
					discussionReadInput{
						Principal: p,
						Access:    access,
						Options:   DiscussionPageOptions{Limit: limit},
					},
				),
			}
			cursor.Stream = "pins"
			args := generated.ListDiscussionPinsParams{
				CompanyID:      p.CompanyID,
				ConversationID: id,
				PageLimit:      int32(limit + 1),
			}
			if options.Cursor != "" {
				if err = decodeDiscussionCursor(options.Cursor, &cursor); err != nil {
					return err
				}
				if err = cursor.authorize(p, access); err != nil {
					return err
				}
				if cursor.Stream != "pins" || cursor.RootID != nil || cursor.Time.IsZero() ||
					cursor.ID == uuid.Nil {
					return ErrDiscussionInvalidCursor
				}
				args.SeekTime = pgtype.Timestamptz{Time: cursor.Time, Valid: true}
				args.SeekID = uuid.NullUUID{UUID: cursor.ID, Valid: true}
			}
			pins, err := q.ListDiscussionPins(ctx, args)
			if err != nil {
				return err
			}
			ids := make([]uuid.UUID, 0, len(pins))
			for _, pin := range pins {
				ids = append(ids, pin.MessageID)
			}
			messages, err := discussionMessagesByIDs(
				ctx,
				q,
				p,
				discussionMessageIDs{ConversationID: id, IDs: ids},
			)
			if err != nil {
				return err
			}
			var rootIDs []uuid.UUID
			for _, message := range messages {
				if message.ThreadRootID != nil {
					rootIDs = append(rootIDs, *message.ThreadRootID)
				}
			}
			roots, err := discussionMessagesByIDs(
				ctx,
				q,
				p,
				discussionMessageIDs{ConversationID: id, IDs: rootIDs},
			)
			if err != nil {
				return err
			}
			used := 0
			for i, pin := range pins {
				message := messages[pin.MessageID]
				item := DiscussionPin{
					Message:        message,
					PinnedAt:       pin.PinnedAt.Time.UTC(),
					PinnedByUserID: pin.PinnedByUserID,
				}
				if message.ThreadRootID != nil {
					root := roots[*message.ThreadRootID]
					item.Root = &root
				}
				encoded, err := json.Marshal(item)
				if err != nil {
					return err
				}
				if i == limit || used+len(encoded)+1 > maxDiscussionPageBytes-8192 {
					last := pins[i-1]
					cursor.Time = last.PinnedAt.Time.UTC()
					cursor.ID = last.MessageID
					next := encodeDiscussionCursor(cursor)
					result.NextCursor = &next
					break
				}
				result.Items = append(result.Items, item)
				used += len(encoded) + 1
			}
			result.TotalCount, err = q.CountDiscussionPins(
				ctx,
				generated.CountDiscussionPinsParams{CompanyID: p.CompanyID, ConversationID: id},
			)
			result.ConversationRevision = access.Version
			return err
		},
	)
	return result, err
}

type discussionMessageIDs struct {
	ConversationID uuid.UUID
	IDs            []uuid.UUID
}

func discussionMessagesByIDs(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	input discussionMessageIDs,
) (map[uuid.UUID]DiscussionMessage, error) {
	result := make(map[uuid.UUID]DiscussionMessage, len(input.IDs))
	if len(input.IDs) == 0 {
		return result, nil
	}
	rows, err := q.GetDiscussionMessagesByIDs(
		ctx,
		generated.GetDiscussionMessagesByIDsParams{
			CompanyID:      p.CompanyID,
			ConversationID: input.ConversationID,
			MessageIds:     input.IDs,
		},
	)
	if err != nil {
		return nil, err
	}
	messages := make([]DiscussionMessage, 0, len(rows))
	for _, row := range rows {
		message, err := discussionMessage(generated.ListDiscussionMessagesRow(row))
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err = hydrateDiscussionMessages(ctx, q, p, messages); err != nil {
		return nil, err
	}
	for _, message := range messages {
		result[message.ID] = message
	}
	return result, nil
}

func (s *DefaultDiscussionService) ListReactions(
	ctx context.Context,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
	options DiscussionPageOptions,
) (DiscussionReactions, error) {
	result := DiscussionReactions{Items: []DiscussionReaction{}}
	limit, err := discussionPageLimit(options.Limit)
	if err != nil {
		return result, err
	}
	err = s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			access, err := currentDiscussion(ctx, q, p, target.ConversationID, false)
			if err != nil {
				return err
			}
			if _, err = getDiscussionItem(ctx, q, p, target); err != nil {
				return err
			}
			summaries, err := q.ListDiscussionReactionSummaries(
				ctx,
				generated.ListDiscussionReactionSummariesParams{
					CompanyID:      p.CompanyID,
					ConversationID: target.ConversationID,
					ActorUserID:    p.UserID,
					MessageIds:     []uuid.UUID{target.MessageID},
				},
			)
			if err != nil {
				return err
			}
			indices := make(map[string]int, len(summaries))
			for _, row := range summaries {
				indices[row.Emoji] = len(result.Items)
				result.Items = append(
					result.Items,
					DiscussionReaction{
						DiscussionReactionSummary: DiscussionReactionSummary{
							Emoji:   row.Emoji,
							Count:   row.Count,
							Reacted: row.Reacted,
						},
						Users: []DiscussionAuthor{},
					},
				)
			}
			cursor := discussionCollectionCursor{
				discussionCursor: newDiscussionCursor(
					discussionReadInput{
						Principal: p,
						Access:    access,
						RootID:    &target.MessageID,
						Options:   DiscussionPageOptions{Limit: limit},
					},
				),
			}
			cursor.Stream = "reactions"
			args := generated.ListDiscussionReactionUsersParams{
				CompanyID:      p.CompanyID,
				ConversationID: target.ConversationID,
				MessageID:      target.MessageID,
				PageLimit:      int32(limit + 1),
			}
			if options.Cursor != "" {
				if err = decodeDiscussionCursor(options.Cursor, &cursor); err != nil {
					return err
				}
				if err = cursor.authorize(p, access); err != nil {
					return err
				}
				if cursor.Stream != "reactions" ||
					!sameDiscussionRoot(cursor.RootID, &target.MessageID) ||
					cursor.Emoji == "" ||
					cursor.ID == uuid.Nil {
					return ErrDiscussionInvalidCursor
				}
				args.SeekEmoji = cursor.Emoji
				args.SeekUserID = uuid.NullUUID{UUID: cursor.ID, Valid: true}
			}
			people, err := q.ListDiscussionReactionUsers(ctx, args)
			if err != nil {
				return err
			}
			result.ConversationRevision = access.Version
			encoded, err := json.Marshal(result)
			if err != nil {
				return err
			}
			used := len(encoded)
			for i, row := range people {
				userID := row.UserID
				person := DiscussionAuthor{
					Type:        "user",
					ID:          &userID,
					DisplayName: boundedDiscussionLabel(row.DisplayName),
					AvatarURL:   discussionOptionalString(row.AvatarUrl),
				}
				encoded, err := json.Marshal(person)
				if err != nil {
					return err
				}
				if i == limit || used+len(encoded)+1 > maxDiscussionPageBytes-8192 {
					last := people[i-1]
					cursor.Emoji = last.Emoji
					cursor.ID = last.UserID
					next := encodeDiscussionCursor(cursor)
					result.NextCursor = &next
					break
				}
				item := &result.Items[indices[row.Emoji]]
				item.Users = append(item.Users, person)
				used += len(encoded) + 1
			}
			return nil
		},
	)
	return result, err
}
