package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

const (
	claudeConnectorDiscoveryTimeout = 20 * time.Second
	claudeConnectorConnectTimeout   = 60 * time.Second
	claudeConnectorOutputLimit      = 1 << 20
	claudeConnectorConfigLimit      = 4 << 20
	// claudePluginInstallCountMaximum preserves exact values through JSON clients.
	claudePluginInstallCountMaximum = 1<<53 - 1

	claudeOfficialMarketplaceName     = "claude-plugins-official"
	claudeOfficialMarketplaceRepo     = "anthropics/claude-plugins-official"
	claudeManagedInstruction          = "Manage this connector with /mcp in Claude Code, then refresh."
	claudeLocalConnectorManagementURL = "https://code.claude.com/docs/en/mcp"
	claudeCloudConnectorManagementURL = "https://claude.com/connectors"
)

var (
	errClaudeConnectorNotDiscovered = errors.New("claude connector was not discovered")
	errClaudeConnectorCommand       = errors.New("claude connector command failed")
	errClaudeConnectorOutput        = errors.New("claude connector output is invalid")
	// errClaudeConnectorContractDrift means a selected local connector no
	// longer matches the sanitized contract frozen into its Run claim.
	errClaudeConnectorContractDrift = errors.New("Claude connector contract changed")

	claudeConnectorTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	claudeANSIPattern           = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
)

// claudeOfficialConnectorPlugins is deliberately bounded rather than inferred
// from descriptions. It was reviewed against Anthropic's official marketplace
// at commit c54b5608d9be1910de9a5b91c2d15bf6673b9c35:
// https://github.com/anthropics/claude-plugins-official/tree/c54b5608d9be1910de9a5b91c2d15bf6673b9c35
//
// Installed plugins outside this list are still eligible when Claude's own
// installed metadata contains a non-empty mcpServers object.
var claudeOfficialConnectorPlugins = map[string]string{
	"asana":          "Asana",
	"atlassian":      "Atlassian",
	"circleback":     "Circleback",
	"context7":       "Context7",
	"figma":          "Figma",
	"firebase":       "Firebase",
	"firecrawl":      "Firecrawl",
	"github":         "GitHub",
	"gitlab":         "GitLab",
	"greptile":       "Greptile",
	"laravel-boost":  "Laravel Boost",
	"linear":         "Linear",
	"notion":         "Notion",
	"pinecone":       "Pinecone",
	"playwright":     "Playwright",
	"posthog":        "PostHog",
	"semgrep":        "Semgrep",
	"sentry":         "Sentry",
	"serena":         "Serena",
	"slack":          "Slack",
	"sonatype-guide": "Sonatype Guide",
	"stripe":         "Stripe",
	"supabase":       "Supabase",
	"vercel":         "Vercel",
}

type ClaudeConnectorStatus string

const (
	ClaudeConnectorStatusAvailable           ClaudeConnectorStatus = "available"
	ClaudeConnectorStatusInstalled           ClaudeConnectorStatus = "installed"
	ClaudeConnectorStatusConnected           ClaudeConnectorStatus = "connected"
	ClaudeConnectorStatusNeedsAuthentication ClaudeConnectorStatus = "needs_authentication"
	ClaudeConnectorStatusManagedInProvider   ClaudeConnectorStatus = "managed_in_provider"
)

type ClaudeConnectorKind string

const (
	ClaudeConnectorKindPlugin ClaudeConnectorKind = "plugin"
	ClaudeConnectorKindMCP    ClaudeConnectorKind = "mcp"
	ClaudeConnectorKindCloud  ClaudeConnectorKind = "cloud"
)

// ClaudeConnector is the deliberately redacted card exposed by the daemon.
// Command lines, paths, MCP configuration, headers, environment variables, and
// provider output never cross this boundary.
type ClaudeConnector struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Provider string                `json:"provider"`
	Kind     ClaudeConnectorKind   `json:"kind"`
	Status   ClaudeConnectorStatus `json:"status"`
	// CanExecute and its tool contract contain only sanitized MCP namespace
	// names. Provider-local configuration remains in ClaudeConnectorBackend.
	CanExecute     bool                  `json:"canExecute"`
	ContractDigest string                `json:"contractDigest,omitempty"`
	Tools          []agent.ConnectorTool `json:"tools,omitempty"`
	// InstallCount is the provider-reported plugin install count, not a distinct-user count.
	InstallCount *int64 `json:"installCount,omitempty"`
}

type ClaudeConnectorCatalog struct {
	Connectors    []ClaudeConnector `json:"connectors"`
	CloudReusable bool              `json:"cloudReusable"`
}

type ClaudeConnectorConnectResult struct {
	ID               string                `json:"id"`
	Status           ClaudeConnectorStatus `json:"status"`
	AuthorizationURL string                `json:"authorizationUrl,omitempty"`
	Instruction      string                `json:"instruction,omitempty"`
}

type claudeConnectorCommandOutput struct {
	stdout []byte
	stderr []byte
}

type claudeConnectorCommandRunner interface {
	Output(context.Context, string, []string) (claudeConnectorCommandOutput, error)
}

type claudeDiscoveredConnector struct {
	card       ClaudeConnector
	pluginName string
	mcpTarget  string
	installed  bool
	execution  claudeConnectorExecution
}

type claudeConnectorExecution struct {
	pluginDirectory  string
	mcpServers       map[string]json.RawMessage
	allowedMCPServer []string
}

// claudeConnectorSelection is the sanitized claim identity revalidated against
// provider-local discovery immediately before execution.
type claudeConnectorSelection struct {
	Kind           ClaudeConnectorKind
	NativeID       string
	ContractDigest string
	Tools          []agent.ConnectorTool
}

// claudeConnectorResolution is local-only invocation state. It is never part
// of connector discovery, a Run claim, or any Cloud request.
type claudeConnectorResolution struct {
	PluginDirectories []string
	MCPServers        map[string]json.RawMessage
	AllowedMCPServers []string
}

type ClaudeConnectorBackend struct {
	mu                 sync.Mutex
	path               string
	runner             claudeConnectorCommandRunner
	discovered         map[string]claudeDiscoveredConnector
	loadUserMCPServers func() (map[string]json.RawMessage, error)
}

func newClaudeConnectorBackend(
	path string,
	runner claudeConnectorCommandRunner,
) *ClaudeConnectorBackend {
	return &ClaudeConnectorBackend{
		path: path, runner: runner, loadUserMCPServers: readClaudeUserMCPServers,
	}
}

// NewOSClaudeConnectorBackend creates a connector backend for one discovered
// Claude executable. The executable path stays local to the daemon.
func NewOSClaudeConnectorBackend(path string) *ClaudeConnectorBackend {
	return newClaudeConnectorBackend(path, osClaudeConnectorCommandRunner{})
}

func (b *ClaudeConnectorBackend) Discover(ctx context.Context) (ClaudeConnectorCatalog, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	cards, discovered := claudeCloudConnectorCatalog()
	b.discovered = nil
	discoveryCtx, cancel := context.WithTimeout(ctx, claudeConnectorDiscoveryTimeout)
	defer cancel()
	cloudReusable := false
	if authOutput, authErr := b.output(discoveryCtx, []string{"auth", "status", "--json"}); authErr == nil {
		cloudReusable = parseClaudeCloudReuse(authOutput.stdout)
	}

	installedOutput, err := b.output(discoveryCtx, []string{"plugin", "list", "--json"})
	if err != nil {
		b.discovered = discovered
		return ClaudeConnectorCatalog{Connectors: cards, CloudReusable: cloudReusable}, nil
	}
	installedPlugins, err := parseClaudePluginList(installedOutput.stdout, claudePluginListInstalled)
	if err != nil {
		return ClaudeConnectorCatalog{}, err
	}

	officialMarketplace := false
	marketplaceOutput, marketplaceErr := b.output(
		discoveryCtx,
		[]string{"plugin", "marketplace", "list", "--json"},
	)
	if marketplaceErr == nil {
		officialMarketplace, err = parseClaudeOfficialMarketplace(marketplaceOutput.stdout)
		if err != nil {
			return ClaudeConnectorCatalog{}, err
		}
	}

	availablePlugins := []claudePluginMetadata(nil)
	availableOutput, availableErr := b.output(
		discoveryCtx,
		[]string{"plugin", "list", "--available", "--json"},
	)
	if availableErr == nil {
		availablePlugins, err = parseClaudePluginList(availableOutput.stdout, claudePluginListAvailable)
		if err != nil {
			return ClaudeConnectorCatalog{}, err
		}
	}

	var health claudeMCPInventory
	healthOutput, healthErr := b.output(discoveryCtx, []string{"mcp", "list"})
	if healthErr == nil {
		health = parseClaudeMCPHealth(healthOutput.stdout)
	}
	standaloneConfigs := map[string]json.RawMessage{}
	if b.loadUserMCPServers != nil {
		if loaded, loadErr := b.loadUserMCPServers(); loadErr == nil {
			standaloneConfigs = loaded
		}
	}

	pluginCards := make([]ClaudeConnector, 0, len(installedPlugins)+len(availablePlugins))
	for _, plugin := range installedPlugins {
		officialConnector := officialMarketplace && isClaudeOfficialConnector(plugin)
		if !plugin.hasMCPServers && !officialConnector {
			continue
		}
		if officialConnector {
			if available, ok := findClaudePlugin(availablePlugins, plugin.id); ok {
				plugin.installCount = available.installCount
			}
		}
		status, _ := claudePluginHealth(plugin.pluginName, health.plugins)
		card := claudePluginConnectorCard(plugin, status)
		execution := claudePluginExecutionContract(plugin, health.plugins)
		applyClaudeExecutionContract(&card, execution)
		pluginCards = append(pluginCards, card)
		discovered[card.ID] = claudeDiscoveredConnector{
			card:       cloneClaudeConnector(card),
			pluginName: plugin.pluginName,
			installed:  true,
			execution:  execution,
		}
	}
	for _, entry := range health.standalone {
		card := ClaudeConnector{
			ID: entry.target, Name: entry.target, Provider: "claude",
			Kind: ClaudeConnectorKindMCP, Status: claudeHealthConnectorStatus(entry.status),
		}
		execution := claudeStandaloneExecutionContract(entry, standaloneConfigs[entry.target])
		applyClaudeExecutionContract(&card, execution)
		pluginCards = append(pluginCards, card)
		discovered[card.ID] = claudeDiscoveredConnector{
			card: cloneClaudeConnector(card), mcpTarget: entry.target,
			installed: true, execution: execution,
		}
	}
	for _, plugin := range availablePlugins {
		if !officialMarketplace || !isClaudeOfficialConnector(plugin) {
			continue
		}
		if _, installed := discovered[plugin.id]; installed {
			continue
		}
		card := claudePluginConnectorCard(plugin, ClaudeConnectorStatusAvailable)
		pluginCards = append(pluginCards, card)
		discovered[card.ID] = claudeDiscoveredConnector{
			card:       card,
			pluginName: plugin.pluginName,
		}
	}
	sort.Slice(pluginCards, func(left, right int) bool {
		if pluginCards[left].Name == pluginCards[right].Name {
			return pluginCards[left].ID < pluginCards[right].ID
		}
		return pluginCards[left].Name < pluginCards[right].Name
	})
	cards = append(cards, pluginCards...)
	b.discovered = discovered
	return ClaudeConnectorCatalog{Connectors: cards, CloudReusable: cloudReusable}, nil
}

func (b *ClaudeConnectorBackend) Connect(
	ctx context.Context,
	id string,
) (ClaudeConnectorConnectResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	connector, ok := b.discovered[id]
	if !ok {
		return ClaudeConnectorConnectResult{}, errClaudeConnectorNotDiscovered
	}
	if connector.card.Kind == ClaudeConnectorKindCloud {
		return claudeManagedConnectorResult(id), nil
	}

	connectCtx, cancel := context.WithTimeout(ctx, claudeConnectorConnectTimeout)
	defer cancel()
	installedBeforeConnect := connector.installed
	if connector.card.Kind == ClaudeConnectorKindPlugin && !connector.installed {
		if _, err := b.output(connectCtx, []string{"plugin", "install", connector.card.ID}); err != nil {
			return ClaudeConnectorConnectResult{}, errClaudeConnectorCommand
		}
		installedOutput, err := b.output(connectCtx, []string{"plugin", "list", "--json"})
		if err != nil {
			return ClaudeConnectorConnectResult{}, errClaudeConnectorCommand
		}
		plugins, err := parseClaudePluginList(installedOutput.stdout, claudePluginListInstalled)
		if err != nil {
			return ClaudeConnectorConnectResult{}, err
		}
		installed, ok := findClaudePlugin(plugins, connector.card.ID)
		if !ok || (!installed.hasMCPServers && !isClaudeOfficialConnector(installed)) {
			return ClaudeConnectorConnectResult{}, errClaudeConnectorOutput
		}
		connector.installed = true
		b.discovered[id] = connector
	}

	healthOutput, err := b.output(connectCtx, []string{"mcp", "list"})
	if err != nil {
		return claudeManagedConnectorResult(id), nil
	}
	health := parseClaudeMCPHealth(healthOutput.stdout)
	var status ClaudeConnectorStatus
	var loginTarget string
	switch connector.card.Kind {
	case ClaudeConnectorKindPlugin:
		status, loginTarget = claudePluginHealth(connector.pluginName, health.plugins)
	case ClaudeConnectorKindMCP:
		status, loginTarget = claudeStandaloneMCPHealth(connector.mcpTarget, health.standalone)
	default:
		return claudeManagedConnectorResult(id), nil
	}
	if status == ClaudeConnectorStatusConnected {
		if installedBeforeConnect {
			return claudeManagedConnectorResult(id), nil
		}
		return ClaudeConnectorConnectResult{ID: id, Status: status}, nil
	}
	if status != ClaudeConnectorStatusNeedsAuthentication || loginTarget == "" {
		return claudeManagedConnectorResult(id), nil
	}

	return claudeManagedConnectorResult(id), nil
}

// resolveExecution revalidates sanitized claim contracts against the latest
// provider-local discovery and returns only the selected local invocation
// material. Paths, commands, environment values, and credentials never leave
// this process through connector discovery or claim payloads.
func (b *ClaudeConnectorBackend) resolveExecution(
	ctx context.Context,
	selections []claudeConnectorSelection,
) (claudeConnectorResolution, error) {
	if b == nil {
		return claudeConnectorResolution{}, errClaudeConnectorContractDrift
	}
	if err := ctx.Err(); err != nil {
		return claudeConnectorResolution{}, err
	}
	// Re-run provider discovery so an install, namespace, authentication, or
	// local configuration change after the Cloud observation fails closed.
	if _, err := b.Discover(ctx); err != nil {
		return claudeConnectorResolution{}, errClaudeConnectorContractDrift
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	result := claudeConnectorResolution{
		PluginDirectories: []string{},
		MCPServers:        map[string]json.RawMessage{},
		AllowedMCPServers: []string{},
	}
	seenConnectors := make(map[string]struct{}, len(selections))
	seenDirectories := make(map[string]struct{}, len(selections))
	seenAllowed := make(map[string]struct{})
	for _, selection := range selections {
		connector, ok := b.discovered[selection.NativeID]
		if !ok || connector.card.Kind != selection.Kind || !connector.card.CanExecute ||
			connector.card.ContractDigest != selection.ContractDigest ||
			!slices.Equal(connector.card.Tools, selection.Tools) {
			return claudeConnectorResolution{}, errClaudeConnectorContractDrift
		}
		if _, duplicate := seenConnectors[selection.NativeID]; duplicate {
			return claudeConnectorResolution{}, errClaudeConnectorContractDrift
		}
		seenConnectors[selection.NativeID] = struct{}{}
		switch selection.Kind {
		case ClaudeConnectorKindPlugin:
			directory := connector.execution.pluginDirectory
			info, err := os.Stat(directory)
			if err != nil || !info.IsDir() {
				return claudeConnectorResolution{}, errClaudeConnectorContractDrift
			}
			if _, duplicate := seenDirectories[directory]; duplicate {
				return claudeConnectorResolution{}, errClaudeConnectorContractDrift
			}
			seenDirectories[directory] = struct{}{}
			result.PluginDirectories = append(result.PluginDirectories, directory)
		case ClaudeConnectorKindMCP:
			for name, config := range connector.execution.mcpServers {
				if _, duplicate := result.MCPServers[name]; duplicate {
					return claudeConnectorResolution{}, errClaudeConnectorContractDrift
				}
				result.MCPServers[name] = append(json.RawMessage(nil), config...)
			}
		default:
			return claudeConnectorResolution{}, errClaudeConnectorContractDrift
		}
		for _, server := range connector.execution.allowedMCPServer {
			if _, duplicate := seenAllowed[server]; duplicate {
				return claudeConnectorResolution{}, errClaudeConnectorContractDrift
			}
			seenAllowed[server] = struct{}{}
			result.AllowedMCPServers = append(result.AllowedMCPServers, server)
		}
	}
	slices.Sort(result.PluginDirectories)
	slices.Sort(result.AllowedMCPServers)
	return result, nil
}

func applyClaudeExecutionContract(
	card *ClaudeConnector,
	execution claudeConnectorExecution,
) {
	if card == nil || len(execution.allowedMCPServer) == 0 {
		return
	}
	tools := make([]agent.ConnectorTool, len(execution.allowedMCPServer))
	for index, namespace := range execution.allowedMCPServer {
		tools[index] = agent.ConnectorTool{Name: namespace, Enabled: true}
	}
	digest, err := agent.ConnectorToolContractDigest(tools)
	if err != nil {
		return
	}
	card.CanExecute = card.Status == ClaudeConnectorStatusConnected
	if !card.CanExecute {
		return
	}
	card.ContractDigest = digest
	card.Tools = tools
}

func cloneClaudeConnector(connector ClaudeConnector) ClaudeConnector {
	connector.Tools = append([]agent.ConnectorTool(nil), connector.Tools...)
	return connector
}

func claudePluginExecutionContract(
	plugin claudePluginMetadata,
	health map[string][]claudeMCPHealth,
) claudeConnectorExecution {
	if !plugin.enabled || !validClaudePluginDirectory(plugin.installPath) ||
		len(plugin.mcpServers) == 0 {
		return claudeConnectorExecution{}
	}
	entries := health[strings.ToLower(plugin.pluginName)]
	if len(entries) != len(plugin.mcpServers) {
		return claudeConnectorExecution{}
	}
	statuses := make(map[string]claudeMCPHealthStatus, len(entries))
	for _, entry := range entries {
		if entry.serverName == "" {
			return claudeConnectorExecution{}
		}
		if _, duplicate := statuses[entry.serverName]; duplicate {
			return claudeConnectorExecution{}
		}
		statuses[entry.serverName] = entry.status
	}
	allowed := make([]string, 0, len(plugin.mcpServers))
	for _, server := range plugin.mcpServers {
		if statuses[server] != claudeMCPHealthConnected {
			return claudeConnectorExecution{}
		}
		allowed = append(
			allowed,
			claudeMCPRuntimeName("plugin:"+plugin.pluginName+":"+server),
		)
	}
	if !uniqueNonemptyStrings(allowed) {
		return claudeConnectorExecution{}
	}
	slices.Sort(allowed)
	return claudeConnectorExecution{
		pluginDirectory:  plugin.installPath,
		allowedMCPServer: allowed,
	}
}

func claudeStandaloneExecutionContract(
	health claudeMCPHealth,
	config json.RawMessage,
) claudeConnectorExecution {
	if health.status != claudeMCPHealthConnected || !validClaudeMCPConfig(config) {
		return claudeConnectorExecution{}
	}
	server := claudeMCPRuntimeName(health.target)
	if server == "" {
		return claudeConnectorExecution{}
	}
	return claudeConnectorExecution{
		mcpServers: map[string]json.RawMessage{
			health.target: append(json.RawMessage(nil), config...),
		},
		allowedMCPServer: []string{server},
	}
}

func claudeMCPRuntimeName(value string) string {
	return strings.Map(func(character rune) rune {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' {
			return character
		}
		return '_'
	}, value)
}

func uniqueNonemptyStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validClaudePluginDirectory(value string) bool {
	return len(value) >= 8 && len(value) <= 4096 && filepath.IsAbs(value) &&
		filepath.Clean(value) == value && !strings.ContainsAny(value, "\x00[]<>")
}

func validClaudeMCPConfig(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || len(trimmed) > claudeConnectorConfigLimit || trimmed[0] != '{' {
		return false
	}
	var config map[string]json.RawMessage
	return decodeSingleClaudeJSON(trimmed, &config)
}

func readClaudeUserMCPServers() (map[string]json.RawMessage, error) {
	path, err := claudeGlobalConfigPath()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, claudeConnectorConfigLimit+1))
	if err != nil || len(content) > claudeConnectorConfigLimit {
		return nil, errClaudeConnectorOutput
	}
	var root map[string]json.RawMessage
	if !decodeSingleClaudeJSON(content, &root) {
		return nil, errClaudeConnectorOutput
	}
	var servers map[string]json.RawMessage
	if raw := root["mcpServers"]; len(raw) != 0 && json.Unmarshal(raw, &servers) != nil {
		return nil, errClaudeConnectorOutput
	}
	result := make(map[string]json.RawMessage, len(servers))
	for name, config := range servers {
		if !claudeConnectorTokenPattern.MatchString(name) || !validClaudeMCPConfig(config) {
			return nil, errClaudeConnectorOutput
		}
		result[name] = append(json.RawMessage(nil), config...)
	}
	return result, nil
}

func claudeGlobalConfigPath() (string, error) {
	configDirectory := os.Getenv("CLAUDE_CONFIG_DIR")
	if configDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDirectory = filepath.Join(home, ".claude")
		if _, err := os.Stat(filepath.Join(configDirectory, ".config.json")); err == nil {
			return filepath.Join(configDirectory, ".config.json"), nil
		}
		return filepath.Join(home, ".claude.json"), nil
	}
	if _, err := os.Stat(filepath.Join(configDirectory, ".config.json")); err == nil {
		return filepath.Join(configDirectory, ".config.json"), nil
	}
	return filepath.Join(configDirectory, ".claude.json"), nil
}

func (b *ClaudeConnectorBackend) output(
	ctx context.Context,
	args []string,
) (claudeConnectorCommandOutput, error) {
	if b.runner == nil || !validClaudeConnectorCommand(args) {
		return claudeConnectorCommandOutput{}, errClaudeConnectorCommand
	}
	output, err := b.runner.Output(ctx, b.path, slices.Clone(args))
	if err != nil {
		return output, errClaudeConnectorCommand
	}
	if !claudeCommandOutputFits(args, len(output.stdout), len(output.stderr)) {
		return claudeConnectorCommandOutput{}, errClaudeConnectorCommand
	}
	return output, nil
}

// claudeCommandOutputLimit is the stdout ceiling of one Claude command; every
// stderr gets claudeConnectorOutputLimit. The available-plugin list has its
// own, 16 MiB: it lists every plugin of every added marketplace, 1.8 MB for
// 2,594 plugins in September 2026 with claude-community added, against
// 156 KB for claude-plugins-official alone.
func claudeCommandOutputLimit(args []string) int {
	if slices.Equal(args, []string{"plugin", "list", "--available", "--json"}) {
		return 16 << 20
	}
	return claudeConnectorOutputLimit
}

// claudeCommandOutputFits reports whether a command's output is within its
// ceilings, and logs the sizes when it is not: the catalog that command feeds
// stops updating, and the daemon log is the only place that says why. The
// arguments are an allowlisted command, never provider output.
func claudeCommandOutputFits(args []string, stdoutBytes, stderrBytes int) bool {
	limit := claudeCommandOutputLimit(args)
	if stdoutBytes <= limit && stderrBytes <= claudeConnectorOutputLimit {
		return true
	}
	slog.Warn(
		"Claude command output exceeds its limit",
		"command", strings.Join(args, " "), "stdout_bytes", stdoutBytes,
		"stderr_bytes", stderrBytes, "limit", limit,
	)
	return false
}

func validClaudeConnectorCommand(args []string) bool {
	switch {
	case slices.Equal(args, []string{"auth", "status", "--json"}):
		return true
	case slices.Equal(args, []string{"plugin", "list", "--json"}):
		return true
	case slices.Equal(args, []string{"plugin", "list", "--available", "--json"}):
		return true
	case slices.Equal(args, []string{"plugin", "marketplace", "list", "--json"}):
		return true
	case slices.Equal(args, []string{"mcp", "list"}):
		return true
	case len(args) == 3 && args[0] == "plugin" && args[1] == "install":
		_, _, ok := parseClaudePluginID(args[2])
		return ok
	default:
		return false
	}
}

func parseClaudeCloudReuse(body []byte) bool {
	var status struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		APIProvider      string `json:"apiProvider"`
		SubscriptionType string `json:"subscriptionType"`
	}
	// Claude may add presentation fields. Decode one bounded object and retain
	// only the four non-secret capability signals.
	var raw map[string]json.RawMessage
	if !decodeSingleClaudeJSON(body, &raw) ||
		json.Unmarshal(raw["loggedIn"], &status.LoggedIn) != nil ||
		json.Unmarshal(raw["authMethod"], &status.AuthMethod) != nil ||
		json.Unmarshal(raw["apiProvider"], &status.APIProvider) != nil ||
		json.Unmarshal(raw["subscriptionType"], &status.SubscriptionType) != nil {
		return false
	}
	return status.LoggedIn && status.AuthMethod == "claude.ai" &&
		status.APIProvider == "firstParty" && status.SubscriptionType != ""
}

func decodeSingleClaudeJSON(body []byte, destination any) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(destination) != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

type claudePluginListType uint8

const (
	claudePluginListInstalled claudePluginListType = iota + 1
	claudePluginListAvailable
)

type claudePluginMetadata struct {
	id            string
	pluginName    string
	marketplace   string
	hasMCPServers bool
	enabled       bool
	installPath   string
	mcpServers    []string
	installCount  *int64
}

type claudePluginJSON struct {
	ID              string          `json:"id"`
	PluginID        string          `json:"pluginId"`
	MarketplaceName string          `json:"marketplaceName"`
	MCPServers      json.RawMessage `json:"mcpServers"`
	Enabled         bool            `json:"enabled"`
	InstallPath     string          `json:"installPath"`
	InstallCount    json.RawMessage `json:"installCount"`
}

func parseClaudePluginList(body []byte, listType claudePluginListType) ([]claudePluginMetadata, error) {
	var raw json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&raw); err != nil {
		return nil, errClaudeConnectorOutput
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errClaudeConnectorOutput
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errClaudeConnectorOutput
	}

	var pluginsJSON []claudePluginJSON
	switch raw[0] {
	case '[':
		if err := json.Unmarshal(raw, &pluginsJSON); err != nil {
			return nil, errClaudeConnectorOutput
		}
	case '{':
		var envelope struct {
			Installed json.RawMessage `json:"installed"`
			Available json.RawMessage `json:"available"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, errClaudeConnectorOutput
		}
		selected := envelope.Installed
		if listType == claudePluginListAvailable {
			selected = envelope.Available
		}
		if len(selected) == 0 || json.Unmarshal(selected, &pluginsJSON) != nil {
			return nil, errClaudeConnectorOutput
		}
	default:
		return nil, errClaudeConnectorOutput
	}

	plugins := make([]claudePluginMetadata, 0, len(pluginsJSON))
	seen := make(map[string]struct{}, len(pluginsJSON))
	for _, rawPlugin := range pluginsJSON {
		id := rawPlugin.ID
		if listType == claudePluginListAvailable {
			id = rawPlugin.PluginID
		}
		pluginName, marketplace, ok := parseClaudePluginID(id)
		if !ok || listType == claudePluginListAvailable && rawPlugin.MarketplaceName != marketplace {
			return nil, errClaudeConnectorOutput
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, errClaudeConnectorOutput
		}
		seen[id] = struct{}{}
		mcpServers, hasMCPServers := claudeMCPServerMetadata(rawPlugin.MCPServers)
		var installCount *int64
		if listType == claudePluginListAvailable {
			installCount = parseClaudePluginInstallCount(rawPlugin.InstallCount)
		}
		plugins = append(plugins, claudePluginMetadata{
			id:            id,
			pluginName:    pluginName,
			marketplace:   marketplace,
			hasMCPServers: hasMCPServers,
			enabled:       rawPlugin.Enabled,
			installPath:   rawPlugin.InstallPath,
			mcpServers:    mcpServers,
			installCount:  installCount,
		})
	}
	return plugins, nil
}

func parseClaudePluginInstallCount(raw json.RawMessage) *int64 {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return nil
	}
	// installCount is undocumented best-effort metadata. Ignore schema drift so
	// an optional presentation signal can never make connector discovery fail.
	var count int64
	if json.Unmarshal(value, &count) != nil || count < 0 || count > claudePluginInstallCountMaximum {
		return nil
	}
	return &count
}

func parseClaudePluginID(id string) (string, string, bool) {
	if strings.Count(id, "@") != 1 {
		return "", "", false
	}
	pluginName, marketplace, _ := strings.Cut(id, "@")
	if !claudeConnectorTokenPattern.MatchString(pluginName) ||
		!claudeConnectorTokenPattern.MatchString(marketplace) {
		return "", "", false
	}
	return pluginName, marketplace, true
}

func claudeMCPServerMetadata(raw json.RawMessage) ([]string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	var servers map[string]json.RawMessage
	if json.Unmarshal(raw, &servers) != nil || len(servers) == 0 {
		return nil, false
	}
	names := make([]string, 0, len(servers))
	for name, config := range servers {
		if !claudeConnectorTokenPattern.MatchString(name) || !validClaudeMCPConfig(config) {
			return nil, false
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, true
}

func isClaudeOfficialConnector(plugin claudePluginMetadata) bool {
	if plugin.marketplace != claudeOfficialMarketplaceName {
		return false
	}
	_, ok := claudeOfficialConnectorPlugins[strings.ToLower(plugin.pluginName)]
	return ok
}

func findClaudePlugin(plugins []claudePluginMetadata, id string) (claudePluginMetadata, bool) {
	for _, plugin := range plugins {
		if plugin.id == id {
			return plugin, true
		}
	}
	return claudePluginMetadata{}, false
}

func parseClaudeOfficialMarketplace(body []byte) (bool, error) {
	var raw json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&raw); err != nil {
		return false, errClaudeConnectorOutput
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return false, errClaudeConnectorOutput
	}
	var marketplaces []struct {
		Name   string `json:"name"`
		Source string `json:"source"`
		Repo   string `json:"repo"`
	}
	if json.Unmarshal(raw, &marketplaces) != nil {
		return false, errClaudeConnectorOutput
	}
	for _, marketplace := range marketplaces {
		if marketplace.Name == claudeOfficialMarketplaceName &&
			marketplace.Source == "github" &&
			marketplace.Repo == claudeOfficialMarketplaceRepo {
			return true, nil
		}
	}
	return false, nil
}

type claudeMCPHealthStatus uint8

const (
	claudeMCPHealthUnknown claudeMCPHealthStatus = iota
	claudeMCPHealthConnected
	claudeMCPHealthNeedsAuthentication
)

type claudeMCPHealth struct {
	target     string
	serverName string
	status     claudeMCPHealthStatus
}

type claudeMCPInventory struct {
	plugins    map[string][]claudeMCPHealth
	standalone []claudeMCPHealth
}

func parseClaudeMCPHealth(body []byte) claudeMCPInventory {
	health := claudeMCPInventory{plugins: make(map[string][]claudeMCPHealth)}
	cleaned := claudeANSIPattern.ReplaceAll(body, nil)
	for line := range strings.Lines(string(cleaned)) {
		line = strings.TrimSpace(line)
		separator := strings.LastIndex(line, " - ")
		if separator < 0 {
			continue
		}
		status := parseClaudeMCPHealthStatus(line[separator+3:])
		if !strings.HasPrefix(line, "plugin:") {
			name, configuration, ok := strings.Cut(line[:separator], ":")
			name = strings.TrimSpace(name)
			if !ok || !claudeConnectorTokenPattern.MatchString(name) ||
				strings.TrimSpace(configuration) == "" {
				continue
			}
			health.standalone = append(health.standalone, claudeMCPHealth{
				target: name, status: status,
			})
			continue
		}
		pluginName, remainder, ok := strings.Cut(strings.TrimPrefix(line, "plugin:"), ":")
		if !ok || !claudeConnectorTokenPattern.MatchString(pluginName) {
			continue
		}
		serverName, _, ok := strings.Cut(remainder, ":")
		if !ok || !claudeConnectorTokenPattern.MatchString(serverName) {
			continue
		}
		key := strings.ToLower(pluginName)
		health.plugins[key] = append(health.plugins[key], claudeMCPHealth{
			target:     "plugin:" + pluginName + ":" + serverName,
			serverName: serverName,
			status:     status,
		})
	}
	sort.Slice(health.standalone, func(left, right int) bool {
		return health.standalone[left].target < health.standalone[right].target
	})
	return health
}

func parseClaudeMCPHealthStatus(value string) claudeMCPHealthStatus {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "✓ connected", "✔ connected", "connected":
		return claudeMCPHealthConnected
	case "! needs authentication", "needs authentication":
		return claudeMCPHealthNeedsAuthentication
	default:
		return claudeMCPHealthUnknown
	}
}

func claudeHealthConnectorStatus(status claudeMCPHealthStatus) ClaudeConnectorStatus {
	switch status {
	case claudeMCPHealthConnected:
		return ClaudeConnectorStatusConnected
	case claudeMCPHealthNeedsAuthentication:
		return ClaudeConnectorStatusNeedsAuthentication
	default:
		return ClaudeConnectorStatusInstalled
	}
}

func claudeStandaloneMCPHealth(
	target string,
	health []claudeMCPHealth,
) (ClaudeConnectorStatus, string) {
	for _, entry := range health {
		if entry.target != target {
			continue
		}
		status := claudeHealthConnectorStatus(entry.status)
		if status == ClaudeConnectorStatusNeedsAuthentication {
			return status, entry.target
		}
		return status, ""
	}
	return ClaudeConnectorStatusInstalled, ""
}

func claudePluginHealth(
	pluginName string,
	health map[string][]claudeMCPHealth,
) (ClaudeConnectorStatus, string) {
	entries := health[strings.ToLower(pluginName)]
	if len(entries) == 0 {
		return ClaudeConnectorStatusInstalled, ""
	}
	allConnected := true
	for _, entry := range entries {
		if entry.status == claudeMCPHealthNeedsAuthentication {
			return ClaudeConnectorStatusNeedsAuthentication, entry.target
		}
		if entry.status != claudeMCPHealthConnected {
			allConnected = false
		}
	}
	if allConnected {
		return ClaudeConnectorStatusConnected, ""
	}
	return ClaudeConnectorStatusInstalled, ""
}

func claudePluginConnectorCard(
	plugin claudePluginMetadata,
	status ClaudeConnectorStatus,
) ClaudeConnector {
	name := plugin.pluginName
	if officialName, ok := claudeOfficialConnectorPlugins[strings.ToLower(plugin.pluginName)]; ok {
		name = officialName
	}
	return ClaudeConnector{
		ID:           plugin.id,
		Name:         name,
		Provider:     "claude",
		Kind:         ClaudeConnectorKindPlugin,
		Status:       status,
		InstallCount: plugin.installCount,
	}
}

func claudeCloudConnectorCatalog() ([]ClaudeConnector, map[string]claudeDiscoveredConnector) {
	cloud := []ClaudeConnector{
		{ID: "claude-cloud:google-drive", Name: "Google Drive"},
		{ID: "claude-cloud:gmail", Name: "Gmail"},
		{ID: "claude-cloud:google-calendar", Name: "Google Calendar"},
		{ID: "claude-cloud:github", Name: "GitHub"},
		{ID: "claude-cloud:slack", Name: "Slack"},
		{ID: "claude-cloud:microsoft-365", Name: "Microsoft 365"},
	}
	discovered := make(map[string]claudeDiscoveredConnector, len(cloud))
	for index := range cloud {
		cloud[index].Provider = "claude"
		cloud[index].Kind = ClaudeConnectorKindCloud
		cloud[index].Status = ClaudeConnectorStatusManagedInProvider
		discovered[cloud[index].ID] = claudeDiscoveredConnector{card: cloud[index]}
	}
	return cloud, discovered
}

func claudeManagedConnectorResult(id string) ClaudeConnectorConnectResult {
	return ClaudeConnectorConnectResult{
		ID:          id,
		Status:      ClaudeConnectorStatusManagedInProvider,
		Instruction: claudeManagedInstruction,
	}
}

type osClaudeConnectorCommandRunner struct{}

func (osClaudeConnectorCommandRunner) Output(
	ctx context.Context,
	path string,
	args []string,
) (claudeConnectorCommandOutput, error) {
	if !filepath.IsAbs(path) || !validClaudeConnectorCommand(args) {
		return claudeConnectorCommandOutput{}, errClaudeConnectorCommand
	}
	stdout := &claudeConnectorCapture{limit: claudeCommandOutputLimit(args)}
	stderr := &claudeConnectorCapture{limit: claudeConnectorOutputLimit}
	command := exec.CommandContext(ctx, path, args...)
	command.WaitDelay = 2 * time.Second
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	output := claudeConnectorCommandOutput{
		stdout: slices.Clone(stdout.data),
		stderr: slices.Clone(stderr.data),
	}
	if !claudeCommandOutputFits(args, stdout.size, stderr.size) || err != nil {
		return output, errClaudeConnectorCommand
	}
	return output, nil
}

// claudeConnectorCapture keeps up to limit bytes and counts every byte
// written in size.
type claudeConnectorCapture struct {
	data  []byte
	limit int
	size  int
}

func (c *claudeConnectorCapture) Write(data []byte) (int, error) {
	c.size += len(data)
	remaining := c.limit - len(c.data)
	if remaining > 0 {
		written := min(remaining, len(data))
		c.data = append(c.data, data[:written]...)
	}
	return len(data), nil
}
