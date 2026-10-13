package canvas

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// CreateWhiteboardInTx makes a Whiteboard Artifact the member owns, made in
// the chat conversationID, with an empty Board under the Artifact's ID. It
// participates in the caller's send transaction: the caller holds the
// Company/membership and chat locks and shares the Artifact into the chat.
func CreateWhiteboardInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	conversationID uuid.UUID,
	title string,
) (uuid.UUID, error) {
	title, err := boardTitle(title)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := q.CreateWhiteboardOutput(
		ctx,
		generated.CreateWhiteboardOutputParams{
			CompanyID:      p.CompanyID,
			Title:          title,
			ActorUserID:    uuid.NullUUID{UUID: p.UserID, Valid: true},
			ConversationID: uuid.NullUUID{UUID: conversationID, Valid: true},
		},
	)
	if err != nil {
		return uuid.Nil, err
	}
	return id, q.CreateBoard(ctx, generated.CreateBoardParams{CompanyID: p.CompanyID, ID: id})
}

// Rename retitles a Whiteboard Artifact for someone who may edit its Board.
// Every reader of the Artifact hears of it, not only those with the Board
// open, since the title also names it in chats and Artifact lists.
func (s *DefaultBoardService) Rename(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	title string,
) (Board, error) {
	title, err := boardTitle(title)
	if err != nil {
		return Board{}, err
	}
	var result Board
	var pending events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		b, viewer, err := boardScopeLock(ctx, q, p, scope, true)
		if err != nil {
			return err
		}
		if err := q.RenameWhiteboardOutput(ctx, generated.RenameWhiteboardOutputParams{
			Title: title, CompanyID: p.CompanyID, ID: b.ID,
		}); err != nil {
			return err
		}
		b.Title = title
		result = boardDTO(b, viewer)
		pending = boardChangeEvent(p, b.ID, b.HeadSeq)
		pending.Name = EventBoardRenamed
		return stampBoardAudience(ctx, q, &pending)
	})
	if err != nil {
		return Board{}, boardError(err)
	}
	s.publishEvent(ctx, pending)
	return result, nil
}

func boardTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > boardMaxTitle {
		return "", ErrBoardInvalid
	}
	return title, nil
}
