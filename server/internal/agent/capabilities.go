package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// CapabilitySettings narrows inherited availability and opts into managed tools.
// Company Connector policy still governs external Connector authority. The
// selection is template state (ADR 0014): the denial lists name entries of
// the Company's catalog for the Agent's provider and apply on whichever
// runtime a member's binding names.
type CapabilitySettings struct {
	EnabledTools       []string    `json:"enabledTools"`
	DisabledBuiltIns   []string    `json:"disabledBuiltIns"`
	DisabledPlugins    []uuid.UUID `json:"disabledPlugins"`
	DisabledMCPServers []uuid.UUID `json:"disabledMcpServers"`
}

func normalizeCapabilitySettings(value CapabilitySettings) (CapabilitySettings, error) {
	tooMany := len(value.EnabledTools) > 1 || len(value.DisabledBuiltIns) > 5 ||
		len(value.DisabledPlugins) > 500 || len(value.DisabledMCPServers) > 500
	if tooMany {
		return CapabilitySettings{}, ErrInvalidInput
	}
	for _, key := range value.EnabledTools {
		if key != "zenith-studio" {
			return CapabilitySettings{}, ErrInvalidInput
		}
	}
	value.EnabledTools = append([]string{}, value.EnabledTools...)
	for _, key := range value.DisabledBuiltIns {
		switch key {
		case "planning", "coordination", "saved-work", "founder-input", "wiki":
		default:
			return CapabilitySettings{}, ErrInvalidInput
		}
	}
	for _, ids := range [][]uuid.UUID{value.DisabledPlugins, value.DisabledMCPServers} {
		for _, id := range ids {
			if !util.ValidUUID(id) {
				return CapabilitySettings{}, ErrInvalidInput
			}
		}
	}
	value.DisabledBuiltIns = append([]string{}, value.DisabledBuiltIns...)
	slices.Sort(value.DisabledBuiltIns)
	value.DisabledBuiltIns = slices.Compact(value.DisabledBuiltIns)
	value.DisabledPlugins = sortedCapabilityIDs(value.DisabledPlugins)
	value.DisabledMCPServers = sortedCapabilityIDs(value.DisabledMCPServers)
	return value, nil
}

func sortedCapabilityIDs(ids []uuid.UUID) []uuid.UUID {
	result := append([]uuid.UUID{}, ids...)
	slices.SortFunc(result, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return slices.Compact(result)
}

func capabilitySettingsFromRow(row generated.Agent) CapabilitySettings {
	stored := struct {
		Capabilities CapabilitySettings `json:"capabilities"`
	}{Capabilities: CapabilitySettings{
		EnabledTools: []string{}, DisabledBuiltIns: []string{},
		DisabledPlugins: []uuid.UUID{}, DisabledMCPServers: []uuid.UUID{},
	}}
	// Trusted structured product state; defaults cover pre-configuration rows.
	_ = json.Unmarshal(row.Settings, &stored)
	return stored.Capabilities
}

func sameCapabilitySettings(a, b CapabilitySettings) bool {
	return slices.Equal(a.EnabledTools, b.EnabledTools) &&
		slices.Equal(a.DisabledBuiltIns, b.DisabledBuiltIns) &&
		slices.Equal(a.DisabledPlugins, b.DisabledPlugins) &&
		slices.Equal(a.DisabledMCPServers, b.DisabledMCPServers)
}

// validateCapabilitySources fences a selection to ids the Company's catalog
// carries for the Agent's provider. Ids already denied stay acceptable even
// after discovery removed them, so a retained denial can always be cleared.
func validateCapabilitySources(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	provider string,
	current, target CapabilitySettings,
) error {
	sources, err := q.ListAgentCapabilitySources(
		ctx,
		generated.ListAgentCapabilitySourcesParams{CompanyID: companyID, Provider: provider},
	)
	if err != nil {
		return fmt.Errorf("read Agent capability sources: %w", err)
	}
	plugins := make(map[uuid.UUID]bool)
	connectors := make(map[uuid.UUID]bool)
	for _, source := range sources {
		if source.SourceType == "plugin" {
			plugins[source.ID] = true
		} else {
			connectors[source.ID] = true
		}
	}
	for _, id := range current.DisabledPlugins {
		plugins[id] = true
	}
	for _, id := range current.DisabledMCPServers {
		connectors[id] = true
	}
	for _, id := range target.DisabledPlugins {
		if !plugins[id] {
			return ErrInvalidInput
		}
	}
	for _, id := range target.DisabledMCPServers {
		if !connectors[id] {
			return ErrInvalidInput
		}
	}
	return nil
}
