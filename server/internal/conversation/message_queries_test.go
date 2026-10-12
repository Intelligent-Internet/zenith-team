package conversation

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestMessagePageSkeletonKeepsCanonicalOrderAndEmptyAttachmentsForLargePage(t *testing.T) {
	t.Parallel()

	actorID, conversationID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := pgtype.Timestamptz{Time: time.Date(2026, time.July, 13, 4, 5, 6, 0, time.UTC), Valid: true}
	rows := make([]generated.ConversationItem, 100)
	for index := range rows {
		rows[index] = generated.ConversationItem{
			ID:             uuid.Must(uuid.NewV7()),
			ConversationID: conversationID,
			Seq:            int64(index + 1),
			RunID: uuid.NullUUID{
				UUID:  uuid.MustParse("00000000-0000-4000-8000-000000000007"),
				Valid: true,
			},
			Type:       "user_message",
			AuthorKind: "user",
			AuthorID:   uuid.NullUUID{UUID: actorID, Valid: true},
			Metadata:   json.RawMessage(`{}`),
			Content:    "Message",
			CreatedAt:  now,
		}
	}
	skeleton := newMessagePageSkeleton(rows)
	if len(skeleton.items) != len(rows) || len(skeleton.messageIDs) != len(rows) {
		t.Fatalf("newMessagePageSkeleton() items = %d, IDs = %d",
			len(skeleton.items), len(skeleton.messageIDs))
	}
	skeleton.applyAttachments(nil)
	for index, item := range skeleton.items {
		if item.Message.ID != rows[index].ID || item.Attachments == nil ||
			len(item.Attachments) != 0 {
			t.Fatalf("Message view %d = %+v", index, item)
		}
	}
}

// The projection reads `type` and `run_id` off the row untranslated. run_id is
// the Run the item belongs to whoever wrote it, so an agent_text row and the
// user_message row of the same turn report the same Run — which is what lets
// the Task workspace group a turn's items under one timeline.
func TestMessageFromRowProjectsTypeRunAndContentForBothAuthors(t *testing.T) {
	t.Parallel()

	agentID, userID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	createdAt := pgtype.Timestamptz{
		Time:  time.Date(2026, time.August, 3, 10, 0, 0, 0, time.UTC),
		Valid: true,
	}
	answer := messageFromRow(generated.ConversationItem{
		ID:             uuid.Must(uuid.NewV7()),
		ConversationID: conversationID,
		Seq:            2,
		RunID: uuid.NullUUID{
			UUID:  uuid.MustParse("00000000-0000-4000-8000-000000000091"),
			Valid: true,
		},
		Type:       "agent_text",
		AuthorKind: "agent",
		AuthorID:   uuid.NullUUID{UUID: agentID, Valid: true},
		Content:    "assistant output\n",
		CreatedAt:  createdAt,
	})
	if answer.Content != "assistant output\n" || answer.Type != ItemTypeAgentText ||
		answer.RunID != uuid.MustParse(
			"00000000-0000-4000-8000-000000000091",
		) || answer.Author.Kind != ActorAgent || answer.Author.ID != agentID {
		t.Fatalf("messageFromRow(agent_text) = %+v", answer)
	}
	prompt := messageFromRow(generated.ConversationItem{
		ID:             uuid.Must(uuid.NewV7()),
		ConversationID: conversationID,
		Seq:            1,
		RunID: uuid.NullUUID{
			UUID:  uuid.MustParse("00000000-0000-4000-8000-000000000091"),
			Valid: true,
		},
		Type:       "user_message",
		AuthorKind: "user",
		AuthorID:   uuid.NullUUID{UUID: userID, Valid: true},
		Content:    "do the thing",
		CreatedAt:  createdAt,
	})
	if prompt.Type != ItemTypeUserMessage || prompt.RunID != answer.RunID ||
		prompt.Author.Kind != ActorUser || prompt.Author.ID != userID {
		t.Fatalf("messageFromRow(user_message) = %+v", prompt)
	}
}
