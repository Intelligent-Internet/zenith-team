package conversation

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestAssembleDirectListItemsMergesBulkProjectionRows(t *testing.T) {
	t.Parallel()

	actorID, companyID, agentID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := pgtype.Timestamptz{Time: time.Date(2026, time.July, 13, 4, 5, 6, 0, time.UTC), Valid: true}
	rows := make([]generated.Conversation, 100)
	bases := make([]generated.ListDirectConversationSummaryBasesBulkRow, 0, len(rows))
	for index := range rows {
		conversationID := uuid.Must(uuid.NewV7())
		rows[index] = generated.Conversation{
			ID:              conversationID,
			CompanyID:       companyID,
			Kind:            "direct",
			AgentID:         uuid.NullUUID{UUID: agentID, Valid: true},
			CreatedByUserID: uuid.NullUUID{UUID: actorID, Valid: true},
			CreatedAt:       now,
			UpdatedAt:       now,
			Version:         1,
		}
		bases = append(bases, generated.ListDirectConversationSummaryBasesBulkRow{
			ConversationID: conversationID, UpdatedAt: now,
		})
	}
	items := assembleDirectListItems(rows, bases, nil, nil, nil)
	if len(items) != len(rows) {
		t.Fatalf("assembleDirectListItems() items = %d", len(items))
	}
	for index, item := range items {
		if item.Conversation.ID != rows[index].ID || item.UserState.UserID != actorID ||
			item.Summary.Runs.ActiveCount != 0 || item.Summary.Runs.Latest != nil {
			t.Fatalf("direct list item %d = %+v", index, item)
		}
	}
}
