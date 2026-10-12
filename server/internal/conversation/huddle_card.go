package conversation

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// HuddleCard is a read projection, not a mutable copy of the call's lifecycle.
// ChatConversationID is disclosed only when the reader has a retained chat grant.
type HuddleCard struct {
	ID                 uuid.UUID   `json:"id"`
	State              HuddleState `json:"state"`
	StartedAt          time.Time   `json:"startedAt"`
	EndedAt            *time.Time  `json:"endedAt"`
	DurationSeconds    int64       `json:"durationSeconds"`
	ParticipantCount   int64       `json:"participantCount"`
	ChatConversationID *uuid.UUID  `json:"chatConversationId"`
}

func insertHuddleCard(ctx context.Context, q *generated.Queries, h generated.Huddle) (events.Event, error) {
	seq, err := q.AllocateDiscussionSequence(ctx, generated.AllocateDiscussionSequenceParams{
		CompanyID: h.CompanyID, ConversationID: h.SourceConversationID,
	})
	if err != nil {
		return events.Event{}, err
	}
	id, err := q.InsertHuddleSourceCard(ctx, generated.InsertHuddleSourceCardParams{
		CompanyID: h.CompanyID, ConversationID: h.SourceConversationID, Seq: seq,
	})
	if err != nil {
		return events.Event{}, fmt.Errorf("insert huddle source card: %w", err)
	}
	if err := q.SetHuddleSourceCard(ctx, generated.SetHuddleSourceCardParams{
		CompanyID: h.CompanyID, HuddleID: h.ID, ItemID: id,
	}); err != nil {
		return events.Event{}, err
	}
	source, err := q.BumpDiscussionConversation(ctx, generated.BumpDiscussionConversationParams{
		CompanyID: h.CompanyID, ConversationID: h.SourceConversationID,
	})
	if err != nil {
		return events.Event{}, err
	}
	return discussionChangedEvent(source), nil
}

func hydrateHuddleCards(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	messages []DiscussionMessage,
) error {
	var ids []uuid.UUID
	byID := make(map[uuid.UUID]*DiscussionMessage)
	for i := range messages {
		if messages[i].System != nil && messages[i].System.Action == "huddle_call" {
			ids = append(ids, messages[i].ID)
			byID[messages[i].ID] = &messages[i]
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.ListHuddleSourceCards(ctx, generated.ListHuddleSourceCardsParams{
		CompanyID: p.CompanyID, ConversationID: messages[0].ConversationID, MessageIds: ids,
	})
	if err != nil {
		return fmt.Errorf("read huddle cards: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	accessArgs := discussionParameters(p)
	accessArgs.PageLimit = int32(len(rows))
	for _, row := range rows {
		accessArgs.ConversationIds = append(accessArgs.ConversationIds, row.ChatConversationID)
	}
	access, err := q.ListDiscussionAccess(ctx, accessArgs)
	if err != nil {
		return fmt.Errorf("read huddle history access: %w", err)
	}
	readable := make(map[uuid.UUID]bool, len(access))
	for _, row := range access {
		readable[row.ID] = row.ContentAccess
	}
	for _, row := range rows {
		card := &HuddleCard{
			ID: row.ID, State: HuddleState(row.State), StartedAt: row.StartedAt.Time.UTC(),
			EndedAt: util.TimestampToTime(row.EndedAt), DurationSeconds: row.DurationSeconds,
			ParticipantCount: row.ParticipantCount,
		}
		if readable[row.ChatConversationID] {
			card.ChatConversationID = &row.ChatConversationID
		}
		byID[row.SourceItemID.UUID].Huddle = card
	}
	return nil
}

// A read invalidation needs no source lock or revision mutation while the
// lifecycle transaction owns the Huddle lock. REST repairs lost hints.
func huddleCardChangedEvent(h generated.Huddle) events.Event {
	return events.Event{
		EntityType: Topic, Name: EventDiscussionHuddleChanged, CompanyID: h.CompanyID,
		ConversationID: h.SourceConversationID, CreatedAt: time.Now().UTC(), Actor: events.SystemActor(),
		Payload: DiscussionHuddleChanged{ConversationID: h.SourceConversationID, HuddleID: h.ID, Revision: h.Revision},
	}
}

type DiscussionHuddleChanged struct {
	ConversationID uuid.UUID `json:"conversationId"`
	HuddleID       uuid.UUID `json:"huddleId"`
	Revision       int64     `json:"revision"`
}
