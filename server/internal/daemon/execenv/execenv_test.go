package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeCodexHome pins CODEX_HOME at a temp dir so tests never touch the
// developer's real ~/.codex.
func withFakeCodexHome(t *testing.T) string {
	t.Helper()
	shared := filepath.Join(t.TempDir(), "codex-shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", shared)
	return shared
}

func TestPrepareCodexProviderPass(t *testing.T) {
	shared := withFakeCodexHome(t)
	if err := os.WriteFile(filepath.Join(shared, "auth.json"), []byte(`{"t":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "config.toml"), []byte("model = \"gpt-5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	env, err := Prepare(Options{
		EnvRoot:  envRoot,
		WorkDir:  workDir,
		Provider: "codex",
		Skills:   []Skill{{Name: "Review Guide", Description: "how to review", Content: "Do reviews."}},
	}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if env.CodexHome == "" {
		t.Fatal("codex home not set")
	}
	if target, err := os.Readlink(
		filepath.Join(env.CodexHome, "auth.json"),
	); err != nil ||
		target != filepath.Join(shared, "auth.json") {
		t.Fatalf("auth.json not symlinked to shared home: target=%q err=%v", target, err)
	}
	config, err := os.ReadFile(filepath.Join(env.CodexHome, "config.toml"))
	if err != nil {
		t.Fatalf("config.toml: %v", err)
	}
	for _, want := range []string{"sandbox_mode", "features.multi_agent = false", "features.memories = false", "model = \"gpt-5\""} {
		if !strings.Contains(string(config), want) {
			t.Fatalf("config.toml missing %q:\n%s", want, config)
		}
	}
	skill, err := os.ReadFile(filepath.Join(env.CodexHome, "skills", "review-guide", "SKILL.md"))
	if err != nil {
		t.Fatalf("materialized skill: %v", err)
	}
	if !strings.Contains(string(skill), "name: review-guide") || !strings.Contains(string(skill), "Do reviews.") {
		t.Fatalf("skill content wrong: %q", skill)
	}

	// A second prepare against the same env root (reuse) must be idempotent.
	first, _ := os.ReadFile(filepath.Join(env.CodexHome, "config.toml"))
	if _, err := Prepare(Options{EnvRoot: envRoot, WorkDir: workDir, Provider: "codex"}, testLogger()); err != nil {
		t.Fatalf("re-prepare: %v", err)
	}
	second, _ := os.ReadFile(filepath.Join(env.CodexHome, "config.toml"))
	if string(first) != string(second) {
		t.Fatalf("reused config.toml drifted:\n%s\nvs\n%s", first, second)
	}
}

func TestPrepareCodexConnectorOnlyDropsInheritedExecutionSurfaces(t *testing.T) {
	shared := withFakeCodexHome(t)
	unsafeConfig := `model_provider = "custom"
model_instructions_file = "instructions.md"
web_search = "live"
approval_policy = "never"
sandbox_mode = "danger-full-access"

[model_providers.custom]
name = "Custom"
base_url = "https://models.example.test"

[features]
apps = true
browser_use = true
computer_use = true
hooks = true
memories = true
multi_agent = true
plugins = true
shell_tool = true
skill_search = true
unified_exec = true
view_image = true

[mcp_servers.unreviewed]
command = "unsafe-mcp"

[apps.unreviewed]
enabled = true
`
	if err := os.WriteFile(filepath.Join(shared, "config.toml"), []byte(unsafeConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(shared, "instructions.md"),
		[]byte("unsafe instructions"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(shared, "config.json"),
		[]byte(`{"mcpServers":{"unsafe":{}}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	userSkill := filepath.Join(shared, "skills", "unsafe", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(userSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userSkill, []byte("unsafe skill"), 0o600); err != nil {
		t.Fatal(err)
	}

	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env, err := Prepare(Options{
		EnvRoot:       envRoot,
		WorkDir:       workDir,
		Provider:      "codex",
		ConnectorOnly: true,
		Skills:        []Skill{{Name: "Also Unsafe", Content: "must not exist"}},
	}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	config, err := os.ReadFile(filepath.Join(env.CodexHome, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(config)
	for _, want := range []string{
		`sandbox_mode = 'read-only'`,
		`approval_policy = 'never'`,
		`web_search = 'disabled'`,
		`allow_login_shell = false`,
		`model_provider = 'custom'`,
		`browser_use = false`,
		`computer_use = false`,
		`hooks = false`,
		`memories = false`,
		`multi_agent = false`,
		`plugins = false`,
		`shell_tool = false`,
		`skill_search = false`,
		`unified_exec = false`,
		`view_image = false`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("connector-only config missing %q:\n%s", want, content)
		}
	}
	for _, forbidden := range []string{
		"model_instructions_file", "unsafe-mcp", "apps.unreviewed", "danger-full-access",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("connector-only config retained %q:\n%s", forbidden, content)
		}
	}
	if _, err := os.Stat(filepath.Join(env.CodexHome, "instructions.md")); !os.IsNotExist(err) {
		t.Fatalf("native instructions survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.CodexHome, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy native config survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.CodexHome, "skills")); !os.IsNotExist(err) {
		t.Fatalf("native skills survived: %v", err)
	}
}

func TestPrepareClaudeProviderPass(t *testing.T) {
	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env, err := Prepare(Options{
		EnvRoot:  envRoot,
		WorkDir:  workDir,
		Provider: "claude",
		Skills: []Skill{
			{Name: "Docs", Content: "write docs", Files: []SkillFile{{Path: "ref/notes.md", Content: "n"}}},
		},
		DisabledRuntimeSkills: []RuntimeSkillRef{
			{Root: "personal", Key: "review", Name: "review"},
		},
	}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if env.ClaudeSettingsPath == "" {
		t.Fatal("claude settings path not produced")
	}
	settings, err := os.ReadFile(env.ClaudeSettingsPath)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	for _, want := range []string{`"skillOverrides"`, `"review":"off"`, `Skill(review)`, `Skill(review *)`} {
		if !strings.Contains(string(settings), want) {
			t.Fatalf("settings missing %q: %s", want, settings)
		}
	}
	if _, err := os.Stat(filepath.Join(env.ClaudeSkillsDir, "docs", "SKILL.md")); err != nil {
		t.Fatalf("claude skill not materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.ClaudeSkillsDir, "docs", "ref", "notes.md")); err != nil {
		t.Fatalf("claude skill support file not materialized: %v", err)
	}
}

func TestPrepareClaudeConnectorOnlySuppressesSkillsAndWritesFailClosedSettings(t *testing.T) {
	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env, err := Prepare(Options{
		EnvRoot:       envRoot,
		WorkDir:       workDir,
		Provider:      "claude",
		ConnectorOnly: true,
		Skills:        []Skill{{Name: "Unsafe", Content: "must not exist"}},
		// No file tools run here, and the provider refuses any permission
		// besides "ask", so the credential paths add nothing.
		ProtectedCredentialPaths: []string{t.TempDir()},
	}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	settings, err := os.ReadFile(env.ClaudeSettingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	for _, want := range []string{`"ask":["*"]`, `"disableAllHooks":true`, `"enabledPlugins":{}`} {
		if !strings.Contains(string(settings), want) {
			t.Fatalf("connector-only settings missing %q: %s", want, settings)
		}
	}
	if strings.Contains(string(settings), `"deny"`) {
		t.Fatalf("connector-only settings carry deny rules: %s", settings)
	}
	if env.ClaudeSkillsDir != "" {
		t.Fatalf("ClaudeSkillsDir = %q, want empty", env.ClaudeSkillsDir)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("connector-only preparation materialized skills: %v", err)
	}
}

func TestPrepareConnectorOnlyRejectsReusableOrUserOwnedState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "resume session",
			opts: Options{Provider: "codex", ConnectorOnly: true, ResumeSessionID: "prior"},
			want: "cannot resume",
		},
		{
			name: "session store",
			opts: Options{Provider: "codex", ConnectorOnly: true, SessionStoreKey: "agent/conversation"},
			want: "session store",
		},
		{
			name: "user directory",
			opts: Options{Provider: "claude", ConnectorOnly: true, LocalDirectory: true},
			want: "local directory",
		},
		{
			name: "read-only directories",
			opts: Options{
				Provider: "claude", ConnectorOnly: true,
				ReadOnlyDirs: []string{"/workspaces/c1/company/main/.memory"},
			},
			want: "read-only",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			test.opts.EnvRoot = t.TempDir()
			test.opts.WorkDir = t.TempDir()
			_, err := Prepare(test.opts, testLogger())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Prepare() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestPrepareClaudeLocalDirectorySkipsWorkdirWrites(t *testing.T) {
	envRoot := t.TempDir()
	workDir := t.TempDir() // the user's own directory
	env, err := Prepare(Options{
		EnvRoot:        envRoot,
		WorkDir:        workDir,
		Provider:       "claude",
		LocalDirectory: true,
		Skills:         []Skill{{Name: "Docs", Content: "write docs"}},
	}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if env.ClaudeSkillsDir == "" {
		t.Fatal("local directory run has no skill directory")
	}
	if _, err := os.Stat(filepath.Join(workDir, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("daemon wrote into the user's directory: err=%v", err)
	}
}

func TestPrepareClaudeReadOnlyDirsEnforcedInSettings(t *testing.T) {
	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	memoryDir := "/workspaces/c1/company/main/.memory"
	env, err := Prepare(Options{
		EnvRoot:      envRoot,
		WorkDir:      workDir,
		Provider:     "claude",
		ReadOnlyDirs: []string{memoryDir},
	}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if env.ClaudeSettingsPath == "" {
		t.Fatal("claude settings path not produced for read-only directories")
	}
	settings, err := os.ReadFile(env.ClaudeSettingsPath)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	for _, want := range []string{
		`"additionalDirectories":["/workspaces/c1/company/main/.memory"]`,
		`Edit(//workspaces/c1/company/main/.memory/**)`,
		`Write(//workspaces/c1/company/main/.memory/**)`,
	} {
		if !strings.Contains(string(settings), want) {
			t.Fatalf("settings missing %q: %s", want, settings)
		}
	}
}

func TestPrepareClaudeReadOnlyDirsComposeWithDisabledSkills(t *testing.T) {
	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env, err := Prepare(Options{
		EnvRoot:      envRoot,
		WorkDir:      workDir,
		Provider:     "claude",
		ReadOnlyDirs: []string{"/workspaces/c1/company/main/.memory"},
		DisabledRuntimeSkills: []RuntimeSkillRef{
			{Root: "personal", Key: "review", Name: "review"},
		},
	}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	settings, err := os.ReadFile(env.ClaudeSettingsPath)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	for _, want := range []string{
		`Skill(review)`,
		`Edit(//workspaces/c1/company/main/.memory/**)`,
		`"additionalDirectories"`,
	} {
		if !strings.Contains(string(settings), want) {
			t.Fatalf("settings missing %q: %s", want, settings)
		}
	}
}

// claudeDenyRules reads the deny list from a run's Claude settings file.
func claudeDenyRules(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	return settings.Permissions.Deny
}

func TestPrepareClaudeDeniesFileToolsOnExistingCredentialPaths(t *testing.T) {
	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boardlyHome := t.TempDir()
	sessionStore := filepath.Join(t.TempDir(), "desktop-session.bin")
	if err := os.WriteFile(sessionStore, []byte("sealed"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing-boardly-home")

	env, err := Prepare(Options{
		EnvRoot:                  envRoot,
		WorkDir:                  workDir,
		Provider:                 "claude",
		ProtectedCredentialPaths: []string{boardlyHome, sessionStore, missing, "relative/.boardly", ""},
	}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if env.ClaudeSettingsPath == "" {
		t.Fatal("claude settings path not produced for credential paths")
	}
	got := map[string]bool{}
	for _, rule := range claudeDenyRules(t, env.ClaudeSettingsPath) {
		got[rule] = true
	}

	// t.TempDir sits behind a symlink on macOS (/var -> /private/var); the
	// rules must name both spellings there and the one spelling elsewhere.
	var want []string
	for _, dir := range pathSpellings(boardlyHome) {
		pattern := "//" + strings.TrimPrefix(dir, "/") + "/**"
		want = append(want, "Read("+pattern+")", "Edit("+pattern+")", "Write("+pattern+")")
	}
	for _, file := range pathSpellings(sessionStore) {
		pattern := "//" + strings.TrimPrefix(file, "/")
		want = append(want, "Read("+pattern+")", "Edit("+pattern+")", "Write("+pattern+")")
	}
	for _, rule := range want {
		if !got[rule] {
			t.Errorf("deny rules missing %q; got %v", rule, got)
		}
	}
	for rule := range got {
		if strings.Contains(rule, "missing-boardly-home") || strings.Contains(rule, "relative") {
			t.Errorf("deny rule %q names a path that does not exist or is not absolute", rule)
		}
	}
	if len(got) != len(want) {
		t.Errorf("deny rules = %v, want exactly %v", got, want)
	}
}

func TestPrepareClaudeLeavesACredentialPathHoldingTheRunUndenied(t *testing.T) {
	t.Run("workspace inside the home", func(t *testing.T) {
		boardlyHome := t.TempDir()
		envRoot := filepath.Join(boardlyHome, "workspaces", "run")
		workDir := filepath.Join(envRoot, "workdir")
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			t.Fatal(err)
		}
		env, err := Prepare(Options{
			EnvRoot:                  envRoot,
			WorkDir:                  workDir,
			Provider:                 "claude",
			ProtectedCredentialPaths: []string{boardlyHome},
		}, testLogger())
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		// Denying the home would deny the Run its own workspace; with no
		// other policy there is no settings file at all.
		if env.ClaudeSettingsPath != "" {
			t.Fatalf(
				"settings path = %q, want none: %v",
				env.ClaudeSettingsPath,
				claudeDenyRules(t, env.ClaudeSettingsPath),
			)
		}
	})
	t.Run("read-only folder not created yet inside the home", func(t *testing.T) {
		// On macOS t.TempDir is spelled /var/... and resolves to
		// /private/var/...; a folder that does not exist yet keeps only the
		// first spelling and must still be found inside the home.
		boardlyHome := t.TempDir()
		envRoot := t.TempDir()
		workDir := filepath.Join(envRoot, "workdir")
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			t.Fatal(err)
		}
		env, err := Prepare(Options{
			EnvRoot:                  envRoot,
			WorkDir:                  workDir,
			Provider:                 "claude",
			ReadOnlyDirs:             []string{filepath.Join(boardlyHome, "attachments")},
			ProtectedCredentialPaths: []string{boardlyHome},
		}, testLogger())
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		for _, rule := range claudeDenyRules(t, env.ClaudeSettingsPath) {
			if strings.HasPrefix(rule, "Read(") {
				t.Fatalf("deny rule %q denies the home that holds the Run's read-only folder", rule)
			}
		}
	})
}

func TestClaudeRulePathIsPOSIXFromTheRoot(t *testing.T) {
	tests := []struct {
		volume, slashed, want string
	}{
		{volume: "", slashed: "/Users/mia/.boardly", want: "//Users/mia/.boardly"},
		{volume: "C:", slashed: "C:/Users/mia/.boardly", want: "//c/Users/mia/.boardly"},
	}
	for _, test := range tests {
		if got := posixRulePath(test.volume, test.slashed); got != test.want {
			t.Errorf("posixRulePath(%q, %q) = %q, want %q", test.volume, test.slashed, got, test.want)
		}
	}
}

func TestPrepareCodexAcceptsReadOnlyDirsWithoutConfig(t *testing.T) {
	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env, err := Prepare(Options{
		EnvRoot:      envRoot,
		WorkDir:      workDir,
		Provider:     "codex",
		ReadOnlyDirs: []string{"/workspaces/c1/company/main/.memory"},
	}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// Codex sandbox modes never restrict reads, so the read-only mount must
	// not surface in the per-run config.
	config, err := os.ReadFile(filepath.Join(env.CodexHome, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	if strings.Contains(string(config), ".memory") {
		t.Fatalf("codex config unexpectedly references the memory mount: %s", config)
	}
}

func TestPrepareClaudeNoDisabledSkillsRemovesSettings(t *testing.T) {
	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(envRoot, claudeRuntimeSkillSettingsFile)
	if err := os.WriteFile(stale, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := Prepare(Options{EnvRoot: envRoot, WorkDir: workDir, Provider: "claude"}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if env.ClaudeSettingsPath != "" {
		t.Fatalf("settings path returned with no policy: %q", env.ClaudeSettingsPath)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale settings file survived: err=%v", err)
	}
}

func TestWriteSkillFilesYieldsToOccupiedSlug(t *testing.T) {
	dir := t.TempDir()
	userDir := filepath.Join(dir, "docs")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(userDir, "user.txt")
	if err := os.WriteFile(marker, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSkillFiles(dir, []Skill{{Name: "Docs", Content: "c"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "mine" {
		t.Fatalf("user bytes clobbered: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "docs-boardly", "SKILL.md")); err != nil {
		t.Fatalf("collision-free sibling missing: %v", err)
	}
}

func TestSeedUserCodexSkillsRunSkillWins(t *testing.T) {
	shared := withFakeCodexHome(t)
	userSkill := filepath.Join(shared, "skills", "docs")
	if err := os.MkdirAll(userSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userSkill, "SKILL.md"), []byte("user version"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherSkill := filepath.Join(shared, "skills", "other")
	if err := os.MkdirAll(otherSkill, 0o755); err != nil {
		t.Fatal(err)
	}

	codexHome := t.TempDir()
	if err := hydrateCodexSkills(
		codexHome,
		[]Skill{{Name: "Docs", Content: "run version"}},
		nil,
		testLogger(),
	); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(codexHome, "skills", "docs", "SKILL.md"))
	if err != nil {
		t.Fatalf("run skill: %v", err)
	}
	if !strings.Contains(string(data), "run version") {
		t.Fatalf("run skill lost the conflict: %q", data)
	}
	target, err := os.Readlink(filepath.Join(codexHome, "skills", "other"))
	if err != nil {
		t.Fatalf("user skill not linked: %v", err)
	}
	wantTarget, err := filepath.EvalSymlinks(otherSkill)
	if err != nil {
		t.Fatal(err)
	}
	if target != wantTarget {
		t.Fatalf("user skill link target = %q, want %q", target, wantTarget)
	}
}

func TestCodexSessionStoreLinkAndResumeRollout(t *testing.T) {
	shared := withFakeCodexHome(t)
	sessionID := "0199a1b2-0000-7000-8000-000000000001"
	rolloutDir := filepath.Join(shared, "sessions", "2026", "08", "14")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(rolloutDir, "rollout-2026-08-14T10-00-00-"+sessionID+".jsonl")
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	codexHome := filepath.Join(t.TempDir(), "codex-home")
	key := CodexSessionStoreKey("agent-1", "conv-1")
	if key == "" {
		t.Fatal("empty store key")
	}
	if err := prepareCodexHomeWithOpts(codexHome, CodexHomeOptions{
		GOOS:             "linux",
		ResumeSessionID:  sessionID,
		IsLocalDirectory: true,
		SessionStoreKey:  key,
	}, testLogger()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !CodexResumeRolloutPresent(codexHome, sessionID) {
		t.Fatal("resume rollout not visible through run-local sessions")
	}
	target, err := os.Readlink(filepath.Join(codexHome, "sessions"))
	if err != nil {
		t.Fatalf("sessions is not a link: %v", err)
	}
	if !strings.Contains(target, codexSessionStoreRoot) {
		t.Fatalf("sessions link points outside the store: %q", target)
	}
	if CodexResumeRolloutPresent(codexHome, "missing-session") {
		t.Fatal("phantom rollout reported present")
	}
}

func TestPrepareCodexMigratesLegacySharedSessionsSymlink(t *testing.T) {
	shared := withFakeCodexHome(t)
	sharedSessions := filepath.Join(shared, "sessions")
	if err := os.MkdirAll(sharedSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sharedSessions, filepath.Join(codexHome, "sessions")); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(codexHome, "state_1.sqlite")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareCodexHomeWithOpts(codexHome, CodexHomeOptions{GOOS: "linux"}, testLogger()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(codexHome, "sessions"))
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("legacy shared-sessions symlink survived migration")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale session-state index survived: err=%v", err)
	}
}

func TestSanitizeSkillName(t *testing.T) {
	cases := map[string]string{
		"Review Guide":  "review-guide",
		"  A  B  ":      "a-b",
		"weird***chars": "weird-chars",
		"":              "",
	}
	for in, want := range cases {
		if got := sanitizeSkillName(in); got != want {
			t.Fatalf("sanitizeSkillName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnsureSkillFrontmatterPreservesExisting(t *testing.T) {
	in := "---\nname: mine\n---\nbody"
	if got := ensureSkillFrontmatter(in, "other", "d"); got != in {
		t.Fatalf("existing frontmatter rewritten: %q", got)
	}
	got := ensureSkillFrontmatter("body", "slug", "desc")
	if !strings.HasPrefix(got, "---\nname: slug\ndescription: desc\n---\n") {
		t.Fatalf("frontmatter not synthesized: %q", got)
	}
}

func TestClaudeCustomSkillsInLocalDirectoryAreIsolatedAndKeepBinaryBytes(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	skill := Skill{
		Name:    "review-work",
		Content: "---\nname: review-work\ndescription: Review work.\n---\nUse this.",
		Files:   []SkillFile{{Path: "assets/icon.bin", Data: []byte{0, 255, 128}}},
	}
	first, err := Prepare(
		Options{EnvRoot: root, WorkDir: cwd, Provider: "claude", LocalDirectory: true, Skills: []Skill{skill}},
		testLogger(),
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(first.ClaudeSkillsDir, "review-work/assets/icon.bin"))
	if err != nil || string(data) != string([]byte{0, 255, 128}) {
		t.Fatalf("binary = %v, %v", data, err)
	}
	second, err := Prepare(
		Options{EnvRoot: root, WorkDir: cwd, Provider: "claude", LocalDirectory: true},
		testLogger(),
	)
	if err != nil || second.ClaudeSkillsDir != "" {
		t.Fatalf("removed skill remains: %+v, %v", second, err)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("user tree modified: %v", err)
	}
	if _, err := os.Stat(filepath.Join(first.ClaudeSkillsDir, "review-work/SKILL.md")); err != nil {
		t.Fatalf("old Run snapshot changed: %v", err)
	}
}

func TestPrepareClaudeRemovesSkillsWhenSettingsCannotBeWritten(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, claudeRuntimeSkillSettingsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Prepare(Options{
		EnvRoot: root, WorkDir: t.TempDir(), Provider: "claude",
		Skills: []Skill{{Name: "review-work", Content: "Review carefully."}},
	}, testLogger())
	if err == nil {
		t.Fatal("expected settings write to fail")
	}
	entries, err := filepath.Glob(filepath.Join(root, ".claude-skills-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed preparation retained skill directories: %v, %v", entries, err)
	}
}
