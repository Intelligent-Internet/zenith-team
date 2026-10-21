package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	codexPluginDiscoveryTimeout       = 90 * time.Second
	codexPluginLifecycleTimeout       = 2 * time.Minute
	codexPluginDetailBudget           = 20 * time.Second
	codexPluginMessageLimit           = 32 << 20 // plugin/list is one frame, ~2.3 KB per plugin
	codexPluginMaximumItems           = 10000
	codexPluginMaximumComponents      = 2048
	codexPluginMaximumFeaturePages    = 16
	codexPluginFeaturePageSize        = 200
	codexPluginMaximumMarketplace     = 160
	codexPluginMaximumMarketplacePath = 4096
	codexPluginAuthFallbackURL        = "https://chatgpt.com/apps"
	codexPluginMaximumIconBytes       = 4 << 20
)

// CodexPluginStatus is Zenith Team's sanitized view of the native Codex plugin state.
type CodexPluginStatus string

const (
	CodexPluginStatusAvailable CodexPluginStatus = "available"
	CodexPluginStatusInstalled CodexPluginStatus = "installed"
	CodexPluginStatusDisabled  CodexPluginStatus = "disabled"
	CodexPluginStatusError     CodexPluginStatus = "error"
)

// CodexPluginAction is the provider-native lifecycle action Zenith Team may request.
type CodexPluginAction string

const (
	CodexPluginActionInstall   CodexPluginAction = "install"
	CodexPluginActionUninstall CodexPluginAction = "uninstall"
	CodexPluginActionNone      CodexPluginAction = "none"
)

// CodexPluginComponentCounts is deliberately aggregate-only. It does not expose
// plugin paths, MCP configuration, commands, environment variables, or credentials.
type CodexPluginComponentCounts struct {
	Apps       int `json:"apps"`
	MCPServers int `json:"mcpServers"`
	Skills     int `json:"skills"`
}

// CodexPlugin contains only bounded catalog and lifecycle metadata safe to send
// to Zenith Team Cloud. NativeID remains Codex's opaque plugin identity.
type CodexPlugin struct {
	NativeID            string                     `json:"nativeId"`
	Name                string                     `json:"name"`
	Description         string                     `json:"description,omitempty"`
	Category            string                     `json:"category,omitempty"`
	IconURL             string                     `json:"iconUrl,omitempty"`
	ShareURL            string                     `json:"shareUrl,omitempty"`
	Status              CodexPluginStatus          `json:"status"`
	Installed           bool                       `json:"installed"`
	Enabled             bool                       `json:"enabled"`
	Featured            bool                       `json:"featured"`
	Action              CodexPluginAction          `json:"action"`
	CatalogRank         int                        `json:"catalogRank"`
	ComponentCounts     CodexPluginComponentCounts `json:"componentCounts"`
	ConnectorIdentities []PluginConnectorIdentity  `json:"connectorIdentities,omitempty"`
	CodexPluginPresentation
}

// CodexPluginPresentation is the sanitized storefront text of a plugin's
// interface: every value is bounded, single-line except LongDescription, and
// every URL is https without credentials or fragment.
type CodexPluginPresentation struct {
	LongDescription   string   `json:"longDescription,omitempty"`
	DefaultPrompts    []string `json:"defaultPrompts,omitempty"`
	BrandColor        string   `json:"brandColor,omitempty"`
	DeveloperName     string   `json:"developerName,omitempty"`
	WebsiteURL        string   `json:"websiteUrl,omitempty"`
	PrivacyPolicyURL  string   `json:"privacyPolicyUrl,omitempty"`
	TermsOfServiceURL string   `json:"termsOfServiceUrl,omitempty"`
}

// CodexPluginDetail is one live plugin/read, sanitized for the desktop plugin
// page. It carries names and descriptions of components, never their paths or
// configuration.
type CodexPluginDetail struct {
	NativeID string
	Name     string
	ShareURL string
	// AuthPolicy is when the plugin's apps authenticate: at install
	// ("on_install") or on first use ("on_use"); empty when Codex gave neither.
	AuthPolicy string
	// InstallationInterstitial is whether installing must first show the
	// connect dialog for the plugin's app rather than open the browser at once;
	// Codex asks for it unless the plugin says false.
	InstallationInterstitial bool
	CodexPluginPresentation
	Apps       []CodexPluginApp
	Skills     []CodexPluginSkill
	MCPServers []string
}

// CodexPluginApp is one app a plugin bundles, as plugin/read summarizes it.
type CodexPluginApp struct {
	ID          string
	Name        string
	Description string
	Category    string
}

// CodexPluginSkill is one skill a plugin bundles, as plugin/read summarizes it.
type CodexPluginSkill struct {
	Name        string
	DisplayName string
	Description string
}

// CodexPluginSnapshot is a complete native plugin generation in Codex catalog order.
type CodexPluginSnapshot struct {
	SchemaVersion int           `json:"schemaVersion"`
	Plugins       []CodexPlugin `json:"plugins"`
}

// CodexPluginInstallResult contains only the bounded lifecycle result and an
// optional credential-free ChatGPT continuation URL. Native MCP OAuth is
// launched by Codex and deliberately has no URL in this result.
type CodexPluginInstallResult struct {
	NativeID            string `json:"nativeId"`
	AppsNeedingAuth     int    `json:"appsNeedingAuth"`
	NeedsAuthentication bool   `json:"needsAuthentication"`
	ContinueURL         string `json:"continueUrl,omitempty"`
	// AuthPolicy is plugin/install's own ("on_install" or "on_use"), and
	// AuthApps names the apps it reported still needing authentication.
	AuthPolicy string           `json:"authPolicy,omitempty"`
	AuthApps   []CodexPluginApp `json:"-"`

	oauthFlow *codexPluginOAuthFlow
}

var (
	// ErrCodexPluginsUnavailable means the experimental native surface could not
	// provide a complete, safe plugin catalog.
	ErrCodexPluginsUnavailable = errors.New("Codex plugins are unavailable")
	// ErrCodexPluginNotFound means the exact ID was absent from the last complete discovery.
	ErrCodexPluginNotFound = errors.New("Codex plugin was not found")
	// ErrCodexPluginNotInstallable means discovery did not authorize install for the ID.
	ErrCodexPluginNotInstallable = errors.New("Codex plugin cannot be installed")
	// ErrCodexPluginNotUninstallable means discovery did not authorize uninstall for the ID.
	ErrCodexPluginNotUninstallable = errors.New("Codex plugin cannot be uninstalled")
	// ErrCodexPluginIconUnavailable means no safe local icon was discovered for the plugin.
	ErrCodexPluginIconUnavailable = errors.New("Codex plugin icon is unavailable")
)

// CodexPluginIcon is a daemon-local raster asset. Its source path is never
// included in this value or in the cloud discovery projection.
type CodexPluginIcon struct {
	ContentType string
	Bytes       []byte
}

type codexPluginLocator struct {
	nativeID        string
	pluginName      string
	marketplaceName string
	marketplacePath string
	remotePluginID  string
	action          CodexPluginAction
}

func (l codexPluginLocator) requestParams() map[string]any {
	pluginName := l.pluginName
	marketplacePath := any(nil)
	remoteMarketplaceName := any(nil)
	if l.remotePluginID != "" {
		pluginName = l.remotePluginID
		remoteMarketplaceName = l.marketplaceName
	} else {
		marketplacePath = l.marketplacePath
	}
	return map[string]any{
		"marketplacePath":       marketplacePath,
		"remoteMarketplaceName": remoteMarketplaceName,
		"pluginName":            pluginName,
	}
}

func (l codexPluginLocator) uninstallID() string {
	if l.remotePluginID != "" {
		return l.remotePluginID
	}
	return l.nativeID
}

type codexPluginCatalog struct {
	plugins  []CodexPlugin
	locators map[string]codexPluginLocator
	icons    map[string][]codexPluginIconAsset
}

// codexPluginCatalogBuilder retains only entries with an unambiguous
// lifecycle locator.
type codexPluginCatalogBuilder struct {
	result       codexPluginCatalog
	featured     map[string]struct{}
	ambiguousIDs map[string]struct{}
	itemCount    int
	// listings caches each marketplace manifest by path, so one catalog read
	// parses each at most once; a nil entry is a manifest that gave none.
	listings map[string]map[string]codexMarketplaceListing
}

type codexPluginIconAsset struct {
	rootDirectory string
	relativePath  string
}

type codexPluginTransport interface {
	Read(context.Context, string, bool) (codexPluginCatalog, error)
	ReadDetail(context.Context, string, codexPluginLocator) (CodexPluginDetail, error)
	Install(context.Context, string, codexPluginLocator) (CodexPluginInstallResult, error)
	Uninstall(context.Context, string, codexPluginLocator) error
}

type osCodexPluginTransport struct{}

func (osCodexPluginTransport) Read(
	ctx context.Context,
	binaryPath string,
	force bool,
) (codexPluginCatalog, error) {
	readCtx, cancel := context.WithTimeout(ctx, codexPluginDiscoveryTimeout)
	defer cancel()
	session, err := startCodexRPCSessionWithLimit(readCtx, binaryPath, codexPluginMessageLimit)
	if err != nil {
		logCodexPluginDiscoveryFailure(ctx, err)
		return codexPluginCatalog{}, ErrCodexPluginsUnavailable
	}
	defer session.close()
	catalog, err := readCodexPluginCatalog(readCtx, session, force)
	if err != nil {
		logCodexPluginDiscoveryFailure(ctx, err)
		return codexPluginCatalog{}, ErrCodexPluginsUnavailable
	}
	return catalog, nil
}

// logCodexPluginDiscoveryFailure records why discovery failed, which the
// public ErrCodexPluginsUnavailable deliberately hides, so a catalog that
// stopped updating (an item or frame limit, a timeout) is diagnosable from
// the daemon log. The cause carries Zenith Team's own messages and Go's I/O
// errors, never Codex's response text. A cancelled daemon context is
// shutdown, not a failure.
func logCodexPluginDiscoveryFailure(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	slog.Warn("Codex plugin discovery failed", "error", err)
}

func (osCodexPluginTransport) ReadDetail(
	ctx context.Context,
	binaryPath string,
	locator codexPluginLocator,
) (CodexPluginDetail, error) {
	readCtx, cancel := context.WithTimeout(ctx, codexPluginDetailBudget)
	defer cancel()
	session, err := startCodexRPCSessionWithLimit(readCtx, binaryPath, codexPluginMessageLimit)
	if err != nil {
		return CodexPluginDetail{}, ErrCodexPluginsUnavailable
	}
	defer session.close()
	raw, err := session.request(readCtx, "plugin/read", locator.requestParams())
	if err != nil {
		return CodexPluginDetail{}, ErrCodexPluginsUnavailable
	}
	var response codexPluginReadResponse
	if json.Unmarshal(raw, &response) != nil {
		return CodexPluginDetail{}, ErrCodexPluginsUnavailable
	}
	detail, ok := codexPluginDetailFromRead(response, locator.nativeID)
	if !ok {
		return CodexPluginDetail{}, ErrCodexPluginsUnavailable
	}
	return detail, nil
}

func (osCodexPluginTransport) Install(
	ctx context.Context,
	binaryPath string,
	locator codexPluginLocator,
) (CodexPluginInstallResult, error) {
	if ctx.Err() != nil {
		return CodexPluginInstallResult{}, ErrCodexPluginsUnavailable
	}
	sessionCtx, sessionCancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		codexConnectorOAuthTimeout,
	)
	closeSession := true
	defer func() {
		if closeSession {
			sessionCancel()
		}
	}()

	installCtx, cancel := context.WithTimeout(ctx, codexPluginLifecycleTimeout)
	defer cancel()
	session, err := startCodexRPCSessionWithLimit(
		sessionCtx,
		binaryPath,
		codexPluginMessageLimit,
	)
	if err != nil {
		return CodexPluginInstallResult{}, ErrCodexPluginsUnavailable
	}
	defer func() {
		if closeSession {
			session.close()
		}
	}()
	params := locator.requestParams()
	mcpServers, err := readCodexPluginInstallMCPServers(installCtx, session, locator)
	if err != nil {
		return CodexPluginInstallResult{}, ErrCodexPluginsUnavailable
	}
	raw, err := session.request(installCtx, "plugin/install", params)
	if err != nil {
		return CodexPluginInstallResult{}, ErrCodexPluginsUnavailable
	}
	var response struct {
		AppsNeedingAuth *[]codexPluginAppSummary `json:"appsNeedingAuth"`
		AuthPolicy      string                   `json:"authPolicy"`
	}
	if json.Unmarshal(raw, &response) != nil ||
		response.AppsNeedingAuth == nil ||
		(response.AuthPolicy != "ON_INSTALL" && response.AuthPolicy != "ON_USE") ||
		len(*response.AppsNeedingAuth) > codexPluginMaximumComponents {
		return CodexPluginInstallResult{}, ErrCodexPluginsUnavailable
	}
	continueURL, ok := codexPluginAuthenticationURL(*response.AppsNeedingAuth)
	if !ok {
		return CodexPluginInstallResult{}, ErrCodexPluginsUnavailable
	}
	result := CodexPluginInstallResult{
		NativeID:            locator.nativeID,
		AppsNeedingAuth:     len(*response.AppsNeedingAuth),
		NeedsAuthentication: len(*response.AppsNeedingAuth) > 0,
		ContinueURL:         continueURL,
		AuthPolicy:          codexPluginAuthPolicy(response.AuthPolicy),
		AuthApps:            make([]CodexPluginApp, 0, len(*response.AppsNeedingAuth)),
	}
	for _, app := range *response.AppsNeedingAuth {
		// codexPluginAuthenticationURL already refused an invalid ID or name.
		result.AuthApps = append(result.AuthApps, CodexPluginApp{
			ID: app.ID, Name: sanitizeCodexConnectorText(app.Name, codexConnectorMaximumName),
		})
	}
	if len(mcpServers) == 0 {
		return result, nil
	}
	mcpServersNeedingAuth, observed := readCodexPluginMCPServersNeedingAuth(
		installCtx,
		session,
		locator.nativeID,
		mcpServers,
	)
	pendingServers := mcpServers
	if observed {
		pendingServers = mcpServersNeedingAuth
		if len(pendingServers) == 0 {
			return result, nil
		}
		result.NeedsAuthentication = true
	}
	// plugin/install itself launches provider-native OAuth in the browser. Keep
	// its app-server session alive so Codex's callback server and completion
	// notifications are not cancelled. An incomplete status snapshot falls back
	// to all declarations for session ownership, but does not claim that user
	// authentication is required without provider evidence.
	flow := newCodexPluginOAuthFlow(sessionCtx, sessionCancel, session, pendingServers)
	closeSession = false
	result.oauthFlow = flow
	return result, nil
}

func readCodexPluginInstallMCPServers(
	ctx context.Context,
	session codexConnectorRPC,
	locator codexPluginLocator,
) ([]string, error) {
	raw, err := session.request(ctx, "plugin/read", locator.requestParams())
	if err != nil {
		return nil, err
	}
	var response codexPluginReadResponse
	if json.Unmarshal(raw, &response) != nil ||
		response.Plugin.Summary.ID != locator.nativeID {
		return nil, errors.New("decode Codex plugin details")
	}
	if _, ok := boundedCodexPluginComponentCounts(response); !ok {
		return nil, errors.New("Codex plugin component count exceeds limit")
	}
	seen := make(map[string]struct{}, len(response.Plugin.MCPServers))
	servers := make([]string, 0, len(response.Plugin.MCPServers))
	for _, server := range response.Plugin.MCPServers {
		if !validCodexConnectorNativeID(server) {
			return nil, errors.New("Codex plugin MCP server ID is invalid")
		}
		if _, duplicate := seen[server]; duplicate {
			return nil, errors.New("Codex plugin MCP server ID is duplicated")
		}
		seen[server] = struct{}{}
		servers = append(servers, server)
	}
	sort.Strings(servers)
	return servers, nil
}

func readCodexPluginMCPServersNeedingAuth(
	ctx context.Context,
	session codexConnectorRPC,
	pluginID string,
	mcpServers []string,
) ([]string, bool) {
	wanted := make(map[string]struct{}, len(mcpServers))
	for _, server := range mcpServers {
		wanted[server] = struct{}{}
	}
	statuses := make(map[string]string, len(mcpServers))
	cursor := ""
	seenCursors := make(map[string]struct{})
	for range codexConnectorMaximumPages {
		raw, err := session.request(ctx, "mcpServerStatus/list", map[string]any{
			"cursor": cursorValue(cursor), "limit": codexConnectorMCPPageSize,
			"detail": "toolsAndAuthOnly",
		})
		if err != nil {
			return nil, false
		}
		var page codexMCPPage
		if json.Unmarshal(raw, &page) != nil {
			return nil, false
		}
		for _, server := range page.Data {
			if server.PluginID != pluginID {
				continue
			}
			if _, expected := wanted[server.Name]; !expected {
				continue
			}
			if _, duplicate := statuses[server.Name]; duplicate {
				return nil, false
			}
			switch server.AuthStatus {
			case "unknown", "unsupported", "notLoggedIn", "bearerToken", "oAuth":
				statuses[server.Name] = server.AuthStatus
			default:
				return nil, false
			}
		}
		next, err := nextCodexConnectorCursor(page.NextCursor, seenCursors)
		if err != nil {
			return nil, false
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(statuses) != len(wanted) {
		return nil, false
	}
	serversNeedingAuth := make([]string, 0, len(statuses))
	for _, server := range mcpServers {
		if statuses[server] == "notLoggedIn" {
			serversNeedingAuth = append(serversNeedingAuth, server)
		}
	}
	return serversNeedingAuth, true
}

type codexPluginOAuthFlow struct {
	session codexConnectorRPC
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	pending map[string]struct{}
}

func newCodexPluginOAuthFlow(
	ctx context.Context,
	cancel context.CancelFunc,
	session codexConnectorRPC,
	mcpServers []string,
) *codexPluginOAuthFlow {
	pending := make(map[string]struct{}, len(mcpServers))
	for _, server := range mcpServers {
		pending[server] = struct{}{}
	}
	flow := &codexPluginOAuthFlow{
		session: session,
		cancel:  cancel,
		done:    make(chan struct{}),
		pending: pending,
	}
	go flow.consumeNotifications(ctx)
	return flow
}

func (f *codexPluginOAuthFlow) consumeNotifications(ctx context.Context) {
	queued, overflow := f.session.drainNotifications()
	if overflow {
		f.finish()
		return
	}
	for _, notification := range queued {
		if f.consumeNotification(notification) {
			return
		}
	}
	for {
		notification, err := f.session.nextNotification(ctx)
		if err != nil {
			f.finish()
			return
		}
		if f.consumeNotification(notification) {
			return
		}
	}
}

func (f *codexPluginOAuthFlow) consumeNotification(notification codexRPCNotification) bool {
	if notification.Method != "mcpServer/oauthLogin/completed" {
		return false
	}
	var completed struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(notification.Params, &completed) != nil {
		return false
	}
	f.mu.Lock()
	if _, expected := f.pending[completed.Name]; !expected {
		f.mu.Unlock()
		return false
	}
	delete(f.pending, completed.Name)
	finished := len(f.pending) == 0
	f.mu.Unlock()
	if finished {
		f.finish()
	}
	return finished
}

func (f *codexPluginOAuthFlow) finish() {
	if f == nil {
		return
	}
	f.once.Do(func() {
		f.session.close()
		f.cancel()
		close(f.done)
	})
}

func (f *codexPluginOAuthFlow) Close() {
	f.finish()
}

type codexPluginAppSummary struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	InstallURL *string `json:"installUrl"`
}

func codexPluginAuthenticationURL(apps []codexPluginAppSummary) (string, bool) {
	continueURL := ""
	fallbackAppID := ""
	for _, app := range apps {
		if !validCodexConnectorNativeID(app.ID) ||
			sanitizeCodexConnectorText(app.Name, codexConnectorMaximumName) == "" {
			return "", false
		}
		if fallbackAppID == "" {
			fallbackAppID = app.ID
		}
		if continueURL == "" && app.InstallURL != nil {
			continueURL = codexHostedConnectorSetupURL(
				validatedCodexPluginAuthURL(*app.InstallURL),
				app.ID,
			)
		}
	}
	if len(apps) == 0 {
		return "", true
	}
	if continueURL == "" {
		continueURL = codexHostedConnectorSetupURL(
			codexPluginAuthFallbackURL,
			fallbackAppID,
		)
	}
	return continueURL, continueURL != ""
}

func validatedCodexPluginAuthURL(value string) string {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) {
		return ""
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Opaque != "" || parsed.RawPath != "" ||
		!strings.EqualFold(parsed.Hostname(), "chatgpt.com") ||
		(parsed.Path != "/apps" && !strings.HasPrefix(parsed.Path, "/apps/")) ||
		path.Clean(parsed.Path) != parsed.Path || strings.Contains(parsed.Path, "//") {
		return ""
	}
	parsed.Scheme = "https"
	parsed.Host = "chatgpt.com"
	return parsed.String()
}

func (osCodexPluginTransport) Uninstall(
	ctx context.Context,
	binaryPath string,
	locator codexPluginLocator,
) error {
	uninstallCtx, cancel := context.WithTimeout(ctx, codexPluginLifecycleTimeout)
	defer cancel()
	session, err := startCodexRPCSessionWithLimit(uninstallCtx, binaryPath, codexPluginMessageLimit)
	if err != nil {
		return ErrCodexPluginsUnavailable
	}
	defer session.close()
	raw, err := session.request(uninstallCtx, "plugin/uninstall", map[string]any{
		"pluginId": locator.uninstallID(),
	})
	if err != nil {
		return ErrCodexPluginsUnavailable
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(raw, &response) != nil {
		return ErrCodexPluginsUnavailable
	}
	return nil
}

// CodexPluginBackend owns plugin discovery and lifecycle state for one Codex executable.
// It intentionally remains separate from connector execution authority.
type CodexPluginBackend struct {
	mu          sync.RWMutex
	operationMu sync.Mutex
	path        string
	transport   codexPluginTransport
	cached      *codexPluginCatalog
	// cacheGeneration counts every change to cached, so a read made outside
	// operationMu (locator) caches its catalog only if nothing replaced or
	// invalidated cached while it ran.
	cacheGeneration uint64
	oauthFlows      map[string]*codexPluginOAuthFlow
	oauthWorkers    sync.WaitGroup
	changeHandler   func()
	closed          bool
}

func newCodexPluginBackend(path string, transport codexPluginTransport) *CodexPluginBackend {
	return &CodexPluginBackend{
		path: path, transport: transport,
		oauthFlows: make(map[string]*codexPluginOAuthFlow),
	}
}

// NewOSCodexPluginBackend creates a plugin backend using the discovered Codex binary.
func NewOSCodexPluginBackend(path string) *CodexPluginBackend {
	return newCodexPluginBackend(path, osCodexPluginTransport{})
}

// Discover returns a complete sanitized plugin catalog. force asks Codex to
// bypass its remote catalog cache, but never clears Zenith Team's last good snapshot.
func (b *CodexPluginBackend) Discover(
	ctx context.Context,
	force bool,
) (CodexPluginSnapshot, error) {
	if b == nil || b.path == "" || b.transport == nil {
		return CodexPluginSnapshot{}, ErrCodexPluginsUnavailable
	}
	b.operationMu.Lock()
	defer b.operationMu.Unlock()
	if b.isClosed() {
		return CodexPluginSnapshot{}, ErrCodexPluginsUnavailable
	}
	catalog, err := b.transport.Read(ctx, b.path, force)
	if err != nil {
		return CodexPluginSnapshot{}, mapCodexPluginError(err)
	}
	b.mu.Lock()
	cached := cloneCodexPluginCatalog(catalog)
	b.cached = &cached
	b.cacheGeneration++
	b.mu.Unlock()
	return snapshotFromCodexPluginCatalog(catalog), nil
}

// Install invokes plugin/install only for an exact ID authorized by discovery.
func (b *CodexPluginBackend) Install(
	ctx context.Context,
	nativeID string,
) (CodexPluginInstallResult, error) {
	if b == nil || !validCodexConnectorNativeID(nativeID) {
		return CodexPluginInstallResult{}, ErrCodexPluginNotFound
	}
	b.operationMu.Lock()
	result, err := b.install(ctx, nativeID)
	b.operationMu.Unlock()
	if err != nil {
		return CodexPluginInstallResult{}, err
	}
	b.notifyChange()
	return result, nil
}

func (b *CodexPluginBackend) install(
	ctx context.Context,
	nativeID string,
) (CodexPluginInstallResult, error) {
	locator, err := b.locator(ctx, nativeID)
	if err != nil {
		return CodexPluginInstallResult{}, err
	}
	if locator.action != CodexPluginActionInstall {
		return CodexPluginInstallResult{}, ErrCodexPluginNotInstallable
	}
	b.mu.RLock()
	_, flowAlreadyActive := b.oauthFlows[nativeID]
	flowCapacityReached := len(b.oauthFlows) >= codexConnectorMaximumOAuthFlows
	b.mu.RUnlock()
	if flowAlreadyActive || flowCapacityReached {
		return CodexPluginInstallResult{}, ErrCodexPluginsUnavailable
	}
	result, err := b.transport.Install(ctx, b.path, locator)
	if err != nil {
		return CodexPluginInstallResult{}, mapCodexPluginError(err)
	}
	b.invalidate()
	if result.oauthFlow != nil {
		b.observeOAuthFlow(nativeID, result.oauthFlow)
	}
	return result, nil
}

func (b *CodexPluginBackend) observeOAuthFlow(
	nativeID string,
	flow *codexPluginOAuthFlow,
) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		flow.Close()
		return
	}
	b.oauthFlows[nativeID] = flow
	b.oauthWorkers.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.oauthWorkers.Done()
		<-flow.done
		b.mu.Lock()
		owned := b.oauthFlows[nativeID] == flow
		if owned {
			delete(b.oauthFlows, nativeID)
			b.cached = nil
			b.cacheGeneration++
		}
		handler := b.changeHandler
		closed := b.closed
		b.mu.Unlock()
		if owned && !closed && handler != nil {
			handler()
		}
	}()
}

// Uninstall invokes plugin/uninstall only for an exact ID authorized by discovery.
func (b *CodexPluginBackend) Uninstall(ctx context.Context, nativeID string) error {
	if b == nil || !validCodexConnectorNativeID(nativeID) {
		return ErrCodexPluginNotFound
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

// ReadDetail reads one plugin live through plugin/read, located exactly as
// the last complete discovery found it. It does not wait for a discovery or
// lifecycle operation in flight.
func (b *CodexPluginBackend) ReadDetail(
	ctx context.Context,
	nativeID string,
) (CodexPluginDetail, error) {
	if b == nil || b.path == "" || b.transport == nil {
		return CodexPluginDetail{}, ErrCodexPluginsUnavailable
	}
	if !validCodexConnectorNativeID(nativeID) {
		return CodexPluginDetail{}, ErrCodexPluginNotFound
	}
	locator, err := b.locator(ctx, nativeID)
	if err != nil {
		return CodexPluginDetail{}, err
	}
	detail, err := b.transport.ReadDetail(ctx, b.path, locator)
	if err != nil {
		return CodexPluginDetail{}, mapCodexPluginError(err)
	}
	b.completeDetail(&detail, locator)
	return detail, nil
}

// completeDetail gives a live read what discovery completed from the
// marketplace listing, which plugin/read does not carry either: a name that
// is still the raw slug takes the catalog's name, and an empty developer or
// website takes the catalog's. Codex's own live values win.
func (b *CodexPluginBackend) completeDetail(
	detail *CodexPluginDetail,
	locator codexPluginLocator,
) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.cached == nil {
		return
	}
	for _, plugin := range b.cached.plugins {
		if plugin.NativeID != detail.NativeID {
			continue
		}
		if detail.Name == sanitizeCodexConnectorText(locator.pluginName, codexConnectorMaximumName) {
			detail.Name = plugin.Name
		}
		if detail.DeveloperName == "" {
			detail.DeveloperName = plugin.DeveloperName
		}
		if detail.WebsiteURL == "" {
			detail.WebsiteURL = plugin.WebsiteURL
		}
		return
	}
}

// ReadIcon returns a bounded local raster or SVG icon for a plugin from the
// last complete discovery. The native filesystem path never crosses this
// boundary.
func (b *CodexPluginBackend) ReadIcon(
	ctx context.Context,
	nativeID string,
) (CodexPluginIcon, error) {
	if b == nil || !validCodexConnectorNativeID(nativeID) {
		return CodexPluginIcon{}, ErrCodexPluginIconUnavailable
	}
	b.mu.RLock()
	if b.closed || b.cached == nil {
		b.mu.RUnlock()
		return CodexPluginIcon{}, ErrCodexPluginIconUnavailable
	}
	assets := append([]codexPluginIconAsset(nil), b.cached.icons[nativeID]...)
	b.mu.RUnlock()
	if len(assets) == 0 {
		return CodexPluginIcon{}, ErrCodexPluginIconUnavailable
	}
	for _, asset := range assets {
		icon, err := readCodexPluginIcon(ctx, asset)
		if err == nil {
			return icon, nil
		}
		if ctx.Err() != nil {
			return CodexPluginIcon{}, ErrCodexPluginIconUnavailable
		}
	}
	return CodexPluginIcon{}, ErrCodexPluginIconUnavailable
}

func (b *CodexPluginBackend) uninstall(ctx context.Context, nativeID string) error {
	locator, err := b.locator(ctx, nativeID)
	if err != nil {
		return err
	}
	if locator.action != CodexPluginActionUninstall {
		return ErrCodexPluginNotUninstallable
	}
	if err := b.transport.Uninstall(ctx, b.path, locator); err != nil {
		return mapCodexPluginError(err)
	}
	b.cancelOAuthFlow(nativeID)
	b.invalidate()
	return nil
}

func (b *CodexPluginBackend) cancelOAuthFlow(nativeID string) {
	b.mu.Lock()
	flow := b.oauthFlows[nativeID]
	delete(b.oauthFlows, nativeID)
	b.mu.Unlock()
	if flow != nil {
		flow.Close()
	}
}

func (b *CodexPluginBackend) locator(
	ctx context.Context,
	nativeID string,
) (codexPluginLocator, error) {
	if b.isClosed() {
		return codexPluginLocator{}, ErrCodexPluginsUnavailable
	}
	b.mu.RLock()
	if b.cached != nil {
		locator, exists := b.cached.locators[nativeID]
		b.mu.RUnlock()
		if !exists {
			return codexPluginLocator{}, ErrCodexPluginNotFound
		}
		return locator, nil
	}
	generation := b.cacheGeneration
	b.mu.RUnlock()
	catalog, err := b.transport.Read(ctx, b.path, false)
	if err != nil {
		return codexPluginLocator{}, mapCodexPluginError(err)
	}
	cached := cloneCodexPluginCatalog(catalog)
	b.mu.Lock()
	// ReadDetail reads without operationMu: a read that a Discover, a
	// lifecycle change or an OAuth completion overtook answers from its own
	// catalog but never restores it over theirs.
	if b.cacheGeneration == generation {
		b.cached = &cached
		b.cacheGeneration++
	}
	b.mu.Unlock()
	locator, exists := cached.locators[nativeID]
	if !exists {
		return codexPluginLocator{}, ErrCodexPluginNotFound
	}
	return locator, nil
}

func (b *CodexPluginBackend) invalidate() {
	b.mu.Lock()
	b.cached = nil
	b.cacheGeneration++
	b.mu.Unlock()
}

func (b *CodexPluginBackend) notifyChange() {
	b.mu.RLock()
	handler := b.changeHandler
	closed := b.closed
	b.mu.RUnlock()
	if !closed && handler != nil {
		handler()
	}
}

// SetChangeHandler replaces the callback invoked after a native lifecycle mutation.
func (b *CodexPluginBackend) SetChangeHandler(handler func()) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if !b.closed {
		b.changeHandler = handler
	}
	b.mu.Unlock()
}

// Close prevents future work and releases every plugin OAuth app-server session.
func (b *CodexPluginBackend) Close() {
	if b == nil {
		return
	}
	b.operationMu.Lock()
	b.mu.Lock()
	b.closed = true
	b.cached = nil
	b.cacheGeneration++
	b.changeHandler = nil
	flows := make([]*codexPluginOAuthFlow, 0, len(b.oauthFlows))
	for _, flow := range b.oauthFlows {
		flows = append(flows, flow)
	}
	b.mu.Unlock()
	b.operationMu.Unlock()
	for _, flow := range flows {
		flow.Close()
	}
	b.oauthWorkers.Wait()
}

func (b *CodexPluginBackend) isClosed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.closed
}

type codexPluginMarketplace struct {
	Name    string               `json:"name"`
	Path    *string              `json:"path"`
	Plugins []codexPluginSummary `json:"plugins"`
}

type codexPluginListResponse struct {
	Marketplaces          *[]codexPluginMarketplace `json:"marketplaces"`
	FeaturedPluginIDs     *[]string                 `json:"featuredPluginIds"`
	MarketplaceLoadErrors []json.RawMessage         `json:"marketplaceLoadErrors"`
}

type codexPluginSummary struct {
	ID                               string                   `json:"id"`
	RemotePluginID                   *string                  `json:"remotePluginId"`
	Name                             string                   `json:"name"`
	Installed                        bool                     `json:"installed"`
	Enabled                          bool                     `json:"enabled"`
	InstallPolicy                    string                   `json:"installPolicy"`
	AuthPolicy                       string                   `json:"authPolicy"`
	Availability                     string                   `json:"availability"`
	MustShowInstallationInterstitial *bool                    `json:"mustShowInstallationInterstitial"`
	Source                           codexPluginSource        `json:"source"`
	Interface                        *codexPluginInterface    `json:"interface"`
	ShareContext                     *codexPluginShareContext `json:"shareContext"`
}

type codexPluginShareContext struct {
	ShareURL *string `json:"shareUrl"`
}

type codexPluginSource struct {
	Type string  `json:"type"`
	Path *string `json:"path"`
}

type codexPluginInterface struct {
	DisplayName       *string   `json:"displayName"`
	ShortDescription  *string   `json:"shortDescription"`
	LongDescription   *string   `json:"longDescription"`
	Category          *string   `json:"category"`
	ComposerIcon      *string   `json:"composerIcon"`
	ComposerIconURL   *string   `json:"composerIconUrl"`
	Logo              *string   `json:"logo"`
	LogoDark          *string   `json:"logoDark"`
	LogoURL           *string   `json:"logoUrl"`
	LogoURLDark       *string   `json:"logoUrlDark"`
	DefaultPrompt     *[]string `json:"defaultPrompt"`
	BrandColor        *string   `json:"brandColor"`
	DeveloperName     *string   `json:"developerName"`
	WebsiteURL        *string   `json:"websiteUrl"`
	PrivacyPolicyURL  *string   `json:"privacyPolicyUrl"`
	TermsOfServiceURL *string   `json:"termsOfServiceUrl"`
}

type codexPluginReadResponse struct {
	Plugin struct {
		Summary    codexPluginSummary `json:"summary"`
		ShareURL   *string            `json:"shareUrl"`
		Apps       []json.RawMessage  `json:"apps"`
		MCPServers []string           `json:"mcpServers"`
		Skills     []json.RawMessage  `json:"skills"`
	} `json:"plugin"`
}

func readCodexPluginCatalog(
	ctx context.Context,
	session codexConnectorRPC,
	force bool,
) (codexPluginCatalog, error) {
	enabled, err := codexPluginFeatureEnabled(ctx, session)
	if err != nil {
		return codexPluginCatalog{}, fmt.Errorf("check Codex plugin feature: %w", err)
	}
	if !enabled {
		return codexPluginCatalog{}, errors.New("Codex plugin feature is unavailable")
	}
	raw, err := session.request(ctx, "plugin/list", map[string]any{
		"cwds": nil, "marketplaceKinds": nil, "forceRefetch": force,
	})
	if err != nil {
		// A frame over codexPluginMessageLimit surfaces as bufio.ErrTooLong.
		return codexPluginCatalog{}, fmt.Errorf("read Codex plugin catalog: %w", err)
	}
	var response codexPluginListResponse
	if json.Unmarshal(raw, &response) != nil || response.Marketplaces == nil ||
		response.FeaturedPluginIDs == nil || len(response.MarketplaceLoadErrors) != 0 {
		return codexPluginCatalog{}, errors.New("decode Codex plugin catalog")
	}
	featured := make(map[string]struct{}, len(*response.FeaturedPluginIDs))
	for _, nativeID := range *response.FeaturedPluginIDs {
		if !validCodexConnectorNativeID(nativeID) {
			return codexPluginCatalog{}, errors.New("Codex featured plugin ID is invalid")
		}
		featured[nativeID] = struct{}{}
	}
	builder := newCodexPluginCatalogBuilder(featured)
	for _, marketplace := range *response.Marketplaces {
		if err := builder.appendMarketplace(marketplace); err != nil {
			return codexPluginCatalog{}, err
		}
	}
	catalog := builder.build()
	readInstalledCodexPluginDetails(ctx, session, &catalog)
	return catalog, nil
}

func newCodexPluginCatalogBuilder(
	featured map[string]struct{},
) *codexPluginCatalogBuilder {
	return &codexPluginCatalogBuilder{
		result: codexPluginCatalog{
			plugins:  make([]CodexPlugin, 0),
			locators: make(map[string]codexPluginLocator),
			icons:    make(map[string][]codexPluginIconAsset),
		},
		featured:     featured,
		ambiguousIDs: make(map[string]struct{}),
		listings:     make(map[string]map[string]codexMarketplaceListing),
	}
}

func (b *codexPluginCatalogBuilder) appendMarketplace(
	marketplace codexPluginMarketplace,
) error {
	marketplaceName := sanitizeCodexConnectorText(
		marketplace.Name,
		codexPluginMaximumMarketplace,
	)
	if marketplaceName == "" || marketplaceName != marketplace.Name {
		return errors.New("Codex plugin marketplace is invalid")
	}
	for _, summary := range marketplace.Plugins {
		if b.itemCount >= codexPluginMaximumItems {
			return errors.New("Codex plugin catalog exceeds item limit")
		}
		b.itemCount++
		plugin, locator, ok := codexPluginFromSummary(
			summary,
			marketplaceName,
			marketplace.Path,
			len(b.result.plugins),
			b.featured,
			b.listing(marketplace.Path, summary),
		)
		if !ok {
			return errors.New("Codex plugin catalog item is invalid")
		}
		if _, ambiguous := b.ambiguousIDs[plugin.NativeID]; ambiguous {
			continue
		}
		if _, duplicate := b.result.locators[plugin.NativeID]; duplicate {
			b.quarantine(plugin.NativeID)
			continue
		}
		b.result.plugins = append(b.result.plugins, plugin)
		b.result.locators[plugin.NativeID] = locator
		if assets := codexPluginIconAssets(summary, marketplace.Path); len(assets) != 0 {
			b.result.icons[plugin.NativeID] = assets
		}
	}
	return nil
}

func (b *codexPluginCatalogBuilder) quarantine(nativeID string) {
	b.ambiguousIDs[nativeID] = struct{}{}
	delete(b.result.locators, nativeID)
	delete(b.result.icons, nativeID)
	for index, plugin := range b.result.plugins {
		if plugin.NativeID != nativeID {
			continue
		}
		b.result.plugins = append(
			b.result.plugins[:index],
			b.result.plugins[index+1:]...,
		)
		break
	}
	for index := range b.result.plugins {
		b.result.plugins[index].CatalogRank = index
	}
}

// listing returns what the plugin's marketplace manifest says about it, read
// only for a plugin whose interface lacks a display name or short description.
func (b *codexPluginCatalogBuilder) listing(
	marketplacePath *string,
	summary codexPluginSummary,
) codexMarketplaceListing {
	if marketplacePath == nil || !codexPluginNeedsListing(summary) {
		return codexMarketplaceListing{}
	}
	listings, read := b.listings[*marketplacePath]
	if !read {
		listings = readCodexMarketplaceListings(*marketplacePath)
		b.listings[*marketplacePath] = listings
	}
	return listings[summary.Name]
}

// build ranks the plugins of OpenAI's own marketplaces ahead of every other
// marketplace's, keeping Codex's order within each group: plugin/list lists a
// third-party marketplace before OpenAI's curated one, whose plugins would
// otherwise sink below it in every category.
func (b *codexPluginCatalogBuilder) build() codexPluginCatalog {
	plugins := b.result.plugins
	firstParty := func(index int) bool {
		return codexFirstPartyMarketplace(b.result.locators[plugins[index].NativeID].marketplaceName)
	}
	sort.SliceStable(plugins, func(left, right int) bool {
		return firstParty(left) && !firstParty(right)
	})
	for index := range plugins {
		plugins[index].CatalogRank = index
	}
	return b.result
}

func codexPluginFromSummary(
	summary codexPluginSummary,
	marketplaceName string,
	marketplacePath *string,
	rank int,
	featured map[string]struct{},
	listing codexMarketplaceListing,
) (CodexPlugin, codexPluginLocator, bool) {
	if !validCodexConnectorNativeID(summary.ID) ||
		!validCodexConnectorNativeID(summary.Name) {
		return CodexPlugin{}, codexPluginLocator{}, false
	}
	name, description, category, iconURL, shareURL := "", "", "", "", ""
	if summary.ShareContext != nil && summary.ShareContext.ShareURL != nil {
		shareURL = validatedCodexPluginShareURL(*summary.ShareContext.ShareURL)
	}
	if summary.Interface != nil {
		if summary.Interface.DisplayName != nil {
			name = sanitizeCodexConnectorText(*summary.Interface.DisplayName, codexConnectorMaximumName)
		}
		if summary.Interface.ShortDescription != nil {
			description = sanitizeCodexConnectorText(
				*summary.Interface.ShortDescription,
				codexConnectorMaximumDescription,
			)
		}
		if description == "" && summary.Interface.LongDescription != nil {
			description = sanitizeCodexConnectorText(
				*summary.Interface.LongDescription,
				codexConnectorMaximumDescription,
			)
		}
		if summary.Interface.Category != nil {
			category = sanitizeCodexConnectorText(*summary.Interface.Category, 80)
		}
		iconURL = codexPluginIconURL(summary.Interface)
	}
	// Codex's own interface values win; the marketplace listing fills what it
	// left empty, and a name still missing is made readable from the slug.
	presentation := codexPluginPresentationFromInterface(summary.Interface)
	if name == "" {
		name = listing.DisplayName
	}
	if description == "" {
		description = listing.Description
	}
	if presentation.DeveloperName == "" {
		presentation.DeveloperName = listing.DeveloperName
	}
	if presentation.WebsiteURL == "" {
		presentation.WebsiteURL = listing.WebsiteURL
	}
	if name == "" {
		// An unnamed plugin of OpenAI's own marketplaces is internal
		// (codex-app-tools, unified-computer-use); it keeps its slug, which is
		// how the client keeps it out of the Installed strip as Codex does.
		if strings.HasPrefix(marketplaceName, codexFirstPartyMarketplacePrefix) {
			name = summary.Name
		} else {
			name = humanizedCodexPluginName(summary.Name, presentation.DeveloperName, description)
		}
	}
	if name == "" {
		return CodexPlugin{}, codexPluginLocator{}, false
	}
	plugin := CodexPlugin{
		NativeID:                summary.ID,
		Name:                    name,
		Description:             description,
		Category:                category,
		IconURL:                 iconURL,
		ShareURL:                shareURL,
		Installed:               summary.Installed,
		Enabled:                 summary.Enabled,
		CatalogRank:             rank,
		Action:                  CodexPluginActionNone,
		Status:                  CodexPluginStatusError,
		CodexPluginPresentation: presentation,
	}
	_, plugin.Featured = featured[summary.ID]
	// Long descriptions dominate the discovery payload, so only the plugins a
	// person is most likely to open carry one; the live read has the rest.
	if !plugin.Installed && !plugin.Featured {
		plugin.LongDescription = ""
	}
	locator := codexPluginLocator{
		nativeID:        summary.ID,
		pluginName:      summary.Name,
		marketplaceName: marketplaceName,
	}
	sourceValid := codexPluginLocatorSource(summary, marketplacePath, &locator)
	switch {
	case !sourceValid:
	case summary.Installed && summary.Enabled:
		plugin.Status = CodexPluginStatusInstalled
		if codexPluginCanUninstall(summary) {
			plugin.Action = CodexPluginActionUninstall
		}
		locator.action = plugin.Action
	case summary.Installed:
		plugin.Enabled = false
		plugin.Status = CodexPluginStatusDisabled
		if codexPluginCanUninstall(summary) {
			plugin.Action = CodexPluginActionUninstall
		}
		locator.action = plugin.Action
	case codexPluginCanInstall(summary):
		plugin.Enabled = false
		plugin.Status = CodexPluginStatusAvailable
		plugin.Action = CodexPluginActionInstall
		locator.action = plugin.Action
	default:
		plugin.Enabled = false
	}
	return plugin, locator, true
}

func codexPluginLocatorSource(
	summary codexPluginSummary,
	marketplacePath *string,
	locator *codexPluginLocator,
) bool {
	switch summary.Source.Type {
	case "remote":
		if summary.RemotePluginID == nil ||
			!validCodexPluginRPCID(*summary.RemotePluginID) {
			return false
		}
		locator.remotePluginID = *summary.RemotePluginID
		return true
	case "local", "git", "npm":
		if marketplacePath == nil || !validCodexPluginMarketplacePath(*marketplacePath) {
			return false
		}
		locator.marketplacePath = *marketplacePath
		return true
	default:
		return false
	}
}

func codexPluginCanInstall(summary codexPluginSummary) bool {
	if summary.Availability != "AVAILABLE" ||
		summary.InstallPolicy == "NOT_AVAILABLE" ||
		summary.InstallPolicy == "INSTALLED_BY_DEFAULT" ||
		(summary.AuthPolicy != "ON_INSTALL" && summary.AuthPolicy != "ON_USE") {
		return false
	}
	if summary.Source.Type == "remote" {
		return summary.MustShowInstallationInterstitial != nil &&
			!*summary.MustShowInstallationInterstitial
	}
	return true
}

func codexPluginCanUninstall(summary codexPluginSummary) bool {
	return summary.InstallPolicy != "INSTALLED_BY_DEFAULT"
}

// codexPluginIconURL picks a plugin's logo from logoUrl, logoUrlDark and
// composerIconUrl, in that order, preferring one that loads outside a ChatGPT
// session; a session-only logo is kept only when no other is valid.
func codexPluginIconURL(pluginInterface *codexPluginInterface) string {
	candidates := make([]string, 0, 3)
	for _, candidate := range []*string{
		pluginInterface.LogoURL,
		pluginInterface.LogoURLDark,
		pluginInterface.ComposerIconURL,
	} {
		if candidate != nil {
			candidates = append(candidates, *candidate)
		}
	}
	return loadableFirstCodexIconURL(candidates...)
}

// loadableFirstCodexIconURL returns the first candidate, in order, that
// sanitizes and loads outside a ChatGPT session, and otherwise the first one
// that sanitizes: a session-only logo is kept only when no other is valid.
func loadableFirstCodexIconURL(candidates ...string) string {
	sessionOnly := ""
	for _, candidate := range candidates {
		sanitized := sanitizedCodexConnectorIconURL(candidate)
		switch {
		case sanitized == "":
		case !sessionOnlyCodexIconURL(sanitized):
			return sanitized
		case sessionOnly == "":
			sessionOnly = sanitized
		}
	}
	return sessionOnly
}

// sessionOnlyCodexIconURL reports whether a sanitized Codex logo URL loads only
// inside a signed-in ChatGPT browser session. Every image chatgpt.com serves,
// the first-party app paths /images/ecosystem/apps/<slug>/icon.png and the
// signed /backend-api/estuary/content URLs alike, answers 403 to anyone else,
// while files.openai.com serves logos to everyone (measured 2026-09-25). This
// is the daemon's one test of it.
func sessionOnlyCodexIconURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && strings.EqualFold(parsed.Hostname(), "chatgpt.com")
}

func codexPluginPresentationFromInterface(
	pluginInterface *codexPluginInterface,
) CodexPluginPresentation {
	var presentation CodexPluginPresentation
	if pluginInterface == nil {
		return presentation
	}
	if pluginInterface.LongDescription != nil {
		presentation.LongDescription = sanitizeCodexPluginParagraphs(
			*pluginInterface.LongDescription,
			4000,
		)
	}
	if pluginInterface.DefaultPrompt != nil {
		presentation.DefaultPrompts = codexPluginDefaultPrompts(*pluginInterface.DefaultPrompt)
	}
	if pluginInterface.BrandColor != nil {
		presentation.BrandColor = normalizedCodexPluginBrandColor(*pluginInterface.BrandColor)
	}
	if pluginInterface.DeveloperName != nil {
		presentation.DeveloperName = sanitizeCodexPluginLine(*pluginInterface.DeveloperName, 120)
	}
	for _, field := range []struct {
		value  *string
		target *string
	}{
		{pluginInterface.WebsiteURL, &presentation.WebsiteURL},
		{pluginInterface.PrivacyPolicyURL, &presentation.PrivacyPolicyURL},
		{pluginInterface.TermsOfServiceURL, &presentation.TermsOfServiceURL},
	} {
		if field.value != nil {
			*field.target = validatedCodexPluginPresentationURL(*field.value)
		}
	}
	return presentation
}

// codexPluginDefaultPrompts keeps at most three starter prompts, each one
// line of 1..128 runes. An entry that breaks the rule is dropped, never cut,
// so a prompt is always what its author wrote.
func codexPluginDefaultPrompts(values []string) []string {
	prompts := make([]string, 0, 3)
	for _, value := range values {
		if len(prompts) == 3 {
			break
		}
		prompt := strings.Join(strings.FieldsFunc(value, codexPluginTextSeparator), " ")
		if prompt == "" || utf8.RuneCountInString(prompt) > 128 {
			continue
		}
		prompts = append(prompts, prompt)
	}
	if len(prompts) == 0 {
		return nil
	}
	return prompts
}

// normalizedCodexPluginBrandColor accepts #RRGGBB and expands #RGB, keeping
// the letter case the plugin wrote.
func normalizedCodexPluginBrandColor(value string) string {
	value = strings.TrimSpace(value)
	if len(value) != 4 && len(value) != 7 || value[0] != '#' {
		return ""
	}
	for _, character := range value[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return ""
		}
	}
	if len(value) == 7 {
		return value
	}
	return string([]byte{
		'#', value[1], value[1], value[2], value[2], value[3], value[3],
	})
}

// validatedCodexPluginPresentationURL returns the canonical form of an https
// link of at most 2048 bytes with a host and no credentials or fragment.
// url.Parse rejects only ASCII control bytes and String keeps the query as
// written, so a C1 control character (U+0080..U+009F) in the query is caught
// here: the server rejects any control character, and one bad link would
// otherwise drop the whole manifest.
func validatedCodexPluginPresentationURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 || strings.Contains(value, "#") {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" ||
		parsed.User != nil || parsed.Hostname() == "" {
		return ""
	}
	canonical := parsed.String()
	if len(canonical) > 2048 || strings.ContainsFunc(canonical, unicode.IsControl) {
		return ""
	}
	return canonical
}

// sanitizeCodexPluginLine collapses whitespace and control characters to
// single spaces and keeps at most maximum runes.
func sanitizeCodexPluginLine(value string, maximum int) string {
	value = strings.Join(strings.FieldsFunc(value, codexPluginTextSeparator), " ")
	return truncateCodexPluginText(value, maximum)
}

// sanitizeCodexPluginParagraphs keeps paragraph structure: every line is
// collapsed like a single line, blank-line runs become one blank line (at
// most two consecutive newlines), and the text is trimmed to maximum runes.
func sanitizeCodexPluginParagraphs(value string, maximum int) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	var builder strings.Builder
	blankLine := false
	for line := range strings.SplitSeq(value, "\n") {
		line = strings.Join(strings.FieldsFunc(line, codexPluginTextSeparator), " ")
		if line == "" {
			blankLine = builder.Len() > 0
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
			if blankLine {
				builder.WriteByte('\n')
			}
		}
		blankLine = false
		builder.WriteString(line)
	}
	return truncateCodexPluginText(builder.String(), maximum)
}

func truncateCodexPluginText(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := 0
	for offset := range value {
		if runes == maximum {
			return strings.TrimRightFunc(value[:offset], unicode.IsSpace)
		}
		runes++
	}
	return value
}

func codexPluginTextSeparator(character rune) bool {
	return unicode.IsSpace(character) || unicode.IsControl(character)
}

// codexPluginDetailFromRead sanitizes one plugin/read response. Component
// entries that break a rule are dropped; the read fails only when the
// response names another plugin or no displayable name.
func codexPluginDetailFromRead(
	response codexPluginReadResponse,
	nativeID string,
) (CodexPluginDetail, bool) {
	summary := response.Plugin.Summary
	if summary.ID != nativeID {
		return CodexPluginDetail{}, false
	}
	name := ""
	if summary.Interface != nil && summary.Interface.DisplayName != nil {
		name = sanitizeCodexConnectorText(*summary.Interface.DisplayName, codexConnectorMaximumName)
	}
	if name == "" {
		name = sanitizeCodexConnectorText(summary.Name, codexConnectorMaximumName)
	}
	if name == "" {
		return CodexPluginDetail{}, false
	}
	detail := CodexPluginDetail{
		NativeID:   nativeID,
		Name:       name,
		AuthPolicy: codexPluginAuthPolicy(summary.AuthPolicy),
		InstallationInterstitial: summary.MustShowInstallationInterstitial == nil ||
			*summary.MustShowInstallationInterstitial,
		CodexPluginPresentation: codexPluginPresentationFromInterface(summary.Interface),
		Apps:                    make([]CodexPluginApp, 0, len(response.Plugin.Apps)),
		Skills:                  make([]CodexPluginSkill, 0, len(response.Plugin.Skills)),
		MCPServers:              make([]string, 0, len(response.Plugin.MCPServers)),
	}
	if response.Plugin.ShareURL != nil {
		detail.ShareURL = validatedCodexPluginShareURL(*response.Plugin.ShareURL)
	}
	if detail.ShareURL == "" && summary.ShareContext != nil &&
		summary.ShareContext.ShareURL != nil {
		detail.ShareURL = validatedCodexPluginShareURL(*summary.ShareContext.ShareURL)
	}
	seenApps := make(map[string]struct{}, len(response.Plugin.Apps))
	for _, raw := range response.Plugin.Apps {
		if len(detail.Apps) == codexPluginMaximumComponents {
			break
		}
		var app struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Description *string `json:"description"`
			Category    *string `json:"category"`
		}
		if json.Unmarshal(raw, &app) != nil || !validCodexConnectorNativeID(app.ID) {
			continue
		}
		appName := sanitizeCodexConnectorText(app.Name, codexConnectorMaximumName)
		if _, duplicate := seenApps[app.ID]; duplicate || appName == "" {
			continue
		}
		seenApps[app.ID] = struct{}{}
		entry := CodexPluginApp{ID: app.ID, Name: appName}
		if app.Description != nil {
			entry.Description = sanitizeCodexConnectorText(
				*app.Description,
				codexConnectorMaximumDescription,
			)
		}
		if app.Category != nil {
			entry.Category = sanitizeCodexConnectorText(*app.Category, 80)
		}
		detail.Apps = append(detail.Apps, entry)
	}
	seenSkills := make(map[string]struct{}, len(response.Plugin.Skills))
	for _, raw := range response.Plugin.Skills {
		if len(detail.Skills) == codexPluginMaximumComponents {
			break
		}
		var skill struct {
			Name             string  `json:"name"`
			Description      string  `json:"description"`
			ShortDescription *string `json:"shortDescription"`
			Interface        *struct {
				DisplayName      *string `json:"displayName"`
				ShortDescription *string `json:"shortDescription"`
			} `json:"interface"`
		}
		if json.Unmarshal(raw, &skill) != nil {
			continue
		}
		skillName := sanitizeCodexConnectorText(skill.Name, codexConnectorMaximumNativeID)
		if _, duplicate := seenSkills[skillName]; duplicate || skillName == "" {
			continue
		}
		seenSkills[skillName] = struct{}{}
		entry := CodexPluginSkill{Name: skillName}
		descriptions := []*string{skill.ShortDescription}
		if skill.Interface != nil {
			if skill.Interface.DisplayName != nil {
				entry.DisplayName = sanitizeCodexConnectorText(
					*skill.Interface.DisplayName,
					codexConnectorMaximumName,
				)
			}
			descriptions = append(descriptions, skill.Interface.ShortDescription)
		}
		descriptions = append(descriptions, &skill.Description)
		for _, description := range descriptions {
			if description == nil || entry.Description != "" {
				continue
			}
			entry.Description = sanitizeCodexConnectorText(
				*description,
				codexConnectorMaximumDescription,
			)
		}
		detail.Skills = append(detail.Skills, entry)
	}
	seenServers := make(map[string]struct{}, len(response.Plugin.MCPServers))
	for _, server := range response.Plugin.MCPServers {
		if len(detail.MCPServers) == codexPluginMaximumComponents {
			break
		}
		if _, duplicate := seenServers[server]; duplicate || !validCodexConnectorNativeID(server) {
			continue
		}
		seenServers[server] = struct{}{}
		detail.MCPServers = append(detail.MCPServers, server)
	}
	return detail, true
}

// codexPluginAuthPolicy maps Codex's PluginAuthPolicy to Zenith Team's
// spelling; anything else is unknown.
func codexPluginAuthPolicy(value string) string {
	switch value {
	case "ON_INSTALL":
		return "on_install"
	case "ON_USE":
		return "on_use"
	default:
		return ""
	}
}

func codexPluginIconAssets(
	summary codexPluginSummary,
	marketplacePath *string,
) []codexPluginIconAsset {
	if summary.Interface == nil || summary.Source.Path == nil || marketplacePath == nil ||
		summary.Source.Type != "local" || !validCodexPluginAssetPath(*marketplacePath) {
		return nil
	}
	marketplaceRoot := codexPluginMarketplaceRoot(*marketplacePath)
	sourceRoot := *summary.Source.Path
	if !validCodexPluginAssetPath(sourceRoot) ||
		!codexPluginPathWithinRoot(marketplaceRoot, sourceRoot) {
		return nil
	}
	marketplaceRoot = filepath.Clean(marketplaceRoot)
	sourceRoot = filepath.Clean(sourceRoot)
	assets := make([]codexPluginIconAsset, 0, 3)
	for _, candidate := range []*string{
		summary.Interface.Logo,
		summary.Interface.LogoDark,
		summary.Interface.ComposerIcon,
	} {
		if candidate == nil || !validCodexPluginAssetPath(*candidate) {
			continue
		}
		if !codexPluginPathWithinRoot(sourceRoot, *candidate) {
			continue
		}
		relativePath, err := filepath.Rel(marketplaceRoot, filepath.Clean(*candidate))
		if err != nil || relativePath == "." || !filepath.IsLocal(relativePath) {
			continue
		}
		assets = append(assets, codexPluginIconAsset{
			rootDirectory: marketplaceRoot,
			relativePath:  relativePath,
		})
	}
	return assets
}

func codexPluginMarketplaceRoot(value string) string {
	root := filepath.Dir(filepath.Clean(value))
	if filepath.Base(root) == "plugins" && filepath.Base(filepath.Dir(root)) == ".agents" {
		return filepath.Dir(filepath.Dir(root))
	}
	if filepath.Base(root) == ".codex-plugin" || filepath.Base(root) == ".claude-plugin" {
		return filepath.Dir(root)
	}
	return root
}

func codexPluginPathWithinRoot(rootDirectory, value string) bool {
	if !validCodexPluginAssetPath(rootDirectory) || !validCodexPluginAssetPath(value) {
		return false
	}
	relativePath, err := filepath.Rel(filepath.Clean(rootDirectory), filepath.Clean(value))
	return err == nil && relativePath != "." && filepath.IsLocal(relativePath)
}

func validCodexPluginAssetPath(value string) bool {
	return value != "" && len(value) <= codexPluginMaximumMarketplacePath &&
		utf8.ValidString(value) && filepath.IsAbs(value) &&
		!strings.ContainsRune(value, '\x00')
}

func readCodexPluginIcon(
	ctx context.Context,
	asset codexPluginIconAsset,
) (CodexPluginIcon, error) {
	if err := ctx.Err(); err != nil {
		return CodexPluginIcon{}, err
	}
	root, err := os.OpenRoot(asset.rootDirectory)
	if err != nil {
		return CodexPluginIcon{}, err
	}
	defer root.Close()
	file, err := root.Open(asset.relativePath)
	if err != nil {
		return CodexPluginIcon{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 ||
		info.Size() > codexPluginMaximumIconBytes {
		return CodexPluginIcon{}, ErrCodexPluginIconUnavailable
	}
	content, err := io.ReadAll(io.LimitReader(file, codexPluginMaximumIconBytes+1))
	if err != nil || len(content) == 0 || len(content) > codexPluginMaximumIconBytes {
		return CodexPluginIcon{}, ErrCodexPluginIconUnavailable
	}
	if err := ctx.Err(); err != nil {
		return CodexPluginIcon{}, err
	}
	contentType := http.DetectContentType(content)
	if validPluginSVGIcon(content) {
		contentType = "image/svg+xml"
	}
	if !validCodexPluginIconContent(contentType, content) {
		return CodexPluginIcon{}, ErrCodexPluginIconUnavailable
	}
	return CodexPluginIcon{ContentType: contentType, Bytes: content}, nil
}

func validCodexPluginIconContent(contentType string, content []byte) bool {
	switch contentType {
	case "image/png":
		return bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		return len(content) >= 3 && content[0] == 0xff && content[1] == 0xd8 &&
			content[2] == 0xff
	case "image/webp":
		return len(content) >= 12 && string(content[:4]) == "RIFF" &&
			string(content[8:12]) == "WEBP"
	case "image/svg+xml":
		return validPluginSVGIcon(content)
	default:
		return false
	}
}

// validPluginSVGIcon reports whether content is UTF-8 XML whose root element
// is <svg>. Only the prolog is parsed: comments, processing instructions and
// a doctype may precede the root, and no entity is ever expanded. The bytes
// are served with a sandboxing Content-Security-Policy, so an SVG that
// carries script cannot run it.
func validPluginSVGIcon(content []byte) bool {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(content) {
		return false
	}
	decoder := xml.NewDecoder(bytes.NewReader(content))
	for {
		token, err := decoder.RawToken()
		if err != nil {
			return false
		}
		switch value := token.(type) {
		case xml.StartElement:
			return value.Name.Local == "svg" &&
				(value.Name.Space == "" || value.Name.Space == "svg")
		case xml.CharData:
			if len(bytes.TrimSpace(value)) != 0 {
				return false
			}
		case xml.Comment, xml.ProcInst, xml.Directive:
		default:
			return false
		}
	}
}

func validCodexPluginMarketplacePath(value string) bool {
	return value != "" && len(value) <= codexPluginMaximumMarketplacePath &&
		utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validCodexPluginRPCID(value string) bool {
	if !validCodexConnectorOpaqueValue(value, codexConnectorMaximumNativeID) ||
		value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("._:@+~-", character) {
			continue
		}
		return false
	}
	return true
}

// codexPluginDetailBatch bounds one pipelined batch of plugin/read requests,
// which stays far inside a pipe buffer while app-server answers it.
const codexPluginDetailBatch = 8

// codexBatchRPC is a session that pipelines requests (codexRPCSession).
type codexBatchRPC interface {
	requestAll(context.Context, string, []any) ([]json.RawMessage, []error)
}

func readInstalledCodexPluginDetails(
	ctx context.Context,
	session codexConnectorRPC,
	catalog *codexPluginCatalog,
) {
	detailCtx, cancel := context.WithTimeout(ctx, codexPluginDetailBudget)
	defer cancel()
	installed := make([]*CodexPlugin, 0)
	for index := range catalog.plugins {
		if catalog.plugins[index].Installed {
			installed = append(installed, &catalog.plugins[index])
		}
	}
	// Each read of a remote plugin is a network round trip of most of a
	// second; one at a time, a few dozen installed plugins took the budget.
	batch := 1
	batched, canBatch := session.(codexBatchRPC)
	if canBatch {
		batch = codexPluginDetailBatch
	}
	for start := 0; start < len(installed); start += batch {
		if detailCtx.Err() != nil {
			return
		}
		plugins := installed[start:min(start+batch, len(installed))]
		params := make([]any, len(plugins))
		for index, plugin := range plugins {
			params[index] = catalog.locators[plugin.NativeID].requestParams()
		}
		var results []json.RawMessage
		var errs []error
		if canBatch {
			results, errs = batched.requestAll(detailCtx, "plugin/read", params)
		} else {
			raw, err := session.request(detailCtx, "plugin/read", params[0])
			results, errs = []json.RawMessage{raw}, []error{err}
		}
		for index, plugin := range plugins {
			if errs[index] == nil {
				applyCodexPluginDetail(plugin, results[index])
			}
		}
	}
}

func applyCodexPluginDetail(plugin *CodexPlugin, raw json.RawMessage) {
	var response codexPluginReadResponse
	if json.Unmarshal(raw, &response) != nil || response.Plugin.Summary.ID != plugin.NativeID {
		return
	}
	counts, ok := boundedCodexPluginComponentCounts(response)
	if !ok {
		return
	}
	plugin.ComponentCounts = counts
	if identities, valid := codexPluginConnectorIdentities(response); valid {
		plugin.ConnectorIdentities = identities
	}
	if response.Plugin.ShareURL != nil {
		if shareURL := validatedCodexPluginShareURL(*response.Plugin.ShareURL); shareURL != "" {
			plugin.ShareURL = shareURL
		}
	}
}

func codexPluginConnectorIdentities(
	response codexPluginReadResponse,
) ([]PluginConnectorIdentity, bool) {
	if len(response.Plugin.Apps)+len(response.Plugin.MCPServers) >
		codexPluginMaximumComponents*2 {
		return nil, false
	}
	identities := make([]PluginConnectorIdentity, 0,
		len(response.Plugin.Apps)+len(response.Plugin.MCPServers))
	seen := make(map[PluginConnectorIdentity]struct{}, cap(identities))
	for _, raw := range response.Plugin.Apps {
		var app struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &app) != nil || !validCodexConnectorNativeID(app.ID) {
			return nil, false
		}
		identity := PluginConnectorIdentity{
			Source: string(CodexConnectorSourceHostedApp), NativeID: app.ID,
		}
		if _, duplicate := seen[identity]; duplicate {
			return nil, false
		}
		seen[identity] = struct{}{}
		identities = append(identities, identity)
	}
	for _, nativeID := range response.Plugin.MCPServers {
		if !validCodexConnectorNativeID(nativeID) {
			return nil, false
		}
		identity := PluginConnectorIdentity{
			Source: string(CodexConnectorSourceLocalMCP), NativeID: nativeID,
		}
		if _, duplicate := seen[identity]; duplicate {
			return nil, false
		}
		seen[identity] = struct{}{}
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(left, right int) bool {
		if identities[left].Source == identities[right].Source {
			return identities[left].NativeID < identities[right].NativeID
		}
		return identities[left].Source < identities[right].Source
	})
	return identities, true
}

func boundedCodexPluginComponentCounts(
	response codexPluginReadResponse,
) (CodexPluginComponentCounts, bool) {
	counts := CodexPluginComponentCounts{
		Apps:       len(response.Plugin.Apps),
		MCPServers: len(response.Plugin.MCPServers),
		Skills:     len(response.Plugin.Skills),
	}
	if counts.Apps > codexPluginMaximumComponents ||
		counts.MCPServers > codexPluginMaximumComponents ||
		counts.Skills > codexPluginMaximumComponents {
		return CodexPluginComponentCounts{}, false
	}
	return counts, true
}

func validatedCodexPluginShareURL(value string) string {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) {
		return ""
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		parsed.Port() != "" || parsed.Fragment != "" || parsed.Opaque != "" ||
		!strings.EqualFold(parsed.Hostname(), "chatgpt.com") ||
		!strings.HasPrefix(parsed.EscapedPath(), "/plugins/") ||
		path.Clean(parsed.Path) != parsed.Path {
		return ""
	}
	if parsed.RawQuery != "" {
		query := parsed.Query()
		if len(query) != 1 || len(query["open_in_app"]) != 1 ||
			(query.Get("open_in_app") != "" && query.Get("open_in_app") != "true") {
			return ""
		}
	}
	parsed.Scheme = "https"
	parsed.Host = "chatgpt.com"
	return parsed.String()
}

func codexPluginFeatureEnabled(
	ctx context.Context,
	session codexConnectorRPC,
) (bool, error) {
	cursor := ""
	seenCursors := make(map[string]struct{})
	for range codexPluginMaximumFeaturePages {
		raw, err := session.request(ctx, "experimentalFeature/list", map[string]any{
			"cursor": cursorValue(cursor), "limit": codexPluginFeaturePageSize,
		})
		if err != nil {
			return false, fmt.Errorf("read Codex plugin feature: %w", err)
		}
		var page struct {
			Data []struct {
				Name    string `json:"name"`
				Stage   string `json:"stage"`
				Enabled bool   `json:"enabled"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &page) != nil {
			return false, errors.New("decode Codex plugin feature")
		}
		for _, feature := range page.Data {
			if feature.Name == "plugins" {
				return feature.Enabled && feature.Stage != "removed", nil
			}
		}
		next, err := nextCodexConnectorCursor(page.NextCursor, seenCursors)
		if err != nil {
			return false, err
		}
		if next == "" {
			return false, nil
		}
		cursor = next
	}
	return false, errors.New("Codex feature catalog exceeds page limit")
}

func cloneCodexPluginCatalog(source codexPluginCatalog) codexPluginCatalog {
	cloned := codexPluginCatalog{
		plugins:  append([]CodexPlugin(nil), source.plugins...),
		locators: make(map[string]codexPluginLocator, len(source.locators)),
		icons:    make(map[string][]codexPluginIconAsset, len(source.icons)),
	}
	for nativeID, locator := range source.locators {
		cloned.locators[nativeID] = locator
	}
	for nativeID, assets := range source.icons {
		cloned.icons[nativeID] = append([]codexPluginIconAsset(nil), assets...)
	}
	return cloned
}

func snapshotFromCodexPluginCatalog(catalog codexPluginCatalog) CodexPluginSnapshot {
	return CodexPluginSnapshot{
		SchemaVersion: 1,
		Plugins:       append([]CodexPlugin(nil), catalog.plugins...),
	}
}

func mapCodexPluginError(err error) error {
	switch {
	case errors.Is(err, ErrCodexPluginNotFound):
		return ErrCodexPluginNotFound
	case errors.Is(err, ErrCodexPluginNotInstallable):
		return ErrCodexPluginNotInstallable
	case errors.Is(err, ErrCodexPluginNotUninstallable):
		return ErrCodexPluginNotUninstallable
	default:
		return ErrCodexPluginsUnavailable
	}
}
