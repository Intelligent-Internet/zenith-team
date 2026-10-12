package execenv

import (
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"

	"boardly.dev/boardly/server/pkg/agent"
)

// ensureCodexConnectorOnlyConfig replaces the run-local copy of config.toml
// with the small model/auth surface needed to reach the selected provider plus
// an exact deny-by-default execution policy. Unknown inherited keys are
// intentionally discarded: a future Codex feature must not silently widen a
// scheduled Run before Boardly has reviewed it. Inside the daemon's macOS
// sandbox Codex's read-only mode (its own Seatbelt) cannot start, so the
// config says danger-full-access and the daemon's profile fences writes.
func ensureCodexConnectorOnlyConfig(configPath string, sandboxed bool) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config.toml: %w", err)
	}
	inherited := map[string]any{}
	if len(data) != 0 {
		if err := toml.Unmarshal(data, &inherited); err != nil {
			return fmt.Errorf("parse config.toml: %w", err)
		}
	}

	config := make(map[string]any)
	for _, key := range []string{
		"disable_response_storage",
		"forced_chatgpt_workspace_id",
		"forced_login_method",
		"model",
		"model_auto_compact_token_limit",
		"model_catalog_json",
		"model_context_window",
		"model_provider",
		"model_providers",
		"model_reasoning_effort",
		"model_reasoning_summary",
		"model_reasoning_summary_format",
		"model_verbosity",
		"preferred_auth_method",
		"service_tier",
	} {
		if value, found := inherited[key]; found {
			config[key] = value
		}
	}

	config["approval_policy"] = "never"
	config["sandbox_mode"] = "read-only"
	if sandboxed {
		config["sandbox_mode"] = "danger-full-access"
	}
	config["web_search"] = "disabled"
	config["allow_login_shell"] = false
	config["features"] = agent.CodexConnectorOnlyFeatures(false)
	config["memories"] = map[string]any{
		"generate_memories": false,
		"use_memories":      false,
	}
	config["agents"] = map[string]any{"enabled": false}
	config["tools"] = map[string]any{"view_image": false}

	updated, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode config.toml: %w", err)
	}
	if err := os.WriteFile(configPath, updated, 0o600); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		return fmt.Errorf("protect config.toml: %w", err)
	}
	return nil
}
