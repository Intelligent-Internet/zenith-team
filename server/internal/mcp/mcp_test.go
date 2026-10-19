package mcp

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"boardly.dev/boardly/server/internal/contentbundle"
	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/internal/runchannel"
)

func TestResolveRunContextUsesOnlyRunScopedIdentity(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"BOARDLY_MCP_COMPANY_ID":      "01990ad8-0bb0-7de0-acfa-9f2821fc1a52",
		"BOARDLY_MCP_CONVERSATION_ID": "01990ad8-0bb0-7de0-acfa-9f2821fc1a53",
		"BOARDLY_MCP_TASK_ID":         "01990ad8-0bb0-7de0-acfa-9f2821fc1a55",
		"BOARDLY_MCP_RUN_ID":          "00000000-0000-4000-8000-000000000071",
		"BOARDLY_MCP_AGENT_ID":        "01990ad8-0bb0-7de0-acfa-9f2821fc1a54",
	}
	got, err := resolveRunContext(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if got.CompanyID != values["BOARDLY_MCP_COMPANY_ID"] ||
		got.ConversationID != values["BOARDLY_MCP_CONVERSATION_ID"] ||
		got.TaskID != values["BOARDLY_MCP_TASK_ID"] ||
		got.RunID != values["BOARDLY_MCP_RUN_ID"] ||
		got.AgentID != values["BOARDLY_MCP_AGENT_ID"] {
		t.Fatalf("resolveRunContext() = %+v", got)
	}
}

func TestResolveSkillSnapshotDirRequiresCanonicalAbsoluteDirectory(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := resolveSkillSnapshotDir(func(key string) string {
		if key == "BOARDLY_SKILL_SNAPSHOT_DIR" {
			return root
		}
		return ""
	})
	if !ok || got != root {
		t.Fatalf("resolveSkillSnapshotDir() = %q, %t", got, ok)
	}

	for _, value := range []string{"", "relative/snapshot", filepath.Join(root, ".", "nested") + string(os.PathSeparator) + ".."} {
		if got, ok := resolveSkillSnapshotDir(func(string) string { return value }); ok || got != "" {
			t.Fatalf("resolveSkillSnapshotDir(%q) = %q, %t", value, got, ok)
		}
	}
}

// The Company Plan draft and export live in the Run environment the daemon
// hands over, never in the directory the Agent works in (Runs TECH §11.2:
// deliverables stay in cwd, a Run's temporary artifacts do not). The process
// cwd is only the fallback for a hand-started MCP process.
func TestResolveCompanyPlanRootPrefersTheRunEnvironment(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveCompanyPlanRoot(func(key string) string {
		if key == "BOARDLY_MCP_PLAN_ROOT" {
			return root
		}
		return ""
	})
	if err != nil || got != root {
		t.Fatalf("resolveCompanyPlanRoot(env) = %q, %v; want %q", got, err, root)
	}
	got, err = resolveCompanyPlanRoot(func(string) string { return "" })
	if err != nil || got != cwd {
		t.Fatalf("resolveCompanyPlanRoot(no env) = %q, %v; want the cwd %q", got, err, cwd)
	}
	// Whether the root is a real directory is the service's judgment.
	for _, value := range []string{"relative/plan", filepath.Join(root, "missing")} {
		if _, err := tools.NewCompanyPlanServiceForWorkspace(nil, value); err == nil {
			t.Fatalf("NewCompanyPlanServiceForWorkspace(%q) accepted a root it must refuse", value)
		}
	}
}

func TestTaskPlatformSkillsRequireBothToolsAndCanonicalRunIdentity(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		"BOARDLY_MCP_RUN_ID": "00000000-0000-4000-8000-000000000071",
	}
	allowed := map[string]struct{}{
		"search_skills":        {},
		"load_skill_reference": {},
	}
	if !taskPlatformSkillsAvailable(allowed, func(key string) string { return environment[key] }) {
		t.Fatal("Task platform Skills were not enabled")
	}
	delete(allowed, "load_skill_reference")
	if taskPlatformSkillsAvailable(allowed, func(key string) string { return environment[key] }) {
		t.Fatal("partial Skill tool allowlist enabled platform Skills")
	}
	allowed["load_skill_reference"] = struct{}{}
	environment["BOARDLY_MCP_RUN_ID"] = "071"
	if taskPlatformSkillsAvailable(allowed, func(key string) string { return environment[key] }) {
		t.Fatal("non-canonical Run identity enabled platform Skills")
	}
}

// boardly-mcp reaches the server only through the Run channel. With no
// run.json in its BOARDLY_HOME it refuses to start: it never looks for
// auth.json, in that home or in ~/.boardly, and never reads a Run token from
// its environment (Access TECH B3b).
func TestRunRefusesToStartWithoutTheRunChannel(t *testing.T) {
	t.Parallel()
	signedIn := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(signedIn, "auth.json"),
		[]byte(`{"version":1,"accessToken":"person-bearer"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	for name, home := range map[string]string{
		"no BOARDLY_HOME":            "",
		"a home with only auth.json": signedIn,
	} {
		environment := map[string]string{
			"BOARDLY_HOME":              home,
			"BOARDLY_RUN_TOKEN":         "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"BOARDLY_MCP_ALLOWED_TOOLS": "ask_human",
		}
		err := Run(t.Context(), func(key string) string { return environment[key] })
		if !runchannel.IsRunControlUnavailable(err) {
			t.Fatalf("%s: Run() error = %v, want the Run channel unavailable", name, err)
		}
	}
}

// The credential client (apiclient.New, which reads auth.json) and the
// credential store serve the daemon's output presenter alone. Nothing
// boardly-mcp runs may build or read them, so a later change cannot hand a
// tool the person's session (Access TECH B3b).
func TestBoardlyMcpNeverBuildsTheCredentialClient(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{".", "tools", "companyplan", "../../cmd/mcp"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := selector.X.(*ast.Ident); ok &&
					(pkg.Name == "authfile" || pkg.Name == "apiclient" && selector.Sel.Name == "New") {
					t.Errorf(
						"%s uses %s.%s; boardly-mcp holds no credential",
						file,
						pkg.Name,
						selector.Sel.Name,
					)
				}
				return true
			})
		}
	}
}

// replacePlatformBundles stands in a pin for one test. Tests that call it do
// not run in parallel, since the loader is package state.
func replacePlatformBundles(t *testing.T, load func() ([]tools.PlatformBundle, []contentbundle.Lock, error)) {
	t.Helper()
	original := loadPlatformBundles
	loadPlatformBundles = load
	t.Cleanup(func() { loadPlatformBundles = original })
}

func TestOpenTaskSkillsServesThePinnedPlaybooks(t *testing.T) {
	var diagnostics bytes.Buffer
	service, err := openTaskSkills("", &diagnostics)
	if err != nil {
		t.Fatalf("openTaskSkills() error = %v", err)
	}
	result, err := service.SearchSkills(t.Context(), tools.SearchSkillsInput{Query: "workflow"})
	if err != nil || len(result.Skills) == 0 || result.Skills[0].Key != tools.ZenithWorkflowsSkillKey {
		t.Fatalf("SearchSkills(workflow) = %+v, %v", result, err)
	}
	if !strings.Contains(diagnostics.String(), "platform bundle zenith-workflows ") ||
		strings.Contains(diagnostics.String(), "dropped") {
		t.Fatalf("diagnostics = %q", diagnostics.String())
	}
	guide, err := service.LoadSkillReference(t.Context(), tools.LoadSkillReferenceInput{
		SkillKey: tools.ZenithPlatformSkillKey, Path: "references/project-pages.md",
	})
	if err != nil || !strings.Contains(guide.Content, "page_edit") {
		t.Fatalf("native Page guide without a custom Skill snapshot = %+v, %v", guide, err)
	}
}

// A pin that fails verification, or a pinned skill that fails to load, costs
// only that skill: boardly-mcp still starts and serves zenith-platform.
// Before ADR 0016 either fault made
// boardly-mcp exit for every Run with skill tools.
func TestOpenTaskSkillsKeepsPlatformSkillWhenAPinFails(t *testing.T) {
	drifted := []byte("---\nname: zenith-workflows\ndescription: Drifted.\n---\n")
	brokenSkill := tools.PlatformBundle{
		Key:         tools.ZenithWorkflowsSkillKey,
		Name:        "Zenith workflows",
		Description: "Load the playbook.",
		Files:       fstest.MapFS{"SKILL.md": {Data: drifted}},
	}
	for name, load := range map[string]func() ([]tools.PlatformBundle, []contentbundle.Lock, error){
		"verification fails": func() ([]tools.PlatformBundle, []contentbundle.Lock, error) {
			return nil, nil, errors.Join(
				errors.New("zenith-workflows.lock.json: size or sha256 does not match the lock"),
				errors.New("no Workflow catalog is pinned"),
			)
		},
		"skill fails to load": func() ([]tools.PlatformBundle, []contentbundle.Lock, error) {
			return []tools.PlatformBundle{brokenSkill}, nil, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			replacePlatformBundles(t, load)
			var diagnostics bytes.Buffer
			service, err := openTaskSkills("", &diagnostics)
			if err != nil {
				t.Fatalf("openTaskSkills() error = %v", err)
			}
			procedure, err := service.LoadSkillReference(t.Context(), tools.LoadSkillReferenceInput{
				SkillKey: tools.ZenithPlatformSkillKey, Path: "SKILL.md",
			})
			if err != nil || !strings.Contains(procedure.Content, "Operating Zenith Team") {
				t.Fatalf("platform instructions = %v", err)
			}
			if _, err := service.LoadSkillReference(t.Context(), tools.LoadSkillReferenceInput{
				SkillKey: tools.ZenithWorkflowsSkillKey, Path: "SKILL.md",
			}); err == nil {
				t.Fatal("a failed pin was served")
			}
			if !strings.Contains(diagnostics.String(), "boardly-mcp: dropped") {
				t.Fatalf("diagnostics = %q, want the drop reported", diagnostics.String())
			}
			// Every line keeps the prefix, even for a joined error.
			for _, line := range strings.Split(strings.TrimSpace(diagnostics.String()), "\n") {
				if !strings.HasPrefix(line, "boardly-mcp: ") {
					t.Fatalf("diagnostics line %q lacks the boardly-mcp: prefix", line)
				}
			}
		})
	}
}
