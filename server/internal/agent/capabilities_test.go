package agent

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestStudioCapabilityIsOptInForEveryAgent(t *testing.T) {
	t.Parallel()
	for _, settings := range []string{`{}`, `{"capabilities":{"disabledBuiltIns":[]}}`} {
		got := capabilitySettingsFromRow(
			generated.Agent{Settings: json.RawMessage(settings)},
		)
		if got.EnabledTools == nil || len(got.EnabledTools) != 0 {
			t.Fatalf("defaults = %+v, want no enabled tools", got)
		}
	}
	enabled := CapabilitySettings{EnabledTools: []string{"zenith-studio"}}
	got, err := normalizeCapabilitySettings(enabled)
	if err != nil || !slices.Equal(got.EnabledTools, enabled.EnabledTools) {
		t.Fatalf("enabled = %+v, %v", got, err)
	}
	if sameCapabilitySettings(got, CapabilitySettings{}) {
		t.Fatal("enabling Studio was treated as unchanged")
	}
	for _, keys := range [][]string{{"unknown"}, {"hyperframes"}, {"zenith-studio", "zenith-studio"}} {
		if _, err := normalizeCapabilitySettings(
			CapabilitySettings{EnabledTools: keys},
		); !errors.Is(
			err,
			ErrInvalidInput,
		) {
			t.Fatalf("accepted tools %v: %v", keys, err)
		}
	}
}

// The selection is template state (ADR 0014): it carries no runtime and
// holds only denials. A stored selection from before the cutover that still
// carries a runtimeId key decodes without it.
func TestCapabilitySettingsAreTemplateStateAndContainOnlyDenials(t *testing.T) {
	pluginID := uuid.New()
	value := CapabilitySettings{
		DisabledBuiltIns: []string{"planning", "planning"},
		DisabledPlugins:  []uuid.UUID{pluginID, pluginID},
	}
	got, err := normalizeCapabilitySettings(value)
	if err != nil || len(got.DisabledBuiltIns) != 1 || len(got.DisabledPlugins) != 1 ||
		got.DisabledMCPServers == nil {
		t.Fatalf("normalized = %+v %v", got, err)
	}
	for _, bad := range []CapabilitySettings{{DisabledBuiltIns: []string{"arbitrary-tool"}}, {DisabledPlugins: []uuid.UUID{uuid.Nil}}} {
		if _, err := normalizeCapabilitySettings(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("bad setting %+v error = %v", bad, err)
		}
	}
	legacy := capabilitySettingsFromRow(generated.Agent{
		Settings: json.RawMessage(
			`{"capabilities":{"runtimeId":"` + uuid.New().
				String() +
				`","disabledPlugins":["` + pluginID.String() + `"]}}`,
		),
	})
	if len(legacy.DisabledPlugins) != 1 || legacy.DisabledPlugins[0] != pluginID {
		t.Fatalf("legacy selection = %+v, want the denial kept and the runtime key ignored", legacy)
	}
}

func TestWikiIsASwitchableBuiltInWithinTheFiveGroupLimit(t *testing.T) {
	t.Parallel()
	all := []string{"wiki", "saved-work", "planning", "founder-input", "coordination"}
	got, err := normalizeCapabilitySettings(CapabilitySettings{DisabledBuiltIns: all})
	if err != nil ||
		!slices.Equal(
			got.DisabledBuiltIns,
			[]string{"coordination", "founder-input", "planning", "saved-work", "wiki"},
		) {
		t.Fatalf("all five groups = %+v, %v", got, err)
	}
}
