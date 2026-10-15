package execution

import (
	"context"

	"github.com/google/uuid"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// Freeze the send's exact contract on each Run before it enters the queue.
// Claim checks the frozen placement rather than switching connected accounts.
func freezeMessageRunConnectors(ctx context.Context, q *generated.Queries, in RunTrigger, run *generated.Run) error {
	requestKey, userID, conversationID := in.SkillRequestKey, in.ActorUserID, in.ConversationID
	if in.InvocationID.Valid {
		message, err := q.GetInvocationConnectorMessage(
			ctx,
			generated.GetInvocationConnectorMessageParams{
				CompanyID: in.CompanyID,
				ID:        in.InvocationID.UUID,
			},
		)
		if err != nil {
			return err
		}
		requestKey, userID, conversationID = message.RequestKey.UUID, message.AuthorID.UUID, message.ConversationID
	}
	if requestKey == uuid.Nil {
		return nil
	}
	refs, err := q.ListMessageConnectors(
		ctx,
		generated.ListMessageConnectorsParams{
			CompanyID:        in.CompanyID,
			ConversationID:   conversationID,
			RequestKey:       requestKey,
			SelectedByUserID: userID,
		},
	)
	if err != nil || len(refs) == 0 {
		return err
	}
	binding, err := q.GetMessageConnectorBinding(
		ctx,
		generated.GetMessageConnectorBindingParams{
			CompanyID:        in.CompanyID,
			ConversationID:   conversationID,
			RequestKey:       requestKey,
			SelectedByUserID: userID,
			AgentID:          in.AgentID,
		},
	)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if err := q.InsertMessageRunConnector(
			ctx,
			generated.InsertMessageRunConnectorParams{
				RunID:          run.ID,
				ConnectorID:    ref.ConnectorID,
				PolicyRevision: ref.PolicyRevision,
				Source:         ref.Source,
				NativeID:       ref.NativeID,
				ContractDigest: ref.ContractDigest,
			},
		); err != nil {
			return err
		}
	}
	selected, err := q.SelectMessageRunConnectors(
		ctx,
		generated.SelectMessageRunConnectorsParams{
			ID:        run.ID,
			CompanyID: in.CompanyID,
			RuntimeID: binding.RuntimeID,
		},
	)
	if err != nil {
		return err
	}
	*run = selected
	return nil
}
