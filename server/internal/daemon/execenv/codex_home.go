package execenv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Files to symlink from the shared ~/.codex/ into the per-run CODEX_HOME.
// Symlinks share state (e.g. auth tokens) so changes propagate automatically.
var codexSymlinkedFiles = []string{
	"auth.json",
}

// Files to copy from the shared ~/.codex/ into the per-run CODEX_HOME.
// Copies are isolated — run-local config and cache refreshes don't mutate
// the shared home.
var codexCopiedFiles = []string{
	"config.json",
	"config.toml",
	"instructions.md",
}

const (
	codexModelsCacheFile = "models_cache.json"
)

// Files whose contents select the model provider/catalog used by Codex. The
// run-local models cache is only reusable while this source configuration
// remains unchanged.
var codexModelsCacheConfigFiles = []string{
	"config.json",
	"config.toml",
}

// CodexHomeOptions carries optional inputs for prepareCodexHomeWithOpts that
// affect the generated per-run config.toml.
type CodexHomeOptions struct {
	// CodexVersion is the detected Codex CLI version. Empty means unknown; on
	// macOS, unknown is treated as "probably broken" so the daemon falls back
	// to danger-full-access for network access. See codex_sandbox.go.
	CodexVersion string
	// GOOS overrides the target platform when deciding the sandbox policy.
	// Empty means use runtime.GOOS; tests pin it.
	GOOS string
	// ResumeSessionID is the Codex session this run intends to resume, when
	// any. Consulted when populating the per-conversation session store or
	// migrating a legacy home whose sessions/ still symlinks the shared
	// ~/.codex/sessions. Empty means a fresh thread.
	ResumeSessionID string
	// IsLocalDirectory marks a run in the user's own directory: its codex-home
	// is fresh per run, so sessions/ links to the per-conversation store
	// (SessionStoreKey) that survives across runs.
	IsLocalDirectory bool
	// SessionStoreKey is a stable per-(agent, conversation) relative path
	// identifying this run's persistent Codex session store. Empty keeps
	// sessions/ run-local.
	SessionStoreKey string
	// ConnectorOnly removes inherited native execution surfaces and writes an
	// exact read-only/no-network config for one-shot Connector execution.
	ConnectorOnly bool
	// Sandboxed marks a Codex launched inside the daemon's macOS sandbox.
	// Codex's own sandbox modes (read-only, workspace-write) are Seatbelts
	// that cannot start inside it, so the config says danger-full-access.
	Sandboxed bool
}

// prepareCodexHomeWithOpts creates a per-run CODEX_HOME directory and seeds it
// with config from the shared ~/.codex/ home. Auth is symlinked (shared),
// config files are copied (isolated). The per-run config.toml gets
// daemon-managed sandbox, multi-agent, and memory blocks.
func prepareCodexHomeWithOpts(codexHome string, opts CodexHomeOptions, logger *slog.Logger) error {
	sharedHome := SharedCodexHome()
	freshHome := false
	if _, err := os.Lstat(codexHome); os.IsNotExist(err) {
		freshHome = true
	}

	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		return fmt.Errorf("create codex-home dir: %w", err)
	}

	// Give the run its own local sessions/ directory instead of symlinking the
	// shared ~/.codex/sessions in — a huge shared history would otherwise
	// stall Codex's `initialize` state backfill.
	if err := prepareCodexSessionsDir(codexHome, sharedHome, opts, logger); err != nil {
		logger.Warn("execenv: codex-home sessions dir prepare failed", "error", err)
	}

	for _, name := range codexSymlinkedFiles {
		src := filepath.Join(sharedHome, name)
		dst := filepath.Join(codexHome, name)
		if err := ensureSymlink(src, dst); err != nil {
			logger.Warn("execenv: codex-home symlink failed", "file", name, "error", err)
		}
	}

	logCodexAuthState(filepath.Join(codexHome, "auth.json"), logger)

	for _, name := range codexCopiedFiles {
		if opts.ConnectorOnly && (name == "config.json" || name == "instructions.md") {
			if err := os.Remove(filepath.Join(codexHome, name)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove inherited codex %s: %w", name, err)
			}
			continue
		}
		src := filepath.Join(sharedHome, name)
		dst := filepath.Join(codexHome, name)
		if err := syncCopiedFile(src, dst); err != nil {
			logger.Warn("execenv: codex-home sync failed", "file", name, "error", err)
		}
	}
	// Drop `[[skills.config]]` entries inherited from the user's
	// ~/.codex/config.toml — plugin-backed entries without a `path` make the
	// CLI's strict TOML parser refuse thread/start. See codex_skill_strip.go.
	if err := sanitizeCopiedCodexConfig(filepath.Join(codexHome, "config.toml")); err != nil {
		logger.Warn("execenv: codex-home sanitize config failed", "error", err)
	}
	if opts.ConnectorOnly {
		if err := ensureCodexConnectorOnlyConfig(
			filepath.Join(codexHome, "config.toml"),
			opts.Sandboxed,
		); err != nil {
			return fmt.Errorf("ensure codex Connector-only config: %w", err)
		}
	}

	if err := syncCodexReferencedFiles(codexHome, sharedHome); err != nil {
		return fmt.Errorf("sync codex config file references: %w", err)
	}

	// Seed the shared model cache only for a fresh run home; on reuse, keep a
	// run-local cache that Codex may have refreshed — but only while the
	// source provider/catalog configuration is still the one that cache was
	// bound to. If binding fails, discard the optional cache so Codex
	// refreshes it instead of possibly using models from the wrong provider.
	if err := syncCodexModelsCache(codexHome, sharedHome, freshHome); err != nil {
		logger.Warn("execenv: codex-home models cache sync failed; discarding cache", "error", err)
		if removeErr := os.RemoveAll(filepath.Join(codexHome, codexModelsCacheFile)); removeErr != nil {
			return fmt.Errorf("sync codex models cache: %w; discard unsafe cache: %w", err, removeErr)
		}
	}

	// The managed sandbox block is the authoritative on-disk policy. If it
	// cannot be written, config.toml keeps whatever it had — on a reused home
	// possibly a stale policy — so abort rather than launch Codex with an
	// unenforced sandbox.
	if opts.ConnectorOnly {
		for _, name := range []string{"memories", "plugins", "skills"} {
			if err := os.RemoveAll(filepath.Join(codexHome, name)); err != nil {
				return fmt.Errorf("clear codex Connector-only %s: %w", name, err)
			}
		}
	} else {
		configFile := filepath.Join(codexHome, "config.toml")
		policy := codexSandboxPolicyFor(opts.GOOS, opts.CodexVersion)
		if opts.Sandboxed {
			policy = codexSandboxPolicy{
				Mode:         "danger-full-access",
				Reason:       "the daemon's macOS sandbox holds the Run; Codex cannot start its own inside it",
				OuterSandbox: true,
			}
		}
		if err := ensureCodexSandboxConfig(configFile, policy, opts.CodexVersion, logger); err != nil {
			return fmt.Errorf("ensure codex sandbox config: %w", err)
		}

		// Disable Codex native multi-agent inside daemon-managed run sessions so
		// the parent thread's `turn/completed` is not read as run completion while
		// spawned subagents still execute. Warn-only. See codex_multi_agent.go.
		if err := ensureCodexMultiAgentConfig(configFile, logger); err != nil {
			logger.Warn("execenv: codex-home ensure multi-agent config failed", "error", err)
		}

		// Disable Codex native auto-memory inside daemon-managed run sessions so
		// cross-run and cross-company context cannot leak via memories/.
		// Warn-only. See codex_memory.go.
		if err := ensureCodexMemoryConfig(configFile, logger); err != nil {
			logger.Warn("execenv: codex-home ensure memory config failed", "error", err)
		}
	}

	return nil
}

// SharedCodexHome returns the path to the user's shared Codex home.
// Checks $CODEX_HOME first, falls back to ~/.codex. It is the daemon's own
// home, never a run's private one: run seeding copies from it, and CLI
// discovery looks for Codex's own installs under it.
func SharedCodexHome() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		abs, err := filepath.Abs(v)
		if err == nil {
			return abs
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".codex")
	}
	return filepath.Join(home, ".codex")
}

// codexSessionStateGlobs are the session-derived SQLite indexes Codex builds
// by scanning sessions/. Dropped during the legacy-symlink migration so Codex
// rebuilds them from the now run-local sessions. Everything matched here is a
// rebuildable derived index — never authoritative data.
var codexSessionStateGlobs = []string{
	"state_*.sqlite",
	"state_*.sqlite-shm",
	"state_*.sqlite-wal",
}

// codexSessionStoreRoot is the directory under the shared Codex home that
// holds the per-conversation session stores. It sits beside the user's own
// sessions/ so it shares that volume (rollout hard links stay zero-copy) but
// is never enumerated by a plain `codex` run.
const codexSessionStoreRoot = "zenith-team-sessions"

// codexSessionStoreDir returns the persistent per-(agent, conversation)
// session store for key. Empty key → "" (sessions stay run-local).
func codexSessionStoreDir(sharedHome, key string) string {
	if key == "" {
		return ""
	}
	return filepath.Join(sharedHome, codexSessionStoreRoot, key)
}

// CodexSessionStoreKey builds the stable session-store key for a run:
// <agent>/<conversation>. Returns "" when either identifier is unusable.
func CodexSessionStoreKey(agentID, conversationID string) string {
	agent := sanitizePathSegment(agentID)
	conversation := sanitizePathSegment(conversationID)
	if agent == "" || conversation == "" {
		return ""
	}
	return filepath.Join(agent, conversation)
}

// sanitizePathSegment reduces s to the characters a UUID uses (hex plus
// dashes/underscores), so the result is always a single safe path segment.
func sanitizePathSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// prepareCodexSessionsDir points codex-home/sessions at a sessions store that
// holds ONLY this run's own history, never the machine's whole
// ~/.codex/sessions:
//
//   - local_directory run: sessions/ links to the per-conversation store on
//     the shared Codex volume.
//   - fresh managed run: an empty local dir.
//   - reused managed run whose sessions/ is a real dir: authoritative, left.
//   - reused home still holding a legacy symlink into the shared sessions:
//     migrate (via the store when resuming; else an empty local dir) and drop
//     the stale session-state indexes.
func prepareCodexSessionsDir(codexHome, sharedHome string, opts CodexHomeOptions, logger *slog.Logger) error {
	dst := filepath.Join(codexHome, "sessions")
	sharedSessions := filepath.Join(sharedHome, "sessions")
	storeDir := codexSessionStoreDir(sharedHome, opts.SessionStoreKey)

	if opts.IsLocalDirectory {
		if storeDir == "" {
			return os.MkdirAll(dst, 0o755)
		}
		return linkCodexSessionsToStore(dst, storeDir, sharedSessions, opts.ResumeSessionID, logger)
	}

	fi, err := os.Lstat(dst)
	switch {
	case os.IsNotExist(err):
		return os.MkdirAll(dst, 0o755)
	case err != nil:
		return fmt.Errorf("stat sessions dir %s: %w", dst, err)
	}

	if fi.Mode()&os.ModeSymlink == 0 {
		return os.MkdirAll(dst, 0o755)
	}

	if storeDir != "" {
		if target, rlErr := os.Readlink(dst); rlErr == nil && sameCodexPath(target, storeDir) {
			return linkCodexSessionsToStore(dst, storeDir, sharedSessions, opts.ResumeSessionID, logger)
		}
	}

	if err := os.Remove(dst); err != nil {
		return fmt.Errorf("remove legacy sessions symlink %s: %w", dst, err)
	}
	resetCodexSessionState(codexHome, logger)

	if opts.ResumeSessionID != "" && storeDir != "" {
		logger.Info("execenv: migrated codex-home sessions from shared symlink to per-conversation store",
			"codex_home", codexHome, "resume_session", true)
		return linkCodexSessionsToStore(dst, storeDir, sharedSessions, opts.ResumeSessionID, logger)
	}
	logger.Info("execenv: migrated codex-home sessions from shared symlink to run-local dir",
		"codex_home", codexHome, "resume_session", false)
	return os.MkdirAll(dst, 0o755)
}

// linkCodexSessionsToStore points codex-home/sessions (dst) at the
// per-conversation store (storeDir) via an idempotent symlink. When resuming
// and the store does not yet hold the rollout, it hard-links that rollout out
// of the shared history — zero-copy, never on initialize's critical path.
func linkCodexSessionsToStore(dst, storeDir, sharedSessions, resumeID string, logger *slog.Logger) error {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return fmt.Errorf("create codex session store %s: %w", storeDir, err)
	}
	if resumeID != "" && len(findCodexRollouts(storeDir, resumeID)) == 0 {
		if err := exposeResumeRollout(sharedSessions, storeDir, resumeID, logger); err != nil {
			logger.Warn(
				"execenv: bootstrap resume rollout into session store failed; run will fall back to a fresh thread",
				"session_id",
				resumeID,
				"error",
				err,
			)
		}
	}
	if err := ensureCodexSessionsLink(dst, storeDir); err != nil {
		return err
	}
	// Stamp the store as just-used so an idle-store prune sampling mtime never
	// reclaims it before the resumed turn writes its first rollout.
	touchCodexSessionStore(storeDir, logger)
	return nil
}

// touchCodexSessionStore refreshes storeDir's modification time. Best-effort.
func touchCodexSessionStore(storeDir string, logger *slog.Logger) {
	now := time.Now()
	if err := os.Chtimes(storeDir, now, now); err != nil {
		logger.Warn("execenv: refresh codex session store activity failed", "store", storeDir, "error", err)
	}
}

// sameCodexPath reports whether two paths refer to the same location,
// tolerating separator/cleanliness differences.
func sameCodexPath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// resetCodexSessionState removes the rebuildable session-derived Codex state
// from a per-run CODEX_HOME so the next `initialize` re-derives it.
func resetCodexSessionState(codexHome string, logger *slog.Logger) {
	for _, pattern := range codexSessionStateGlobs {
		matches, err := filepath.Glob(filepath.Join(codexHome, pattern))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
				logger.Warn("execenv: codex-home reset session state failed", "path", m, "error", err)
			}
		}
	}
}

// ensureCodexSessionsLink points dst at src via a symlink, creating src if
// needed. Idempotent: a link already pointing at src is left as-is; anything
// else at dst is replaced.
func ensureCodexSessionsLink(dst, src string) error {
	if err := os.MkdirAll(src, 0o755); err != nil {
		return fmt.Errorf("create codex session store %s: %w", src, err)
	}
	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, rlErr := os.Readlink(dst); rlErr == nil && sameCodexPath(target, src) {
				return nil
			}
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("remove stale sessions path %s: %w", dst, err)
		}
	}
	return createDirLink(src, dst)
}

// codexRolloutGlobs returns the glob patterns matching a session's rollout
// under a Codex sessions directory: date-nested (sessions/YYYY/MM/DD/) and
// flat, each as .jsonl or background-compressed .jsonl.zst.
func codexRolloutGlobs(sessionsDir, sessionID string) []string {
	name := "rollout-*-" + sessionID + ".jsonl*"
	return []string{
		filepath.Join(sessionsDir, name),
		filepath.Join(sessionsDir, "*", "*", "*", name),
	}
}

// findCodexRollouts returns every rollout file for sessionID under
// sessionsDir, across the supported layouts.
func findCodexRollouts(sessionsDir, sessionID string) []string {
	if sessionsDir == "" || sessionID == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, pattern := range codexRolloutGlobs(sessionsDir, sessionID) {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// CodexResumeRolloutPresent reports whether sessionID's rollout is present in
// the run's codex-home sessions dir. The daemon uses this to avoid claiming a
// resume Codex would silently restart from scratch.
func CodexResumeRolloutPresent(codexHome, sessionID string) bool {
	if codexHome == "" || sessionID == "" {
		return false
	}
	return len(findCodexRollouts(filepath.Join(codexHome, "sessions"), sessionID)) > 0
}

// exposeResumeRollout links sessionID's rollout(s) out of the shared sessions
// history into the run-local sessions dir, preserving the relative layout so
// thread/resume can find it. Links, never copies — a rollout can be gigabytes.
func exposeResumeRollout(sharedSessions, localSessions, sessionID string, logger *slog.Logger) error {
	matches := findCodexRollouts(sharedSessions, sessionID)
	if len(matches) == 0 {
		return fmt.Errorf("no rollout found for session %s under %s", sessionID, sharedSessions)
	}
	linked := 0
	for _, src := range matches {
		rel, err := filepath.Rel(sharedSessions, src)
		if err != nil {
			rel = filepath.Base(src)
		}
		dst := filepath.Join(localSessions, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create rollout dir %s: %w", filepath.Dir(dst), err)
		}
		if err := linkCodexRollout(src, dst); err != nil {
			return fmt.Errorf("link rollout %s: %w", src, err)
		}
		linked++
	}
	logger.Info("execenv: exposed resume rollout into run-local sessions", "session_id", sessionID, "files", linked)
	return nil
}

// linkCodexRollout materializes src at dst without copying its bytes: a hard
// link first, falling back to a symlink across filesystems.
func linkCodexRollout(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return os.Symlink(src, dst)
}

// syncCodexReferencedFiles materializes every file the copied config.toml
// points at inside the per-run CODEX_HOME. Only the keys below are followed —
// following arbitrary paths would let any user config pull host files into a
// run sandbox.
func syncCodexReferencedFiles(codexHome, sharedHome string) error {
	configPath := filepath.Join(codexHome, "config.toml")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}

	var cfg struct {
		ModelCatalogJSON             string `toml:"model_catalog_json"`
		ModelInstructionsFile        string `toml:"model_instructions_file"`
		ExperimentalInstructionsFile string `toml:"experimental_instructions_file"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", configPath, err)
	}

	for _, ref := range []struct{ key, path string }{
		{"model_catalog_json", cfg.ModelCatalogJSON},
		{"model_instructions_file", cfg.ModelInstructionsFile},
		{"experimental_instructions_file", cfg.ExperimentalInstructionsFile},
	} {
		if err := syncCodexReferencedFile(codexHome, sharedHome, ref.key, ref.path); err != nil {
			return err
		}
	}
	return nil
}

// syncCodexReferencedFile copies one config-referenced file from the shared
// Codex home into the run home at the same relative location. Absolute and
// ~-rooted values are left alone. Destination writes are root-scoped to
// codexHome so a reused home cannot redirect them outside it.
func syncCodexReferencedFile(codexHome, sharedHome, key, configValue string) error {
	referencedPath := strings.TrimSpace(configValue)
	if referencedPath == "" {
		return nil
	}

	src, err := resolveCodexConfigPath(referencedPath, sharedHome, key)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if os.IsNotExist(err) {
		return fmt.Errorf("%s %q resolved to missing file %s: %w", key, referencedPath, src, err)
	}
	if err != nil {
		return fmt.Errorf("%s %q: cannot stat source %s: %w", key, referencedPath, src, err)
	}

	if filepath.IsAbs(referencedPath) || strings.HasPrefix(referencedPath, "~") {
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %q resolved to %s, which is not a regular file", key, referencedPath, src)
	}
	cleanReferencedPath := filepath.Clean(referencedPath)
	if !filepath.IsLocal(cleanReferencedPath) {
		return fmt.Errorf("%s %q must be a local relative path or an absolute path", key, referencedPath)
	}
	return materialiseInCodexHome(codexHome, cleanReferencedPath, src, key)
}

// openVerifiedCodexHomeRoot opens codexHome as a confinement root and only
// returns it once the opened directory is proven to still BE codexHome, so a
// directory swapped for a link between check and open cannot redirect the
// root-scoped writes below.
func openVerifiedCodexHomeRoot(codexHome, key string) (*os.Root, error) {
	root, err := os.OpenRoot(codexHome)
	if err != nil {
		return nil, fmt.Errorf("open codex home %s: %w", codexHome, err)
	}
	if err := verifyCodexHomeRoot(root, codexHome, key); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

// verifyCodexHomeRoot proves that root is the directory codexHome names right
// now: not reached through a symlink, and the same directory os.Lstat sees.
func verifyCodexHomeRoot(root *os.Root, codexHome, key string) error {
	opened, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("stat opened codex home %s: %w", codexHome, err)
	}
	current, err := os.Lstat(codexHome)
	if err != nil {
		return fmt.Errorf("stat codex home %s: %w", codexHome, err)
	}
	if current.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("codex home %s is a symlink; refusing to write %s through it", codexHome, key)
	}
	if !os.SameFile(opened, current) {
		return fmt.Errorf(
			"codex home %s was replaced while opening it; refusing to write %s through it",
			codexHome,
			key,
		)
	}
	return nil
}

// materialiseInCodexHome writes src to relPath inside codexHome using
// root-scoped operations, so no symlink below the run home can redirect the
// daemon's mkdir, remove, or write outside it.
func materialiseInCodexHome(codexHome, relPath, src, key string) error {
	root, err := openVerifiedCodexHomeRoot(codexHome, key)
	if err != nil {
		return err
	}
	defer root.Close()

	if dir := filepath.Dir(relPath); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s directory %s: %w", key, dir, err)
		}
	}
	if _, err := root.Lstat(relPath); err == nil {
		if err := root.Remove(relPath); err != nil {
			return fmt.Errorf("remove stale %s copy %s: %w", key, relPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s copy %s: %w", key, relPath, err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s %s: %w", key, src, err)
	}
	defer in.Close()

	out, err := root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create %s copy %s: %w", key, relPath, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s %s to %s: %w", key, src, relPath, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s copy %s: %w", key, relPath, err)
	}
	return nil
}

// syncCodexModelsCache seeds models_cache.json once for a fresh run home and
// binds it to the shared provider/catalog configuration. A changed or missing
// binding makes an existing cache unsafe (it does not record the provider
// identity), so it is dropped and Codex refetches.
func syncCodexModelsCache(codexHome, sharedHome string, freshHome bool) error {
	fingerprint, err := codexModelsCacheConfigFingerprint(sharedHome)
	if err != nil {
		return err
	}

	bindingPath := filepath.Join(codexHome, ".models_cache_config.sha256")
	previous, bound, err := readCodexModelsCacheBinding(bindingPath)
	if err != nil {
		return err
	}

	cachePath := filepath.Join(codexHome, codexModelsCacheFile)
	cacheInfo, cacheErr := os.Lstat(cachePath)
	cacheExists := cacheErr == nil
	if cacheErr != nil && !os.IsNotExist(cacheErr) {
		return fmt.Errorf("stat codex models cache %s: %w", cachePath, cacheErr)
	}

	if bound && previous == fingerprint {
		if cacheExists && !cacheInfo.Mode().IsRegular() {
			if err := os.RemoveAll(cachePath); err != nil {
				return fmt.Errorf("remove non-regular codex models cache %s: %w", cachePath, err)
			}
		}
		return nil
	}

	if cacheExists {
		if err := os.RemoveAll(cachePath); err != nil {
			return fmt.Errorf("remove unbound codex models cache %s: %w", cachePath, err)
		}
	}

	if freshHome && !bound && !cacheExists {
		if err := seedCopiedFile(filepath.Join(sharedHome, codexModelsCacheFile), cachePath); err != nil {
			return fmt.Errorf("seed codex models cache: %w", err)
		}
	}

	return writeCodexModelsCacheBinding(bindingPath, fingerprint)
}

// codexModelsCacheConfigFingerprint hashes the shared config files plus the
// contents of any model_catalog_json they reference.
func codexModelsCacheConfigFingerprint(sharedHome string) (string, error) {
	h := sha256.New()
	var configTOML []byte

	for _, name := range codexModelsCacheConfigFiles {
		path := filepath.Join(sharedHome, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(h, "%s\x00missing\x00", name)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read codex model cache config %s: %w", path, err)
		}
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(data))
		_, _ = h.Write(data)
		if name == "config.toml" {
			configTOML = data
		}
	}

	if len(configTOML) > 0 {
		var cfg struct {
			ModelCatalogJSON string `toml:"model_catalog_json"`
		}
		if err := toml.Unmarshal(configTOML, &cfg); err != nil {
			return "", fmt.Errorf(
				"parse codex model cache config %s: %w",
				filepath.Join(sharedHome, "config.toml"),
				err,
			)
		}
		catalogPath := strings.TrimSpace(cfg.ModelCatalogJSON)
		if catalogPath != "" {
			resolved, err := resolveCodexConfigPath(catalogPath, sharedHome, "model_catalog_json")
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", fmt.Errorf("read model_catalog_json %s: %w", resolved, err)
			}
			fmt.Fprintf(h, "model_catalog_json\x00%d\x00", len(data))
			_, _ = h.Write(data)
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// readCodexModelsCacheBinding returns bound=false for a missing or
// non-regular marker; non-regular paths are removed defensively.
func readCodexModelsCacheBinding(path string) (fingerprint string, bound bool, err error) {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("stat codex models cache binding %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		if err := os.RemoveAll(path); err != nil {
			return "", false, fmt.Errorf("remove non-regular codex models cache binding %s: %w", path, err)
		}
		return "", false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read codex models cache binding %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), true, nil
}

func writeCodexModelsCacheBinding(path, fingerprint string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove prior codex models cache binding %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create codex models cache binding %s: %w", path, err)
	}
	if _, err := io.WriteString(f, fingerprint+"\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("write codex models cache binding %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close codex models cache binding %s: %w", path, err)
	}
	return nil
}

// resolveCodexConfigPath maps a path-valued config.toml entry to its source
// file on this host. A relative path resolves against the shared Codex home.
func resolveCodexConfigPath(configPath, sharedHome, key string) (string, error) {
	if filepath.IsAbs(configPath) {
		return filepath.Clean(configPath), nil
	}
	if strings.HasPrefix(configPath, "~/") || strings.HasPrefix(configPath, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve %s %q: user home: %w", key, configPath, err)
		}
		return filepath.Join(home, configPath[2:]), nil
	}
	if strings.HasPrefix(configPath, "~") {
		return "", fmt.Errorf("%s %q uses unsupported ~user expansion", key, configPath)
	}
	return filepath.Join(sharedHome, filepath.Clean(configPath)), nil
}

// ensureSymlink ensures dst tracks src. If src doesn't exist, no-op. If dst is
// already a symlink at src, no-op. Otherwise dst is removed and recreated so
// the per-run home doesn't drift from the shared source.
func ensureSymlink(src, dst string) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	}

	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(dst); err == nil && target == src {
				return nil
			}
		}
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove stale dst %s: %w", dst, err)
		}
	}

	return createFileLink(src, dst)
}

// logCodexAuthState records the kind of auth.json the per-run CODEX_HOME
// ended up with — never its contents — so token-refresh failures are
// diagnosable from daemon logs.
func logCodexAuthState(authPath string, logger *slog.Logger) {
	fi, err := os.Lstat(authPath)
	if err != nil {
		logger.Info("execenv: codex auth.json absent", "path", authPath, "error", err)
		return
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(authPath)
		logger.Info("execenv: codex auth.json is symlink", "path", authPath, "target", target)
		return
	}
	logger.Info("execenv: codex auth.json is regular file",
		"path", authPath,
		"size", fi.Size(),
		"mtime", fi.ModTime().UTC(),
	)
}

// syncCopiedFile mirrors a per-run dst onto the current state of the shared
// src across reuses: src present → refresh copy; src absent → drop dst.
func syncCopiedFile(src, dst string) error {
	_, srcErr := os.Stat(src)
	srcMissing := os.IsNotExist(srcErr)
	if srcErr != nil && !srcMissing {
		return fmt.Errorf("stat src %s: %w", src, srcErr)
	}

	if _, err := os.Lstat(dst); err == nil {
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove stale dst %s: %w", dst, err)
		}
	}

	if srcMissing {
		return nil
	}
	return copyFile(src, dst)
}

// seedCopiedFile copies src only when dst has no run-local regular file.
// Non-regular destinations are removed defensively.
func seedCopiedFile(src, dst string) error {
	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode().IsRegular() {
			return nil
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("remove non-regular dst %s: %w", dst, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat dst %s: %w", dst, err)
	}

	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat src %s: %w", src, err)
	}
	return copyFile(src, dst)
}

// copyFile copies src to dst unconditionally.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	return nil
}
