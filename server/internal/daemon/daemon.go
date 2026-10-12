package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/authfile"
	"boardly.dev/boardly/server/internal/daemon/repocache"
	"boardly.dev/boardly/server/internal/daemon/workspace"
	"boardly.dev/boardly/server/pkg/protocol"
)

const (
	runSlotCapacityBackoff = 5 * time.Second
	// defaultRunPrepareTimeout is a hard liveness bound for everything after
	// claim and before StartRun succeeds: skill bundles, workspace
	// acquisition, the execenv provider pass, and the StartRun request. It is
	// deliberately independent from AgentTimeout, which only governs the
	// provider process after the run reaches running.
	defaultRunPrepareTimeout = 5 * time.Minute
	// pendingWorkHeartbeatTimeout bounds the out-of-band heartbeat a
	// server-pushed daemon.pending_work hint triggers. Short on purpose: the
	// hint is only a latency optimisation.
	pendingWorkHeartbeatTimeout = 15 * time.Second
	// pendingWorkHintBookkeepingTTL keeps the last-hint map bounded.
	pendingWorkHintBookkeepingTTL = 10 * time.Minute
	modelDiscoveryCommandTimeout  = 30 * time.Second
	modelDiscoveryWorkTimeout     = 50 * time.Second
)

// pendingWorkHintMinInterval is the floor between two hint-driven heartbeats
// for the same runtime. A var so tests can shrink it.
var pendingWorkHintMinInterval = time.Second

var (
	runPrepareLeaseRefresh = 15 * time.Second
	runPrepareLeaseTimeout = 10 * time.Second
)

// errRunPrepareTimeout distinguishes the dispatched → running startup
// deadline from provider execution timeouts.
var errRunPrepareTimeout = errors.New("run preparation timed out")

// errSkillBundleUnavailable marks a run that died in preparation because the
// daemon could not download one of the agent's skill bundles.
var errSkillBundleUnavailable = errors.New("skill bundle unavailable")

// runScopedAuthToken extracts and validates the run-scoped brt_ credential
// the claim carried. The daemon must never fall back to its own session
// token in the agent process environment.
func runScopedAuthToken(run Run) (string, error) {
	token := run.AuthToken
	if token == "" {
		return "", errors.New("server did not provide a run-scoped auth token")
	}
	if !validRunScopedAuthTokenValue(token) {
		return "", errors.New("server provided a non-run-scoped auth token")
	}
	return token, nil
}

func validRunScopedAuthTokenValue(token string) bool {
	if len(token) != 68 || token[:4] != "brt_" {
		return false
	}
	for _, value := range token[4:] {
		if value < '0' || value > '9' {
			if value < 'a' || value > 'f' {
				return false
			}
		}
	}
	return true
}

// runner executes a single agent run; an interface so tests can inject a fake
// without spawning real agent processes.
type runner interface {
	run(ctx context.Context, run Run, provider string, slot int, log *slog.Logger) (RunResult, error)
}

type runnerFunc func(context.Context, Run, string, int, *slog.Logger) (RunResult, error)

func (f runnerFunc) run(ctx context.Context, run Run, provider string, slot int, log *slog.Logger) (RunResult, error) {
	return f(ctx, run, provider, slot, log)
}

type terminalRunReportKind uint8

const (
	terminalRunReportComplete terminalRunReportKind = iota + 1
	terminalRunReportFail
)

// terminalRunReport is the single daemon-side representation of a terminal
// callback: every complete/fail path goes through reportTerminalRun.
type terminalRunReport struct {
	kind       terminalRunReportKind
	runID      uuid.UUID
	transcript RunTranscriptProof
	// pendingItem is the one item the daemon sent without ever seeing it
	// acknowledged. A refused transition replays it to find out whether the
	// server holds it, which is the only way the proof can name an item the
	// local head does not.
	pendingItem           *protocol.RunTranscriptAppendRequest
	errorMessage          string
	sessionID             string
	failureReason         string
	sessionRolloutMissing bool
	retiredSessionID      string
}

// companyState tracks registered runtimes for a single company.
type companyState struct {
	companyID  string
	runtimeIDs []string
	// registeredVersions is the provider CLI version the last accepted
	// registration for this company carried, per provider; the version
	// refresh compares a fresh probe against it. registeredSignIn is the
	// sign-in state that registration carried, compared the same way.
	registeredVersions map[string]string
	registeredSignIn   map[string]string
}

// Daemon is the local agent runtime that polls for and executes runs.
type Daemon struct {
	cfg        Config
	client     *Client
	workspaces *workspace.Manager
	localDirs  *workspace.LocalDirectoryManager
	skillCache *SkillBundleCache
	connectors *localConnectorService
	plugins    *localPluginService
	logger     *slog.Logger

	// transcriptDeliveryWindowOverride shortens the completed-item delivery
	// bound in tests; zero means defaultTranscriptDeliveryWindow.
	transcriptDeliveryWindowOverride time.Duration

	mu           sync.Mutex
	companies    map[string]*companyState
	runtimeIndex map[string]Runtime // runtimeID -> Runtime for provider lookups
	reloading    sync.Mutex         // prevents concurrent company syncs
	runtimeSet   *runtimeSetWatcher // multi-subscriber pub/sub for runtime-set changes

	versionsMu    sync.RWMutex
	agentVersions map[string]string // provider -> detected CLI version

	// agentsAvailable is the current CLI availability set, copy-on-write so
	// run-execution paths can read it without a lock.
	agentsAvailable atomic.Pointer[map[string]AgentEntry]

	// skippedAgents records why a discovered provider did not register
	// online in the last probe round — dropped from the payload, or
	// registered offline with an unusableReason. Surfaced on /health;
	// diagnostic only. unusableProviders is the subset that registered
	// offline with a reason: their runtimes stay in the roster (heartbeat
	// and deregister keep the row alive) but are never claimed for.
	skippedAgentsMu   sync.RWMutex
	skippedAgents     map[string]string
	unusableProviders map[string]struct{}

	// isolationMu guards the credential-isolation state
	// (credential_isolation.go): isolation, the last probe round's verdict
	// (nil until the first round, which means not enforced), and
	// unwrappedLaunches, the Runs' providers and app previews that started
	// without the sandbox and have not ended. Reports and launches read both
	// under the lock, so the computer never reports enforced while such a
	// process still runs.
	isolationMu       sync.Mutex
	isolation         *credentialIsolationVerdict
	unwrappedLaunches int

	// runtimeRefreshInFlight coalesces on-demand setup refreshes; a request
	// that arrives mid-round sets runtimeRefreshPending so one more round
	// follows, and runtimeRefreshLastAt enforces the floor between rounds.
	runtimeRefreshInFlight atomic.Bool
	runtimeRefreshPending  atomic.Bool
	runtimeRefreshMu       sync.Mutex
	runtimeRefreshLastAt   time.Time
	// providerAdoption paces the availability re-probe's attempts at a
	// provider found on disk that does not register online yet
	// (provider_availability.go).
	providerAdoption *providerBackoff

	wsHBMu      sync.RWMutex
	wsHBLastAck map[string]time.Time // runtime_id -> last WS heartbeat ack

	// reconcile fans a "re-check server state now" signal out to the run
	// cancellation watchers so a WS reconnect shrinks reconciliation gaps.
	reconcile *reconcileBroadcaster

	// wsRPC carries request/response RPCs (runs.claim) over the wakeup WS
	// connection; detached on disconnect, callers fall back to HTTP.
	wsRPC *wsRPCClient

	// wsClaimHTTPFallbackAfter is set after an uncertain WS claim outcome;
	// once the safety delay elapses, the next claim bypasses WS once.
	wsClaimHTTPFallbackAfter atomic.Int64

	// runtimeGoneMu guards the recovery coalescing state so heartbeat,
	// poller, and WS-ack handlers converge on one re-register per wave.
	runtimeGoneMu             sync.Mutex
	runtimeGoneInflight       map[string]struct{}
	reregisterNextAttempt     map[string]time.Time // company_id -> earliest next attempt
	reregisterLastCompletedAt map[string]time.Time // company_id -> last SUCCESSFUL re-register
	runtimeGoneWorkers        sync.WaitGroup       // recoveries startRuntimeGoneRecovery launched

	// pendingWorkMu coalesces and rate-limits server-pushed "heartbeat now"
	// hints so a caller loop cannot become a heartbeat amplifier.
	pendingWorkMu       sync.Mutex
	pendingWorkInflight map[string]struct{}
	pendingWorkLastRun  map[string]time.Time

	rootCtx context.Context // set by Run(); used by recoveries that outlive per-runtime ctx
	// cancelFunc ends Run's root context; triggerRestart uses it to hand the
	// process over to a replaced boardly binary (self_reload.go).
	cancelFunc context.CancelFunc

	// restartMu guards restartBinary: non-empty once a handoff is scheduled,
	// read by the CLI after Run returns.
	restartMu     sync.Mutex
	restartBinary string
	// selfDigest is the digest of the boardly binary this process started
	// from; reloadPendingReason explains a confirmed change the daemon has
	// not restarted into yet. Both are diagnostic only.
	selfDigest          atomic.Pointer[string]
	reloadPendingReason atomic.Pointer[string]

	// claimMu guards pauseClaims and claimsInFlight, the barrier a restart
	// takes so it never interrupts a claimed run: the poller refuses to claim
	// while pauseClaims is set, and the barrier refuses to set it while a
	// claim or a run is in flight.
	claimMu        sync.Mutex
	pauseClaims    bool
	claimsInFlight int

	// activeRuns counts runs in handleRun (preparation and local-directory
	// waiters included); runningRuns counts live provider sessions;
	// resourceWaitRuns counts runs parked on a path lock.
	activeRuns                atomic.Int64
	runningRuns               atomic.Int64
	resourceWaitRuns          atomic.Int64
	ready                     atomic.Bool // gates /health status starting -> running
	localPreview              *localPreviewHub
	localPreviewToken         string
	appPreviews               *appPreviewService
	commerceDev               *commerceDevService
	commerceDevEndpoint       string
	commerceDevBootstrapMu    sync.RWMutex
	taskCommerceDevBootstraps map[commerceDevBootstrapKey]*taskCommerceDevBootstrap
	// localPlaneEndpoint is the loopback origin of the health/control
	// listener; run_cli_channel.go writes it into every Run's run.json.
	localPlaneEndpoint       string
	runChannelMu             sync.RWMutex
	runChannels              map[runChannelKey]*runChannel
	boardlyExecutable        *os.File
	cleanupBoardlyExecutable func() error

	tools              *localToolService
	runner             runner        // set to d.doRun by New; overridable in tests
	probes             cliProbes     // set by New; overridable in tests
	cancelPollInterval time.Duration // server-side cancellation poll cadence
	runPrepareTimeout  time.Duration
	// Test-only shutdown overrides keep the production health-server drain
	// deadlines fixed while allowing deterministic lifecycle tests.
	healthShutdownTimeoutOverride   time.Duration
	healthCloseDrainTimeoutOverride time.Duration
}

// New creates a new Daemon instance.
func New(cfg Config, logger *slog.Logger) (*Daemon, error) {
	if _, err := loadReviewedToolCatalog(); err != nil {
		return nil, fmt.Errorf("initialize managed tools: %w", err)
	}
	boardlyExecutablePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve boardly executable: %w", err)
	}
	boardlyExecutablePath, err = filepath.Abs(boardlyExecutablePath)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute boardly executable: %w", err)
	}
	boardlyExecutable, cleanupBoardlyExecutable, err := pinBoardlyExecutable(boardlyExecutablePath)
	if err != nil {
		return nil, err
	}
	client := NewClient(cfg.ServerBaseURL)
	client.SetVersion(cfg.CLIVersion)
	client.SetDaemonID(cfg.DaemonID)

	manager, err := workspace.NewManager(
		cfg.WorkspacesRoot,
		repositoryCacheRoot(cfg.WorkspacesRoot),
		workspaceMetadataKey(cfg.DaemonID),
		repocache.OSRunner{},
		workspace.Retention{Completed: 24 * time.Hour, Orphaned: 72 * time.Hour},
		nil,
	)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("construct workspace manager: %w", err),
			cleanupBoardlyExecutable(),
		)
	}
	d := &Daemon{
		cfg:                       cfg,
		tools:                     newLocalToolService(filepath.Join(cfg.WorkspacesRoot, ".tools")),
		client:                    client,
		workspaces:                manager,
		localDirs:                 workspace.NewLocalDirectoryManager(),
		skillCache:                NewSkillBundleCache(filepath.Join(cfg.WorkspacesRoot, ".skill-cache", "v1")),
		logger:                    logger,
		companies:                 make(map[string]*companyState),
		runtimeIndex:              make(map[string]Runtime),
		runtimeSet:                newRuntimeSetWatcher(),
		agentVersions:             make(map[string]string),
		probes:                    defaultCLIProbes(),
		skippedAgents:             make(map[string]string),
		wsHBLastAck:               make(map[string]time.Time),
		runtimeGoneInflight:       make(map[string]struct{}),
		reregisterNextAttempt:     make(map[string]time.Time),
		reregisterLastCompletedAt: make(map[string]time.Time),
		pendingWorkInflight:       make(map[string]struct{}),
		pendingWorkLastRun:        make(map[string]time.Time),
		reconcile:                 newReconcileBroadcaster(),
		wsRPC:                     newWSRPCClient(wsRPCResponseGrace),
		localPreview:              newLocalPreviewHub(256 << 10),
		boardlyExecutable:         boardlyExecutable,
		cleanupBoardlyExecutable:  cleanupBoardlyExecutable,
		providerAdoption: newProviderBackoff(
			providerAvailabilityProbeInterval,
			providerAvailabilityProbeMaxBackoff,
		),
		cancelPollInterval: 5 * time.Second,
		runPrepareTimeout:  defaultRunPrepareTimeout,
	}
	// App previews run Agent-written commands, so they start in the Runs'
	// sandbox and count toward its report (Access TECH B3e).
	d.appPreviews, err = newAppPreviewService(
		filepath.Join(cfg.WorkspacesRoot, ".app-previews", cfg.DaemonID),
		logger,
		d.appPreviewIsolation,
	)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("construct app preview supervisor: %w", err),
			cleanupBoardlyExecutable(),
		)
	}
	d.tools.studioOrigin = cfg.StudioAPIOrigin
	initialAgents := make(map[string]AgentEntry, len(cfg.Agents))
	for name, entry := range cfg.Agents {
		initialAgents[name] = entry
	}
	d.agentsAvailable.Store(&initialAgents)
	d.connectors = newLocalConnectorService(client)
	d.connectors.keepaliveInterval = connectorDiscoveryInterval(cfg.ConnectorDiscoveryInterval)
	d.plugins = newLocalPluginService(client)
	for provider, entry := range initialAgents {
		d.wireProviderBackends(provider, entry)
	}
	d.runner = runnerFunc(d.doRun)
	d.probes.isolation = d.probeCredentialIsolation
	return d, nil
}

// wireProviderBackends gives a provider its native connector and plugin
// backends over the discovered executable. New calls it for every provider
// found at start and the availability re-probe for one adopted later, so a
// late-installed CLI gets exactly the capability surface a start-time one
// has. Replacing a backend closes the previous one.
func (d *Daemon) wireProviderBackends(provider string, entry AgentEntry) {
	switch provider {
	case "codex":
		d.connectors.setBackend(provider, codexNativeConnectorBackend{
			backend: NewOSCodexConnectorBackend(entry.Path),
		})
		d.plugins.setBackend(provider, codexNativePluginBackend{
			backend: NewOSCodexPluginBackend(entry.Path),
		})
	case "claude":
		d.connectors.setBackend(provider, claudeNativeConnectorBackend{
			backend: NewOSClaudeConnectorBackend(entry.Path),
		})
		d.plugins.setBackend(provider, claudeNativePluginBackend{
			backend: NewOSClaudePluginBackend(entry.Path),
		})
	}
}

// adoptProvider adds (or re-points) a provider in the availability set and
// wires its backends. The set is copy-on-write: run execution, heartbeat
// model discovery and /health read it without a lock, so a new map is stored
// and the old one is never mutated.
func (d *Daemon) adoptProvider(provider string, entry AgentEntry) {
	current := d.agents()
	next := make(map[string]AgentEntry, len(current)+1)
	for name, known := range current {
		next[name] = known
	}
	next[provider] = entry
	d.agentsAvailable.Store(&next)
	d.wireProviderBackends(provider, entry)
}

// repositoryCacheRoot is where the workspace manager keeps its repository
// caches. It must lie outside the workspaces root (the manager rejects
// overlapping roots), so it sits beside it.
func repositoryCacheRoot(workspacesRoot string) string {
	return workspacesRoot + "-repos"
}

// workspaceMetadataKey derives the 32-byte workspace metadata HMAC key from
// the daemon identity: metadata authenticates this daemon's own directories
// to itself, so a machine-stable derivation is exactly the needed strength.
func workspaceMetadataKey(daemonID string) []byte {
	key := []byte("boardly-workspace-metadata:" + daemonID)
	for len(key) < 32 {
		key = append(key, '.')
	}
	return key
}

func (d *Daemon) agents() map[string]AgentEntry {
	if p := d.agentsAvailable.Load(); p != nil {
		return *p
	}
	return nil
}

// setSkippedAgents records a probe round's verdicts. skipped is replaced
// wholesale — it describes this round. The unusable set is sticky: a provider
// registered unusable stays excluded from claiming until a probe for it
// succeeds, because a round where its probe merely fails (a hung CLI, ETXTBSY
// during the upgrade that fixes it) says nothing new while the server row is
// still offline with the reason. A provider in neither map this round
// succeeded and leaves the set. Both maps are never mutated afterwards, so
// readers may hold a snapshot outside the lock.
func (d *Daemon) setSkippedAgents(skipped map[string]string, unusable map[string]struct{}) {
	d.skippedAgentsMu.Lock()
	defer d.skippedAgentsMu.Unlock()
	sticky := make(map[string]struct{}, len(unusable)+len(d.unusableProviders))
	for provider := range unusable {
		sticky[provider] = struct{}{}
	}
	for provider := range d.unusableProviders {
		if _, notProbedOK := skipped[provider]; notProbedOK {
			sticky[provider] = struct{}{}
		}
	}
	d.skippedAgents = skipped
	d.unusableProviders = sticky
}

// claimableRuntimeIDs returns the runtime ids the daemon claims work for:
// allRuntimeIDs minus the runtimes of unusable providers. Heartbeat and
// deregister keep using allRuntimeIDs so an unusable runtime's row stays
// fresh (the server keeps it offline) while the machine is on.
func (d *Daemon) claimableRuntimeIDs() []string {
	d.skippedAgentsMu.RLock()
	unusable := d.unusableProviders
	d.skippedAgentsMu.RUnlock()
	if len(unusable) == 0 {
		return d.allRuntimeIDs()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var ids []string
	for _, cs := range d.companies {
		for _, id := range cs.runtimeIDs {
			if rt, ok := d.runtimeIndex[id]; ok {
				if _, broken := unusable[rt.Provider]; broken {
					continue
				}
			}
			ids = append(ids, id)
		}
	}
	return ids
}

func (d *Daemon) skippedAgentsSnapshot() map[string]string {
	d.skippedAgentsMu.RLock()
	defer d.skippedAgentsMu.RUnlock()
	if len(d.skippedAgents) == 0 {
		return nil
	}
	out := make(map[string]string, len(d.skippedAgents))
	for k, v := range d.skippedAgents {
		out[k] = v
	}
	return out
}

// setAgentVersion records the detected CLI version for a provider. A blank
// detection never replaces a version we already know — "couldn't read it" is
// not a version.
func (d *Daemon) setAgentVersion(provider, version string) {
	d.versionsMu.Lock()
	prev := d.agentVersions[provider]
	if version == "" && prev != "" {
		d.versionsMu.Unlock()
		d.logger.Warn("agent CLI reported no version; keeping the previous one",
			"provider", provider, "previous", prev)
		return
	}
	d.agentVersions[provider] = version
	d.versionsMu.Unlock()
}

func (d *Daemon) agentVersion(provider string) string {
	d.versionsMu.RLock()
	defer d.versionsMu.RUnlock()
	return d.agentVersions[provider]
}

// Run starts the daemon: bind the health port first (single-instance lock),
// resolve auth, discover providers, register runtimes per company, then start
// the wakeup WS, heartbeat, and poller loops.
func (d *Daemon) Run(ctx context.Context) error {
	if d.cleanupBoardlyExecutable != nil {
		defer func() {
			if err := d.cleanupBoardlyExecutable(); err != nil {
				d.logger.Warn("remove pinned Zenith Team executable failed", "error", err)
			}
		}()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.rootCtx = ctx
	d.cancelFunc = cancel
	d.recordSelfDigest()

	healthLn, err := d.listenHealth(ctx)
	if err != nil {
		return err
	}
	defer healthLn.Close()
	boardlyHome, err := BoardlyHome()
	if err != nil {
		return err
	}
	// Purge authority written by the pre-memory-only implementation before
	// authentication or provider startup. A stale Task must not retain a
	// readable signing key merely because daemon preflight later fails.
	if err := removeLegacyCommerceDevAuthority(boardlyHome); err != nil {
		return err
	}

	agentNames := make([]string, 0, len(d.agents()))
	for name := range d.agents() {
		agentNames = append(agentNames, name)
	}
	sort.Strings(agentNames)
	d.logger.Info(
		"starting daemon",
		"version",
		d.cfg.CLIVersion,
		"agents",
		agentNames,
		"server",
		d.cfg.ServerBaseURL,
	)
	d.logger.Debug(
		"daemon config resolved",
		"daemon_id", d.cfg.DaemonID,
		"device_name", d.cfg.DeviceName,
		"workspaces_root", d.cfg.WorkspacesRoot,
		"health_port", d.cfg.HealthPort,
		"poll_interval", d.cfg.PollInterval,
		"heartbeat_interval", d.cfg.HeartbeatInterval,
		"max_concurrent_runs", d.cfg.MaxConcurrentRuns,
		"launched_by", d.cfg.LaunchedBy,
	)

	if err := d.resolveAuth(ctx); err != nil {
		return err
	}
	if err := d.loadServerConfig(ctx); err != nil {
		return err
	}
	if err := d.prepareLocalPreview(healthLn); err != nil {
		d.logger.Warn("local run preview unavailable", "error", err)
	}
	d.localPlaneEndpoint = "http://" + healthLn.Addr().String()
	cleanupCommerceDev, err := d.prepareCommerceDev(healthLn)
	if err != nil {
		return fmt.Errorf("prepare local Commerce signing: %w", err)
	}

	// Serve the health port before the (potentially slow) preflight so
	// `daemon start` sees a live "starting" daemon instead of
	// connection-refused. Liveness (port up) and readiness (status running)
	// stay separate signals.
	stopHealthServer := d.startHealthServer(healthLn, time.Now())
	// The health server outlives the signal on purpose: it keeps answering
	// for the whole shutdown, so `zen daemon stop` and the desktop
	// supervisor see a daemon that is still draining rather than one that is
	// gone (§9.3). This deferred cleanup is what ends it, and it runs after
	// the drain and after deregisterRuntimes.
	//
	// The local HTTP plane reads Commerce lease/key state. Stop accepting
	// requests and join every active handler before cleanupCommerceDev clears
	// that state. A handler that survives forced connection closure keeps its
	// authority until process exit rather than racing an in-place clear.
	defer func() {
		cancel()
		if d.appPreviews != nil {
			if err := d.appPreviews.Close(); err != nil {
				d.logger.Warn("stop app previews failed", "error", err)
			}
		}
		healthErr := stopHealthServer()
		if healthErr != nil {
			d.logger.Warn("local health server stopped with an error", "error", healthErr)
		}
		if errors.Is(healthErr, errHealthHandlersNotDrained) {
			d.logger.Error(
				"local Commerce signing state retained until process exit because a handler did not drain",
			)
			return
		}
		cleanupCommerceDev()
	}()
	stopTools := d.startTools(ctx)
	defer stopTools()
	stopConnectorDiscovery := d.connectors.startBackground(ctx, func(err error) {
		d.logger.Warn("connector discovery refresh failed", "error", err)
	})
	defer stopConnectorDiscovery()
	stopPluginDiscovery := d.plugins.startBackground(ctx, func(err error) {
		d.logger.Warn("plugin discovery refresh failed", "error", err)
	})
	defer stopPluginDiscovery()

	if err := d.syncCompaniesFromAPI(ctx); err != nil {
		return err
	}

	defer d.deregisterRuntimes()

	runWakeups := make(chan runWakeup, 256)
	go d.runWakeupLoop(ctx, runWakeups)
	go d.heartbeatLoop(ctx)
	go d.gcLoop(ctx)
	go d.selfReloadLoop(ctx)
	go d.providerVersionRefreshLoop(ctx)
	go d.providerAvailabilityLoop(ctx)

	d.ready.Store(true)
	d.logger.Debug("background loops launched (run-wakeup, heartbeat); health now reporting ready")
	err = d.pollLoop(ctx, runWakeups)
	d.logger.Debug("daemon main loop returning", "error", err)
	return err
}

// resolveAuth installs the daemon's own session (daemon-auth.json, D31),
// which `zen daemon start` mints from the person's session or the desktop app
// installs. The daemon never falls back to the person's auth.json: that
// session is the CLI's, and the server refuses a daemon session wherever a
// person's would act. Access tokens are short-lived, so the daemon holds the
// file-backed session rather than a token: every request draws a valid
// token and renews it (and the rotating refresh token on disk) as needed,
// which is what lets a daemon run for days without another login.
func (d *Daemon) resolveAuth(ctx context.Context) error {
	path, err := OwnCredentialPath()
	if err != nil {
		return err
	}
	session := NewFileSession(path)
	if _, err := session.AccessToken(ctx); err != nil {
		if errors.Is(err, authfile.ErrNotFound) {
			d.logger.Warn("the daemon has no session of its own — run 'zen auth login' and " +
				"'zen daemon start', or sign in to the desktop app")
			return errors.New("not authenticated: the daemon has no session; run `zen daemon start` " +
				"after `zen auth login`")
		}
		return fmt.Errorf("load the daemon session: %w", err)
	}
	d.client.SetSession(session)
	d.logger.Info("authenticated")
	return nil
}

// deregisterTimeout bounds the shutdown deregister call. Named because
// ShutdownDrainBudget sums it with the other shutdown waits.
const deregisterTimeout = 5 * time.Second

// deregisterRuntimes notifies the server that all runtimes are going offline.
func (d *Daemon) deregisterRuntimes() {
	runtimeIDs := d.allRuntimeIDs()
	if len(runtimeIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), deregisterTimeout)
	defer cancel()
	if err := d.client.Deregister(ctx, runtimeIDs); err != nil {
		d.logger.Warn("failed to deregister runtimes on shutdown", "error", err)
	} else {
		d.logger.Info("deregistered runtimes", "count", len(runtimeIDs))
	}
}

// allRuntimeIDs returns all runtime IDs across all watched companies.
func (d *Daemon) allRuntimeIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ids []string
	for _, cs := range d.companies {
		ids = append(ids, cs.runtimeIDs...)
	}
	return ids
}

func (d *Daemon) findRuntime(id string) *Runtime {
	d.mu.Lock()
	defer d.mu.Unlock()
	if rt, ok := d.runtimeIndex[id]; ok {
		return &rt
	}
	return nil
}

func (d *Daemon) notifyRuntimeSetChanged() {
	d.runtimeSet.notify()
	d.replaceCapabilityRuntimes()
}

func (d *Daemon) replaceCapabilityRuntimes() {
	d.mu.Lock()
	runtimes := make([]Runtime, 0, len(d.runtimeIndex))
	for _, runtime := range d.runtimeIndex {
		runtimes = append(runtimes, runtime)
	}
	d.mu.Unlock()
	if d.connectors != nil {
		d.connectors.replaceRuntimes(runtimes)
	}
	if d.plugins != nil {
		d.plugins.replaceRuntimes(runtimes)
	}
}

// recoveryContext returns the daemon root context for long-running recovery
// HTTP calls that must survive a per-runtime context teardown.
func (d *Daemon) recoveryContext() context.Context {
	if d.rootCtx != nil {
		return d.rootCtx
	}
	return context.Background()
}

// runtimeSetWatcher is a tiny pub/sub for runtime-set changes: more than one
// supervisor (wakeup loop, heartbeat loop, poller) reacts to them, and a
// single buffered channel would race. Each subscriber gets a 1-slot channel;
// missed nudges coalesce.
type runtimeSetWatcher struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
}

func newRuntimeSetWatcher() *runtimeSetWatcher {
	return &runtimeSetWatcher{subscribers: make(map[chan struct{}]struct{})}
}

func (w *runtimeSetWatcher) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	w.subscribers[ch] = struct{}{}
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		delete(w.subscribers, ch)
		w.mu.Unlock()
	}
}

func (w *runtimeSetWatcher) notify() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (d *Daemon) gcLoop(ctx context.Context) {
	if !d.cfg.GCEnabled {
		d.logger.Info("workspace gc disabled")
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	ticker := time.NewTicker(d.cfg.GCInterval)
	defer ticker.Stop()
	for {
		removed, err := d.workspaces.GC(ctx)
		if err != nil {
			d.logger.Warn("workspace gc", "error", err)
		}
		if len(removed) > 0 {
			d.logger.Info("workspace gc reclaimed", "count", len(removed))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
