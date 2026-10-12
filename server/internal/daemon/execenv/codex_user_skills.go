package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// seedUserCodexSkills links user-installed skill directories from the shared
// ~/.codex/skills/ into the per-run CODEX_HOME so the codex CLI discovers
// them natively. Codex is the only runtime whose HOME is redirected to a
// per-run directory, so without this step the CLI never sees the user's
// skills.
//
// Links, never copies: a copy charges the whole skill tree to every run
// directory and hydrateCodexSkills re-does it on every run start, on the
// critical path. A skill directory is read-only input to the CLI.
//
// Run-assigned skills take precedence on name conflict: any user skill whose
// sanitized name matches a run skill's sanitized name is skipped here, and
// writeSkillFiles then writes the run version into a clean slot.
//
// Per-skill failures are logged and skipped — a single broken user skill
// must not prevent the run. Returning an error is reserved for failures that
// affect every skill.
func seedUserCodexSkills(codexHome string, runSkills []Skill, logger *slog.Logger) error {
	sharedSkillsDir := filepath.Join(SharedCodexHome(), "skills")

	info, err := os.Stat(sharedSkillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat shared skills dir: %w", err)
	}
	if !info.IsDir() {
		return nil
	}

	reserved := make(map[string]struct{}, len(runSkills))
	for _, s := range runSkills {
		reserved[sanitizeSkillName(s.Name)] = struct{}{}
	}

	entries, err := os.ReadDir(sharedSkillsDir)
	if err != nil {
		return fmt.Errorf("read shared skills dir: %w", err)
	}

	targetSkillsDir := filepath.Join(codexHome, "skills")
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if _, claimed := reserved[sanitizeSkillName(name)]; claimed {
			logger.Info("execenv: codex user-skill yields to run skill", "name", name)
			continue
		}
		src := filepath.Join(sharedSkillsDir, name)
		// Installers ship skills as symlinks into a shared directory. Resolve
		// so the per-run link points at the real directory instead of at
		// another link, and so the IsDir check sees the actual target.
		resolved, err := filepath.EvalSymlinks(src)
		if err != nil {
			logger.Warn("execenv: codex user-skill resolve failed", "name", name, "error", err)
			continue
		}
		fi, err := os.Stat(resolved)
		if err != nil || !fi.IsDir() {
			continue
		}
		// hydrateCodexSkills wipes the skills dir before every seed; created
		// lazily so a run with no eligible user skills gets no directory.
		if err := os.MkdirAll(targetSkillsDir, 0o755); err != nil {
			return fmt.Errorf("create codex skills dir %s: %w", targetSkillsDir, err)
		}
		dst := filepath.Join(targetSkillsDir, name)
		// Removes the link, never the link target.
		if err := os.RemoveAll(dst); err != nil {
			logger.Warn("execenv: codex user-skill clean dst failed", "name", name, "error", err)
			continue
		}
		if err := createDirLink(resolved, dst); err != nil {
			logger.Warn("execenv: codex user-skill link failed", "name", name, "error", err)
			continue
		}
	}
	return nil
}
