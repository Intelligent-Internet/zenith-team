// Package execenv is the provider pass of run preparation: given a workdir
// prepared by zenith-team's own workspace design, it sets up the per-run Codex
// home, the run-local Claude settings file, and materializes the run's skills
// into provider-native locations.
package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Skill is one skill bundle to materialize into the execution environment.
type Skill struct {
	Name        string
	Description string
	Content     string
	Files       []SkillFile
}

// SkillFile is a supporting file within a skill.
type SkillFile struct {
	Path    string
	Content string
	Data    []byte
}

// Options configures Prepare's provider pass for one run.
type Options struct {
	// EnvRoot is the run's lifecycle root (workspace.Acquired.Root for managed
	// runs, a daemon scratch dir for local_directory runs). The Codex home and
	// the Claude settings file live here, outside the agent's cwd.
	EnvRoot string
	// WorkDir is the agent cwd. Provider skills stay outside this directory.
	WorkDir  string
	Provider string
	// CodexVersion is the detected Codex CLI version; empty means unknown
	// (macOS then falls back to danger-full-access, see codex_sandbox.go).
	CodexVersion string
	// ResumeSessionID is the prior Codex session this run intends to resume.
	ResumeSessionID string
	// LocalDirectory routes Codex sessions through the per-conversation store.
	LocalDirectory bool
	// SessionStoreKey is the stable per-(agent, conversation) key for the
	// persistent Codex session store; empty keeps sessions run-local.
	SessionStoreKey string
	// ConnectorOnly prepares a fail-closed one-shot environment with no native
	// provider tools, memory, skills, hooks, plugins, or inherited MCP/apps.
	// The provider adapter supplies only the exact frozen Connector scope.
	ConnectorOnly bool
	// ReadOnlyDirs lists absolute directories outside the workspace this run
	// may read but must never modify — today the Company main .memory and
	// business context every Task Run inherits. Codex needs no per-run config
	// for them:
	// workspace-write leaves reads unrestricted and already refuses writes
	// outside the workspace, and danger-full-access has no enforcement seam.
	// Claude runs under bypassPermissions, where settings deny rules still
	// bind, so the run-local settings file grants the directories and denies
	// edits inside them.
	ReadOnlyDirs []string
	// ProtectedCredentialPaths lists absolute paths that can hold the
	// person's Zenith Team sign-in: the daemon's real ZENITH_TEAM_HOME,
	// ~/.zenith-team and the desktop session store. Claude's own file tools
	// (Read, Edit, Write) are denied every one that exists. The rules do not
	// bind Claude's shell or Codex; only the macOS sandbox does that.
	// Connector-only Runs have no file tools, so their settings ignore it.
	ProtectedCredentialPaths []string
	// Sandboxed marks a Run whose provider starts inside the daemon's macOS
	// sandbox (Seatbelt). A Seatbelt cannot start inside another, so the
	// provider is told not to start its own: Codex runs danger-full-access
	// and Claude's run settings turn its sandbox off. The daemon's profile
	// is the wall instead.
	Sandboxed bool
	Skills    []Skill
	// DisabledRuntimeSkills lists runtime-local skills to hide from the
	// provider process (Claude settings deny rules, Codex skills.config).
	DisabledRuntimeSkills []RuntimeSkillRef
	DisabledPlugins       []string
}

// Environment is the provider-pass result.
type Environment struct {
	// CodexHome is the per-run CODEX_HOME (codex provider only).
	CodexHome string
	// ClaudeSettingsPath is a run-local --settings JSON file (claude provider
	// only, and only when disabled-skill, read-only-directory or credential
	// path policy applies).
	ClaudeSettingsPath string
	// ClaudeSkillsDir is where claude skills were materialized ("" when none).
	ClaudeSkillsDir string
	// ClaudeSkillsRoot is the Run-owned folder under the env root that holds
	// .claude/skills, passed to Claude with --add-dir ("" when none).
	ClaudeSkillsRoot string
	// SandboxWritableRoots, set only for a sandboxed connector-only Codex
	// Run, are the trees the daemon's sandbox lets it write: the Run's env
	// root (its scratch and per-run Codex home) and the shared Codex home,
	// where Codex renews its own sign-in. They stand in for Codex's
	// read-only mode, which cannot start inside the daemon's sandbox.
	SandboxWritableRoots []string
}

// Prepare runs the provider pass over an already-prepared workdir. It is
// idempotent: re-running against a reused environment refreshes every
// daemon-managed artifact in place.
func Prepare(opts Options, logger *slog.Logger) (*Environment, error) {
	if opts.EnvRoot == "" {
		return nil, fmt.Errorf("execenv: env root is required")
	}
	if opts.WorkDir == "" {
		return nil, fmt.Errorf("execenv: workdir is required")
	}
	if opts.ConnectorOnly {
		switch {
		case opts.ResumeSessionID != "":
			return nil, fmt.Errorf("execenv: connector-only execution cannot resume a provider session")
		case opts.SessionStoreKey != "":
			return nil, fmt.Errorf("execenv: connector-only execution cannot use a session store")
		case opts.LocalDirectory:
			return nil, fmt.Errorf("execenv: connector-only execution cannot use a local directory")
		case len(opts.ReadOnlyDirs) != 0:
			return nil, fmt.Errorf("execenv: connector-only execution cannot mount read-only directories")
		}
	}

	env := &Environment{}
	prepared := false
	defer func() {
		if !prepared && env.ClaudeSkillsRoot != "" {
			_ = os.RemoveAll(env.ClaudeSkillsRoot)
		}
	}()
	switch opts.Provider {
	case "codex":
		codexHome := filepath.Join(opts.EnvRoot, "codex-home")
		if err := prepareCodexHomeWithOpts(codexHome, CodexHomeOptions{
			CodexVersion:     opts.CodexVersion,
			ResumeSessionID:  opts.ResumeSessionID,
			IsLocalDirectory: opts.LocalDirectory,
			SessionStoreKey:  opts.SessionStoreKey,
			ConnectorOnly:    opts.ConnectorOnly,
			Sandboxed:        opts.Sandboxed,
		}, logger); err != nil {
			return nil, fmt.Errorf("execenv: prepare codex-home: %w", err)
		}
		if opts.ConnectorOnly {
			if err := os.RemoveAll(filepath.Join(codexHome, "skills")); err != nil {
				return nil, fmt.Errorf("execenv: clear codex skills: %w", err)
			}
		} else if err := hydrateCodexSkills(
			codexHome,
			opts.Skills,
			opts.DisabledRuntimeSkills,
			logger,
		); err != nil {
			return nil, fmt.Errorf("execenv: hydrate codex skills: %w", err)
		}
		env.CodexHome = codexHome
		if opts.Sandboxed && opts.ConnectorOnly {
			env.SandboxWritableRoots = []string{opts.EnvRoot, SharedCodexHome()}
		}
	case "claude":
		if len(opts.Skills) > 0 && !opts.ConnectorOnly {
			root, err := os.MkdirTemp(opts.EnvRoot, ".claude-skills-")
			if err != nil {
				return nil, fmt.Errorf("create Claude skill root: %w", err)
			}
			env.ClaudeSkillsRoot = root
			env.ClaudeSkillsDir = filepath.Join(root, ".claude", "skills")
			if err := writeSkillFiles(env.ClaudeSkillsDir, opts.Skills); err != nil {
				_ = os.RemoveAll(root)
				return nil, fmt.Errorf("write Claude skills: %w", err)
			}
			opts.ReadOnlyDirs = append(append([]string(nil), opts.ReadOnlyDirs...), root)
		}
		var settingsPath string
		var err error
		if opts.ConnectorOnly {
			settingsPath, err = prepareClaudeConnectorOnlySettings(opts.EnvRoot)
		} else {
			settingsPath, err = prepareClaudeSkillSettings(
				opts.EnvRoot,
				opts.DisabledRuntimeSkills,
				opts.Skills,
				opts.ReadOnlyDirs,
				credentialDenyRules(
					opts.ProtectedCredentialPaths,
					append([]string{opts.EnvRoot, opts.WorkDir}, opts.ReadOnlyDirs...),
					logger,
				),
				opts.Sandboxed,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("execenv: prepare claude skill settings: %w", err)
		}
		if !opts.ConnectorOnly {
			settingsPath, err = prepareClaudePluginSettings(
				opts.EnvRoot,
				settingsPath,
				opts.DisabledPlugins,
			)
			if err != nil {
				return nil, fmt.Errorf("execenv: prepare Claude plugin settings: %w", err)
			}
		}
		env.ClaudeSettingsPath = settingsPath

	}
	prepared = true
	return env, nil
}

// hydrateCodexSkills populates the per-run CODEX_HOME/skills directory with
// user-installed skills (linked from the shared ~/.codex/skills/) and the
// run's own skills. Run skills win on name conflict — seedUserCodexSkills
// pre-filters their names, and they are written last. The directory is wiped
// first so a reused home cannot leak a stale skill.
func hydrateCodexSkills(codexHome string, skills []Skill, disabled []RuntimeSkillRef, logger *slog.Logger) error {
	skillsDir := filepath.Join(codexHome, "skills")
	if err := os.RemoveAll(skillsDir); err != nil {
		return fmt.Errorf("clear codex skills dir: %w", err)
	}
	if err := seedUserCodexSkills(codexHome, skills, logger); err != nil {
		logger.Warn("execenv: seed user codex skills failed", "error", err)
	}
	if len(skills) > 0 {
		if err := writeSkillFiles(skillsDir, skills); err != nil {
			return err
		}
	}
	return ensureCodexDisabledSkillsConfig(filepath.Join(codexHome, "config.toml"), codexHome, disabled, skills)
}

// writeSkillFiles materializes skills as <dir>/<slug>/SKILL.md plus supporting
// files. Slugs are deduplicated within the batch; a slot occupied by something
// this call did not create (a user-installed skill or link) yields to a
// "-zenith-team" suffixed sibling instead of overwriting user bytes.
func writeSkillFiles(skillsDir string, skills []Skill) error {
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}
	taken := make(map[string]bool, len(skills))
	for _, skill := range skills {
		slug := sanitizeSkillName(skill.Name)
		if slug == "" {
			slug = "skill"
		}
		base := slug
		for n := 2; taken[slug]; n++ {
			slug = fmt.Sprintf("%s-%d", base, n)
		}
		taken[slug] = true

		dir := filepath.Join(skillsDir, slug)
		// A pre-existing entry (dir, file, or link) at the natural slug is not
		// ours: allocate a collision-free sibling rather than overwrite it.
		if _, err := os.Lstat(dir); err == nil {
			slug += "-zenith-team"
			for n := 2; taken[slug]; n++ {
				slug = fmt.Sprintf("%s-zenith-team-%d", base, n)
			}
			taken[slug] = true
			dir = filepath.Join(skillsDir, slug)
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("clear skill dir %s: %w", dir, err)
			}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create skill dir %s: %w", dir, err)
		}
		body := ensureSkillFrontmatter(skill.Content, slug, skill.Description)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write SKILL.md for %q: %w", skill.Name, err)
		}
		for _, f := range skill.Files {
			if !safeSkillRelPath(f.Path) || filepath.ToSlash(filepath.Clean(f.Path)) == "SKILL.md" {
				continue
			}
			fpath := filepath.Join(dir, filepath.FromSlash(f.Path))
			if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
				return fmt.Errorf("create skill file dir: %w", err)
			}
			data := f.Data
			if data == nil {
				data = []byte(f.Content)
			}
			if err := os.WriteFile(fpath, data, 0o644); err != nil {
				return fmt.Errorf("write skill file %s: %w", f.Path, err)
			}
		}
	}
	return nil
}

// safeSkillRelPath accepts only clean, local, slash-separated relative paths.
func safeSkillRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "\x00") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && filepath.IsLocal(clean)
}

// sanitizeSkillName reduces a display name to a filesystem-safe lowercase slug.
func sanitizeSkillName(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// ensureSkillFrontmatter guarantees SKILL.md carries YAML frontmatter with a
// name (and description when known) so providers that key on frontmatter see
// a well-formed skill even when the upstream content omitted it.
func ensureSkillFrontmatter(content, name, description string) string {
	trimmed := strings.TrimLeft(content, "\n")
	if strings.HasPrefix(trimmed, "---\n") || strings.HasPrefix(trimmed, "---\r\n") || trimmed == "---" {
		return content
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", name)
	if description != "" {
		fmt.Fprintf(&b, "description: %s\n", strings.ReplaceAll(description, "\n", " "))
	}
	b.WriteString("---\n\n")
	b.WriteString(content)
	return b.String()
}
