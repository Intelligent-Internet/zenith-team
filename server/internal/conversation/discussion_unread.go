package conversation

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// DiscussionReadInput fences queued device updates to the membership they saw.
// Zero manualUnreadSeq clears the marker; absent leaves it unchanged.
type DiscussionReadInput struct {
	ReadThroughSeq   *int64
	ManualUnreadSeq  *int64
	ExpectedRevision *int64
	GrantGeneration  int64
}

func refreshDiscussionUnread(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, id uuid.UUID) error {
	if err := q.RefreshDiscussionRootUnread(
		ctx,
		generated.RefreshDiscussionRootUnreadParams{ConversationID: id, UserID: p.UserID},
	); err != nil {
		return err
	}
	return q.RefreshDiscussionThreadUnread(
		ctx,
		generated.RefreshDiscussionThreadUnreadParams{ConversationID: id, UserID: p.UserID},
	)
}

type discussionUnreadDelta struct {
	ConversationID uuid.UUID
	RootID         uuid.NullUUID
	AuthorID       uuid.UUID
	Seq            int64
	Delta          int64
}

func applyDiscussionUnreadDelta(ctx context.Context, q *generated.Queries, input discussionUnreadDelta) error {
	if input.RootID.Valid {
		return q.ApplyDiscussionThreadUnreadDelta(
			ctx,
			generated.ApplyDiscussionThreadUnreadDeltaParams{
				ConversationID: input.ConversationID,
				RootID:         input.RootID.UUID,
				AuthorID:       input.AuthorID,
				Seq:            input.Seq,
				Delta:          input.Delta,
			},
		)
	}
	return q.ApplyDiscussionRootUnreadDelta(
		ctx,
		generated.ApplyDiscussionRootUnreadDeltaParams{
			ConversationID: input.ConversationID,
			AuthorID:       input.AuthorID,
			Seq:            input.Seq,
			Delta:          input.Delta,
		},
	)
}

func normalizeDiscussionReadInput(input DiscussionReadInput) error {
	if input.ReadThroughSeq == nil && input.ManualUnreadSeq == nil ||
		!discussionRevisionValid(input.GrantGeneration) {
		return ErrDiscussionInvalid
	}
	for _, seq := range []*int64{input.ReadThroughSeq, input.ManualUnreadSeq, input.ExpectedRevision} {
		if seq != nil && (*seq < 0 || *seq > maxDiscussionInteger) {
			return ErrDiscussionInvalid
		}
	}
	if input.ManualUnreadSeq != nil && input.ExpectedRevision == nil {
		return ErrDiscussionInvalid
	}
	return nil
}

func nextDiscussionReadState(
	ctx context.Context,
	q *generated.Queries,
	target DiscussionMessageTarget,
	current, input DiscussionReadInput,
) (int64, pgtype.Int8, error) {
	read := *current.ReadThroughSeq
	manual := pgtype.Int8{}
	if current.ManualUnreadSeq != nil {
		manual = pgtype.Int8{Int64: *current.ManualUnreadSeq, Valid: true}
	}
	for _, seq := range []*int64{input.ReadThroughSeq, input.ManualUnreadSeq} {
		if seq == nil || *seq == 0 {
			continue
		}
		if _, err := q.GetDiscussionReadPosition(
			ctx,
			generated.GetDiscussionReadPositionParams{
				ConversationID: target.ConversationID,
				RootID: uuid.NullUUID{
					UUID:  target.MessageID,
					Valid: target.MessageID != uuid.Nil,
				},
				Seq: *seq,
			},
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, manual, ErrDiscussionInvalid
			}
			return 0, manual, err
		}
	}
	if input.ManualUnreadSeq != nil {
		if *input.ExpectedRevision != *current.ExpectedRevision {
			return 0, manual, ErrDiscussionRevisionConflict
		}
		manual = pgtype.Int8{Int64: *input.ManualUnreadSeq, Valid: *input.ManualUnreadSeq > 0}
	}
	if input.ReadThroughSeq != nil {
		read = max(read, *input.ReadThroughSeq)
		// A read started before another device marked unread cannot clear that marker.
		if input.ManualUnreadSeq == nil && manual.Valid && *input.ReadThroughSeq >= manual.Int64 &&
			input.ExpectedRevision != nil &&
			*input.ExpectedRevision == *current.ExpectedRevision {
			manual = pgtype.Int8{}
		}
	}
	return read, manual, nil
}

func (s *DefaultDiscussionService) UpdateReadState(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input DiscussionReadInput,
) (DiscussionUserState, error) {
	if err := normalizeDiscussionReadInput(input); err != nil {
		return DiscussionUserState{}, err
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
		if access.GrantGeneration.Int64 != input.GrantGeneration {
			return ErrDiscussionRevisionConflict
		}
		read, manual, err := nextDiscussionReadState(
			ctx,
			q,
			DiscussionMessageTarget{ConversationID: id},
			DiscussionReadInput{
				ReadThroughSeq:   &access.MainReadSeq,
				ManualUnreadSeq:  discussionInt(access.ManualUnreadSeq),
				ExpectedRevision: &access.StateRevision.Int64,
			},
			input,
		)
		if err != nil {
			return err
		}
		if read != access.MainReadSeq || manual != access.ManualUnreadSeq {
			row, err := q.SetDiscussionReadState(
				ctx,
				generated.SetDiscussionReadStateParams{
					ConversationID:  id,
					UserID:          p.UserID,
					GrantGeneration: input.GrantGeneration,
					ReadSeq:         read,
					ManualUnreadSeq: manual,
				},
			)
			if err != nil {
				return err
			}
			if err = refreshDiscussionUnread(ctx, q, p, id); err != nil {
				return err
			}
			pending = append(pending, discussionStateEvent(row))
		}
		access, err = currentDiscussion(ctx, q, p, id, false)
		if err != nil {
			return err
		}
		result = *discussionUserState(access)
		return nil
	})
	if err == nil {
		publishDiscussionEvents(ctx, s.bus, pending)
	}
	return result, err
}

func (s *DefaultDiscussionService) updateThreadReadState(
	ctx context.Context,
	p auth.UserPrincipal,
	target DiscussionMessageTarget,
	input DiscussionReadInput,
) (DiscussionThreadState, error) {
	if err := normalizeDiscussionReadInput(input); err != nil {
		return DiscussionThreadState{}, err
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
		if access.GrantGeneration.Int64 != input.GrantGeneration {
			return ErrDiscussionRevisionConflict
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
		read, manual, err := nextDiscussionReadState(
			ctx,
			q,
			target,
			DiscussionReadInput{
				ReadThroughSeq:   &state.ReadThroughSeq,
				ManualUnreadSeq:  state.ManualUnreadSeq,
				ExpectedRevision: &state.Revision,
			},
			input,
		)
		if err != nil {
			return err
		}
		if read != state.ReadThroughSeq || (manual.Valid != (state.ManualUnreadSeq != nil)) ||
			manual.Valid && manual.Int64 != *state.ManualUnreadSeq {
			if err = q.SetDiscussionThreadReadState(
				ctx,
				generated.SetDiscussionThreadReadStateParams{
					CompanyID:       p.CompanyID,
					ConversationID:  target.ConversationID,
					RootID:          target.MessageID,
					UserID:          p.UserID,
					GrantGeneration: input.GrantGeneration,
					ReadSeq:         read,
					ManualUnreadSeq: manual,
				},
			); err != nil {
				return err
			}
			if err = refreshDiscussionUnread(ctx, q, p, target.ConversationID); err != nil {
				return err
			}
			row, err := q.TouchDiscussionUserState(
				ctx,
				generated.TouchDiscussionUserStateParams{
					ConversationID: target.ConversationID,
					UserID:         p.UserID,
				},
			)
			if err != nil {
				return err
			}
			pending = append(pending, discussionStateEvent(row))
		}
		result, err = threadStateInTx(ctx, q, p, target)
		return err
	})
	if err == nil {
		publishDiscussionEvents(ctx, s.bus, pending)
	}
	return result, err
}
