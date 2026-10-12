package execenv

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const claudeRuntimeSkillSettingsFile = "claude-runtime-skill-settings.json"

func prepareClaudeConnectorOnlySettings(envRoot string) (string, error) {
	path := filepath.Join(envRoot, claudeRuntimeSkillSettingsFile)
	payload := map[string]any{
		"disableAllHooks": true,
		"enabledPlugins":  map[string]bool{},
		"permissions": map[string]any{
			"ask": []string{"*"},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// RuntimeSkillRef identifies a runtime-local skill for provider-specific
// environment filtering. Provider and runtime are already selected by the
// run, so only the discovery root and provider-native key are needed here.
type RuntimeSkillRef struct {
	Root   string
	Key    string
	Name   string
	Plugin string
}

func cleanRuntimeSkillKey(key string) (string, bool) {
	cleaned := filepath.Clean(filepath.FromSlash(strings.TrimSpace(key)))
	if cleaned == "." || filepath.IsAbs(cleaned) || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(cleaned), true
}

// prepareClaudeSkillSettings produces the run-local --settings JSON that
// applies disabled runtime-skill policy, read-only directory mounts and the
// credential deny rules without mutating the user's Claude config. A Run in
// the daemon's sandbox also turns Claude's own sandbox off: it would start a
// Seatbelt for every shell command, and a Seatbelt cannot start inside the
// daemon's. With nothing to enforce the file is removed and "" returned.
func prepareClaudeSkillSettings(
	envRoot string,
	disabled []RuntimeSkillRef,
	runSkills []Skill,
	readOnlyDirs []string,
	credentialRules []string,
	sandboxed bool,
) (string, error) {
	path := filepath.Join(envRoot, claudeRuntimeSkillSettingsFile)
	overrides := make(map[string]string)
	capacity := len(disabled)*2 + len(readOnlyDirs)*2 + len(credentialRules)
	deny := make([]string, 0, capacity)
	seenDeny := make(map[string]struct{}, capacity)
	addDeny := func(rule string) {
		if _, exists := seenDeny[rule]; exists {
			return
		}
		seenDeny[rule] = struct{}{}
		deny = append(deny, rule)
	}
	for _, skill := range disabled {
		key, ok := cleanRuntimeSkillKey(skill.Key)
		if !ok {
			continue
		}
		invocationName := strings.TrimSpace(skill.Name)
		if invocationName == "" {
			invocationName = filepath.Base(filepath.FromSlash(key))
		}
		if runClaimsRuntimeSkill(invocationName, runSkills) {
			continue
		}
		// Claude Code's skillOverrides fully hides personal/project skills.
		// Plugin skills ignore that setting, so the permission deny below is
		// also emitted for every key and is the enforcement path for plugins.
		if skill.Root != "plugin" {
			overrides[invocationName] = "off"
		} else {
			invocationName = key
		}
		addDeny("Skill(" + invocationName + ")")
		addDeny("Skill(" + invocationName + " *)")
	}
	for _, dir := range readOnlyDirs {
		// Claude Code rule paths anchor to the filesystem root with a leading
		// "//", so an absolute directory becomes Edit(//abs/path/**). Deny
		// rules bind even under bypassPermissions; Bash writes are out of
		// their reach, so the system prompt narrates the read-only contract
		// as well.
		pattern := claudeRulePath(dir) + "/**"
		addDeny("Edit(" + pattern + ")")
		addDeny("Write(" + pattern + ")")
	}
	for _, rule := range credentialRules {
		addDeny(rule)
	}
	if len(overrides) == 0 && len(deny) == 0 && !sandboxed {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return "", nil
	}
	permissions := map[string]any{
		"deny": deny,
	}
	if len(readOnlyDirs) != 0 {
		// additionalDirectories keeps the mounts readable even if the run
		// ever drops bypassPermissions, and surfaces them in the CLI's
		// working-directory list.
		permissions["additionalDirectories"] = readOnlyDirs
	}
	payload := map[string]any{
		"skillOverrides": overrides,
		"permissions":    permissions,
	}
	if sandboxed {
		payload["sandbox"] = map[string]any{"enabled": false}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// claudeRulePath anchors an absolute path for a Claude Code permission rule:
// a rule path that starts with "//" is read from the filesystem root.
func claudeRulePath(path string) string {
	return posixRulePath(filepath.VolumeName(path), filepath.ToSlash(path))
}

// posixRulePath writes a slashed absolute path in the POSIX form Claude Code
// matches rules against. On Windows it reads C:\Users as /c/Users, so a
// drive letter becomes the first path segment, in lower case.
func posixRulePath(volume, slashed string) string {
	if len(volume) == 2 && volume[1] == ':' {
		slashed = "/" + strings.ToLower(volume[:1]) + slashed[2:]
	}
	return "//" + strings.TrimPrefix(slashed, "/")
}

// credentialDenyRules keeps Claude's own file tools out of every protected
// credential path that exists: Read, Edit and Write, on the whole tree for a
// directory and on the file itself otherwise. A path reached through a
// symlink is denied under both names. A path that holds one of the Run's own
// folders is skipped with a warning, because the rule would deny the Run its
// workspace.
func credentialDenyRules(protected, runDirs []string, logger *slog.Logger) []string {
	if len(protected) == 0 {
		return nil
	}
	runDirNames := make([]string, 0, 2*len(runDirs))
	for _, dir := range runDirs {
		if dir = strings.TrimSpace(dir); dir != "" {
			runDirNames = append(runDirNames, pathSpellings(dir)...)
		}
	}
	var rules []string
	for _, path := range protected {
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		names := pathSpellings(path)
		if runDir, inside := firstInside(names, runDirNames); inside {
			logger.Warn(
				"execenv: a credential path holds the run's folders; Claude file tools are not denied it",
				"path",
				path,
				"run_dir",
				runDir,
			)
			continue
		}
		for _, name := range names {
			pattern := claudeRulePath(name)
			if info.IsDir() {
				pattern += "/**"
			}
			rules = append(rules, "Read("+pattern+")", "Edit("+pattern+")", "Write("+pattern+")")
		}
	}
	return rules
}

// pathSpellings is path cleaned and, when it differs, path with its symlinks
// resolved. A folder that does not exist yet keeps only its first spelling.
func pathSpellings(path string) []string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		return []string{path, resolved}
	}
	return []string{path}
}

// firstInside returns the first of dirs that is one of roots or lies under
// one of them.
func firstInside(roots, dirs []string) (string, bool) {
	for _, root := range roots {
		for _, dir := range dirs {
			rel, err := filepath.Rel(root, dir)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return dir, true
			}
		}
	}
	return "", false
}

// ensureCodexDisabledSkillsConfig appends `[[skills.config]]` disable entries
// for runtime-local skills that must stay hidden from the codex CLI.
func ensureCodexDisabledSkillsConfig(
	configPath, codexHome string,
	disabled []RuntimeSkillRef,
	runSkills []Skill,
) error {
	if len(disabled) == 0 {
		return nil
	}
	home := ""
	paths := make([]string, 0, len(disabled))
	seen := make(map[string]struct{}, len(disabled))
	for _, skill := range disabled {
		key, ok := cleanRuntimeSkillKey(skill.Key)
		if !ok {
			continue
		}
		var skillPath string
		switch skill.Root {
		case "provider":
			firstKeyPart := strings.SplitN(key, "/", 2)[0]
			if runClaimsRuntimeSkill(firstKeyPart, runSkills) {
				continue
			}
			skillPath = filepath.Join(codexHome, "skills", filepath.FromSlash(key), "SKILL.md")
		case "universal":
			if home == "" {
				var err error
				home, err = os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("resolve user home for disabled Codex skills: %w", err)
				}
			}
			skillPath = filepath.Join(home, ".agents", "skills", filepath.FromSlash(key), "SKILL.md")
		default:
			continue
		}
		if _, exists := seen[skillPath]; exists {
			continue
		}
		seen[skillPath] = struct{}{}
		paths = append(paths, skillPath)
	}
	if len(paths) == 0 {
		return nil
	}
	file, err := os.OpenFile(configPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, path := range paths {
		block := "\n[[skills.config]]\npath = " + strconv.Quote(filepath.ToSlash(path)) + "\nenabled = false\n"
		if _, err := file.WriteString(block); err != nil {
			return err
		}
	}
	return nil
}

func runClaimsRuntimeSkill(name string, runSkills []Skill) bool {
	claim := sanitizeSkillName(name)
	for _, skill := range runSkills {
		if sanitizeSkillName(skill.Name) == claim {
			return true
		}
	}
	return false
}
