package conversation

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// StageTaskReportTurnInTx stages the Conversation projections for a
// user-confirmed Task-report prompt. The Task domain owns the transaction and
// the execution domain has already staged run.queued.
func StageTaskReportTurnInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	actorUserID, companyID uuid.UUID,
	messageRow generated.ConversationItem,
) error {
	message := messageFromRow(messageRow)
	bumped, err := bumpConversationProjection(
		ctx,
		queries,
		message.ConversationID,
		companyID,
		pending,
		events.UserActor(actorUserID.String()),
	)
	if err != nil {
		return err
	}
	view, err := messageViewWithAttachments(ctx, queries, actorUserID, companyID, message)
	if err != nil {
		return err
	}
	summary, err := conversationListSummary(
		ctx, queries, actorUserID, message.ConversationID, companyID,
	)
	if err != nil {
		return fmt.Errorf("build Task report Conversation summary: %w", err)
	}
	*pending = append(
		*pending,
		newMessageCreatedEvent(actorUserID, bumped, view),
		newConversationEvent(actorUserID, nil, bumped, EventConversationUpdated, &summary),
	)
	return nil
}
