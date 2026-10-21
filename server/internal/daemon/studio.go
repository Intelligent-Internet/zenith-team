package daemon

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/execenv"
)

//go:embed studio_runtime.md
var studioRuntimeInstructions string

//go:embed studio_skill.md
var studioSkillInstructions string

//go:embed studio_media.md
var studioMediaInstructions string

//go:embed studio_video.md
var studioVideoInstructions string

func studioEnabledForRun(doc RunContext) bool {
	return !isScheduledRun(doc) && slices.Contains(doc.Agent.Capabilities.EnabledTools, "zenith-studio")
}

type studioRunInput struct {
	Enabled   bool
	EnvRoot   string
	CacheRoot string
	AuthHome  string
	APIURL    string
	Endpoint  string
	RunToken  string
	Run       Run
}

type studioRunEnvironment struct {
	BinDir      string
	SnapshotDir string
	Skills      []execenv.Skill
	// Bundle is the Studio skill, frozen into the Run's skill snapshot with
	// the Agent's custom skills.
	Bundle *SkillData
	// Home is the Agent's own Studio sign-in folder under the workspaces
	// root, which the Run's Studio CLI reads and renews; empty when Studio
	// is off. It is the one folder there outside the Run's own that its
	// sandbox lets it write.
	Home string
	// ToolDirs are the reviewed Tool installations the Run's commands start,
	// under the workspaces root. A tagged Run's sandbox, which hides that
	// root, opens them for reading.
	ToolDirs []string
}

// prepareStudioRun materializes reviewed executables and skills after opt-in.
// Authentication is managed by Desktop, never initiated by a Run.
func prepareStudioRun(
	ctx context.Context,
	input studioRunInput,
	install func(context.Context, string, toolRelease) (toolInstallation, error),
) (studioRunEnvironment, error) {
	result := studioRunEnvironment{BinDir: filepath.Join(input.EnvRoot, ".managed-tools")}
	if err := os.MkdirAll(result.BinDir, 0o700); err != nil {
		return result, err
	}
	if !input.Enabled {
		// Shadow an ambient CLI on the normal Run PATH when the Agent opted out.
		return result, writeToolCommand(result.BinDir, "zenith-studio", toolInstallation{})
	}
	if _, err := loadReviewedToolCatalog(); err != nil {
		return result, err
	}
	release, _ := findToolRelease("zenith-studio")
	releases, err := toolReleasesForRun([]toolSkillRef{release.Skill})
	if err != nil {
		return result, err
	}
	result.Home = input.AuthHome
	for _, release := range releases {
		installed, err := install(ctx, input.CacheRoot, release)
		if err != nil {
			return result, fmt.Errorf("prepare %s: %w", release.Name, err)
		}
		if installed.Prefix != "" {
			result.ToolDirs = append(result.ToolDirs, installed.Prefix)
		}
		switch release.Key {
		case "zenith-studio":
			// Studio runs only against the origin the server configured.
			apiURL := input.APIURL
			if apiURL == "" {
				return result, fmt.Errorf(
					"prepare %s: Zenith Studio is off on this server",
					release.Name,
				)
			}
			installed, err = writeStudioLauncher(
				result.BinDir,
				installed,
				studioLauncherConfig{
					Home:     input.AuthHome,
					APIURL:   apiURL,
					Endpoint: input.Endpoint,
					RunToken: input.RunToken,
				},
			)
		case "hyperframes":
			installed, err = writeHyperframesLauncher(result.BinDir, installed)
		}
		if err != nil {
			return result, err
		}
		if err := writeToolCommand(result.BinDir, release.Command, installed); err != nil {
			return result, err
		}
	}
	bundle, err := resolveToolSkill(release.Skill)
	if err != nil {
		return result, err
	}
	bundle = adaptStudioSkill(bundle)
	result.Bundle = &bundle
	result.SnapshotDir, err = writeStudioSkillSnapshot(input.EnvRoot, input.Run, bundle)
	if err != nil {
		return result, err
	}
	skill := execenv.Skill{Name: bundle.Name, Description: bundle.Description, Content: bundle.Content}
	for _, file := range bundle.Files {
		skill.Files = append(skill.Files, execenv.SkillFile{Path: file.Path, Content: file.Content})
	}
	result.Skills = []execenv.Skill{skill}
	return result, nil
}

func writeToolCommand(root, command string, installed toolInstallation) error {
	filename := filepath.Join(root, command)
	body := "#!/bin/sh\nprintf '%s\\n' 'Zenith Studio is disabled for this Agent. Enable it in Zenith Team tools for a new Run.' >&2\nexit 78\n"
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	if installed.Node != "" {
		body = "#!/bin/sh\nexec " + quote(installed.Node) + " " + quote(installed.Entrypoint) + " \"$@\"\n"
	}
	if os.PathSeparator == '\\' {
		filename += ".cmd"
		body = "@echo Zenith Studio is disabled for this Agent. Enable it in Zenith Team tools for a new Run. 1>&2\r\n@exit /b 78\r\n"
		if installed.Node != "" {
			quote = func(value string) string { return "\"" + strings.ReplaceAll(value, "%", "%%") + "\"" }
			body = "@echo off\r\n" + quote(installed.Node) + " " + quote(installed.Entrypoint) + " %*\r\n"
		}
	}
	return os.WriteFile(filename, []byte(body), 0o700)
}

// Adapt the reviewed skill to Zenith Team’s Run and output contracts.
func adaptStudioSkill(bundle toolSkillBundle) toolSkillBundle {
	bundle.Content = studioSkillInstructions
	// Own only the Team execution adapters; retain every shared creative guide.
	bundle.Files = slices.Clone(bundle.Files)
	for _, file := range []toolSkillFile{
		{Path: "references/zenith-team-runtime.md", Content: studioRuntimeInstructions},
		{Path: "references/zenith-team-media.md", Content: studioMediaInstructions},
		{Path: "references/zenith-team-video.md", Content: studioVideoInstructions},
	} {
		digest := sha256.Sum256([]byte(file.Content))
		file.SHA256 = hex.EncodeToString(digest[:])
		file.SizeBytes = int64(len(file.Content))
		i := slices.IndexFunc(
			bundle.Files,
			func(existing toolSkillFile) bool { return existing.Path == file.Path },
		)
		if i < 0 {
			bundle.Files = append(bundle.Files, file)
		} else {
			bundle.Files[i] = file
		}
	}
	digest := sha256.New()
	bundle.SizeBytes = 0
	for _, file := range toolSkillFiles(bundle) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(file.Path)))
		digest.Write(length[:])
		digest.Write([]byte(file.Path))
		binary.BigEndian.PutUint64(length[:], uint64(len(file.Content)))
		digest.Write(length[:])
		digest.Write([]byte(file.Content))
		bundle.SizeBytes += int64(len(file.Content))
	}
	bundle.Hash = hex.EncodeToString(digest.Sum(nil))
	return bundle
}

func studioRuntimeSkillDenials() []execenv.RuntimeSkillRef {
	var result []execenv.RuntimeSkillRef
	for _, name := range []string{"zenith-studio-cli", "zenith-studio-cli-skill-installer"} {
		for _, root := range []string{"provider", "universal"} {
			result = append(result, execenv.RuntimeSkillRef{Root: root, Key: name, Name: name})
		}
	}
	return result
}

// prepareStudioEnvironment uses the same Company-and-Agent home and shared installation as the
// Desktop controls. The service never starts a browser during Run preparation.
func (d *Daemon) prepareStudioEnvironment(ctx context.Context, input studioRunInput) (studioRunEnvironment, error) {
	if !input.Enabled {
		return prepareStudioRun(ctx, input, nil)
	}
	if d.cfg.StudioAPIOrigin == "" {
		return studioRunEnvironment{}, errStudioNotConfigured
	}
	if d.tools == nil {
		return studioRunEnvironment{}, fmt.Errorf(
			"Zenith Studio setup is unavailable; restart Zenith Team Desktop",
		)
	}
	var err error
	input.RunToken, err = runScopedAuthToken(input.Run)
	if err != nil {
		return studioRunEnvironment{}, err
	}
	input.Endpoint = fmt.Sprintf(
		"http://127.0.0.1:%d/v1/tools/studio-project/%s/%s",
		d.cfg.HealthPort,
		input.Run.RuntimeID,
		input.Run.ID,
	)
	input.APIURL = d.cfg.StudioAPIOrigin
	release, _ := findToolRelease("zenith-studio")
	input.AuthHome, err = d.tools.authHome(toolAuthScope{input.Run.CompanyID, input.Run.AgentID}, release)
	if err != nil {
		return studioRunEnvironment{}, err
	}
	return prepareStudioRun(
		ctx,
		input,
		func(_ context.Context, _ string, release toolRelease) (toolInstallation, error) {
			d.tools.mu.Lock()
			installed, ok := d.tools.installed[release.ID()]
			d.tools.mu.Unlock()
			if !ok {
				return toolInstallation{}, fmt.Errorf(
					"%s setup is unavailable; use Repair setup in Zenith Team tools",
					release.Name,
				)
			}
			return installed, nil
		},
	)
}
