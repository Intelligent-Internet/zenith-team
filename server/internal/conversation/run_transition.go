package conversation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// runConversationActorIsWellFormed decides whether an actor names a kind these
// events recognize and carries the identity that kind implies: the system acts
// with no actor id, and a user or an agent must carry one.
//
// The events table stores the pair as two independent nullable columns, so no
// constraint can state the correlation between them, and this seam is reached
// in-process from execution rather than through a request binder.
func runConversationActorIsWellFormed(actorKind string, actorID *uuid.UUID) bool {
	switch actorKind {
	case "system":
		return actorID == nil
	case "user", "agent":
		return actorID != nil
	default:
		return false
	}
}

// BumpAndStageRunConversationUpdates coalesces any number of Run mutations
// to exactly one list-visible Conversation projection update per identity.
// tx must be transaction-bound so the bump and rebuilt summaries commit or
// roll back as one state transition; the update events are event on pending
// for the caller to publish after its transaction commits.
func BumpAndStageRunConversationUpdates(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	actorKind string,
	actorID *uuid.UUID,
	companyID uuid.UUID,
	conversationIDs []uuid.UUID,
	observed ...*activity.Snapshot,
) error {
	if tx == nil || pending == nil || !runConversationActorIsWellFormed(actorKind, actorID) {
		return errors.New("Run Conversation update actor is invalid")
	}
	unique := make(map[uuid.UUID]struct{}, len(conversationIDs))
	for _, conversationID := range conversationIDs {
		if conversationID == uuid.Nil {
			return errors.New("Run Conversation update identity is invalid")
		}
		unique[conversationID] = struct{}{}
	}
	if len(unique) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sortUUIDs(ids)

	beforeStates := make(map[uuid.UUID]*activity.Snapshot, len(ids))
	for _, before := range observed {
		if before != nil {
			beforeStates[before.EntityID] = before
		}
	}
	for _, id := range ids {
		if beforeStates[id] != nil {
			continue
		}
		before, err := activity.ObserveInTx(ctx, tx, Topic, id)
		if err != nil {
			return err
		}
		beforeStates[id] = before
	}
	rows, err := tx.BumpConversationProjectionsBulk(ctx,
		generated.BumpConversationProjectionsBulkParams{
			CompanyID: companyID, ConversationIds: pgUUIDs(ids),
		})
	if err != nil {
		return fmt.Errorf("bump Run Conversation projections: %w", err)
	}
	for _, row := range rows {
		if err := activity.RecordInTx(
			ctx,
			tx,
			pending,
			runConversationActor(actorKind, actorID),
			EventConversationUpdated,
			beforeStates[row.ID],
			Topic,
			row.ID,
		); err != nil {
			return err
		}
	}
	sortBumpedRunConversations(rows)
	bases, err := tx.ListRunConversationSummaryBasesBulk(ctx,
		generated.ListRunConversationSummaryBasesBulkParams{
			ConversationIds: pgUUIDs(ids), CompanyID: companyID,
		})
	if err != nil {
		return fmt.Errorf("list Run Conversation summary bases in bulk: %w", err)
	}
	messages, err := tx.ListRunConversationLatestMessagesBulk(ctx,
		generated.ListRunConversationLatestMessagesBulkParams{
			ConversationIds: pgUUIDs(ids), CompanyID: companyID,
		})
	if err != nil {
		return fmt.Errorf("list latest Run Conversation Messages in bulk: %w", err)
	}
	latestRuns, err := tx.ListRunConversationLatestRunsBulk(ctx,
		generated.ListRunConversationLatestRunsBulkParams{
			ConversationIds: pgUUIDs(ids), CompanyID: companyID,
		})
	if err != nil {
		return fmt.Errorf("list latest Run Conversation Runs in bulk: %w", err)
	}
	return stageInTx(ctx, tx, pending, buildRunConversationTransitionEvents(
		actorKind, actorID, rows, bases, messages, latestRuns,
	)...)
}

// StageUpdatedInTx publishes the current projection after a cross-domain
// mutation has already advanced and captured the Conversation revision.
// The caller owns the transaction and publishes pending only after commit.
func StageUpdatedInTx(ctx context.Context, q *generated.Queries, pending *[]events.Event,
	actorUserID uuid.UUID, row generated.Conversation,
) error {
	summary, err := conversationListSummary(ctx, q, actorUserID, row.ID, row.CompanyID)
	if err != nil {
		return fmt.Errorf("build changed Conversation summary: %w", err)
	}
	return stageInTx(ctx, q, pending,
		newConversationEvent(actorUserID, nil, conversationFromRow(row), EventConversationUpdated, &summary))
}

// buildRunConversationTransitionEvents assembles the per-Conversation
// projection summaries purely from the already-fetched bulk rows, so the
// fixed transition query count stays structural rather than asserted.
func buildRunConversationTransitionEvents(
	actorKind string,
	actorID *uuid.UUID,
	rows []generated.Conversation,
	bases []generated.ListRunConversationSummaryBasesBulkRow,
	messages []generated.ListRunConversationLatestMessagesBulkRow,
	latestRuns []generated.ListRunConversationLatestRunsBulkRow,
) []events.Event {
	summaries := make(map[uuid.UUID]ListSummary, len(rows))
	for _, row := range rows {
		summaries[row.ID] = ListSummary{
			UpdatedAt: row.UpdatedAt.Time.UTC(),
			Runs:      RunsSummary{},
		}
	}
	for _, base := range bases {
		conversationID := base.ConversationID
		summary := summaries[conversationID]
		summary.UpdatedAt = base.UpdatedAt.Time.UTC()
		summary.Runs.QueuedCount = base.QueuedRunCount
		summary.Runs.ActiveCount = base.ActiveRunCount
		summaries[conversationID] = summary
	}
	for _, message := range messages {
		conversationID := message.ConversationID
		summary := summaries[conversationID]
		summary.LastMessage = newLastMessageSummary(
			message.Seq, message.ContentPreview, message.ContentTruncated,
			message.CreatedAt, message.Type, message.AuthorKind,
		)
		summaries[conversationID] = summary
	}
	for _, run := range latestRuns {
		conversationID := run.ConversationID
		summary := summaries[conversationID]
		summary.Runs.Latest = newLatestRunSummary(
			run.RunID, run.AgentID, run.Status, run.CreatedAt,
			run.FailureReason,
			run.Attempt,
		)
		summaries[conversationID] = summary
	}

	actor := runConversationActor(actorKind, actorID)
	wireEvents := make([]events.Event, 0, len(rows))
	for _, row := range rows {
		// Schedule history is projected by the Schedule API and Inbox. Keep
		// its Conversation projection current without publishing it into the
		// ordinary chat list.
		if Kind(row.Kind) == KindSchedule {
			continue
		}
		summary := summaries[row.ID]
		// The conversation row itself carries task_id, so the projection needs
		// no separate task join to name what the conversation is about.
		value := conversationFromRow(row)
		wireEvents = append(wireEvents, newConversationEventForActor(
			actor,
			nil,
			value,
			EventConversationUpdated,
			&summary,
		))
	}
	return wireEvents
}

// runConversationActor maps the caller-facing actor kind ("user", "agent",
// "system") onto the one actor value a event event carries.
func runConversationActor(actorKind string, actorID *uuid.UUID) events.Actor {
	switch actorKind {
	case "user":
		return events.UserActor(actorID.String())
	case "agent":
		return events.AgentActor(actorID.String())
	default:
		return events.SystemActor()
	}
}

func sortBumpedRunConversations(rows []generated.Conversation) {
	sort.Slice(rows, func(left, right int) bool {
		return bytes.Compare(rows[left].ID[:], rows[right].ID[:]) < 0
	})
}

func sortUUIDs(ids []uuid.UUID) {
	sort.Slice(ids, func(left, right int) bool {
		return bytes.Compare(ids[left][:], ids[right][:]) < 0
	})
}

func pgUUIDs(ids []uuid.UUID) []uuid.UUID {
	values := make([]uuid.UUID, len(ids))
	copy(values, ids)
	return values
}
