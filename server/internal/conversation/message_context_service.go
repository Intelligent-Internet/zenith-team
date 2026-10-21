package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func (s *DefaultConversationService) FetchMessageContext(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	messageID uuid.UUID,
	limit int,
) (MessageContextPage, error) {
	if !util.ValidUUID(conversationID) || !util.ValidUUID(messageID) {
		return MessageContextPage{}, ErrNotFound
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > maxListLimit {
		return MessageContextPage{}, ErrInvalidInput
	}
	target, err := s.store.Queries().
		GetAuthorizedMessageContextTarget(ctx, generated.GetAuthorizedMessageContextTargetParams{
			ActorUserID: principal.UserID, MessageID: messageID,
			ConversationID: conversationID,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return MessageContextPage{}, ErrNotFound
	}
	if err != nil {
		return MessageContextPage{}, fmt.Errorf("authorize Message context: %w", err)
	}
	items, err := s.messageContextWindow(ctx, principal.UserID, conversationID, target, limit)
	if err != nil {
		return MessageContextPage{}, fmt.Errorf("load Message context: %w", err)
	}
	found := false
	for index := range items {
		if items[index].Message.ID == messageID && items[index].Message.Seq == target.TargetSeq {
			found = true
			break
		}
	}
	if !found {
		// The authorization query resolves any item of the Conversation, but
		// this surface renders the chat timeline — two of the six item types.
		// A deep link to a tool call or a reasoning item therefore names a row
		// that exists and is readable and still has no place here, which is a
		// miss on this surface rather than a store fault.
		return MessageContextPage{}, ErrNotFound
	}
	if items == nil {
		items = []MessageView{}
	}
	return MessageContextPage{
		Items: items, TargetSeq: target.TargetSeq, EarliestAvailableSeq: 1,
		ThroughSeq: target.ThroughSeq,
	}, nil
}

// messageContextWindow reads `limit` chat messages centered on the target. The
// window is a COUNT of messages, not a seq range: seq numbers every item a
// Run wrote, so a range of width limit can hold one message or limit of them
// (see newestMessageViews). The newer half is read forward from the target,
// then the rest backward through it, so a target near the end of the stream
// still gets a full window; when nothing older fills it, the forward read is
// widened instead, so a target near the start does too.
func (s *DefaultConversationService) messageContextWindow(
	ctx context.Context,
	actorID uuid.UUID,
	conversationID uuid.UUID,
	target generated.GetAuthorizedMessageContextTargetRow,
	limit int,
) ([]MessageView, error) {
	q := s.store.Queries()
	newer := func(count int) ([]MessageView, error) {
		if count == 0 {
			return nil, nil
		}
		return listMessageViews(
			ctx, q, actorID, target.CompanyID, conversationID,
			target.TargetSeq, target.ThroughSeq, count,
		)
	}
	newerWanted := limit - limit/2 - 1
	after, err := newer(newerWanted)
	if err != nil {
		return nil, err
	}
	before, err := newestMessageViews(
		ctx, q, actorID, target.CompanyID, conversationID, target.TargetSeq, limit-len(after),
	)
	if err != nil {
		return nil, err
	}
	if len(before)+len(after) < limit && len(after) == newerWanted {
		if after, err = newer(limit - len(before)); err != nil {
			return nil, err
		}
	}
	return append(before, after...), nil
}
