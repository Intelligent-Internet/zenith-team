package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
)

// registerRecorder is a server that accepts registrations and remembers the
// provider version each one carried, plus every entry as sent.
type registerRecorder struct {
	mu       sync.Mutex
	versions []string
	entries  []RegisterRuntime
}

func (r *registerRecorder) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/v1/daemon/register" {
			http.NotFound(w, req)
			return
		}
		var body RegisterRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode register request: %v", err)
		}
		r.mu.Lock()
		for _, rt := range body.Runtimes {
			r.versions = append(r.versions, rt.Version)
			r.entries = append(r.entries, rt)
		}
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []Runtime{{ID: "rt-1", CompanyID: "company-1", Provider: "claude", Status: "online"}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func (r *registerRecorder) registered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.versions...)
}

func (r *registerRecorder) registeredEntries() []RegisterRuntime {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RegisterRuntime(nil), r.entries...)
}

// A provider the daemon found but cannot run is reported as unusable — not
// silently skipped — for exactly two verdicts: the CLI parsed a version
// below the minimum, or the version probe could not execute the binary.
// Everything else (a hung probe, unparseable output, a transient exec
// failure during an in-place upgrade) stays a plain skip.
func TestUnusableReasonForClassifiesBelowMinimumAndExecFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want *protocol.RuntimeUnusableReason
	}{
		{
			name: "below minimum",
			err:  &agent.BelowMinimumError{AgentType: "codex", Detected: "0.90.0", Minimum: "0.100.0"},
			want: &protocol.RuntimeUnusableReason{
				Code:    protocol.RuntimeUnusableCodeBelowMinimumVersion,
				Message: "Codex CLI 0.90.0 is below the minimum 0.100.0",
			},
		},
		{
			name: "below minimum wrapped",
			err: fmt.Errorf("gate: %w",
				&agent.BelowMinimumError{AgentType: "claude", Detected: "1.9.0", Minimum: "2.0.0"}),
			want: &protocol.RuntimeUnusableReason{
				Code:    protocol.RuntimeUnusableCodeBelowMinimumVersion,
				Message: "Claude CLI 1.9.0 is below the minimum 2.0.0",
			},
		},
		{
			name: "permission denied on exec",
			err:  &fs.PathError{Op: "fork/exec", Path: "/opt/secret/bin/codex", Err: syscall.EACCES},
			want: &protocol.RuntimeUnusableReason{
				Code:    protocol.RuntimeUnusableCodeNotExecutable,
				Message: "Codex CLI is not executable",
			},
		},
		{
			name: "wrong binary format",
			err:  &fs.PathError{Op: "fork/exec", Path: "/opt/secret/bin/codex", Err: syscall.ENOEXEC},
			want: &protocol.RuntimeUnusableReason{
				Code:    protocol.RuntimeUnusableCodeNotExecutable,
				Message: "Codex CLI is not executable",
			},
		},
		{
			name: "text file busy during an upgrade",
			err:  &fs.PathError{Op: "fork/exec", Path: "/opt/secret/bin/codex", Err: syscall.ETXTBSY},
		},
		{name: "plain error", err: errors.New("text file busy")},
		{name: "hung probe", err: context.DeadlineExceeded},
		{name: "unparseable version", err: errors.New(`parse version "banana"`)},
		{name: "nil"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := unusableReasonFor("codex", test.err)
			if test.want == nil {
				if got != nil {
					t.Fatalf("unusableReasonFor() = %+v, want nil (a plain skip)", got)
				}
				return
			}
			if got == nil || *got != *test.want {
				t.Fatalf("unusableReasonFor() = %+v, want %+v", got, test.want)
			}
			if strings.Contains(got.Message, "/opt/secret") {
				t.Fatalf("message %q leaks the binary path", got.Message)
			}
			if len([]rune(got.Message)) > protocol.RuntimeUnusableMessageMaxLen {
				t.Fatalf(
					"message %q exceeds %d characters",
					got.Message,
					protocol.RuntimeUnusableMessageMaxLen,
				)
			}
		})
	}
}

// stubVersionProbeByPath answers the version probe per binary path so one
// round can carry a healthy provider and a broken one side by side.
func stubVersionProbeByPath(d *Daemon, answers map[string]func() (string, error)) {
	d.probes.version = func(_ context.Context, path string) (string, error) {
		answer, ok := answers[path]
		if !ok {
			return "", fmt.Errorf("unexpected probe of %s", path)
		}
		return answer()
	}
	d.probes.retryDelay = time.Millisecond
}

// DR-AC19: a machine whose codex is below the minimum registers a Codex
// runtime offline with the reason, the Claude runtime is unaffected, and
// /health (what `daemon status` renders) carries the identical message. The
// daemon then keeps heartbeating the unusable runtime but never claims for
// it.
func TestDetectRuntimesRegistersAnUnusableProviderOffline(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	agents := map[string]AgentEntry{
		"claude": {Path: "/opt/fake/claude", Command: "claude"},
		"codex":  {Path: "/opt/fake/codex", Command: "codex"},
	}
	d.agentsAvailable.Store(&agents)
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		"/opt/fake/codex":  func() (string, error) { return "0.90.0", nil },
	})

	payload := d.detectRuntimes(context.Background())
	if len(payload) != 2 || payload[0].Provider != "claude" || payload[1].Provider != "codex" {
		t.Fatalf("payload = %+v, want claude then codex", payload)
	}
	claude, codex := payload[0], payload[1]
	if claude.Status != "online" || claude.Version != "2.5.0" || claude.UnusableReason != nil {
		t.Fatalf("claude entry = %+v, want online 2.5.0 without a reason", claude)
	}
	wantReason := protocol.RuntimeUnusableReason{
		Code:    protocol.RuntimeUnusableCodeBelowMinimumVersion,
		Message: "Codex CLI 0.90.0 is below the minimum 0.100.0",
	}
	if codex.Status != "offline" || codex.Version != "0.90.0" || codex.Name != "Codex (test-box)" ||
		codex.UnusableReason == nil || *codex.UnusableReason != wantReason {
		t.Fatalf(
			"codex entry = %+v (reason %+v), want offline with %+v",
			codex,
			codex.UnusableReason,
			wantReason,
		)
	}
	if skipped := d.skippedAgentsSnapshot(); skipped["codex"] != wantReason.Message || len(skipped) != 1 {
		t.Fatalf("skippedAgents = %v, want only codex with the registered message", skipped)
	}

	d.indexRegisterResponse("company-1", &RegisterResponse{Runtimes: []Runtime{
		{ID: "rt-claude", CompanyID: "company-1", Provider: "claude", Status: "online"},
		{ID: "rt-codex", CompanyID: "company-1", Provider: "codex", Status: "offline"},
	}}, payload)
	if all := d.allRuntimeIDs(); len(all) != 2 {
		t.Fatalf("allRuntimeIDs() = %v, want both (heartbeat and deregister keep the row alive)", all)
	}
	if claimable := d.claimableRuntimeIDs(); len(claimable) != 1 || claimable[0] != "rt-claude" {
		t.Fatalf("claimableRuntimeIDs() = %v, want only the claude runtime", claimable)
	}
}

// A binary the probe cannot execute is the other unusable verdict; a probe
// that merely fails stays a skip and never reaches the payload.
func TestDetectRuntimesSkipsProvidersWhoseProbeMerelyFails(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	agents := map[string]AgentEntry{
		"claude": {Path: "/opt/fake/claude", Command: "claude"},
		"codex":  {Path: "/opt/fake/codex", Command: "codex"},
	}
	d.agentsAvailable.Store(&agents)
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "", errors.New("text file busy") },
		"/opt/fake/codex": func() (string, error) {
			return "", &fs.PathError{Op: "fork/exec", Path: "/opt/fake/codex", Err: syscall.EACCES}
		},
	})

	payload := d.detectRuntimes(context.Background())
	if len(payload) != 1 || payload[0].Provider != "codex" || payload[0].Status != "offline" ||
		payload[0].Version != "" || payload[0].UnusableReason == nil ||
		payload[0].UnusableReason.Code != protocol.RuntimeUnusableCodeNotExecutable ||
		payload[0].UnusableReason.Message != "Codex CLI is not executable" {
		t.Fatalf("payload = %+v, want only codex offline as not_executable", payload)
	}
	skipped := d.skippedAgentsSnapshot()
	if skipped["codex"] != "Codex CLI is not executable" || !strings.Contains(skipped["claude"], "text file busy") {
		t.Fatalf("skippedAgents = %v, want codex's reason and claude's probe failure", skipped)
	}
}

// The claim exclusion of an unusable provider is sticky: a later round where
// its probe merely fails (a hung CLI, ETXTBSY during the very upgrade that
// fixes it) says nothing new, and the server row is still offline with the
// reason — so its runtime must stay unclaimable until a probe succeeds.
func TestUnusableExclusionStaysUntilTheProbeSucceeds(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	agents := map[string]AgentEntry{
		"claude": {Path: "/opt/fake/claude", Command: "claude"},
		"codex":  {Path: "/opt/fake/codex", Command: "codex"},
	}
	d.agentsAvailable.Store(&agents)
	var mu sync.Mutex
	codexAnswer := func() (string, error) { return "0.90.0", nil }
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		"/opt/fake/codex": func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return codexAnswer()
		},
	})
	d.indexRegisterResponse("company-1", &RegisterResponse{Runtimes: []Runtime{
		{ID: "rt-claude", CompanyID: "company-1", Provider: "claude", Status: "online"},
		{ID: "rt-codex", CompanyID: "company-1", Provider: "codex", Status: "offline"},
	}}, d.detectRuntimes(context.Background()))
	if claimable := d.claimableRuntimeIDs(); len(claimable) != 1 || claimable[0] != "rt-claude" {
		t.Fatalf("claimableRuntimeIDs() = %v after the unusable verdict, want only rt-claude", claimable)
	}

	mu.Lock()
	codexAnswer = func() (string, error) { return "", errors.New("text file busy") }
	mu.Unlock()
	payload := d.detectRuntimes(context.Background())
	if len(payload) != 1 || payload[0].Provider != "claude" {
		t.Fatalf("payload = %+v while the codex probe fails, want claude only", payload)
	}
	if reason := d.skippedAgentsSnapshot()["codex"]; !strings.Contains(reason, "text file busy") {
		t.Fatalf("skippedAgents[codex] = %q, want this round's probe failure", reason)
	}
	if claimable := d.claimableRuntimeIDs(); len(claimable) != 1 || claimable[0] != "rt-claude" {
		t.Fatalf("claimableRuntimeIDs() = %v after a merely failed probe, want the exclusion kept", claimable)
	}

	mu.Lock()
	codexAnswer = func() (string, error) { return "0.120.0", nil }
	mu.Unlock()
	if payload := d.detectRuntimes(context.Background()); len(payload) != 2 || payload[1].Status != "online" {
		t.Fatalf("payload = %+v once codex probes, want codex online", payload)
	}
	claimable := d.claimableRuntimeIDs()
	sort.Strings(claimable)
	if len(claimable) != 2 || claimable[0] != "rt-claude" || claimable[1] != "rt-codex" {
		t.Fatalf("claimableRuntimeIDs() = %v once the probe succeeds, want both runtimes", claimable)
	}
}

// Recovery is automatic: an upgrade changes the detected version, so the
// next refresh round re-registers the company without the reason.
func TestRefreshProviderVersionsClearsAnUnusableReasonAfterUpgrade(t *testing.T) {
	recorder := &registerRecorder{}
	d := newTestDaemon(t, recorder.serve(t).URL)
	agents := map[string]AgentEntry{"codex": {Path: "/opt/fake/codex", Command: "codex"}}
	d.agentsAvailable.Store(&agents)
	var mu sync.Mutex
	version := "0.90.0"
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/codex": func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return version, nil
		},
	})
	trackRuntime(d, Runtime{ID: "rt-1", CompanyID: "company-1", Provider: "codex"})
	d.mu.Lock()
	d.companies["company-1"].registeredVersions = map[string]string{"codex": "0.100.0"}
	d.mu.Unlock()

	d.refreshProviderVersions(context.Background())
	entries := recorder.registeredEntries()
	if len(entries) != 1 || entries[0].Status != "offline" || entries[0].UnusableReason == nil ||
		entries[0].UnusableReason.Code != protocol.RuntimeUnusableCodeBelowMinimumVersion {
		t.Fatalf("registered %+v after the downgrade verdict, want one offline entry with the reason", entries)
	}

	mu.Lock()
	version = "0.101.0"
	mu.Unlock()
	d.refreshProviderVersions(context.Background())
	entries = recorder.registeredEntries()
	if len(entries) != 2 || entries[1].Status != "online" || entries[1].Version != "0.101.0" ||
		entries[1].UnusableReason != nil {
		t.Fatalf("registered %+v after the upgrade, want a second entry online without a reason", entries)
	}
	if skipped := d.skippedAgentsSnapshot(); len(skipped) != 0 {
		t.Fatalf("skippedAgents = %v after the upgrade, want empty", skipped)
	}
}

// newVersionRefreshDaemon wires a daemon that registered claude at version
// 2.5.0 for company-1, with the version probe answering from probe.
func newVersionRefreshDaemon(t *testing.T, serverURL string, probe func() (string, error)) *Daemon {
	t.Helper()
	d := newTestDaemon(t, serverURL)
	agents := map[string]AgentEntry{"claude": {Path: "/bin/echo", Command: "claude"}}
	d.agentsAvailable.Store(&agents)
	d.probes.version = func(context.Context, string) (string, error) { return probe() }
	d.probes.retryDelay = time.Millisecond
	trackRuntime(d, Runtime{ID: "rt-1", CompanyID: "company-1", Provider: "claude"})
	d.mu.Lock()
	d.companies["company-1"].registeredVersions = map[string]string{"claude": "2.5.0"}
	d.mu.Unlock()
	return d
}

// An in-place upgrade of a provider CLI reaches the roster without a daemon
// restart: the next refresh round re-registers the companies whose last
// accepted registration carried the old version, and only those.
func TestRefreshProviderVersionsReregistersWhenACLIVersionChanges(t *testing.T) {
	recorder := &registerRecorder{}
	var mu sync.Mutex
	version := "2.5.0"
	d := newVersionRefreshDaemon(t, recorder.serve(t).URL, func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return version, nil
	})

	d.refreshProviderVersions(context.Background())
	if got := recorder.registered(); len(got) != 0 {
		t.Fatalf("registered %v with the version unchanged, want nothing", got)
	}

	mu.Lock()
	version = "2.6.0"
	mu.Unlock()
	d.refreshProviderVersions(context.Background())
	if got := recorder.registered(); len(got) != 1 || got[0] != "2.6.0" {
		t.Fatalf("registered %v after the upgrade, want exactly one registration carrying 2.6.0", got)
	}

	// The accepted registration is now the baseline: nothing to do next round.
	d.refreshProviderVersions(context.Background())
	if got := recorder.registered(); len(got) != 1 {
		t.Fatalf("registered %v on an unchanged round, want no further registration", got)
	}
}

// A probe that fails says nothing about the version, so it must not trigger
// registrations or disturb the recorded baseline.
func TestRefreshProviderVersionsIgnoresAProviderThatFailsItsProbe(t *testing.T) {
	recorder := &registerRecorder{}
	d := newVersionRefreshDaemon(t, recorder.serve(t).URL, func() (string, error) {
		return "", errors.New("text file busy")
	})

	d.refreshProviderVersions(context.Background())

	if got := recorder.registered(); len(got) != 0 {
		t.Fatalf("registered %v after a failed probe, want nothing", got)
	}
	d.mu.Lock()
	recorded := d.companies["company-1"].registeredVersions["claude"]
	d.mu.Unlock()
	if recorded != "2.5.0" {
		t.Fatalf("recorded version = %q, want the baseline kept after a failed probe", recorded)
	}
}

// stubSignInProbe answers the sign-in probe for every provider with one
// verdict.
func stubSignInProbe(d *Daemon, state func() string) {
	d.probes.signIn = func(context.Context, string, string) protocol.RuntimeSignIn {
		return protocol.RuntimeSignIn{State: state(), CheckedAt: "2026-09-16T09:00:00Z"}
	}
}

// A person asked: the setup refresh registers every tracked company again
// even when nothing looks different, and the payload carries the sign-in
// verdict beside the version.
func TestRefreshRuntimeSetupReregistersEveryTrackedCompany(t *testing.T) {
	recorder := &registerRecorder{}
	d := newVersionRefreshDaemon(t, recorder.serve(t).URL, func() (string, error) { return "2.5.0", nil })
	stubSignInProbe(d, func() string { return protocol.RuntimeSignInSignedOut })
	trackRuntime(d, Runtime{ID: "rt-2", CompanyID: "company-2", Provider: "claude"})
	previousReprobe := reprobeAgentCLIs
	t.Cleanup(func() { reprobeAgentCLIs = previousReprobe })
	reprobeAgentCLIs = func() map[string]AgentEntry { return d.agents() }

	d.refreshProviderVersions(context.Background())
	if got := recorder.registered(); len(got) != 0 {
		t.Fatalf("the version loop registered %v with nothing changed, want nothing", got)
	}

	if err := d.refreshRuntimeSetup(context.Background(), "test"); err != nil {
		t.Fatalf("refreshRuntimeSetup: %v", err)
	}
	entries := recorder.registeredEntries()
	if len(entries) != 2 {
		t.Fatalf("registered %d entries, want one per tracked company", len(entries))
	}
	for _, entry := range entries {
		if entry.SignIn == nil || entry.SignIn.State != protocol.RuntimeSignInSignedOut ||
			entry.Status != "online" {
			t.Fatalf("registered %+v, want an online entry carrying the signed-out verdict", entry)
		}
	}
	d.mu.Lock()
	recorded := d.companies["company-1"].registeredSignIn["claude"]
	d.mu.Unlock()
	if recorded != protocol.RuntimeSignInSignedOut {
		t.Fatalf("recorded sign-in = %q, want the registered verdict", recorded)
	}
}

// The background version loop also re-registers when the sign-in verdict
// changes, so a founder who signed in again sees the roster follow without
// clicking anything.
func TestRefreshProviderVersionsReregistersWhenSignInChanges(t *testing.T) {
	recorder := &registerRecorder{}
	d := newVersionRefreshDaemon(t, recorder.serve(t).URL, func() (string, error) { return "2.5.0", nil })
	var mu sync.Mutex
	state := protocol.RuntimeSignInSignedOut
	stubSignInProbe(d, func() string {
		mu.Lock()
		defer mu.Unlock()
		return state
	})
	d.mu.Lock()
	d.companies["company-1"].registeredSignIn = map[string]string{"claude": protocol.RuntimeSignInSignedOut}
	d.mu.Unlock()

	d.refreshProviderVersions(context.Background())
	if got := recorder.registered(); len(got) != 0 {
		t.Fatalf("registered %v with the verdict unchanged, want nothing", got)
	}
	mu.Lock()
	state = protocol.RuntimeSignInSignedIn
	mu.Unlock()
	d.refreshProviderVersions(context.Background())
	entries := recorder.registeredEntries()
	if len(entries) != 1 || entries[0].SignIn == nil || entries[0].SignIn.State != protocol.RuntimeSignInSignedIn {
		t.Fatalf("registered %+v after signing in, want one registration carrying signed_in", entries)
	}
}

// A probe that merely failed this round must not narrow a company's roster
// through the on-demand refresh either: the company is held, the other
// companies and providers still refresh.
func TestRefreshRuntimeSetupHoldsACompanyWhoseInstalledProviderDidNotAnswer(t *testing.T) {
	recorder := &registerRecorder{}
	d := newTwoProviderDaemon(t, recorder.serve(t).URL)
	stubDiscovery(t, lateInstalledAgents())
	stubSignInProbe(d, func() string { return protocol.RuntimeSignInSignedIn })
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "", errors.New("text file busy") },
		"/opt/fake/codex":  func() (string, error) { return "0.120.0", nil },
	})

	if err := d.refreshRuntimeSetup(context.Background(), "test"); err != nil {
		t.Fatalf("refreshRuntimeSetup: %v", err)
	}
	if got := recorder.registered(); len(got) != 0 {
		t.Fatalf("registered %v while claude did not answer, want every company held", got)
	}
	for _, companyID := range adoptionCompanies {
		if d.findRuntime(adoptionRuntimeID("claude", companyID)) == nil {
			t.Fatalf("company %s lost its claude runtime to a transient probe miss", companyID)
		}
	}
}

// An unknown sign-in verdict is a check that did not answer, not news: the
// background loop keeps the last known verdict instead of flapping.
func TestRefreshProviderVersionsIgnoresAnUnknownSignInVerdict(t *testing.T) {
	recorder := &registerRecorder{}
	d := newVersionRefreshDaemon(t, recorder.serve(t).URL, func() (string, error) { return "2.5.0", nil })
	stubSignInProbe(d, func() string { return protocol.RuntimeSignInUnknown })
	d.mu.Lock()
	d.companies["company-1"].registeredSignIn = map[string]string{"claude": protocol.RuntimeSignInSignedIn}
	d.mu.Unlock()

	d.refreshProviderVersions(context.Background())
	if got := recorder.registered(); len(got) != 0 {
		t.Fatalf("registered %v on an unknown verdict, want nothing", got)
	}
}

// The floor drops a second click inside runtimeRefreshMinInterval; a request
// that lands while a round is in flight is honored by one more round.
func TestRefreshRuntimeSetupOnceThrottlesAndQueues(t *testing.T) {
	recorder := &registerRecorder{}
	d := newVersionRefreshDaemon(t, recorder.serve(t).URL, func() (string, error) { return "2.5.0", nil })
	stubSignInProbe(d, func() string { return protocol.RuntimeSignInSignedIn })
	previousReprobe, previousInterval := reprobeAgentCLIs, runtimeRefreshMinInterval
	t.Cleanup(func() {
		reprobeAgentCLIs = previousReprobe
		runtimeRefreshMinInterval = previousInterval
	})
	reprobeAgentCLIs = func() map[string]AgentEntry { return d.agents() }
	runtimeRefreshMinInterval = time.Hour

	d.refreshRuntimeSetupOnce("first")
	d.refreshRuntimeSetupOnce("second")
	if got := recorder.registered(); len(got) != 1 {
		t.Fatalf("registered %v, want exactly one round inside the floor", got)
	}

	runtimeRefreshMinInterval = 0
	d.runtimeRefreshInFlight.Store(true)
	d.refreshRuntimeSetupOnce("while in flight")
	if !d.runtimeRefreshPending.Load() {
		t.Fatal("a request during an in-flight round was not queued")
	}
	d.runtimeRefreshInFlight.Store(false)
	d.refreshRuntimeSetupOnce("third")
	if got := recorder.registered(); len(got) != 2 {
		t.Fatalf("registered %v, want a second round once the floor passed", got)
	}
}
