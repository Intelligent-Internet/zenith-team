package conversation

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// conversationRouting is who may see anything belonging to one Conversation,
// read from its authoritative row: a Task conversation belongs to the whole
// Company, and a direct chat to the member who opened it. Every
// conversation-derived event carries these two facts so the consumer that
// delivers can decide without a query of its own.
//
// Callers must fail closed when the lookup fails; a missing route must never
// widen to the company.
type conversationRouting struct {
	Kind            Kind
	CreatedByUserID *uuid.UUID
}

func resolveConversationRouting(
	ctx context.Context,
	q *generated.Queries,
	companyID, conversationID uuid.UUID,
) (conversationRouting, error) {
	row, err := q.GetConversationRoute(
		ctx,
		generated.GetConversationRouteParams{
			ConversationID: conversationID,
			CompanyID:      companyID,
		},
	)
	if err != nil {
		return conversationRouting{}, fmt.Errorf("resolve Conversation routing: %w", err)
	}
	return conversationRouting{
		Kind:            Kind(row.Kind),
		CreatedByUserID: util.NullUUIDPtr(row.CreatedByUserID),
	}, nil
}

// ResolveConversationRoute answers the same question for the execution
// domain, which writes transcript items into Conversations it does not own,
// and for a Task chat also decides the audience, its Project's readers (ADR
// 0017). It satisfies execution.ConversationRouteResolver, injected at
// composition so execution never imports this package.
func ResolveConversationRoute(
	ctx context.Context,
	q *generated.Queries,
	companyID, conversationID uuid.UUID,
) (execution.ConversationRoute, error) {
	routing, err := resolveConversationRouting(ctx, q, companyID, conversationID)
	if err != nil {
		return execution.ConversationRoute{}, err
	}
	route := execution.ConversationRoute{
		ConversationKind:      string(routing.Kind),
		ConversationCreatedBy: routing.CreatedByUserID,
	}
	if routing.Kind == KindTask {
		route.Audience, err = auth.ConversationFrameAudienceInTx(ctx, q, companyID, conversationID)
		if err != nil {
			return execution.ConversationRoute{}, err
		}
	}
	return route, nil
}

// stageInTx appends frames to the caller's batch after deciding, in the same
// transaction, the audience of every Task chat frame among them: the readers
// of the chat's Project (ADR 0017). Other chats' frames pass unchanged.
func stageInTx(ctx context.Context, q *generated.Queries, pending *[]events.Event, staged ...events.Event) error {
	if err := auth.StampProjectAudiencesInTx(ctx, q, staged); err != nil {
		return err
	}
	*pending = append(*pending, staged...)
	return nil
}
