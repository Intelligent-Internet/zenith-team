package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sort"
	"sync"
	"time"
)

const (
	claudePluginDiscoveryTimeout = 30 * time.Second
	claudePluginLifecycleTimeout = 2 * time.Minute
	claudePluginCatalogLimit     = 5000
)

// ClaudePluginStatus is Boardly's sanitized view of Claude Code plugin state.
type ClaudePluginStatus string

const (
	ClaudePluginStatusAvailable ClaudePluginStatus = "available"
	ClaudePluginStatusInstalled ClaudePluginStatus = "installed"
	ClaudePluginStatusDisabled  ClaudePluginStatus = "disabled"
	ClaudePluginStatusError     ClaudePluginStatus = "error"
)

// ClaudePluginAction is the user-scoped native lifecycle action Boardly may request.
type ClaudePluginAction string

const (
	ClaudePluginActionInstall   ClaudePluginAction = "install"
	ClaudePluginActionUninstall ClaudePluginAction = "uninstall"
	ClaudePluginActionNone      ClaudePluginAction = "none"
)

// ClaudePlugin contains no install path, marketplace source path, plugin
// configuration, command, environment variable, or credential.
type ClaudePlugin struct {
	NativeID     string             `json:"nativeId"`
	Name         string             `json:"name"`
	Description  string             `json:"description,omitempty"`
	Status       ClaudePluginStatus `json:"status"`
	Installed    bool               `json:"installed"`
	Enabled      bool               `json:"enabled"`
	Action       ClaudePluginAction `json:"action"`
	CatalogRank  int                `json:"catalogRank"`
	InstallCount *int64             `json:"installCount,omitempty"`
}

// ClaudePluginDetail is what the last complete discovery knows about one
// plugin. Claude's catalog exposes no apps, skills or storefront text, so the
// detail carries only the plugin's MCP server names.
type ClaudePluginDetail struct {
	NativeID   string
	Name       string
	MCPServers []string
}

// ClaudePluginSnapshot is a complete configured-marketplace catalog.
type ClaudePluginSnapshot struct {
	SchemaVersion int            `json:"schemaVersion"`
	Plugins       []ClaudePlugin `json:"plugins"`
}

var (
	// ErrClaudePluginsUnavailable means Claude could not provide a complete JSON catalog.
	ErrClaudePluginsUnavailable = errors.New("Claude plugins are unavailable")
	// ErrClaudePluginNotFound means the exact ID was absent from the last complete discovery.
	ErrClaudePluginNotFound = errors.New("Claude plugin was not found")
	// ErrClaudePluginNotInstallable means the discovered plugin is not installable by Boardly.
	ErrClaudePluginNotInstallable = errors.New("Claude plugin cannot be installed")
	// ErrClaudePluginNotUninstallable means no user-scoped installation was discovered.
	ErrClaudePluginNotUninstallable = errors.New("Claude plugin cannot be uninstalled")
)

type claudePluginCatalog struct {
	plugins    []ClaudePlugin
	actions    map[string]ClaudePluginAction
	mcpServers map[string][]string
}

type claudePluginTransport interface {
	Read(context.Context, string) (claudePluginCatalog, error)
	Install(context.Context, string, string) error
	Uninstall(context.Context, string, string) error
}

type osClaudePluginTransport struct {
	runner claudeConnectorCommandRunner
}

func (t osClaudePluginTransport) Read(
	ctx context.Context,
	binaryPath string,
) (claudePluginCatalog, error) {
	readCtx, cancel := context.WithTimeout(ctx, claudePluginDiscoveryTimeout)
	defer cancel()
	output, err := t.output(readCtx, binaryPath, []string{
		"plugin", "list", "--available", "--json",
	})
	if err != nil {
		return claudePluginCatalog{}, ErrClaudePluginsUnavailable
	}
	catalog, err := parseClaudePluginCatalog(output.stdout)
	if err != nil {
		return claudePluginCatalog{}, ErrClaudePluginsUnavailable
	}
	return catalog, nil
}

func (t osClaudePluginTransport) Install(
	ctx context.Context,
	binaryPath string,
	nativeID string,
) error {
	installCtx, cancel := context.WithTimeout(ctx, claudePluginLifecycleTimeout)
	defer cancel()
	_, err := t.output(installCtx, binaryPath, []string{
		"plugin", "install", nativeID, "--scope", "user",
	})
	if err != nil {
		return ErrClaudePluginsUnavailable
	}
	return nil
}

func (t osClaudePluginTransport) Uninstall(
	ctx context.Context,
	binaryPath string,
	nativeID string,
) error {
	uninstallCtx, cancel := context.WithTimeout(ctx, claudePluginLifecycleTimeout)
	defer cancel()
	_, err := t.output(uninstallCtx, binaryPath, []string{
		"plugin", "uninstall", nativeID, "--scope", "user", "-y",
	})
	if err != nil {
		return ErrClaudePluginsUnavailable
	}
	return nil
}

func (t osClaudePluginTransport) output(
	ctx context.Context,
	binaryPath string,
	args []string,
) (claudeConnectorCommandOutput, error) {
	if t.runner == nil || !validClaudePluginCommand(args) {
		return claudeConnectorCommandOutput{}, ErrClaudePluginsUnavailable
	}
	output, err := t.runner.Output(ctx, binaryPath, slices.Clone(args))
	if err != nil || !claudeCommandOutputFits(args, len(output.stdout), len(output.stderr)) {
		return claudeConnectorCommandOutput{}, ErrClaudePluginsUnavailable
	}
	return output, nil
}

func validClaudePluginCommand(args []string) bool {
	switch {
	case slices.Equal(args, []string{"plugin", "list", "--available", "--json"}):
		return true
	case len(args) == 5 && args[0] == "plugin" && args[1] == "install" &&
		args[3] == "--scope" && args[4] == "user":
		_, _, ok := parseClaudePluginID(args[2])
		return ok
	case len(args) == 6 && args[0] == "plugin" && args[1] == "uninstall" &&
		args[3] == "--scope" && args[4] == "user" && args[5] == "-y":
		_, _, ok := parseClaudePluginID(args[2])
		return ok
	default:
		return false
	}
}

// ClaudePluginBackend owns plugin discovery and user-scoped lifecycle state for
// one Claude executable. It is separate from MCP/connector execution authority.
type ClaudePluginBackend struct {
	mu          sync.RWMutex
	operationMu sync.Mutex
	path        string
	transport   claudePluginTransport
	cached      *claudePluginCatalog
	// cacheGeneration counts every change to cached, so a read made outside
	// operationMu (catalog) caches its result only if nothing replaced or
	// invalidated cached while it ran.
	cacheGeneration uint64
	changeHandler   func()
	closed          bool
}

func newClaudePluginBackend(path string, transport claudePluginTransport) *ClaudePluginBackend {
	return &ClaudePluginBackend{path: path, transport: transport}
}

// NewOSClaudePluginBackend creates a plugin backend for one discovered Claude executable.
func NewOSClaudePluginBackend(path string) *ClaudePluginBackend {
	return newClaudePluginBackend(path, osClaudePluginTransport{
		runner: osClaudeConnectorCommandRunner{},
	})
}

// Discover returns the full catalog reported by configured Claude marketplaces.
// Claude's JSON list command has no documented force-cache flag, so force is accepted
// for provider parity but intentionally does not mutate marketplace configuration.
func (b *ClaudePluginBackend) Discover(
	ctx context.Context,
	force bool,
) (ClaudePluginSnapshot, error) {
	_ = force
	if b == nil || b.path == "" || b.transport == nil {
		return ClaudePluginSnapshot{}, ErrClaudePluginsUnavailable
	}
	b.operationMu.Lock()
	defer b.operationMu.Unlock()
	if b.isClosed() {
		return ClaudePluginSnapshot{}, ErrClaudePluginsUnavailable
	}
	catalog, err := b.transport.Read(ctx, b.path)
	if err != nil {
		return ClaudePluginSnapshot{}, mapClaudePluginError(err)
	}
	b.mu.Lock()
	cached := cloneClaudePluginCatalog(catalog)
	b.cached = &cached
	b.cacheGeneration++
	b.mu.Unlock()
	return snapshotFromClaudePluginCatalog(catalog), nil
}

// Install runs Claude's documented user-scope install command for one exact
// available ID from the last complete discovery.
func (b *ClaudePluginBackend) Install(ctx context.Context, nativeID string) error {
	if b == nil {
		return ErrClaudePluginNotFound
	}
	if _, _, ok := parseClaudePluginID(nativeID); !ok {
		return ErrClaudePluginNotFound
	}
	b.operationMu.Lock()
	err := b.install(ctx, nativeID)
	b.operationMu.Unlock()
	if err != nil {
		return err
	}
	b.notifyChange()
	return nil
}

func (b *ClaudePluginBackend) install(ctx context.Context, nativeID string) error {
	action, err := b.action(ctx, nativeID)
	if err != nil {
		return err
	}
	if action != ClaudePluginActionInstall {
		return ErrClaudePluginNotInstallable
	}
	if err := b.transport.Install(ctx, b.path, nativeID); err != nil {
		return mapClaudePluginError(err)
	}
	b.invalidate()
	return nil
}

// Uninstall removes only an exact user-scoped plugin discovered as installed.
// -y keeps the provider-native operation non-interactive.
func (b *ClaudePluginBackend) Uninstall(ctx context.Context, nativeID string) error {
	if b == nil {
		return ErrClaudePluginNotFound
	}
	if _, _, ok := parseClaudePluginID(nativeID); !ok {
		return ErrClaudePluginNotFound
	}
	b.operationMu.Lock()
	err := b.uninstall(ctx, nativeID)
	b.operationMu.Unlock()
	if err != nil {
		return err
	}
	b.notifyChange()
	return nil
}

func (b *ClaudePluginBackend) uninstall(ctx context.Context, nativeID string) error {
	action, err := b.action(ctx, nativeID)
	if err != nil {
		return err
	}
	if action != ClaudePluginActionUninstall {
		return ErrClaudePluginNotUninstallable
	}
	if err := b.transport.Uninstall(ctx, b.path, nativeID); err != nil {
		return mapClaudePluginError(err)
	}
	b.invalidate()
	return nil
}

// ReadDetail answers from the last complete discovery, reading the catalog
// only when none is cached. It does not wait for a lifecycle operation.
func (b *ClaudePluginBackend) ReadDetail(
	ctx context.Context,
	nativeID string,
) (ClaudePluginDetail, error) {
	if b == nil || b.path == "" || b.transport == nil {
		return ClaudePluginDetail{}, ErrClaudePluginsUnavailable
	}
	if _, _, ok := parseClaudePluginID(nativeID); !ok {
		return ClaudePluginDetail{}, ErrClaudePluginNotFound
	}
	catalog, err := b.catalog(ctx)
	if err != nil {
		return ClaudePluginDetail{}, err
	}
	for _, plugin := range catalog.plugins {
		if plugin.NativeID == nativeID {
			return ClaudePluginDetail{
				NativeID:   nativeID,
				Name:       plugin.Name,
				MCPServers: slices.Clone(catalog.mcpServers[nativeID]),
			}, nil
		}
	}
	return ClaudePluginDetail{}, ErrClaudePluginNotFound
}

func (b *ClaudePluginBackend) action(
	ctx context.Context,
	nativeID string,
) (ClaudePluginAction, error) {
	catalog, err := b.catalog(ctx)
	if err != nil {
		return ClaudePluginActionNone, err
	}
	action, exists := catalog.actions[nativeID]
	if !exists {
		return ClaudePluginActionNone, ErrClaudePluginNotFound
	}
	return action, nil
}

// catalog returns the cached catalog, or reads and caches one. The cached
// value is replaced whole and never mutated, so a shallow copy is safe to read.
func (b *ClaudePluginBackend) catalog(ctx context.Context) (claudePluginCatalog, error) {
	if b.isClosed() {
		return claudePluginCatalog{}, ErrClaudePluginsUnavailable
	}
	b.mu.RLock()
	if b.cached != nil {
		catalog := *b.cached
		b.mu.RUnlock()
		return catalog, nil
	}
	generation := b.cacheGeneration
	b.mu.RUnlock()
	catalog, err := b.transport.Read(ctx, b.path)
	if err != nil {
		return claudePluginCatalog{}, mapClaudePluginError(err)
	}
	cached := cloneClaudePluginCatalog(catalog)
	b.mu.Lock()
	// ReadDetail reads without operationMu: a read that a Discover or a
	// lifecycle change overtook answers from its own catalog but never
	// restores it over theirs.
	if b.cacheGeneration == generation {
		b.cached = &cached
		b.cacheGeneration++
	}
	b.mu.Unlock()
	return cached, nil
}

func (b *ClaudePluginBackend) invalidate() {
	b.mu.Lock()
	b.cached = nil
	b.cacheGeneration++
	b.mu.Unlock()
}

func (b *ClaudePluginBackend) notifyChange() {
	b.mu.RLock()
	handler := b.changeHandler
	closed := b.closed
	b.mu.RUnlock()
	if !closed && handler != nil {
		handler()
	}
}

// SetChangeHandler replaces the callback invoked after a native lifecycle mutation.
func (b *ClaudePluginBackend) SetChangeHandler(handler func()) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if !b.closed {
		b.changeHandler = handler
	}
	b.mu.Unlock()
}

// Close prevents future discovery or lifecycle work.
func (b *ClaudePluginBackend) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.closed = true
	b.cached = nil
	b.cacheGeneration++
	b.changeHandler = nil
	b.mu.Unlock()
}

func (b *ClaudePluginBackend) isClosed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.closed
}

type claudeInstalledPluginJSON struct {
	ID         string          `json:"id"`
	Scope      string          `json:"scope"`
	Enabled    bool            `json:"enabled"`
	MCPServers json.RawMessage `json:"mcpServers"`
}

type claudeAvailablePluginJSON struct {
	PluginID        string          `json:"pluginId"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	MarketplaceName string          `json:"marketplaceName"`
	InstallCount    json.RawMessage `json:"installCount"`
	MCPServers      json.RawMessage `json:"mcpServers"`
}

type claudePluginCatalogJSON struct {
	Installed *[]claudeInstalledPluginJSON `json:"installed"`
	Available *[]claudeAvailablePluginJSON `json:"available"`
}

func parseClaudePluginCatalog(body []byte) (claudePluginCatalog, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var response claudePluginCatalogJSON
	if decoder.Decode(&response) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		response.Installed == nil || response.Available == nil {
		return claudePluginCatalog{}, errClaudeConnectorOutput
	}
	if len(*response.Available)+len(*response.Installed) > claudePluginCatalogLimit {
		return claudePluginCatalog{}, errClaudeConnectorOutput
	}
	type installedState struct {
		user      bool
		protected bool
		enabled   bool
	}
	installed := make(map[string]installedState, len(*response.Installed))
	installedOrder := make([]string, 0, len(*response.Installed))
	// MCP server names are best-effort presentation: a declaration Boardly
	// cannot validate hides the names, never the plugin.
	mcpServers := make(map[string][]string)
	recordMCPServers := func(nativeID string, raw json.RawMessage) {
		if _, known := mcpServers[nativeID]; known {
			return
		}
		if names, ok := claudeMCPServerMetadata(raw); ok {
			mcpServers[nativeID] = names
		}
	}
	for _, rawPlugin := range *response.Installed {
		if _, _, ok := parseClaudePluginID(rawPlugin.ID); !ok {
			return claudePluginCatalog{}, errClaudeConnectorOutput
		}
		recordMCPServers(rawPlugin.ID, rawPlugin.MCPServers)
		state, seen := installed[rawPlugin.ID]
		if !seen {
			installedOrder = append(installedOrder, rawPlugin.ID)
		}
		switch rawPlugin.Scope {
		case "user":
			state.user = true
		case "managed":
			state.protected = true
		case "project", "local":
			state.protected = true
		default:
			return claudePluginCatalog{}, errClaudeConnectorOutput
		}
		state.enabled = state.enabled || rawPlugin.Enabled
		installed[rawPlugin.ID] = state
	}
	type availableMetadata struct {
		name         string
		description  string
		installCount *int64
	}
	available := make(map[string]availableMetadata, len(*response.Available))
	availableOrder := make([]string, 0, len(*response.Available))
	for _, rawPlugin := range *response.Available {
		pluginName, marketplace, ok := parseClaudePluginID(rawPlugin.PluginID)
		if !ok || rawPlugin.Name != pluginName || rawPlugin.MarketplaceName != marketplace {
			return claudePluginCatalog{}, errClaudeConnectorOutput
		}
		if _, duplicate := available[rawPlugin.PluginID]; duplicate {
			return claudePluginCatalog{}, errClaudeConnectorOutput
		}
		recordMCPServers(rawPlugin.PluginID, rawPlugin.MCPServers)
		name := sanitizeCodexConnectorText(rawPlugin.Name, codexConnectorMaximumName)
		if name == "" {
			return claudePluginCatalog{}, errClaudeConnectorOutput
		}
		available[rawPlugin.PluginID] = availableMetadata{
			name: name,
			description: sanitizeCodexConnectorText(
				rawPlugin.Description,
				codexConnectorMaximumDescription,
			),
			installCount: parseClaudePluginInstallCount(rawPlugin.InstallCount),
		}
		availableOrder = append(availableOrder, rawPlugin.PluginID)
	}
	sort.SliceStable(availableOrder, func(left, right int) bool {
		leftCount := available[availableOrder[left]].installCount
		rightCount := available[availableOrder[right]].installCount
		switch {
		case leftCount == nil:
			return false
		case rightCount == nil:
			return true
		default:
			return *leftCount > *rightCount
		}
	})
	catalog := claudePluginCatalog{
		plugins:    make([]ClaudePlugin, 0, len(available)+len(installed)),
		actions:    make(map[string]ClaudePluginAction),
		mcpServers: mcpServers,
	}
	for _, nativeID := range installedOrder {
		pluginName, _, _ := parseClaudePluginID(nativeID)
		name := sanitizeCodexConnectorText(pluginName, codexConnectorMaximumName)
		if name == "" {
			return claudePluginCatalog{}, errClaudeConnectorOutput
		}
		description := ""
		var installCount *int64
		if metadata, exists := available[nativeID]; exists {
			name = metadata.name
			description = metadata.description
			installCount = metadata.installCount
		}
		state := installed[nativeID]
		status := ClaudePluginStatusDisabled
		if state.enabled {
			status = ClaudePluginStatusInstalled
		}
		action := ClaudePluginActionNone
		if state.user && !state.protected {
			action = ClaudePluginActionUninstall
		}
		plugin := ClaudePlugin{
			NativeID:     nativeID,
			Name:         name,
			Description:  description,
			Status:       status,
			Installed:    true,
			Enabled:      state.enabled,
			Action:       action,
			CatalogRank:  len(catalog.plugins),
			InstallCount: installCount,
		}
		catalog.plugins = append(catalog.plugins, plugin)
		catalog.actions[plugin.NativeID] = plugin.Action
	}
	for _, nativeID := range availableOrder {
		if _, isInstalled := installed[nativeID]; isInstalled {
			continue
		}
		metadata := available[nativeID]
		plugin := ClaudePlugin{
			NativeID:     nativeID,
			Name:         metadata.name,
			Description:  metadata.description,
			Status:       ClaudePluginStatusAvailable,
			Action:       ClaudePluginActionInstall,
			CatalogRank:  len(catalog.plugins),
			InstallCount: metadata.installCount,
		}
		catalog.plugins = append(catalog.plugins, plugin)
		catalog.actions[plugin.NativeID] = plugin.Action
	}
	return catalog, nil
}

func cloneClaudePluginCatalog(source claudePluginCatalog) claudePluginCatalog {
	cloned := claudePluginCatalog{
		plugins:    cloneClaudePlugins(source.plugins),
		actions:    make(map[string]ClaudePluginAction, len(source.actions)),
		mcpServers: make(map[string][]string, len(source.mcpServers)),
	}
	for nativeID, action := range source.actions {
		cloned.actions[nativeID] = action
	}
	for nativeID, servers := range source.mcpServers {
		cloned.mcpServers[nativeID] = slices.Clone(servers)
	}
	return cloned
}

func snapshotFromClaudePluginCatalog(catalog claudePluginCatalog) ClaudePluginSnapshot {
	return ClaudePluginSnapshot{
		SchemaVersion: 1,
		Plugins:       cloneClaudePlugins(catalog.plugins),
	}
}

func cloneClaudePlugins(source []ClaudePlugin) []ClaudePlugin {
	cloned := append([]ClaudePlugin(nil), source...)
	for index := range cloned {
		if cloned[index].InstallCount == nil {
			continue
		}
		count := *cloned[index].InstallCount
		cloned[index].InstallCount = &count
	}
	return cloned
}

func mapClaudePluginError(err error) error {
	switch {
	case errors.Is(err, ErrClaudePluginNotFound):
		return ErrClaudePluginNotFound
	case errors.Is(err, ErrClaudePluginNotInstallable):
		return ErrClaudePluginNotInstallable
	case errors.Is(err, ErrClaudePluginNotUninstallable):
		return ErrClaudePluginNotUninstallable
	default:
		return ErrClaudePluginsUnavailable
	}
}
