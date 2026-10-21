package agent

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// Native managed policy can outrank a local switch. Verify provider-reported
// effective state rather than proceeding with a denial it did not honor.
func checkCodexDisabledPlugins(raw json.RawMessage, disabled []string) error {
	var response struct {
		Marketplaces *[]struct {
			Plugins []struct {
				ID      string `json:"id"`
				Enabled bool   `json:"enabled"`
			} `json:"plugins"`
		} `json:"marketplaces"`
		Errors []json.RawMessage `json:"marketplaceLoadErrors"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Marketplaces == nil || len(response.Errors) != 0 {
		return errors.New("cannot verify disabled plugins from Codex discovery")
	}
	for _, marketplace := range *response.Marketplaces {
		for _, plugin := range marketplace.Plugins {
			if plugin.Enabled && slices.Contains(disabled, plugin.ID) {
				return errors.New(
					"Codex still enables a disabled agent plugin; check provider managed settings",
				)
			}
		}
	}
	return nil
}

func checkClaudeDisabledPlugins(raw json.RawMessage, disabled []string) error {
	if len(disabled) == 0 {
		return nil
	}
	var plugins []struct {
		Name string `json:"name"`
	}
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &plugins) != nil {
		return errors.New("Claude did not report the loaded plugins needed to verify agent settings")
	}
	for _, plugin := range plugins {
		for _, id := range disabled {
			name, _, _ := strings.Cut(id, "@")
			if plugin.Name == id || plugin.Name == name {
				return errors.New(
					"Claude still loads a disabled agent plugin; check provider managed settings",
				)
			}
		}
	}
	return nil
}
