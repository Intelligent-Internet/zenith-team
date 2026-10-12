package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"boardly.dev/boardly/server/pkg/protocol"
)

// adoptionRecorder is a daemon-plane server that answers every registration
// with one stable runtime id per (provider, company) and counts the
// recover-orphans calls per runtime id, so a test can tell the ids adoption
// minted from the ones a company already held.
type adoptionRecorder struct {
	mu            sync.Mutex
	registrations map[string][][]RegisterRuntime // companyId -> the runtimes of each request
	recovered     map[string]int                 // runtimeId -> recover-orphans calls
}

func newAdoptionRecorder() *adoptionRecorder {
	return &adoptionRecorder{
		registrations: make(map[string][][]RegisterRuntime),
		recovered:     make(map[string]int),
	}
}

// adoptionRuntimeID is the id the recorder mints for a provider's runtime in
// a company — stable across registrations, as the server's upsert is.
func adoptionRuntimeID(provider, companyID string) string {
	return "rt-" + provider + "-" + companyID
}

func (r *adoptionRecorder) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/daemon/register", func(w http.ResponseWriter, req *http.Request) {
		var body RegisterRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode register request: %v", err)
		}
		r.mu.Lock()
		r.registrations[body.CompanyID] = append(r.registrations[body.CompanyID], body.Runtimes)
		r.mu.Unlock()
		items := make([]Runtime, 0, len(body.Runtimes))
		for _, rt := range body.Runtimes {
			items = append(items, Runtime{
				ID:        adoptionRuntimeID(rt.Provider, body.CompanyID),
				CompanyID: body.CompanyID,
				Name:      rt.Name,
				Provider:  rt.Provider,
				Status:    rt.Status,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	})
	mux.HandleFunc("/api/v1/daemon/runtimes/", func(w http.ResponseWriter, req *http.Request) {
		id, action, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/api/v1/daemon/runtimes/"), "/")
		if action != "recover-orphans" {
			http.NotFound(w, req)
			return
		}
		r.mu.Lock()
		r.recovered[id]++
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// registered returns the runtimes of every registration a company received.
func (r *adoptionRecorder) registered(companyID string) [][]RegisterRuntime {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]RegisterRuntime(nil), r.registrations[companyID]...)
}

func (r *adoptionRecorder) recoveredCount(runtimeID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recovered[runtimeID]
}

// findRegistered returns the entry for provider in the n-th registration a
// company received, failing the test when the registration or entry is
// missing.
func (r *adoptionRecorder) findRegistered(t *testing.T, companyID string, n int, provider string) RegisterRuntime {
	t.Helper()
	registrations := r.registered(companyID)
	if len(registrations) <= n {
		t.Fatalf("%s received %d registration(s), want at least %d", companyID, len(registrations), n+1)
	}
	for _, rt := range registrations[n] {
		if rt.Provider == provider {
			return rt
		}
	}
	t.Fatalf("registration %d for %s = %+v, want a %s entry", n, companyID, registrations[n], provider)
	return RegisterRuntime{}
}

// stubDiscovery makes the availability re-probe find exactly found on disk
// and counts how often discovery ran.
func stubDiscovery(t *testing.T, found map[string]AgentEntry) *atomic.Int32 {
	t.Helper()
	return stubDynamicDiscovery(t, func() map[string]AgentEntry { return found })
}

// stubDynamicDiscovery is stubDiscovery for a machine that changes between
// rounds: each pass reports whatever onDisk returns at the time, so one test
// can uninstall a CLI and install it again.
func stubDynamicDiscovery(t *testing.T, onDisk func() map[string]AgentEntry) *atomic.Int32 {
	t.Helper()
	previous := reprobeAgentCLIs
	t.Cleanup(func() { reprobeAgentCLIs = previous })
	var calls atomic.Int32
	reprobeAgentCLIs = func() map[string]AgentEntry {
		calls.Add(1)
		found := onDisk()
		copied := make(map[string]AgentEntry, len(found))
		for name, entry := range found {
			copied[name] = entry
		}
		return copied
	}
	return &calls
}

// adoptionAgents is the machine every adoption test starts from: claude
// installed, codex not.
func adoptionAgents() map[string]AgentEntry {
	return map[string]AgentEntry{"claude": {Path: "/opt/fake/claude", Command: "claude"}}
}

// lateInstalledAgents is the same machine after `codex` was installed.
func lateInstalledAgents() map[string]AgentEntry {
	found := adoptionAgents()
	found["codex"] = AgentEntry{Path: "/opt/fake/codex", Command: "codex"}
	return found
}

// newAdoptionDaemon wires a daemon that registered claude 2.5.0 for
// company-1 and company-2 and knows nothing about codex.
func newAdoptionDaemon(t *testing.T, serverURL string) *Daemon {
	t.Helper()
	d := newTestDaemon(t, serverURL)
	agents := adoptionAgents()
	d.agentsAvailable.Store(&agents)
	for _, companyID := range []string{"company-1", "company-2"} {
		trackRuntime(d, Runtime{
			ID:        adoptionRuntimeID("claude", companyID),
			CompanyID: companyID,
			Provider:  "claude",
			Status:    "online",
		})
		d.mu.Lock()
		d.companies[companyID].registeredVersions = map[string]string{"claude": "2.5.0"}
		d.mu.Unlock()
	}
	return d
}

// newTwoProviderDaemon wires a daemon that registered both providers for
// company-1 and company-2 — the machine an uninstall takes one provider away
// from.
func newTwoProviderDaemon(t *testing.T, serverURL string) *Daemon {
	t.Helper()
	d := newTestDaemon(t, serverURL)
	agents := lateInstalledAgents()
	d.agentsAvailable.Store(&agents)
	for _, companyID := range adoptionCompanies {
		for _, provider := range []string{"claude", "codex"} {
			trackRuntime(d, Runtime{
				ID:        adoptionRuntimeID(provider, companyID),
				CompanyID: companyID,
				Provider:  provider,
				Status:    "online",
			})
		}
		d.mu.Lock()
		d.companies[companyID].registeredVersions = map[string]string{
			"claude": "2.5.0", "codex": "0.120.0",
		}
		d.mu.Unlock()
	}
	return d
}

// adoptionCompanies are the two companies every adoption test tracks.
var adoptionCompanies = []string{"company-1", "company-2"}

// countingAnswer wraps a version-probe answer with a call counter.
func countingAnswer(answer func() (string, error)) (func() (string, error), *atomic.Int32) {
	var calls atomic.Int32
	return func() (string, error) {
		calls.Add(1)
		return answer()
	}, &calls
}

func recordedVersion(d *Daemon, companyID, provider string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cs, ok := d.companies[companyID]
	if !ok {
		return "", false
	}
	version, has := cs.registeredVersions[provider]
	return version, has
}

func hasConnectorBackend(d *Daemon, provider string) bool {
	d.connectors.mu.RLock()
	defer d.connectors.mu.RUnlock()
	_, ok := d.connectors.backends[provider]
	return ok
}

func hasPluginBackend(d *Daemon, provider string) bool {
	d.plugins.mu.RLock()
	defer d.plugins.mu.RUnlock()
	_, ok := d.plugins.backends[provider]
	return ok
}

// makeDue clears the backoff wait for a provider while keeping its failure
// count, standing in for the wall clock reaching the next attempt.
func makeDue(d *Daemon, provider string) {
	d.providerAdoption.mu.Lock()
	defer d.providerAdoption.mu.Unlock()
	delete(d.providerAdoption.next, provider)
}

func backoffFailures(d *Daemon, provider string) int {
	d.providerAdoption.mu.Lock()
	defer d.providerAdoption.mu.Unlock()
	return d.providerAdoption.failures[provider]
}

func sortedCopy(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// DR-AC20 fixes the cadence: a CLI installed after start is found within two
// minutes, and a CLI that keeps failing costs at most one attempt per half
// hour.
func TestProviderAvailabilityCadenceMatchesTheSpec(t *testing.T) {
	if providerAvailabilityProbeInterval != 2*time.Minute {
		t.Fatalf("providerAvailabilityProbeInterval = %v, want 2m", providerAvailabilityProbeInterval)
	}
	if providerAvailabilityProbeMaxBackoff != 30*time.Minute {
		t.Fatalf("providerAvailabilityProbeMaxBackoff = %v, want 30m", providerAvailabilityProbeMaxBackoff)
	}
}

// A provider that keeps failing is retried on a doubling ladder from the
// probe interval to the cap; a success resets the ladder; a provider never
// seen before is due at once; providers back off independently.
func TestProviderBackoffDoublesToTheCap(t *testing.T) {
	backoff := newProviderBackoff(2*time.Minute, 30*time.Minute)
	now := time.Now()
	if !backoff.due("codex", now) {
		t.Fatal("a provider that never failed must be due immediately")
	}
	want := []time.Duration{
		2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute,
	}
	for i, delay := range want {
		if got := backoff.fail("codex", now); got != delay {
			t.Fatalf("failure %d: delay = %v, want %v", i+1, got, delay)
		}
		if backoff.due("codex", now.Add(delay-time.Second)) {
			t.Fatalf("failure %d: due before the %v wait elapsed", i+1, delay)
		}
		if !backoff.due("codex", now.Add(delay)) {
			t.Fatalf("failure %d: not due once the %v wait elapsed", i+1, delay)
		}
	}
	if !backoff.due("claude", now) {
		t.Fatal("another provider must not inherit codex's backoff")
	}
	backoff.succeed("codex")
	if !backoff.due("codex", now) {
		t.Fatal("a success must make the provider due immediately")
	}
	if got := backoff.fail("codex", now); got != 2*time.Minute {
		t.Fatalf("delay after a success = %v, want the ladder reset to 2m", got)
	}
}

// providersWithoutRecord names the providers found on disk that some tracked
// company has no registration record for: a CLI installed after start, or
// one present at start whose registration never happened.
func TestProvidersWithoutRecord(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	found := lateInstalledAgents()
	if got := d.providersWithoutRecord(found); got != nil {
		t.Fatalf("providersWithoutRecord() = %v with no tracked company, want nil", got)
	}

	trackRuntime(d, Runtime{ID: "rt-claude-a", CompanyID: "company-a", Provider: "claude"})
	d.mu.Lock()
	d.companies["company-a"].registeredVersions = map[string]string{"claude": "2.5.0"}
	d.mu.Unlock()
	agents := adoptionAgents()
	d.agentsAvailable.Store(&agents)
	if got := d.providersWithoutRecord(found); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("providersWithoutRecord() = %v, want [codex]", got)
	}

	// A provider the daemon already knows (skipped at start) but never
	// registered is a candidate too.
	known := lateInstalledAgents()
	d.agentsAvailable.Store(&known)
	if got := d.providersWithoutRecord(found); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("providersWithoutRecord() = %v for a known but unrecorded provider, want [codex]", got)
	}

	d.mu.Lock()
	d.companies["company-a"].registeredVersions["codex"] = "0.120.0"
	d.mu.Unlock()
	if got := d.providersWithoutRecord(found); got != nil {
		t.Fatalf("providersWithoutRecord() = %v once every company records codex, want nil", got)
	}

	// A provider that disappeared from disk is never a candidate.
	if got := d.providersWithoutRecord(map[string]AgentEntry{}); got != nil {
		t.Fatalf("providersWithoutRecord() = %v with nothing found, want nil", got)
	}
}

// companiesToRegister selects the companies a fresh payload must be
// registered for: those lacking a record for a provider the payload carries
// (adoption), and those whose recorded provider is gone from disk and
// therefore absent from the payload (DR-FR9a's uninstall drop). A company
// whose recorded provider is still installed but absent from this round's
// payload is held back instead, because the register response replaces the
// company's runtime ids and would drop the runtime it runs today over a probe
// that merely failed.
func TestCompaniesToRegister(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	records := map[string]map[string]string{
		"company-a": {"claude": "2.5.0"},
		"company-b": {"claude": "2.5.0", "codex": "0.120.0"},
		"company-c": {},
		"company-d": {"codex": "0.120.0"},
	}
	for companyID, versions := range records {
		trackRuntime(d, Runtime{ID: "rt-" + companyID, CompanyID: companyID, Provider: "claude"})
		d.mu.Lock()
		d.companies[companyID].registeredVersions = versions
		d.mu.Unlock()
	}
	online := func(providers ...string) []RegisterRuntime {
		payload := make([]RegisterRuntime, 0, len(providers))
		for _, provider := range providers {
			payload = append(payload, RegisterRuntime{Provider: provider, Version: "1", Status: "online"})
		}
		return payload
	}
	bothFound := lateInstalledAgents()
	claudeOnly := adoptionAgents()

	targets, held := d.companiesToRegister(online("claude", "codex"), bothFound)
	if want := []string{"company-a", "company-c", "company-d"}; !equalStrings(targets, want) || len(held) != 0 {
		t.Fatalf("both providers online: targets = %v, held = %v; want %v and nothing held",
			targets, held, want)
	}

	// codex's probe failed this round while codex is still installed:
	// company-d must wait rather than lose its codex runtime, and company-b,
	// which has nothing to adopt, is left alone entirely.
	targets, held = d.companiesToRegister(online("claude"), bothFound)
	if want := []string{"company-c"}; !equalStrings(targets, want) {
		t.Fatalf("codex skipped: targets = %v, want %v", targets, want)
	}
	if absent := held["company-d"]; len(held) != 1 || len(absent) != 1 || absent[0] != "codex" {
		t.Fatalf("codex skipped: held = %v, want company-d held for codex", held)
	}

	// codex was uninstalled: every company that records it must re-register
	// without it — company-b too, which has nothing to adopt and would
	// otherwise keep heartbeating a runtime no CLI can serve.
	targets, held = d.companiesToRegister(online("claude"), claudeOnly)
	if want := []string{"company-b", "company-c", "company-d"}; !equalStrings(targets, want) || len(held) != 0 {
		t.Fatalf("codex uninstalled: targets = %v, held = %v; want %v and nothing held", targets, held, want)
	}

	if targets, held = d.companiesToRegister(nil, bothFound); targets != nil || len(held) != 0 {
		t.Fatalf("empty payload: targets = %v, held = %v; want nothing", targets, held)
	}
}

// vanishedProviders is the uninstall trigger: it names the providers a
// tracked company registered that are no longer on disk, and nothing else.
func TestVanishedProviders(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	if got := d.vanishedProviders(map[string]AgentEntry{}); got != nil {
		t.Fatalf("vanishedProviders() = %v with no tracked company, want nil", got)
	}
	trackRuntime(d, Runtime{ID: "rt-a", CompanyID: "company-a", Provider: "claude"})
	trackRuntime(d, Runtime{ID: "rt-b", CompanyID: "company-b", Provider: "claude"})
	d.mu.Lock()
	d.companies["company-a"].registeredVersions = map[string]string{"claude": "2.5.0", "codex": "0.120.0"}
	d.companies["company-b"].registeredVersions = map[string]string{"codex": "0.120.0"}
	d.mu.Unlock()

	if got := d.vanishedProviders(lateInstalledAgents()); got != nil {
		t.Fatalf("vanishedProviders() = %v with both CLIs installed, want nil", got)
	}
	if got := d.vanishedProviders(adoptionAgents()); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("vanishedProviders() = %v after codex was uninstalled, want [codex]", got)
	}
	if got := d.vanishedProviders(map[string]AgentEntry{}); !equalStrings(got, []string{"claude", "codex"}) {
		t.Fatalf("vanishedProviders() = %v with nothing installed, want both, deduped and sorted", got)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// DR-AC20: a daemon that registered only Claude picks up a codex installed
// later without a restart — one registration per tracked company carrying the
// new provider online beside the existing one, the runtime ids indexed for
// heartbeat and claiming, the connector/plugin backends wired exactly as at
// start, recover-orphans on the new ids only, and /health listing the
// provider. A second round finds nothing left to do.
func TestProviderAvailabilityAdoptsALateInstalledCLI(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	stubDiscovery(t, lateInstalledAgents())
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		"/opt/fake/codex":  func() (string, error) { return "0.120.0", nil },
	})
	notified, unsubscribe := d.runtimeSet.Subscribe()
	defer unsubscribe()

	d.refreshProviderAvailability(context.Background())

	for _, companyID := range []string{"company-1", "company-2"} {
		if got := recorder.registered(companyID); len(got) != 1 || len(got[0]) != 2 {
			t.Fatalf("%s registrations = %+v, want exactly one carrying both providers", companyID, got)
		}
		codex := recorder.findRegistered(t, companyID, 0, "codex")
		if codex.Status != "online" || codex.Version != "0.120.0" || codex.Name != "Codex (test-box)" ||
			codex.UnusableReason != nil {
			t.Fatalf("%s codex entry = %+v, want online 0.120.0 named Codex (test-box)", companyID, codex)
		}
		if claude := recorder.findRegistered(t, companyID, 0, "claude"); claude.Version != "2.5.0" {
			t.Fatalf("%s claude entry = %+v, want the existing provider re-sent at 2.5.0",
				companyID, claude)
		}
		if version, has := recordedVersion(d, companyID, "codex"); !has || version != "0.120.0" {
			t.Fatalf("%s recorded codex version = %q (%v), want 0.120.0 recorded", companyID, version, has)
		}
		if d.findRuntime(adoptionRuntimeID("codex", companyID)) == nil {
			t.Fatalf("%s codex runtime is not indexed", companyID)
		}
		if d.findRuntime(adoptionRuntimeID("claude", companyID)) == nil {
			t.Fatalf("%s claude runtime was dropped by the re-registration", companyID)
		}
		if n := recorder.recoveredCount(adoptionRuntimeID("codex", companyID)); n != 1 {
			t.Fatalf("%s codex recover-orphans calls = %d, want 1 for the new id", companyID, n)
		}
		if n := recorder.recoveredCount(adoptionRuntimeID("claude", companyID)); n != 0 {
			t.Fatalf("%s claude recover-orphans calls = %d, want none for an id the company held",
				companyID, n)
		}
	}
	if entry, ok := d.agents()["codex"]; !ok || entry.Path != "/opt/fake/codex" {
		t.Fatalf("agents()[codex] = %+v (%v), want the discovered entry", entry, ok)
	}
	if !hasConnectorBackend(d, "codex") || !hasPluginBackend(d, "codex") {
		t.Fatal("adoption must wire the codex connector and plugin backends as New does")
	}
	select {
	case <-notified:
	default:
		t.Fatal("adoption must announce the runtime-set change to the heartbeat, wakeup and poll loops")
	}
	want := []string{
		adoptionRuntimeID("claude", "company-1"), adoptionRuntimeID("claude", "company-2"),
		adoptionRuntimeID("codex", "company-1"), adoptionRuntimeID("codex", "company-2"),
	}
	if got := sortedCopy(d.claimableRuntimeIDs()); !equalStrings(got, want) {
		t.Fatalf("claimableRuntimeIDs() = %v, want %v", got, want)
	}
	health, _ := readHealthBody(t, d)
	if !equalStrings(sortedCopy(health.Agents), []string{"claude", "codex"}) ||
		health.AgentVersions["codex"] != "0.120.0" {
		t.Fatalf("health agents = %v versions = %v, want codex listed at 0.120.0",
			health.Agents, health.AgentVersions)
	}

	d.refreshProviderAvailability(context.Background())
	for _, companyID := range []string{"company-1", "company-2"} {
		if got := recorder.registered(companyID); len(got) != 1 {
			t.Fatalf("%s registrations after a second round = %d, want still 1", companyID, len(got))
		}
	}
}

// A machine that has not changed costs nothing beyond discovery: no version
// probe, no registration, no backend, no runtime-set announcement.
func TestProviderAvailabilityIgnoresAnUnchangedMachine(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	discoveries := stubDiscovery(t, adoptionAgents())
	answer, probes := countingAnswer(func() (string, error) { return "2.5.0", nil })
	stubVersionProbeByPath(d, map[string]func() (string, error){"/opt/fake/claude": answer})
	notified, unsubscribe := d.runtimeSet.Subscribe()
	defer unsubscribe()
	before := d.agentsAvailable.Load()

	d.refreshProviderAvailability(context.Background())

	if discoveries.Load() != 1 {
		t.Fatalf("discovery ran %d times, want 1", discoveries.Load())
	}
	if probes.Load() != 0 {
		t.Fatalf("version probe ran %d times on an unchanged machine, want 0", probes.Load())
	}
	if got := recorder.registered("company-1"); len(got) != 0 {
		t.Fatalf("registered %+v on an unchanged machine, want nothing", got)
	}
	if d.agentsAvailable.Load() != before {
		t.Fatal("the availability set was replaced although nothing changed")
	}
	if hasConnectorBackend(d, "claude") || hasPluginBackend(d, "claude") {
		t.Fatal("no backend may be wired for a provider that was not adopted")
	}
	select {
	case <-notified:
		t.Fatal("an unchanged machine must not announce a runtime-set change")
	default:
	}
}

// DR-FR9: a CLI that disappears is not removed until restart — the daemon
// keeps the provider, registers nothing and probes nothing.
func TestProviderAvailabilityKeepsAProviderThatDisappeared(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	stubDiscovery(t, map[string]AgentEntry{})
	answer, probes := countingAnswer(func() (string, error) { return "2.5.0", nil })
	stubVersionProbeByPath(d, map[string]func() (string, error){"/opt/fake/claude": answer})

	d.refreshProviderAvailability(context.Background())

	if _, ok := d.agents()["claude"]; !ok {
		t.Fatal("a provider that disappeared from disk must stay in the availability set until restart")
	}
	if probes.Load() != 0 || len(recorder.registered("company-1")) != 0 {
		t.Fatalf("probes = %d, registrations = %d; want nothing to happen", probes.Load(),
			len(recorder.registered("company-1")))
	}
}

// A newly found CLI whose probe fails is adopted (so /health can say why)
// but registered nowhere, and it is not probed again until its backoff
// elapses — discovery itself keeps running every tick. Once it probes, every
// company registers it and the ladder resets.
func TestProviderAvailabilityBacksOffWhileANewCLIFailsItsProbe(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	discoveries := stubDiscovery(t, lateInstalledAgents())
	var mu sync.Mutex
	codexVersion, codexErr := "", errors.New("text file busy")
	answer, probes := countingAnswer(func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return codexVersion, codexErr
	})
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		"/opt/fake/codex":  answer,
	})

	d.refreshProviderAvailability(context.Background())

	if got := recorder.registered("company-1"); len(got) != 0 {
		t.Fatalf("registered %+v while codex fails its probe, want nothing", got)
	}
	if _, ok := d.agents()["codex"]; !ok {
		t.Fatal("a found provider is adopted even when its first probe fails")
	}
	if reason := d.skippedAgentsSnapshot()["codex"]; !strings.HasPrefix(reason, "version detection failed") {
		t.Fatalf("skippedAgents[codex] = %q, want the probe failure recorded for /health", reason)
	}
	if backoffFailures(d, "codex") != 1 {
		t.Fatalf("codex failures = %d after one failed round, want 1", backoffFailures(d, "codex"))
	}
	probesAfterFirstRound := probes.Load()
	if probesAfterFirstRound == 0 {
		t.Fatal("the first round must probe the new provider")
	}

	d.refreshProviderAvailability(context.Background())
	if discoveries.Load() != 2 {
		t.Fatalf("discovery ran %d times, want every tick (2)", discoveries.Load())
	}
	if probes.Load() != probesAfterFirstRound {
		t.Fatalf("codex was probed again while backed off (%d -> %d)", probesAfterFirstRound, probes.Load())
	}
	if got := recorder.registered("company-1"); len(got) != 0 {
		t.Fatalf("registered %+v while backed off, want nothing", got)
	}

	mu.Lock()
	codexVersion, codexErr = "0.120.0", nil
	mu.Unlock()
	makeDue(d, "codex")
	d.refreshProviderAvailability(context.Background())
	for _, companyID := range []string{"company-1", "company-2"} {
		if codex := recorder.findRegistered(t, companyID, 0, "codex"); codex.Status != "online" ||
			codex.Version != "0.120.0" {
			t.Fatalf("%s codex entry = %+v once the probe succeeds, want online 0.120.0", companyID, codex)
		}
	}
	if backoffFailures(d, "codex") != 0 {
		t.Fatalf("codex failures = %d after a success, want the ladder reset", backoffFailures(d, "codex"))
	}
}

// A found CLI that registers as unusable (DR-C22) counts as failing for the
// backoff, but it is still registered offline with its reason so the roster
// can say why, and its runtimes are never claimed for. From then on it has a
// record, so recovery belongs to the version refresh (§9.8).
func TestProviderAvailabilityBacksOffWhileAFoundCLIIsUnusable(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	stubDiscovery(t, lateInstalledAgents())
	var mu sync.Mutex
	codexVersion := "0.90.0"
	answer, probes := countingAnswer(func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return codexVersion, nil
	})
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		"/opt/fake/codex":  answer,
	})

	d.refreshProviderAvailability(context.Background())

	wantReason := protocol.RuntimeUnusableReason{
		Code:    protocol.RuntimeUnusableCodeBelowMinimumVersion,
		Message: "Codex CLI 0.90.0 is below the minimum 0.100.0",
	}
	for _, companyID := range []string{"company-1", "company-2"} {
		codex := recorder.findRegistered(t, companyID, 0, "codex")
		if codex.Status != "offline" || codex.UnusableReason == nil || *codex.UnusableReason != wantReason {
			t.Fatalf("%s codex entry = %+v, want offline with %+v", companyID, codex, wantReason)
		}
	}
	if backoffFailures(d, "codex") != 1 {
		t.Fatalf("codex failures = %d after registering unusable, want 1 (backed off)",
			backoffFailures(d, "codex"))
	}
	all := sortedCopy(d.allRuntimeIDs())
	if len(all) != 4 {
		t.Fatalf("allRuntimeIDs() = %v, want the unusable runtimes kept for heartbeat", all)
	}
	wantClaimable := []string{adoptionRuntimeID("claude", "company-1"), adoptionRuntimeID("claude", "company-2")}
	if got := sortedCopy(d.claimableRuntimeIDs()); !equalStrings(got, wantClaimable) {
		t.Fatalf("claimableRuntimeIDs() = %v, want only the claude runtimes", got)
	}

	// Recorded now, so the availability loop is done with it.
	probesAfterFirstRound := probes.Load()
	d.refreshProviderAvailability(context.Background())
	if probes.Load() != probesAfterFirstRound || len(recorder.registered("company-1")) != 1 {
		t.Fatal("a recorded unusable provider must not be probed or registered by the availability loop again")
	}

	// The version refresh recovers it once the CLI is upgraded.
	mu.Lock()
	codexVersion = "0.101.0"
	mu.Unlock()
	d.refreshProviderVersions(context.Background())
	for _, companyID := range []string{"company-1", "company-2"} {
		if codex := recorder.findRegistered(t, companyID, 1, "codex"); codex.Status != "online" ||
			codex.Version != "0.101.0" || codex.UnusableReason != nil {
			t.Fatalf("%s codex entry after the upgrade = %+v, want online 0.101.0 without a reason",
				companyID, codex)
		}
	}
	if got := sortedCopy(d.claimableRuntimeIDs()); len(got) != 4 {
		t.Fatalf("claimableRuntimeIDs() = %v after the upgrade, want all four", got)
	}
}

// A provider present at start but never registered (its probe was skipped
// then) is picked up by the same loop once it probes — without adopting it a
// second time, since its backends exist already.
func TestProviderAvailabilityRegistersAStartupSkippedProviderOnceItProbes(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	d.adoptProvider("codex", AgentEntry{Path: "/opt/fake/codex", Command: "codex"})
	stubDiscovery(t, lateInstalledAgents())
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		"/opt/fake/codex":  func() (string, error) { return "0.120.0", nil },
	})
	before := d.agentsAvailable.Load()

	d.refreshProviderAvailability(context.Background())

	for _, companyID := range []string{"company-1", "company-2"} {
		if codex := recorder.findRegistered(t, companyID, 0, "codex"); codex.Status != "online" {
			t.Fatalf("%s codex entry = %+v, want online", companyID, codex)
		}
	}
	if d.agentsAvailable.Load() != before {
		t.Fatal("a provider the daemon already carries must not be adopted again")
	}
}

// A candidate the daemon carries at a stale path (skipped at start, then
// reinstalled elsewhere) is re-adopted from disk, so the probe reaches the
// binary the user actually fixed.
func TestProviderAvailabilityRefreshesTheRecordedPathOfAnUnregisteredProvider(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	d.adoptProvider("codex", AgentEntry{Path: "/opt/stale/codex", Command: "codex"})
	found := adoptionAgents()
	found["codex"] = AgentEntry{Path: "/opt/fresh/codex", Command: "codex"}
	stubDiscovery(t, found)
	staleAnswer, staleProbes := countingAnswer(func() (string, error) { return "", errors.New("no such file") })
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		"/opt/stale/codex": staleAnswer,
		"/opt/fresh/codex": func() (string, error) { return "0.120.0", nil },
	})

	d.refreshProviderAvailability(context.Background())

	if entry := d.agents()["codex"]; entry.Path != "/opt/fresh/codex" {
		t.Fatalf("agents()[codex].Path = %q, want the path found on disk", entry.Path)
	}
	if staleProbes.Load() != 0 {
		t.Fatalf("the stale path was probed %d times, want 0", staleProbes.Load())
	}
	for _, companyID := range []string{"company-1", "company-2"} {
		if codex := recorder.findRegistered(t, companyID, 0, "codex"); codex.Version != "0.120.0" {
			t.Fatalf("%s codex entry = %+v, want the version of the fresh binary", companyID, codex)
		}
	}
}

// A company is not re-registered while a provider it runs today is still
// installed but did not answer this round: the register response replaces
// the company's runtime ids, and dropping the runtime would stop its
// heartbeat and fail its runs. The company adopts on the next round that
// carries every recorded provider.
func TestProviderAvailabilityHoldsACompanyWhoseInstalledProviderDidNotAnswer(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	stubDiscovery(t, lateInstalledAgents())
	var mu sync.Mutex
	claudeErr := errors.New("text file busy")
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if claudeErr != nil {
				return "", claudeErr
			}
			return "2.5.0", nil
		},
		"/opt/fake/codex": func() (string, error) { return "0.120.0", nil },
	})

	d.refreshProviderAvailability(context.Background())

	for _, companyID := range []string{"company-1", "company-2"} {
		if got := recorder.registered(companyID); len(got) != 0 {
			t.Fatalf("%s registered %+v while claude did not answer, want the company held", companyID, got)
		}
		if d.findRuntime(adoptionRuntimeID("claude", companyID)) == nil {
			t.Fatalf("%s claude runtime was dropped", companyID)
		}
	}
	if backoffFailures(d, "codex") != 0 {
		t.Fatal("codex probed fine; holding the companies is not a codex failure")
	}

	mu.Lock()
	claudeErr = nil
	mu.Unlock()
	d.refreshProviderAvailability(context.Background())
	for _, companyID := range []string{"company-1", "company-2"} {
		if got := recorder.registered(companyID); len(got) != 1 || len(got[0]) != 2 {
			t.Fatalf("%s registrations = %+v once claude answers, want one carrying both", companyID, got)
		}
	}
}

// When the provider a company runs was uninstalled, adoption goes ahead: the
// registration carries only what the machine can run, and the missing
// provider's runtime goes offline through the heartbeat it no longer gets.
func TestProviderAvailabilityLetsAnUninstalledProviderGoOfflineOnAdoption(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	stubDiscovery(t, map[string]AgentEntry{"codex": {Path: "/opt/fake/codex", Command: "codex"}})
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "", errors.New("no such file or directory") },
		"/opt/fake/codex":  func() (string, error) { return "0.120.0", nil },
	})

	d.refreshProviderAvailability(context.Background())

	for _, companyID := range []string{"company-1", "company-2"} {
		got := recorder.registered(companyID)
		if len(got) != 1 || len(got[0]) != 1 || got[0][0].Provider != "codex" {
			t.Fatalf("%s registrations = %+v, want one carrying codex only", companyID, got)
		}
		if d.findRuntime(adoptionRuntimeID("claude", companyID)) != nil {
			t.Fatalf("%s still indexes the claude runtime the server no longer hears about", companyID)
		}
		if _, has := recordedVersion(d, companyID, "claude"); has {
			t.Fatalf("%s still records claude, which would hide it from a later re-install", companyID)
		}
	}
	if _, ok := d.agents()["claude"]; !ok {
		t.Fatal("the availability set keeps the uninstalled provider until restart")
	}
	want := []string{adoptionRuntimeID("codex", "company-1"), adoptionRuntimeID("codex", "company-2")}
	if got := sortedCopy(d.allRuntimeIDs()); !equalStrings(got, want) {
		t.Fatalf("allRuntimeIDs() = %v, want only the codex runtimes still heartbeated", got)
	}
}

// DR-FR9a: a provider CLI uninstalled while the daemon runs is never
// deregistered, and it does not wait for another provider's adoption to be
// noticed. The round that finds it gone re-registers every company that had a
// record for it with a payload that omits it — which is what stops the daemon
// heartbeating that runtime and lets the server's stale sweep take it offline
// — while the provider stays in the availability set so a re-install is
// adopted without a restart.
func TestProviderAvailabilityDropsAnUninstalledProviderFromTheNextRegistration(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newTwoProviderDaemon(t, recorder.serve(t).URL)
	onDisk := adoptionAgents() // codex was uninstalled while the daemon ran
	stubDynamicDiscovery(t, func() map[string]AgentEntry { return onDisk })
	var mu sync.Mutex
	codexVersion, codexErr := "", errors.New("no such file or directory")
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		"/opt/fake/codex": func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return codexVersion, codexErr
		},
	})
	notified, unsubscribe := d.runtimeSet.Subscribe()
	defer unsubscribe()

	d.refreshProviderAvailability(context.Background())

	for _, companyID := range adoptionCompanies {
		got := recorder.registered(companyID)
		if len(got) != 1 || len(got[0]) != 1 || got[0][0].Provider != "claude" {
			t.Fatalf("%s registrations = %+v, want exactly one carrying claude alone", companyID, got)
		}
		if claude := recorder.findRegistered(t, companyID, 0, "claude"); claude.Status != "online" ||
			claude.Version != "2.5.0" {
			t.Fatalf("%s claude entry = %+v, want the surviving provider re-sent online at 2.5.0",
				companyID, claude)
		}
		if d.findRuntime(adoptionRuntimeID("codex", companyID)) != nil {
			t.Fatalf("%s still indexes the codex runtime the server no longer hears about", companyID)
		}
		if d.findRuntime(adoptionRuntimeID("claude", companyID)) == nil {
			t.Fatalf("%s lost its claude runtime", companyID)
		}
		if _, has := recordedVersion(d, companyID, "codex"); has {
			t.Fatalf("%s still records codex, which would hide a later re-install", companyID)
		}
	}
	surviving := []string{adoptionRuntimeID("claude", "company-1"), adoptionRuntimeID("claude", "company-2")}
	if got := sortedCopy(d.allRuntimeIDs()); !equalStrings(got, surviving) {
		t.Fatalf("allRuntimeIDs() = %v, want %v — the dropped runtime is no longer heartbeated", got, surviving)
	}
	if got := sortedCopy(d.claimableRuntimeIDs()); !equalStrings(got, surviving) {
		t.Fatalf("claimableRuntimeIDs() = %v, want %v — the dropped runtime is no longer claimed for",
			got, surviving)
	}
	if _, ok := d.agents()["codex"]; !ok {
		t.Fatal("the uninstalled provider must stay in the availability set until restart")
	}
	select {
	case <-notified:
	default:
		t.Fatal("the drop must announce the runtime-set change to the heartbeat, wakeup and poll loops")
	}

	// The drop is a one-off: with no record left, the next round has nothing
	// to do.
	d.refreshProviderAvailability(context.Background())
	for _, companyID := range adoptionCompanies {
		if got := recorder.registered(companyID); len(got) != 1 {
			t.Fatalf("%s registrations after a second round = %d, want still 1", companyID, len(got))
		}
	}

	// The re-install is adopted without a restart.
	onDisk = lateInstalledAgents()
	mu.Lock()
	codexVersion, codexErr = "0.121.0", nil
	mu.Unlock()
	d.refreshProviderAvailability(context.Background())
	for _, companyID := range adoptionCompanies {
		if got := recorder.registered(companyID); len(got) != 2 || len(got[1]) != 2 {
			t.Fatalf("%s registrations after the re-install = %+v, want a second carrying both",
				companyID, got)
		}
		if codex := recorder.findRegistered(t, companyID, 1, "codex"); codex.Status != "online" ||
			codex.Version != "0.121.0" {
			t.Fatalf("%s codex entry after the re-install = %+v, want online 0.121.0", companyID, codex)
		}
		if d.findRuntime(adoptionRuntimeID("codex", companyID)) == nil {
			t.Fatalf("%s did not index the re-installed codex runtime", companyID)
		}
	}
}

// Only a CLI that is genuinely gone from disk may be dropped. A recorded
// provider that is still installed and merely failed its probe this round
// holds its companies back instead: the register response replaces their
// runtime ids, so registering without it would stop the heartbeat of a
// runtime that is about to work again.
func TestProviderAvailabilityNeverDropsAProviderThatIsStillInstalled(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newTwoProviderDaemon(t, recorder.serve(t).URL)
	// claude was uninstalled; codex is still installed but does not answer.
	stubDiscovery(t, map[string]AgentEntry{"codex": {Path: "/opt/fake/codex", Command: "codex"}})
	var mu sync.Mutex
	codexErr := errors.New("text file busy")
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "", errors.New("no such file or directory") },
		"/opt/fake/codex": func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if codexErr != nil {
				return "", codexErr
			}
			return "0.120.0", nil
		},
	})

	d.refreshProviderAvailability(context.Background())

	for _, companyID := range adoptionCompanies {
		if got := recorder.registered(companyID); len(got) != 0 {
			t.Fatalf("%s registered %+v while codex did not answer, want the company held", companyID, got)
		}
		for _, provider := range []string{"claude", "codex"} {
			if d.findRuntime(adoptionRuntimeID(provider, companyID)) == nil {
				t.Fatalf("%s lost its %s runtime to a probe failure", companyID, provider)
			}
		}
	}

	mu.Lock()
	codexErr = nil
	mu.Unlock()
	d.refreshProviderAvailability(context.Background())

	for _, companyID := range adoptionCompanies {
		got := recorder.registered(companyID)
		if len(got) != 1 || len(got[0]) != 1 || got[0][0].Provider != "codex" {
			t.Fatalf("%s registrations = %+v once codex answers, want one carrying codex alone",
				companyID, got)
		}
		if d.findRuntime(adoptionRuntimeID("claude", companyID)) != nil {
			t.Fatalf("%s still indexes the uninstalled claude runtime", companyID)
		}
		if d.findRuntime(adoptionRuntimeID("codex", companyID)) == nil {
			t.Fatalf("%s lost the codex runtime that came back", companyID)
		}
	}
	if _, ok := d.agents()["claude"]; !ok {
		t.Fatal("the availability set keeps the uninstalled provider until restart")
	}
}

// The loop does the adoption unattended: with the interval shortened, a
// codex that appears is registered for every company once, and later ticks
// leave the registrations alone.
func TestProviderAvailabilityLoopAdoptsWithoutARestart(t *testing.T) {
	shortenProviderAvailabilityInterval(t, 5*time.Millisecond)
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	stubDiscovery(t, lateInstalledAgents())
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		"/opt/fake/codex":  func() (string, error) { return "0.120.0", nil },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		d.providerAvailabilityLoop(ctx)
		close(done)
	}()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if len(recorder.registered("company-1")) > 0 && len(recorder.registered("company-2")) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Let a few more ticks run before stopping: they must not register again.
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	for _, companyID := range []string{"company-1", "company-2"} {
		if got := recorder.registered(companyID); len(got) != 1 {
			t.Fatalf("%s registrations = %d, want exactly 1 from the loop", companyID, len(got))
		}
	}
}

// The loop is owned by the daemon's root context and returns with it.
func TestProviderAvailabilityLoopStopsWithItsContext(t *testing.T) {
	shortenProviderAvailabilityInterval(t, time.Hour)
	recorder := newAdoptionRecorder()
	d := newAdoptionDaemon(t, recorder.serve(t).URL)
	discoveries := stubDiscovery(t, lateInstalledAgents())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		d.providerAvailabilityLoop(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not stop with its context")
	}
	if discoveries.Load() != 0 {
		t.Fatalf("discovery ran %d times on a cancelled context, want 0", discoveries.Load())
	}
}

func shortenProviderAvailabilityInterval(t *testing.T, interval time.Duration) {
	t.Helper()
	previous := providerAvailabilityProbeInterval
	t.Cleanup(func() { providerAvailabilityProbeInterval = previous })
	providerAvailabilityProbeInterval = interval
}
