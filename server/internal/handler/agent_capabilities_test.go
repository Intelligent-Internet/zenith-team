package handler

import (
	"encoding/json"
	"testing"
)

func TestAgentCapabilitiesParseStudioOptIn(t *testing.T) {
	t.Parallel()
	const raw = `{"enabledTools":["zenith-studio"],"disabledBuiltIns":[],"disabledPlugins":[],"disabledMcpServers":[]}`
	got, err := parseAgentCapabilities(json.RawMessage(raw))
	if err != nil || got == nil || len(got.EnabledTools) != 1 || got.EnabledTools[0] != "zenith-studio" {
		t.Fatalf("parse = %+v, %v", got, err)
	}
}

func TestAgentCapabilitiesRequireCompleteStrictSelection(t *testing.T) {
	const valid = `{"disabledBuiltIns":[],"disabledPlugins":[],"disabledMcpServers":[]}`
	got, err := parseAgentCapabilities(json.RawMessage(valid))
	if err != nil || got == nil || got.DisabledBuiltIns == nil {
		t.Fatalf("parse = %+v, %v", got, err)
	}
	for _, raw := range []string{`null`, `{}`, `{"disabledBuiltIns":null,"disabledPlugins":[],"disabledMcpServers":[]}`, `{"runtimeId":"d4e87361-b733-4d84-8669-51d2abfb8b98","disabledBuiltIns":[],"disabledPlugins":[],"disabledMcpServers":[]}`, valid[:len(valid)-1] + `,"disabledSkills":[]}`} {
		if _, err := parseAgentCapabilities(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
