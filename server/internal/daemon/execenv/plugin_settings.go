package execenv

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// CLI-level settings compose with the existing skill and directory rules.
// Neither provider's shared configuration is modified.
func prepareClaudePluginSettings(envRoot, base string, disabled []string) (string, error) {
	if len(disabled) == 0 {
		return base, nil
	}
	settings := make(map[string]any)
	if base != "" {
		data, err := os.ReadFile(base)
		if err != nil {
			return "", fmt.Errorf("read run settings: %w", err)
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			return "", fmt.Errorf("decode run settings: %w", err)
		}
	}
	plugins := make(map[string]bool, len(disabled))
	for _, name := range disabled {
		plugins[name] = false
	}
	settings["enabledPlugins"] = plugins
	data, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("encode run plugin settings: %w", err)
	}
	path := filepath.Join(envRoot, "claude-plugin-settings.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write run plugin settings: %w", err)
	}
	return path, nil
}
