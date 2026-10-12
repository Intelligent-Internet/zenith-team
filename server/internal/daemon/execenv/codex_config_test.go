package execenv

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestEnsureCodexSandboxConfigIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	policy := codexSandboxPolicyFor("linux", "")

	if err := ensureCodexSandboxConfig(configPath, policy, "", testLogger()); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	first, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(first), `sandbox_mode = "danger-full-access"`) {
		t.Fatalf("managed block missing sandbox_mode: %q", first)
	}
	if err := ensureCodexSandboxConfig(configPath, policy, "", testLogger()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	second, _ := os.ReadFile(configPath)
	if string(first) != string(second) {
		t.Fatalf("rewrite not byte-idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestEnsureCodexSandboxConfigPreservesUserContentAndStripsLegacy(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	user := "sandbox_mode = \"read-only\"\n\n[sandbox_workspace_write]\nnetwork_access = false\n\n[model_providers.x]\nbase_url = \"https://example.test\"\n"
	if err := os.WriteFile(configPath, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := codexSandboxPolicyFor("darwin", "")
	if err := ensureCodexSandboxConfig(configPath, policy, "", testLogger()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	content := string(got)
	if strings.Contains(content, "read-only") {
		t.Fatalf("legacy sandbox_mode survived: %q", content)
	}
	if strings.Contains(content, "[sandbox_workspace_write]") {
		t.Fatalf("legacy sandbox_workspace_write table survived: %q", content)
	}
	if !strings.Contains(content, "[model_providers.x]") || !strings.Contains(content, "base_url") {
		t.Fatalf("user content lost: %q", content)
	}
	if !strings.HasPrefix(content, boardlyManagedBeginMarker) {
		t.Fatalf("managed block not hoisted to top: %q", content)
	}
}

func TestCodexSandboxPolicyForPlatforms(t *testing.T) {
	if got := codexSandboxPolicyFor("linux", "0.100.0"); got.Mode != "danger-full-access" {
		t.Fatalf("linux mode = %q", got.Mode)
	}
	if got := codexSandboxPolicyFor("darwin", ""); got.Mode != "danger-full-access" {
		t.Fatalf("darwin unknown-version mode = %q", got.Mode)
	}
}

func TestEnsureCodexMultiAgentConfigIdempotentAndInTable(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(
		configPath,
		[]byte("[features]\nmulti_agent = true\nweb_search = true\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexMultiAgentConfig(configPath, testLogger()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	first, _ := os.ReadFile(configPath)
	content := string(first)
	if !strings.Contains(content, boardlyMultiAgentBeginMarker) {
		t.Fatalf("managed block missing: %q", content)
	}
	if strings.Contains(content, "multi_agent = true") {
		t.Fatalf("user multi_agent directive survived: %q", content)
	}
	if !strings.Contains(content, "web_search = true") {
		t.Fatalf("unrelated feature lost: %q", content)
	}
	// In-table form: bare key, no dotted key that would redefine [features].
	if strings.Contains(content, "features.multi_agent") {
		t.Fatalf("dotted key emitted inside [features] file: %q", content)
	}
	if err := ensureCodexMultiAgentConfig(configPath, testLogger()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	second, _ := os.ReadFile(configPath)
	if string(first) != string(second) {
		t.Fatalf("rewrite not byte-idempotent:\n%s\nvs\n%s", first, second)
	}
}

func TestEnsureCodexMultiAgentConfigEscapeHatch(t *testing.T) {
	t.Setenv(BoardlyCodexMultiAgentEnv, "1")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := ensureCodexMultiAgentConfig(configPath, testLogger()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("escape hatch still wrote config: err=%v", err)
	}
}

func TestEnsureCodexMemoryConfigRootFormIdempotent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := ensureCodexMemoryConfig(configPath, testLogger()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	first, _ := os.ReadFile(configPath)
	content := string(first)
	for _, want := range []string{
		"features.memories = false",
		"memories.generate_memories = false",
		"memories.use_memories = false",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in %q", want, content)
		}
	}
	if err := ensureCodexMemoryConfig(configPath, testLogger()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	second, _ := os.ReadFile(configPath)
	if string(first) != string(second) {
		t.Fatalf("rewrite not byte-idempotent:\n%s\nvs\n%s", first, second)
	}
}

func TestEnsureCodexMemoryConfigInTableForms(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	user := "[features]\nmemories = true\n\n[memories]\ngenerate_memories = true\nuse_memories = true\nextra = 1\n"
	if err := os.WriteFile(configPath, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexMemoryConfig(configPath, testLogger()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	first, _ := os.ReadFile(configPath)
	content := string(first)
	if strings.Contains(content, "memories = true") || strings.Contains(content, "generate_memories = true") {
		t.Fatalf("user memory directives survived: %q", content)
	}
	if !strings.Contains(content, "extra = 1") {
		t.Fatalf("unrelated key lost: %q", content)
	}
	if strings.Contains(content, "features.memories") || strings.Contains(content, "memories.generate_memories") {
		t.Fatalf("dotted keys emitted alongside real tables (TOML redefinition): %q", content)
	}
	if err := ensureCodexMemoryConfig(configPath, testLogger()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	second, _ := os.ReadFile(configPath)
	if string(first) != string(second) {
		t.Fatalf("rewrite not byte-idempotent:\n%s\nvs\n%s", first, second)
	}
}

func TestEnsureCodexMemoryConfigEscapeHatch(t *testing.T) {
	t.Setenv(BoardlyCodexMemoryEnv, "true")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := ensureCodexMemoryConfig(configPath, testLogger()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("escape hatch still wrote config: err=%v", err)
	}
}

func TestStripSkillsConfigEntries(t *testing.T) {
	in := "model = \"gpt-5\"\n\n[[skills.config]]\nname = \"superpowers:brainstorming\"\n\n[[skills.config]]\npath = \"/tmp/skill\"\nenabled = true\n\n[other]\nkey = 1\n"
	out := stripSkillsConfigEntries(in)
	if strings.Contains(out, "skills.config") {
		t.Fatalf("skills.config survived: %q", out)
	}
	if !strings.Contains(out, "model = \"gpt-5\"") || !strings.Contains(out, "[other]") {
		t.Fatalf("unrelated content lost: %q", out)
	}
	if again := stripSkillsConfigEntries(out); again != out {
		t.Fatalf("strip not idempotent")
	}
}

func TestCodexShellEnvAllowlist(t *testing.T) {
	inherited := []string{
		"HOME=/home/u",
		"MY_API_KEY=inherit-secret",
		"BOARDLY_SERVER_URL=http://daemon-only",
		"GH_TOKEN=abc",
	}
	explicit := map[string]string{
		"PATH":              "/run/boardly-bin:/usr/bin",
		"BOARDLY_RUN_TOKEN": "brt_x",
		"BOARDLY_HOME":      "/run/boardly-home",
		"ANTHROPIC_API_KEY": "sk-x",
		"UNSANCTIONED_KEY":  "y",
		"PLAIN":             "1",
	}
	got := CodexShellEnvAllowlist(inherited, explicit, []string{"ANTHROPIC_API_KEY"})
	want := map[string]bool{
		"PATH": true, "HOME": true, "BOARDLY_RUN_TOKEN": true, "BOARDLY_HOME": true,
		"ANTHROPIC_API_KEY": true, "PLAIN": true,
	}
	if len(got) != len(want) {
		t.Fatalf("allowlist = %v, want keys %v", got, want)
	}
	for _, k := range got {
		if !want[k] {
			t.Fatalf("unexpected key %q in %v", k, got)
		}
	}
}

func TestEnsureCodexShellEnvPolicyConfigIdempotentAndReplacesUserPolicy(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	user := "model = \"gpt-5\"\n\n[shell_environment_policy]\ninherit = \"none\"\n\n[profiles.dev.shell_environment_policy]\ninherit = \"all\"\n"
	if err := os.WriteFile(configPath, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCodexShellEnvPolicyConfig(configPath, []string{"PATH", "HOME"}, testLogger()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	first, _ := os.ReadFile(configPath)
	content := string(first)
	if !strings.Contains(content, boardlyShellEnvBeginMarker) {
		t.Fatalf("managed shell env block missing: %q", content)
	}
	if strings.Contains(content, `inherit = 'none'`) || strings.Contains(content, `inherit = "none"`) {
		t.Fatalf("user shell policy survived: %q", content)
	}
	if !strings.Contains(content, "model = \"gpt-5\"") {
		t.Fatalf("unrelated key lost: %q", content)
	}
	if err := EnsureCodexShellEnvPolicyConfig(configPath, []string{"PATH", "HOME"}, testLogger()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	second, _ := os.ReadFile(configPath)
	if string(first) != string(second) {
		t.Fatalf("rewrite not byte-idempotent:\n%s\nvs\n%s", first, second)
	}
}
