package conversation

import (
	"context"
	"errors"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MaterializeInitializationRunInTx starts a platform-authored turn without
// inventing a user message. Company initialization owns the receipt and tx.
func MaterializeInitializationRunInTx(ctx context.Context, q *generated.Queries, pending *[]events.Event, in execution.RunTrigger) (generated.Run, error) {
	row, err := q.LockConversationForActiveUserMutation(ctx, generated.LockConversationForActiveUserMutationParams{ActorUserID: in.AuthorizedByUserID, CompanyID: in.CompanyID, ConversationID: in.ConversationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.Run{}, ErrNotFound
	}
	if err != nil {
		return generated.Run{}, err
	}
	if row.Kind != "direct" || !row.AgentID.Valid || row.AgentID.UUID != in.AgentID || row.CreatedByUserID.UUID != in.AuthorizedByUserID {
		return generated.Run{}, ErrConflict
	}
	if in.Source != execution.RunSourceCompanyResearch && in.Source != execution.RunSourceCompanyWelcome {
		return generated.Run{}, ErrInvalidInput
	}
	before, err := activity.ObserveInTx(ctx, q, Topic, in.ConversationID)
	if err != nil {
		return generated.Run{}, err
	}
	run, err := execution.EnqueueRunInTx(ctx, q, pending, in)
	if err != nil {
		return generated.Run{}, err
	}
	_, err = bumpConversationProjection(ctx, q, in.ConversationID, in.CompanyID, pending, events.SystemActor(), before)
	return run, err
}

// ResumeInitializationQueueInTx releases the preserved queue after Company has
// completed its receipt. The caller owns the Company and Conversation locks
// and commits the receipt, promotion and projection together.
func ResumeInitializationQueueInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	taskMessageRuns TaskMessageRunPreparer,
	companyID, conversationID uuid.UUID,
) (QueuePromotion, error) {
	before, err := activity.ObserveInTx(ctx, q, Topic, conversationID)
	if err != nil {
		return QueuePromotion{}, err
	}
	promotion, err := PromoteRunQueueInTx(ctx, q, pending, taskMessageRuns, companyID, conversationID)
	if err != nil || promotion.RunID == nil && promotion.RemovedCount == 0 {
		return promotion, err
	}
	err = BumpAndStageRunConversationUpdates(
		ctx, q, pending, string(events.ActorTypeSystem), nil,
		companyID, []uuid.UUID{conversationID}, before,
	)
	return promotion, err
}
