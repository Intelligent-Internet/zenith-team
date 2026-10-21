// Package connectorselection shares message-scoped Connector eligibility with
// conversation and execution without importing the Agent services, which
// themselves depend on those domains.
package connectorselection

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type MessageConnectorScope struct{ ConversationID, AgentID uuid.UUID }

type MessageConnector struct {
	ID               uuid.UUID   `json:"id"`
	Name             string      `json:"name"`
	IconURL          *string     `json:"iconUrl,omitempty"`
	Provider         string      `json:"provider"`
	EligibleAgentIDs []uuid.UUID `json:"eligibleAgentIds"`
	AgentNames       []string    `json:"agentNames"`
}

// ReadMessageConnectors uses the caller's transaction when used during a send.
// Runtime identities never come from the selected label or another member.
func ReadMessageConnectors(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	scope MessageConnectorScope,
) ([]MessageConnector, error) {
	rows, err := q.ListMessageConnectorAgents(ctx, generated.ListMessageConnectorAgentsParams{
		CompanyID:      principal.CompanyID,
		ActorUserID:    principal.UserID,
		ConversationID: scope.ConversationID,
		AgentID:        scope.AgentID,
	})
	if err != nil {
		return nil, err
	}
	result := []MessageConnector{}
	positions := map[uuid.UUID]int{}
	for _, row := range rows {
		available, err := ReadAgentMessageConnectors(ctx, q, principal, row.ID)
		if err != nil {
			return nil, err
		}
		for _, connector := range available.Items {
			index, exists := positions[connector.ID]
			if !exists {
				index = len(result)
				positions[connector.ID] = index
				result = append(
					result,
					MessageConnector{
						ID:       connector.ID,
						Name:     connector.Name,
						IconURL:  iconURL(connector.IconUrl),
						Provider: available.Provider,
					},
				)
			}
			result[index].EligibleAgentIDs = append(result[index].EligibleAgentIDs, row.ID)
			result[index].AgentNames = append(result[index].AgentNames, row.Name)
		}
	}
	return result, nil
}

type AgentMessageConnectors struct {
	RuntimeID uuid.UUID
	Provider  string
	Items     []generated.ListExecutableMessageConnectorsRow
}

// ReadAgentMessageConnectors applies the same discovery and contract rules as
// claim freezing, plus the Agent's capability denials. The caller already
// proves conversation access; this function owns the member's binding scope.
func ReadAgentMessageConnectors(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
) (AgentMessageConnectors, error) {
	readiness, err := q.GetAgentConnectorReadinessContext(ctx, generated.GetAgentConnectorReadinessContextParams{
		CompanyID: principal.CompanyID, ViewerUserID: principal.UserID, AgentID: agentID,
		DiscoveryStaleAfterMilliseconds: protocol.ConnectorDiscoveryFreshWindow.Milliseconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentMessageConnectors{}, nil
	}
	if err != nil {
		return AgentMessageConnectors{}, err
	}
	result := AgentMessageConnectors{Provider: readiness.Provider}
	if !readiness.RuntimeEligible || !readiness.DiscoveryFresh {
		return result, nil
	}
	result.RuntimeID = readiness.BoundRuntimeID.UUID
	denial, err := ReadDenial(ctx, q, principal.CompanyID, result.RuntimeID, readiness)
	if err != nil {
		return result, err
	}
	rows, err := q.ListExecutableMessageConnectors(ctx, generated.ListExecutableMessageConnectorsParams{
		CompanyID: principal.CompanyID, RuntimeID: result.RuntimeID, Provider: readiness.Provider,
		FreshMilliseconds: protocol.ConnectorDiscoveryFreshWindow.Milliseconds(),
	})
	if err != nil {
		return result, err
	}
	for _, row := range rows {
		if !denial.Denies(row.ID, row.Source, row.NativeID) {
			result.Items = append(result.Items, row)
		}
	}
	return result, nil
}

func iconURL(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

type identity struct {
	Source   string `json:"source"`
	NativeID string `json:"nativeId"`
}
type Denial struct {
	ids        map[string]bool
	identities map[identity]bool
}

func (d Denial) Denies(id uuid.UUID, source, nativeID string) bool {
	return d.ids[id.String()] || d.identities[identity{source, nativeID}]
}

func ReadDenial(
	ctx context.Context,
	q *generated.Queries,
	companyID, runtimeID uuid.UUID,
	readiness generated.GetAgentConnectorReadinessContextRow,
) (Denial, error) {
	d := Denial{ids: map[string]bool{}, identities: map[identity]bool{}}
	var disabled []string
	if err := json.Unmarshal(readiness.DisabledMcpServers, &disabled); err != nil {
		return d, err
	}
	for _, id := range disabled {
		d.ids[id] = true
	}
	if len(readiness.DisabledPluginIds) == 0 {
		return d, nil
	}
	rows, err := q.ListDisabledPluginIdentitiesForRuntime(
		ctx,
		generated.ListDisabledPluginIdentitiesForRuntimeParams{
			CompanyID: companyID,
			RuntimeID: runtimeID,
			PluginIds: readiness.DisabledPluginIds,
		},
	)
	if err != nil {
		return d, err
	}
	for _, row := range rows {
		d.identities[identity{"claude_plugin", row.NativeID}] = true
		var linked []identity
		if err := json.Unmarshal(row.ConnectorIdentities, &linked); err != nil {
			return d, err
		}
		for _, item := range linked {
			d.identities[item] = true
		}
	}
	return d, nil
}
