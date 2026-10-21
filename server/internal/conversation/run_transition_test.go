package conversation

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// runTransitionRowFixtures mirrors the bulk-query rows the transition reads:
// bumped Conversation rows arrive unsorted (reversed here), every Conversation
// is a Task Conversation except directID, which routes to its creator.
func runTransitionRowFixtures(
	companyID uuid.UUID,
	ids []uuid.UUID,
	directID, directCreatorID, directAgentID uuid.UUID,
	now time.Time,
) (
	[]generated.Conversation,
	[]generated.ListRunConversationSummaryBasesBulkRow,
) {
	rows := make([]generated.Conversation, len(ids))
	bases := make([]generated.ListRunConversationSummaryBasesBulkRow, len(ids))
	for index, conversationID := range ids {
		reversedIndex := len(ids) - index - 1
		rows[reversedIndex] = generated.Conversation{
			ID: conversationID, CompanyID: companyID, Kind: "task",
			TaskID:    uuid.NullUUID{UUID: uuid.Must(uuid.NewV7()), Valid: true},
			CreatedAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
			UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
			Version:   2,
		}
		if conversationID == directID {
			rows[reversedIndex].Kind = "direct"
			rows[reversedIndex].TaskID = uuid.NullUUID{}
			rows[reversedIndex].AgentID = uuid.NullUUID{UUID: directAgentID, Valid: true}
			rows[reversedIndex].CreatedByUserID = uuid.NullUUID{UUID: directCreatorID, Valid: true}
		}
		bases[index] = generated.ListRunConversationSummaryBasesBulkRow{
			ConversationID: conversationID,
			UpdatedAt:      pgtype.Timestamptz{Time: now, Valid: true},
		}
	}
	return rows, bases
}

func TestBuildRunConversationTransitionEventsKeepsSortedOrderAndDirectCreatorRouting(t *testing.T) {
	t.Parallel()

	companyID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	conversationIDs := make([]uuid.UUID, 100)
	for index := range conversationIDs {
		conversationIDs[index] = uuid.Must(uuid.NewV7())
	}
	directID := conversationIDs[0]
	directCreatorID, directAgentID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	ids := append([]uuid.UUID(nil), conversationIDs...)
	sortUUIDs(ids)
	rows, bases := runTransitionRowFixtures(companyID, ids, directID, directCreatorID, directAgentID, now)
	sortBumpedRunConversations(rows)

	wireEvents := buildRunConversationTransitionEvents(
		"system", nil, rows, bases, nil, nil,
	)
	if len(wireEvents) != len(conversationIDs) {
		t.Fatalf("Run Conversation wire event count = %d, want %d", len(wireEvents), len(conversationIDs))
	}
	// Staged events are opaque, and the bus preserves publication order, so
	// the sorted order the transition promises is read at the subscriber.
	bus := newTestBus(t)
	received := recordTopic[Snapshot](t, bus, Topic)
	for _, group := range events.GroupByTopic(wireEvents) {
		bus.Publish(context.Background(), group.Topic, group.Events...)
	}

	published := received.snapshot()
	if len(published) != len(conversationIDs) {
		t.Fatalf("published Conversation events = %d, want %d", len(published), len(conversationIDs))
	}
	directEvents := 0
	for index, event := range published {
		if event.Name != EventConversationUpdated {
			t.Fatalf("Run Conversation wire event name[%d] = %q", index, event.Name)
		}
		payload := event.Payload
		if payload.Conversation.ID != ids[index] {
			t.Fatalf(
				"Run Conversation wire event ID[%d] = %s, want %s",
				index,
				payload.Conversation.ID,
				ids[index],
			)
		}
		if event.Actor != events.SystemActor() {
			t.Fatalf("system Conversation wire event actor = %+v", event.Actor)
		}
		if payload.Conversation.Kind == KindDirect {
			directEvents++
			if payload.Conversation.CreatedByUserID == nil ||
				*payload.Conversation.CreatedByUserID != directCreatorID {
				t.Fatalf("direct Conversation wire event payload omits its creator: %#v", payload)
			}
			if payload.Conversation.TaskID != nil {
				t.Fatalf("direct Conversation wire event payload carries a task id: %#v", payload)
			}
		} else if payload.Conversation.TaskID == nil {
			t.Fatalf("task Conversation wire event payload omits its task id: %#v", payload)
		}
	}
	if directEvents != 1 {
		t.Fatalf("direct Conversation wire event count = %d, want 1", directEvents)
	}
}

func TestBuildRunConversationTransitionEventsAcceptsAgentActor(t *testing.T) {
	t.Parallel()

	companyID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	ids := []uuid.UUID{uuid.Must(uuid.NewV7())}
	rows, bases := runTransitionRowFixtures(companyID, ids, uuid.Nil, uuid.Nil, uuid.Nil, now)

	wireEvents := buildRunConversationTransitionEvents(
		"agent", &agentID, rows, bases, nil, nil,
	)
	if len(wireEvents) != 1 {
		t.Fatalf("buildRunConversationTransitionEvents(agent) = %d events, want 1", len(wireEvents))
	}
	bus := newTestBus(t)
	received := recordTopic[Snapshot](t, bus, Topic)
	for _, group := range events.GroupByTopic(wireEvents) {
		bus.Publish(context.Background(), group.Topic, group.Events...)
	}

	published := received.snapshot()
	if len(published) != 1 {
		t.Fatalf("published Conversation events = %d, want 1", len(published))
	}
	if published[0].Name != EventConversationUpdated {
		t.Fatalf("agent Conversation wire event name = %q", published[0].Name)
	}
	if published[0].Actor != events.AgentActor(agentID.String()) {
		t.Fatalf("agent Conversation wire event actor = %+v", published[0].Actor)
	}
}

func TestBuildRunConversationTransitionEventsOmitsScheduleConversationFromChatEvents(t *testing.T) {
	t.Parallel()

	companyID := uuid.Must(uuid.NewV7())
	creatorID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	scheduleID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, time.August, 17, 1, 2, 3, 0, time.UTC)
	rows := []generated.Conversation{{
		ID: conversationID, CompanyID: companyID, Kind: "schedule",
		AgentID:         uuid.NullUUID{UUID: agentID, Valid: true},
		CreatedByUserID: uuid.NullUUID{UUID: creatorID, Valid: true},
		ScheduleID:      uuid.NullUUID{UUID: scheduleID, Valid: true},
		CreatedAt:       pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
		UpdatedAt:       pgtype.Timestamptz{Time: now, Valid: true},
		Version:         2,
	}}
	bases := []generated.ListRunConversationSummaryBasesBulkRow{{
		ConversationID: conversationID,
		UpdatedAt:      pgtype.Timestamptz{Time: now, Valid: true},
	}}

	wireEvents := buildRunConversationTransitionEvents(
		"system", nil, rows, bases, nil, nil,
	)
	if len(wireEvents) != 0 {
		t.Fatalf("Schedule Conversation emitted %d chat events, want 0", len(wireEvents))
	}
}
