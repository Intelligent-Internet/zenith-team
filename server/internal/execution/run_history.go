package execution

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

// ObserveRunsInTx captures exact preimages after the caller's Company,
// Conversation and Task locks. It also takes Run locks in identity order so
// concurrent callbacks cannot move a Run between observation and mutation.
func ObserveRunsInTx(ctx context.Context, q *db.Queries, ids []uuid.UUID) (map[uuid.UUID]*activity.Snapshot, error) {
	locked, err := q.LockRunHistoryRows(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("lock Run history: %w", err)
	}
	snapshots := make(map[uuid.UUID]*activity.Snapshot, len(locked))
	for _, id := range locked {
		snapshot, err := activity.ObserveInTx(ctx, q, Topic, id)
		if err != nil {
			return nil, err
		}
		snapshots[id] = snapshot
	}
	return snapshots, nil
}

func (s *DefaultRunService) changeRun(
	ctx context.Context,
	id uuid.UUID,
	action string,
	mutate func(*db.Queries) (db.Run, error),
) (db.Run, error) {
	var run db.Run
	var pending []events.Event
	err := s.runInTx(ctx, func(q *db.Queries) error {
		base, err := q.GetRun(ctx, id)
		if err != nil {
			return err
		}
		recoveringCalendarClaim := base.TaskScheduled && action == EventRunQueued
		if recoveringCalendarClaim {
			// Calendar edits skip dispatched children, so their Conversation
			// lock alone cannot fence recovery. A shared Company lock serializes
			// with definition/consent edits before observing or locking the Run.
			if _, err := q.LockCompanyForMembershipRead(ctx, base.CompanyID); err != nil {
				return fmt.Errorf("lock Company for Calendar claim recovery: %w", err)
			}
		}
		if base.InvocationID.Valid || base.ScheduleFiringID.Valid || recoveringCalendarClaim {
			if err := lockTerminalRunConversation(ctx, q, base); err != nil {
				return err
			}
			if action == EventRunRunning {
				if err := validateChannelRunStart(ctx, q, base); err != nil {
					return err
				}
			}
		}
		before, err := ObserveRunsInTx(ctx, q, []uuid.UUID{id})
		if err != nil {
			return err
		}
		run, err = mutate(q)
		if err != nil {
			return err
		}
		if action == EventRunQueued && run.Status == "cancelled" {
			action = EventRunCancelled
		}
		return recordRunChangeInTx(ctx, q, &pending, events.SystemActor(), action, before[id], run)
	})
	if err != nil {
		return db.Run{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.Bus.Publish(ctx, group.Topic, group.Events...)
	}
	return run, nil
}

func (s *DefaultRunService) promoteDueDeferredRuns(ctx context.Context, runtimeIDs []uuid.UUID) ([]db.Run, error) {
	var runs []db.Run
	var pending []events.Event
	err := s.runInTx(ctx, func(q *db.Queries) error {
		selected, err := q.SelectDueDeferredRunsForUpdate(ctx, db.SelectDueDeferredRunsForUpdateParams{
			FilterRuntimes: len(runtimeIDs) != 0, RuntimeIds: runtimeIDs,
		})
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, 0, len(selected))
		before := make(map[uuid.UUID]*activity.Snapshot, len(selected))
		for _, run := range selected {
			snapshot, err := activity.ObserveInTx(ctx, q, Topic, run.ID)
			if err != nil {
				return err
			}
			ids = append(ids, run.ID)
			before[run.ID] = snapshot
		}
		if len(ids) == 0 {
			return nil
		}
		runs, err = q.PromoteDueDeferredRunsByIDs(ctx, ids)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if err := recordRunChangeInTx(
				ctx,
				q,
				&pending,
				events.SystemActor(),
				EventRunQueued,
				before[run.ID],
				run,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.Bus.Publish(ctx, group.Topic, group.Events...)
	}
	return runs, nil
}

// recordRunChangeInTx binds history, actor and the lifecycle projection to the
// same locked mutation. Nothing is reloaded from live state after commit.
func recordRunChangeInTx(ctx context.Context, q *db.Queries, pending *[]events.Event, actor events.Actor,
	action string, before *activity.Snapshot, run db.Run,
) error {
	count := len(*pending)
	if err := activity.RecordInTx(ctx, q, pending, actor, action, before, Topic, run.ID); err != nil {
		return err
	}
	if len(*pending) == count || action == EventRunQueued && run.Status == "deferred" {
		return nil
	}
	view, err := loadRunView(ctx, q, run)
	if err != nil {
		return err
	}
	*pending = append(*pending, events.Event{
		ActivityRecorded: true, EntityType: Topic, Name: action, CompanyID: view.CompanyID,
		CreatedAt: view.UpdatedAt, RunID: view.ID, TaskID: util.UUIDToValue(view.TaskID),
		ConversationID: view.ConversationID, Actor: actor, Payload: view.RunSnapshot,
	})
	return nil
}
