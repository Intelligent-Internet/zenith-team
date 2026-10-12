package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Registration and company sync
// ---------------------------------------------------------------------------

// runtimeVersionProbeConcurrency bounds concurrent `<cli> --version` probes
// during registration.
const runtimeVersionProbeConcurrency = 8

// runtimeVersionProbeAttempts bounds probe attempts per provider per round.
const runtimeVersionProbeAttempts = 2

// runtimeVersionProbeRetryDelay spaces a retry so an in-flight binary swap or
// momentary fork/exec failure has time to settle.
const runtimeVersionProbeRetryDelay = 500 * time.Millisecond

// cliProbes are the provider CLI checks a probe round runs. New wires the
// exec-backed defaults; a test replaces them on its own Daemon, so no two
// tests share a stub.
type cliProbes struct {
	version    func(ctx context.Context, path string) (string, error)
	signIn     func(ctx context.Context, provider, path string) protocol.RuntimeSignIn
	retryDelay time.Duration
	// isolation tests the macOS sandbox once per round (Access TECH B3d).
	// New wires the daemon's own probeCredentialIsolation.
	isolation func(ctx context.Context) credentialIsolationVerdict
}

func defaultCLIProbes() cliProbes {
	return cliProbes{
		version:    detectAgentVersion,
		signIn:     detectAgentSignIn,
		retryDelay: runtimeVersionProbeRetryDelay,
	}
}

// runtimeVersionProbeRetryWindow gates the retry on how quickly the failure
// came back: a probe that burned its full timeout is a hung CLI, not a
// hiccup. Overridable for tests.
var runtimeVersionProbeRetryWindow = time.Second

// runtimeProbe is one provider's verdict for a probe round. Exactly one of
// the three shapes applies: a version (register online), an Unusable reason
// (register offline carrying it — the machine is on, the CLI is broken), or
// a Skip reason (drop the provider from the round; only /health learns why).
type runtimeProbe struct {
	Version  string
	Unusable *protocol.RuntimeUnusableReason
	Skip     string
	// SignIn is the CLI's own sign-in verdict, taken once the version probe
	// proved the CLI runs; absent for an unusable or skipped provider.
	SignIn *protocol.RuntimeSignIn
}

// probeRuntime resolves and version-detects one provider, retrying a fast
// failure once. A below-minimum verdict and an exec failure the daemon can
// name become an Unusable reason; a hung or unparseable probe stays a Skip.
func (d *Daemon) probeRuntime(ctx context.Context, name string, entry AgentEntry) runtimeProbe {
	var lastErr error
	for attempt := 1; attempt <= runtimeVersionProbeAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
			case <-time.After(d.probes.retryDelay):
			}
			if ctx.Err() != nil {
				break
			}
		}
		startedAt := time.Now()
		version, err := d.probes.version(ctx, entry.Path)
		if err != nil {
			lastErr = err
			if time.Since(startedAt) >= runtimeVersionProbeRetryWindow {
				break
			}
			continue
		}
		if err := agent.CheckMinVersion(name, version); err != nil {
			if reason := unusableReasonFor(name, err); reason != nil {
				// A confirmed verdict about a version that parsed — a retry
				// would reach the same conclusion. The runtime registers
				// offline with the reason so the roster can say why.
				d.logger.Warn(
					"registering runtime as unusable: version too old",
					"name",
					name,
					"version",
					version,
					"error",
					err,
				)
				return runtimeProbe{Version: version, Unusable: reason}
			}
			// The CLI ran but printed something the gate could not parse:
			// "we didn't learn a version", not "it is too old".
			lastErr = err
			if time.Since(startedAt) >= runtimeVersionProbeRetryWindow {
				break
			}
			continue
		}
		d.setAgentVersion(name, version)
		if version == "" {
			version = d.agentVersion(name)
		}
		d.logger.Debug("agent version detected", "name", name, "version", version, "path", entry.Path)
		signIn := d.probes.signIn(ctx, name, entry.Path)
		d.logger.Debug("agent sign-in checked", "name", name, "state", signIn.State)
		return runtimeProbe{Version: version, SignIn: &signIn}
	}
	if reason := unusableReasonFor(name, lastErr); reason != nil {
		d.logger.Warn("registering runtime as unusable: CLI not executable",
			"name", name, "path", entry.Path, "error", lastErr)
		return runtimeProbe{Unusable: reason}
	}
	d.logger.Warn("skip registering runtime", "name", name, "error", lastErr)
	reason := "version detection failed"
	if lastErr != nil {
		reason = fmt.Sprintf("version detection failed: %v", lastErr)
	}
	return runtimeProbe{Skip: reason}
}

// unusableReasonFor classifies a probe failure into the closed
// protocol.RuntimeUnusableCodes vocabulary, or nil when the failure says
// nothing durable about the CLI (a hung probe, unparseable output, ETXTBSY
// while a binary is being replaced) and the provider is merely skipped this
// round. The message is the sentence the roster and `daemon status` show:
// it names the CLI, never its path.
func unusableReasonFor(provider string, err error) *protocol.RuntimeUnusableReason {
	if err == nil {
		return nil
	}
	var tooOld *agent.BelowMinimumError
	if errors.As(err, &tooOld) {
		return &protocol.RuntimeUnusableReason{
			Code: protocol.RuntimeUnusableCodeBelowMinimumVersion,
			Message: boundUnusableMessage(fmt.Sprintf("%s CLI %s is below the minimum %s",
				providerDisplayName(tooOld.AgentType), tooOld.Detected, tooOld.Minimum)),
		}
	}
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOEXEC) {
		return &protocol.RuntimeUnusableReason{
			Code:    protocol.RuntimeUnusableCodeNotExecutable,
			Message: boundUnusableMessage(providerDisplayName(provider) + " CLI is not executable"),
		}
	}
	return nil
}

// boundUnusableMessage caps a composed message at the wire limit; version
// strings come from CLI output, which the daemon does not control.
func boundUnusableMessage(message string) string {
	runes := []rune(message)
	if len(runes) <= protocol.RuntimeUnusableMessageMaxLen {
		return message
	}
	return string(runes[:protocol.RuntimeUnusableMessageMaxLen])
}

// detectRuntimes version-detects every configured agent CLI concurrently and
// returns one registration entry per provider that cleared the gate, plus
// one offline entry carrying an unusableReason per provider that was found
// but cannot run. Unusable providers are also recorded as skipped with the
// same message, so /health and `daemon status` show the text the roster
// shows. The result describes the machine, so one probe round serves every
// company.
func (d *Daemon) detectRuntimes(ctx context.Context) []RegisterRuntime {
	type detected struct {
		name     string
		version  string
		unusable *protocol.RuntimeUnusableReason
		signIn   *protocol.RuntimeSignIn
	}
	var (
		mu       sync.Mutex
		results  []detected
		skipped  = map[string]string{}
		unusable = map[string]struct{}{}
		g        errgroup.Group
	)
	g.SetLimit(runtimeVersionProbeConcurrency)
	for name, entry := range d.agents() {
		g.Go(func() error {
			probe := d.probeRuntime(ctx, name, entry)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case probe.Skip != "":
				skipped[name] = probe.Skip
				return nil
			case probe.Unusable != nil:
				skipped[name] = probe.Unusable.Message
				unusable[name] = struct{}{}
			}
			results = append(results, detected{
				name: name, version: probe.Version, unusable: probe.Unusable, signIn: probe.SignIn,
			})
			return nil
		})
	}
	_ = g.Wait()

	d.setSkippedAgents(skipped, unusable)
	// One sandbox verdict per round serves every runtime: it describes the
	// computer, and heartbeats repeat it until the next round.
	if d.probes.isolation != nil {
		d.recordCredentialIsolation(d.probes.isolation(ctx))
	}
	isolation := d.credentialIsolation()

	sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })
	runtimes := make([]RegisterRuntime, 0, len(results))
	for _, r := range results {
		displayName := providerDisplayName(r.name)
		if d.cfg.DeviceName != "" {
			displayName = fmt.Sprintf("%s (%s)", displayName, d.cfg.DeviceName)
		}
		status := "online"
		if r.unusable != nil {
			status = "offline"
		}
		runtimes = append(runtimes, RegisterRuntime{
			Name:                displayName,
			Provider:            r.name,
			Version:             r.version,
			Status:              status,
			UnusableReason:      r.unusable,
			SignIn:              r.signIn,
			CredentialIsolation: isolation,
		})
	}
	return runtimes
}

// providerDisplayName returns the human-facing runtime name for a provider.
func providerDisplayName(name string) string {
	if name == "" {
		return name
	}
	return string(name[0]-'a'+'A') + name[1:]
}

// registerRuntimesForCompany registers this host's runtimes for one company
// using an already-probed payload. The entries are plain values, so sharing
// the slice across companies cannot leak a mutation between registrations.
func (d *Daemon) registerRuntimesForCompany(
	ctx context.Context,
	companyID string,
	runtimes []RegisterRuntime,
) (*RegisterResponse, error) {
	if len(runtimes) == 0 {
		return nil, errors.New("no agent runtimes could be registered")
	}
	resp, err := d.client.Register(ctx, RegisterRequest{
		CompanyID:  companyID,
		DaemonID:   d.cfg.DaemonID,
		DeviceName: d.cfg.DeviceName,
		CLIVersion: d.cfg.CLIVersion,
		LaunchedBy: d.cfg.LaunchedBy,
		Runtimes:   runtimes,
	})
	if err != nil {
		return nil, fmt.Errorf("register runtimes: %w", err)
	}
	if len(resp.Runtimes) == 0 {
		return nil, errors.New("register runtimes: empty response")
	}
	return resp, nil
}

// providerVersionRefreshInterval is how often a running daemon re-probes the
// version of every provider CLI it registered, so an in-place upgrade of
// claude or codex reaches the roster without a restart. One `--version` fork
// per installed CLI, machine-level. Overridable for tests.
var providerVersionRefreshInterval = 10 * time.Minute

func (d *Daemon) providerVersionRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(providerVersionRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshProviderVersions(ctx)
		}
	}
}

// refreshProviderVersions re-probes the provider CLIs and re-registers every
// tracked company whose last accepted registration carried another version
// for a provider it registered. A provider whose probe failed this round is
// absent from the payload and therefore never compared, so an unreadable or
// uninstalled CLI cannot trigger registrations. An unusable provider stays
// in the payload as an offline entry with its reason, and its version string
// is what changes on an upgrade — so the same comparison re-registers the
// company without the reason once the CLI is fixed.
func (d *Daemon) refreshProviderVersions(ctx context.Context) {
	d.reloading.Lock()
	defer d.reloading.Unlock()

	payload := d.detectRuntimes(ctx)
	if len(payload) == 0 {
		return
	}
	behind := d.companiesBehind(payload)
	if len(behind) == 0 {
		return
	}
	d.logger.Info("provider CLI version changed; re-registering without a restart",
		"companies", len(behind))
	changed := false
	for _, companyID := range behind {
		resp, err := d.registerRuntimesForCompany(ctx, companyID, payload)
		if err != nil {
			d.logger.Warn("re-register after provider version change failed; will retry",
				"company_id", companyID, "error", err)
			continue
		}
		d.indexRegisterResponse(companyID, resp, payload)
		changed = true
	}
	if changed {
		d.notifyRuntimeSetChanged()
	}
}

// companiesBehind lists, in stable order, the tracked companies whose
// recorded provider versions differ from payload for a provider they have
// registered. A provider with no record has never registered there; bringing
// it online is registration's job, not a version change.
func (d *Daemon) companiesBehind(payload []RegisterRuntime) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var behind []string
	for id, cs := range d.companies {
		for _, rt := range payload {
			sent, has := cs.registeredVersions[rt.Provider]
			if !has {
				continue
			}
			// An unknown verdict is "the check did not answer", not news:
			// it never re-registers on its own, so a status command that
			// hiccups under load cannot flap every roster for ten minutes.
			recordedSignIn, hasSignIn := cs.registeredSignIn[rt.Provider]
			signIn := signInState(rt.SignIn)
			if sent != rt.Version ||
				(hasSignIn && signIn != protocol.RuntimeSignInUnknown && recordedSignIn != signIn) {
				behind = append(behind, id)
				break
			}
		}
	}
	sort.Strings(behind)
	return behind
}

func signInState(signIn *protocol.RuntimeSignIn) string {
	if signIn == nil {
		return ""
	}
	return signIn.State
}

// runtimeRefreshMinInterval is the floor between two on-demand setup
// refreshes: a double-click wants one answer, and every refresh forks a
// login shell plus two probes per provider.
var runtimeRefreshMinInterval = 5 * time.Second

// refreshRuntimeSetupOnce runs one on-demand setup refresh on the daemon
// root context. A refresh already in flight absorbs later requests but
// remembers that one arrived, so a token replaced while the probe was
// running is still seen by one more round; a request inside the floor is
// dropped, since the probe describes the machine and the answer is fresh.
func (d *Daemon) refreshRuntimeSetupOnce(reason string) {
	if !d.runtimeRefreshInFlight.CompareAndSwap(false, true) {
		d.runtimeRefreshPending.Store(true)
		d.logger.Debug("runtime setup refresh already in flight; queued one more round", "reason", reason)
		return
	}
	defer d.runtimeRefreshInFlight.Store(false)
	d.runtimeRefreshMu.Lock()
	tooSoon := time.Since(d.runtimeRefreshLastAt) < runtimeRefreshMinInterval
	if !tooSoon {
		d.runtimeRefreshLastAt = time.Now()
	}
	d.runtimeRefreshMu.Unlock()
	if tooSoon {
		d.logger.Debug("runtime setup refresh throttled", "reason", reason)
		return
	}
	for {
		d.runtimeRefreshPending.Store(false)
		ctx := d.recoveryContext()
		if ctx.Err() != nil {
			return
		}
		if err := d.refreshRuntimeSetup(ctx, reason); err != nil {
			d.logger.Warn("runtime setup refresh failed", "reason", reason, "error", err)
		}
		if !d.runtimeRefreshPending.Load() {
			return
		}
		reason += " (queued)"
	}
}

// refreshRuntimeSetup is the on-demand form of the two background loops: it
// re-discovers the provider CLIs without the login-shell cache, adopts any
// that moved or appeared, probes version and sign-in again, and re-registers
// every tracked company whether or not anything looks different — a person
// asked for a fresh answer, and a token replaced by hand changes nothing the
// comparison could see. The register upsert is what tells the roster.
func (d *Daemon) refreshRuntimeSetup(ctx context.Context, reason string) error {
	// Ask every installed Codex copy for its version again instead of
	// trusting remembered answers: a version manager's shim runs a newly
	// installed Codex from a file that did not change.
	codexInstalls.forgetAnswers()
	found := reprobeAgentCLIs()

	d.reloading.Lock()
	defer d.reloading.Unlock()

	for provider, entry := range found {
		if known, ok := d.agents()[provider]; ok && known.Path == entry.Path {
			continue
		}
		d.logger.Info("provider CLI adopted during setup refresh", "provider", provider, "path", entry.Path)
		d.adoptProvider(provider, entry)
	}
	payload := d.detectRuntimes(ctx)
	if len(payload) == 0 {
		return errors.New("no provider CLI could be probed")
	}
	d.logger.Info("refreshing runtime setup", "reason", reason, "runtimes", len(payload))
	// A probe that merely failed this round (a binary being replaced, a hung
	// --version) leaves its provider out of the payload. Registering that
	// payload would narrow the company's roster and stop claiming for a
	// runtime that runs today, so such a company is held — the same rule the
	// availability loop applies — and the skipped reason stays on /health.
	held := d.companiesMissingInstalledProvider(payload, found)
	var errs []error
	changed := false
	for _, companyID := range d.trackedCompanyIDs() {
		if absent, ok := held[companyID]; ok {
			d.logger.Warn("runtime setup refresh held: installed provider did not answer its probe",
				"company_id", companyID, "providers", absent)
			continue
		}
		resp, err := d.registerRuntimesForCompany(ctx, companyID, payload)
		if err != nil {
			errs = append(errs, fmt.Errorf("company %s: %w", companyID, err))
			continue
		}
		d.indexRegisterResponse(companyID, resp, payload)
		changed = true
	}
	if changed {
		d.notifyRuntimeSetChanged()
	}
	return errors.Join(errs...)
}

// companiesMissingInstalledProvider maps each tracked company to the
// providers it registered that are installed on this machine but absent
// from payload — the ones a transient probe failure would drop.
func (d *Daemon) companiesMissingInstalledProvider(
	payload []RegisterRuntime,
	found map[string]AgentEntry,
) map[string][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	carried := make(map[string]struct{}, len(payload))
	for _, rt := range payload {
		carried[rt.Provider] = struct{}{}
	}
	held := make(map[string][]string)
	for id, cs := range d.companies {
		var absent []string
		for provider := range cs.registeredVersions {
			if _, ok := carried[provider]; ok {
				continue
			}
			if _, installed := found[provider]; installed {
				absent = append(absent, provider)
			}
		}
		if len(absent) > 0 {
			sort.Strings(absent)
			held[id] = absent
		}
	}
	return held
}

// trackedCompanyIDs lists the companies this daemon has registered for, in
// stable order.
func (d *Daemon) trackedCompanyIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]string, 0, len(d.companies))
	for id := range d.companies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// syncCompaniesOnce runs one company sync on the daemon root context.
// Coalescing lives in syncCompaniesFromAPI's reloading mutex.
func (d *Daemon) syncCompaniesOnce(reason string) {
	ctx := d.recoveryContext()
	if ctx.Err() != nil {
		return
	}
	if err := d.syncCompaniesFromAPI(ctx); err != nil {
		d.logger.Warn("company sync failed", "reason", reason, "error", err)
	}
}

// syncCompaniesFromAPI fetches the user's companies and registers runtimes
// for any that aren't already tracked; companies the user has left are
// cleaned up. One probe round serves every company registered in the batch.
func (d *Daemon) syncCompaniesFromAPI(ctx context.Context) error {
	d.reloading.Lock()
	defer d.reloading.Unlock()

	apiCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	companies, err := d.client.ListCompanies(apiCtx)
	if err != nil {
		return fmt.Errorf("list companies: %w", err)
	}
	d.logger.Debug("company sync: fetched companies", "count", len(companies))

	apiIDs := make(map[string]string, len(companies))
	for _, company := range companies {
		apiIDs[company.ID] = company.Name
	}

	d.mu.Lock()
	currentIDs := make(map[string]bool, len(d.companies))
	for id := range d.companies {
		currentIDs[id] = true
	}
	d.mu.Unlock()

	var (
		payload []RegisterRuntime
		probed  bool
	)
	probeRuntimes := func() []RegisterRuntime {
		if !probed {
			payload = d.detectRuntimes(ctx)
			probed = true
		}
		return payload
	}

	var registered, removed int
	for id, name := range apiIDs {
		if currentIDs[id] {
			if !d.companyNeedsRuntimeRecovery(id) {
				continue
			}
			d.logger.Info("company has no runtimes; retrying registration", "company_id", id, "name", name)
			if err := d.reregisterCompanyRuntimes(ctx, id); err != nil {
				d.logger.Warn("retry register failed", "company_id", id, "error", err)
				continue
			}
			registered++
			continue
		}
		resp, err := d.registerRuntimesForCompany(ctx, id, probeRuntimes())
		if err != nil {
			d.logger.Error("failed to register runtimes", "company_id", id, "name", name, "error", err)
			continue
		}
		runtimeIDs := d.indexRegisterResponse(id, resp, payload)

		// Tell the server about any runs the previous daemon process was
		// running on these runtimes; without this a run can stay stuck until
		// the runtime sweeper.
		for _, rid := range runtimeIDs {
			if err := d.client.RecoverOrphans(ctx, rid); err != nil {
				d.logger.Warn("recover-orphans failed", "runtime_id", rid, "error", err)
			}
		}
		d.logger.Info("watching company", "company_id", id, "name", name, "runtimes", len(runtimeIDs))
		registered++
	}

	for id := range currentIDs {
		if _, ok := apiIDs[id]; ok {
			continue
		}
		d.mu.Lock()
		if cs, exists := d.companies[id]; exists {
			for _, rid := range cs.runtimeIDs {
				delete(d.runtimeIndex, rid)
			}
		}
		delete(d.companies, id)
		d.mu.Unlock()
		d.logger.Info("stopped watching company", "company_id", id)
		removed++
	}
	if registered > 0 || removed > 0 {
		d.notifyRuntimeSetChanged()
	}

	if len(d.allRuntimeIDs()) == 0 && registered == 0 && len(companies) > 0 {
		return fmt.Errorf("failed to register runtimes for any of the %d company(ies)", len(companies))
	}
	return nil
}

// indexRegisterResponse folds a register response into local state, records
// the provider versions the accepted payload carried, and returns the
// authoritative runtime ID set for the company.
func (d *Daemon) indexRegisterResponse(
	companyID string,
	resp *RegisterResponse,
	payload []RegisterRuntime,
) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	runtimeIDs := make([]string, 0, len(resp.Runtimes))
	cs, ok := d.companies[companyID]
	if !ok {
		cs = &companyState{companyID: companyID}
		d.companies[companyID] = cs
	}
	cs.registeredVersions = make(map[string]string, len(payload))
	cs.registeredSignIn = make(map[string]string, len(payload))
	for _, rt := range payload {
		cs.registeredVersions[rt.Provider] = rt.Version
		cs.registeredSignIn[rt.Provider] = signInState(rt.SignIn)
	}
	for _, old := range cs.runtimeIDs {
		delete(d.runtimeIndex, old)
	}
	for _, rt := range resp.Runtimes {
		runtimeIDs = append(runtimeIDs, rt.ID)
		d.runtimeIndex[rt.ID] = rt
	}
	// The response is authoritative — replace, do not append, so an ID
	// rotation never leaves a stale heartbeat goroutine behind.
	cs.runtimeIDs = runtimeIDs
	return runtimeIDs
}

func (d *Daemon) companyNeedsRuntimeRecovery(companyID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	cs, ok := d.companies[companyID]
	if !ok {
		return false
	}
	return len(cs.runtimeIDs) == 0
}

// ---------------------------------------------------------------------------
// runtime_gone recovery
// ---------------------------------------------------------------------------

// reregisterCoalesceWindow caps how often the daemon re-registers a company
// after a runtime_not_found: one delete clears all of a daemon's runtimes and
// a single re-register replaces every one, so concurrent recoveries must
// collapse to one API call.
const reregisterCoalesceWindow = 30 * time.Second

// reregisterFailureBackoff is the extra wait before the next attempt when the
// previous one failed, so ~15s heartbeat ticks cannot become a register flood.
const reregisterFailureBackoff = 60 * time.Second

// startRuntimeGoneRecovery runs handleRuntimeGone off the caller's goroutine.
// runtimeGoneWorkers tracks it, so a caller can join every recovery it set
// off and read the settled outcome.
func (d *Daemon) startRuntimeGoneRecovery(runtimeID string) {
	d.runtimeGoneWorkers.Go(func() { d.handleRuntimeGone(runtimeID) })
}

// handleRuntimeGone is the single recovery entry point shared by the HTTP
// heartbeat path, the poller, and the WS runtime_gone ack: prune the stale
// runtime, then re-register the company, coalescing concurrent callers.
func (d *Daemon) handleRuntimeGone(runtimeID string) {
	if runtimeID == "" {
		return
	}
	entryAt := time.Now()

	d.runtimeGoneMu.Lock()
	if _, inflight := d.runtimeGoneInflight[runtimeID]; inflight {
		d.runtimeGoneMu.Unlock()
		return
	}
	d.runtimeGoneInflight[runtimeID] = struct{}{}
	d.runtimeGoneMu.Unlock()
	defer func() {
		d.runtimeGoneMu.Lock()
		delete(d.runtimeGoneInflight, runtimeID)
		d.runtimeGoneMu.Unlock()
	}()

	companyID, removed := d.removeStaleRuntime(runtimeID)
	if !removed {
		return
	}

	d.logger.Info("runtime deleted server-side; pruned from local state",
		"runtime_id", runtimeID, "company_id", companyID)
	d.notifyRuntimeSetChanged()

	if !d.tryClaimRegisterSlot(companyID, entryAt, time.Now()) {
		d.logger.Debug("skip re-register: coalescing with recent attempt", "company_id", companyID)
		return
	}

	err := d.reregisterCompanyRuntimes(d.recoveryContext(), companyID)
	d.recordRegisterCompletion(companyID, time.Now(), err)
	if err != nil {
		d.logger.Warn("re-register after runtime gone failed", "company_id", companyID, "error", err)
	}
}

// tryClaimRegisterSlot atomically decides whether the calling goroutine runs
// the re-register: false coalesces the call with a peer that holds the slot,
// is inside a failure backoff, or already completed a same-wave register.
func (d *Daemon) tryClaimRegisterSlot(companyID string, entryAt, now time.Time) bool {
	d.runtimeGoneMu.Lock()
	defer d.runtimeGoneMu.Unlock()
	if next, ok := d.reregisterNextAttempt[companyID]; ok && now.Before(next) {
		return false
	}
	if last, ok := d.reregisterLastCompletedAt[companyID]; ok && !last.Before(entryAt) {
		return false
	}
	d.reregisterNextAttempt[companyID] = now.Add(reregisterCoalesceWindow)
	return true
}

// recordRegisterCompletion stamps a successful register (suppressing
// same-wave stragglers, clearing the slot) or extends the failure backoff.
// Failures intentionally don't stamp lastCompletedAt — a failed register
// covered nothing.
func (d *Daemon) recordRegisterCompletion(companyID string, completedAt time.Time, err error) {
	d.runtimeGoneMu.Lock()
	defer d.runtimeGoneMu.Unlock()
	if err != nil {
		d.reregisterNextAttempt[companyID] = completedAt.Add(reregisterFailureBackoff)
		return
	}
	d.reregisterLastCompletedAt[companyID] = completedAt
	delete(d.reregisterNextAttempt, companyID)
}

// removeStaleRuntime drops a runtime ID from its company's list, the
// runtimeIndex, and the WS heartbeat freshness map.
func (d *Daemon) removeStaleRuntime(runtimeID string) (string, bool) {
	d.mu.Lock()
	var companyID string
	for id, cs := range d.companies {
		found := false
		filtered := cs.runtimeIDs[:0:0]
		for _, rid := range cs.runtimeIDs {
			if rid == runtimeID {
				found = true
				continue
			}
			filtered = append(filtered, rid)
		}
		if found {
			cs.runtimeIDs = filtered
			companyID = id
			break
		}
	}
	if companyID == "" {
		d.mu.Unlock()
		return "", false
	}
	delete(d.runtimeIndex, runtimeID)
	d.mu.Unlock()

	d.wsHBMu.Lock()
	delete(d.wsHBLastAck, runtimeID)
	d.wsHBMu.Unlock()

	return companyID, true
}

// reregisterCompanyRuntimes re-probes and re-registers one company, then
// recover-orphans the fresh runtime IDs — anything still in flight on the
// deleted rows is an orphan that must be failed and retried.
func (d *Daemon) reregisterCompanyRuntimes(ctx context.Context, companyID string) error {
	payload := d.detectRuntimes(ctx)
	resp, err := d.registerRuntimesForCompany(ctx, companyID, payload)
	if err != nil {
		return err
	}
	newIDs := d.indexRegisterResponse(companyID, resp, payload)
	d.notifyRuntimeSetChanged()
	for _, rid := range newIDs {
		d.logger.Info(
			"re-registered runtime after server-side deletion",
			"company_id",
			companyID,
			"runtime_id",
			rid,
		)
		if err := d.client.RecoverOrphans(ctx, rid); err != nil {
			d.logger.Warn("recover-orphans after re-register failed", "runtime_id", rid, "error", err)
		}
	}
	return nil
}
