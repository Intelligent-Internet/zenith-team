package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/daemon/execenv"
	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
)

// The test switch has one value and only lowers the report: any other
// value refuses to start the daemon rather than being read as "raise".
func TestCredentialIsolationSwitchOnlyLowers(t *testing.T) {
	t.Setenv("BOARDLY_HOME", t.TempDir())
	t.Setenv("BOARDLY_WORKSPACES_ROOT", t.TempDir())
	for _, value := range []string{"enforced", "off", "NOT_ENFORCED"} {
		t.Setenv(credentialIsolationEnv, value)
		if _, err := LoadConfig(Overrides{AllowNoAgents: true}); err == nil ||
			!strings.Contains(err.Error(), credentialIsolationEnv) {
			t.Fatalf(
				"LoadConfig with %s=%q = %v, want a refusal naming the switch",
				credentialIsolationEnv,
				value,
				err,
			)
		}
	}
	t.Setenv(credentialIsolationEnv, "not_enforced")
	cfg, err := LoadConfig(Overrides{AllowNoAgents: true})
	if err != nil || !cfg.CredentialIsolationOff {
		t.Fatalf("LoadConfig with the switch = %+v, %v; want the switch on", cfg.CredentialIsolationOff, err)
	}
	t.Setenv(credentialIsolationEnv, "")
	if cfg, err := LoadConfig(Overrides{AllowNoAgents: true}); err != nil || cfg.CredentialIsolationOff {
		t.Fatalf("LoadConfig without the switch = %+v, %v; want it off", cfg.CredentialIsolationOff, err)
	}
}

// The switch and every computer that is not a Mac report not_enforced
// without trying the sandbox.
func TestProbeReportsNotEnforcedWithTheSwitchOrOffMacOS(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	writeDaemonSession(t)
	d.cfg.CredentialIsolationOff = true
	verdict := d.probeCredentialIsolation(t.Context())
	if verdict.Value != protocol.CredentialIsolationNotEnforced ||
		!strings.Contains(verdict.Reason, credentialIsolationEnv) {
		t.Fatalf("verdict with the switch = %+v, want not_enforced naming the switch", verdict)
	}
	if runtime.GOOS == "darwin" {
		return
	}
	d.cfg.CredentialIsolationOff = false
	if verdict := d.probeCredentialIsolation(
		t.Context(),
	); verdict.Value != protocol.CredentialIsolationNotEnforced {
		t.Fatalf("verdict on %s = %+v, want not_enforced", runtime.GOOS, verdict)
	}
}

// Before the first probe round the daemon reports not_enforced.
func TestCredentialIsolationIsNotEnforcedBeforeTheFirstRound(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	if got := d.credentialIsolation(); got != protocol.CredentialIsolationNotEnforced {
		t.Fatalf("report before any round = %q, want not_enforced", got)
	}
}

// A Run that started without the sandbox, while the probe was failing,
// keeps the computer's report at not_enforced until its provider ends, even
// when the next round's probe passes; Runs started after that round are
// wrapped. The computer never says enforced while an unwrapped provider
// still runs.
func TestAnUnwrappedRunHoldsTheReportAtNotEnforcedUntilItEnds(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	recorder := &execOptionsRecorder{}
	stubAgentProvider(t, recorder)
	var (
		duringRun   string
		laterRun    bool
		enforced    = credentialIsolationVerdict{Value: protocol.CredentialIsolationEnforced}
		probeFailed = credentialIsolationNotEnforced("the sandbox could not start a process")
	)
	d := handleOneTaskRun(t, "claude", executable, func(d *Daemon) {
		d.recordCredentialIsolation(probeFailed)
		recorder.during = func() {
			d.recordCredentialIsolation(enforced)
			duringRun = d.credentialIsolation()
			sandboxed, done := d.startLaunchIsolation()
			laterRun = sandboxed
			done()
		}
	})
	launches := recorder.launches()
	if len(launches) != 1 || launches[0].Sandbox != nil {
		t.Fatalf("launches = %+v, want one without the sandbox", launches)
	}
	if duringRun != protocol.CredentialIsolationNotEnforced {
		t.Fatalf("report while the unwrapped provider ran = %q, want not_enforced", duringRun)
	}
	if !laterRun {
		t.Fatal("a Run started after the probe passed again was not wrapped")
	}
	if got := d.credentialIsolation(); got != protocol.CredentialIsolationEnforced {
		t.Fatalf("report after the unwrapped Run ended = %q, want enforced", got)
	}
}

// One verdict per probe round rides every runtime's registration and every
// heartbeat after it, over HTTP and over the WebSocket, until the next round
// changes it.
func TestEveryRegistrationAndHeartbeatCarriesTheRoundsVerdict(t *testing.T) {
	var (
		mu    sync.Mutex
		beats []protocol.DaemonHeartbeatRequestPayload
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/daemon/heartbeat" {
			var beat protocol.DaemonHeartbeatRequestPayload
			if err := json.NewDecoder(r.Body).Decode(&beat); err != nil {
				t.Errorf("decode heartbeat: %v", err)
			}
			mu.Lock()
			beats = append(beats, beat)
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	t.Cleanup(server.Close)

	d := newTestDaemon(t, server.URL)
	agents := map[string]AgentEntry{"claude": {Path: "/bin/echo"}, "codex": {Path: "/bin/echo"}}
	d.agentsAvailable.Store(&agents)
	d.probes.version = func(context.Context, string) (string, error) { return "9.9.9", nil }
	stubSignInProbe(d, func() string { return protocol.RuntimeSignInSignedIn })
	verdict := protocol.CredentialIsolationEnforced
	d.probes.isolation = func(context.Context) credentialIsolationVerdict {
		return credentialIsolationVerdict{Value: verdict}
	}

	for _, round := range []string{protocol.CredentialIsolationEnforced, protocol.CredentialIsolationNotEnforced} {
		verdict = round
		payload := d.detectRuntimes(t.Context())
		if len(payload) != 2 {
			t.Fatalf("payload = %+v, want both providers", payload)
		}
		for _, entry := range payload {
			if entry.CredentialIsolation != round {
				t.Fatalf("round %s: %s registers %q", round, entry.Provider, entry.CredentialIsolation)
			}
		}
		d.runHeartbeatTick(t.Context(), "rt-1")
		mu.Lock()
		last := beats[len(beats)-1]
		mu.Unlock()
		if last.RuntimeID != "rt-1" || last.CredentialIsolation != round {
			t.Fatalf("round %s: HTTP heartbeat = %+v", round, last)
		}

		writes := make(chan *wsOutbound, 1)
		d.sendWSHeartbeats(t.Context(), []string{"rt-1"}, writes)
		var message protocol.Message
		if err := json.Unmarshal((<-writes).data, &message); err != nil {
			t.Fatal(err)
		}
		var beat protocol.DaemonHeartbeatRequestPayload
		if err := json.Unmarshal(message.Payload, &beat); err != nil {
			t.Fatal(err)
		}
		if beat.CredentialIsolation != round {
			t.Fatalf("round %s: WS heartbeat = %+v", round, beat)
		}
	}
}

// execOptionsRecorder is a provider that keeps the prompt and options of
// every launch and finishes at once with one answer. during, when set, runs
// inside each launch, while the provider counts as running.
type execOptionsRecorder struct {
	mu      sync.Mutex
	opts    []agent.ExecOptions
	prompts []string
	during  func()
}

func (r *execOptionsRecorder) Execute(
	_ context.Context,
	prompt string,
	opts agent.ExecOptions,
) (*agent.Execution, error) {
	r.mu.Lock()
	r.opts = append(r.opts, opts)
	r.prompts = append(r.prompts, prompt)
	during := r.during
	r.mu.Unlock()
	if during != nil {
		during()
	}
	events := make(chan agent.Event, 2)
	events <- agent.CompletedItem{
		Key: "item-1", Role: agent.RoleAgent, Parts: []agent.Part{agent.TextPart{Text: "done"}},
	}
	events <- agent.Finished{Result: agent.Result{Status: agent.ResultCompleted, Output: "done"}}
	close(events)
	return &agent.Execution{Events: events}, nil
}

func (r *execOptionsRecorder) launches() []agent.ExecOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.ExecOptions(nil), r.opts...)
}

// launchPrompts are the prompts of every launch, in the order of launches.
func (r *execOptionsRecorder) launchPrompts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prompts...)
}

// writeDaemonSession puts a daemon session file in the current BOARDLY_HOME,
// the file the probe must find the sandbox hiding.
func writeDaemonSession(t *testing.T) string {
	t.Helper()
	path, err := OwnCredentialPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"refreshToken":"daemon-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The identity of the one Run handleOneTaskRun and handleOneRun start.
const (
	oneRunCompanyID      = "01900000-0000-4000-8000-0000000000e1"
	oneRunConversationID = "01900000-0000-4000-8000-0000000000e3"
)

// handleOneTaskRun runs one Task Run of provider end to end through
// handleRun against a server that accepts everything, with the provider's
// executable at path. configure runs on the daemon before the Run.
func handleOneTaskRun(t *testing.T, provider, path string, configure func(*Daemon)) *Daemon {
	t.Helper()
	return handleOneRun(t, provider, path, RunContext{ExecutionProfile: RunExecutionProfileContext{
		Project: &RunProjectContext{ID: taskMergeTaskID},
		Task:    &RunTaskContext{ConversationID: oneRunConversationID},
	}}, configure)
}

// handleOneRun runs one Run with doc as its context, as handleOneTaskRun
// runs a Task Run.
func handleOneRun(t *testing.T, provider, path string, doc RunContext, configure func(*Daemon)) *Daemon {
	t.Helper()
	run := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000003d01"),
		RuntimeID:      "rt-1",
		CompanyID:      oneRunCompanyID,
		AgentID:        "01900000-0000-4000-8000-0000000000e2",
		ConversationID: oneRunConversationID,
		AuthToken:      "brt_" + strings.Repeat("ab", 32),
	}
	runContext, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	run.Context = runContext
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	d := newTestDaemon(t, server.URL)
	d.cancelPollInterval = time.Hour
	trackRuntime(d, Runtime{ID: run.RuntimeID, CompanyID: run.CompanyID, Provider: provider})
	agents := map[string]AgentEntry{provider: {Path: path}}
	d.agentsAvailable.Store(&agents)
	appends := attachTranscriptAppendStatusRPC(t, d, http.StatusOK)
	configure(d)
	d.handleRun(t.Context(), run, 0)
	if got := appends(); got == 0 {
		t.Fatal("the Run reached the server with no transcript item")
	}
	return d
}

// A provider starts inside the sandbox exactly when the computer reports
// enforced: the launch carries the profile that denies the daemon's home,
// and the provider's own sandbox is turned off. Reporting not_enforced
// launches it directly. Runs on every platform: the profile is built in Go.
func TestRunLaunchesInTheSandboxExactlyWhenEnforced(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range []string{protocol.CredentialIsolationEnforced, protocol.CredentialIsolationNotEnforced} {
		t.Run(report, func(t *testing.T) {
			recorder := &execOptionsRecorder{}
			stubAgentProvider(t, recorder)
			var home, workspaces string
			handleOneTaskRun(t, "claude", executable, func(d *Daemon) {
				home = os.Getenv("BOARDLY_HOME")
				workspaces = d.cfg.WorkspacesRoot
				d.recordCredentialIsolation(credentialIsolationVerdict{Value: report})
			})
			launches := recorder.launches()
			if len(launches) != 1 {
				t.Fatalf("launches = %d, want 1", len(launches))
			}
			sandbox := launches[0].Sandbox
			if report == protocol.CredentialIsolationNotEnforced {
				if sandbox != nil {
					t.Fatalf("not_enforced launch carried sandbox %+v", sandbox)
				}
				return
			}
			if sandbox == nil {
				t.Fatal("an enforced computer launched the provider without the sandbox")
				return
			}
			named := false
			for _, value := range sandbox.Params {
				named = named || value == home || value == filepath.Join(home, "local-preview.json")
			}
			if !named {
				t.Fatalf("sandbox params %v do not name the daemon's home %s", sandbox.Params, home)
			}
			// The profile opens this Run's isolated home, in its env root
			// under the workspaces root, and no other (B3e).
			var runHomes []string
			for _, value := range sandbox.Params {
				if strings.HasPrefix(filepath.Base(value), execenv.RunHomePrefix) {
					runHomes = append(runHomes, value)
				}
			}
			if len(runHomes) != 1 ||
				!strings.HasPrefix(runHomes[0], workspaces+string(filepath.Separator)) ||
				!strings.Contains(sandbox.Profile, "(allow file-read* file-write*") {
				t.Fatalf("sandbox opens Run homes %v, want this Run's own under %s:\n%s",
					runHomes, workspaces, sandbox.Profile)
			}
			// It closes the workspaces root and the repository cache beside
			// it to writes and opens only this Run's own folders there: its
			// env root, which holds its workdir, and its worktree's own folder
			// in the Project main's repository (B3f).
			closed := sandboxRuleFolders(sandbox, "(deny file-write*\n")
			cache := repositoryCacheRoot(workspaces)
			if !slices.Contains(closed, workspaces) || !slices.Contains(closed, cache) {
				t.Fatalf("sandbox closes %v to writes, want the workspaces root %s and its cache:\n%s",
					closed, workspaces, sandbox.Profile)
			}
			gitFolders := filepath.Join(
				workspaces, oneRunCompanyID, "projects", taskMergeTaskID, "main", ".git", "worktrees",
			)
			inGitFolders := func(folder string) bool { return filepath.Dir(folder) == gitFolders }
			workDir := launches[0].Cwd
			opened := sandboxRuleFolders(sandbox, "(allow file-write*\n")
			if len(opened) != 3 || !slices.Contains(opened, filepath.Dir(workDir)) ||
				!slices.Contains(opened, workDir) || !slices.ContainsFunc(opened, inGitFolders) {
				t.Fatalf(
					"sandbox opens %v to writes, want the env root, workdir %s and a folder in %s:\n%s",
					opened,
					workDir,
					gitFolders,
					sandbox.Profile,
				)
			}
			settings, err := os.ReadFile(launches[0].ClaudeSettingsPath)
			if err != nil {
				t.Fatalf("read Claude run settings: %v", err)
			}
			if !strings.Contains(string(settings), `"sandbox":{"enabled":false}`) {
				t.Fatalf("Claude run settings %s do not turn Claude's own sandbox off", settings)
			}
		})
	}
}

// sandboxRuleFolders lists, sorted, the folders the profile's first rule
// that starts with rule names as subpaths.
func sandboxRuleFolders(sandbox *agent.Sandbox, rule string) []string {
	_, body, found := strings.Cut(sandbox.Profile, rule)
	if !found {
		return nil
	}
	body, _, _ = strings.Cut(body, ")\n(")
	var folders []string
	for _, line := range strings.Split(body, "\n") {
		name, ok := strings.CutPrefix(strings.TrimSpace(line), `(subpath (param "`)
		if !ok {
			continue
		}
		name, _, _ = strings.Cut(name, `"`)
		folders = append(folders, sandbox.Params[name])
	}
	slices.Sort(folders)
	return slices.Compact(folders)
}

// An app preview runs commands an Agent wrote, so it starts in the Runs'
// sandbox, with no Run home of its own, exactly when the computer reports
// enforced (Access TECH B3e). One that cannot be wrapped, because the
// computer was not enforced or the preview's folder lies in a denied path,
// still starts, and holds the report at not_enforced until its processes
// have stopped, even when the next round's probe passes. Runs on every
// platform: the profile is built in Go and the backend is a fake.
func TestAnUnwrappedAppPreviewHoldsTheReportAtNotEnforcedUntilItStops(t *testing.T) {
	enforced := credentialIsolationVerdict{Value: protocol.CredentialIsolationEnforced}
	for _, tc := range []struct {
		name          string
		verdict       credentialIsolationVerdict
		folderInHome  bool
		wantSandboxed bool
	}{
		{name: "enforced", verdict: enforced, wantSandboxed: true},
		{name: "not enforced at the start", verdict: credentialIsolationNotEnforced("the probe failed")},
		{name: "folder in a denied path", verdict: enforced, folderInHome: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDaemon(t, "http://127.0.0.1:1")
			d.recordCredentialIsolation(tc.verdict)
			backend := newFakeAppPreviewBackend()
			service, err := newAppPreviewServiceWithBackend(
				t.TempDir(), testLogger(), backend, d.appPreviewIsolation, testAppPreviewSettings(),
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Close() })
			manifest := testAppPreviewManifest(t, 4301)
			if tc.folderInHome {
				manifest.WorkDir = filepath.Join(os.Getenv("BOARDLY_HOME"), "preview")
				if err := os.MkdirAll(manifest.WorkDir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := service.Register(manifest); err != nil {
				t.Fatal(err)
			}
			lease := attachTestAppPreview(t, service, manifest)
			if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
				t.Fatal(err)
			}
			waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning)

			sandboxes := backend.launchSandboxes()
			if len(sandboxes) != 1 || (sandboxes[0] != nil) != tc.wantSandboxed {
				t.Fatalf("launch sandboxes = %v, want sandboxed %t", sandboxes, tc.wantSandboxed)
			}
			if tc.wantSandboxed && strings.Contains(sandboxes[0].Profile, "(allow file-read* file-write*") {
				t.Fatalf("a preview's sandbox opens a Run home:\n%s", sandboxes[0].Profile)
			}
			// The next round's probe passes while the preview runs.
			d.recordCredentialIsolation(enforced)
			want := protocol.CredentialIsolationNotEnforced
			if tc.wantSandboxed {
				want = protocol.CredentialIsolationEnforced
			}
			if got := d.credentialIsolation(); got != want {
				t.Fatalf("report while the preview runs = %q, want %q", got, want)
			}

			inspection, err := service.Inspect(manifest.identity())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Stop(inspection.StopToken, manifest.identity()); err != nil {
				t.Fatal(err)
			}
			waitForBackendStops(t, backend, 1)
			deadline := time.Now().Add(5 * time.Second)
			for d.credentialIsolation() != protocol.CredentialIsolationEnforced {
				if time.Now().After(deadline) {
					t.Fatal("the report stayed not_enforced after the preview stopped")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

// A Run whose folder is the person's home (a local directory) can name the
// sign-in files and every other Run's home as workspace files, or link to
// them where its provider saves an image. The daemon reads those outside
// the sandbox, so its output presenter and its copy of a generated image
// refuse what the sandbox denies the Run and still take an ordinary file
// (Access TECH B3e). Runs on every platform: no sandbox is started.
func TestTheDaemonReadsForARunOnlyWhatItsSandboxAllows(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	person, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", person)
	t.Setenv("BOARDLY_HOME", filepath.Join(person, "Library", "Application Support", "Zenith Team"))
	denied := []string{
		filepath.Join("Library", "Application Support", "Zenith Team", "daemon-auth.json"),
		filepath.Join(".boardly", "auth.json"),
		filepath.Join(
			"boardly_workspaces", ".local-scratch", "run-b", execenv.RunHomePrefix+"2222", "run.json",
		),
	}
	for _, name := range append([]string{"notes.txt", "image.png"}, denied...) {
		path := filepath.Join(person, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("content of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	run := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000003e01"),
		CompanyID:      "01900000-0000-4000-8000-0000000000e1",
		AgentID:        "01900000-0000-4000-8000-0000000000e2",
		ConversationID: "01900000-0000-4000-8000-0000000000e3",
	}
	presenter := d.newRunOutputPresenter(
		run, RunContext{}, "brt_"+strings.Repeat("ab", 32), person, execenv.SeatbeltOptions{},
	)
	if presenter == nil {
		t.Fatal("no output presenter for the Run")
	}
	for _, name := range denied {
		_, err := presenter.PresentOutput(t.Context(), tools.PresentOutputInput{
			Title: "Result", Summary: "Summary",
			Items: []tools.PresentOutputItem{{
				Role: "primary", Label: "File",
				Target: tools.PresentOutputTarget{Kind: "workspace_file", WorkspaceRelativePath: name},
			}},
		})
		if !errors.Is(err, errSandboxDeniedRead) {
			t.Errorf("presenting %s = %v, want the sandbox's refusal", name, err)
		}
	}

	recorder := &channelPresenterStub{}
	copyImage := func(path string) error {
		return presentGeneratedFile(
			t.Context(), runStreamPresentation{workDir: person, presenter: recorder},
			agent.CompletedItem{Key: "item-1", Role: agent.RoleTool},
			agent.ToolResultPart{
				Name:     "image_generation",
				FilePath: path,
				Status:   agent.ToolResultStatusCompleted,
			},
		)
	}
	link := filepath.Join(person, "linked.png")
	if err := os.Symlink(filepath.Join(person, denied[2]), link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(person, denied[0]), filepath.Join(person, denied[2]), link} {
		if err := copyImage(path); !errors.Is(err, errSandboxDeniedRead) {
			t.Errorf("copying a generated image from %s = %v, want the sandbox's refusal", path, err)
		}
	}
	if len(recorder.inputs) != 0 {
		t.Fatalf("refused images were presented: %+v", recorder.inputs)
	}
	if _, err := os.Stat(filepath.Join(person, ".zenith-generated")); !os.IsNotExist(err) {
		t.Fatalf("a refused image left a copy in the Run's folder (%v)", err)
	}
	if err := copyImage(filepath.Join(person, "image.png")); err != nil {
		t.Fatalf("copying an ordinary generated image: %v", err)
	}
	if len(recorder.inputs) != 1 {
		t.Fatalf("ordinary image presentations = %d, want 1", len(recorder.inputs))
	}
}

// taggedRunContext is the context of a Run tagged in a channel, in a Company
// that bound the given resources.
func taggedRunContext(resources ...RunResourceContext) RunContext {
	return RunContext{
		ChannelInvocation: &RunChannelInvocation{ID: "0198aa00-0000-7000-8000-0000000000c6"},
		Resources:         resources,
	}
}

// A Run tagged in a channel or group DM hides the workspaces root, the
// repository cache beside it and every local folder the Company bound, this
// daemon's and another's (which may run on this computer too). A relative
// path or another kind of resource names no folder. Every other Run hides
// nothing (Access TECH B7b).
func TestRunHiddenFoldersHideOnlyForATaggedRun(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	here := filepath.Join(t.TempDir(), "projects", "acme")
	there := filepath.Join(t.TempDir(), "on-another-computer")
	resources := []RunResourceContext{
		{
			ResourceType:  "local_directory",
			LocalDaemonID: d.cfg.DaemonID,
			LocalPath:     here + string(filepath.Separator),
		},
		{ResourceType: "local_directory", LocalDaemonID: "daemon-2", LocalPath: there},
		{ResourceType: "local_directory", LocalPath: filepath.Join("relative", "folder")},
		{ResourceType: "repository", LocalPath: filepath.Join(t.TempDir(), "repository")},
	}
	want := []string{d.cfg.WorkspacesRoot, d.cfg.WorkspacesRoot + "-repos", here, there}
	if got := d.runHiddenFolders(taggedRunContext(resources...)); !slices.Equal(got, want) {
		t.Fatalf("a tagged Run hides %v, want %v", got, want)
	}
	for name, doc := range map[string]RunContext{
		"direct chat": {Resources: resources},
		"task": {Resources: resources, ExecutionProfile: RunExecutionProfileContext{
			Project: &RunProjectContext{ID: taskMergeTaskID},
			Task:    &RunTaskContext{ConversationID: oneRunConversationID},
		}},
		"schedule": {ExecutionProfile: RunExecutionProfileContext{Source: runSourceSchedule}},
	} {
		if hidden := d.runHiddenFolders(doc); hidden != nil {
			t.Errorf("a %s Run hides %v, want nothing", name, hidden)
		}
	}
}

// A tagged Run on a computer that reports enforced starts in a sandbox that
// hides its person's private folders (Access TECH B7b): the workspaces root,
// which holds the Company main with its Memory and every other Run's
// folders, the repository cache and every bound local folder. It opens its
// own scratch (its workdir in <root>/<id>/workdir, the env root beside it with
// its isolated home) and the business context folder, read-only. A direct
// chat's Run and a Task Run of the same Company hide nothing, and on a
// computer that reports not_enforced the tagged Run starts without the
// sandbox, as before. Runs on every platform: the profile is built in Go.
func TestATaggedRunsSandboxHidesItsPersonsPrivateFolders(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bound, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "on-another-computer")
	resources := []RunResourceContext{
		{ResourceType: "local_directory", LocalDaemonID: "daemon-1", LocalPath: bound},
		{ResourceType: "local_directory", LocalDaemonID: "daemon-2", LocalPath: elsewhere},
	}
	type launched struct {
		opts                  agent.ExecOptions
		workspaces, cache     string
		companyMain, business string
	}
	launch := func(t *testing.T, doc RunContext, report string) launched {
		t.Helper()
		recorder := &execOptionsRecorder{}
		stubAgentProvider(t, recorder)
		var got launched
		handleOneRun(t, "claude", executable, doc, func(d *Daemon) {
			got.workspaces = d.cfg.WorkspacesRoot
			got.cache = repositoryCacheRoot(d.cfg.WorkspacesRoot)
			companyMain, err := d.workspaces.EnsureMain(t.Context(), oneRunCompanyID, companyWorkspaceScope)
			if err != nil {
				t.Fatal(err)
			}
			got.companyMain = companyMain
			got.business = filepath.Join(companyMain, "assets", "general")
			for path, content := range map[string]string{
				filepath.Join(got.business, "business_context.md"): "# Business\n",
				filepath.Join(companyMain, ".memory", "team.md"):   "private\n",
			} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			d.recordCredentialIsolation(credentialIsolationVerdict{Value: report})
		})
		launches := recorder.launches()
		if len(launches) != 1 {
			t.Fatalf("launches = %d, want 1", len(launches))
		}
		got.opts = launches[0]
		return got
	}

	t.Run("tagged run", func(t *testing.T) {
		got := launch(t, taggedRunContext(resources...), protocol.CredentialIsolationEnforced)
		sandbox := got.opts.Sandbox
		if sandbox == nil {
			t.Fatal("an enforced computer launched the tagged Run without the sandbox")
		}
		workDir := got.opts.Cwd
		envRoot := filepath.Dir(workDir)
		if filepath.Base(workDir) != "workdir" || filepath.Dir(envRoot) != got.workspaces {
			t.Fatalf(
				"the tagged Run works in %s, want <root>/<id>/workdir under %s",
				workDir,
				got.workspaces,
			)
		}
		hidden := sandboxRuleFolders(sandbox, "(allow default)\n(deny file-read* file-write* process-exec*\n")
		for _, folder := range []string{got.workspaces, got.cache, bound, elsewhere} {
			if !slices.Contains(hidden, folder) {
				t.Errorf(
					"the sandbox does not hide %s; it hides %v:\n%s",
					folder,
					hidden,
					sandbox.Profile,
				)
			}
		}
		seen := sandboxRuleFolders(sandbox, "(allow file-read* process-exec*\n")
		for _, folder := range []string{envRoot, workDir, got.business} {
			if !slices.Contains(seen, folder) {
				t.Errorf(
					"the sandbox does not open %s; it opens %v:\n%s",
					folder,
					seen,
					sandbox.Profile,
				)
			}
		}
		for _, folder := range seen {
			if folder != got.business && folder != envRoot &&
				!strings.HasPrefix(folder, envRoot+string(filepath.Separator)) {
				t.Errorf(
					"the sandbox opens %s, which is not the Run's own nor the business context:\n%s",
					folder,
					sandbox.Profile,
				)
			}
		}
		if !slices.ContainsFunc(seen, func(folder string) bool {
			return filepath.Dir(folder) == envRoot &&
				strings.HasPrefix(filepath.Base(folder), execenv.RunHomePrefix)
		}) {
			t.Errorf("the sandbox does not open the Run's isolated home beside its workdir: %v", seen)
		}
	})

	t.Run("tagged run on a computer that is not enforced", func(t *testing.T) {
		got := launch(t, taggedRunContext(resources...), protocol.CredentialIsolationNotEnforced)
		if got.opts.Sandbox != nil {
			t.Fatalf("a not_enforced computer launched the tagged Run in sandbox %+v", got.opts.Sandbox)
		}
	})

	for name, doc := range map[string]RunContext{
		"direct chat run": {Resources: resources},
		"task run": {Resources: resources, ExecutionProfile: RunExecutionProfileContext{
			Project: &RunProjectContext{ID: taskMergeTaskID},
			Task:    &RunTaskContext{ConversationID: oneRunConversationID},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			got := launch(t, doc, protocol.CredentialIsolationEnforced)
			if got.opts.Sandbox == nil {
				t.Fatal("an enforced computer launched the Run without the sandbox")
			}
			if profile := got.opts.Sandbox.Profile; strings.Contains(profile, "file-read-metadata") ||
				strings.Contains(profile, "(allow file-read* process-exec*") {
				t.Fatalf("an untagged Run's sandbox hides folders:\n%s", profile)
			}
		})
	}
}

// The daemon reads for a tagged Run only what its sandbox shows it (Access
// TECH B7b). A provider's generated image is copied from the Run's own
// folder, and refused from the Company main's Memory, a bound folder, another
// Run's folder, or through a link in its own folder that names one of them;
// the output presenter takes the same guard. A direct chat's Run, which hides
// nothing, still has the daemon copy an image from the Company main. The
// guard holds on every computer, whether the sandbox runs or not.
func TestTheDaemonReadsForATaggedRunOnlyWhatItsSandboxShows(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	root := d.cfg.WorkspacesRoot
	bound, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	envRoot := filepath.Join(root, "0198aa00-0000-7000-8000-0000000000c6")
	workDir := filepath.Join(envRoot, "workdir")
	companyMain := filepath.Join(root, oneRunCompanyID, "main")
	own := filepath.Join(workDir, "own.png")
	hidden := []string{
		filepath.Join(companyMain, ".memory", "chart.png"),
		filepath.Join(bound, "secret.png"),
		filepath.Join(root, "0198aa00-0000-7000-8000-0000000000c7", "workdir", "theirs.png"),
	}
	for _, path := range append([]string{own}, hidden...) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("image "+path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(workDir, "linked.png")
	if err := os.Symlink(hidden[0], link); err != nil {
		t.Fatal(err)
	}
	ownFolders := execenv.SeatbeltOptions{
		RunDirs:  []string{envRoot, workDir},
		Writable: []string{envRoot, workDir},
	}
	tagged := ownFolders
	tagged.Hidden = d.runHiddenFolders(taggedRunContext(
		RunResourceContext{ResourceType: "local_directory", LocalDaemonID: d.cfg.DaemonID, LocalPath: bound},
	))

	recorder := &channelPresenterStub{}
	copyImage := func(launch execenv.SeatbeltOptions, path, key string) error {
		return presentGeneratedFile(
			t.Context(),
			runStreamPresentation{workDir: workDir, presenter: recorder, launch: launch},
			agent.CompletedItem{Key: key, Role: agent.RoleTool},
			agent.ToolResultPart{
				Name:     "image_generation",
				FilePath: path,
				Status:   agent.ToolResultStatusCompleted,
			},
		)
	}
	for _, path := range append(hidden, link) {
		if err := copyImage(tagged, path, "refused"); !errors.Is(err, errSandboxDeniedRead) {
			t.Errorf("copying %s for a tagged Run = %v, want the sandbox's refusal", path, err)
		}
	}
	if len(recorder.inputs) != 0 {
		t.Fatalf("refused images were presented: %+v", recorder.inputs)
	}
	if err := copyImage(tagged, own, "own-image"); err != nil {
		t.Fatalf("copying the tagged Run's own image: %v", err)
	}
	if err := copyImage(ownFolders, hidden[0], "direct-image"); err != nil {
		t.Fatalf("copying a Company main image for a direct chat's Run: %v", err)
	}
	if len(recorder.inputs) != 2 {
		t.Fatalf("presented images = %d, want 2", len(recorder.inputs))
	}

	run := Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000003e02"),
		CompanyID:      oneRunCompanyID,
		AgentID:        "01900000-0000-4000-8000-0000000000e2",
		ConversationID: oneRunConversationID,
	}
	presenter := d.newRunOutputPresenter(run, RunContext{}, "brt_"+strings.Repeat("ab", 32), companyMain, tagged)
	if presenter == nil {
		t.Fatal("no output presenter for the Run")
	}
	_, err = presenter.PresentOutput(t.Context(), tools.PresentOutputInput{
		Title: "Result", Summary: "Summary",
		Items: []tools.PresentOutputItem{{
			Role: "primary", Label: "File",
			Target: tools.PresentOutputTarget{
				Kind: "workspace_file", WorkspaceRelativePath: filepath.Join(".memory", "chart.png"),
			},
		}},
	})
	if !errors.Is(err, errSandboxDeniedRead) {
		t.Fatalf("presenting a file the tagged Run's sandbox hides = %v, want the sandbox's refusal", err)
	}
}

// The app a tagged Run presents runs its commands, which the Agent wrote, with
// the folders the Run's sandbox hid (Access TECH B7b): the registration keeps
// them, a daemon that starts again reads them back, and each launch of the
// preview hides them while it opens the preview's own folder. A direct
// chat's app hides nothing. Runs on every platform: the profile is built in
// Go and the backend is a fake.
func TestATaggedRunsAppPreviewHidesWhatTheRunHid(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.recordCredentialIsolation(credentialIsolationVerdict{Value: protocol.CredentialIsolationEnforced})
	bound, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tagged := d.runHiddenFolders(taggedRunContext(
		RunResourceContext{ResourceType: "local_directory", LocalDaemonID: d.cfg.DaemonID, LocalPath: bound},
	))
	for name, hidden := range map[string][]string{"tagged run": tagged, "direct chat run": nil} {
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			workDir := filepath.Join(d.cfg.WorkspacesRoot, uuid.NewString(), "workdir")
			if err := os.MkdirAll(workDir, 0o700); err != nil {
				t.Fatal(err)
			}
			run := channelTestRun(t, false)
			run.RuntimeID = uuid.NewString()
			outputID := uuid.Must(uuid.NewV7())
			first, err := newAppPreviewServiceWithBackend(
				stateDir,
				testLogger(),
				newFakeAppPreviewBackend(),
				d.appPreviewIsolation,
				testAppPreviewSettings(),
			)
			if err != nil {
				t.Fatal(err)
			}
			registrar := &runCLIAppPreviewRegistrar{
				previews: first, run: run, workDir: workDir, hidden: hidden,
			}
			commands := []tools.PresentOutputCommand{
				{Command: "npm", Args: []string{"run", "dev"}, Port: 4311},
			}
			if err := registrar.RegisterAppPreview(t.Context(), outputID.String(), commands); err != nil {
				t.Fatalf("register the app: %v", err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}

			// The daemon starts again and launches the preview from what it
			// stored.
			backend := newFakeAppPreviewBackend()
			service, err := newAppPreviewServiceWithBackend(
				stateDir, testLogger(), backend, d.appPreviewIsolation, testAppPreviewSettings(),
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Close() })
			manifest, _, err := service.loadStoredManifest(outputID)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(manifest.Hidden, hidden) {
				t.Fatalf("the stored registration hides %v, want %v", manifest.Hidden, hidden)
			}
			lease := attachTestAppPreview(t, service, manifest)
			if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
				t.Fatal(err)
			}
			waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning)
			sandboxes := backend.launchSandboxes()
			if len(sandboxes) != 1 || sandboxes[0] == nil {
				t.Fatalf("launch sandboxes = %v, want one sandbox", sandboxes)
			}
			sandbox := sandboxes[0]
			hideRule := "(allow default)\n(deny file-read* file-write* process-exec*\n"
			got := sandboxRuleFolders(sandbox, hideRule)
			seen := sandboxRuleFolders(sandbox, "(allow file-read* process-exec*\n")
			switch {
			case hidden == nil && (len(got) != 0 || len(seen) != 0):
				t.Fatalf("a direct chat's preview hides %v and opens %v", got, seen)
			case hidden != nil && !slices.Contains(seen, manifest.WorkDir):
				t.Errorf("the preview does not open its folder %s; it opens %v", manifest.WorkDir, seen)
			}
			for _, folder := range hidden {
				if !slices.Contains(got, folder) {
					t.Errorf("the preview does not hide %s; it hides %v", folder, got)
				}
			}

			inspection, err := service.Inspect(manifest.identity())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Stop(inspection.StopToken, manifest.identity()); err != nil {
				t.Fatal(err)
			}
			waitForBackendStops(t, backend, 1)
		})
	}
}
