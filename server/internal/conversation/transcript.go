package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// TranscriptEntry is one immutable provider item in a Conversation. RunID
// identifies the execution that produced it; Seq is the Conversation-wide
// ordering and reconnect cursor.
type TranscriptEntry = execution.TranscriptEntry

// TranscriptListOptions selects one ascending page after a Conversation seq.
type TranscriptListOptions struct {
	AfterSeq int64
	Limit    int32
}

// TranscriptPage is one bounded, ascending page of a Conversation transcript.
// ThroughSeq is the newest provider item's Conversation seq at the start of
// the read, not the final row of this page.
type TranscriptPage struct {
	Items      []TranscriptEntry
	ThroughSeq int64
	HasMore    bool
}

// Transcript reads all durable provider items in one visible Conversation,
// across every Run that contributed to it.
func (s *DefaultConversationService) Transcript(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	options TranscriptListOptions,
) (TranscriptPage, error) {
	if conversationID == uuid.Nil || options.AfterSeq < 0 || options.Limit < 1 || options.Limit > 200 {
		return TranscriptPage{}, ErrInvalidInput
	}

	queries := s.store.Queries()
	throughSeq, err := queries.GetConversationTranscriptWatermark(
		ctx,
		generated.GetConversationTranscriptWatermarkParams{
			ActorUserID:    principal.UserID,
			ConversationID: conversationID,
			CompanyID:      principal.CompanyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return TranscriptPage{}, ErrNotFound
	}
	if err != nil {
		return TranscriptPage{}, fmt.Errorf("read Conversation transcript watermark: %w", err)
	}

	rows, err := queries.ListConversationTranscriptItems(
		ctx,
		generated.ListConversationTranscriptItemsParams{
			ActorUserID:    principal.UserID,
			ConversationID: conversationID,
			CompanyID:      principal.CompanyID,
			AfterSeq:       options.AfterSeq,
			ThroughSeq:     throughSeq,
			PageSize:       options.Limit + 1,
		},
	)
	if err != nil {
		return TranscriptPage{}, fmt.Errorf("list Conversation transcript: %w", err)
	}
	hasMore := len(rows) > int(options.Limit)
	if hasMore {
		rows = rows[:options.Limit]
	}
	items, err := execution.MapTranscriptRows(rows)
	if err != nil {
		return TranscriptPage{}, fmt.Errorf("decode Conversation transcript: %w", err)
	}
	return TranscriptPage{Items: items, ThroughSeq: throughSeq, HasMore: hasMore}, nil
}
