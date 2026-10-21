package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/execenv"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

// One machine often holds several copies of the Codex CLI: one from Homebrew
// or npm on PATH, one inside the ChatGPT app, one Codex keeps up to date for
// itself under ~/.codex. PATH order says nothing about which is newest — an
// old npm global first on PATH used to win over a current app copy — so
// discovery asks every copy for its version and keeps the newest.

// codexRankTimeout bounds one ranking pass. The first run of a freshly
// updated 240 MB binary can take seconds while macOS scans it; a copy that
// does not answer in time is left out of this pass, never chosen by default.
var codexRankTimeout = 8 * time.Second

// codexInstallLocations returns where Codex installs itself off PATH, in
// tie-break order. A var so tests can point discovery at fixtures.
var codexInstallLocations = func() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return codexInstallLocationsFor(runtime.GOOS, execenv.SharedCodexHome(), home)
}

// codexInstallLocationsFor builds the off-PATH install list for one
// platform. Every path goes through a stable link (current, the app bundle),
// so the path kept follows each update.
func codexInstallLocationsFor(goos, codexHome, home string) []string {
	exe := "codex"
	if goos == "windows" {
		exe = "codex.exe"
	}
	packages := filepath.Join(codexHome, "packages")
	// OpenAI's install script; the ~/.local/bin/codex it adds links here.
	paths := []string{filepath.Join(packages, "standalone", "current", "bin", exe)}
	if goos == "darwin" {
		// The Codex desktop app bundles its CLI instead of putting it on PATH.
		// ChatGPT.app is its current name, Codex.app the earlier one.
		paths = append(paths,
			"/Applications/ChatGPT.app/Contents/Resources/codex",
			"/Applications/Codex.app/Contents/Resources/codex",
		)
		if home != "" {
			paths = append(paths,
				filepath.Join(home, "Applications", "ChatGPT.app", "Contents", "Resources", "codex"),
				filepath.Join(home, "Applications", "Codex.app", "Contents", "Resources", "codex"),
			)
		}
	}
	// The copy Codex's own app-server daemon keeps updating. Its layout is
	// Codex's private business, so it comes last and loses every tie to a
	// copy the user installed.
	return append(paths, filepath.Join(packages, "app-server-daemon", "current", "bin", exe))
}

// codexCandidate is one installed copy of the Codex CLI.
type codexCandidate struct {
	// path is where the copy was found, symlinks kept: what the daemon runs.
	path string
	// info describes the file path leads to; it identifies duplicates and
	// tells the version cache whether the file changed.
	info fs.FileInfo
}

// codexCandidates lists every installed copy of the Codex CLI in tie-break
// order: each codex on PATH, then the login shell's answer (asked only when
// PATH has none, so the happy path never forks the user's shell), then the
// install locations. Paths that lead to the same file count once, under the
// first path.
func codexCandidates(shellResolved func() map[string]string) []codexCandidate {
	paths := pathExecutables("codex")
	if len(paths) == 0 {
		if path, ok := shellResolved()["codex"]; ok {
			paths = append(paths, path)
		}
	}
	paths = append(paths, codexInstallLocations()...)

	candidates := make([]codexCandidate, 0, len(paths))
	for _, path := range paths {
		// os.Stat rather than exec.LookPath: a copy that exists but lost its
		// execute bit stays a candidate, so registration can say why it
		// cannot run instead of Codex silently going missing.
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if slices.ContainsFunc(candidates, func(c codexCandidate) bool { return os.SameFile(c.info, info) }) {
			continue
		}
		candidates = append(candidates, codexCandidate{path: path, info: info})
	}
	return candidates
}

// codexBannerVersion reads a `codex --version` banner ("codex-cli 0.160.1").
// Anything else — codex-acp, a version manager's own banner, an error
// message — is not a Codex CLI answering.
func codexBannerVersion(banner string) (agent.Version, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(banner), "codex-cli ")
	if !ok {
		return agent.Version{}, false
	}
	version, err := agent.ParseVersion(rest)
	return version, err == nil
}

// codexNewer reports whether a copy at version a should be used over one at
// b: a release beats any prerelease, as Codex's own updater treats them, and
// otherwise the higher version wins.
func codexNewer(a, b agent.Version) bool {
	if a.IsPrerelease() != b.IsPrerelease() {
		return !a.IsPrerelease()
	}
	return a.Compare(b) > 0
}

// codexVersionAnswer is one copy's last --version banner. It stays valid
// while the file it came from is unchanged.
type codexVersionAnswer struct {
	size    int64
	modTime time.Time
	mode    fs.FileMode
	banner  string
}

func (a codexVersionAnswer) describes(info fs.FileInfo) bool {
	return a.size == info.Size() && a.modTime.Equal(info.ModTime()) && a.mode == info.Mode()
}

// codexVersionResult is what one ranking pass learned about one copy.
type codexVersionResult struct {
	banner  string
	version agent.Version
	// ok means the copy answered with a codex-cli banner.
	ok bool
	// timedOut means the copy did not answer in time: no news either way.
	timedOut bool
	err      error
}

// codexInstallMemory carries what one discovery pass learned into the next:
// each copy's --version answer, so the two-minute re-probe costs only file
// stats once every copy has answered, and the copy picked last, so a pass
// that merely failed to hear from it does not move the daemon elsewhere.
type codexInstallMemory struct {
	mu sync.Mutex
	// answers is keyed by the real path of each copy's file.
	answers       map[string]codexVersionAnswer
	picked        string
	pickedVersion agent.Version
	pickedKnown   bool
	// logger receives the pick and per-copy lines; nil means slog.Default().
	logger *slog.Logger
}

// codexInstalls is the process-wide memory every discovery pass shares.
var codexInstalls = &codexInstallMemory{}

// forgetAnswers drops every remembered --version answer, so the next pass
// asks each copy again. Refresh setup calls it: a person asked for a fresh
// answer, and a version manager's shim (Volta, mise) runs a newly installed
// Codex from a file that did not change. The pick itself is kept, so a tie
// still does not move the daemon.
func (m *codexInstallMemory) forgetAnswers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answers = nil
}

func (m *codexInstallMemory) log() *slog.Logger {
	if m.logger != nil {
		return m.logger
	}
	return slog.Default()
}

// pick returns the path of the copy to use, logging when it changes. One
// copy is used as found, with no extra process; several are ranked by
// version. When no copy answers, the first is kept so the registration
// probe reports why it cannot run.
func (m *codexInstallMemory) pick(
	ctx context.Context,
	candidates []codexCandidate,
	versionOf func(ctx context.Context, path string) (string, error),
) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(candidates) == 1 {
		m.remember(candidates[0].path, nil, len(candidates))
		return candidates[0].path
	}
	results := m.versions(ctx, candidates, versionOf)
	logger := m.log()
	for i, c := range candidates {
		r := results[i]
		logger.Debug("Codex CLI candidate", "path", c.path, "banner", r.banner, "error", r.err)
	}

	best := -1
	for i, r := range results {
		if r.ok && (best < 0 || codexNewer(r.version, results[best].version)) {
			best = i
		}
	}
	choice := best
	// Stay on the previous pick unless something strictly newer answered or
	// it broke. Switching rewires the connector and plugin backends and
	// re-registers every company, so a tie or a slow answer must not do it.
	isPrevious := func(c codexCandidate) bool { return c.path == m.picked }
	if previous := slices.IndexFunc(candidates, isPrevious); previous >= 0 {
		switch r := results[previous]; {
		case r.ok:
			if best < 0 || !codexNewer(results[best].version, r.version) {
				choice = previous
			}
		case r.timedOut:
			if best < 0 || !m.pickedKnown || !codexNewer(results[best].version, m.pickedVersion) {
				choice = previous
			}
		}
		// Any other failure means the previous pick is broken: rank without it.
	}
	if choice < 0 {
		choice = 0
	}
	m.remember(candidates[choice].path, &results[choice], len(candidates))
	return candidates[choice].path
}

// remember records the pick and logs it when it changed. result is nil when
// the pick was made without asking for a version.
func (m *codexInstallMemory) remember(path string, result *codexVersionResult, candidates int) {
	changed := path != m.picked
	switch {
	case result != nil && result.ok:
		m.pickedVersion, m.pickedKnown = result.version, true
	case changed:
		m.pickedVersion, m.pickedKnown = agent.Version{}, false
	}
	m.picked = path
	if !changed {
		return
	}
	banner := ""
	if result != nil {
		banner = result.banner
	}
	m.log().Info("Codex CLI picked", "path", path, "banner", banner, "candidates", candidates)
}

// versions asks every copy for its --version in parallel under one
// deadline, reusing the remembered answer of a copy whose file is unchanged.
// Only an answer from a run that exited 0 is remembered: an error or a
// timeout is asked again next pass.
func (m *codexInstallMemory) versions(
	ctx context.Context,
	candidates []codexCandidate,
	versionOf func(ctx context.Context, path string) (string, error),
) []codexVersionResult {
	ctx, cancel := context.WithTimeout(ctx, codexRankTimeout)
	defer cancel()

	results := make([]codexVersionResult, len(candidates))
	keys := make([]string, len(candidates))
	var wg sync.WaitGroup
	for i, c := range candidates {
		keys[i] = c.path
		if target, err := filepath.EvalSymlinks(c.path); err == nil {
			keys[i] = target
		}
		if answer, ok := m.answers[keys[i]]; ok && answer.describes(c.info) {
			results[i].banner = answer.banner
			continue
		}
		wg.Go(func() {
			banner, err := versionOf(ctx, c.path)
			results[i].banner, results[i].err = banner, err
			// Read the deadline right away: a copy that failed fast is
			// broken, not slow, even when another copy later runs out the
			// clock. A probe may also report its own deadline.
			results[i].timedOut = err != nil &&
				(ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded))
		})
	}
	wg.Wait()

	answers := make(map[string]codexVersionAnswer, len(candidates))
	for i, c := range candidates {
		if results[i].err != nil {
			continue
		}
		answers[keys[i]] = codexVersionAnswer{
			size:    c.info.Size(),
			modTime: c.info.ModTime(),
			mode:    c.info.Mode(),
			banner:  results[i].banner,
		}
		results[i].version, results[i].ok = codexBannerVersion(results[i].banner)
		if !results[i].ok {
			results[i].err = fmt.Errorf("not a Codex CLI version banner: %q", results[i].banner)
		}
	}
	m.answers = answers
	return results
}
