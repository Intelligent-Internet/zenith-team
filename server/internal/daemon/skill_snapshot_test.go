package daemon

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"
	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/execenv"
	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
)

func TestCustomSkillSnapshotMatchesNativeFilesAndRejectsTampering(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{
		"SKILL.md": []byte(
			"---\nname: review-work\ndescription: Review work.\n---\nRead [guide](references/guide.md).\n",
		),
		"references/guide.md": []byte("Review the result before submitting."),
		"assets/sample.bin":   {0, 255, 128, 1},
	}
	bundle, err := contentbundle.BuildSkill("review-work", "Review work.", files)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ID, bundle.Source = uuid.NewString(), "company"
	root := t.TempDir()
	snapshot, err := writeRunSkillSnapshot(
		root,
		Run{ID: uuid.New(), CompanyID: uuid.NewString()},
		[]SkillData{bundle},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := tools.NewSkillService(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for name, want := range files {
		raw, err := os.ReadFile(filepath.Join(snapshot, bundle.Name, name))
		if err != nil || !bytes.Equal(raw, want) {
			t.Fatalf("snapshot %s = %v, %v", name, raw, err)
		}
		if name == "assets/sample.bin" {
			continue
		}
		got, err := service.LoadSkillReference(
			t.Context(),
			tools.LoadSkillReferenceInput{SkillKey: bundle.Name, Path: name},
		)
		if err != nil || got.Content != string(want) {
			t.Fatalf("MCP %s = %+v, %v", name, got, err)
		}
	}
	native := execenv.Skill{Name: bundle.Name, Description: bundle.Description, Content: bundle.Content}
	for _, file := range bundle.Files {
		native.Files = append(
			native.Files,
			execenv.SkillFile{Path: file.Path, Content: file.Content, Data: file.Data},
		)
	}
	env, err := execenv.Prepare(
		execenv.Options{
			EnvRoot:        root,
			WorkDir:        t.TempDir(),
			Provider:       "claude",
			LocalDirectory: true,
			Skills:         []execenv.Skill{native},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(env.ClaudeSkillsDir, bundle.Name, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("native %s differs: %v", name, err)
		}
	}
	corrupt := bundle
	corrupt.Content += "tampered"
	if _, err := writeRunSkillSnapshot(
		root,
		Run{ID: uuid.New(), CompanyID: uuid.NewString()},
		[]SkillData{corrupt},
	); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
	if validateSkillBundle(contentbundle.SkillReference(bundle), corrupt) {
		t.Fatal("tampered bundle passed daemon validation")
	}
	changed := bundle
	changed.Hash = "changed"
	if validateSkillBundle(contentbundle.SkillReference(bundle), changed) {
		t.Fatal("changed version passed frozen identity")
	}
}

func TestRenamedSkillsMatchCodexClaudeAndMCPCopies(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, provider := range []string{"codex", "claude"} {
		for _, name := range []string{"market-research", "market-research-2", "custom-zenith-platform"} {
			t.Run(provider+"/"+name, func(t *testing.T) {
				files := map[string][]byte{
					"SKILL.md": []byte(
						"---\nname: " + name + "\ndescription: Review the result.\n---\nRead [guide](references/guide.md), then run scripts/review.sh.\n",
					),
					"references/guide.md": []byte("Inspect assets/sample.bin."),
					"scripts/review.sh":   []byte("#!/bin/sh\nprintf 'review'\n"),
					"assets/sample.bin":   {0, 255, 128},
				}
				bundle, err := contentbundle.BuildSkill(name, "Review the result.", files)
				if err != nil {
					t.Fatal(err)
				}
				bundle.ID, bundle.Source = uuid.NewString(), "company"
				root := t.TempDir()
				snapshot, err := writeRunSkillSnapshot(
					root,
					Run{ID: uuid.New(), CompanyID: uuid.NewString()},
					[]SkillData{bundle},
				)
				if err != nil {
					t.Fatal(err)
				}
				service, err := tools.NewSkillService(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = service.Close() })
				native := execenv.Skill{
					Name:        name,
					Description: bundle.Description,
					Content:     bundle.Content,
				}
				for _, file := range bundle.Files {
					native.Files = append(
						native.Files,
						execenv.SkillFile{
							Path: file.Path,
							Data: contentbundle.SkillFileBytes(file),
						},
					)
				}
				env, err := execenv.Prepare(execenv.Options{
					EnvRoot: root, WorkDir: t.TempDir(), Provider: provider,
					LocalDirectory: true, Skills: []execenv.Skill{native},
				}, logger)
				if err != nil {
					t.Fatal(err)
				}
				directory := env.ClaudeSkillsDir
				if provider == "codex" {
					directory = filepath.Join(env.CodexHome, "skills")
				}
				for path, want := range files {
					got, err := os.ReadFile(filepath.Join(directory, name, path))
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("native %s = %q, %v, want %q", path, got, err, want)
					}
					if path == "assets/sample.bin" {
						continue
					}
					ref, err := service.LoadSkillReference(
						t.Context(),
						tools.LoadSkillReferenceInput{SkillKey: name, Path: path},
					)
					if err != nil || ref.Content != string(want) {
						t.Fatalf("MCP %s = %+v, %v, want %q", path, ref, err, want)
					}
				}
			})
		}
	}
}

func TestCustomSkillPromptNamesCurrentRunFilesWhenSessionResumes(t *testing.T) {
	t.Parallel()
	bundle, err := contentbundle.BuildSkill("review-work", "Review work.", map[string][]byte{
		"SKILL.md":          []byte("Read assets/sample.bin."),
		"assets/sample.bin": {0, 255, 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	previous, err := writeRunSkillSnapshot(
		root,
		Run{ID: uuid.New(), CompanyID: uuid.NewString()},
		[]SkillData{bundle},
	)
	if err != nil {
		t.Fatal(err)
	}
	current, err := writeRunSkillSnapshot(
		root,
		Run{ID: uuid.New(), CompanyID: uuid.NewString()},
		[]SkillData{bundle},
	)
	if err != nil {
		t.Fatal(err)
	}
	prompt := buildRunSystemPromptWithCLI(
		RunContext{Agent: RunAgentContext{SkillRefs: []SkillRefData{contentbundle.SkillReference(bundle)}}},
		t.TempDir(), runMemoryContext{}, runPlatformPromptContext{SkillSnapshotRoot: current},
	)
	if !strings.Contains(prompt, filepath.Join(current, bundle.Name)) || strings.Contains(prompt, previous) {
		t.Fatal("prompt does not identify the current Run's asset directory")
	}
	contents, err := os.ReadFile(filepath.Join(current, bundle.Name, "assets/sample.bin"))
	if err != nil || !bytes.Equal(contents, []byte{0, 255, 1}) {
		t.Fatalf("advertised asset: %v, %v", contents, err)
	}
}
