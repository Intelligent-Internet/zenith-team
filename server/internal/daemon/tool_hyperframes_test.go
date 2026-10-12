package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHyperframesLauncherKeepsTeamGuidanceAndPinnedCLI(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is not available")
	}
	for _, test := range []struct {
		name        string
		args        []string
		wantAllowed bool
	}{
		{"render", []string{"render", "-o", "a file's name.mp4"}, true},
		{"version", []string{"--version"}, true},
		{"init", []string{"init", "composition"}, true},
		{"external skills", []string{"skills", "update"}, false},
		{"upgrade", []string{"upgrade"}, false},
		{"capture writes guides", []string{"capture", "https://example.com"}, false},
		{"remote template", []string{"init", "composition", "--example", "external"}, false},
		{"repeated template", []string{"init", "composition", "--example", "blank", "--example=external"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			record := filepath.Join(root, "invocation.json")
			entry := filepath.Join(root, "upstream.cjs")
			if err := os.WriteFile(
				entry,
				[]byte(
					`const fs=require('node:fs'), path=require('node:path'); const args=process.argv.slice(2); fs.writeFileSync(process.env.RECORD, JSON.stringify({args,skip:process.env.HYPERFRAMES_SKIP_SKILLS,update:process.env.HYPERFRAMES_NO_UPDATE_CHECK})); if(args[0]==='init'){fs.mkdirSync(args[1],{recursive:true}); for(const name of ['AGENTS.md','CLAUDE.md'])fs.writeFileSync(path.join(args[1],name),'upstream instructions'); fs.writeFileSync(path.join(args[1],'package.json'),'{}'); fs.writeFileSync(path.join(args[1],'index.html'),'composition');} console.log('Install upstream authoring skills');`,
				),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			installed, err := writeHyperframesLauncher(
				root,
				toolInstallation{Node: node, Entrypoint: entry},
			)
			if err != nil {
				t.Fatal(err)
			}
			args := append([]string{}, test.args...)
			project := filepath.Join(root, "composition")
			if args[0] == "init" {
				args[1] = project
			}
			output, err := runToolProcess(
				t.Context(),
				toolProcess{
					Executable: node,
					Args:       append([]string{installed.Entrypoint}, args...),
					Env:        map[string]string{"RECORD": record, "HYPERFRAMES_SKIP_SKILLS": "0"},
				},
			)
			if (err == nil) != test.wantAllowed {
				t.Fatalf("command error = %v, want allowed %v", err, test.wantAllowed)
			}
			raw, readErr := os.ReadFile(record)
			if !test.wantAllowed {
				if !os.IsNotExist(readErr) {
					t.Fatal("blocked command reached upstream")
				}
				return
			}
			var invocation struct {
				Args         []string
				Skip, Update string
			}
			if readErr != nil || json.Unmarshal(raw, &invocation) != nil || invocation.Skip != "1" ||
				invocation.Update != "1" {
				t.Fatalf("invocation = %s, %v", raw, readErr)
			}
			if args[0] == "init" {
				for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
					if _, err := os.Stat(filepath.Join(project, name)); !os.IsNotExist(err) {
						t.Fatal("upstream guide entered the project")
					}
				}
				if strings.Contains(string(output), "upstream") {
					t.Fatal("upstream skill onboarding leaked into Run output")
				}
				packageJSON, err := os.ReadFile(filepath.Join(project, "package.json"))
				if err != nil || strings.Contains(string(packageJSON), "npx") {
					t.Fatal("scaffold bypasses the managed CLI")
				}
			}
		})
	}
}

func TestHyperframesInitPreservesExistingProjectGuides(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is not available")
	}
	root := t.TempDir()
	guide := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(guide, []byte("user guidance"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher, err := writeHyperframesLauncher(
		root,
		toolInstallation{Node: node, Entrypoint: filepath.Join(root, "must-not-run.cjs")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runToolProcess(
		t.Context(),
		toolProcess{Executable: node, Args: []string{launcher.Entrypoint, "init", root}},
	); err == nil {
		t.Fatal("initialized over existing project")
	}
	content, err := os.ReadFile(guide)
	if err != nil || string(content) != "user guidance" {
		t.Fatal("existing guide was modified")
	}
}
