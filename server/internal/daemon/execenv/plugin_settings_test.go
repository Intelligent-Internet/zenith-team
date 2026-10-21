package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudePluginDenialsComposeWithRunLocalPermissions(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(
		base,
		[]byte(`{"permissions":{"deny":["Write(//readonly/**)"]},"skillOverrides":{"design":"off"}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	result, err := prepareClaudePluginSettings(dir, base, []string{"design@local"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
		Permissions    struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if enabled, ok := value.EnabledPlugins["design@local"]; !ok || enabled || len(value.Permissions.Deny) != 1 {
		t.Fatalf("settings = %s", data)
	}
	untouched, err := os.ReadFile(base)
	if err != nil ||
		string(
			untouched,
		) != `{"permissions":{"deny":["Write(//readonly/**)"]},"skillOverrides":{"design":"off"}}` {
		t.Fatal("base settings mutated")
	}
}
