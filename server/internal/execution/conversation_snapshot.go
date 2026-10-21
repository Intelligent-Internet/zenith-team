package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// conversationSnapshotMaxItems bounds a Run's frozen chat history. The query
// asks for one row more to learn whether older chat exists, and the trim loop
// drops whole oldest items back down to it.
const conversationSnapshotMaxItems = 64

// conversationSnapshotMaxBytes bounds the encoded snapshot and, on its own,
// any single item in it: an item that alone exceeds the budget is left out
// whole rather than cut, because a sliced message reads as a different one.
const conversationSnapshotMaxBytes = 64 << 10

// runContextActor is one message author as the frozen context names it. The
// JSON tags are the wire contract with internal/daemon.RunConversationAuthor:
// separate types, identical wire.
type runContextActor struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// runTriggerBrief is who wrote the words a Run starts on, frozen beside the
// prompt so the agent can address the person it is answering.
type runTriggerBrief struct {
	Author    runContextActor `json:"author"`
	CreatedAt time.Time       `json:"createdAt"`
}

// runConversationSnapshotItem is one chat message inside the snapshot.
type runConversationSnapshotItem struct {
	ID        string          `json:"id"`
	Seq       int64           `json:"seq"`
	RunID     string          `json:"runId"`
	Author    runContextActor `json:"author"`
	CreatedAt time.Time       `json:"createdAt"`
	Content   string          `json:"content"`
}

// runConversationSnapshot is the bounded chat history a Run carries so the
// agent can read what was said before even when its provider session is new.
// Items ascend by seq, hold chat only, and never include the trigger, which
// the caller writes after the Run row exists. ThroughSeq is the whole stream's
// watermark before the trigger, not an item count.
//
// A Task Run's snapshot ends with the Messages colleagues queued while only
// the assignee could start it (ADR 0014): they are not in the transcript yet
// when the snapshot is frozen, but the caller materializes them, in this
// order and with these ids, right after the Run row and before the trigger,
// under the same Conversation lock — so the seqs the snapshot assigns are the
// seqs they receive, and ThroughSeq already counts them. Their runId is empty
// because they belong to the Run being enqueued, whose id does not exist yet.
type runConversationSnapshot struct {
	SchemaVersion  int                           `json:"schemaVersion"`
	Kind           string                        `json:"kind"`
	ConversationID string                        `json:"conversationId"`
	ThroughSeq     int64                         `json:"throughSeq"`
	Truncated      bool                          `json:"truncated"`
	Items          []runConversationSnapshotItem `json:"items"`
}

// runContextQueuedTaskMessages reads the colleagues' queued Messages a Task
// Run carries into its transcript (ADR 0014), the list the Conversation
// domain materializes after the enqueue. Other Runs carry none.
func runContextQueuedTaskMessages(
	ctx context.Context,
	q *db.Queries,
	in RunTrigger,
) ([]db.ListQueuedRunQueueItemsForTaskRunRow, error) {
	if !in.TaskID.Valid {
		return nil, nil
	}
	queued, err := q.ListQueuedRunQueueItemsForTaskRun(ctx, db.ListQueuedRunQueueItemsForTaskRunParams{
		ConversationID: in.ConversationID, CompanyID: in.CompanyID,
		AuthorizedByUserID: in.AuthorizedByUserID,
	})
	if err != nil {
		return nil, fmt.Errorf("list queued Messages for the frozen Task snapshot: %w", err)
	}
	return queued, nil
}

// runContextConversationSnapshot freezes the Conversation's recent chat for
// the Run being enqueued. The caller holds the Conversation lock, so the
// watermark read here is exactly the last item before the trigger the caller
// is about to write. A Task Run also freezes the colleagues' queued Messages
// (see runConversationSnapshot) as the items right after the watermark.
func runContextConversationSnapshot(
	ctx context.Context,
	q *db.Queries,
	in RunTrigger,
	queued []db.ListQueuedRunQueueItemsForTaskRunRow,
) (json.RawMessage, error) {
	head, err := q.GetConversationContextSnapshotHead(ctx, db.GetConversationContextSnapshotHeadParams{
		ConversationID: in.ConversationID, CompanyID: in.CompanyID,
	})
	if err != nil {
		return nil, fmt.Errorf("load frozen Conversation snapshot head: %w", err)
	}
	rows, err := q.ListChatItemsForContextSnapshot(ctx, db.ListChatItemsForContextSnapshotParams{
		ConversationID: in.ConversationID, CompanyID: in.CompanyID,
		ItemLimit: conversationSnapshotMaxItems + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("list frozen Conversation snapshot items: %w", err)
	}
	return boundConversationSnapshot(head.Kind, in.ConversationID.String(), head.ThroughSeq, rows, queued)
}

// boundConversationSnapshot turns the newest-first query rows into the frozen
// document: ascending, within the item and byte budgets. The limits drop whole
// oldest items, so what survives is the most recent stretch of chat, and
// truncated says whenever anything was left out. An empty history encodes as an
// explicit empty list, never as an absent key. The queued rows follow the
// transcript, in queue order, at the seqs the caller's materialization will
// hand them; the watermark moves past them.
func boundConversationSnapshot(
	kind, conversationID string,
	throughSeq int64,
	newestFirst []db.ListChatItemsForContextSnapshotRow,
	queued []db.ListQueuedRunQueueItemsForTaskRunRow,
) (json.RawMessage, error) {
	snapshot := runConversationSnapshot{
		SchemaVersion: 1, Kind: kind, ConversationID: conversationID, ThroughSeq: throughSeq,
		Items: make([]runConversationSnapshotItem, 0, len(newestFirst)+len(queued)),
	}
	items := make([]runConversationSnapshotItem, 0, len(newestFirst)+len(queued))
	for i := len(newestFirst) - 1; i >= 0; i-- {
		items = append(items, snapshotItemFromRow(newestFirst[i]))
	}
	for _, row := range queued {
		snapshot.ThroughSeq++
		items = append(items, snapshotItemFromQueuedRow(row, snapshot.ThroughSeq))
	}
	for _, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("encode frozen Conversation snapshot item: %w", err)
		}
		if len(encoded) > conversationSnapshotMaxBytes {
			snapshot.Truncated = true
			continue
		}
		snapshot.Items = append(snapshot.Items, item)
	}
	if len(snapshot.Items) > conversationSnapshotMaxItems {
		snapshot.Items = snapshot.Items[len(snapshot.Items)-conversationSnapshotMaxItems:]
		snapshot.Truncated = true
	}
	for {
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return nil, fmt.Errorf("encode frozen Conversation snapshot: %w", err)
		}
		if len(encoded) <= conversationSnapshotMaxBytes || len(snapshot.Items) == 0 {
			return encoded, nil
		}
		snapshot.Items = snapshot.Items[1:]
		snapshot.Truncated = true
	}
}

// snapshotItemFromQueuedRow freezes a colleague's queued Message at the seq
// its materialization will allocate. The id is the item's reserved message
// id, so the transcript row the caller inserts is the one the agent read.
func snapshotItemFromQueuedRow(
	row db.ListQueuedRunQueueItemsForTaskRunRow,
	seq int64,
) runConversationSnapshotItem {
	item := row.ConversationRunQueueItem
	return runConversationSnapshotItem{
		ID:  item.MessageID.String(),
		Seq: seq,
		Author: runContextActor{
			ID:   item.InitiatedByUserID.String(),
			Kind: string(protocol.ActorKindUser),
			Name: row.DisplayName,
		},
		CreatedAt: item.CreatedAt.Time.UTC(),
		Content:   item.Content,
	}
}

func snapshotItemFromRow(row db.ListChatItemsForContextSnapshotRow) runConversationSnapshotItem {
	return runConversationSnapshotItem{
		ID:    row.ID.String(),
		Seq:   row.Seq,
		RunID: row.RunID.UUID.String(),
		Author: runContextActor{
			ID: row.AuthorID.UUID.String(), Kind: row.AuthorKind, Name: row.DisplayName,
		},
		CreatedAt: row.CreatedAt.Time.UTC(),
		Content:   row.Content,
	}
}

// runContextTriggerBrief freezes who wrote the words a Run starts on. It is
// present only when a member's message is the trigger: an assignment Run has
// no prompt, and a continuation the server composed has no member to name.
// Missing membership is an invariant violation, not a case to attribute
// around: every caller locks the acting membership before it enqueues.
func runContextTriggerBrief(
	ctx context.Context,
	q *db.Queries,
	in RunTrigger,
) (json.RawMessage, error) {
	if in.ActorUserID == uuid.Nil || in.TriggerPrompt == "" {
		return nil, nil
	}
	row, err := q.GetRunTriggerAuthor(ctx, db.GetRunTriggerAuthorParams{
		CompanyID: in.CompanyID, UserID: in.ActorUserID,
	})
	if err != nil {
		return nil, fmt.Errorf("load frozen trigger author: %w", err)
	}
	encoded, err := json.Marshal(runTriggerBrief{
		Author: runContextActor{
			ID: in.ActorUserID.String(), Kind: string(protocol.ActorKindUser), Name: row.DisplayName,
		},
		CreatedAt: row.EnqueuedAt.Time.UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("encode frozen trigger author: %w", err)
	}
	return encoded, nil
}

// encodeRunContext serializes the document and refuses one over 1 MiB. The
// snapshot is bounded on its own, so exceeding this means a caller composed
// an oversize context: an invariant violation that neither a retry nor a
// shorter history could cure, refused before the Run row exists.
func encodeRunContext(document map[string]json.RawMessage) ([]byte, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode run context: %w", err)
	}
	if len(encoded) > 1<<20 {
		return nil, ErrRunContextTooLarge
	}
	return encoded, nil
}
