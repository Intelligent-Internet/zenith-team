package agent

import (
	"context"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent/connectorselection"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"

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
