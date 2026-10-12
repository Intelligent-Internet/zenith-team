package handler

import (
	"bytes"
	"encoding/json"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/agent/voice"
)

func parseAgentVoice(raw json.RawMessage) (*voice.Settings, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var request struct {
		Enabled       *bool  `json:"enabled"`
		Voice         string `json:"voice"`
		Greet         *bool  `json:"greet"`
		Participation string `json:"participation"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Enabled == nil || request.Greet == nil {
		return nil, errInvalidAgentJSONShape
	}
	settings := voice.Settings{
		Enabled: *request.Enabled, Voice: request.Voice,
		Greet: *request.Greet, Participation: request.Participation,
	}
	if !settings.Valid() {
		return nil, agent.ErrInvalidInput
	}
	return &settings, nil
}
