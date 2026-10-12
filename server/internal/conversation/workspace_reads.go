package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// TaskWorkspaceRead is the Conversation-owned portion of a Task workspace.
// The Task domain owns the surrounding transaction and the final composite;
// this package keeps its canonical Conversation, Message, Attachment and Run
// queue projections in one place. Like every message window it carries only
// the navigation bounds — missing older/newer rows are derived by the client.
type TaskWorkspaceRead struct {
	Task                 generated.Task
	Conversation         Response
	Messages             []MessageView
	EarliestAvailableSeq int64
	ThroughSeq           int64
	RunQueue             RunQueueProjection
}

// ReadTaskWorkspaceInTx reads the exact Task Conversation against a caller's
// transaction-bound query set. The SQL applies active-membership and
// task/direct visibility using the explicit actor, Company and Task ids.
func ReadTaskWorkspaceInTx(
	ctx context.Context,
	q *generated.Queries,
	actorUserID, companyID, taskID uuid.UUID,
	messageLimit int,
) (TaskWorkspaceRead, error) {
	anchor, err := q.GetTaskWorkspaceAnchor(ctx, generated.GetTaskWorkspaceAnchorParams{
		ActorUserID: actorUserID,
		TaskID:      taskID,
		CompanyID:   companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskWorkspaceRead{}, ErrNotFound
	}
	if err != nil {
		return TaskWorkspaceRead{}, fmt.Errorf("read Task workspace anchor: %w", err)
	}

	// A workspace opens on the newest messages. The anchor's ThroughSeq is the
	// whole stream's head, so it bounds the page without being the place the
	// page starts.
	messages, err := newestMessageViews(
		ctx,
		q,
		actorUserID,
		companyID,
		anchor.Conversation.ID,
		anchor.ThroughSeq,
		messageLimit,
	)
	if err != nil {
		return TaskWorkspaceRead{}, fmt.Errorf("read Task workspace Messages: %w", err)
	}

	resolved := resolvedConversationTarget{
		CompanyID:        companyID,
		ConversationID:   anchor.Conversation.ID,
		ConversationKind: anchor.Conversation.Kind,
		TaskID:           &anchor.Task.ID,
	}
	queue, err := runQueueProjection(ctx, q, actorUserID, resolved)
	if err != nil {
		return TaskWorkspaceRead{}, fmt.Errorf("read Task workspace Run queue: %w", err)
	}
	return TaskWorkspaceRead{
		Task:                 anchor.Task,
		Conversation:         conversationFromRow(anchor.Conversation),
		Messages:             messages,
		EarliestAvailableSeq: 1,
		ThroughSeq:           anchor.ThroughSeq,
		RunQueue:             queue,
	}, nil
}
