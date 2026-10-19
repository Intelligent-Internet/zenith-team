package daemon

import (
	"errors"
	"slices"
)

func checkAgentCapabilityRuntime(doc RunContext, runtimeID string) error {
	settings := doc.Agent.Capabilities
	if runToolPolicy(doc) == "" && (len(settings.DisabledPlugins) != 0 || len(settings.DisabledMCPServers) != 0) &&
		settings.RuntimeID != runtimeID {
		return errors.New("agent capability selection belongs to a different runtime")
	}
	return nil
}

// These mirror the server's frozen run input; no credentials or native paths
// are synchronized. Plugin-bundle skills follow their containing plugin.
type RunCapabilitySettings struct {
	EnabledTools       []string `json:"enabledTools"`
	RuntimeID          string   `json:"runtimeId"`
	DisabledBuiltIns   []string `json:"disabledBuiltIns"`
	DisabledPlugins    []string `json:"disabledPlugins"`
	DisabledMCPServers []string `json:"disabledMcpServers"`
}

type RunDisabledPlugin struct {
	NativeID            string                    `json:"nativeId"`
	ConnectorIdentities []PluginConnectorIdentity `json:"connectorIdentities"`
}

func disabledPluginNames(doc RunContext) []string {
	if isScheduledRun(doc) {
		return nil
	}
	names := make([]string, len(doc.Agent.DisabledPlugins))
	for index, plugin := range doc.Agent.DisabledPlugins {
		names[index] = plugin.NativeID
	}
	return names
}

func removeDisabledBoardlyTools(allowed map[string]struct{}, disabled []string) {
	for _, group := range disabled {
		var tools []string
		switch group {
		case "planning":
			tools = []string{
				"company_plan_export",
				"company_plan_transaction_apply",
				"company_plan_transaction_preview",
				"list_agents",
			}
		case "coordination":
			tools = []string{
				"mark_task_failed",
				"create_mail_drafts",
				"calendar_read",
				"calendar_write",
				"social_read",
				"social_publish",
				"search_skills",
				"load_skill_reference",
			}
		case "saved-work":
			tools = []string{"present_output", "artifact_inspect", "artifact_fetch"}
		case "founder-input":
			tools = []string{"ask_human"}
		case "wiki":
			tools = []string{"wiki_read", "wiki_search", "wiki_write"}
		}
		for _, tool := range tools {
			delete(allowed, tool)
		}
	}
}

// runOutputPresentationAvailable is the frozen saved-work authority for the
// MCP tool, its daemon channel, and automatic media presentation.
func runOutputPresentationAvailable(doc RunContext) bool {
	return runToolPolicy(doc) == "" && !isCompanyInitializationRun(doc) &&
		!slices.Contains(doc.Agent.Capabilities.DisabledBuiltIns, "saved-work")
}

// runTaskCreationAvailable is the CLI authority for `zen task create` (ADR
// 0014 D6). Creating Tasks is work coordination, so the same switch that
// withholds Task failure and drafting withholds it. Task creation has
// no MCP tool to derive it from. The root Agent outside a Task may create
// explicitly requested scheduled work; its channel row requires a schedule
// so immediate work still goes through the reviewed Company Plan.
func runTaskCreationAvailable(doc RunContext) bool {
	return runToolPolicy(doc) == "" && !isCompanyInitializationRun(doc) &&
		!slices.Contains(doc.Agent.Capabilities.DisabledBuiltIns, "coordination")
}

// runCompanyReadsAvailable is the CLI authority for reading the Company's
// Projects and listing the Run's own Tasks: every Run outside a Task,
// unless Company planning is switched off. A Task read by name is not
// behind it; every Run with a channel has that. It does not follow the
// company_plan_export tool, which is offered to the root Agent alone; the
// server scopes these reads, the export's included (ADR 0014 rule 14, Access
// TECH B6), so they are safe for any Agent.
func runCompanyReadsAvailable(doc RunContext) bool {
	return runToolPolicy(doc) == "" && doc.ExecutionProfile.Task == nil &&
		!slices.Contains(doc.Agent.Capabilities.DisabledBuiltIns, "planning")
}

func applyAgentCapabilityDenials(doc RunContext) RunContext {
	if isScheduledRun(doc) || doc.ConnectorSnapshot == nil {
		return doc
	}
	snapshot := *doc.ConnectorSnapshot
	snapshot.Connectors = make([]RunConnectorContext, 0, len(doc.ConnectorSnapshot.Connectors))
	for _, connector := range doc.ConnectorSnapshot.Connectors {
		disabled := slices.Contains(doc.Agent.Capabilities.DisabledMCPServers, connector.ID)
		for _, plugin := range doc.Agent.DisabledPlugins {
			if connector.Source == "claude_plugin" && connector.NativeID == plugin.NativeID {
				disabled = true
			}
			for _, identity := range plugin.ConnectorIdentities {
				if connector.Source == identity.Source && connector.NativeID == identity.NativeID {
					disabled = true
				}
			}
		}
		if !disabled {
			snapshot.Connectors = append(snapshot.Connectors, connector)
		}
	}
	doc.ConnectorSnapshot = &snapshot
	return doc
}
