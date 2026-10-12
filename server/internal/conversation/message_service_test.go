package conversation

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestUserStateFromConversationRowMapsDirectCreatorState(t *testing.T) {
	t.Parallel()

	creatorID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, time.July, 13, 4, 5, 6, 0, time.UTC)
	row := generated.Conversation{
		ID: uuid.Must(uuid.NewV7()), CompanyID: uuid.Must(uuid.NewV7()),
		Kind:            "direct",
		CreatedByUserID: uuid.NullUUID{UUID: creatorID, Valid: true},
		PinnedAt:        pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
		CreatedAt:       pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
		UpdatedAt:       pgtype.Timestamptz{Time: now, Valid: true},
		Version:         4,
	}

	state := userStateFromConversationRow(row)
	if state.ConversationID != row.ID || state.UserID != creatorID ||
		state.PinnedAt == nil || state.ArchivedAt != nil || state.Revision != 4 {
		t.Fatalf("userStateFromConversationRow(direct) = %+v", state)
	}
}
