package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/mcp/tools"
)

func TestStudioPreparationDisabledDoesNotInstallOrExposeSkills(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	got, err := prepareStudioRun(
		t.Context(),
		studioRunInput{EnvRoot: root},
		func(context.Context, string, toolRelease) (toolInstallation, error) {
			t.Fatal("disabled Studio installed a CLI")
			return toolInstallation{}, nil
		},
	)
	if err != nil || got.SnapshotDir != "" || len(got.Skills) != 0 {
		t.Fatalf("disabled = %+v, %v", got, err)
	}
	if _, err := runToolProcess(
		t.Context(),
		toolProcess{Executable: filepath.Join(got.BinDir, "zenith-studio"), Args: []string{"--version"}},
	); err == nil {
		t.Fatal("disabled Studio executable succeeded")
	}
}

func TestStudioPreparationDeliversReviewedSkillToMCP(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	root := t.TempDir()
	entry := filepath.Join(root, "fake.cjs")
	if err := os.WriteFile(entry, []byte(`if(process.argv.includes("auth")){console.log(JSON.stringify({ok:true,data:{authenticated:true}}));}else{console.log(JSON.stringify(process.argv.slice(2)));}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var installed []string
	got, err := prepareStudioRun(t.Context(), studioRunInput{
		Enabled: true, EnvRoot: root, CacheRoot: filepath.Join(root, "cache"),
		Run: Run{ID: uuid.New(), CompanyID: uuid.NewString()},
	}, func(_ context.Context, _ string, release toolRelease) (toolInstallation, error) {
		installed = append(installed, release.Key)
		return toolInstallation{Node: node, Entrypoint: entry}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(installed, []string{"zenith-studio", "hyperframes"}) || len(got.Skills) != 1 {
		t.Fatalf("installed = %v, skills = %v", installed, len(got.Skills))
	}
	service, err := tools.NewSkillService(got.SnapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, name := range []string{"SKILL.md", "references/zenith-team-runtime.md", "references/zenith-team-media.md", "references/zenith-team-video.md", "references/media-generation.md", "references/assembly.md"} {
		result, err := service.LoadSkillReference(
			t.Context(),
			tools.LoadSkillReferenceInput{SkillKey: "zenith-studio-cli", Path: name},
		)
		if err != nil || result.Content == "" {
			t.Fatalf("load %s: %v", name, err)
		}
		nativeContent := got.Skills[0].Content
		if name != "SKILL.md" {
			nativeContent = ""
			for _, file := range got.Skills[0].Files {
				if file.Path == name {
					nativeContent = file.Content
					break
				}
			}
		}
		if result.Content != nativeContent {
			t.Fatalf("native and MCP Studio instructions differ for %s", name)
		}
		if strings.Contains(result.Content, "Department") {
			t.Fatalf("obsolete contract in %s", name)
		}
		if name == "SKILL.md" && len(result.References) != 0 {
			t.Fatal("Studio entrypoint includes the entire reference inventory")
		}

		if name == "references/zenith-team-runtime.md" {
			for _, required := range []string{
				"studioCanvasPath", "One Project per Conversation", "voice, audio and music",
				`"workspaceRelativePath": "output/music.mp3"`, "source-NODE_ID.canvas.json",
				"If the export path already exists", "unused path", "Never overwrite",
			} {
				if !strings.Contains(result.Content, required) {
					t.Fatalf("missing runtime contract %q", required)
				}
			}
		}
		if name == "references/zenith-team-media.md" {
			for _, required := range []string{"image", "voice", "audio", "music", "idempotency", "jobs wait", "mime_type", "review"} {
				if !strings.Contains(result.Content, required) {
					t.Errorf("media route missing %q", required)
				}
			}
		}
		if name == "references/zenith-team-video.md" {
			for _, required := range []string{"100 MiB", "renders publish", "source_node_ids", "same bytes", "ffprobe"} {
				if !strings.Contains(result.Content, required) {
					t.Errorf("video route missing %q", required)
				}
			}
		}
	}
	command := filepath.Join(got.BinDir, "zenith-studio")
	output, err := runToolProcess(
		t.Context(),
		toolProcess{
			Executable: command,
			Args:       []string{"--json", "models", "list", "space argument", "$(false)"},
		},
	)
	if err != nil || !strings.Contains(string(output), `"space argument","$(false)"`) {
		t.Fatalf("CLI invocation = %s, %v", output, err)
	}
	for _, args := range [][]string{{"auth", "login"}, {"--json", "auth", "logout"}, {"integrations", "install", "codex"}, {"setup"}} {
		if _, err := runToolProcess(t.Context(), toolProcess{Executable: command, Args: args}); err == nil {
			t.Fatalf("allowed in-Run account/setup mutation: %v", args)
		}
	}
}

func TestStudioPreparationFailsIfReviewedCLIUnavailable(t *testing.T) {
	t.Parallel()
	_, err := prepareStudioRun(
		t.Context(),
		studioRunInput{Enabled: true, EnvRoot: t.TempDir()},
		func(context.Context, string, toolRelease) (toolInstallation, error) {
			return toolInstallation{}, errors.New("offline")
		},
	)
	if err == nil {
		t.Fatal("missing installation silently used ambient tool")
	}
}
