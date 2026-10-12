package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/pkg/agent"
)

var (
	errConnectorsUnavailable  = errors.New("connectors are unavailable")
	errConnectorNotDiscovered = errors.New("connector was not discovered")
)

type nativeConnectorIdentity struct {
	Source   string
	NativeID string
}

type nativeConnectorItem struct {
	Source         string
	NativeID       string
	Name           string
	Description    string
	Category       string
	IconURL        string
	InstallCount   *int64
	Status         string
	Action         string
	CanExecute     bool
	ContractDigest string
	Tools          []agent.ConnectorTool
}

type nativeConnectorOperation struct {
	State       string
	ContinueURL string
	Refresh     bool
}

type nativeConnectorBackend interface {
	Discover(context.Context, bool) ([]nativeConnectorItem, error)
	Connect(context.Context, nativeConnectorIdentity) (nativeConnectorOperation, error)
}

// nativeConnectorStatusBackend answers, live, whether one connector's account
// is linked in the provider now.
type nativeConnectorStatusBackend interface {
	Connected(context.Context, nativeConnectorIdentity) (bool, error)
}

// nativeConnectorLinkedBackend discovers with only the provider's record of
// linked accounts read live, for a linked refresh (capabilityRefreshTypeLinked).
// A backend without it runs a forced Discover instead.
type nativeConnectorLinkedBackend interface {
	DiscoverLinked(context.Context) ([]nativeConnectorItem, error)
}

type nativeConnectorLifecycle interface {
	Close()
	SetChangeHandler(func())
}

// nativeConnectorDirectoryNotifier is a backend whose first read publishes
// only the linked apps and whose full directory lands later in the
// background (CodexConnectorBackend.fillDirectory).
type nativeConnectorDirectoryNotifier interface {
	SetDirectoryHandler(func())
}

type connectorDiscoveryPublisher interface {
	publishConnectorDiscovery(
		context.Context,
		string,
		string,
		connectorDiscoveryManifest,
	) (discoveryPublishResult, error)
	reaffirmConnectorDiscovery(
		context.Context,
		string,
		string,
		connectorDiscoveryReaffirmation,
	) (discoveryPublishResult, error)
}

// connectorKeepaliveAttemptFloor spaces keepalive ticks' re-observations of
// one provider, so a failing discovery retries without spinning. A
// re-observation within it of the last successful Discover reuses that
// observation instead of running Discover again.
const connectorKeepaliveAttemptFloor = time.Minute

// withinConnectorKeepaliveFloor reports whether since is no more than the
// floor before now. A since in the future — the wall clock stepped back — is
// not within it, so a clock correction never suspends the keepalive.
func withinConnectorKeepaliveFloor(now, since time.Time) bool {
	age := now.Sub(since)
	return age >= 0 && age < connectorKeepaliveAttemptFloor
}

// connectorRuntimeAssignment is one Runtime this daemon publishes a
// provider's discovery to. publishedHash and publishedGeneration name what
// the cloud holds; confirmedAt is the wall-clock observation time the cloud
// last accepted, which is what its freshness window measures.
type connectorRuntimeAssignment struct {
	companyID           string
	runtimeID           string
	publishedHash       string
	publishedGeneration string
	confirmedAt         time.Time
}

type connectorOperation struct {
	SchemaVersion int    `json:"schemaVersion"`
	State         string `json:"state"`
	ContinueURL   string `json:"continueUrl,omitempty"`
}

type connectorStatus struct {
	SchemaVersion int  `json:"schemaVersion"`
	Connected     bool `json:"connected"`
}

type connectorExecution struct {
	PluginDirectories []string
	MCPServers        map[string]json.RawMessage
	AllowedMCPServers []string
}

type localConnectorService struct {
	mu        sync.RWMutex
	refreshMu sync.Mutex
	publisher connectorDiscoveryPublisher
	backends  map[string]nativeConnectorBackend
	catalogs  map[string][]nativeConnectorItem
	// catalogObservedAt is when Discover produced each cached catalog; every
	// manifest built from that catalog carries this time, never the publish
	// time.
	catalogObservedAt map[string]time.Time
	runtimes          map[string]map[string]connectorRuntimeAssignment
	refresh           *capabilityRefreshQueue
	now               func() time.Time
	generation        func() (string, error)
	// keepaliveInterval is the target age at which an assignment is
	// re-observed; keepaliveTicks supplies the short check tick.
	keepaliveInterval time.Duration
	keepaliveTicks    func() (<-chan time.Time, func())
	keepaliveMu       sync.Mutex
	keepaliveAttempts map[string]time.Time
	// claimRefreshWait and claimColdRefreshWait are how long a claim waits
	// for a stale provider's re-observation. claimRefreshes holds the one
	// running for each provider, and claimAttempts when each provider's last
	// one ended, which paces the next.
	claimRefreshWait     time.Duration
	claimColdRefreshWait time.Duration
	claimRefreshes       map[string]*connectorClaimRefresh
	claimAttempts        map[string]time.Time
}

// connectorClaimRefresh is one provider's claim-time re-observation. err is
// set before done closes.
type connectorClaimRefresh struct {
	done chan struct{}
	err  error
}

func newLocalConnectorService(publisher connectorDiscoveryPublisher) *localConnectorService {
	service := &localConnectorService{
		publisher:         publisher,
		backends:          make(map[string]nativeConnectorBackend),
		catalogs:          make(map[string][]nativeConnectorItem),
		catalogObservedAt: make(map[string]time.Time),
		runtimes:          make(map[string]map[string]connectorRuntimeAssignment),
		now:               time.Now,
		generation: func() (string, error) {
			id, err := uuid.NewV7()
			return id.String(), err
		},
		keepaliveInterval: DefaultConnectorDiscoveryInterval,
		keepaliveTicks: func() (<-chan time.Time, func()) {
			// A short tick against wall-clock ages, rather than one long
			// timer, recovers within a tick after the machine sleeps.
			ticker := time.NewTicker(30 * time.Second)
			return ticker.C, ticker.Stop
		},
		keepaliveAttempts:    make(map[string]time.Time),
		claimRefreshWait:     connectorClaimRefreshWait,
		claimColdRefreshWait: connectorClaimColdRefreshWait,
		claimRefreshes:       make(map[string]*connectorClaimRefresh),
		claimAttempts:        make(map[string]time.Time),
	}
	service.refresh = newCapabilityRefreshQueue(service.refreshProvider)
	return service
}

// startBackground starts the refresh worker and the discovery keepalive. The
// returned stop cancels and joins the keepalive before it stops the worker it
// feeds, then closes the native backends.
func (s *localConnectorService) startBackground(ctx context.Context, report func(error)) func() {
	stopRefresh := s.refresh.start(ctx, report)
	keepaliveCtx, cancelKeepalive := context.WithCancel(ctx)
	ticks, stopTicks := s.keepaliveTicks()
	var keepalive sync.WaitGroup
	keepalive.Add(1)
	go func() {
		defer keepalive.Done()
		defer stopTicks()
		for {
			select {
			case <-keepaliveCtx.Done():
				return
			case <-ticks:
				s.scheduleStaleRefresh()
			}
		}
	}()
	return func() {
		cancelKeepalive()
		keepalive.Wait()
		stopRefresh()
		s.closeBackends()
	}
}

// scheduleStaleRefresh re-observes, from the keepalive tick, every provider
// whose cloud freshness is aging out, at most once per
// connectorKeepaliveAttemptFloor per provider.
func (s *localConnectorService) scheduleStaleRefresh() {
	for _, provider := range s.dueKeepaliveProviders(s.now(), true) {
		_ = s.refresh.schedule(provider, capabilityRefreshTypeReobserve)
	}
}

// scheduleReconnectRefresh re-observes every provider whose cloud freshness is
// aging out when the wakeup socket reconnects, because claims resume at once
// after a network gap or a sleep. The reconnect ends the gap that most likely
// failed the last keepalive attempt, so it is not held to the attempt floor:
// the socket's reconnect backoff paces it, and a re-observation within the
// floor of the last Discover reuses that observation.
func (s *localConnectorService) scheduleReconnectRefresh() {
	for _, provider := range s.dueKeepaliveProviders(s.now(), false) {
		_ = s.refresh.schedule(provider, capabilityRefreshTypeReobserve)
	}
}

// dueKeepaliveProviders returns, in order, the providers with a backend and at
// least one assignment whose last confirmed observation is older than the
// jittered keepalive interval, and records each as attempted. A paced caller
// skips a provider attempted within connectorKeepaliveAttemptFloor. Ages are
// wall-clock: a monotonic reading stops while the machine sleeps.
func (s *localConnectorService) dueKeepaliveProviders(now time.Time, paced bool) []string {
	now = now.Round(0)
	s.mu.RLock()
	aged := make([]string, 0, len(s.runtimes))
	for provider, assignments := range s.runtimes {
		if s.backends[provider] == nil {
			continue
		}
		for _, assignment := range assignments {
			if now.Sub(assignment.confirmedAt) >= jitterDuration(s.keepaliveInterval) {
				aged = append(aged, provider)
				break
			}
		}
	}
	s.mu.RUnlock()
	sort.Strings(aged)

	s.keepaliveMu.Lock()
	defer s.keepaliveMu.Unlock()
	due := aged[:0]
	for _, provider := range aged {
		if last, ok := s.keepaliveAttempts[provider]; paced && ok && withinConnectorKeepaliveFloor(now, last) {
			continue
		}
		s.keepaliveAttempts[provider] = now
		due = append(due, provider)
	}
	return due
}

// Before a claim, a claimable Runtime whose discovery the cloud last
// confirmed more than connectorClaimFreshAge ago is re-observed first, so a
// Run claimed right after the daemon starts, the machine wakes or the socket
// reconnects freezes the apps that are really connected instead of none. The
// age sits well past the keepalive's steady-state renewal and leaves margin
// inside protocol.ConnectorDiscoveryFreshWindow for claim latency and clock
// skew. A claim waits connectorClaimRefreshWait for a provider this process
// has confirmed before, whose re-observation may list Codex's app directory
// again after a sleep (codexAppDirectoryBudget), and
// connectorClaimColdRefreshWait for one it never has, whose first discovery
// lists the whole directory with nothing to fall back on
// (codexConnectorReadTimeout); both leave time for the publish.
// connectorClaimRefreshLimit bounds a re-observation the claim stopped
// waiting for, which finishes in the background.
const (
	connectorClaimFreshAge        = 6 * time.Minute
	connectorClaimRefreshWait     = codexAppDirectoryBudget + 10*time.Second
	connectorClaimColdRefreshWait = codexConnectorReadTimeout + 15*time.Second
	connectorClaimRefreshLimit    = 2 * time.Minute
)

var errConnectorClaimRefreshPending = errors.New("connector discovery is still being confirmed")

// ensureFreshForClaim re-observes, and waits for the cloud to confirm, every
// provider with one of runtimeIDs whose confirmation is older than
// connectorClaimFreshAge. Runtimes that are fresh never wait. It fails open:
// the error it returns is for the log, and the caller claims regardless,
// exactly as it would have without the guard. A claim that finds a
// provider's re-observation still running waits on that one rather than
// starting another, and a provider is attempted again only a keepalive floor
// after its last attempt ended, so a failing or slow Discover holds claims
// only while one attempt runs and then leaves a floor of claims unheld.
func (s *localConnectorService) ensureFreshForClaim(ctx context.Context, runtimeIDs []string) error {
	if s == nil || len(runtimeIDs) == 0 {
		return nil
	}
	now := s.now().UTC().Round(0)
	s.mu.RLock()
	var stale []string
	// cold marks a stale provider this process has never confirmed for one
	// of runtimeIDs, or has no catalog for: its first discovery is the slow one.
	cold := make(map[string]bool)
	for provider, assignments := range s.runtimes {
		if s.backends[provider] == nil {
			continue
		}
		_, known := s.catalogs[provider]
		due, unconfirmed := false, false
		for _, runtimeID := range runtimeIDs {
			assignment, ok := assignments[runtimeID]
			if !ok {
				continue
			}
			due = due || now.Sub(assignment.confirmedAt) >= connectorClaimFreshAge
			unconfirmed = unconfirmed || assignment.confirmedAt.IsZero()
		}
		if due {
			stale = append(stale, provider)
			cold[provider] = !known || unconfirmed
		}
	}
	s.mu.RUnlock()
	sort.Strings(stale)

	type claimWait struct {
		provider string
		refresh  *connectorClaimRefresh
		wait     time.Duration
	}
	s.keepaliveMu.Lock()
	waits := make([]claimWait, 0, len(stale))
	for _, provider := range stale {
		refresh := s.claimRefreshes[provider]
		if refresh == nil {
			if last, ok := s.claimAttempts[provider]; ok && withinConnectorKeepaliveFloor(now, last) {
				continue
			}
			refresh = &connectorClaimRefresh{done: make(chan struct{})}
			s.claimRefreshes[provider] = refresh
			go s.runClaimRefresh(ctx, provider, refresh)
		}
		wait := s.claimRefreshWait
		if cold[provider] {
			wait = s.claimColdRefreshWait
		}
		waits = append(waits, claimWait{provider: provider, refresh: refresh, wait: wait})
	}
	s.keepaliveMu.Unlock()

	started := time.Now()
	var refreshErrors []error
	for _, claim := range waits {
		timer := time.NewTimer(claim.wait - time.Since(started))
		select {
		case <-claim.refresh.done:
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
		timer.Stop()
		select {
		case <-claim.refresh.done:
			if claim.refresh.err != nil {
				refreshErrors = append(refreshErrors, claim.refresh.err)
			}
		default:
			refreshErrors = append(
				refreshErrors, fmt.Errorf("%s: %w", claim.provider, errConnectorClaimRefreshPending),
			)
		}
	}
	return errors.Join(refreshErrors...)
}

// runClaimRefresh re-observes provider for claims under its own deadline, not
// a claim's wait: a re-observation the claim stops waiting for still
// completes and renews freshness. Its end starts the provider's attempt floor.
func (s *localConnectorService) runClaimRefresh(
	ctx context.Context,
	provider string,
	refresh *connectorClaimRefresh,
) {
	refreshCtx, cancel := context.WithTimeout(ctx, connectorClaimRefreshLimit)
	defer cancel()
	err := s.refreshProvider(refreshCtx, provider, capabilityRefreshTypeReobserve)
	if err != nil {
		err = fmt.Errorf("%s: %w", provider, err)
	}
	s.keepaliveMu.Lock()
	refresh.err = err
	delete(s.claimRefreshes, provider)
	s.claimAttempts[provider] = s.now().UTC().Round(0)
	s.keepaliveMu.Unlock()
	close(refresh.done)
}

func (s *localConnectorService) setBackend(provider string, backend nativeConnectorBackend) {
	s.mu.Lock()
	previous := s.backends[provider]
	if backend == nil {
		delete(s.backends, provider)
		delete(s.catalogs, provider)
		delete(s.catalogObservedAt, provider)
		delete(s.runtimes, provider)
	} else {
		s.backends[provider] = backend
		delete(s.catalogs, provider)
		delete(s.catalogObservedAt, provider)
	}
	s.mu.Unlock()
	closeNativeConnectorBackend(previous)
	if lifecycle, ok := backend.(nativeConnectorLifecycle); ok {
		lifecycle.SetChangeHandler(func() { _ = s.refresh.schedule(provider, capabilityRefreshTypeForced) })
	}
	// A linked refresh rediscovers on the directory that just landed without
	// listing it again, then publishes the full catalog.
	if notifier, ok := backend.(nativeConnectorDirectoryNotifier); ok {
		notifier.SetDirectoryHandler(func() { _ = s.refresh.schedule(provider, capabilityRefreshTypeLinked) })
	}
}

func (s *localConnectorService) replaceRuntimes(runtimes []Runtime) {
	next := make(map[string]map[string]connectorRuntimeAssignment)
	s.mu.Lock()
	for _, runtime := range runtimes {
		if _, supported := s.backends[runtime.Provider]; !supported {
			continue
		}
		if next[runtime.Provider] == nil {
			next[runtime.Provider] = make(map[string]connectorRuntimeAssignment)
		}
		assignment := connectorRuntimeAssignment{companyID: runtime.CompanyID, runtimeID: runtime.ID}
		if previous, ok := s.runtimes[runtime.Provider][runtime.ID]; ok &&
			previous.companyID == runtime.CompanyID {
			assignment = previous
		}
		next[runtime.Provider][runtime.ID] = assignment
	}
	changed := make([]string, 0, len(s.backends))
	for provider := range s.backends {
		if !sameConnectorRuntimeAssignments(s.runtimes[provider], next[provider]) {
			changed = append(changed, provider)
		}
	}
	s.runtimes = next
	s.mu.Unlock()
	sort.Strings(changed)
	for _, provider := range changed {
		_ = s.refresh.schedule(provider, capabilityRefreshTypeChanged)
	}
}

// discoverNativeConnectors runs the Discover a refresh type asks for: a
// linked read where the backend has one, else a forced read for a linked or
// forced refresh and a cached read for the rest.
func discoverNativeConnectors(
	ctx context.Context,
	backend nativeConnectorBackend,
	refreshType capabilityRefreshType,
) ([]nativeConnectorItem, error) {
	if refreshType == capabilityRefreshTypeLinked {
		if linked, ok := backend.(nativeConnectorLinkedBackend); ok {
			return linked.DiscoverLinked(ctx)
		}
	}
	return backend.Discover(
		ctx,
		refreshType == capabilityRefreshTypeForced || refreshType == capabilityRefreshTypeLinked,
	)
}

// refreshProvider brings every assignment of one provider up to date.
//
// A changed refresh reuses the cached catalog and publishes only where the
// digest differs. A reobserve, linked or forced refresh runs Discover —
// bypassing the provider's caches when forced, and only its record of linked
// accounts when linked — and then renews freshness wherever the
// cloud holds an older observation: an unchanged manifest reaffirms the
// generation the cloud already holds, and anything else is a full publish
// under a new generation. A reobserve within connectorKeepaliveAttemptFloor
// of the last Discover reuses that observation, so a re-observation queued
// behind another, or a reconnect after a failed send, does not run Discover
// twice. Every manifest and reaffirmation carries the time Discover ran. A
// failure leaves the assignment as it was, so the next refresh retries it.
func (s *localConnectorService) refreshProvider(
	ctx context.Context,
	provider string,
	refreshType capabilityRefreshType,
) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.RLock()
	backend := s.backends[provider]
	items, known := s.catalogs[provider]
	items = cloneNativeConnectorItems(items)
	observedAt := s.catalogObservedAt[provider]
	s.mu.RUnlock()
	if backend == nil {
		return errConnectorsUnavailable
	}
	now := s.now().UTC().Round(0)
	if !known || refreshType == capabilityRefreshTypeForced || refreshType == capabilityRefreshTypeLinked ||
		refreshType == capabilityRefreshTypeReobserve && !withinConnectorKeepaliveFloor(now, observedAt) {
		discovered, err := discoverNativeConnectors(ctx, backend, refreshType)
		if err != nil {
			return fmt.Errorf("discover %s connectors: %w", provider, err)
		}
		items, observedAt = canonicalConnectorItems(discovered), now
		s.mu.Lock()
		s.catalogs[provider] = cloneNativeConnectorItems(items)
		s.catalogObservedAt[provider] = observedAt
		s.mu.Unlock()
	}
	manifestItems := connectorManifestItems(items)
	digest := connectorManifestDigest(manifestItems)
	if digest == "" || len(manifestItems) > maximumConnectorDiscoveryItems {
		return errors.New("connector discovery manifest is invalid")
	}
	s.mu.RLock()
	assignments := make([]connectorRuntimeAssignment, 0, len(s.runtimes[provider]))
	for _, assignment := range s.runtimes[provider] {
		if assignment.publishedHash != digest ||
			refreshType != capabilityRefreshTypeChanged && assignment.confirmedAt.Before(observedAt) {
			assignments = append(assignments, assignment)
		}
	}
	s.mu.RUnlock()
	sort.Slice(assignments, func(left, right int) bool {
		return assignments[left].runtimeID < assignments[right].runtimeID
	})
	var refreshErrors []error
	for _, assignment := range assignments {
		if assignment.publishedHash == digest && assignment.publishedGeneration != "" {
			reaffirmed, err := s.reaffirm(ctx, provider, assignment, digest, observedAt)
			if err != nil {
				refreshErrors = append(refreshErrors, err)
				continue
			}
			if reaffirmed {
				continue
			}
		}
		if err := s.publish(ctx, provider, assignment, manifestItems, digest, observedAt); err != nil {
			refreshErrors = append(refreshErrors, err)
		}
	}
	return errors.Join(refreshErrors...)
}

// reaffirm renews the cloud freshness of the generation an assignment already
// published. It reports false with no error when the cloud refused that
// generation on its merits, so the caller publishes the whole manifest.
func (s *localConnectorService) reaffirm(
	ctx context.Context,
	provider string,
	assignment connectorRuntimeAssignment,
	digest string,
	observedAt time.Time,
) (bool, error) {
	if s.publisher != nil {
		_, err := s.publisher.reaffirmConnectorDiscovery(
			ctx, assignment.companyID, assignment.runtimeID,
			connectorDiscoveryReaffirmation{
				SchemaVersion: 1, Generation: assignment.publishedGeneration,
				ManifestDigest: digest, ObservedAt: observedAt,
			},
		)
		if connectorReaffirmationRejected(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	s.markPublished(provider, assignment, digest, assignment.publishedGeneration, observedAt)
	return true, nil
}

// publish replaces one Runtime's discovery with the whole manifest under a
// new generation.
func (s *localConnectorService) publish(
	ctx context.Context,
	provider string,
	assignment connectorRuntimeAssignment,
	items []connectorDiscoveryItem,
	digest string,
	observedAt time.Time,
) error {
	generation, err := s.generation()
	if err != nil {
		return fmt.Errorf("generate connector discovery: %w", err)
	}
	manifest := connectorDiscoveryManifest{
		SchemaVersion: 1, Generation: generation, ManifestDigest: digest,
		ObservedAt: observedAt, Items: items,
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || len(encoded) > maximumConnectorDiscoveryRequestBytes {
		return errors.New("connector discovery manifest exceeds transport limit")
	}
	if s.publisher != nil {
		if _, err = s.publisher.publishConnectorDiscovery(
			ctx, assignment.companyID, assignment.runtimeID, manifest,
		); err != nil {
			return err
		}
	}
	s.markPublished(provider, assignment, digest, generation, observedAt)
	return nil
}

func (s *localConnectorService) connect(
	ctx context.Context,
	companyID, runtimeID, provider, source, nativeID string,
) (connectorOperation, error) {
	if !s.runtimeMatches(provider, companyID, runtimeID) {
		return connectorOperation{}, errConnectorNotDiscovered
	}
	s.mu.RLock()
	backend := s.backends[provider]
	known := slices.ContainsFunc(s.catalogs[provider], func(item nativeConnectorItem) bool {
		return item.Source == source && item.NativeID == nativeID
	})
	s.mu.RUnlock()
	if backend == nil {
		return connectorOperation{}, errConnectorsUnavailable
	}
	if !known {
		return connectorOperation{}, errConnectorNotDiscovered
	}
	result, err := backend.Connect(ctx, nativeConnectorIdentity{Source: source, NativeID: nativeID})
	if err != nil {
		return connectorOperation{}, err
	}
	if result.State == "awaiting_user" && result.ContinueURL == "" {
		return connectorOperation{}, errors.New("connector operation has no continuation URL")
	}
	if result.Refresh {
		_ = s.refresh.schedule(provider, capabilityRefreshTypeForced)
	}
	return connectorOperation{SchemaVersion: 1, State: result.State, ContinueURL: result.ContinueURL}, nil
}

// status reads, live, whether a discovered connector is connected. The
// desktop polls it while someone connects an app in the browser, whose
// connect page has no callback. A connection the cached catalog does not show
// yet schedules a linked discovery, so the cloud learns of it in seconds.
func (s *localConnectorService) status(
	ctx context.Context,
	companyID, runtimeID, provider, source, nativeID string,
) (connectorStatus, error) {
	if !s.runtimeMatches(provider, companyID, runtimeID) {
		return connectorStatus{}, errConnectorNotDiscovered
	}
	s.mu.RLock()
	backend := s.backends[provider]
	cachedStatus, known := "", false
	for _, item := range s.catalogs[provider] {
		if item.Source == source && item.NativeID == nativeID {
			cachedStatus, known = item.Status, true
			break
		}
	}
	s.mu.RUnlock()
	if backend == nil {
		return connectorStatus{}, errConnectorsUnavailable
	}
	if !known {
		return connectorStatus{}, errConnectorNotDiscovered
	}
	statusBackend, ok := backend.(nativeConnectorStatusBackend)
	if !ok {
		return connectorStatus{}, errConnectorsUnavailable
	}
	connected, err := statusBackend.Connected(
		ctx,
		nativeConnectorIdentity{Source: source, NativeID: nativeID},
	)
	if err != nil {
		return connectorStatus{}, err
	}
	// Connected answers discovery's own rule, so only a catalog that already
	// shows the app connected (managed_in_provider when its tools cannot be
	// frozen) is up to date; installed_not_ready is an app it could not call.
	if connected && cachedStatus != "connected" && cachedStatus != "managed_in_provider" {
		_ = s.refresh.schedule(provider, capabilityRefreshTypeLinked)
	}
	return connectorStatus{SchemaVersion: 1, Connected: connected}, nil
}

// refreshRuntime rediscovers when forced; otherwise it reobserves, which
// reuses a catalog still inside the keepalive floor and renews its freshness.
func (s *localConnectorService) refreshRuntime(
	ctx context.Context,
	companyID, runtimeID, provider string,
	force bool,
) (connectorOperation, error) {
	if !s.runtimeMatches(provider, companyID, runtimeID) {
		return connectorOperation{}, errConnectorNotDiscovered
	}
	refreshType := capabilityRefreshTypeReobserve
	if force {
		refreshType = capabilityRefreshTypeForced
	}
	if err := s.refreshProvider(ctx, provider, refreshType); err != nil {
		return connectorOperation{}, err
	}
	return connectorOperation{SchemaVersion: 1, State: "complete"}, nil
}

func (s *localConnectorService) resolveRunExecution(
	ctx context.Context,
	companyID, runtimeID, provider string,
	snapshot *RunConnectorSnapshot,
) (connectorExecution, error) {
	if snapshot == nil || provider == "codex" {
		return connectorExecution{}, nil
	}
	if err := validateRunConnectorSnapshot(*snapshot, provider); err != nil ||
		!s.runtimeMatches(provider, companyID, runtimeID) {
		return connectorExecution{}, errClaudeConnectorContractDrift
	}
	s.mu.RLock()
	backend, ok := s.backends[provider].(claudeNativeConnectorBackend)
	s.mu.RUnlock()
	if !ok || backend.backend == nil {
		return connectorExecution{}, errClaudeConnectorContractDrift
	}
	selections := make([]claudeConnectorSelection, len(snapshot.Connectors))
	for index, connector := range snapshot.Connectors {
		var kind ClaudeConnectorKind
		switch connector.Source {
		case "claude_plugin":
			kind = ClaudeConnectorKindPlugin
		case "claude_mcp":
			kind = ClaudeConnectorKindMCP
		default:
			return connectorExecution{}, errClaudeConnectorContractDrift
		}
		selections[index] = claudeConnectorSelection{
			Kind: kind, NativeID: connector.NativeID,
			ContractDigest: connector.ContractDigest,
			Tools:          append([]agent.ConnectorTool(nil), connector.Tools...),
		}
	}
	resolved, err := backend.backend.resolveExecution(ctx, selections)
	if err != nil {
		return connectorExecution{}, err
	}
	return connectorExecution{
		PluginDirectories: append([]string(nil), resolved.PluginDirectories...),
		MCPServers:        cloneRawMessages(resolved.MCPServers),
		AllowedMCPServers: append([]string(nil), resolved.AllowedMCPServers...),
	}, nil
}

func connectorManifestItems(items []nativeConnectorItem) []connectorDiscoveryItem {
	result := make([]connectorDiscoveryItem, len(items))
	for index, item := range items {
		result[index] = connectorDiscoveryItem{
			Source: item.Source, NativeID: item.NativeID, Name: item.Name,
			Description: item.Description, Category: item.Category, IconURL: item.IconURL,
			InstallCount: item.InstallCount, Status: item.Status, Action: item.Action,
			CanExecute: item.CanExecute, ContractDigest: item.ContractDigest,
			Tools: append([]agent.ConnectorTool(nil), item.Tools...),
		}
	}
	return result
}

func canonicalConnectorItems(items []nativeConnectorItem) []nativeConnectorItem {
	result := cloneNativeConnectorItems(items)
	sort.Slice(result, func(left, right int) bool {
		if result[left].Source == result[right].Source {
			return result[left].NativeID < result[right].NativeID
		}
		return result[left].Source < result[right].Source
	})
	return result
}

func cloneNativeConnectorItems(items []nativeConnectorItem) []nativeConnectorItem {
	result := append([]nativeConnectorItem(nil), items...)
	for index := range result {
		result[index].Tools = append([]agent.ConnectorTool(nil), items[index].Tools...)
		if items[index].InstallCount != nil {
			value := *items[index].InstallCount
			result[index].InstallCount = &value
		}
	}
	return result
}

func cloneRawMessages(source map[string]json.RawMessage) map[string]json.RawMessage {
	if source == nil {
		return nil
	}
	result := make(map[string]json.RawMessage, len(source))
	for name, value := range source {
		result[name] = append(json.RawMessage(nil), value...)
	}
	return result
}

func (s *localConnectorService) runtimeMatches(provider, companyID, runtimeID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	assignment, ok := s.runtimes[provider][runtimeID]
	return ok && assignment.companyID == companyID
}

// iconURLs maps each connector of one source in the provider's cached catalog
// to its sanitized icon URL. Connectors without one are absent.
func (s *localConnectorService) iconURLs(provider, source string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	icons := make(map[string]string)
	for _, item := range s.catalogs[provider] {
		if item.Source == source && item.IconURL != "" {
			icons[item.NativeID] = item.IconURL
		}
	}
	return icons
}

// markPublished records what the cloud accepted for an assignment that still
// exists for the same Company; a concurrent runtime-set change wins.
func (s *localConnectorService) markPublished(
	provider string,
	published connectorRuntimeAssignment,
	digest, generation string,
	observedAt time.Time,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	assignment, ok := s.runtimes[provider][published.runtimeID]
	if !ok || assignment.companyID != published.companyID {
		return
	}
	assignment.publishedHash = digest
	assignment.publishedGeneration = generation
	assignment.confirmedAt = observedAt
	s.runtimes[provider][published.runtimeID] = assignment
}

func sameConnectorRuntimeAssignments(left, right map[string]connectorRuntimeAssignment) bool {
	if len(left) != len(right) {
		return false
	}
	for runtimeID, leftAssignment := range left {
		rightAssignment, ok := right[runtimeID]
		if !ok || leftAssignment.companyID != rightAssignment.companyID {
			return false
		}
	}
	return true
}

func (s *localConnectorService) closeBackends() {
	s.mu.RLock()
	backends := make([]nativeConnectorBackend, 0, len(s.backends))
	for _, backend := range s.backends {
		backends = append(backends, backend)
	}
	s.mu.RUnlock()
	for _, backend := range backends {
		closeNativeConnectorBackend(backend)
	}
}

func closeNativeConnectorBackend(backend nativeConnectorBackend) {
	if lifecycle, ok := backend.(nativeConnectorLifecycle); ok {
		lifecycle.Close()
	}
}

type claudeNativeConnectorBackend struct{ backend *ClaudeConnectorBackend }

func (b claudeNativeConnectorBackend) Discover(ctx context.Context, _ bool) ([]nativeConnectorItem, error) {
	catalog, err := b.backend.Discover(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]nativeConnectorItem, 0, len(catalog.Connectors))
	for _, connector := range catalog.Connectors {
		item := nativeConnectorItem{
			NativeID: connector.ID, Name: connector.Name, CanExecute: connector.CanExecute,
			ContractDigest: connector.ContractDigest,
			Tools:          append([]agent.ConnectorTool(nil), connector.Tools...),
		}
		switch connector.Kind {
		case ClaudeConnectorKindPlugin:
			item.Source, item.Category, item.InstallCount = "claude_plugin", "Claude plugins", connector.InstallCount
		case ClaudeConnectorKindMCP:
			item.Source, item.Category = "claude_mcp", "Standalone MCP"
		case ClaudeConnectorKindCloud:
			item.Source, item.Category = "claude_cloud", "Claude cloud"
		default:
			continue
		}
		if connector.Kind == ClaudeConnectorKindMCP {
			item.Status, item.Action = "managed_in_provider", "manage_in_provider"
			if connector.CanExecute {
				item.Status = "connected"
			}
		} else {
			item.Status, item.Action = mapClaudeConnectorState(
				connector.Status,
				connector.Kind == ClaudeConnectorKindCloud && !catalog.CloudReusable,
			)
		}
		items = append(items, enforceSelectiveConnectorExecution(item))
	}
	return items, nil
}

func (b claudeNativeConnectorBackend) Connect(
	ctx context.Context,
	identity nativeConnectorIdentity,
) (nativeConnectorOperation, error) {
	result, err := b.backend.Connect(ctx, identity.NativeID)
	if errors.Is(err, errClaudeConnectorNotDiscovered) {
		return nativeConnectorOperation{}, errConnectorNotDiscovered
	}
	if err != nil {
		return nativeConnectorOperation{}, err
	}
	operation := nativeConnectorOperation{State: "complete", Refresh: true}
	if result.AuthorizationURL != "" {
		operation.State, operation.ContinueURL = "awaiting_user", result.AuthorizationURL
	} else if result.Status == ClaudeConnectorStatusManagedInProvider {
		operation.State = "awaiting_user"
		if identity.Source == "claude_cloud" {
			operation.ContinueURL = claudeCloudConnectorManagementURL
		} else {
			operation.ContinueURL = claudeLocalConnectorManagementURL
		}
	}
	return operation, nil
}

func mapClaudeConnectorState(status ClaudeConnectorStatus, unavailable bool) (string, string) {
	if unavailable {
		return "unavailable", "none"
	}
	switch status {
	case ClaudeConnectorStatusAvailable:
		return "available", "install_and_auth"
	case ClaudeConnectorStatusInstalled:
		return "installed_not_ready", "manage_in_provider"
	case ClaudeConnectorStatusConnected:
		return "connected", "manage_in_provider"
	case ClaudeConnectorStatusNeedsAuthentication:
		return "needs_auth", "manage_in_provider"
	case ClaudeConnectorStatusManagedInProvider:
		return "managed_in_provider", "manage_in_provider"
	default:
		return "error", "none"
	}
}

type codexNativeConnectorBackend struct{ backend *CodexConnectorBackend }

func (b codexNativeConnectorBackend) SetChangeHandler(handler func()) {
	b.backend.SetChangeHandler(handler)
}
func (b codexNativeConnectorBackend) Close() { b.backend.Close() }

func (b codexNativeConnectorBackend) Discover(
	ctx context.Context,
	force bool,
) ([]nativeConnectorItem, error) {
	snapshot, err := b.backend.Discover(ctx, force)
	if err != nil {
		return nil, err
	}
	return codexNativeConnectorItems(snapshot), nil
}

func (b codexNativeConnectorBackend) DiscoverLinked(ctx context.Context) ([]nativeConnectorItem, error) {
	snapshot, err := b.backend.DiscoverLinked(ctx)
	if err != nil {
		return nil, err
	}
	return codexNativeConnectorItems(snapshot), nil
}

func codexNativeConnectorItems(snapshot CodexConnectorSnapshot) []nativeConnectorItem {
	items := make([]nativeConnectorItem, 0, len(snapshot.Connectors))
	for _, connector := range snapshot.Connectors {
		item := nativeConnectorItem{
			Source: string(connector.Source), NativeID: connector.NativeID,
			Name: connector.Name, Description: connector.Description,
			Category: connector.Category, IconURL: connector.IconURL,
			Status: string(connector.Status), Action: string(connector.Action),
			CanExecute: connector.CanExecute, ContractDigest: connector.ContractDigest,
			Tools: append([]agent.ConnectorTool(nil), connector.Tools...),
		}
		items = append(items, enforceSelectiveConnectorExecution(item))
	}
	return items
}

func (b codexNativeConnectorBackend) Connect(
	ctx context.Context,
	identity nativeConnectorIdentity,
) (nativeConnectorOperation, error) {
	result, err := b.backend.Connect(ctx, CodexConnectorSource(identity.Source), identity.NativeID)
	if errors.Is(err, ErrCodexConnectorNotFound) || errors.Is(err, ErrCodexConnectorNotConnectable) {
		return nativeConnectorOperation{}, errConnectorNotDiscovered
	}
	if err != nil {
		return nativeConnectorOperation{}, err
	}
	operation := nativeConnectorOperation{State: "complete"}
	if result.AuthorizationURL != "" {
		operation.State, operation.ContinueURL = "awaiting_user", result.AuthorizationURL
	}
	return operation, nil
}

// Connected answers for hosted apps only; a Codex MCP server's sign-in is
// Codex's own OAuth flow, which reports its completion to Connect.
func (b codexNativeConnectorBackend) Connected(
	ctx context.Context,
	identity nativeConnectorIdentity,
) (bool, error) {
	if identity.Source != string(CodexConnectorSourceHostedApp) {
		return false, errConnectorsUnavailable
	}
	connected, err := b.backend.HostedAppConnected(ctx, identity.NativeID)
	if err != nil {
		return false, errConnectorsUnavailable
	}
	return connected, nil
}

func enforceSelectiveConnectorExecution(item nativeConnectorItem) nativeConnectorItem {
	selective := item.Source == "codex_app" || item.Source == "claude_plugin" || item.Source == "claude_mcp"
	if selective {
		digest, err := agent.ConnectorToolContractDigest(item.Tools)
		item.CanExecute = item.CanExecute && item.Status == "connected" && err == nil &&
			digest == item.ContractDigest
	} else {
		item.CanExecute = false
	}
	if item.CanExecute {
		return item
	}
	item.ContractDigest = ""
	item.Tools = nil
	if item.Status == "connected" {
		item.Status = "managed_in_provider"
		if item.Source == "codex_mcp" {
			item.Action = "none"
		} else {
			item.Action = "manage_in_provider"
		}
	}
	return item
}
