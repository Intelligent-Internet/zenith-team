package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitializationPolicySeparatesResearchFromGreeting(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"company_research", "company_welcome"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			var b strings.Builder
			appendCompanyInitializationPolicy(&b, phase)
			for _, want := range []string{"Do not create a Company Plan", "Do not send email", "untrusted"} {
				if !strings.Contains(b.String(), want) {
					t.Fatalf("policy missing %q", want)
				}
			}
			if phase == "company_research" && !strings.Contains(b.String(), "final answer must be the business context Markdown") {
				t.Fatal("research must return persistable context")
			}
			if phase == "company_welcome" && !strings.Contains(b.String(), "configured name") {
				t.Fatal("welcome must honor renamed CEO")
			}
			if phase == "company_welcome" && !strings.Contains(b.String(), "business context is saved") {
				t.Fatal("welcome must point the founder at the saved file")
			}
		})
	}
}

func TestInitializationCannotAskForInputOrCreatePlans(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"company_research", "company_welcome"} {
		doc := RunContext{ExecutionProfile: RunExecutionProfileContext{Source: source}, Agent: RunAgentContext{IsRoot: true}}
		if tools := boardlyMCPToolsForRun(doc); len(tools) != 0 {
			t.Fatalf("%s platform tools = %v, want none during initialization", source, tools)
		}
	}
}

func TestSeedInitialBusinessContextWritesTheFileOnlyForTheWelcome(t *testing.T) {
	t.Parallel()
	welcome := RunContext{
		ExecutionProfile: RunExecutionProfileContext{Source: "company_welcome"},
		Company:          &RunCompanyContext{ResearchedContext: "# Business context\nA shop.\n"},
	}
	workDir := t.TempDir()
	path, err := seedInitialBusinessContext(welcome, workDir)
	if err != nil || path == "" || existingBusinessContextPath(workDir) != path {
		t.Fatalf("seed = %q/%v, want the shared business context file", path, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "# Business context\nA shop.\n" {
		t.Fatalf("content = %q", got)
	}

	// A file the founder or the CEO already wrote is never replaced.
	if err := os.WriteFile(path, []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if again, err := seedInitialBusinessContext(welcome, workDir); err != nil || again != "" {
		t.Fatalf("second seed = %q/%v, want no write", again, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "edited" {
		t.Fatalf("existing file replaced with %q", got)
	}

	for name, doc := range map[string]RunContext{
		"research":   {ExecutionProfile: RunExecutionProfileContext{Source: "company_research"}, Company: welcome.Company},
		"message":    {ExecutionProfile: RunExecutionProfileContext{Source: "message"}, Company: welcome.Company},
		"no context": {ExecutionProfile: welcome.ExecutionProfile, Company: &RunCompanyContext{}},
	} {
		dir := t.TempDir()
		if path, err := seedInitialBusinessContext(doc, dir); err != nil || path != "" || existingBusinessContextPath(dir) != "" {
			t.Fatalf("%s seed = %q/%v, want nothing written", name, path, err)
		}
	}
}

func TestSeedInitialBusinessContextStaysInsideTheWorkspace(t *testing.T) {
	t.Parallel()
	workDir, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, "assets", "general")); err != nil {
		t.Fatal(err)
	}
	doc := RunContext{
		ExecutionProfile: RunExecutionProfileContext{Source: "company_welcome"},
		Company:          &RunCompanyContext{ResearchedContext: "# Business context"},
	}
	if _, err := seedInitialBusinessContext(doc, workDir); err == nil {
		t.Fatal("seed followed a symlink out of the workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "business_context.md")); !os.IsNotExist(err) {
		t.Fatalf("file written outside the workspace: %v", err)
	}
}
