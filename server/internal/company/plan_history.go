package company

import (
	"context"
	"fmt"
	"sort"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/task"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
)

// planHistory holds preimages while one Company Plan transaction reconciles
// metadata, dependencies and deletions. Capture happens after its graph locks
// and before any Task write, so dependency edits share the metadata revision.
type planHistory struct {
	tasks         map[uuid.UUID]*activity.Snapshot
	conversations map[uuid.UUID]*activity.Snapshot
	runs          map[uuid.UUID]*activity.Snapshot
}

func observePlanHistoryInTx(ctx context.Context, q *generated.Queries, companyID uuid.UUID,
	current map[string]persistedTask, desired map[string]desiredTask,
) (planHistory, error) {
	history := planHistory{tasks: make(map[uuid.UUID]*activity.Snapshot), conversations: make(map[uuid.UUID]*activity.Snapshot)}
	var deletedTaskIDs []uuid.UUID
	for _, key := range sortedPersistedTaskKeys(current) {
		row := current[key]
		beforeTask, err := activity.ObserveInTx(ctx, q, task.Topic, row.ID)
		if err != nil {
			return planHistory{}, err
		}
		beforeConversation, err := activity.ObserveInTx(ctx, q, conversation.Topic, row.ConversationID)
		if err != nil {
			return planHistory{}, err
		}
		history.tasks[row.ID] = beforeTask
		history.conversations[row.ConversationID] = beforeConversation
		if _, retained := desired[key]; !retained && !terminalTaskStatus(row.Status) {
			deletedTaskIDs = append(deletedTaskIDs, row.ID)
		}
	}
	ids, err := q.ListRunIDsForDeletedTasksForUpdate(ctx, generated.ListRunIDsForDeletedTasksForUpdateParams{CompanyID: companyID, TaskIds: deletedTaskIDs})
	if err != nil {
		return planHistory{}, fmt.Errorf("lock detached Task Runs: %w", err)
	}
	history.runs, err = execution.ObserveRunsInTx(ctx, q, ids)
	return history, err
}

func (h planHistory) recordInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	actor events.Actor,
) error {
	for _, subject := range []struct {
		entityType string
		before     map[uuid.UUID]*activity.Snapshot
	}{
		{task.Topic, h.tasks}, {conversation.Topic, h.conversations}, {execution.Topic, h.runs},
	} {
		ids := make([]uuid.UUID, 0, len(subject.before))
		for id := range subject.before {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
		for _, id := range ids {
			action := subject.entityType + ".updated"
			if subject.before[id] == nil {
				action = subject.entityType + ".created"
			}
			after, err := activity.ObserveInTx(ctx, q, subject.entityType, id)
			if err != nil {
				return err
			}
			if after == nil {
				action = subject.entityType + ".deleted"
			}
			if err := activity.RecordInTx(ctx, q, pending, actor, action, subject.before[id], subject.entityType, id); err != nil {
				return err
			}
		}
	}
	return nil
}
