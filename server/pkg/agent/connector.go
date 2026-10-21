package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxConnectorApps          = 128
	maxAllowedMCPServers      = maxConnectorApps + 1
	maxConnectorTools         = 128
	maxConnectorToolNameBytes = 240
	connectorToolDomain       = "zenith-team.connector-tool-contract.v1\x00"
)

var (
	connectorAppIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@+-]{0,239}$`)
	connectorDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ConnectorTool is one reviewed provider-hosted app tool. It carries only
// public namespace metadata, never credentials or provider configuration.
type ConnectorTool struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	ReadOnly bool   `json:"readOnly"`
}

// ConnectorApp is the exact provider-hosted app scope frozen for one run.
// ContractDigest prevents a provider-side tool expansion from being admitted
// after that run was reviewed.
type ConnectorApp struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	ContractDigest string          `json:"contractDigest"`
	Tools          []ConnectorTool `json:"tools"`
}

type canonicalConnectorTool struct {
	Name       string `json:"name"`
	IsEnabled  bool   `json:"isEnabled"`
	IsReadOnly bool   `json:"isReadOnly"`
}

// ConnectorToolContractDigest validates and hashes the complete public tool
// namespace. Presentation-only connector fields are deliberately excluded.
func ConnectorToolContractDigest(tools []ConnectorTool) (string, error) {
	if len(tools) == 0 || len(tools) > maxConnectorTools {
		return "", fmt.Errorf("connector tool contract must contain between 1 and %d tools", maxConnectorTools)
	}
	canonical := make([]canonicalConnectorTool, len(tools))
	seen := make(map[string]struct{}, len(tools))
	hasEnabled := false
	for index, tool := range tools {
		if !validConnectorToolName(tool.Name) {
			return "", errors.New("connector tool name is invalid")
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return "", errors.New("connector tool name is duplicated")
		}
		seen[tool.Name] = struct{}{}
		hasEnabled = hasEnabled || tool.Enabled
		canonical[index] = canonicalConnectorTool{
			Name: tool.Name, IsEnabled: tool.Enabled, IsReadOnly: tool.ReadOnly,
		}
	}
	if !hasEnabled {
		return "", errors.New("connector tool contract has no enabled tool")
	}
	sort.Slice(canonical, func(left, right int) bool {
		return canonical[left].Name < canonical[right].Name
	})
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode connector tool contract: %w", err)
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, connectorToolDomain)
	_, _ = hash.Write(payload)
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func validateConnectorExecutionOptions(opts ExecOptions) error {
	switch opts.ToolPolicy {
	case "":
		if len(opts.AllowedMCPServers) != 0 {
			return errors.New("MCP namespace allowlist requires connector-only tool policy")
		}
	case ToolPolicyConnectorOnly:
		if opts.ResumeSessionID != "" {
			return errors.New("connector-only execution cannot resume a provider session")
		}
	default:
		return fmt.Errorf("unsupported tool policy %q", opts.ToolPolicy)
	}

	if len(opts.ConnectorApps) > maxConnectorApps {
		return fmt.Errorf("connector app collection exceeds %d entries", maxConnectorApps)
	}
	seenApps := make(map[string]struct{}, len(opts.ConnectorApps))
	for _, app := range opts.ConnectorApps {
		if !connectorAppIDPattern.MatchString(app.ID) ||
			strings.TrimSpace(app.Name) == "" || app.Name != strings.TrimSpace(app.Name) ||
			!connectorDigestPattern.MatchString(app.ContractDigest) {
			return errors.New("connector app identity is invalid")
		}
		if _, duplicate := seenApps[app.ID]; duplicate {
			return errors.New("connector app identity is duplicated")
		}
		digest, err := ConnectorToolContractDigest(app.Tools)
		if err != nil || digest != app.ContractDigest {
			return errors.New("connector app tool contract is invalid")
		}
		seenApps[app.ID] = struct{}{}
	}

	if len(opts.PluginDirectories) > maxConnectorApps {
		return fmt.Errorf("plugin directory collection exceeds %d entries", maxConnectorApps)
	}
	seenDirectories := make(map[string]struct{}, len(opts.PluginDirectories))
	for _, directory := range opts.PluginDirectories {
		if !filepath.IsAbs(directory) {
			return errors.New("plugin directory must be absolute")
		}
		if filepath.Clean(directory) != directory {
			return errors.New("plugin directory must be canonical")
		}
		if len(directory) < 8 || strings.ContainsAny(directory, "[]<>") {
			return errors.New("plugin directory cannot be safely redacted")
		}
		if _, duplicate := seenDirectories[directory]; duplicate {
			return errors.New("plugin directory collection contains a duplicate")
		}
		seenDirectories[directory] = struct{}{}
	}

	if len(opts.AllowedMCPServers) > maxAllowedMCPServers {
		return fmt.Errorf("MCP namespace allowlist exceeds %d entries", maxAllowedMCPServers)
	}
	allowedMCP := make(map[string]struct{}, len(opts.AllowedMCPServers))
	for _, server := range opts.AllowedMCPServers {
		if !connectorAppIDPattern.MatchString(server) {
			return errors.New("MCP namespace allowlist contains an invalid identity")
		}
		if _, duplicate := allowedMCP[server]; duplicate {
			return errors.New("MCP namespace allowlist contains a duplicate")
		}
		allowedMCP[server] = struct{}{}
	}
	if opts.ToolPolicy == ToolPolicyConnectorOnly {
		servers, err := connectorOnlyMCPServers(opts.McpConfig)
		if err != nil {
			return err
		}
		for server := range servers {
			if _, allowed := allowedMCP[server]; !allowed {
				return fmt.Errorf("MCP server %q is outside the connector-only allowlist", server)
			}
		}
	}
	return nil
}

func normalizeConnectorExecutionOptions(opts ExecOptions) ExecOptions {
	opts.McpConfig = append(json.RawMessage(nil), opts.McpConfig...)
	opts.PluginDirectories = append([]string(nil), opts.PluginDirectories...)
	opts.AllowedMCPServers = append([]string(nil), opts.AllowedMCPServers...)
	opts.ConnectorApps = cloneConnectorApps(opts.ConnectorApps)
	if opts.ToolPolicy == ToolPolicyConnectorOnly {
		if opts.PluginDirectories == nil {
			opts.PluginDirectories = []string{}
		}
		if opts.AllowedMCPServers == nil {
			opts.AllowedMCPServers = []string{}
		}
		if opts.ConnectorApps == nil {
			opts.ConnectorApps = []ConnectorApp{}
		}
		if !hasManagedMcpConfig(opts.McpConfig) {
			opts.McpConfig = json.RawMessage(`{"mcpServers":{}}`)
		}
	}
	return opts
}

func connectorOnlyMCPServers(raw json.RawMessage) (map[string]any, error) {
	if !hasManagedMcpConfig(raw) {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode connector-only MCP configuration: %w", err)
	}
	for key := range root {
		if key != "mcpServers" {
			return nil, fmt.Errorf("connector-only MCP configuration contains unsupported field %q", key)
		}
	}
	value, present := root["mcpServers"]
	if !present {
		return map[string]any{}, nil
	}
	servers, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("connector-only mcpServers must be an object")
	}
	for server, config := range servers {
		if !connectorAppIDPattern.MatchString(server) {
			return nil, errors.New("connector-only MCP configuration contains an invalid server identity")
		}
		if _, ok := config.(map[string]any); !ok {
			return nil, fmt.Errorf("connector-only MCP server %q configuration must be an object", server)
		}
	}
	return servers, nil
}

func connectorOnlyMCPToolAllowed(opts ExecOptions, tool string) bool {
	if opts.ToolPolicy != ToolPolicyConnectorOnly || !strings.HasPrefix(tool, "mcp__") {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(tool, "mcp__"), "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, server := range opts.AllowedMCPServers {
		if server == parts[0] {
			return true
		}
	}
	for _, app := range opts.ConnectorApps {
		if app.ID != parts[0] {
			continue
		}
		for _, candidate := range app.Tools {
			if candidate.Enabled && candidate.Name == parts[1] {
				return true
			}
		}
	}
	return false
}

func applyCodexConnectorOnlyThreadPolicy(params map[string]any, opts ExecOptions) {
	if opts.ToolPolicy != ToolPolicyConnectorOnly {
		return
	}
	servers, err := connectorOnlyMCPServers(opts.McpConfig)
	if err != nil {
		servers = map[string]any{}
	}
	features := CodexConnectorOnlyFeatures(len(opts.ConnectorApps) != 0)
	// Code-mode-only models defer MCP tools even when code_mode is disabled.
	// Keep the reviewed namespaces directly callable: the restricted Run has
	// no code-mode host through which to discover or invoke deferred tools.
	namespaces := make([]string, 0, len(opts.AllowedMCPServers)+len(opts.ConnectorApps))
	for _, server := range opts.AllowedMCPServers {
		namespaces = append(namespaces, "mcp__"+server)
	}
	for _, app := range opts.ConnectorApps {
		namespaces = append(namespaces, "mcp__"+app.ID)
	}
	sort.Strings(namespaces)
	features["code_mode"] = map[string]any{
		"enabled": false, "direct_only_tool_namespaces": namespaces,
	}
	features["code_mode_only_strict_3p_tools"] = false
	config := map[string]any{
		"features":          features,
		"memories":          map[string]any{"generate_memories": false, "use_memories": false},
		"agents":            map[string]any{"enabled": false},
		"tools":             map[string]any{"view_image": false},
		"allow_login_shell": false,
		"web_search":        "disabled",
		"mcp_servers":       servers,
	}
	params["config"] = config
	applyCodexConnectorScope(params, opts.ConnectorApps)
	params["approvalPolicy"] = "never"
	params["sandbox"] = "read-only"
	if opts.Sandbox != nil {
		// Codex's read-only mode is its own Seatbelt, which cannot start
		// inside the daemon's. The daemon's profile fences writes to the
		// Run's scratch and Codex's home instead.
		params["sandbox"] = "danger-full-access"
	}
	params["ephemeral"] = true
	params["excludeTurns"] = true
	params["includeApplyPatchTool"] = false
	params["persistExtendedHistory"] = false
}

func applyCodexConnectorOnlyTurnPolicy(params map[string]any, opts ExecOptions) {
	if opts.ToolPolicy != ToolPolicyConnectorOnly {
		return
	}
	params["runtimeWorkspaceRoots"] = []string{}
	if opts.Sandbox != nil {
		params["sandboxPolicy"] = map[string]any{"type": "dangerFullAccess"}
		return
	}
	params["sandboxPolicy"] = map[string]any{
		"type":          "readOnly",
		"networkAccess": false,
	}
}

func validConnectorToolName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || len(name) > maxConnectorToolNameBytes ||
		!utf8.ValidString(name) {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func cloneConnectorApps(apps []ConnectorApp) []ConnectorApp {
	if apps == nil {
		return nil
	}
	cloned := make([]ConnectorApp, len(apps))
	for index, app := range apps {
		cloned[index] = app
		cloned[index].Tools = append([]ConnectorTool(nil), app.Tools...)
	}
	return cloned
}

// CodexConnectorOnlyFeatures is shared by the run-local startup config and
// thread overrides so their native-tool restrictions cannot drift.
func CodexConnectorOnlyFeatures(apps bool) map[string]any {
	return map[string]any{
		"apps":                            apps,
		"auth_elicitation":                false,
		"browser_use":                     false,
		"browser_use_external":            false,
		"browser_use_full_cdp_access":     false,
		"code_mode":                       false,
		"code_mode_host":                  false,
		"computer_use":                    false,
		"default_mode_request_user_input": false,
		"goals":                           false,
		"hooks":                           false,
		"image_generation":                false,
		"in_app_browser":                  false,
		"memories":                        false,
		"multi_agent":                     false,
		"multi_agent_v2":                  false,
		"plugins":                         false,
		"remote_plugin":                   false,
		"recommended_plugins":             false,
		"shell_snapshot":                  false,
		"shell_tool":                      false,
		"skill_mcp_dependency_install":    false,
		"skill_search":                    false,
		"standalone_web_search":           false,
		"tool_call_mcp_elicitation":       false,
		"tool_suggest":                    false,
		"unified_exec":                    false,
		"view_image":                      false,
		"workspace_dependencies":          false,
	}
}
