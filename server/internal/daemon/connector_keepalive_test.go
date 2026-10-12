package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
)

type connectorPublisherCall struct {
	method     string
	companyID  string
	runtimeID  string
	generation string
	digest     string
	observedAt time.Time
	items      int
}

// scriptedConnectorPublisher records every publish and reaffirmation, and
// fails them with the configured error.
type scriptedConnectorPublisher struct {
	mu          sync.Mutex
	calls       []connectorPublisherCall
	putErr      error
	reaffirmErr error
}

func (p *scriptedConnectorPublisher) publishConnectorDiscovery(
	_ context.Context,
	companyID, runtimeID string,
	manifest connectorDiscoveryManifest,
) (discoveryPublishResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, connectorPublisherCall{
		method: "put", companyID: companyID, runtimeID: runtimeID, generation: manifest.Generation,
		digest: manifest.ManifestDigest, observedAt: manifest.ObservedAt, items: len(manifest.Items),
	})
	if p.putErr != nil {
		return discoveryPublishResult{}, p.putErr
	}
	return discoveryPublishResult{Applied: true}, nil
}

func (p *scriptedConnectorPublisher) reaffirmConnectorDiscovery(
	_ context.Context,
	companyID, runtimeID string,
	reaffirmation connectorDiscoveryReaffirmation,
) (discoveryPublishResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, connectorPublisherCall{
		method: "reaffirm", companyID: companyID, runtimeID: runtimeID,
		generation: reaffirmation.Generation, digest: reaffirmation.ManifestDigest,
		observedAt: reaffirmation.ObservedAt,
	})
	if p.reaffirmErr != nil {
		return discoveryPublishResult{}, p.reaffirmErr
	}
	return discoveryPublishResult{Applied: true}, nil
}

func (p *scriptedConnectorPublisher) recorded() []connectorPublisherCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

// scriptedConnectorBackend serves a mutable catalog and records every
// Discover's force flag.
type scriptedConnectorBackend struct {
	mu     sync.Mutex
	items  []nativeConnectorItem
	err    error
	forces []bool
}

func (b *scriptedConnectorBackend) Discover(_ context.Context, force bool) ([]nativeConnectorItem, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.forces = append(b.forces, force)
	if b.err != nil {
		return nil, b.err
	}
	return cloneNativeConnectorItems(b.items), nil
}

func (*scriptedConnectorBackend) Connect(
	context.Context,
	nativeConnectorIdentity,
) (nativeConnectorOperation, error) {
	return nativeConnectorOperation{State: "complete"}, nil
}

func (b *scriptedConnectorBackend) set(items []nativeConnectorItem, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items, b.err = items, err
}

func (b *scriptedConnectorBackend) discovered() []bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.forces)
}

type keepaliveClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *keepaliveClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *keepaliveClock) advance(step time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(step)
	return c.now
}

func connectedCodexApp(t *testing.T, nativeID, name string, toolNames ...string) nativeConnectorItem {
	t.Helper()
	tools := make([]agent.ConnectorTool, len(toolNames))
	for index, toolName := range toolNames {
		tools[index] = agent.ConnectorTool{Name: toolName, Enabled: true, ReadOnly: true}
	}
	digest, err := agent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatalf("ConnectorToolContractDigest() error = %v", err)
	}
	return nativeConnectorItem{
		Source: "codex_app", NativeID: nativeID, Name: name, Status: "connected",
		Action: "none", CanExecute: true, ContractDigest: digest, Tools: tools,
	}
}

type keepaliveFixture struct {
	service   *localConnectorService
	backend   *scriptedConnectorBackend
	publisher *scriptedConnectorPublisher
	clock     *keepaliveClock
	start     time.Time
}

func newKeepaliveFixture(t *testing.T) keepaliveFixture {
	t.Helper()
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	fixture := keepaliveFixture{
		backend: &scriptedConnectorBackend{items: []nativeConnectorItem{
			connectedCodexApp(t, "connector_gmail", "Gmail", "read_email", "search_emails"),
		}},
		publisher: &scriptedConnectorPublisher{},
		clock:     &keepaliveClock{now: start},
		start:     start,
	}
	fixture.service = newLocalConnectorService(fixture.publisher)
	fixture.service.setBackend("codex", fixture.backend)
	fixture.service.runtimes["codex"] = map[string]connectorRuntimeAssignment{
		"runtime-1": {companyID: "company-1", runtimeID: "runtime-1"},
	}
	fixture.service.now = fixture.clock.Now
	var generations atomic.Int64
	fixture.service.generation = func() (string, error) {
		return fmt.Sprintf("generation-%d", generations.Add(1)), nil
	}
	return fixture
}

func (f keepaliveFixture) assignment(t *testing.T, runtimeID string) connectorRuntimeAssignment {
	t.Helper()
	f.service.mu.RLock()
	defer f.service.mu.RUnlock()
	assignment, ok := f.service.runtimes["codex"][runtimeID]
	if !ok {
		t.Fatalf("assignment %s missing", runtimeID)
	}
	return assignment
}

// publishInitial runs the first publication a new runtime assignment gets and
// returns its manifest digest.
func (f keepaliveFixture) publishInitial(t *testing.T) string {
	t.Helper()
	if err := f.service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeChanged); err != nil {
		t.Fatalf("initial refreshProvider() error = %v", err)
	}
	calls := f.publisher.recorded()
	if len(calls) != 1 || calls[0].method != "put" || calls[0].generation != "generation-1" ||
		!calls[0].observedAt.Equal(f.start) {
		t.Fatalf("initial publication = %+v", calls)
	}
	assignment := f.assignment(t, "runtime-1")
	if assignment.publishedGeneration != "generation-1" || !assignment.confirmedAt.Equal(f.start) ||
		assignment.publishedHash != calls[0].digest {
		t.Fatalf("initial assignment = %+v", assignment)
	}
	return calls[0].digest
}

func TestConnectorReobserveReaffirmsAnUnchangedGeneration(t *testing.T) {
	t.Parallel()

	fixture := newKeepaliveFixture(t)
	digest := fixture.publishInitial(t)
	observedAt := fixture.clock.advance(3 * time.Minute)

	if err := fixture.service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeReobserve); err != nil {
		t.Fatalf("reobserve refreshProvider() error = %v", err)
	}
	if forces := fixture.backend.discovered(); !slices.Equal(forces, []bool{false, false}) {
		t.Fatalf("Discover forces = %v, want a non-forced re-observation", forces)
	}
	calls := fixture.publisher.recorded()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want one reaffirmation after the initial publish", calls)
	}
	reaffirmation := calls[1]
	if reaffirmation.method != "reaffirm" || reaffirmation.companyID != "company-1" ||
		reaffirmation.runtimeID != "runtime-1" || reaffirmation.generation != "generation-1" ||
		reaffirmation.digest != digest || !reaffirmation.observedAt.Equal(observedAt) {
		t.Fatalf("reaffirmation = %+v, want generation-1 %s observed at %v", reaffirmation, digest, observedAt)
	}
	assignment := fixture.assignment(t, "runtime-1")
	if assignment.publishedGeneration != "generation-1" || !assignment.confirmedAt.Equal(observedAt) {
		t.Fatalf("assignment after reaffirm = %+v", assignment)
	}
}

func TestConnectorForcedRefreshReaffirmsWhenNothingChanged(t *testing.T) {
	t.Parallel()

	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)
	fixture.clock.advance(time.Minute)

	if _, err := fixture.service.refreshRuntime(t.Context(), "company-1", "runtime-1", "codex", true); err != nil {
		t.Fatalf("refreshRuntime() error = %v", err)
	}
	if forces := fixture.backend.discovered(); !slices.Equal(forces, []bool{false, true}) {
		t.Fatalf("Discover forces = %v, want the user refresh to bypass provider caches", forces)
	}
	calls := fixture.publisher.recorded()
	if len(calls) != 2 || calls[1].method != "reaffirm" || calls[1].generation != "generation-1" {
		t.Fatalf("calls = %+v, want an unchanged forced refresh to reaffirm instead of rewriting", calls)
	}
}

func TestConnectorReobservePublishesAChangedManifest(t *testing.T) {
	t.Parallel()

	fixture := newKeepaliveFixture(t)
	digest := fixture.publishInitial(t)
	fixture.backend.set([]nativeConnectorItem{
		connectedCodexApp(t, "connector_gmail", "Gmail", "read_email", "search_emails"),
		connectedCodexApp(t, "connector_calendar", "Google Calendar", "list_events"),
	}, nil)
	observedAt := fixture.clock.advance(3 * time.Minute)

	if err := fixture.service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeReobserve); err != nil {
		t.Fatalf("reobserve refreshProvider() error = %v", err)
	}
	calls := fixture.publisher.recorded()
	if len(calls) != 2 || calls[1].method != "put" || calls[1].generation != "generation-2" ||
		calls[1].digest == digest || calls[1].items != 2 || !calls[1].observedAt.Equal(observedAt) {
		t.Fatalf("calls = %+v, want a full publish of the changed manifest", calls)
	}
	assignment := fixture.assignment(t, "runtime-1")
	if assignment.publishedGeneration != "generation-2" || assignment.publishedHash != calls[1].digest ||
		!assignment.confirmedAt.Equal(observedAt) {
		t.Fatalf("assignment after changed publish = %+v", assignment)
	}
}

func TestConnectorRejectedReaffirmationFallsBackToAFullPublish(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusNotFound, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			fixture := newKeepaliveFixture(t)
			digest := fixture.publishInitial(t)
			fixture.publisher.reaffirmErr = newRequestError(
				http.MethodPost, "/reaffirm", status, `{"code":"connector_discovery_conflict"}`,
			)
			observedAt := fixture.clock.advance(3 * time.Minute)

			if err := fixture.service.refreshProvider(
				t.Context(), "codex", capabilityRefreshTypeReobserve,
			); err != nil {
				t.Fatalf("refreshProvider() error = %v", err)
			}
			calls := fixture.publisher.recorded()
			if len(calls) != 3 || calls[1].method != "reaffirm" || calls[2].method != "put" ||
				calls[2].generation != "generation-2" || calls[2].digest != digest ||
				!calls[2].observedAt.Equal(observedAt) {
				t.Fatalf("calls = %+v, want reaffirm then a full publish under a new generation", calls)
			}
			assignment := fixture.assignment(t, "runtime-1")
			if assignment.publishedGeneration != "generation-2" ||
				!assignment.confirmedAt.Equal(observedAt) {
				t.Fatalf("assignment after fallback = %+v", assignment)
			}
		})
	}
}

func TestConnectorChangedRefreshSkipsAnUnchangedDigest(t *testing.T) {
	t.Parallel()

	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)
	fixture.clock.advance(time.Minute)

	if err := fixture.service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeChanged); err != nil {
		t.Fatalf("refreshProvider() error = %v", err)
	}
	if forces := fixture.backend.discovered(); len(forces) != 1 {
		t.Fatalf("Discover forces = %v, want the cached catalog reused", forces)
	}
	if calls := fixture.publisher.recorded(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want nothing published for an unchanged digest", calls)
	}
}

// A runtime added after discovery receives the cached catalog. Its manifest
// states when that catalog was observed, never the publish time, so a stale
// cache cannot pass itself off as a fresh observation.
func TestConnectorCachedCatalogKeepsItsObservationTime(t *testing.T) {
	t.Parallel()

	fixture := newKeepaliveFixture(t)
	digest := fixture.publishInitial(t)
	fixture.clock.advance(2 * time.Minute)
	fixture.service.mu.Lock()
	fixture.service.runtimes["codex"]["runtime-2"] = connectorRuntimeAssignment{
		companyID: "company-1", runtimeID: "runtime-2",
	}
	fixture.service.mu.Unlock()

	if err := fixture.service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeChanged); err != nil {
		t.Fatalf("refreshProvider() error = %v", err)
	}
	calls := fixture.publisher.recorded()
	if len(calls) != 2 || calls[1].method != "put" || calls[1].runtimeID != "runtime-2" ||
		calls[1].digest != digest || !calls[1].observedAt.Equal(fixture.start) {
		t.Fatalf("calls = %+v, want runtime-2 published with the catalog's observation time", calls)
	}
	if forces := fixture.backend.discovered(); len(forces) != 1 {
		t.Fatalf("Discover forces = %v, want the cached catalog reused", forces)
	}
	if assignment := fixture.assignment(t, "runtime-2"); !assignment.confirmedAt.Equal(fixture.start) {
		t.Fatalf("runtime-2 assignment = %+v, want freshness measured from the observation", assignment)
	}
}

func TestConnectorRefreshErrorsLeaveStateForRetry(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		arrange     func(t *testing.T, fixture keepaliveFixture)
		wantMethods []string
	}{
		{
			name: "discover fails",
			arrange: func(_ *testing.T, fixture keepaliveFixture) {
				fixture.backend.set(nil, errors.New("codex app-server exited"))
			},
		},
		{
			name: "reaffirm fails transiently",
			arrange: func(_ *testing.T, fixture keepaliveFixture) {
				fixture.publisher.reaffirmErr = newRequestError(
					http.MethodPost, "/reaffirm", http.StatusBadGateway, "bad gateway",
				)
			},
			wantMethods: []string{"reaffirm"},
		},
		{
			name: "full publish fails",
			arrange: func(t *testing.T, fixture keepaliveFixture) {
				t.Helper()
				fixture.backend.set([]nativeConnectorItem{
					connectedCodexApp(t, "connector_gmail", "Gmail", "read_email"),
				}, nil)
				fixture.publisher.putErr = errors.New("connection reset")
			},
			wantMethods: []string{"put"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newKeepaliveFixture(t)
			fixture.publishInitial(t)
			before := fixture.assignment(t, "runtime-1")
			test.arrange(t, fixture)
			fixture.clock.advance(3 * time.Minute)

			if err := fixture.service.refreshProvider(
				t.Context(), "codex", capabilityRefreshTypeReobserve,
			); err == nil {
				t.Fatal("refreshProvider() succeeded, want the failure reported")
			}
			if after := fixture.assignment(t, "runtime-1"); after != before {
				t.Fatalf("assignment = %+v, want %+v kept for the next attempt", after, before)
			}
			var methods []string
			for _, call := range fixture.publisher.recorded()[1:] {
				methods = append(methods, call.method)
			}
			if !slices.Equal(methods, test.wantMethods) {
				t.Fatalf("calls after the initial publish = %v, want %v", methods, test.wantMethods)
			}
		})
	}
}

func TestConnectorReplaceRuntimesCarriesPublicationState(t *testing.T) {
	t.Parallel()

	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)
	before := fixture.assignment(t, "runtime-1")

	fixture.service.replaceRuntimes([]Runtime{
		{ID: "runtime-1", CompanyID: "company-1", Provider: "codex"},
		{ID: "runtime-2", CompanyID: "company-1", Provider: "codex"},
	})
	if kept := fixture.assignment(t, "runtime-1"); kept != before {
		t.Fatalf("surviving assignment = %+v, want %+v", kept, before)
	}
	if added := fixture.assignment(t, "runtime-2"); added.publishedGeneration != "" || !added.confirmedAt.IsZero() {
		t.Fatalf("new assignment = %+v, want no publication state", added)
	}

	fixture.service.replaceRuntimes([]Runtime{{ID: "runtime-1", CompanyID: "company-2", Provider: "codex"}})
	if moved := fixture.assignment(t, "runtime-1"); moved.publishedHash != "" ||
		moved.publishedGeneration != "" || !moved.confirmedAt.IsZero() {
		t.Fatalf("assignment moved to another company = %+v, want its publication state dropped", moved)
	}
}

func TestConnectorKeepaliveSchedulesOnlyAgedProvidersAndRespectsTheFloor(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	service := newLocalConnectorService(&scriptedConnectorPublisher{})
	service.keepaliveInterval = 3 * time.Minute
	service.setBackend("codex", &scriptedConnectorBackend{})
	service.setBackend("claude", &scriptedConnectorBackend{})
	service.setBackend("idle", &scriptedConnectorBackend{})
	service.runtimes["codex"] = map[string]connectorRuntimeAssignment{
		"fresh": {companyID: "company-1", runtimeID: "fresh", confirmedAt: now.Add(-time.Minute)},
		// Beyond the +20% jitter bound, so it is due whatever the draw.
		"aged": {companyID: "company-1", runtimeID: "aged", confirmedAt: now.Add(-4 * time.Minute)},
	}
	service.runtimes["claude"] = map[string]connectorRuntimeAssignment{
		// Inside the -20% jitter bound for every check below, so never due.
		"recent": {companyID: "company-1", runtimeID: "recent", confirmedAt: now.Add(-30 * time.Second)},
	}

	if due := service.dueKeepaliveProviders(now, true); !slices.Equal(due, []string{"codex"}) {
		t.Fatalf("due = %v, want only the provider with an aged assignment", due)
	}
	if due := service.dueKeepaliveProviders(now.Add(30*time.Second), true); len(due) != 0 {
		t.Fatalf("due = %v, want the per-provider attempt floor respected", due)
	}
	if due := service.dueKeepaliveProviders(now.Add(connectorKeepaliveAttemptFloor), true); !slices.Equal(
		due, []string{"codex"},
	) {
		t.Fatalf("due = %v, want a retry once the floor elapsed", due)
	}

	service.mu.Lock()
	service.runtimes["claude"]["unpublished"] = connectorRuntimeAssignment{
		companyID: "company-1", runtimeID: "unpublished",
	}
	service.mu.Unlock()
	if due := service.dueKeepaliveProviders(now.Add(90*time.Second), true); !slices.Equal(due, []string{"claude"}) {
		t.Fatalf("due = %v, want a never-confirmed assignment treated as aged", due)
	}
}

func TestConnectorKeepaliveTickSchedulesReobserveAndStopJoins(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	service := newLocalConnectorService(&scriptedConnectorPublisher{})
	service.setBackend("codex", &scriptedConnectorBackend{})
	service.runtimes["codex"] = map[string]connectorRuntimeAssignment{
		"runtime-1": {companyID: "company-1", runtimeID: "runtime-1", confirmedAt: now.Add(-time.Hour)},
	}
	service.now = func() time.Time { return now }
	type refreshRequest struct {
		provider    string
		refreshType capabilityRefreshType
	}
	requests := make(chan refreshRequest, 4)
	service.refresh = newCapabilityRefreshQueue(
		func(_ context.Context, provider string, refreshType capabilityRefreshType) error {
			requests <- refreshRequest{provider: provider, refreshType: refreshType}
			return nil
		},
	)
	ticks := make(chan time.Time)
	var tickerStopped atomic.Bool
	service.keepaliveTicks = func() (<-chan time.Time, func()) {
		return ticks, func() { tickerStopped.Store(true) }
	}

	stop := service.startBackground(t.Context(), nil)
	ticks <- now
	select {
	case request := <-requests:
		if request.provider != "codex" || request.refreshType != capabilityRefreshTypeReobserve {
			t.Fatalf("request = %+v, want a codex re-observation", request)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("keepalive tick scheduled no refresh")
	}

	stop()
	if !tickerStopped.Load() {
		t.Fatal("stop returned before the keepalive goroutine released its ticker")
	}
	select {
	case ticks <- now:
		t.Fatal("keepalive goroutine still receives ticks after stop")
	default:
	}
}

func TestConnectorDiscoveryIntervalClampsInsideTheFreshWindow(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		configured time.Duration
		want       time.Duration
	}{
		{configured: 0, want: DefaultConnectorDiscoveryInterval},
		{configured: -time.Minute, want: DefaultConnectorDiscoveryInterval},
		{configured: 10 * time.Second, want: time.Minute},
		{configured: 2 * time.Minute, want: 2 * time.Minute},
		{configured: time.Hour, want: 3*time.Minute + 30*time.Second},
	} {
		if got := connectorDiscoveryInterval(test.configured); got != test.want {
			t.Errorf("connectorDiscoveryInterval(%v) = %v, want %v", test.configured, got, test.want)
		}
	}

	// Two jittered intervals, one tick and one Codex read: every allowed
	// cadence renews freshness even when one keepalive attempt fails.
	for _, interval := range []time.Duration{
		DefaultConnectorDiscoveryInterval, connectorDiscoveryInterval(time.Hour),
	} {
		worstCase := 2*interval*6/5 + 30*time.Second + codexConnectorReadTimeout
		if worstCase >= protocol.ConnectorDiscoveryFreshWindow {
			t.Fatalf("interval %v worst case %v does not fit the %v fresh window",
				interval, worstCase, protocol.ConnectorDiscoveryFreshWindow)
		}
	}
}

func TestLoadConfigReadsTheConnectorDiscoveryInterval(t *testing.T) {
	isolateProviderDiscovery(t, t.TempDir())
	stubLoginShellResolver(t, nil)
	t.Setenv("BOARDLY_HOME", t.TempDir())
	t.Setenv("BOARDLY_WORKSPACES_ROOT", t.TempDir())
	t.Setenv("BOARDLY_DAEMON_ID", "pinned-id")

	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{value: "", want: DefaultConnectorDiscoveryInterval},
		{value: "2m", want: 2 * time.Minute},
		{value: "20m", want: 3*time.Minute + 30*time.Second},
		{value: "5s", want: time.Minute},
	} {
		t.Setenv("BOARDLY_CONNECTOR_DISCOVERY_INTERVAL", test.value)
		cfg, err := LoadConfig(Overrides{AllowNoAgents: true})
		if err != nil {
			t.Fatalf("LoadConfig(%q) error = %v", test.value, err)
		}
		if cfg.ConnectorDiscoveryInterval != test.want {
			t.Fatalf("interval for %q = %v, want %v", test.value, cfg.ConnectorDiscoveryInterval, test.want)
		}
	}
	t.Setenv("BOARDLY_CONNECTOR_DISCOVERY_INTERVAL", "soon")
	if _, err := LoadConfig(Overrides{AllowNoAgents: true}); err == nil {
		t.Fatal("LoadConfig() accepted an invalid connector discovery interval")
	}
}

// A re-observation that follows another within the attempt floor reuses that
// observation instead of running Discover again: the one queued behind the
// first publication renews nothing, and a reconnect after a keepalive whose
// Discover succeeded but whose reaffirmation failed only resends it.
func TestConnectorReobserveReusesAnObservationWithinTheFloor(t *testing.T) {
	t.Parallel()

	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)
	fixture.clock.advance(5 * time.Second)
	if err := fixture.service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeReobserve); err != nil {
		t.Fatalf("queued reobserve refreshProvider() error = %v", err)
	}
	if forces := fixture.backend.discovered(); len(forces) != 1 {
		t.Fatalf("Discover forces = %v, want the fresh observation reused", forces)
	}
	if calls := fixture.publisher.recorded(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want nothing renewed for an assignment confirmed at this observation", calls)
	}

	fixture.publisher.mu.Lock()
	fixture.publisher.reaffirmErr = newRequestError(http.MethodPost, "/reaffirm", http.StatusBadGateway, "down")
	fixture.publisher.mu.Unlock()
	observedAt := fixture.clock.advance(3 * time.Minute)
	if err := fixture.service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeReobserve); err == nil {
		t.Fatal("keepalive refreshProvider() succeeded, want the failed reaffirmation reported")
	}
	fixture.publisher.mu.Lock()
	fixture.publisher.reaffirmErr = nil
	fixture.publisher.mu.Unlock()
	fixture.clock.advance(10 * time.Second)

	if err := fixture.service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeReobserve); err != nil {
		t.Fatalf("reconnect reobserve refreshProvider() error = %v", err)
	}
	if forces := fixture.backend.discovered(); len(forces) != 2 {
		t.Fatalf("Discover forces = %v, want the 10s-old observation reused", forces)
	}
	calls := fixture.publisher.recorded()
	last := calls[len(calls)-1]
	if len(calls) != 3 || last.method != "reaffirm" || last.generation != "generation-1" ||
		!last.observedAt.Equal(observedAt) {
		t.Fatalf("calls = %+v, want the reused observation reaffirmed with its own time", calls)
	}
	if assignment := fixture.assignment(t, "runtime-1"); !assignment.confirmedAt.Equal(observedAt) {
		t.Fatalf("assignment = %+v, want freshness renewed to the reused observation", assignment)
	}

	// Past the floor a re-observation runs Discover again.
	fixture.clock.advance(connectorKeepaliveAttemptFloor)
	if err := fixture.service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeReobserve); err != nil {
		t.Fatalf("later reobserve refreshProvider() error = %v", err)
	}
	if forces := fixture.backend.discovered(); len(forces) != 3 {
		t.Fatalf("Discover forces = %v, want a new observation past the floor", forces)
	}
}

// The attempt floor paces the keepalive tick only. A wakeup reconnect ends
// the gap that most likely failed the last attempt, so it re-observes at once;
// its attempt still paces the next tick.
func TestConnectorReconnectRefreshIsNotHeldToTheAttemptFloor(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	service := newLocalConnectorService(&scriptedConnectorPublisher{})
	service.setBackend("codex", &scriptedConnectorBackend{})
	service.runtimes["codex"] = map[string]connectorRuntimeAssignment{
		"runtime-1": {companyID: "company-1", runtimeID: "runtime-1"},
	}

	if due := service.dueKeepaliveProviders(now, true); !slices.Equal(due, []string{"codex"}) {
		t.Fatalf("tick due = %v, want codex", due)
	}
	if due := service.dueKeepaliveProviders(now.Add(10*time.Second), true); len(due) != 0 {
		t.Fatalf("tick due = %v, want the floor respected", due)
	}
	if due := service.dueKeepaliveProviders(now.Add(10*time.Second), false); !slices.Equal(due, []string{"codex"}) {
		t.Fatalf("reconnect due = %v, want codex re-observed at once", due)
	}
	if due := service.dueKeepaliveProviders(now.Add(65*time.Second), true); len(due) != 0 {
		t.Fatalf("tick due = %v, want the reconnect's attempt to pace the tick", due)
	}

	service.now = func() time.Time { return now.Add(20 * time.Second) }
	requests := make(chan capabilityRefreshType, 1)
	service.refresh = newCapabilityRefreshQueue(
		func(_ context.Context, _ string, refreshType capabilityRefreshType) error {
			requests <- refreshType
			return nil
		},
	)
	stop := service.refresh.start(t.Context(), nil)
	defer stop()
	service.scheduleReconnectRefresh()
	select {
	case refreshType := <-requests:
		if refreshType != capabilityRefreshTypeReobserve {
			t.Fatalf("reconnect refresh type = %v, want reobserve", refreshType)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reconnect scheduled no re-observation")
	}
}

// A wall clock stepped back must not leave a future attempt holding the
// floor: the next tick retries instead of waiting for the clock to catch up.
func TestConnectorKeepaliveFloorIgnoresAnAttemptInTheFuture(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	service := newLocalConnectorService(&scriptedConnectorPublisher{})
	service.setBackend("codex", &scriptedConnectorBackend{})
	service.runtimes["codex"] = map[string]connectorRuntimeAssignment{
		"runtime-1": {companyID: "company-1", runtimeID: "runtime-1"},
	}

	if due := service.dueKeepaliveProviders(now, true); !slices.Equal(due, []string{"codex"}) {
		t.Fatalf("due = %v, want codex", due)
	}
	if due := service.dueKeepaliveProviders(now.Add(-time.Hour), true); !slices.Equal(due, []string{"codex"}) {
		t.Fatalf("due after the clock stepped back = %v, want codex retried", due)
	}
}

// gatedConnectorBackend holds every Discover until release is closed, as a
// slow provider read does.
type gatedConnectorBackend struct {
	*scriptedConnectorBackend
	release chan struct{}
}

func (b gatedConnectorBackend) Discover(ctx context.Context, force bool) ([]nativeConnectorItem, error) {
	<-b.release
	return b.scriptedConnectorBackend.Discover(ctx, force)
}

func TestConnectorClaimGuardRenewsStaleDiscoveryBeforeTheClaim(t *testing.T) {
	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)
	now := fixture.clock.advance(connectorClaimFreshAge + time.Second)

	if err := fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"}); err != nil {
		t.Fatalf("ensureFreshForClaim() error = %v", err)
	}
	calls := fixture.publisher.recorded()
	if len(calls) != 2 || calls[1].method != "reaffirm" || !calls[1].observedAt.Equal(now) {
		t.Fatalf("calls = %+v, want one reaffirmation observed now", calls)
	}
	if assignment := fixture.assignment(t, "runtime-1"); !assignment.confirmedAt.Equal(now) {
		t.Fatalf("confirmedAt = %v, want %v", assignment.confirmedAt, now)
	}
}

func TestConnectorClaimGuardLeavesFreshAndUnclaimedRuntimesAlone(t *testing.T) {
	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)

	fixture.clock.advance(connectorClaimFreshAge - time.Second)
	if err := fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"}); err != nil {
		t.Fatalf("fresh ensureFreshForClaim() error = %v", err)
	}
	fixture.clock.advance(time.Minute)
	if err := fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-2"}); err != nil {
		t.Fatalf("unclaimed ensureFreshForClaim() error = %v", err)
	}
	if calls := fixture.publisher.recorded(); len(calls) != 1 {
		t.Fatalf("calls = %+v, want only the initial publication", calls)
	}
	if forces := fixture.backend.discovered(); len(forces) != 1 {
		t.Fatalf("Discover ran %d times, want only the initial one", len(forces))
	}
}

// A failing Discover must not stall the claim: the guard reports the error,
// the claim goes ahead as before, and the provider is not read again until
// the keepalive floor has passed.
func TestConnectorClaimGuardFailsOpenAndPacesAFailingDiscover(t *testing.T) {
	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)
	fixture.clock.advance(connectorClaimFreshAge + time.Second)
	fixture.backend.set(nil, errors.New("codex is signed out"))

	if err := fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"}); err == nil {
		t.Fatal("ensureFreshForClaim() error = nil, want the Discover failure")
	}
	if err := fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"}); err != nil {
		t.Fatalf("paced ensureFreshForClaim() error = %v, want nil (skipped)", err)
	}
	if forces := fixture.backend.discovered(); len(forces) != 2 {
		t.Fatalf("Discover ran %d times, want the initial read and one retry", len(forces))
	}
	fixture.clock.advance(connectorKeepaliveAttemptFloor)
	_ = fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"})
	if forces := fixture.backend.discovered(); len(forces) != 3 {
		t.Fatalf("Discover ran %d times after the floor, want 3", len(forces))
	}
}

// The server answers a daemon only for the runtimes its signed-in member
// registered (ADR 0014) and 404s any other. Every runtime this daemon keeps
// alive is one it registered, but one removed or re-owned since its last
// register is refused by the reaffirmation and by the publish that follows.
// The claim still goes ahead, the assignment keeps its state for a later
// attempt, and nothing is sent again until the keepalive floor has passed.
func TestConnectorClaimGuardFailsOpenForARuntimeTheRegistrantFenceRefuses(t *testing.T) {
	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)
	before := fixture.assignment(t, "runtime-1")
	notFound := `{"code":"runtime_not_found"}`
	fixture.publisher.reaffirmErr = newRequestError(http.MethodPost, "/reaffirm", http.StatusNotFound, notFound)
	fixture.publisher.putErr = newRequestError(http.MethodPut, "/discovery", http.StatusNotFound, notFound)
	fixture.clock.advance(connectorClaimFreshAge + time.Second)

	if err := fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"}); err == nil {
		t.Fatal("ensureFreshForClaim() error = nil, want the refused publish reported")
	}
	var methods []string
	for _, call := range fixture.publisher.recorded()[1:] {
		methods = append(methods, call.method)
	}
	if !slices.Equal(methods, []string{"reaffirm", "put"}) {
		t.Fatalf("calls after the initial publish = %v, want a reaffirmation then a full publish", methods)
	}
	if after := fixture.assignment(t, "runtime-1"); after != before {
		t.Fatalf("assignment = %+v, want %+v kept for the next attempt", after, before)
	}
	if err := fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"}); err != nil {
		t.Fatalf("paced ensureFreshForClaim() error = %v, want nil (skipped)", err)
	}
	if calls := fixture.publisher.recorded(); len(calls) != 3 {
		t.Fatalf("calls = %+v, want nothing sent again within the floor", calls)
	}
}

// A slow Discover holds the claim only for the wait; the refresh finishes in
// the background and still renews the assignment.
func TestConnectorClaimGuardStopsWaitingForASlowDiscover(t *testing.T) {
	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)
	gated := gatedConnectorBackend{scriptedConnectorBackend: fixture.backend, release: make(chan struct{})}
	fixture.service.mu.Lock()
	fixture.service.backends["codex"] = gated
	fixture.service.mu.Unlock()
	fixture.service.claimRefreshWait = 20 * time.Millisecond
	now := fixture.clock.advance(connectorClaimFreshAge + time.Second)

	started := time.Now()
	if err := fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"}); err == nil {
		t.Fatal("ensureFreshForClaim() error = nil, want the wait to lapse")
	}
	if waited := time.Since(started); waited > 2*time.Second {
		t.Fatalf("ensureFreshForClaim() waited %v", waited)
	}
	close(gated.release)
	deadline := time.Now().Add(5 * time.Second)
	for !fixture.assignment(t, "runtime-1").confirmedAt.Equal(now) {
		if time.Now().After(deadline) {
			t.Fatalf("background refresh never renewed the assignment: %+v", fixture.assignment(t, "runtime-1"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A provider this process has never confirmed is on its first discovery,
// which for Codex lists the whole app directory: a claim waits the cold
// budget for it, not the shorter one for a re-observation.
func TestConnectorClaimGuardWaitsForTheFirstDiscovery(t *testing.T) {
	fixture := newKeepaliveFixture(t)
	gated := gatedConnectorBackend{scriptedConnectorBackend: fixture.backend, release: make(chan struct{})}
	fixture.service.mu.Lock()
	fixture.service.backends["codex"] = gated
	fixture.service.mu.Unlock()
	fixture.service.claimRefreshWait = 20 * time.Millisecond
	fixture.service.claimColdRefreshWait = 10 * time.Second

	result := make(chan error, 1)
	go func() { result <- fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"}) }()
	select {
	case err := <-result:
		t.Fatalf("ensureFreshForClaim() = %v before the first discovery finished", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(gated.release)
	if err := <-result; err != nil {
		t.Fatalf("ensureFreshForClaim() error = %v, want the first discovery confirmed", err)
	}
	if assignment := fixture.assignment(t, "runtime-1"); !assignment.confirmedAt.Equal(fixture.start) {
		t.Fatalf("confirmedAt = %v, want %v", assignment.confirmedAt, fixture.start)
	}
}

// A claim that finds a re-observation still running, one an earlier claim
// stopped waiting for, waits on that one instead of claiming at once on stale
// discovery or reading the provider again. A Runtime whose discovery is fresh
// never waits.
func TestConnectorClaimGuardWaitsOnARefreshStillRunning(t *testing.T) {
	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)
	gated := gatedConnectorBackend{scriptedConnectorBackend: fixture.backend, release: make(chan struct{})}
	fixture.service.mu.Lock()
	fixture.service.backends["codex"] = gated
	fixture.service.mu.Unlock()
	fixture.service.claimRefreshWait = 20 * time.Millisecond
	now := fixture.clock.advance(connectorClaimFreshAge + time.Second)
	fixture.service.mu.Lock()
	fixture.service.runtimes["codex"]["runtime-2"] = connectorRuntimeAssignment{
		companyID: "company-1", runtimeID: "runtime-2", confirmedAt: now,
	}
	fixture.service.mu.Unlock()

	if err := fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"}); !errors.Is(
		err, errConnectorClaimRefreshPending,
	) {
		t.Fatalf("ensureFreshForClaim() error = %v, want the wait to lapse", err)
	}
	if err := fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-2"}); err != nil {
		t.Fatalf("fresh ensureFreshForClaim() error = %v", err)
	}
	fixture.service.claimRefreshWait = 10 * time.Second
	result := make(chan error, 1)
	go func() { result <- fixture.service.ensureFreshForClaim(t.Context(), []string{"runtime-1"}) }()
	select {
	case err := <-result:
		t.Fatalf("ensureFreshForClaim() = %v while the re-observation still ran", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(gated.release)
	if err := <-result; err != nil {
		t.Fatalf("ensureFreshForClaim() error = %v, want the running re-observation confirmed", err)
	}
	if forces := fixture.backend.discovered(); len(forces) != 2 {
		t.Fatalf("Discover ran %d times, want the initial read and one re-observation", len(forces))
	}
	if assignment := fixture.assignment(t, "runtime-1"); !assignment.confirmedAt.Equal(now) {
		t.Fatalf("confirmedAt = %v, want %v", assignment.confirmedAt, now)
	}
}

// The claim loop renews a stale Runtime's discovery before it asks the cloud
// for Runs, so the Run it claims freezes the apps that are really connected.
func TestRunPollerRenewsStaleDiscoveryBeforeItClaims(t *testing.T) {
	fixture := newKeepaliveFixture(t)
	fixture.publishInitial(t)
	fixture.clock.advance(connectorClaimFreshAge + time.Second)
	fixture.service.mu.Lock()
	fixture.service.runtimes["codex"] = map[string]connectorRuntimeAssignment{
		"rt-1": fixture.service.runtimes["codex"]["runtime-1"],
	}
	assignment := fixture.service.runtimes["codex"]["rt-1"]
	assignment.runtimeID = "rt-1"
	fixture.service.runtimes["codex"]["rt-1"] = assignment
	fixture.service.mu.Unlock()

	claimed := make(chan []connectorPublisherCall, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/daemon/runs/claim" {
			select {
			case claimed <- fixture.publisher.recorded():
			default:
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []Run{}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	d := newTestDaemon(t, srv.URL)
	trackRuntime(d, Runtime{ID: "rt-1", CompanyID: "company-1", Provider: "codex"})
	d.connectors = fixture.service

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var runWG sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.runBatchPoller(ctx, ctx, newRunSlotSemaphore(1), make(chan struct{}, 1), &runWG)
	}()
	select {
	case calls := <-claimed:
		if len(calls) != 2 || calls[1].method != "reaffirm" || calls[1].runtimeID != "rt-1" {
			t.Fatalf("publisher calls before the first claim = %+v, want the rt-1 reaffirmation", calls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the poller never claimed")
	}
	cancel()
	<-done
}
