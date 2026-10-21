package daemon

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

const maximumSafeJSONInteger int64 = 1<<53 - 1

func connectorAppsForRun(
	snapshot *RunConnectorSnapshot,
	provider string,
) ([]agent.ConnectorApp, error) {
	if snapshot == nil {
		return nil, nil
	}
	if err := validateRunConnectorSnapshot(*snapshot, provider); err != nil {
		return nil, err
	}
	apps := make([]agent.ConnectorApp, 0, len(snapshot.Connectors))
	for _, connector := range snapshot.Connectors {
		if provider == "claude" {
			continue
		}
		if provider != "codex" || connector.Source != "codex_app" {
			return nil, errors.New("run connector source has no selective execution surface")
		}
		apps = append(apps, agent.ConnectorApp{
			ID: connector.NativeID, Name: runConnectorDisplayName(connector),
			ContractDigest: connector.ContractDigest,
			Tools:          append([]agent.ConnectorTool(nil), connector.Tools...),
		})
	}
	return apps, nil
}

// runConnectorDisplayName is the app name a Run shows the model: the frozen
// display name on one line, bounded, falling back to the provider identity
// when the snapshot carries none. It is presentation only; binding always uses
// the native identity.
func runConnectorDisplayName(connector RunConnectorContext) string {
	name := strings.Join(strings.FieldsFunc(connector.Name, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}), " ")
	if name == "" {
		return connector.NativeID
	}
	if runes := []rune(name); len(runes) > 80 {
		name = strings.TrimSpace(string(runes[:80]))
	}
	return name
}

func validateRunConnectorSnapshot(snapshot RunConnectorSnapshot, provider string) error {
	if snapshot.SchemaVersion != 1 || snapshot.Connectors == nil || len(snapshot.Connectors) > 128 {
		return errors.New("run connector snapshot is invalid")
	}
	seenIDs := make(map[string]struct{}, len(snapshot.Connectors))
	seenNative := make(map[string]struct{}, len(snapshot.Connectors))
	for index, connector := range snapshot.Connectors {
		if _, err := util.ParseGoUUID(connector.ID); err != nil ||
			connector.PolicyRevision < 1 || connector.PolicyRevision > maximumSafeJSONInteger ||
			!validRunConnectorSource(connector.Source, provider) ||
			!validRunConnectorNativeID(connector.NativeID) {
			return fmt.Errorf("run connector snapshot item %d is invalid", index)
		}
		digest, err := agent.ConnectorToolContractDigest(connector.Tools)
		if err != nil || digest != connector.ContractDigest {
			return fmt.Errorf("run connector snapshot item %d tool contract is invalid", index)
		}
		for toolIndex := 1; toolIndex < len(connector.Tools); toolIndex++ {
			if connector.Tools[toolIndex-1].Name >= connector.Tools[toolIndex].Name {
				return fmt.Errorf("run connector snapshot item %d tools are not canonical", index)
			}
		}
		if provider == "claude" && slices.ContainsFunc(connector.Tools, func(tool agent.ConnectorTool) bool {
			return !tool.Enabled || tool.ReadOnly
		}) {
			return fmt.Errorf("run connector snapshot item %d is not full access", index)
		}
		identity := connector.Source + "\x00" + connector.NativeID
		if _, duplicate := seenIDs[connector.ID]; duplicate {
			return errors.New("run connector snapshot contains a duplicate ID")
		}
		if _, duplicate := seenNative[identity]; duplicate {
			return errors.New("run connector snapshot contains a duplicate native identity")
		}
		if index > 0 {
			previous := snapshot.Connectors[index-1]
			if previous.Source > connector.Source ||
				previous.Source == connector.Source && previous.NativeID > connector.NativeID ||
				previous.Source == connector.Source && previous.NativeID == connector.NativeID &&
					previous.ID >= connector.ID {
				return errors.New("run connector snapshot is not canonical")
			}
		}
		seenIDs[connector.ID] = struct{}{}
		seenNative[identity] = struct{}{}
	}
	return nil
}

func validRunConnectorSource(source, provider string) bool {
	switch provider {
	case "codex":
		return source == "codex_app"
	case "claude":
		return source == "claude_plugin" || source == "claude_mcp"
	default:
		return false
	}
}

func validRunConnectorNativeID(value string) bool {
	if value == "" || len(value) > 240 || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._:@+-", character) {
			continue
		}
		return false
	}
	return true
}
