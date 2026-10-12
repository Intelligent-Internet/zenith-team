package approval

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// AbandonAccountContinuationsInTx terminalizes human-input work owned by an
// account, closes its Inbox items, and stages the canonical domain updates.
// The caller must hold the Company lock before entering this participant; the
// participant takes Approval and Question receipt locks before any later
// Conversation locks in the caller's transaction.
func AbandonAccountContinuationsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, userID uuid.UUID,
	reason string,
) error {
	if q == nil || pending == nil {
		return ErrApprovalInvalidInput
	}
	if !util.ValidUUID(companyID) || !util.ValidUUID(userID) {
		return ErrApprovalInvalidInput
	}
	if reason == "" || strings.TrimSpace(reason) != reason {
		return ErrApprovalInvalidInput
	}
	rows, err := q.AbandonPendingContinuationsForAccountDeletion(
		ctx,
		generated.AbandonPendingContinuationsForAccountDeletionParams{
			Reason: reason, CompanyID: companyID, UserID: userID,
		},
	)
	if err != nil {
		return fmt.Errorf("abandon account human-input continuations: %w", err)
	}
	for _, row := range rows {
		switch row.EntityType {
		case string(protocol.EntityApproval):
			if err := closeAbandonedApprovalInTx(
				ctx,
				q,
				pending,
				companyID,
				row.EntityID,
			); err != nil {
				return err
			}
		case string(protocol.EntityQuestion):
			if err := closeAbandonedQuestionInTx(
				ctx,
				q,
				pending,
				companyID,
				row.EntityID,
			); err != nil {
				return err
			}
		default:
			return fmt.Errorf(
				"abandon account human-input continuations: unexpected entity type %q",
				row.EntityType,
			)
		}
	}
	return nil
}

func closeAbandonedApprovalInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, approvalID uuid.UUID,
) error {
	if err := inbox.CloseEntityInTx(
		ctx,
		q,
		pending,
		companyID,
		inbox.Entity{Type: protocol.EntityApproval, ID: approvalID.String()},
		inbox.Actor{Kind: "system"},
	); err != nil {
		return fmt.Errorf("close abandoned Approval Inbox items: %w", err)
	}
	projection, err := q.GetApprovalProjectionForSystem(ctx, generated.GetApprovalProjectionForSystemParams{
		CompanyID: companyID, ApprovalID: approvalID,
	})
	if err != nil {
		return fmt.Errorf("load abandoned Approval projection: %w", err)
	}
	value := approvalFromProjection(
		projection.Approval,
		projection.RequestingAgentName,
		projection.DecidedByDisplayName,
		projection.DecidedByAvatarUrl,
	)
	if err := stageApprovalEvent(
		ctx, q, pending,
		EventApprovalResolved,
		events.SystemActor(),
		value,
	); err != nil {
		return fmt.Errorf("stage abandoned Approval event: %w", err)
	}
	return nil
}

func closeAbandonedQuestionInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, questionID uuid.UUID,
) error {
	if err := inbox.CloseEntityInTx(
		ctx,
		q,
		pending,
		companyID,
		inbox.Entity{Type: protocol.EntityQuestion, ID: questionID.String()},
		inbox.Actor{Kind: "system"},
	); err != nil {
		return fmt.Errorf("close abandoned Question Inbox items: %w", err)
	}
	projection, err := q.GetQuestionProjectionForSystem(ctx, generated.GetQuestionProjectionForSystemParams{
		CompanyID: companyID, QuestionID: questionID,
	})
	if err != nil {
		return fmt.Errorf("load abandoned Question projection: %w", err)
	}
	value, err := questionFromProjection(
		projection.Question,
		projection.AskingAgentName,
		projection.ResolvedByDisplayName,
		projection.ResolvedByAvatarUrl,
	)
	if err != nil {
		return fmt.Errorf("decode abandoned Question projection: %w", err)
	}
	if err := stageQuestionEvent(
		ctx, q, pending,
		EventQuestionResolved,
		events.SystemActor(),
		value,
	); err != nil {
		return fmt.Errorf("stage abandoned Question event: %w", err)
	}
	return nil
}
