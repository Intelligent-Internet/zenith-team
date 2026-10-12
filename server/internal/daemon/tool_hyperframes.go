package daemon

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// The managed renderer is an executable dependency. Its upstream skill
// installer and authoring workflows must never replace Team's reviewed guides.
//
//go:embed tool_hyperframes.cjs
var hyperframesLauncher string

func hyperframesEnvironment() map[string]string {
	environment := map[string]string{
		"HYPERFRAMES_SKIP_SKILLS":     "1",
		"HYPERFRAMES_NO_UPDATE_CHECK": "1",
		"HYPERFRAMES_NO_AUTO_INSTALL": "1",
		"HYPERFRAMES_NO_TELEMETRY":    "1",
	}
	for command, key := range map[string]string{
		"ffmpeg": "HYPERFRAMES_FFMPEG_PATH", "ffprobe": "HYPERFRAMES_FFPROBE_PATH",
	} {
		filename, err := resolveToolExecutablePath(command)
		if err != nil {
			filename = resolveAgentsViaLoginShell([]string{command})[command]
		}
		if filename != "" {
			environment[key] = filename
		}
	}
	return environment
}

func prepareHyperframes(ctx context.Context, installed toolInstallation) error {
	environment := hyperframesEnvironment()
	for _, key := range []string{"HYPERFRAMES_FFMPEG_PATH", "HYPERFRAMES_FFPROBE_PATH"} {
		if environment[key] == "" {
			return errToolPrerequisites
		}
		if _, err := runToolProcess(
			ctx,
			toolProcess{Executable: environment[key], Args: []string{"-version"}},
		); err != nil {
			return fmt.Errorf("renderer prerequisite: %w", errToolPrerequisites)
		}
	}
	_, err := runToolProcess(ctx, toolProcess{
		Executable: installed.Node, Args: []string{installed.Entrypoint, "browser", "ensure"}, Env: environment,
	})
	if err != nil {
		return err
	}
	return checkHyperframes(ctx, installed)
}

func checkHyperframes(ctx context.Context, installed toolInstallation) error {
	output, err := runToolProcess(ctx, toolProcess{
		Executable: installed.Node,
		Args:       []string{installed.Entrypoint, "doctor", "--json"},
		Env:        hyperframesEnvironment(),
	})
	if err != nil {
		return err
	}
	return validateHyperframesHealth(output)
}

func validateHyperframesHealth(output []byte) error {
	var report struct {
		Checks []struct {
			Name string
			OK   bool
		}
	}
	if json.Unmarshal(output, &report) != nil {
		return errToolPrerequisites
	}
	for _, required := range []string{"FFmpeg", "FFprobe", "Chrome"} {
		ready := false
		for _, check := range report.Checks {
			if check.Name == required {
				ready = check.OK
				break
			}
		}
		if !ready {
			return errToolPrerequisites
		}
	}
	return nil
}

func writeHyperframesLauncher(root string, installed toolInstallation) (toolInstallation, error) {
	config, err := json.Marshal(struct {
		Entrypoint string            `json:"entrypoint"`
		Env        map[string]string `json:"env"`
	}{installed.Entrypoint, hyperframesEnvironment()})
	if err != nil {
		return toolInstallation{}, err
	}
	filename := filepath.Join(root, "hyperframes.cjs")
	if err := os.WriteFile(
		filename,
		[]byte("const managed = "+string(config)+";\n"+hyperframesLauncher),
		0o600,
	); err != nil {
		return toolInstallation{}, err
	}
	installed.Entrypoint = filename
	return installed, nil
}
