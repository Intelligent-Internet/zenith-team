package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// shellResolveTTL bounds how long one login-shell PATH resolution is reused
// across probeAgentCLIs calls. Deliberately long: resolveAgentsViaLoginShell
// forks the user's login shell and runs their rc files, and there is almost
// always at least one uninstalled provider to miss LookPath on.
var shellResolveTTL = 30 * time.Minute

var (
	shellResolveMu    sync.Mutex
	shellResolveCache map[string]string
	shellResolveKey   string
	shellResolvedAt   time.Time
)

// shellResolveEnvKey fingerprints the environment that determines what a
// login shell resolves; any change invalidates the cache immediately.
func shellResolveEnvKey() string {
	return strings.Join([]string{
		os.Getenv("PATH"),
		os.Getenv("SHELL"),
		os.Getenv("HOME"),
	}, "\x00")
}

// cachedShellResolvedAgents resolves every agent command name through the
// user's login shell, reusing the previous result for shellResolveTTL as long
// as the resolution-relevant environment is unchanged.
func cachedShellResolvedAgents() map[string]string {
	shellResolveMu.Lock()
	defer shellResolveMu.Unlock()
	key := shellResolveEnvKey()
	if shellResolveCache != nil && shellResolveKey == key && time.Since(shellResolvedAt) < shellResolveTTL {
		return shellResolveCache
	}
	resolved := resolveAgentsViaLoginShell(agentCommandNames)
	if resolved == nil {
		// Distinguish "resolved nothing" from "never resolved" so a failing
		// shell doesn't get re-forked on every probe inside the TTL window.
		resolved = map[string]string{}
	}
	shellResolveCache = resolved
	shellResolveKey = key
	shellResolvedAt = time.Now()
	return shellResolveCache
}

// agentCommandNames lists the command names the probe loop tries before any
// BOARDLY_*_PATH override is applied.
var agentCommandNames = []string{"claude", "codex"}

// probeAgentCLIs discovers which agent CLIs are installed on this machine and
// returns one AgentEntry per provider that resolved. It applies no
// minimum-version gate (detectRuntimes owns that); it runs --version only to
// pick the newest of several installed Codex copies (codex_install.go). A
// var so tests can stub discovery without installing real CLIs.
var probeAgentCLIs = func() map[string]AgentEntry {
	return discoverAgentCLIs(cachedShellResolvedAgents, detectAgentVersion)
}

// reprobeAgentCLIs is the discovery pass the availability re-probe runs while
// the daemon is up (provider_availability.go). It bypasses the login-shell
// cache: a CLI installed into a directory only the user's rc files add
// (nvm, fnm, volta, ~/.local/bin) is invisible to exec.LookPath in a
// GUI-launched daemon, and the cached resolution would hide it for up to
// shellResolveTTL — far longer than the two minutes the re-probe promises.
// The fork stays bounded by resolveAgentsViaLoginShell's timeout, happens at
// most once per pass, and only when a bare command name misses PATH. A var so
// tests can stub discovery.
var reprobeAgentCLIs = func() map[string]AgentEntry {
	return discoverAgentCLIs(freshShellResolvedAgents, detectAgentVersion)
}

// freshShellResolvedAgents drops the cached login-shell resolution and
// resolves again, so the answer is current and cached for every other caller.
func freshShellResolvedAgents() map[string]string {
	shellResolveMu.Lock()
	shellResolvedAt = time.Time{}
	shellResolveMu.Unlock()
	return cachedShellResolvedAgents()
}

// discoverAgentCLIs is one discovery pass over the supported providers.
// shellResolve is the login-shell fallback; it is consulted lazily and at most
// once per pass, however many providers miss PATH. versionOf reads a CLI's
// --version banner when several Codex copies have to be ranked.
func discoverAgentCLIs(
	shellResolve func() map[string]string,
	versionOf func(ctx context.Context, path string) (string, error),
) map[string]AgentEntry {
	// exec.LookPath is the primary path, but a GUI-launched daemon does not
	// inherit the user's interactive shell PATH — fnm/nvm/volta multishells,
	// the Anthropic native installer prefix, and per-user npm prefixes all
	// live in dirs only ~/.zshrc adds. The login-shell fallback is lazy so
	// the happy path never pays for spawning the user's shell.
	var (
		shellOnce     sync.Once
		shellResolved map[string]string
	)
	getShellResolved := func() map[string]string {
		shellOnce.Do(func() { shellResolved = shellResolve() })
		return shellResolved
	}
	probe := func(envVar, defaultCmd, modelEnv string) (AgentEntry, bool) {
		cmd := envOrDefault(envVar, defaultCmd)
		model := strings.TrimSpace(os.Getenv(modelEnv))
		if defaultCmd == "codex" && cmd == defaultCmd {
			// Unpinned Codex: every installed copy is a candidate and the
			// newest one wins (codex_install.go).
			candidates := codexCandidates(getShellResolved)
			if len(candidates) == 0 {
				return AgentEntry{}, false
			}
			path := codexInstalls.pick(context.Background(), candidates, versionOf)
			return AgentEntry{Path: path, Command: cmd, Model: model}, true
		}
		if path, err := findProviderExecutable(cmd); err == nil {
			return AgentEntry{Path: path, Command: cmd, Model: model}, true
		}
		// The shell fallback only rescues bare command names: an operator who
		// pinned BOARDLY_*_PATH to a missing path should hard-miss.
		if strings.ContainsAny(cmd, "/\\") {
			return AgentEntry{}, false
		}
		if path, ok := getShellResolved()[cmd]; ok {
			return AgentEntry{Path: path, Command: cmd, Model: model}, true
		}
		return AgentEntry{}, false
	}

	agents := map[string]AgentEntry{}
	if e, ok := probe("BOARDLY_CLAUDE_PATH", "claude", "BOARDLY_CLAUDE_MODEL"); ok {
		agents["claude"] = e
	}
	if e, ok := probe("BOARDLY_CODEX_PATH", "codex", "BOARDLY_CODEX_MODEL"); ok {
		agents["codex"] = e
	}
	return agents
}

// pathExecutables lists every executable called name on the daemon's PATH,
// in PATH order — exec.LookPath stops at the first. Each directory is asked
// through exec.LookPath, so Windows extensions (codex.exe) are honored.
func pathExecutables(name string) []string {
	var found []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		// An empty or relative entry resolves against the working directory,
		// which exec.LookPath refuses too (exec.ErrDot).
		if !filepath.IsAbs(dir) {
			continue
		}
		if path, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			found = append(found, settleShellScopedPath(path))
		}
	}
	return found
}

const (
	// loginShellResolveWaitDelay is the hard cap after the context kills the
	// shell: rc files routinely background things that inherit stdout, and
	// cmd.Output() would otherwise block on EOF for as long as they live.
	loginShellResolveWaitDelay = 2 * time.Second
)

// supportedLoginShells limits which interpreters we invoke via
// `<shell> -ilc <script>`. POSIX-compatible only (no fish).
var supportedLoginShells = map[string]struct{}{
	"bash": {},
	"zsh":  {},
	"sh":   {},
	"dash": {},
	"ksh":  {},
}

// resolveAgentsViaLoginShell asks the user's login shell to print the
// canonical absolute path to each name. Returns name → path for whatever the
// shell could find; empty on any failure. A var so tests can stub the fork.
var resolveAgentsViaLoginShell = func(names []string) map[string]string {
	out := map[string]string{}
	if len(names) == 0 {
		return out
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return out
	}
	if _, ok := supportedLoginShells[filepath.Base(shell)]; !ok {
		return out
	}

	safe := make([]string, 0, len(names))
	for _, n := range names {
		if isSafeAgentName(n) {
			safe = append(safe, n)
		}
	}
	if len(safe) == 0 {
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-ilc", buildLoginShellResolveScript(safe))
	cmd.WaitDelay = loginShellResolveWaitDelay
	raw, err := cmd.Output()
	if err != nil {
		return out
	}

	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, path := parts[0], strings.TrimSpace(parts[1])
		if !filepath.IsAbs(path) {
			continue
		}
		// The path the shell gave us must still be executable from the
		// daemon's perspective right now — fnm multishell dirs can vanish on
		// shell exit.
		if _, err := exec.LookPath(path); err != nil {
			continue
		}
		out[name] = path
	}
	return out
}

// buildLoginShellResolveScript returns the shell script run inside
// `$SHELL -ilc`: it unaliases each name (so `command -v` reaches a real
// binary), resolves it via POSIX `command -v`, rejects non-absolute results,
// and canonicalises the directory via `cd ... && pwd -P` while the spawned
// shell is still alive. All input names are vetted by isSafeAgentName, so
// inlining them unquoted is safe.
func buildLoginShellResolveScript(names []string) string {
	var b strings.Builder
	b.WriteString("for n in")
	for _, n := range names {
		b.WriteByte(' ')
		b.WriteString(n)
	}
	b.WriteString("; do\n")
	b.WriteString("  unalias \"$n\" 2>/dev/null\n")
	b.WriteString("  unset -f \"$n\" 2>/dev/null\n")
	b.WriteString("  p=$(command -v \"$n\" 2>/dev/null) || continue\n")
	b.WriteString("  [ -n \"$p\" ] || continue\n")
	b.WriteString("  case \"$p\" in /*) ;; *) continue ;; esac\n")
	b.WriteString(
		"  d=$(dirname \"$p\") && f=$(basename \"$p\") && c=$(cd \"$d\" 2>/dev/null && pwd -P) || continue\n",
	)
	b.WriteString("  printf '%s\\t%s\\n' \"$n\" \"$c/$f\"\n")
	b.WriteString("done\n")
	return b.String()
}

// isSafeAgentName checks that s is a bare command name composed only of
// characters safe to inline into a shell script.
func isSafeAgentName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// detectAgentVersion runs `<path> --version` with a short timeout and returns
// the trimmed first line. It is the default cliProbes.version.
func detectAgentVersion(ctx context.Context, path string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, path, "--version")
	// A wrapper (npm's codex.js runs node, which runs the native binary) can
	// leave a child holding stdout after the kill; stop waiting for it.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line), nil
}
