package schedule

import (
	"context"
	"fmt"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// notifyAttentionInTx reuses the personal Inbox. Schedule details never fan
// out to Company administrators or a shared Task notification channel.
func notifyAttentionInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	current Response,
	reason string,
) error {
	if reason == ReasonCompanyChanged || reason == ReasonCreatorChanged {
		return nil
	}
	link := "/scheduled/" + current.ID.String()
	_, err := inbox.CreateInTx(ctx, queries, pending, inbox.CreateInput{
		CompanyID:       current.CompanyID,
		RecipientUserID: current.CreatorUserID,
		ConversationID:  &current.ConversationID,
		Type:            inbox.TypeBlocker,
		Entity:          inbox.Entity{Type: protocol.EntityConversation, ID: current.ConversationID.String()},
		Title:           "Schedule needs review",
		Body:            "Review your schedule's Agent, connections and timing before resuming.",
		DeepLink:        &link,
		DedupeKey:       fmt.Sprintf("schedule:%s:attention:%d", current.ID, current.Revision),
		Actor:           inbox.Actor{Kind: "system"},
	})
	return err
}

func closeAttentionInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	current Response,
) error {
	return inbox.CloseEntityInTx(
		ctx,
		queries,
		pending,
		current.CompanyID,
		inbox.Entity{
			Type: protocol.EntityConversation,
			ID:   current.ConversationID.String(),
		},
		inbox.Actor{Kind: "system"},
	)
}
