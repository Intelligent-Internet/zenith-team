package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// resetShellResolveCache empties the login-shell resolution cache for the
// test and restores whatever was cached afterwards, so a test neither
// inherits nor leaks a resolution.
func resetShellResolveCache(t *testing.T) {
	t.Helper()
	shellResolveMu.Lock()
	previousCache, previousKey, previousAt := shellResolveCache, shellResolveKey, shellResolvedAt
	shellResolveCache, shellResolveKey, shellResolvedAt = nil, "", time.Time{}
	shellResolveMu.Unlock()
	t.Cleanup(func() {
		shellResolveMu.Lock()
		shellResolveCache, shellResolveKey, shellResolvedAt = previousCache, previousKey, previousAt
		shellResolveMu.Unlock()
	})
}

// stubLoginShellResolver replaces the `$SHELL -ilc` fork with a canned
// answer and counts how often discovery reached for it.
func stubLoginShellResolver(t *testing.T, resolved map[string]string) *atomic.Int32 {
	t.Helper()
	previous := resolveAgentsViaLoginShell
	t.Cleanup(func() { resolveAgentsViaLoginShell = previous })
	var calls atomic.Int32
	resolveAgentsViaLoginShell = func([]string) map[string]string {
		calls.Add(1)
		out := make(map[string]string, len(resolved))
		for name, path := range resolved {
			out[name] = path
		}
		return out
	}
	return &calls
}

// isolateProviderDiscovery points discovery at dir as the whole PATH with no
// BOARDLY_* overrides, no Codex install locations off PATH and a fresh
// memory of Codex picks, so what it finds is exactly what the test placed
// there or what the (stubbed) login shell answers.
func isolateProviderDiscovery(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("FNM_MULTISHELL_PATH", "")
	for _, key := range []string{
		"BOARDLY_CLAUDE_PATH", "BOARDLY_CODEX_PATH", "BOARDLY_CLAUDE_MODEL", "BOARDLY_CODEX_MODEL",
	} {
		t.Setenv(key, "")
	}
	stubCodexInstallLocations(t)
	previousMemory := codexInstalls
	t.Cleanup(func() { codexInstalls = previousMemory })
	codexInstalls = &codexInstallMemory{}
	resetShellResolveCache(t)
}

// stubCodexInstallLocations replaces the off-PATH Codex install list (the
// app bundles, ~/.codex/packages) with paths.
func stubCodexInstallLocations(t *testing.T, paths ...string) {
	t.Helper()
	previous := codexInstallLocations
	t.Cleanup(func() { codexInstallLocations = previous })
	codexInstallLocations = func() []string { return slices.Clone(paths) }
}

// writeFakeCLI drops an executable script named name into dir and returns
// its path, which is what discovery reports: the path a CLI is found at,
// never the file a symlink on the way points to.
func writeFakeCLI(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return path
}

// linkFakeCLI points the symlink link at target, replacing any link already
// there, the way an installer's upgrade retargets bin/codex.
func linkFakeCLI(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("make %s: %v", filepath.Dir(link), err)
	}
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove %s: %v", link, err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("link %s -> %s: %v", link, target, err)
	}
}

// The availability re-probe must see a CLI installed into a directory only
// the user's rc files add (nvm, fnm, volta, ~/.local/bin) on its next tick,
// not after the 30-minute login-shell cache expires: probeAgentCLIs reuses
// the cached resolution, reprobeAgentCLIs resolves again — and refreshes the
// cache for everyone else.
func TestReprobeAgentCLIsBypassesTheLoginShellCache(t *testing.T) {
	empty := t.TempDir()
	isolateProviderDiscovery(t, empty)
	codexPath := writeFakeCLI(t, t.TempDir(), "codex")
	shellCalls := stubLoginShellResolver(t, map[string]string{"codex": codexPath})

	if found := probeAgentCLIs(); found["codex"].Path != codexPath {
		t.Fatalf("probeAgentCLIs() = %+v, want codex from the login shell", found)
	}
	probeAgentCLIs()
	if shellCalls.Load() != 1 {
		t.Fatalf("login shell forked %d times across two cached probes, want 1", shellCalls.Load())
	}

	found := reprobeAgentCLIs()
	if shellCalls.Load() != 2 {
		t.Fatalf("login shell forked %d times after a re-probe, want 2 (the cache bypassed)", shellCalls.Load())
	}
	if entry, ok := found["codex"]; !ok || entry.Path != codexPath || entry.Command != "codex" {
		t.Fatalf("reprobeAgentCLIs() = %+v, want codex at %s", found, codexPath)
	}
	if _, ok := found["claude"]; ok {
		t.Fatalf("reprobeAgentCLIs() = %+v, want no claude: nothing resolves it", found)
	}

	// The re-probe's fresh answer is what the cached path serves next.
	probeAgentCLIs()
	if shellCalls.Load() != 2 {
		t.Fatalf("login shell forked %d times, want the re-probe's result reused by the cached probe",
			shellCalls.Load())
	}
}

// One discovery pass forks the login shell at most once, however many
// providers miss PATH — the second miss reuses the first answer.
func TestReprobeAgentCLIsForksTheLoginShellOncePerPass(t *testing.T) {
	isolateProviderDiscovery(t, t.TempDir())
	shellCalls := stubLoginShellResolver(t, map[string]string{})

	if found := reprobeAgentCLIs(); len(found) != 0 {
		t.Fatalf("reprobeAgentCLIs() = %+v, want nothing found", found)
	}
	if shellCalls.Load() != 1 {
		t.Fatalf("login shell forked %d times in one pass with two misses, want 1", shellCalls.Load())
	}
}

// exec.LookPath stays the primary path: when every provider is on PATH the
// login shell is never forked, and the entry carries the resolved path.
func TestDiscoverAgentCLIsPrefersLookPathOverTheShell(t *testing.T) {
	bin := t.TempDir()
	isolateProviderDiscovery(t, bin)
	claudePath := writeFakeCLI(t, bin, "claude")
	codexPath := writeFakeCLI(t, bin, "codex")
	shellCalls := stubLoginShellResolver(t, map[string]string{"codex": "/elsewhere/codex"})

	found := discoverAgentCLIs(
		func() map[string]string { return resolveAgentsViaLoginShell(agentCommandNames) },
		noVersionProbe(t),
	)

	if found["claude"].Path != claudePath || found["codex"].Path != codexPath {
		t.Fatalf("discoverAgentCLIs() = %+v, want both providers from PATH", found)
	}
	if shellCalls.Load() != 0 {
		t.Fatalf("login shell forked %d times although PATH resolved every provider, want 0", shellCalls.Load())
	}
}

// Installers upgrade a CLI by moving the link it is found through: Homebrew
// relinks bin/codex into a new Caskroom/<version> and deletes the old one,
// and Claude's installer retargets ~/.local/bin/claude into a new
// versions/<version>. Discovery keeps the link, so the stored path follows
// the upgrade instead of vanishing with the old version's folder.
func TestDiscoverAgentCLIsKeepsTheLinkAnUpgradeMoves(t *testing.T) {
	for _, tc := range []struct {
		provider string
		link     string // where the link sits, relative to the test root
		oldPath  string // the version folder it points into first
		newPath  string // the folder the upgrade moves it to
	}{
		{
			provider: "codex",
			link:     "bin/codex",
			oldPath:  "Caskroom/codex/0.159.2/bin/codex",
			newPath:  "Caskroom/codex/0.160.1/bin/codex",
		},
		{
			provider: "claude",
			link:     ".local/bin/claude",
			oldPath:  ".local/share/claude/versions/2.1.5",
			newPath:  ".local/share/claude/versions/2.1.6",
		},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			root := t.TempDir()
			link := filepath.Join(root, tc.link)
			isolateProviderDiscovery(t, filepath.Dir(link))
			stubLoginShellResolver(t, nil)
			for _, version := range []string{tc.oldPath, tc.newPath} {
				dir := filepath.Join(root, filepath.Dir(version))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("make %s: %v", dir, err)
				}
				writeFakeCLI(t, dir, filepath.Base(version))
			}
			linkFakeCLI(t, filepath.Join(root, tc.oldPath), link)

			found := discoverAgentCLIs(cachedShellResolvedAgents, noVersionProbe(t))
			if got := found[tc.provider].Path; got != link {
				t.Fatalf("%s path = %q, want the link %q itself", tc.provider, got, link)
			}

			linkFakeCLI(t, filepath.Join(root, tc.newPath), link)
			if err := os.RemoveAll(filepath.Join(root, tc.oldPath)); err != nil {
				t.Fatalf("remove the old version: %v", err)
			}
			if !agentExecutablePresent(found[tc.provider].Path) {
				t.Fatalf("stored path %q vanished with the old version, want it to follow the upgrade",
					found[tc.provider].Path)
			}
		})
	}
}

// fnm puts a per-shell link ($FNM_MULTISHELL_PATH) on PATH and repoints it on
// every `fnm use` in the shell that started the daemon. That one link is
// settled to the Node install it points at, so switching Node versions in
// the terminal does not switch, or remove, the CLI under a running daemon.
func TestDiscoverAgentCLIsSettlesFnmPerShellLink(t *testing.T) {
	root := t.TempDir()
	node22 := filepath.Join(root, "node-versions", "v22", "installation")
	node18 := filepath.Join(root, "node-versions", "v18", "installation")
	for _, dir := range []string{filepath.Join(node22, "bin"), filepath.Join(node18, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
	}
	writeFakeCLI(t, filepath.Join(node22, "bin"), "claude")
	writeFakeCLI(t, filepath.Join(node22, "bin"), "codex")
	multishell := filepath.Join(root, "fnm_multishells", "4242_1")
	linkFakeCLI(t, node22, multishell)
	isolateProviderDiscovery(t, filepath.Join(multishell, "bin"))
	t.Setenv("FNM_MULTISHELL_PATH", multishell)
	stubLoginShellResolver(t, nil)

	found := discoverAgentCLIs(cachedShellResolvedAgents, noVersionProbe(t))

	settled, err := filepath.EvalSymlinks(node22)
	if err != nil {
		t.Fatalf("resolve %s: %v", node22, err)
	}
	linkFakeCLI(t, node18, multishell) // `fnm use 18` in the launching terminal
	for _, provider := range []string{"claude", "codex"} {
		want := filepath.Join(settled, "bin", provider)
		if got := found[provider].Path; got != want {
			t.Fatalf("%s path = %q, want %q (the per-shell link settled)", provider, got, want)
		}
		if !agentExecutablePresent(want) {
			t.Fatalf("%s path %q vanished when the shell switched Node versions", provider, want)
		}
	}
}

// A version manager's shim (Volta, mise, asdf) is one program linked under
// every tool's name; it picks the real tool from the name it is called by.
// Following the link would store the shim program itself and lose the name.
func TestDiscoverAgentCLIsKeepsTheNameAShimIsCalledBy(t *testing.T) {
	root := t.TempDir()
	shim := writeFakeCLI(t, root, "volta-shim")
	bin := filepath.Join(root, "bin")
	linkFakeCLI(t, shim, filepath.Join(bin, "codex"))
	isolateProviderDiscovery(t, bin)
	stubLoginShellResolver(t, nil)

	found := discoverAgentCLIs(cachedShellResolvedAgents, noVersionProbe(t))
	if got, want := found["codex"].Path, filepath.Join(bin, "codex"); got != want {
		t.Fatalf("codex path = %q, want the shim link %q", got, want)
	}
}
