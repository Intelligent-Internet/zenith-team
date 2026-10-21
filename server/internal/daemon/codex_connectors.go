package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

const (
	// A read of Codex's ~4,600 hosted apps takes about 35-40 s, leaving room
	// to retry a page Codex failed to answer. The keepalive cadence counts on
	// one read fitting this budget (connectorDiscoveryInterval).
	codexConnectorReadTimeout   = 60 * time.Second
	codexConnectorOAuthTimeout  = 5 * time.Minute
	codexConnectorStatusTimeout = 20 * time.Second
	// app/list/updated carries the whole hosted app directory in one frame,
	// about 1.4 KB per app (6.3 MB for 4,568 apps in September 2026), so the
	// item limit fits with room to spare.
	codexConnectorMessageLimit        = 24 << 20
	codexConnectorPageSize            = 200
	codexConnectorMCPPageSize         = 50
	codexConnectorAppReadBatchSize    = 100
	codexConnectorMaximumAppTools     = 128
	codexConnectorMaximumPages        = 256
	codexConnectorMaximumItems        = 10000
	codexConnectorMaximumOAuthFlows   = 8
	codexConnectorMaximumWarnings     = 65535
	codexConnectorMaximumNativeID     = 240
	codexConnectorMaximumName         = 160
	codexConnectorMaximumDescription  = 1000
	codexConnectorMaximumOperationURL = 4096
	codexConnectorNotificationSettle  = 100 * time.Millisecond
	// The retry delays space the retries of a catalog page Codex
	// answered with an error. Its hosted app directory fails a page now and
	// then, and one failed page used to fail the whole discovery.
	codexConnectorFirstRetryDelay  = time.Second
	codexConnectorSecondRetryDelay = 3 * time.Second
	// A keepalive or linked read reuses the last app directory for
	// codexAppDirectoryReuse instead of asking ChatGPT to list every hosted
	// app again, which takes it about 30 s and sometimes fails with "Request
	// timeout" (a 500). A forced read always lists them. When listing fails, a directory up to
	// codexAppDirectoryFallback old stands in, and a listing that may fall
	// back gets codexAppDirectoryBudget, leaving the read's budget time for
	// the installed apps, their tools and the MCP servers.
	codexAppDirectoryReuse    = 30 * time.Minute
	codexAppDirectoryFallback = 24 * time.Hour
	codexAppDirectoryBudget   = 40 * time.Second
	// codexConnectorStatusReuse lets checks of several apps in quick
	// succession share one live app/installed read.
	codexConnectorStatusReuse = 3 * time.Second
)

// CodexConnectorSource identifies the native Codex surface that owns a connector.
type CodexConnectorSource string

const (
	// CodexConnectorSourceHostedApp is a ChatGPT-hosted app discovered through app/list.
	CodexConnectorSourceHostedApp CodexConnectorSource = "codex_app"
	// CodexConnectorSourceLocalMCP is an MCP server configured in the user's Codex home.
	CodexConnectorSourceLocalMCP CodexConnectorSource = "codex_mcp"
)

// CodexConnectorStatus is Zenith Team's conservative projection of native Codex state.
type CodexConnectorStatus string

const (
	CodexConnectorStatusConnected         CodexConnectorStatus = "connected"
	CodexConnectorStatusNeedsAuth         CodexConnectorStatus = "needs_auth"
	CodexConnectorStatusAvailable         CodexConnectorStatus = "available"
	CodexConnectorStatusInstalledNotReady CodexConnectorStatus = "installed_not_ready"
)

// CodexConnectorAction describes the safe user action available for a connector.
type CodexConnectorAction string

const (
	CodexConnectorActionInstallAndAuth CodexConnectorAction = "install_and_auth"
	CodexConnectorActionAuthenticate   CodexConnectorAction = "authenticate"
	CodexConnectorActionNone           CodexConnectorAction = "none"
)

// CodexConnector contains only bounded presentation metadata and the sanitized
// public tool namespace safe to map to daemon discovery. NativeID remains the
// exact opaque ID returned by Codex.
type CodexConnector struct {
	Source         CodexConnectorSource  `json:"source"`
	NativeID       string                `json:"nativeId"`
	Name           string                `json:"name"`
	Description    string                `json:"description,omitempty"`
	Category       string                `json:"category,omitempty"`
	IconURL        string                `json:"iconUrl,omitempty"`
	Status         CodexConnectorStatus  `json:"status"`
	Action         CodexConnectorAction  `json:"action"`
	CanExecute     bool                  `json:"canExecute"`
	ContractDigest string                `json:"contractDigest,omitempty"`
	Tools          []agent.ConnectorTool `json:"tools,omitempty"`
}

// CodexConnectorSnapshot is one locally discovered connector catalog.
type CodexConnectorSnapshot struct {
	SchemaVersion int              `json:"schemaVersion"`
	Connectors    []CodexConnector `json:"connectors"`
}

// CodexHostedConnect is the validated ChatGPT URL for a hosted app connection flow.
type CodexHostedConnect struct {
	NativeID string `json:"nativeId"`
	URL      string `json:"url"`
}

// CodexConnectorConnectResult is the safe result of starting a native Codex flow.
type CodexConnectorConnectResult struct {
	Source           CodexConnectorSource `json:"source"`
	NativeID         string               `json:"nativeId"`
	Status           CodexConnectorStatus `json:"status"`
	AuthorizationURL string               `json:"authorizationUrl,omitempty"`
}

// CodexMCPOAuthStatus is the terminal status of one local MCP OAuth attempt.
type CodexMCPOAuthStatus string

const (
	CodexMCPOAuthStatusSucceeded CodexMCPOAuthStatus = "succeeded"
	CodexMCPOAuthStatusFailed    CodexMCPOAuthStatus = "failed"
	CodexMCPOAuthStatusCancelled CodexMCPOAuthStatus = "cancelled"
)

// CodexMCPOAuthResult deliberately excludes the provider's raw error text.
type CodexMCPOAuthResult struct {
	NativeID string              `json:"nativeId"`
	Status   CodexMCPOAuthStatus `json:"status"`
}

var (
	// ErrCodexConnectorsUnavailable means Codex could not provide a safe local snapshot.
	ErrCodexConnectorsUnavailable = errors.New("Codex connectors are unavailable")
	// ErrCodexConnectorNotFound means the opaque native ID was absent from discovery.
	ErrCodexConnectorNotFound = errors.New("Codex connector was not found")
	// ErrCodexConnectorNotConnectable means Codex exposes no safe connect action.
	ErrCodexConnectorNotConnectable = errors.New("Codex connector cannot be connected")
)

type codexConnectorAction struct {
	source     CodexConnectorSource
	status     CodexConnectorStatus
	connectURL string
	oauth      bool
}

type codexConnectorWarnings struct {
	duplicates int
	conflicts  int
	skipped    int
}

func (w *codexConnectorWarnings) addDuplicate(conflict bool) {
	w.duplicates = incrementBounded(w.duplicates)
	if conflict {
		w.conflicts = incrementBounded(w.conflicts)
	}
}

func (w *codexConnectorWarnings) addSkipped() {
	w.skipped = incrementBounded(w.skipped)
}

func incrementBounded(value int) int {
	if value < codexConnectorMaximumWarnings {
		return value + 1
	}
	return value
}

type codexConnectorCatalog struct {
	connectors []CodexConnector
	actions    map[string]codexConnectorAction
	warnings   codexConnectorWarnings
}

// codexCatalogRefresh says which of Codex's caches one catalog read bypasses.
// A cached read may reuse the last app directory (readCodexAppDirectory) and
// Codex's installed-app cache. A linked read keeps that directory reuse but
// reads the installed apps with forceRefresh, which is all that changes when
// an app connects. A forced read also lists the app directory again.
type codexCatalogRefresh int

const (
	codexCatalogCached codexCatalogRefresh = iota
	codexCatalogLinked
	codexCatalogForced
)

func codexCatalogRefreshFor(force bool) codexCatalogRefresh {
	if force {
		return codexCatalogForced
	}
	return codexCatalogCached
}

type codexConnectorRPC interface {
	request(context.Context, string, any) (json.RawMessage, error)
	nextNotification(context.Context) (codexRPCNotification, error)
	drainNotifications() ([]codexRPCNotification, bool)
	close()
}

type codexConnectorBackend interface {
	Read(context.Context, string, bool) (codexConnectorCatalog, error)
	StartMCPOAuth(context.Context, string, string) (*CodexMCPOAuthFlow, error)
}

// codexInstalledAppsReader reads, live, the hosted apps Codex's runtime has
// installed: the apps whose account is linked, each with whether Codex can
// call it.
type codexInstalledAppsReader interface {
	ReadInstalledApps(context.Context, string) (map[string]codexInstalledCandidate, error)
}

type osCodexConnectorBackend struct{}

// ReadInstalledApps reads app/installed past Codex's cache the way discovery
// does (readInstalledCodexApps), so both see the same apps.
func (osCodexConnectorBackend) ReadInstalledApps(
	ctx context.Context,
	path string,
) (map[string]codexInstalledCandidate, error) {
	readCtx, cancel := context.WithTimeout(ctx, codexConnectorStatusTimeout)
	defer cancel()
	session, err := startCodexRPCSessionWithLimit(readCtx, path, codexConnectorMessageLimit)
	if err != nil {
		return nil, ErrCodexConnectorsUnavailable
	}
	defer session.close()
	installed, _, err := readInstalledCodexApps(readCtx, session, true, &codexConnectorWarnings{})
	if err != nil {
		return nil, ErrCodexConnectorsUnavailable
	}
	return installed, nil
}

// codexAppDirectory is one complete app/list read: Codex's hosted app
// directory, which changes rarely but is slow to list.
type codexAppDirectory struct {
	apps     map[string]codexAppCandidate
	order    []string
	warnings codexConnectorWarnings
	readAt   time.Time
}

// codexDirectoryReader reads a catalog that may reuse the previous app
// directory (readCodexAppDirectory) and answers the directory it used.
type codexDirectoryReader interface {
	ReadWithDirectory(
		context.Context,
		string,
		codexCatalogRefresh,
		*codexAppDirectory,
	) (codexConnectorCatalog, *codexAppDirectory, error)
}

func (backend osCodexConnectorBackend) Read(
	ctx context.Context,
	path string,
	refresh bool,
) (codexConnectorCatalog, error) {
	catalog, _, err := backend.ReadWithDirectory(ctx, path, codexCatalogRefreshFor(refresh), nil)
	return catalog, err
}

func (osCodexConnectorBackend) ReadWithDirectory(
	ctx context.Context,
	path string,
	refresh codexCatalogRefresh,
	previous *codexAppDirectory,
) (codexConnectorCatalog, *codexAppDirectory, error) {
	readCtx, cancel := context.WithTimeout(ctx, codexConnectorReadTimeout)
	defer cancel()
	session, err := startCodexRPCSessionWithLimit(readCtx, path, codexConnectorMessageLimit)
	if err != nil {
		logCodexConnectorDiscoveryFailure(ctx, err)
		return codexConnectorCatalog{}, nil, ErrCodexConnectorsUnavailable
	}
	defer session.close()
	catalog, directory, err := readCodexConnectorCatalogFrom(
		readCtx, session, refresh, previous, time.Now(),
	)
	if err != nil {
		logCodexConnectorDiscoveryFailure(ctx, err)
		return codexConnectorCatalog{}, nil, ErrCodexConnectorsUnavailable
	}
	return catalog, directory, nil
}

// logCodexConnectorDiscoveryFailure records why discovery failed, which the
// public ErrCodexConnectorsUnavailable hides, so a catalog that stopped
// updating (an item or page limit, a timeout) is diagnosable from the daemon
// log. The cause carries Zenith Team's own messages and Go's I/O errors, never
// Codex's response text. A cancelled daemon context is shutdown.
func logCodexConnectorDiscoveryFailure(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	slog.Warn("Codex connector discovery failed", "error", err)
}

func (osCodexConnectorBackend) StartMCPOAuth(
	ctx context.Context,
	path string,
	nativeID string,
) (*CodexMCPOAuthFlow, error) {
	flowCtx, cancel := context.WithTimeout(ctx, codexConnectorOAuthTimeout)
	session, err := startCodexRPCSessionWithLimit(flowCtx, path, codexConnectorMessageLimit)
	if err != nil {
		cancel()
		return nil, ErrCodexConnectorsUnavailable
	}
	flow, err := startCodexMCPOAuth(flowCtx, session, nativeID)
	if err != nil {
		cancel()
		return nil, err
	}
	flow.attachCancel(cancel)
	return flow, nil
}

// CodexConnectorBackend owns discovery and connect state for one Codex executable.
// The executable path and native action metadata never leave this daemon-local type.
type CodexConnectorBackend struct {
	mu        sync.RWMutex
	path      string
	transport codexConnectorBackend
	cached    *codexConnectorCatalog
	// directory is the app directory of the last successful read.
	directory     *codexAppDirectory
	oauth         map[string]*codexConnectorOAuthAttempt
	oauthWorkers  sync.WaitGroup
	changeHandler func()
	// directoryFilling marks the background listing a cold read starts
	// (fillDirectory); directoryHandler runs once that listing lands.
	directoryFilling    bool
	directoryFillDone   chan struct{}
	directoryFillCancel context.CancelFunc
	directoryFill       sync.WaitGroup
	directoryHandler    func()
	closed              bool
	// statusMu serializes live installed-app reads; installed is the last
	// one, read at installedAt.
	statusMu    sync.Mutex
	installed   map[string]codexInstalledCandidate
	installedAt time.Time
}

type codexConnectorOAuthAttempt struct {
	ready  chan struct{}
	cancel context.CancelFunc
	flow   *CodexMCPOAuthFlow
	err    error
}

func newCodexConnectorBackend(path string, transport codexConnectorBackend) *CodexConnectorBackend {
	return &CodexConnectorBackend{
		path: path, transport: transport,
		oauth: make(map[string]*codexConnectorOAuthAttempt),
	}
}

// NewOSCodexConnectorBackend creates a backend for one discovered Codex executable.
func NewOSCodexConnectorBackend(path string) *CodexConnectorBackend {
	return newCodexConnectorBackend(path, osCodexConnectorBackend{})
}

// Discover reads a complete connector catalog. force bypasses Codex's hosted app
// and installed-runtime caches before the snapshot is committed locally; an
// unforced read may reuse the last app directory (readCodexAppDirectory).
func (b *CodexConnectorBackend) Discover(
	ctx context.Context,
	force bool,
) (CodexConnectorSnapshot, error) {
	return b.discover(ctx, codexCatalogRefreshFor(force))
}

// DiscoverLinked reads a complete connector catalog with only Codex's record
// of linked apps bypassed (codexCatalogLinked): after an app connects in the
// browser, this publishes it in seconds, where a forced read would first wait
// on ChatGPT listing every hosted app.
func (b *CodexConnectorBackend) DiscoverLinked(ctx context.Context) (CodexConnectorSnapshot, error) {
	return b.discover(ctx, codexCatalogLinked)
}

func (b *CodexConnectorBackend) discover(
	ctx context.Context,
	refresh codexCatalogRefresh,
) (CodexConnectorSnapshot, error) {
	if b == nil || b.path == "" || b.transport == nil {
		return CodexConnectorSnapshot{}, ErrCodexConnectorsUnavailable
	}
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return CodexConnectorSnapshot{}, ErrCodexConnectorsUnavailable
	}
	var (
		catalog   codexConnectorCatalog
		directory *codexAppDirectory
		err       error
	)
	if reader, ok := b.transport.(codexDirectoryReader); ok {
		b.mu.RLock()
		previous := b.directory
		b.mu.RUnlock()
		// With no directory yet (every daemon start), listing it is ChatGPT
		// returning all of its hosted apps, about 30 s and often a 500 at
		// its own 30 s timeout. A Run needs only the linked apps, which
		// app/installed answers in milliseconds, so a read that is not
		// forced stands an empty directory in for the listing and the
		// listing runs in the background; once it lands, directoryHandler
		// asks for the full catalog.
		var installedOnly *codexAppDirectory
		if previous == nil && refresh != codexCatalogForced {
			installedOnly = &codexAppDirectory{readAt: time.Now()}
			previous = installedOnly
			b.fillDirectory(reader)
		}
		catalog, directory, err = reader.ReadWithDirectory(ctx, b.path, refresh, previous)
		if directory == installedOnly {
			directory = nil
		}
	} else {
		catalog, err = b.transport.Read(ctx, b.path, refresh != codexCatalogCached)
	}
	if err != nil {
		return CodexConnectorSnapshot{}, mapCodexConnectorError(err)
	}
	b.mu.Lock()
	cached := cloneCodexConnectorCatalog(catalog)
	b.cached = &cached
	if directory != nil {
		b.directory = directory
	}
	b.mu.Unlock()
	return snapshotFromCodexConnectorCatalog(catalog), nil
}

// HostedAppConnected reports whether the hosted app nativeID is connected
// now by discovery's own rule (codexInstalledCandidate.connected), from a
// live app/installed read that bypasses Codex's cache; checks within
// codexConnectorStatusReuse share one read. It is how the desktop notices an
// app connected in the browser, since the connect page has no callback into
// Zenith Team. A linked app Codex cannot call yet is not connected here, as
// discovery publishes it installed_not_ready.
func (b *CodexConnectorBackend) HostedAppConnected(
	ctx context.Context,
	nativeID string,
) (bool, error) {
	if b == nil || b.path == "" || !validCodexConnectorNativeID(nativeID) {
		return false, ErrCodexConnectorsUnavailable
	}
	reader, ok := b.transport.(codexInstalledAppsReader)
	if !ok {
		return false, ErrCodexConnectorsUnavailable
	}
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return false, ErrCodexConnectorsUnavailable
	}
	if b.installed == nil || time.Since(b.installedAt) >= codexConnectorStatusReuse {
		installed, err := reader.ReadInstalledApps(ctx, b.path)
		if err != nil {
			return false, mapCodexConnectorError(err)
		}
		b.installed, b.installedAt = installed, time.Now()
	}
	app, linked := b.installed[nativeID]
	return linked && app.connected(), nil
}

// Connect starts only the native action previously proven by Discover. It never
// opens a browser and returns no provider configuration or credentials.
func (b *CodexConnectorBackend) Connect(
	ctx context.Context,
	source CodexConnectorSource,
	nativeID string,
) (CodexConnectorConnectResult, error) {
	if !validCodexConnectorNativeID(nativeID) {
		return CodexConnectorConnectResult{}, ErrCodexConnectorNotFound
	}
	b.mu.RLock()
	cold := b.directory == nil
	b.mu.RUnlock()
	catalog, err := b.cachedOrDiscover(ctx)
	if err != nil {
		return CodexConnectorConnectResult{}, err
	}
	action, exists := catalog.actions[nativeID]
	// Before the directory lands a catalog holds only the linked apps, and an
	// app still to connect, with the URL that connects it, is only in the
	// directory.
	if !exists && cold && b.awaitDirectory(ctx) {
		// discover caches the catalog it read on the landed directory.
		if _, err := b.discover(ctx, codexCatalogCached); err != nil {
			return CodexConnectorConnectResult{}, err
		}
		if catalog, err = b.cachedOrDiscover(ctx); err != nil {
			return CodexConnectorConnectResult{}, err
		}
		action, exists = catalog.actions[nativeID]
	}
	if !exists {
		return CodexConnectorConnectResult{}, ErrCodexConnectorNotFound
	}
	if action.source != source || action.status == CodexConnectorStatusConnected {
		return CodexConnectorConnectResult{}, ErrCodexConnectorNotConnectable
	}
	result := CodexConnectorConnectResult{
		Source: source, NativeID: nativeID, Status: action.status,
	}
	switch source {
	case CodexConnectorSourceHostedApp:
		result.AuthorizationURL = codexHostedConnectorSetupURL(action.connectURL, nativeID)
		if result.AuthorizationURL == "" {
			return CodexConnectorConnectResult{}, ErrCodexConnectorNotConnectable
		}
		return result, nil
	case CodexConnectorSourceLocalMCP:
		if !action.oauth {
			return CodexConnectorConnectResult{}, ErrCodexConnectorNotConnectable
		}
		flow, err := b.startOrReuseMCPOAuth(ctx, nativeID)
		if err != nil {
			return CodexConnectorConnectResult{}, mapCodexConnectorError(err)
		}
		if flow == nil {
			return CodexConnectorConnectResult{}, ErrCodexConnectorsUnavailable
		}
		result.AuthorizationURL = validatedCodexOAuthURL(flow.AuthorizationURL)
		if result.AuthorizationURL == "" {
			flow.Close()
			return CodexConnectorConnectResult{}, ErrCodexConnectorNotConnectable
		}
		return result, nil
	default:
		return CodexConnectorConnectResult{}, ErrCodexConnectorNotConnectable
	}
}

func (b *CodexConnectorBackend) startOrReuseMCPOAuth(
	ctx context.Context,
	nativeID string,
) (*CodexMCPOAuthFlow, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrCodexConnectorsUnavailable
	}
	if attempt, exists := b.oauth[nativeID]; exists {
		b.mu.Unlock()
		return b.waitForMCPOAuthStart(ctx, attempt)
	}
	if len(b.oauth) >= codexConnectorMaximumOAuthFlows {
		b.mu.Unlock()
		return nil, ErrCodexConnectorsUnavailable
	}
	flowCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	attempt := &codexConnectorOAuthAttempt{ready: make(chan struct{}), cancel: cancel}
	b.oauth[nativeID] = attempt
	b.oauthWorkers.Add(1)
	b.mu.Unlock()

	flow, err := b.transport.StartMCPOAuth(flowCtx, b.path, nativeID)
	b.mu.Lock()
	if err != nil || flow == nil || flow.done == nil || b.closed {
		if err == nil {
			err = ErrCodexConnectorsUnavailable
		}
		attempt.err = err
		if b.oauth[nativeID] == attempt {
			delete(b.oauth, nativeID)
		}
		close(attempt.ready)
		b.mu.Unlock()
		cancel()
		if flow != nil {
			flow.Close()
		}
		b.oauthWorkers.Done()
		return nil, err
	}
	attempt.flow = flow
	close(attempt.ready)
	b.mu.Unlock()
	go b.observeMCPOAuth(nativeID, attempt)
	return flow, nil
}

func (b *CodexConnectorBackend) waitForMCPOAuthStart(
	ctx context.Context,
	attempt *codexConnectorOAuthAttempt,
) (*CodexMCPOAuthFlow, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-attempt.ready:
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if attempt.err != nil {
		return nil, attempt.err
	}
	if attempt.flow == nil || b.closed {
		return nil, ErrCodexConnectorsUnavailable
	}
	return attempt.flow, nil
}

func (b *CodexConnectorBackend) observeMCPOAuth(
	nativeID string,
	attempt *codexConnectorOAuthAttempt,
) {
	defer b.oauthWorkers.Done()
	<-attempt.flow.done
	attempt.cancel()
	attempt.flow.mu.RLock()
	status := attempt.flow.result.Status
	attempt.flow.mu.RUnlock()

	b.mu.Lock()
	if b.oauth[nativeID] == attempt {
		delete(b.oauth, nativeID)
	}
	b.cached = nil
	handler := b.changeHandler
	closed := b.closed
	b.mu.Unlock()
	if !closed && status != CodexMCPOAuthStatusCancelled && handler != nil {
		handler()
	}
}

// fillDirectory lists the app directory in the background unless a listing
// is already running. A failed listing is only logged (ReadWithDirectory);
// the next cold read starts another.
func (b *CodexConnectorBackend) fillDirectory(reader codexDirectoryReader) {
	b.mu.Lock()
	if b.closed || b.directoryFilling {
		b.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	b.directoryFilling, b.directoryFillCancel, b.directoryFillDone = true, cancel, done
	b.directoryFill.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.directoryFill.Done()
		defer close(done)
		defer cancel()
		_, directory, err := reader.ReadWithDirectory(ctx, b.path, codexCatalogCached, nil)
		b.mu.Lock()
		b.directoryFilling, b.directoryFillCancel, b.directoryFillDone = false, nil, nil
		if err != nil || directory == nil || b.closed {
			b.mu.Unlock()
			return
		}
		// A forced read may have landed a directory meanwhile; keep it.
		if b.directory == nil {
			b.directory = directory
		}
		handler := b.directoryHandler
		b.mu.Unlock()
		if handler != nil {
			handler()
		}
	}()
}

// awaitDirectory waits for the app directory, starting its background
// listing when none runs, and reports whether one is now held.
func (b *CodexConnectorBackend) awaitDirectory(ctx context.Context) bool {
	reader, ok := b.transport.(codexDirectoryReader)
	if !ok {
		return false
	}
	b.fillDirectory(reader)
	b.mu.RLock()
	done, held := b.directoryFillDone, b.directory != nil
	b.mu.RUnlock()
	if !held && done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return false
		}
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.directory != nil
}

// SetDirectoryHandler replaces the callback invoked when a background
// directory listing lands, so the full catalog can be published. The
// callback must return promptly.
func (b *CodexConnectorBackend) SetDirectoryHandler(handler func()) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if !b.closed {
		b.directoryHandler = handler
	}
	b.mu.Unlock()
}

// SetChangeHandler replaces the callback invoked after a native OAuth attempt
// reaches a terminal provider state. The callback must return promptly.
func (b *CodexConnectorBackend) SetChangeHandler(handler func()) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if !b.closed {
		b.changeHandler = handler
	}
	b.mu.Unlock()
}

// Close cancels and waits for every app-server process owned by this backend.
func (b *CodexConnectorBackend) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.closed = true
	b.changeHandler = nil
	b.directoryHandler = nil
	if b.directoryFillCancel != nil {
		b.directoryFillCancel()
	}
	attempts := make([]*codexConnectorOAuthAttempt, 0, len(b.oauth))
	for _, attempt := range b.oauth {
		attempts = append(attempts, attempt)
	}
	b.mu.Unlock()
	for _, attempt := range attempts {
		attempt.cancel()
		<-attempt.ready
		if attempt.flow != nil {
			attempt.flow.Close()
		}
	}
	b.oauthWorkers.Wait()
	b.directoryFill.Wait()
}

func (b *CodexConnectorBackend) cachedOrDiscover(
	ctx context.Context,
) (codexConnectorCatalog, error) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return codexConnectorCatalog{}, ErrCodexConnectorsUnavailable
	}
	if b.cached != nil {
		catalog := cloneCodexConnectorCatalog(*b.cached)
		b.mu.RUnlock()
		return catalog, nil
	}
	b.mu.RUnlock()
	if _, err := b.Discover(ctx, false); err != nil {
		return codexConnectorCatalog{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.cached == nil {
		return codexConnectorCatalog{}, ErrCodexConnectorsUnavailable
	}
	return cloneCodexConnectorCatalog(*b.cached), nil
}

func mapCodexConnectorError(err error) error {
	switch {
	case errors.Is(err, ErrCodexConnectorNotFound):
		return ErrCodexConnectorNotFound
	case errors.Is(err, ErrCodexConnectorNotConnectable):
		return ErrCodexConnectorNotConnectable
	default:
		return ErrCodexConnectorsUnavailable
	}
}

func cloneCodexConnectorCatalog(source codexConnectorCatalog) codexConnectorCatalog {
	cloned := codexConnectorCatalog{
		connectors: cloneCodexConnectors(source.connectors),
		actions:    make(map[string]codexConnectorAction, len(source.actions)),
		warnings:   source.warnings,
	}
	for key, action := range source.actions {
		cloned.actions[key] = action
	}
	return cloned
}

func snapshotFromCodexConnectorCatalog(catalog codexConnectorCatalog) CodexConnectorSnapshot {
	return CodexConnectorSnapshot{
		SchemaVersion: 1,
		Connectors:    cloneCodexConnectors(catalog.connectors),
	}
}

func cloneCodexConnectors(source []CodexConnector) []CodexConnector {
	cloned := append([]CodexConnector(nil), source...)
	for index := range cloned {
		cloned[index].Tools = append([]agent.ConnectorTool(nil), source[index].Tools...)
	}
	return cloned
}

type codexAppPage struct {
	Data []struct {
		ID             string            `json:"id"`
		Name           string            `json:"name"`
		Description    string            `json:"description"`
		LogoURL        string            `json:"logoUrl"`
		LogoURLDark    string            `json:"logoUrlDark"`
		IconAssets     map[string]string `json:"iconAssets"`
		IconDarkAssets map[string]string `json:"iconDarkAssets"`
		AppMetadata    *codexAppMetadata `json:"appMetadata"`
		InstallURL     string            `json:"installUrl"`
		IsAccessible   bool              `json:"isAccessible"`
		IsEnabled      bool              `json:"isEnabled"`
	} `json:"data"`
	NextCursor string `json:"nextCursor"`
}

type codexAppMetadata struct {
	Categories []string `json:"categories"`
}

type codexInstalledApps struct {
	Apps []struct {
		ID          string `json:"id"`
		RuntimeName string `json:"runtimeName"`
		Enabled     bool   `json:"enabled"`
		Callable    bool   `json:"callable"`
	} `json:"apps"`
}

type codexAppsReadResponse struct {
	Apps []struct {
		ID            string                 `json:"id"`
		ToolSummaries *[]codexAppToolSummary `json:"toolSummaries"`
	} `json:"apps"`
	MissingAppIDs []string `json:"missingAppIds"`
}

type codexAppToolSummary struct {
	Name       string `json:"name"`
	IsEnabled  *bool  `json:"isEnabled"`
	IsReadOnly *bool  `json:"isReadOnly"`
}

type codexMCPPage struct {
	Data []struct {
		Name       string `json:"name"`
		PluginID   string `json:"pluginId"`
		AuthStatus string `json:"authStatus"`
		ServerInfo *struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"serverInfo"`
		Tools map[string]json.RawMessage `json:"tools"`
	} `json:"data"`
	NextCursor string `json:"nextCursor"`
}

type codexAppCandidate struct {
	connector  CodexConnector
	accessible bool
	enabled    bool
	installURL string
}

type codexInstalledCandidate struct {
	name     string
	enabled  bool
	callable bool
}

// connected is when a linked hosted app counts as connected, in discovery
// and in the live check alike: Codex can call it. A linked app it cannot
// call yet is installed_not_ready.
func (c codexInstalledCandidate) connected() bool {
	return c.callable
}

type codexAppToolContract struct {
	digest string
	tools  []agent.ConnectorTool
}

type codexMCPCandidate struct {
	connector CodexConnector
	auth      string
	callable  bool
}

func readCodexConnectorCatalog(
	ctx context.Context,
	session codexConnectorRPC,
	refresh bool,
) (codexConnectorCatalog, error) {
	catalog, _, err := readCodexConnectorCatalogFrom(
		ctx, session, codexCatalogRefreshFor(refresh), nil, time.Now(),
	)
	return catalog, err
}

// readCodexConnectorCatalogFrom reads the catalog on the app directory
// readCodexAppDirectory chooses, and answers that directory too. The
// installed apps, their tools and the MCP servers are read on every call,
// the installed apps past Codex's cache unless the read is cached.
func readCodexConnectorCatalogFrom(
	ctx context.Context,
	session codexConnectorRPC,
	refresh codexCatalogRefresh,
	previous *codexAppDirectory,
	now time.Time,
) (codexConnectorCatalog, *codexAppDirectory, error) {
	directory, err := readCodexAppDirectory(ctx, session, refresh == codexCatalogForced, previous, now)
	if err != nil {
		return codexConnectorCatalog{}, nil, err
	}
	catalog, err := buildCodexConnectorCatalog(ctx, session, refresh != codexCatalogCached, directory)
	if err != nil {
		return codexConnectorCatalog{}, nil, err
	}
	return catalog, directory, nil
}

// readCodexAppDirectory lists Codex's hosted apps unless a read that is not
// forced can reuse the previous directory, and falls back to that directory
// when the listing fails (codexAppDirectoryReuse).
func readCodexAppDirectory(
	ctx context.Context,
	session codexConnectorRPC,
	refresh bool,
	previous *codexAppDirectory,
	now time.Time,
) (*codexAppDirectory, error) {
	usable := previous != nil && now.Sub(previous.readAt) < codexAppDirectoryFallback
	if !refresh && usable && now.Sub(previous.readAt) < codexAppDirectoryReuse {
		return previous, nil
	}
	listCtx := ctx
	if usable {
		var cancel context.CancelFunc
		listCtx, cancel = context.WithTimeout(ctx, codexAppDirectoryBudget)
		defer cancel()
	}
	apps, order, warnings, err := readCodexApps(listCtx, session, refresh)
	if err == nil {
		return &codexAppDirectory{apps: apps, order: order, warnings: warnings, readAt: now}, nil
	}
	if !usable || ctx.Err() != nil {
		return nil, err
	}
	slog.Warn(
		"Codex app directory read failed; reusing the previous one",
		"error", err, "age", now.Sub(previous.readAt).Round(time.Second),
	)
	return previous, nil
}

func buildCodexConnectorCatalog(
	ctx context.Context,
	session codexConnectorRPC,
	refresh bool,
	directory *codexAppDirectory,
) (codexConnectorCatalog, error) {
	apps, appOrder, warnings := directory.apps, directory.order, directory.warnings
	installed, installedOrder, err := readInstalledCodexApps(ctx, session, refresh, &warnings)
	if err != nil {
		return codexConnectorCatalog{}, err
	}
	appContracts := readCodexAppToolContracts(
		ctx,
		session,
		installed,
		installedOrder,
		&warnings,
	)
	mcpServers, mcpOrder, err := readCodexMCPServers(ctx, session, &warnings)
	if err != nil {
		return codexConnectorCatalog{}, err
	}
	notifications, err := settleCodexConnectorNotifications(ctx, session)
	if err != nil {
		return codexConnectorCatalog{}, err
	}
	reauthentication := codexReauthenticationNotifications(notifications, &warnings)

	catalog := codexConnectorCatalog{actions: make(map[string]codexConnectorAction), warnings: warnings}
	seen := make(map[string]codexConnectorAction)
	appendConnector := func(connector CodexConnector, action codexConnectorAction) bool {
		action.status = connector.Status
		connector.Action = publicCodexConnectorAction(action)
		connector.CanExecute = false
		if connector.Source == CodexConnectorSourceHostedApp &&
			connector.Status == CodexConnectorStatusConnected {
			digest, digestErr := agent.ConnectorToolContractDigest(connector.Tools)
			connector.CanExecute = digestErr == nil && digest == connector.ContractDigest
		}
		if !connector.CanExecute {
			connector.ContractDigest = ""
			connector.Tools = nil
		}
		if previous, exists := seen[connector.NativeID]; exists {
			catalog.warnings.addDuplicate(previous != action)
			return true
		}
		if len(catalog.connectors) >= codexConnectorMaximumItems {
			return false
		}
		seen[connector.NativeID] = action
		catalog.connectors = append(catalog.connectors, connector)
		catalog.actions[connector.NativeID] = action
		return true
	}

	for _, id := range appOrder {
		app := apps[id]
		// An app in the live installed snapshot is linked, even when a reused
		// directory does not say it is accessible yet.
		installedState, linked := installed[id]
		switch {
		case installedState.connected():
			app.connector.Status = CodexConnectorStatusConnected
			if contract, exists := appContracts[id]; exists {
				app.connector.ContractDigest = contract.digest
				app.connector.Tools = append([]agent.ConnectorTool(nil), contract.tools...)
			}
		case app.accessible || linked:
			app.connector.Status = CodexConnectorStatusInstalledNotReady
		case app.installURL != "":
			app.connector.Status = CodexConnectorStatusAvailable
		default:
			catalog.warnings.addSkipped()
			continue
		}
		if !appendConnector(app.connector, codexConnectorAction{
			source: CodexConnectorSourceHostedApp, connectURL: app.installURL,
		}) {
			return codexConnectorCatalog{}, errors.New("Codex connector catalog exceeds item limit")
		}
	}
	for _, id := range installedOrder {
		if _, exists := apps[id]; exists {
			continue
		}
		state := installed[id]
		status := CodexConnectorStatusInstalledNotReady
		if state.connected() {
			status = CodexConnectorStatusConnected
		}
		connector := CodexConnector{
			Source: CodexConnectorSourceHostedApp, NativeID: id,
			Name: state.name, Status: status,
		}
		if contract, exists := appContracts[id]; exists {
			connector.ContractDigest = contract.digest
			connector.Tools = append([]agent.ConnectorTool(nil), contract.tools...)
		}
		if !appendConnector(connector, codexConnectorAction{source: CodexConnectorSourceHostedApp}) {
			return codexConnectorCatalog{}, errors.New("Codex connector catalog exceeds item limit")
		}
	}
	for _, id := range mcpOrder {
		server := mcpServers[id]
		status := CodexConnectorStatusInstalledNotReady
		if server.callable {
			status = CodexConnectorStatusConnected
		} else if reauthentication[id] {
			status = CodexConnectorStatusNeedsAuth
		}
		server.connector.Status = status
		if !appendConnector(server.connector, codexConnectorAction{
			source: CodexConnectorSourceLocalMCP,
			oauth:  server.auth == "notLoggedIn" || server.auth == "oAuth" || reauthentication[id],
		}) {
			return codexConnectorCatalog{}, errors.New("Codex connector catalog exceeds item limit")
		}
	}
	sort.SliceStable(catalog.connectors, func(left, right int) bool {
		if catalog.connectors[left].Source != catalog.connectors[right].Source {
			return catalog.connectors[left].Source < catalog.connectors[right].Source
		}
		leftName := strings.ToLower(catalog.connectors[left].Name)
		rightName := strings.ToLower(catalog.connectors[right].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return catalog.connectors[left].NativeID < catalog.connectors[right].NativeID
	})
	return catalog, nil
}

func settleCodexConnectorNotifications(
	ctx context.Context,
	session codexConnectorRPC,
) ([]codexRPCNotification, error) {
	notifications, overflow := session.drainNotifications()
	if overflow {
		return nil, errors.New("Codex connector notification limit exceeded")
	}
	settleCtx, cancel := context.WithTimeout(ctx, codexConnectorNotificationSettle)
	defer cancel()
	for len(notifications) < codexRPCNotificationLimit {
		notification, err := session.nextNotification(settleCtx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if settleCtx.Err() != nil {
				return notifications, nil
			}
			return nil, err
		}
		notifications = append(notifications, notification)
	}
	return nil, errors.New("Codex connector notification limit exceeded")
}

func publicCodexConnectorAction(action codexConnectorAction) CodexConnectorAction {
	if action.status == CodexConnectorStatusConnected {
		return CodexConnectorActionNone
	}
	switch action.source {
	case CodexConnectorSourceHostedApp:
		if action.connectURL == "" {
			return CodexConnectorActionNone
		}
		if action.status == CodexConnectorStatusNeedsAuth {
			return CodexConnectorActionAuthenticate
		}
		return CodexConnectorActionInstallAndAuth
	case CodexConnectorSourceLocalMCP:
		if action.oauth {
			return CodexConnectorActionAuthenticate
		}
		return CodexConnectorActionNone
	default:
		return CodexConnectorActionNone
	}
}

// requestCodexConnectorPage asks for one catalog page and retries it when
// the app-server answers with an error. A managed-configuration refusal, a
// broken session or the end of ctx is final.
func requestCodexConnectorPage(
	ctx context.Context,
	session codexConnectorRPC,
	method string,
	params map[string]any,
) (json.RawMessage, error) {
	delays := []time.Duration{codexConnectorFirstRetryDelay, codexConnectorSecondRetryDelay}
	for attempt := 0; ; attempt++ {
		raw, err := session.request(ctx, method, params)
		var rpcErr *codexRPCError
		if err == nil || attempt == len(delays) || !errors.As(err, &rpcErr) {
			return raw, err
		}
		slog.Info("Codex connector page retried", "method", method, "error", err)
		timer := time.NewTimer(delays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, err
		case <-timer.C:
		}
	}
}

func readCodexApps(
	ctx context.Context,
	session codexConnectorRPC,
	refresh bool,
) (map[string]codexAppCandidate, []string, codexConnectorWarnings, error) {
	apps := make(map[string]codexAppCandidate)
	order := make([]string, 0)
	warnings := codexConnectorWarnings{}
	cursor := ""
	seenCursors := make(map[string]struct{})
	for page := range codexConnectorMaximumPages {
		params := map[string]any{
			"cursor": cursorValue(cursor), "limit": codexConnectorPageSize,
			"forceRefetch": refresh && cursor == "",
		}
		raw, err := requestCodexConnectorPage(ctx, session, "app/list", params)
		if err != nil {
			return nil, nil, warnings, fmt.Errorf("read Codex app catalog page %d: %w", page+1, err)
		}
		var appPage codexAppPage
		if json.Unmarshal(raw, &appPage) != nil {
			return nil, nil, warnings, errors.New("decode Codex app catalog")
		}
		for _, rawApp := range appPage.Data {
			if !validCodexConnectorNativeID(rawApp.ID) {
				warnings.addSkipped()
				continue
			}
			name := sanitizeCodexConnectorText(rawApp.Name, codexConnectorMaximumName)
			if name == "" {
				warnings.addSkipped()
				continue
			}
			installURL := ""
			if rawApp.InstallURL != "" {
				installURL = validatedHostedConnectURL(rawApp.InstallURL)
				if installURL == "" {
					warnings.addSkipped()
					continue
				}
			}
			candidate := codexAppCandidate{
				connector: CodexConnector{
					Source: CodexConnectorSourceHostedApp, NativeID: rawApp.ID, Name: name,
					Description: sanitizeCodexConnectorText(
						rawApp.Description,
						codexConnectorMaximumDescription,
					),
					Category: codexAppCategory(rawApp.AppMetadata),
					IconURL: codexAppIconURL(
						rawApp.IconAssets,
						rawApp.IconDarkAssets,
						rawApp.LogoURL,
						rawApp.LogoURLDark,
					),
				},
				accessible: rawApp.IsAccessible, enabled: rawApp.IsEnabled, installURL: installURL,
			}
			if previous, exists := apps[rawApp.ID]; exists {
				warnings.addDuplicate(
					previous.accessible != candidate.accessible ||
						previous.enabled != candidate.enabled || previous.installURL != candidate.installURL,
				)
				continue
			}
			apps[rawApp.ID] = candidate
			order = append(order, rawApp.ID)
		}
		next, err := nextCodexConnectorCursor(appPage.NextCursor, seenCursors)
		if err != nil {
			return nil, nil, warnings, err
		}
		if next == "" {
			return apps, order, warnings, nil
		}
		cursor = next
	}
	return nil, nil, warnings, errors.New("Codex app catalog exceeds page limit")
}

func readInstalledCodexApps(
	ctx context.Context,
	session codexConnectorRPC,
	refresh bool,
	warnings *codexConnectorWarnings,
) (map[string]codexInstalledCandidate, []string, error) {
	raw, err := requestCodexConnectorPage(
		ctx, session, "app/installed", map[string]any{"forceRefresh": refresh},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("read installed Codex apps: %w", err)
	}
	var response codexInstalledApps
	if json.Unmarshal(raw, &response) != nil {
		return nil, nil, errors.New("decode installed Codex apps")
	}
	installed := make(map[string]codexInstalledCandidate, len(response.Apps))
	order := make([]string, 0, len(response.Apps))
	for _, rawApp := range response.Apps {
		if !validCodexConnectorNativeID(rawApp.ID) {
			warnings.addSkipped()
			continue
		}
		name := sanitizeCodexConnectorText(rawApp.RuntimeName, codexConnectorMaximumName)
		if name == "" {
			name = rawApp.ID
		}
		candidate := codexInstalledCandidate{
			name: name, enabled: rawApp.Enabled, callable: rawApp.Callable,
		}
		if previous, exists := installed[rawApp.ID]; exists {
			warnings.addDuplicate(
				previous.enabled != candidate.enabled || previous.callable != candidate.callable,
			)
			continue
		}
		installed[rawApp.ID] = candidate
		order = append(order, rawApp.ID)
	}
	return installed, order, nil
}

func readCodexAppToolContracts(
	ctx context.Context,
	session codexConnectorRPC,
	installed map[string]codexInstalledCandidate,
	installedOrder []string,
	warnings *codexConnectorWarnings,
) map[string]codexAppToolContract {
	callableIDs := make([]string, 0, len(installedOrder))
	for _, id := range installedOrder {
		if installed[id].callable {
			callableIDs = append(callableIDs, id)
		}
	}
	contracts := make(map[string]codexAppToolContract, len(callableIDs))
	for start := 0; start < len(callableIDs); start += codexConnectorAppReadBatchSize {
		end := min(start+codexConnectorAppReadBatchSize, len(callableIDs))
		batch := callableIDs[start:end]
		raw, err := session.request(ctx, "app/read", map[string]any{
			"appIds": append([]string(nil), batch...), "includeTools": true,
		})
		if err != nil {
			addCodexContractWarnings(warnings, len(batch))
			continue
		}
		var response codexAppsReadResponse
		if json.Unmarshal(raw, &response) != nil {
			addCodexContractWarnings(warnings, len(batch))
			continue
		}
		readCodexAppToolContractBatch(batch, response, contracts, warnings)
	}
	return contracts
}

func readCodexAppToolContractBatch(
	requested []string,
	response codexAppsReadResponse,
	contracts map[string]codexAppToolContract,
	warnings *codexConnectorWarnings,
) {
	expected := make(map[string]struct{}, len(requested))
	for _, id := range requested {
		expected[id] = struct{}{}
	}
	missing := make(map[string]struct{}, len(response.MissingAppIDs))
	for _, id := range response.MissingAppIDs {
		if _, exists := expected[id]; exists {
			missing[id] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(requested))
	invalid := make(map[string]struct{})
	batchContracts := make(map[string]codexAppToolContract, len(requested))
	for _, app := range response.Apps {
		if _, exists := expected[app.ID]; !exists {
			continue
		}
		if _, duplicate := seen[app.ID]; duplicate {
			invalid[app.ID] = struct{}{}
			delete(batchContracts, app.ID)
			continue
		}
		seen[app.ID] = struct{}{}
		contract, valid := codexAppToolContractFromSummaries(app.ToolSummaries)
		if _, absent := missing[app.ID]; absent || !valid {
			invalid[app.ID] = struct{}{}
			continue
		}
		batchContracts[app.ID] = contract
	}
	for _, id := range requested {
		_, observed := seen[id]
		_, absent := missing[id]
		_, rejected := invalid[id]
		contract, valid := batchContracts[id]
		if !observed || absent || rejected || !valid {
			addCodexContractWarnings(warnings, 1)
			continue
		}
		contracts[id] = contract
	}
}

func codexAppToolContractFromSummaries(
	summaries *[]codexAppToolSummary,
) (codexAppToolContract, bool) {
	if summaries == nil || len(*summaries) == 0 ||
		len(*summaries) > codexConnectorMaximumAppTools {
		return codexAppToolContract{}, false
	}
	tools := make([]agent.ConnectorTool, len(*summaries))
	for index, summary := range *summaries {
		if summary.IsEnabled == nil || summary.IsReadOnly == nil {
			return codexAppToolContract{}, false
		}
		tools[index] = agent.ConnectorTool{
			Name: summary.Name, Enabled: *summary.IsEnabled, ReadOnly: *summary.IsReadOnly,
		}
	}
	sort.Slice(tools, func(left, right int) bool {
		return tools[left].Name < tools[right].Name
	})
	digest, err := agent.ConnectorToolContractDigest(tools)
	if err != nil {
		return codexAppToolContract{}, false
	}
	return codexAppToolContract{digest: digest, tools: tools}, true
}

func addCodexContractWarnings(warnings *codexConnectorWarnings, count int) {
	if warnings == nil {
		return
	}
	for range count {
		warnings.addSkipped()
	}
}

func readCodexMCPServers(
	ctx context.Context,
	session codexConnectorRPC,
	warnings *codexConnectorWarnings,
) (map[string]codexMCPCandidate, []string, error) {
	servers := make(map[string]codexMCPCandidate)
	order := make([]string, 0)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for range codexConnectorMaximumPages {
		raw, err := requestCodexConnectorPage(ctx, session, "mcpServerStatus/list", map[string]any{
			"cursor": cursorValue(cursor), "limit": codexConnectorMCPPageSize,
			"detail": "toolsAndAuthOnly",
		})
		if err != nil {
			return nil, nil, fmt.Errorf("read Codex MCP status: %w", err)
		}
		var page codexMCPPage
		if json.Unmarshal(raw, &page) != nil {
			return nil, nil, errors.New("decode Codex MCP status")
		}
		for _, rawServer := range page.Data {
			if !validCodexConnectorNativeID(rawServer.Name) {
				warnings.addSkipped()
				continue
			}
			name, description := rawServer.Name, ""
			if rawServer.ServerInfo != nil {
				name = rawServer.ServerInfo.Title
				if name == "" {
					name = rawServer.ServerInfo.Name
				}
				description = rawServer.ServerInfo.Description
			}
			name = sanitizeCodexConnectorText(name, codexConnectorMaximumName)
			if name == "" {
				name = rawServer.Name
			}
			candidate := codexMCPCandidate{
				connector: CodexConnector{
					Source: CodexConnectorSourceLocalMCP, NativeID: rawServer.Name, Name: name,
					Description: sanitizeCodexConnectorText(
						description,
						codexConnectorMaximumDescription,
					),
				},
				auth: rawServer.AuthStatus, callable: len(rawServer.Tools) > 0,
			}
			if previous, exists := servers[rawServer.Name]; exists {
				warnings.addDuplicate(
					previous.auth != candidate.auth || previous.callable != candidate.callable,
				)
				continue
			}
			servers[rawServer.Name] = candidate
			order = append(order, rawServer.Name)
		}
		next, err := nextCodexConnectorCursor(page.NextCursor, seenCursors)
		if err != nil {
			return nil, nil, err
		}
		if next == "" {
			return servers, order, nil
		}
		cursor = next
	}
	return nil, nil, errors.New("Codex MCP catalog exceeds page limit")
}

func nextCodexConnectorCursor(value string, seen map[string]struct{}) (string, error) {
	if value == "" {
		return "", nil
	}
	if !validCodexConnectorOpaqueValue(value, 2048) {
		return "", errors.New("Codex connector cursor is invalid")
	}
	if _, exists := seen[value]; exists {
		return "", errors.New("Codex connector cursor repeated")
	}
	seen[value] = struct{}{}
	return value, nil
}

func cursorValue(cursor string) any {
	if cursor == "" {
		return nil
	}
	return cursor
}

func codexReauthenticationNotifications(
	notifications []codexRPCNotification,
	warnings *codexConnectorWarnings,
) map[string]bool {
	result := make(map[string]bool)
	for _, notification := range notifications {
		if notification.Method != "mcpServer/startupStatus/updated" {
			continue
		}
		var update struct {
			Name          string `json:"name"`
			FailureReason string `json:"failureReason"`
		}
		if json.Unmarshal(notification.Params, &update) != nil ||
			!validCodexConnectorNativeID(update.Name) {
			warnings.addSkipped()
			continue
		}
		if update.FailureReason == "reauthenticationRequired" {
			result[update.Name] = true
		}
	}
	return result
}

func validCodexConnectorNativeID(value string) bool {
	if !validCodexConnectorOpaqueValue(value, codexConnectorMaximumNativeID) ||
		value != strings.TrimSpace(value) {
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

func validCodexConnectorOpaqueValue(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value)
}

func sanitizeCodexConnectorText(value string, maximum int) string {
	if !utf8.ValidString(value) {
		return ""
	}
	fields := strings.FieldsFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	})
	value = strings.Join(fields, " ")
	if value == "" {
		return ""
	}
	if len(value) > maximum {
		end := 0
		for offset, character := range value {
			next := offset + utf8.RuneLen(character)
			if next > maximum {
				break
			}
			end = next
		}
		// A cut just after a space would leave it trailing; the server trims
		// text before recomputing the manifest digest, so it must match.
		value = strings.TrimRight(value[:end], " ")
	}
	return value
}

func validatedHostedConnectURL(value string) string {
	if len(value) == 0 || len(value) > 4096 || !utf8.ValidString(value) {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "chatgpt.com") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" ||
		(parsed.EscapedPath() != "/apps" && !strings.HasPrefix(parsed.EscapedPath(), "/apps/")) {
		return ""
	}
	parsed.Scheme = "https"
	parsed.Host = "chatgpt.com"
	return parsed.String()
}

func codexHostedConnectorSetupURL(value, nativeID string) string {
	connectURL := validatedHostedConnectURL(value)
	if connectURL == "" || !validCodexConnectorNativeID(nativeID) {
		return ""
	}
	parsed, err := url.Parse(connectURL)
	if err != nil {
		return ""
	}
	query := url.Values{
		"add-connector-link": {"true"},
		"connector":          {nativeID},
		"product-sku":        {"CODEX"},
		"referrer":           {"codex"},
	}
	parsed.Fragment = "settings/Connectors?" + query.Encode()
	return parsed.String()
}

func codexAppCategory(metadata *codexAppMetadata) string {
	if metadata == nil {
		return ""
	}
	for _, category := range metadata.Categories {
		switch category {
		case "BUSINESS":
			return "Business"
		case "COLLABORATION":
			return "Collaboration"
		case "DESIGN":
			return "Design"
		case "DEVELOPER_TOOLS":
			return "Developer tools"
		case "EDUCATION":
			return "Education"
		case "ENTERTAINMENT":
			return "Entertainment"
		case "FINANCE":
			return "Finance"
		case "FOOD":
			return "Food"
		case "LIFESTYLE":
			return "Lifestyle"
		case "NEWS":
			return "News"
		case "PRODUCTIVITY":
			return "Productivity"
		case "SHOPPING":
			return "Shopping"
		case "TRAVEL":
			return "Travel"
		}
	}
	return ""
}

// codexAppIconURL picks an app's logo from its icon assets and logo URLs, in
// the order below, preferring one that loads outside a ChatGPT session: some
// apps carry a chatgpt.com light asset and a files.openai.com dark one.
func codexAppIconURL(
	iconAssets map[string]string,
	iconDarkAssets map[string]string,
	logoURL string,
	logoURLDark string,
) string {
	return loadableFirstCodexIconURL(
		iconAssets["256_square"],
		iconDarkAssets["256_square"],
		iconAssets["original"],
		iconDarkAssets["original"],
		iconAssets["256_circle"],
		iconDarkAssets["256_circle"],
		logoURL,
		logoURLDark,
	)
}

func sanitizedCodexConnectorIconURL(value string) string {
	if len(value) == 0 || len(value) > 1024 || !utf8.ValidString(value) {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return ""
	}
	if parsed.Scheme == "" && parsed.Host == "" {
		return resolvedCodexStaticIconURL(parsed)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.Port() != "" {
		return ""
	}
	parsed.Scheme = "https"
	parsed.Host = strings.ToLower(parsed.Host)
	if isCodexSignedIconPath(parsed) {
		query, queryErr := url.ParseQuery(parsed.RawQuery)
		if queryErr != nil || !validCodexSignedIconQuery(query) {
			return ""
		}
		return parsed.String()
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	return parsed.String()
}

func resolvedCodexStaticIconURL(parsed *url.URL) string {
	const prefix = "/images/ecosystem/apps/"
	if parsed == nil || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		!strings.HasPrefix(parsed.Path, prefix) || !strings.HasSuffix(parsed.Path, ".png") ||
		path.Clean(parsed.Path) != parsed.Path || strings.Contains(parsed.Path, "//") {
		return ""
	}
	return "https://chatgpt.com" + parsed.Path
}

func isCodexSignedIconPath(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	switch parsed.Hostname() {
	case "files.openai.com":
		return parsed.Path == "/content"
	case "chatgpt.com":
		return parsed.Path == "/backend-api/estuary/content"
	default:
		return false
	}
}

func validCodexSignedIconQuery(query url.Values) bool {
	if len(query["id"]) != 1 || query.Get("id") == "" ||
		len(query["sig"]) != 1 || query.Get("sig") == "" {
		return false
	}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" || !codexSignedIconQueryKey(key) {
			return false
		}
	}
	return true
}

func codexSignedIconQueryKey(key string) bool {
	switch key {
	case "cdn", "cid", "cp", "id", "ma", "p", "sig", "ts", "v":
		return true
	default:
		return false
	}
}

func validatedCodexOAuthURL(value string) string {
	if len(value) == 0 || len(value) > codexConnectorMaximumOperationURL || !utf8.ValidString(value) {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" || parsed.Opaque != "" {
		return ""
	}
	for key := range parsed.Query() {
		switch strings.ToLower(key) {
		case "access_token", "refresh_token", "id_token", "token", "authorization", "api_key", "client_secret":
			return ""
		}
	}
	return parsed.String()
}

// CodexMCPOAuthFlow owns the transient app-server session for one local MCP login.
type CodexMCPOAuthFlow struct {
	NativeID         string `json:"nativeId"`
	AuthorizationURL string `json:"authorizationUrl"`

	session    codexConnectorRPC
	cancel     context.CancelFunc
	done       chan struct{}
	finishOnce sync.Once
	mu         sync.RWMutex
	result     CodexMCPOAuthResult
}

func startCodexMCPOAuth(
	ctx context.Context,
	session codexConnectorRPC,
	nativeID string,
) (*CodexMCPOAuthFlow, error) {
	if session == nil || !validCodexConnectorNativeID(nativeID) {
		if session != nil {
			session.close()
		}
		return nil, ErrCodexConnectorNotConnectable
	}
	raw, err := session.request(ctx, "mcpServer/oauth/login", map[string]any{
		"name": nativeID, "timeoutSecs": int64(codexConnectorOAuthTimeout / time.Second),
	})
	if err != nil {
		session.close()
		return nil, ErrCodexConnectorsUnavailable
	}
	var response struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	if json.Unmarshal(raw, &response) != nil {
		session.close()
		return nil, ErrCodexConnectorsUnavailable
	}
	authorizationURL := validatedCodexOAuthURL(response.AuthorizationURL)
	if authorizationURL == "" {
		session.close()
		return nil, ErrCodexConnectorNotConnectable
	}
	flow := &CodexMCPOAuthFlow{
		NativeID: nativeID, AuthorizationURL: authorizationURL,
		session: session, done: make(chan struct{}),
	}
	go flow.consumeNotifications(ctx)
	return flow, nil
}

func (f *CodexMCPOAuthFlow) consumeNotifications(ctx context.Context) {
	if f == nil || f.session == nil {
		return
	}
	queued, overflow := f.session.drainNotifications()
	if overflow {
		f.finish(CodexMCPOAuthStatusFailed)
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
			f.finish(CodexMCPOAuthStatusFailed)
			return
		}
		if f.consumeNotification(notification) {
			return
		}
	}
}

func (f *CodexMCPOAuthFlow) consumeNotification(notification codexRPCNotification) bool {
	if notification.Method != "mcpServer/oauthLogin/completed" {
		return false
	}
	var completed struct {
		Name    string `json:"name"`
		Success bool   `json:"success"`
	}
	if json.Unmarshal(notification.Params, &completed) != nil || completed.Name != f.NativeID {
		return false
	}
	status := CodexMCPOAuthStatusFailed
	if completed.Success {
		status = CodexMCPOAuthStatusSucceeded
	}
	f.finish(status)
	return true
}

func (f *CodexMCPOAuthFlow) finish(status CodexMCPOAuthStatus) {
	f.finishOnce.Do(func() {
		f.mu.Lock()
		f.result = CodexMCPOAuthResult{NativeID: f.NativeID, Status: status}
		cancel := f.cancel
		f.mu.Unlock()
		f.session.close()
		if cancel != nil {
			cancel()
		}
		close(f.done)
	})
}

func (f *CodexMCPOAuthFlow) attachCancel(cancel context.CancelFunc) {
	if f == nil || cancel == nil {
		return
	}
	f.mu.Lock()
	f.cancel = cancel
	finished := false
	select {
	case <-f.done:
		finished = true
	default:
	}
	f.mu.Unlock()
	if finished {
		cancel()
	}
}

// Wait waits for Codex's OAuth completion notification or the caller's context.
func (f *CodexMCPOAuthFlow) Wait(ctx context.Context) (CodexMCPOAuthResult, error) {
	if f == nil || f.done == nil {
		return CodexMCPOAuthResult{}, ErrCodexConnectorsUnavailable
	}
	select {
	case <-ctx.Done():
		return CodexMCPOAuthResult{}, ctx.Err()
	case <-f.done:
		f.mu.RLock()
		defer f.mu.RUnlock()
		return f.result, nil
	}
}

// Close cancels an unfinished OAuth attempt and releases its app-server process.
func (f *CodexMCPOAuthFlow) Close() {
	if f != nil && f.done != nil {
		f.finish(CodexMCPOAuthStatusCancelled)
	}
}
