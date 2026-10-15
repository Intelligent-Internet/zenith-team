package agent

import (
	"context"

	"boardly.dev/boardly/server/internal/agent/connectorselection"
	"boardly.dev/boardly/server/internal/auth"

	"github.com/google/uuid"
)

type (
	MessageConnectorScope = connectorselection.MessageConnectorScope
	MessageConnector      = connectorselection.MessageConnector
)

func (s *DefaultConnectorService) ListMessageConnectors(
	ctx context.Context,
	principal auth.UserPrincipal,
	scope MessageConnectorScope,
) ([]MessageConnector, error) {
	if (scope.ConversationID == uuid.Nil) == (scope.AgentID == uuid.Nil) {
		return nil, ErrInvalidInput
	}
	return connectorselection.ReadMessageConnectors(ctx, s.store.Queries(), principal, scope)
}
