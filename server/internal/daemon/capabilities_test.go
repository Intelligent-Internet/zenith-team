package daemon

import (
	"slices"
	"testing"
)

func TestAgentCapabilitySelectionCannotMoveToAnotherRuntime(t *testing.T) {
	doc := RunContext{
		Agent: RunAgentContext{
			Capabilities: RunCapabilitySettings{
				RuntimeID:          "old",
				DisabledMCPServers: []string{"private-server"},
			},
		},
	}
	if err := checkAgentCapabilityRuntime(doc, "new"); err == nil {
		t.Fatal("accepted runtime mismatch")
	}
	if err := checkAgentCapabilityRuntime(doc, "old"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentCapabilityDenialsNarrowToolsAndConnectorSnapshot(t *testing.T) {
	doc := RunContext{
		Agent: RunAgentContext{
			Capabilities: RunCapabilitySettings{
				DisabledBuiltIns:   []string{"planning", "founder-input"},
				DisabledMCPServers: []string{"blocked"},
			},
			DisabledPlugins: []RunDisabledPlugin{
				{
					NativeID: "design@local",
					ConnectorIdentities: []PluginConnectorIdentity{
						{Source: "codex_mcp", NativeID: "figma"},
					},
				},
			},
		},
		ConnectorSnapshot: &RunConnectorSnapshot{
			Connectors: []RunConnectorContext{
				{ID: "blocked", Source: "codex_mcp", NativeID: "github"},
				{ID: "plugin-mcp", Source: "codex_mcp", NativeID: "figma"},
				{ID: "allowed", Source: "codex_mcp", NativeID: "calendar"},
			},
		},
	}
	tools := zenithTeamMCPToolsForRun(doc)
	for _, blocked := range []string{"ask_human", "company_plan_export", "company_plan_transaction_apply"} {
		if slices.Contains(tools, blocked) {
			t.Errorf("disabled tool still present: %s", blocked)
		}
	}
	if !slices.Contains(tools, "present_output") {
		t.Fatal("present_output MCP tool unavailable")
	}
	if !runOutputPresentationAvailable(doc) {
		t.Fatal("unrelated capability denial removed MCP output presentation")
	}
	narrowed := applyAgentCapabilityDenials(doc)
	if len(narrowed.ConnectorSnapshot.Connectors) != 1 || narrowed.ConnectorSnapshot.Connectors[0].ID != "allowed" {
		t.Fatalf("connectors = %+v", narrowed.ConnectorSnapshot)
	}
	if len(doc.ConnectorSnapshot.Connectors) != 3 {
		t.Fatal("frozen source snapshot mutated")
	}
	doc.ExecutionProfile.Source = runSourceSchedule
	scheduled := applyAgentCapabilityDenials(doc)
	if len(scheduled.ConnectorSnapshot.Connectors) != 3 ||
		len(zenithTeamMCPToolsForRun(scheduled)) != 0 || runOutputPresentationAvailable(scheduled) {
		t.Fatal("agent preferences changed reviewed Schedule scope")
	}
}

func TestSavedWorkCapabilityControlsMCPOutputPresentation(t *testing.T) {
	t.Parallel()
	doc := RunContext{}
	if !runOutputPresentationAvailable(doc) {
		t.Fatal("output presentation disabled by default")
	}
	doc.Agent.Capabilities.DisabledBuiltIns = []string{"saved-work"}
	if runOutputPresentationAvailable(doc) || slices.Contains(zenithTeamMCPToolsForRun(doc), "present_output") ||
		slices.Contains(zenithTeamMCPToolsForRun(doc), "artifact_inspect") ||
		slices.Contains(zenithTeamMCPToolsForRun(doc), "artifact_fetch") {
		t.Fatal("saved-work denial did not disable MCP output presentation")
	}
}

func TestStudioToolsAreOptInAcrossInteractiveRuns(t *testing.T) {
	t.Parallel()
	for _, root := range []bool{false, true} {
		for _, task := range []*RunTaskContext{nil, {Title: "Create media"}} {
			doc := RunContext{
				Agent:            RunAgentContext{IsRoot: root},
				ExecutionProfile: RunExecutionProfileContext{Task: task},
			}
			if studioEnabledForRun(doc) {
				t.Fatal("Studio enabled by default")
			}
			doc.Agent.Capabilities.EnabledTools = []string{"zenith-studio"}
			doc.Agent.Capabilities.DisabledBuiltIns = []string{"coordination"}
			if !studioEnabledForRun(doc) {
				t.Fatal("Studio missing from interactive Run")
			}
			for _, tool := range []string{"search_skills", "load_skill_reference"} {
				if !slices.Contains(zenithTeamMCPToolsForRun(doc), tool) {
					t.Fatalf("Studio cannot load instructions: missing %s", tool)
				}
			}
			doc.ExecutionProfile.Source = runSourceSchedule
			if studioEnabledForRun(doc) {
				t.Fatal("Studio widened Schedule authority")
			}
		}
	}
}
