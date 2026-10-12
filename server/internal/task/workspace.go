package task

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/execution"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// WorkspaceTranscriptPreviewLimit is the fixed newest-row cap for each Run
// transcript preview embedded by the Task workspace. The full transcript
// endpoint remains the drill-down.
const WorkspaceTranscriptPreviewLimit = 20

type WorkspaceOptions struct {
	MessageLimit int
}

type RunTimeline struct {
	RunID      uuid.UUID                   `json:"runId"`
	Items      []execution.TranscriptEntry `json:"items"`
	ThroughSeq int64                       `json:"throughSeq"`
	HasMore    bool                        `json:"hasMore"`
}

type WorkspaceConversation struct {
	conversation.Response
	TaskID uuid.UUID `json:"taskId"`
}

type Workspace struct {
	Task                 Response                        `json:"task"`
	Conversation         WorkspaceConversation           `json:"conversation"`
	Messages             []conversation.MessageView      `json:"messages"`
	EarliestAvailableSeq int64                           `json:"earliestAvailableSeq"`
	ThroughSeq           int64                           `json:"throughSeq"`
	RunTimelines         []RunTimeline                   `json:"runTimelines"`
	ActiveRuns           []execution.RunView             `json:"activeRuns"`
	RunQueue             conversation.RunQueueProjection `json:"runQueue"`
	Dependencies         DependencySet                   `json:"dependencies"`
}

// Workspace reads a Task with everything a person needs to follow it: its
// Conversation, Run timelines, queue and dependencies. Inside a Run (a
// non-nil scope) it carries the whole Task, so a Task that is not the Run's
// own is refused with ErrOutsideRunScope, as Get refuses it, and its
// dependencies are the ones ListDependencies gives the Run.
func (s *DefaultTaskService) Workspace(
	ctx context.Context,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
	options WorkspaceOptions,
	run *RunScope,
) (Workspace, error) {
	var result Workspace
	err := s.store.WithinTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	}, func(q *generated.Queries) error {
		conversationRead, err := conversation.ReadTaskWorkspaceInTx(
			ctx,
			q,
			principal.UserID,
			principal.CompanyID,
			taskID,
			options.MessageLimit,
		)
		if err != nil {
			return err
		}
		taskValue, err := taskProjectionForActiveMember(ctx, q, principal, taskID)
		if err != nil {
			return err
		}
		if err := run.admitRead(ctx, q, taskValue); err != nil {
			return err
		}
		timelines, err := workspaceRunTimelines(ctx, q, conversationRead.Messages)
		if err != nil {
			return err
		}
		activeRuns, err := workspaceActiveRuns(ctx, q, principal, taskID)
		if err != nil {
			return err
		}
		dependencies, err := listDependencies(ctx, q, principal, taskID)
		if err != nil {
			return err
		}
		result = Workspace{
			Task: taskValue,
			Conversation: WorkspaceConversation{
				Response: conversationRead.Conversation,
				TaskID:   taskID,
			},
			Messages:             conversationRead.Messages,
			EarliestAvailableSeq: conversationRead.EarliestAvailableSeq,
			ThroughSeq:           conversationRead.ThroughSeq,
			RunTimelines:         timelines,
			ActiveRuns:           activeRuns,
			RunQueue:             conversationRead.RunQueue,
			Dependencies:         run.dependencies(dependencies),
		}
		return nil
	})
	if errors.Is(err, conversation.ErrNotFound) {
		return Workspace{}, ErrNotFound
	}
	if err != nil {
		return Workspace{}, fmt.Errorf("read Task workspace: %w", err)
	}
	return result, nil
}

func workspaceRunTimelines(
	ctx context.Context,
	q *generated.Queries,
	messages []conversation.MessageView,
) ([]RunTimeline, error) {
	runIDs := workspaceRunIDs(messages)
	if len(runIDs) == 0 {
		return []RunTimeline{}, nil
	}
	rows, err := q.ListRunItemTailsBulk(ctx, generated.ListRunItemTailsBulkParams{
		RunIds: runIDs, PerRunLimit: WorkspaceTranscriptPreviewLimit + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("list Task workspace Run transcripts in bulk: %w", err)
	}
	rowsByRun := make(map[uuid.UUID][]generated.ConversationItem, len(runIDs))
	for _, row := range rows {
		rowsByRun[row.RunID.UUID] = append(rowsByRun[row.RunID.UUID], row)
	}
	timelines := make([]RunTimeline, 0, len(runIDs))
	for _, runID := range runIDs {
		runRows := rowsByRun[runID]
		hasMore := len(runRows) > WorkspaceTranscriptPreviewLimit
		if hasMore {
			runRows = runRows[len(runRows)-WorkspaceTranscriptPreviewLimit:]
		}
		items, err := execution.MapTranscriptRows(runRows)
		if err != nil {
			return nil, fmt.Errorf("decode Task workspace Run transcript: %w", err)
		}
		var throughSeq int64
		if len(items) > 0 {
			throughSeq = items[len(items)-1].Seq
		}
		timelines = append(timelines, RunTimeline{
			RunID: runID, Items: items, ThroughSeq: throughSeq, HasMore: hasMore,
		})
	}
	return timelines, nil
}

// workspaceRunIDs is the distinct set of Runs the returned message window
// refers to, in window order. Every item names a Run now — the user's words
// name the Run that consumed them, not only the answer names the Run that
// wrote it — so a turn still waiting on its Agent gets its (empty) timeline
// instead of appearing only once the Agent has spoken.
func workspaceRunIDs(messages []conversation.MessageView) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(messages))
	result := make([]uuid.UUID, 0, len(messages))
	for _, message := range messages {
		runID := message.Message.RunID
		if _, exists := seen[runID]; exists {
			continue
		}
		seen[runID] = struct{}{}
		result = append(result, runID)
	}
	return result
}

func workspaceActiveRuns(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
) ([]execution.RunView, error) {
	rows, err := q.ListRunProjections(ctx, generated.ListRunProjectionsParams{
		CompanyID: principal.CompanyID,
		UserID:    principal.UserID, EnforceVisibility: true,
		Statuses: []string{}, ActiveOnly: true,
		UsageRoles: auth.RolesWith(auth.PermissionUsageRead),
		HasTaskID:  true, TaskID: taskID,
		PageSize: math.MaxInt32,
	})
	if err != nil {
		return nil, fmt.Errorf("list active Task workspace Runs: %w", err)
	}
	return execution.MapRunProjectionRows(rows, principal.UserID), nil
}
