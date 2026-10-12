package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"boardly.dev/boardly/server/internal/runchannel"
)

var errHealthHandlersNotDrained = errors.New("local health handlers did not drain")

// healthHandlerTracker closes the admission gate before shutdown and reports
// when every handler that entered before that point has returned. Unlike a
// WaitGroup, admission and the zero-to-drained transition share one lock, so
// an accepted request cannot race an Add against a shutdown Wait.
type healthHandlerTracker struct {
	mu            sync.Mutex
	active        int
	closing       bool
	drained       chan struct{}
	drainedClosed bool
}

func newHealthHandlerTracker() *healthHandlerTracker {
	return &healthHandlerTracker{drained: make(chan struct{})}
}

func (t *healthHandlerTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !t.enter() {
			w.Header().Set("Connection", "close")
			http.Error(w, "daemon is stopping", http.StatusServiceUnavailable)
			return
		}
		defer t.leave()
		next.ServeHTTP(w, r)
	})
}

func (t *healthHandlerTracker) enter() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closing {
		return false
	}
	t.active++
	return true
}

func (t *healthHandlerTracker) leave() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active--
	if t.closing && t.active == 0 {
		t.closeDrained()
	}
}

func (t *healthHandlerTracker) stop() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closing = true
	if t.active == 0 {
		t.closeDrained()
	}
	return t.drained
}

// closeDrained is called with mu held.
func (t *healthHandlerTracker) closeDrained() {
	if t.drainedClosed {
		return
	}
	t.drainedClosed = true
	close(t.drained)
}

// HealthResponse is returned by the daemon's local health endpoint.
type HealthResponse struct {
	Status     string    `json:"status"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"startedAt"`
	OS         string    `json:"os"`
	Uptime     string    `json:"uptime"`
	DaemonID   string    `json:"daemonId"`
	DeviceName string    `json:"deviceName"`
	ServerURL  string    `json:"serverUrl"`
	CLIVersion string    `json:"cliVersion"`
	// CLIDigest identifies the executable captured at startup, even when a
	// development rebuild keeps the same version. Never hash the current
	// on-disk replacement here: Desktop compares it to this running build.
	CLIDigest  string `json:"cliDigest,omitempty"`
	LaunchedBy string `json:"launchedBy,omitempty"`
	// ActiveRunCount is the safety count of every claimed handleRun
	// lifecycle; RunningRunCount counts live provider sessions; the wait
	// counter counts runs parked on a local-directory path lock.
	ActiveRunCount       int64    `json:"activeRunCount"`
	RunningRunCount      int64    `json:"runningRunCount"`
	ResourceWaitRunCount int64    `json:"resourceWaitRunCount"`
	Agents               []string `json:"agents"`
	// AgentVersions maps a detected provider to the CLI version the last
	// probe read. A provider whose version could not be read has no entry,
	// so a daemon that knows no version omits the key. The desktop daemon
	// panel is the consumer; diagnostic only.
	AgentVersions map[string]string `json:"agentVersions,omitempty"`
	// SkippedAgents maps a provider that WAS discovered on this machine to
	// the reason the last probe round did not register it online: dropped
	// from the payload (version undetectable), or registered offline with an
	// unusableReason (below minimum, not executable) — in which case the
	// text is the same message the roster shows. Diagnostic only.
	SkippedAgents map[string]string `json:"skippedAgents,omitempty"`
	// ReloadPendingReason explains why the daemon has confirmed a change to
	// the zen binary on disk but has not restarted into it yet — it was
	// busy at the last check and retries when idle. Diagnostic only.
	ReloadPendingReason string          `json:"reloadPendingReason,omitempty"`
	Companies           []HealthCompany `json:"companies"`
}

// HealthCompany is one company the daemon registered runtimes in, with the
// runtime ids it holds for it.
type HealthCompany struct {
	ID       string   `json:"id"`
	Runtimes []string `json:"runtimes"`
}

// listenHealth binds the health port — the single-instance lock. Returns an
// error if another daemon is already running (port taken).
func (d *Daemon) listenHealth(ctx context.Context) (net.Listener, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", d.cfg.HealthPort)
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("another daemon is already running on %s: %w", addr, err)
	}
	return ln, nil
}

// healthHandler returns the /health HTTP handler. Extracted from serveHealth
// so tests can exercise it without a listener.
func (d *Daemon) healthHandler(startedAt time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		d.mu.Lock()
		var companies []HealthCompany
		for id, cs := range d.companies {
			companies = append(companies, HealthCompany{ID: id, Runtimes: cs.runtimeIDs})
		}
		d.mu.Unlock()

		agents := make([]string, 0, len(d.agents()))
		var versions map[string]string
		for name := range d.agents() {
			agents = append(agents, name)
			if version := d.agentVersion(name); version != "" {
				if versions == nil {
					versions = make(map[string]string)
				}
				versions[name] = version
			}
		}

		// "starting" until preflight (auth + initial company sync + runtime
		// registration) completes; "running" once the daemon can claim runs.
		// The health port is bound before preflight for liveness, so callers
		// gate on this status, never on mere reachability.
		status := "starting"
		if d.ready.Load() {
			status = "running"
		}

		// Round(0) drops the monotonic reading, so time.Since falls back to
		// the wall clock: on macOS the monotonic clock pauses while the
		// machine sleeps, and uptime must agree with startedAt in the same
		// payload, which is wall-clock. No test can pause the monotonic
		// clock, so the intent lives here.
		uptime := time.Since(startedAt.Round(0)).Truncate(time.Second)
		resp := HealthResponse{
			Status:               status,
			PID:                  os.Getpid(),
			StartedAt:            startedAt.UTC(),
			OS:                   runtime.GOOS,
			Uptime:               uptime.String(),
			DaemonID:             d.cfg.DaemonID,
			DeviceName:           d.cfg.DeviceName,
			ServerURL:            d.cfg.ServerBaseURL,
			CLIVersion:           d.cfg.CLIVersion,
			LaunchedBy:           d.cfg.LaunchedBy,
			ActiveRunCount:       d.activeRuns.Load(),
			RunningRunCount:      d.runningRuns.Load(),
			ResourceWaitRunCount: d.resourceWaitRuns.Load(),
			Agents:               agents,
			AgentVersions:        versions,
			SkippedAgents:        d.skippedAgentsSnapshot(),
			ReloadPendingReason:  d.reloadPending(),
			Companies:            companies,
		}
		if digest := d.selfDigest.Load(); digest != nil {
			resp.CLIDigest = *digest
		}

		w.Header().Set("Content-Type", "application/json")
		// The status line is already on the wire; a write failure here means
		// the caller hung up, which nothing on this side can act on.
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// startHealthServer serves the local health plane on ln and returns the one
// function that stops it. It deliberately watches no context: SIGTERM is not
// the end of the daemon — pollLoop keeps draining in-flight runs for up to
// 30 s plus 150 s of terminal callbacks and the process holds this port for
// all of it, so a health server tied to the signal would answer "stopping"
// (and then nothing) about a daemon that is still executing runs and still
// owns the single-instance lock. `zen daemon stop` and the desktop
// supervisor read this port; both would start a replacement into a port they
// cannot bind. Run calls the returned stop from its deferred cleanup, after
// the drain and deregistration and before the commerce-dev clear.
//
// stop joins every handler admitted before it was called and reports
// errHealthHandlersNotDrained when forced connection closure could not join
// one, so callers do not clear handler-owned in-memory capabilities while
// that handler can still access them. It is safe to call more than once and
// returns the same result each time.
func (d *Daemon) startHealthServer(ln net.Listener, startedAt time.Time) func() error {
	mux := http.NewServeMux()
	localBaseURL := "http://" + ln.Addr().String()
	mux.HandleFunc("/health", d.healthHandler(startedAt))
	mux.HandleFunc(localPreviewPath, d.localPreviewHandler())
	mux.HandleFunc(localConnectorConnectPath, d.localConnectorConnectHandler())
	mux.HandleFunc(localConnectorRefreshPath, d.localConnectorRefreshHandler())
	mux.HandleFunc(localConnectorStatusPath, d.localConnectorStatusHandler())
	mux.HandleFunc(localPluginInstallPath, d.localPluginActionHandler(true))
	mux.HandleFunc(localPluginUninstallPath, d.localPluginActionHandler(false))
	mux.HandleFunc("/v1/tools/operate", d.localToolHandler())
	mux.HandleFunc("POST /v1/tools/studio-project/{runtimeId}/{runId}", d.localStudioProjectHandler())
	mux.HandleFunc(localPluginRefreshPath, d.localPluginRefreshHandler())
	mux.HandleFunc(localPluginIconPath, d.localPluginIconHandler())
	mux.HandleFunc(localPluginReadPath, d.localPluginReadHandler())
	mux.HandleFunc(localWorkspaceFilePreviewPath, d.localWorkspaceFilePreviewHandler())
	mux.HandleFunc(localWorkspaceFileResolvePath, d.localWorkspaceFileResolveHandler())
	mux.HandleFunc(localWorkspaceFileExportPath, d.localWorkspaceFileExportHandler())
	mux.HandleFunc(localWorkspaceAssetListPath, d.localWorkspaceAssetListHandler())
	mux.HandleFunc(localWorkspaceAssetPreviewPath, d.localWorkspaceAssetPreviewHandler())
	mux.HandleFunc(localWorkspaceAssetExportPath, d.localWorkspaceAssetExportHandler())
	mux.HandleFunc(localWorkspaceMemoryListPath, d.localWorkspaceMemoryListHandler())
	mux.HandleFunc(localTaskReviewPreparePath, d.localTaskReviewPrepareHandler())
	mux.HandleFunc(localTaskReviewAcceptPath, d.localTaskReviewAcceptHandler())
	mux.HandleFunc(localAppPreviewLegacyEnsurePath, d.localAppPreviewUnsupportedHandler())
	mux.HandleFunc(localAppPreviewLegacyRestartPath, d.localAppPreviewUnsupportedHandler())
	mux.HandleFunc(localAppPreviewInspectPath, d.localAppPreviewControlHandler(appPreviewOperationInspect))
	mux.HandleFunc(localAppPreviewAttachPath, d.localAppPreviewControlHandler(appPreviewOperationAttach))
	mux.HandleFunc(localAppPreviewEnsurePath, d.localAppPreviewControlHandler(appPreviewOperationEnsure))
	mux.HandleFunc(localAppPreviewRenewPath, d.localAppPreviewControlHandler(appPreviewOperationRenew))
	mux.HandleFunc(localAppPreviewReleasePath, d.localAppPreviewControlHandler(appPreviewOperationRelease))
	mux.HandleFunc(localAppPreviewRestartPath, d.localAppPreviewControlHandler(appPreviewOperationRestart))
	mux.HandleFunc(localAppPreviewStopPath, d.localAppPreviewControlHandler(appPreviewOperationStop))
	mux.HandleFunc(commerceDevLeaseCreatePath, d.commerceDevLeaseCreateHandler(localBaseURL))
	mux.HandleFunc(commerceDevLeaseRenewPath, d.commerceDevLeaseRenewHandler())
	mux.HandleFunc(commerceDevLeaseRevokePath, d.commerceDevLeaseRevokeHandler())
	mux.HandleFunc(commerceDevSignPath, d.commerceDevSignHandler())
	mux.HandleFunc(runchannel.Path, d.runChannelHandler())

	handlers := newHealthHandlerTracker()
	srv := &http.Server{
		Handler:           handlers.wrap(mux),
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	serveDone := make(chan error, 1)
	go func() {
		serveErr := srv.Serve(ln)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		if serveErr != nil {
			serveErr = fmt.Errorf("serve local health HTTP: %w", serveErr)
		}
		serveDone <- serveErr
	}()
	d.logger.Info("health server listening", "addr", ln.Addr().String())

	var (
		stopOnce sync.Once
		stopErr  error
	)
	return func() error {
		stopOnce.Do(func() {
			stopErr = d.shutdownHealthServer(srv, handlers, serveDone)
		})
		return stopErr
	}
}

// shutdownHealthServer closes admission, drains the server, and joins both
// the accept loop and every handler still running.
func (d *Daemon) shutdownHealthServer(
	srv *http.Server,
	handlers *healthHandlerTracker,
	serveDone <-chan error,
) error {
	drained := handlers.stop()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), d.healthShutdownTimeout())
	shutdownErr := srv.Shutdown(shutdownCtx)
	cancelShutdown()
	if shutdownErr != nil {
		shutdownErr = fmt.Errorf("gracefully shut down local health server: %w", shutdownErr)
		if closeErr := srv.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			shutdownErr = errors.Join(
				shutdownErr,
				fmt.Errorf("force close local health server: %w", closeErr),
			)
		}
	}

	drainTimer := time.NewTimer(d.healthCloseDrainTimeout())
	defer drainTimer.Stop()
	select {
	case <-drained:
	case <-drainTimer.C:
		shutdownErr = errors.Join(shutdownErr, errHealthHandlersNotDrained)
	}
	// Shutdown closed the listener, so the accept loop has returned or is
	// about to.
	return errors.Join(<-serveDone, shutdownErr)
}

// The two health-server shutdown waits, in the order shutdownHealthServer
// takes them: they are named because ShutdownDrainBudget sums them.
const (
	// healthShutdownGrace bounds the graceful server shutdown.
	healthShutdownGrace = 10 * time.Second
	// healthCloseDrainGrace bounds joining handlers that survived the forced
	// connection close.
	healthCloseDrainGrace = 2 * time.Second
)

func (d *Daemon) healthShutdownTimeout() time.Duration {
	if d.healthShutdownTimeoutOverride > 0 {
		return d.healthShutdownTimeoutOverride
	}
	return healthShutdownGrace
}

func (d *Daemon) healthCloseDrainTimeout() time.Duration {
	if d.healthCloseDrainTimeoutOverride > 0 {
		return d.healthCloseDrainTimeoutOverride
	}
	return healthCloseDrainGrace
}
