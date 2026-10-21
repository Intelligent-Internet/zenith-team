package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// codexAnswer is what a stubbed `codex --version` does.
type codexAnswer struct {
	banner string
	err    error
	// hang blocks until the ranking deadline, like a binary macOS is still
	// scanning on its first run.
	hang bool
}

// codexVersionStub answers --version per path and counts the calls.
type codexVersionStub struct {
	t       *testing.T
	mu      sync.Mutex
	answers map[string]codexAnswer
	calls   map[string]int
}

func newCodexVersionStub(t *testing.T, answers map[string]codexAnswer) *codexVersionStub {
	t.Helper()
	return &codexVersionStub{t: t, answers: answers, calls: map[string]int{}}
}

func (s *codexVersionStub) set(path string, answer codexAnswer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[path] = answer
}

func (s *codexVersionStub) probe(ctx context.Context, path string) (string, error) {
	s.mu.Lock()
	answer, ok := s.answers[path]
	s.calls[path]++
	s.mu.Unlock()
	if !ok {
		s.t.Errorf("unexpected version probe of %s", path)
		return "", errors.New("unexpected version probe")
	}
	if answer.hang {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return answer.banner, answer.err
}

func (s *codexVersionStub) callsFor(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[path]
}

func (s *codexVersionStub) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, calls := range s.calls {
		n += calls
	}
	return n
}

// noVersionProbe is the version probe for a pass that must not run one.
func noVersionProbe(t *testing.T) func(context.Context, string) (string, error) {
	t.Helper()
	return func(_ context.Context, path string) (string, error) {
		t.Errorf("unexpected version probe of %s", path)
		return "", errors.New("unexpected version probe")
	}
}

// rewriteFakeCLI replaces a fake CLI with a different file, the way an
// in-place upgrade does, so its remembered version answer no longer holds.
func rewriteFakeCLI(t *testing.T, path, note string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n# "+note+"\n"), 0o755); err != nil {
		t.Fatalf("rewrite %s: %v", path, err)
	}
}

// codexFixture is a machine with Codex copies on PATH and at install
// locations off PATH, each its own file, in tie-break order.
type codexFixture struct {
	onPath  []string
	offPath []string
}

func newCodexFixture(t *testing.T, onPath, offPath int) codexFixture {
	t.Helper()
	root := t.TempDir()
	var fixture codexFixture
	var pathDirs []string
	for i := range onPath {
		dir := filepath.Join(root, fmt.Sprintf("path%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
		pathDirs = append(pathDirs, dir)
		fixture.onPath = append(fixture.onPath, writeFakeCLI(t, dir, "codex"))
	}
	for i := range offPath {
		dir := filepath.Join(root, fmt.Sprintf("install%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
		fixture.offPath = append(fixture.offPath, writeFakeCLI(t, dir, "codex"))
	}
	isolateProviderDiscovery(t, strings.Join(pathDirs, string(os.PathListSeparator)))
	stubCodexInstallLocations(t, fixture.offPath...)
	return fixture
}

func discoverCodex(t *testing.T, versionOf func(context.Context, string) (string, error)) (AgentEntry, bool) {
	t.Helper()
	entry, ok := discoverAgentCLIs(cachedShellResolvedAgents, versionOf)["codex"]
	return entry, ok
}

// Several copies of Codex on one machine are common, and PATH order says
// nothing about which is newest: an old npm global first on PATH used to win
// over a current ChatGPT app copy. Discovery asks every copy and keeps the
// newest; a release beats any prerelease; a tie keeps the earlier copy.
func TestDiscoverAgentCLIsPicksTheNewestCodex(t *testing.T) {
	for _, tc := range []struct {
		name    string
		onPath  []codexAnswer
		offPath []codexAnswer
		want    string // "path0", "install1", ...
	}{
		{
			name:    "an old copy first on PATH loses to the app copy",
			onPath:  []codexAnswer{{banner: "codex-cli 0.136.0"}},
			offPath: []codexAnswer{{banner: "codex-cli 0.160.0"}},
			want:    "install0",
		},
		{
			name:   "the newer of two copies on PATH",
			onPath: []codexAnswer{{banner: "codex-cli 0.159.2"}, {banner: "codex-cli 0.160.1"}},
			want:   "path1",
		},
		{
			name:    "a release beats its own prerelease",
			onPath:  []codexAnswer{{banner: "codex-cli 0.148.0-alpha.15"}},
			offPath: []codexAnswer{{banner: "codex-cli 0.148.0"}},
			want:    "install0",
		},
		{
			name:    "a release beats a higher prerelease",
			onPath:  []codexAnswer{{banner: "codex-cli 0.161.0-alpha.3"}},
			offPath: []codexAnswer{{banner: "codex-cli 0.160.1"}},
			want:    "install0",
		},
		{
			name:    "with only prereleases the highest wins",
			onPath:  []codexAnswer{{banner: "codex-cli 0.151.0-alpha.7.2"}},
			offPath: []codexAnswer{{banner: "codex-cli 0.151.0-alpha.15"}},
			want:    "install0",
		},
		{
			name:    "a tie keeps the copy on PATH",
			onPath:  []codexAnswer{{banner: "codex-cli 0.160.1"}},
			offPath: []codexAnswer{{banner: "codex-cli 0.160.1"}},
			want:    "path0",
		},
		{
			name:    "codex-acp is not a Codex CLI",
			onPath:  []codexAnswer{{banner: "@agentclientprotocol/codex-acp 1.11.0"}},
			offPath: []codexAnswer{{banner: "codex-cli 0.159.2"}},
			want:    "install0",
		},
		{
			name:    "a copy that cannot run is passed over",
			onPath:  []codexAnswer{{err: errors.New("fork/exec: permission denied")}},
			offPath: []codexAnswer{{banner: "codex-cli 0.159.2"}},
			want:    "install0",
		},
		{
			name:    "when no copy answers the first is kept",
			onPath:  []codexAnswer{{err: errors.New("exit status 1")}},
			offPath: []codexAnswer{{banner: "garbage"}},
			want:    "path0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCodexFixture(t, len(tc.onPath), len(tc.offPath))
			stubLoginShellResolver(t, nil)
			answers := map[string]codexAnswer{}
			named := map[string]string{}
			for i, answer := range tc.onPath {
				answers[fixture.onPath[i]] = answer
				named[fmt.Sprintf("path%d", i)] = fixture.onPath[i]
			}
			for i, answer := range tc.offPath {
				answers[fixture.offPath[i]] = answer
				named[fmt.Sprintf("install%d", i)] = fixture.offPath[i]
			}
			versions := newCodexVersionStub(t, answers)

			entry, ok := discoverCodex(t, versions.probe)
			if !ok || entry.Path != named[tc.want] || entry.Command != "codex" {
				t.Fatalf("codex = %+v (found %v), want %s at %s", entry, ok, tc.want, named[tc.want])
			}
		})
	}
}

// The common machine has one copy: it is used as found, with no extra
// process, exactly as before ranking existed.
func TestDiscoverAgentCLIsRunsNoVersionProbeForOneCodex(t *testing.T) {
	fixture := newCodexFixture(t, 1, 0)
	stubLoginShellResolver(t, nil)

	entry, ok := discoverCodex(t, noVersionProbe(t))
	if !ok || entry.Path != fixture.onPath[0] {
		t.Fatalf("codex = %+v (found %v), want %s", entry, ok, fixture.onPath[0])
	}
}

// Two paths that lead to one file are one copy, kept under the first path;
// a PATH that lists a directory twice does the same.
func TestDiscoverAgentCLIsCountsALinkedCodexOnce(t *testing.T) {
	root := t.TempDir()
	target := writeFakeCLI(t, root, "codex-real")
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	linkFakeCLI(t, target, filepath.Join(first, "codex"))
	linkFakeCLI(t, target, filepath.Join(second, "codex"))
	isolateProviderDiscovery(t, strings.Join([]string{first, second, first}, string(os.PathListSeparator)))
	stubCodexInstallLocations(t, target)
	stubLoginShellResolver(t, nil)

	entry, ok := discoverCodex(t, noVersionProbe(t))
	if want := filepath.Join(first, "codex"); !ok || entry.Path != want {
		t.Fatalf("codex = %+v (found %v), want the first path %s", entry, ok, want)
	}
}

// The login shell is still asked only when PATH has no codex, so the happy
// path never forks the user's shell; its answer is then ranked with the
// install locations like any other copy.
func TestDiscoverAgentCLIsAsksTheShellForCodexOnlyWhenPATHHasNone(t *testing.T) {
	t.Run("a codex on PATH", func(t *testing.T) {
		fixture := newCodexFixture(t, 1, 1)
		writeFakeCLI(t, filepath.Dir(fixture.onPath[0]), "claude")
		shellCalls := stubLoginShellResolver(t, map[string]string{"codex": "/elsewhere/codex"})
		versions := newCodexVersionStub(t, map[string]codexAnswer{
			fixture.onPath[0]:  {banner: "codex-cli 0.159.2"},
			fixture.offPath[0]: {banner: "codex-cli 0.148.0-alpha.15"},
		})

		if entry, _ := discoverCodex(t, versions.probe); entry.Path != fixture.onPath[0] {
			t.Fatalf("codex = %+v, want %s", entry, fixture.onPath[0])
		}
		if shellCalls.Load() != 0 {
			t.Fatalf("login shell forked %d times although PATH had codex, want 0", shellCalls.Load())
		}
	})
	t.Run("no codex on PATH", func(t *testing.T) {
		bin := t.TempDir()
		fromShell := writeFakeCLI(t, t.TempDir(), "codex")
		install := writeFakeCLI(t, t.TempDir(), "codex")
		isolateProviderDiscovery(t, bin)
		writeFakeCLI(t, bin, "claude")
		stubCodexInstallLocations(t, install)
		shellCalls := stubLoginShellResolver(t, map[string]string{"codex": fromShell})
		versions := newCodexVersionStub(t, map[string]codexAnswer{
			fromShell: {banner: "codex-cli 0.136.0"},
			install:   {banner: "codex-cli 0.160.0"},
		})

		if entry, _ := discoverCodex(t, versions.probe); entry.Path != install {
			t.Fatalf("codex = %+v, want the newer install %s over the shell's older copy", entry, install)
		}
		if shellCalls.Load() != 1 {
			t.Fatalf("login shell forked %d times, want 1", shellCalls.Load())
		}
	})
}

// ZENITH_TEAM_CODEX_PATH pins one copy: it is used exactly as written, link and
// all, and nothing is ranked. A pinned path that is missing stays a miss
// rather than quietly becoming some other copy. Pinning the bare name
// "codex" is the default and ranks as usual.
func TestDiscoverAgentCLIsUsesAPinnedCodexPathAsWritten(t *testing.T) {
	root := t.TempDir()
	pinned := filepath.Join(root, "bin", "codex")
	linkFakeCLI(t, writeFakeCLI(t, root, "codex-0.136.0"), pinned)
	newer := writeFakeCLI(t, t.TempDir(), "codex")
	isolateProviderDiscovery(t, t.TempDir())
	stubCodexInstallLocations(t, newer)
	stubLoginShellResolver(t, nil)

	t.Setenv("ZENITH_TEAM_CODEX_PATH", pinned)
	if entry, ok := discoverCodex(t, noVersionProbe(t)); !ok || entry.Path != pinned {
		t.Fatalf("codex = %+v (found %v), want the pinned path %s as written", entry, ok, pinned)
	}

	t.Setenv("ZENITH_TEAM_CODEX_PATH", filepath.Join(root, "missing", "codex"))
	if entry, ok := discoverCodex(t, noVersionProbe(t)); ok {
		t.Fatalf("codex = %+v, want a miss for a pinned path that does not exist", entry)
	}

	t.Setenv("ZENITH_TEAM_CODEX_PATH", "codex")
	if entry, ok := discoverCodex(t, noVersionProbe(t)); !ok || entry.Path != newer {
		t.Fatalf("codex = %+v (found %v), want the default search to find %s", entry, ok, newer)
	}
}

// The off-PATH locations follow CODEX_HOME, use the platform's executable
// name, include the app bundles only on macOS, and put Codex's own
// self-updating copy last so it loses every tie.
func TestCodexInstallLocationsFollowCodexHome(t *testing.T) {
	packages := filepath.Join("/ch", "packages")
	for _, tc := range []struct {
		goos string
		want []string
	}{
		{
			goos: "darwin",
			want: []string{
				filepath.Join(packages, "standalone", "current", "bin", "codex"),
				"/Applications/ChatGPT.app/Contents/Resources/codex",
				"/Applications/Codex.app/Contents/Resources/codex",
				filepath.Join("/h", "Applications", "ChatGPT.app", "Contents", "Resources", "codex"),
				filepath.Join("/h", "Applications", "Codex.app", "Contents", "Resources", "codex"),
				filepath.Join(packages, "app-server-daemon", "current", "bin", "codex"),
			},
		},
		{
			goos: "linux",
			want: []string{
				filepath.Join(packages, "standalone", "current", "bin", "codex"),
				filepath.Join(packages, "app-server-daemon", "current", "bin", "codex"),
			},
		},
		{
			goos: "windows",
			want: []string{
				filepath.Join(packages, "standalone", "current", "bin", "codex.exe"),
				filepath.Join(packages, "app-server-daemon", "current", "bin", "codex.exe"),
			},
		},
	} {
		got := codexInstallLocationsFor(tc.goos, "/ch", "/h")
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("codexInstallLocationsFor(%s) =\n%s\nwant\n%s",
				tc.goos, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
		}
	}
}

// pathExecutables walks the whole PATH in order, skipping entries that would
// resolve against the working directory and files that cannot run.
func TestPathExecutablesListsEveryCopyInPATHOrder(t *testing.T) {
	first, notExecutable, last := t.TempDir(), t.TempDir(), t.TempDir()
	firstCodex := writeFakeCLI(t, first, "codex")
	if err := os.WriteFile(filepath.Join(notExecutable, "codex"), []byte("not a program"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	lastCodex := writeFakeCLI(t, last, "codex")
	t.Setenv("PATH", strings.Join(
		[]string{first, "relative", "", notExecutable, last},
		string(os.PathListSeparator),
	))

	got := pathExecutables("codex")
	if want := []string{firstCodex, lastCodex}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("pathExecutables() = %v, want %v", got, want)
	}
}

// Once every copy has answered, a later pass reads the remembered answers
// and runs nothing, so the two-minute re-probe costs only file stats; a copy
// whose file changed is asked again.
func TestCodexVersionAnswersAreReusedWhileTheFileIsUnchanged(t *testing.T) {
	fixture := newCodexFixture(t, 1, 1)
	stubLoginShellResolver(t, nil)
	versions := newCodexVersionStub(t, map[string]codexAnswer{
		fixture.onPath[0]:  {banner: "codex-cli 0.159.2"},
		fixture.offPath[0]: {banner: "codex-cli 0.160.1"},
	})

	discoverCodex(t, versions.probe)
	discoverCodex(t, versions.probe)
	if versions.total() != 2 {
		t.Fatalf("ran %d version probes over two passes of an unchanged machine, want 2", versions.total())
	}

	rewriteFakeCLI(t, fixture.onPath[0], "upgraded")
	versions.set(fixture.onPath[0], codexAnswer{banner: "codex-cli 0.161.0"})
	entry, _ := discoverCodex(t, versions.probe)
	if versions.callsFor(fixture.onPath[0]) != 2 || versions.callsFor(fixture.offPath[0]) != 1 {
		t.Fatalf("probes after one upgrade: %v, want only the upgraded copy asked again", versions.calls)
	}
	if entry.Path != fixture.onPath[0] {
		t.Fatalf("codex = %+v, want the upgraded copy %s", entry, fixture.onPath[0])
	}
}

// A failed answer is not remembered: the next pass asks that copy again.
func TestCodexVersionFailuresAreAskedAgain(t *testing.T) {
	fixture := newCodexFixture(t, 1, 1)
	stubLoginShellResolver(t, nil)
	versions := newCodexVersionStub(t, map[string]codexAnswer{
		fixture.onPath[0]:  {err: errors.New("text file busy")},
		fixture.offPath[0]: {banner: "codex-cli 0.159.2"},
	})

	discoverCodex(t, versions.probe)
	discoverCodex(t, versions.probe)
	if calls := versions.callsFor(fixture.onPath[0]); calls != 2 {
		t.Fatalf("the failing copy was asked %d times over two passes, want 2", calls)
	}
}

// A copy that never answers is abandoned at the ranking deadline, and the
// pass goes on with the copies that did.
func TestDiscoverAgentCLIsStopsWaitingForAHungCodex(t *testing.T) {
	previous := codexRankTimeout
	t.Cleanup(func() { codexRankTimeout = previous })
	codexRankTimeout = 20 * time.Millisecond
	fixture := newCodexFixture(t, 1, 1)
	stubLoginShellResolver(t, nil)
	versions := newCodexVersionStub(t, map[string]codexAnswer{
		fixture.onPath[0]:  {hang: true},
		fixture.offPath[0]: {banner: "codex-cli 0.159.2"},
	})

	if entry, _ := discoverCodex(t, versions.probe); entry.Path != fixture.offPath[0] {
		t.Fatalf("codex = %+v, want the copy that answered %s", entry, fixture.offPath[0])
	}
}

// Moving to another copy rewires the connector and plugin backends and
// re-registers every company, so the pick holds until something strictly
// newer answers or the picked copy breaks. A tie or a copy that is merely
// slow to answer this pass never moves it.
func TestCodexPickHoldsUntilACopyIsStrictlyNewerOrBroken(t *testing.T) {
	fixture := newCodexFixture(t, 1, 1)
	stubLoginShellResolver(t, nil)
	brew, app := fixture.onPath[0], fixture.offPath[0]
	versions := newCodexVersionStub(t, map[string]codexAnswer{
		brew: {banner: "codex-cli 0.159.2"},
		app:  {banner: "codex-cli 0.160.1"},
	})
	step := func(name, want string) {
		t.Helper()
		if entry, _ := discoverCodex(t, versions.probe); entry.Path != want {
			t.Fatalf("%s: codex = %s, want %s", name, entry.Path, want)
		}
	}
	// The probe itself reports running out of time, so no step depends on a
	// wall-clock deadline.
	slow := codexAnswer{err: fmt.Errorf("version probe: %w", context.DeadlineExceeded)}

	step("the newest copy is picked", app)

	rewriteFakeCLI(t, app, "updating")
	versions.set(app, slow)
	step("the picked copy is slow to answer", app)

	versions.set(app, codexAnswer{banner: "codex-cli 0.160.1"})
	rewriteFakeCLI(t, brew, "upgraded to a tie")
	versions.set(brew, codexAnswer{banner: "codex-cli 0.160.1"})
	step("another copy ties with the pick", app)

	rewriteFakeCLI(t, app, "updating again")
	versions.set(app, slow)
	rewriteFakeCLI(t, brew, "upgraded past the pick")
	versions.set(brew, codexAnswer{banner: "codex-cli 0.161.0"})
	step("another copy is strictly newer than the pick's last answer", brew)

	versions.set(app, codexAnswer{banner: "codex-cli 0.160.1"})
	rewriteFakeCLI(t, brew, "broken")
	versions.set(brew, codexAnswer{err: errors.New("exit status 1")})
	step("the picked copy broke", app)
}

// The pick is logged once when it changes, not on every two-minute pass.
func TestCodexPickIsLoggedOnlyWhenItChanges(t *testing.T) {
	fixture := newCodexFixture(t, 1, 1)
	logs := &lockedBuffer{}
	codexInstalls.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	stubLoginShellResolver(t, nil)
	versions := newCodexVersionStub(t, map[string]codexAnswer{
		fixture.onPath[0]:  {banner: "codex-cli 0.159.2"},
		fixture.offPath[0]: {banner: "codex-cli 0.160.1"},
	})
	picks := func() int { return strings.Count(logs.String(), "Codex CLI picked") }

	discoverCodex(t, versions.probe)
	discoverCodex(t, versions.probe)
	if picks() != 1 {
		t.Fatalf("logged %d picks over two passes with the same pick, want 1:\n%s", picks(), logs.String())
	}
	rewriteFakeCLI(t, fixture.onPath[0], "upgraded")
	versions.set(fixture.onPath[0], codexAnswer{banner: "codex-cli 0.161.0"})
	discoverCodex(t, versions.probe)
	if picks() != 2 || !strings.Contains(logs.String(), fixture.onPath[0]) {
		t.Fatalf("logged %d picks after the pick moved, want 2 naming %s:\n%s",
			picks(), fixture.onPath[0], logs.String())
	}
}

// The real probe reads each copy's banner: the newest of two scripts wins
// and one that exits non-zero is passed over.
func TestDiscoverAgentCLIsReadsRealCodexBanners(t *testing.T) {
	fixture := newCodexFixture(t, 2, 1)
	stubLoginShellResolver(t, nil)
	scripts := map[string]string{
		fixture.onPath[0]:  "echo 'codex-cli 0.159.2'",
		fixture.onPath[1]:  "echo 'codex-cli 0.170.0'; exit 1",
		fixture.offPath[0]: "echo 'codex-cli 0.160.1'",
	}
	for path, body := range scripts {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	if entry, _ := discoverCodex(t, detectAgentVersion); entry.Path != fixture.offPath[0] {
		t.Fatalf("codex = %+v, want the newest working copy %s", entry, fixture.offPath[0])
	}
}

// Refresh setup is how a person moves the daemon to a newer copy installed
// while it runs: it adopts the copy discovery now picks and registers the
// version that copy reports.
func TestRefreshRuntimeSetupMovesCodexToANewerCopy(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newTwoProviderDaemon(t, recorder.serve(t).URL)
	found := lateInstalledAgents()
	found["codex"] = AgentEntry{Path: "/opt/newest/codex", Command: "codex"}
	stubDiscovery(t, found)
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude":  func() (string, error) { return "2.5.0", nil },
		"/opt/newest/codex": func() (string, error) { return "codex-cli 0.160.1", nil },
	})

	if err := d.refreshRuntimeSetup(context.Background(), "test"); err != nil {
		t.Fatalf("refreshRuntimeSetup: %v", err)
	}
	if got := d.agents()["codex"].Path; got != "/opt/newest/codex" {
		t.Fatalf("agents()[codex].Path = %q, want the newer copy", got)
	}
	for _, companyID := range adoptionCompanies {
		if codex := recorder.findRegistered(t, companyID, 0, "codex"); codex.Version != "codex-cli 0.160.1" {
			t.Fatalf("%s codex entry = %+v, want the newer copy's version", companyID, codex)
		}
	}
}

// A version manager's shim runs whatever Codex is installed from a file that
// never changes, so a remembered answer can outlive an upgrade. Refresh setup
// is how a person asks for a fresh answer: it asks every copy again.
func TestRefreshRuntimeSetupAsksEveryCodexCopyAgain(t *testing.T) {
	fixture := newCodexFixture(t, 1, 1)
	stubLoginShellResolver(t, nil)
	shim, app := fixture.onPath[0], fixture.offPath[0]
	versions := newCodexVersionStub(t, map[string]codexAnswer{
		shim: {banner: "codex-cli 0.150.0"},
		app:  {banner: "codex-cli 0.160.0"},
	})
	recorder := newAdoptionRecorder()
	d := newTwoProviderDaemon(t, recorder.serve(t).URL)
	stubDynamicDiscovery(t, func() map[string]AgentEntry {
		found := adoptionAgents()
		found["codex"] = discoverAgentCLIs(cachedShellResolvedAgents, versions.probe)["codex"]
		return found
	})
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		app:                func() (string, error) { return "codex-cli 0.160.0", nil },
		shim:               func() (string, error) { return "codex-cli 0.170.0", nil },
	})

	if err := d.refreshRuntimeSetup(context.Background(), "test"); err != nil {
		t.Fatalf("first refreshRuntimeSetup: %v", err)
	}
	if got := d.agents()["codex"].Path; got != app {
		t.Fatalf("agents()[codex].Path = %q, want the newer app copy %s", got, app)
	}

	// `volta install @openai/codex@0.170.0`: the shim file is unchanged.
	versions.set(shim, codexAnswer{banner: "codex-cli 0.170.0"})
	if err := d.refreshRuntimeSetup(context.Background(), "test"); err != nil {
		t.Fatalf("second refreshRuntimeSetup: %v", err)
	}
	if got := d.agents()["codex"].Path; got != shim {
		t.Fatalf("agents()[codex].Path = %q, want the upgraded shim %s", got, shim)
	}
	if calls := versions.callsFor(shim); calls != 2 {
		t.Fatalf("the shim was asked %d times over two refreshes, want 2", calls)
	}
}

// The two-minute availability pass does not move a registered provider to
// another copy: that waits for Refresh setup or a restart.
func TestProviderAvailabilityLeavesARegisteredCodexOnItsPath(t *testing.T) {
	recorder := newAdoptionRecorder()
	d := newTwoProviderDaemon(t, recorder.serve(t).URL)
	found := lateInstalledAgents()
	found["codex"] = AgentEntry{Path: "/opt/newest/codex", Command: "codex"}
	stubDiscovery(t, found)
	stubVersionProbeByPath(d, map[string]func() (string, error){
		"/opt/fake/claude": func() (string, error) { return "2.5.0", nil },
		"/opt/fake/codex":  func() (string, error) { return "0.120.0", nil },
	})

	d.refreshProviderAvailability(context.Background())

	if got := d.agents()["codex"].Path; got != "/opt/fake/codex" {
		t.Fatalf("agents()[codex].Path = %q, want the registered copy kept", got)
	}
}

// lockedBuffer is a bytes.Buffer safe to write from a log handler.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
