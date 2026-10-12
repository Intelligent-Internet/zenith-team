package handler

import (
	"bytes"
	"encoding/json"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/util"
)

// Capability selections replace the denials and optional managed-tool opt-ins.
// Requiring denial arrays prevents omitted fields from restoring inherited access.
func parseAgentCapabilities(raw json.RawMessage) (*agent.CapabilitySettings, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var request struct {
		EnabledTools       []string  `json:"enabledTools"`
		DisabledBuiltIns   *[]string `json:"disabledBuiltIns"`
		DisabledPlugins    *[]string `json:"disabledPlugins"`
		DisabledMCPServers *[]string `json:"disabledMcpServers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(
		&request,
	); err != nil || request.DisabledBuiltIns == nil || request.DisabledPlugins == nil ||
		request.DisabledMCPServers == nil {
		return nil, errInvalidAgentJSONShape
	}
	plugins, err := parseCapabilityIDs(*request.DisabledPlugins)
	if err != nil {
		return nil, err
	}
	servers, err := parseCapabilityIDs(*request.DisabledMCPServers)
	if err != nil {
		return nil, err
	}
	return &agent.CapabilitySettings{
		EnabledTools:     request.EnabledTools,
		DisabledBuiltIns: *request.DisabledBuiltIns, DisabledPlugins: plugins, DisabledMCPServers: servers,
	}, nil
}

func parseCapabilityIDs(values []string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, len(values))
	for index, value := range values {
		id, err := util.ParseGoUUID(value)
		if err != nil {
			return nil, agent.ErrInvalidInput
		}
		ids[index] = id
	}
	return ids, nil
}
