package daemon

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

var errToolPrerequisites = errors.New("node and npm prerequisites are unavailable")

type (
	toolInstallation struct{ Prefix, Node, Entrypoint string }
	toolProcess      struct {
		Executable string
		Args       []string
		Env        map[string]string
		Dir        string
	}
)

func runToolProcess(ctx context.Context, input toolProcess) ([]byte, error) {
	command := exec.CommandContext(ctx, input.Executable, input.Args...)
	command.Dir = input.Dir
	command.Env = os.Environ()
	for key, value := range input.Env {
		filtered := command.Env[:0]
		for _, entry := range command.Env {
			if !strings.HasPrefix(entry, key+"=") {
				filtered = append(filtered, entry)
			}
		}
		command.Env = filtered
		command.Env = append(command.Env, key+"="+value)
	}
	command.WaitDelay = 2 * time.Second
	var output toolOutput
	command.Stdout = &output
	// CLI stdout/stderr may include account data. Never propagate them into
	// daemon logs or synchronized status reasons.
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("tool process failed: %w", err)
	}
	if output.overflow {
		return nil, errors.New("tool response exceeds limit")
	}
	return output.Bytes(), nil
}

type toolOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *toolOutput) Write(value []byte) (int, error) {
	length := len(value)
	if b.Len()+length > 2<<20 {
		b.overflow = true
		return length, nil
	}
	_, err := b.Buffer.Write(value)
	return length, err
}

func resolveToolPrerequisites(ctx context.Context, release toolRelease) (string, string, error) {
	node, nodeErr := resolveToolExecutablePath("node")
	npm, npmErr := resolveToolExecutablePath("npm")
	if nodeErr != nil || npmErr != nil {
		found := resolveAgentsViaLoginShell([]string{"node", "npm"})
		if nodeErr != nil {
			node = found["node"]
		}
		if npmErr != nil {
			npm = found["npm"]
		}
	}
	if node == "" || npm == "" {
		return "", "", errToolPrerequisites
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := runToolProcess(probeCtx, toolProcess{Executable: node, Args: []string{"--version"}})
	if err != nil {
		return "", "", err
	}
	major, err := strconv.Atoi(strings.Split(strings.TrimPrefix(strings.TrimSpace(string(output)), "v"), ".")[0])
	if err != nil || major < release.MinimumNodeMajor {
		return "", "", errToolPrerequisites
	}
	return node, npm, nil
}

func npmToolProcess(node, npm string, args []string, environment map[string]string) toolProcess {
	if runtime.GOOS == "windows" {
		// npm.cmd is a shell shim. Node's npm installation exposes the JS
		// entrypoint beside it, avoiding cmd.exe command-string interpolation.
		return toolProcess{
			Executable: node,
			Args: append(
				[]string{filepath.Join(filepath.Dir(npm), "node_modules", "npm", "bin", "npm-cli.js")},
				args...),
			Env: environment,
		}
	}
	return toolProcess{Executable: npm, Args: args, Env: environment}
}

func installToolRelease(ctx context.Context, root string, release toolRelease) (toolInstallation, error) {
	node, npm, err := resolveToolPrerequisites(ctx, release)
	if err != nil {
		return toolInstallation{}, err
	}
	if err := os.MkdirAll(filepath.Join(root, release.Key), 0o700); err != nil {
		return toolInstallation{}, err
	}
	unlock, err := lockToolInstallation(ctx, filepath.Join(root, release.Key, ".install.lock"))
	if err != nil {
		return toolInstallation{}, err
	}
	defer unlock()
	final := filepath.Join(root, release.Key, release.Version)
	if installed, err := inspectToolInstallation(ctx, final, node, release); err == nil {
		return installed, nil
	}
	repairs, err := filepath.Glob(final + "-repair-*")
	if err != nil {
		return toolInstallation{}, err
	}
	slices.Reverse(repairs)
	for _, prefix := range repairs {
		if installed, err := inspectToolInstallation(ctx, prefix, node, release); err == nil {
			return installed, nil
		}
	}
	if _, err := os.Lstat(final); err == nil {
		final += "-repair-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	stage, err := os.MkdirTemp(filepath.Dir(final), ".install-")
	if err != nil {
		return toolInstallation{}, err
	}
	defer os.RemoveAll(stage)
	environment := map[string]string{
		"PATH": pathWithRunCLI(os.Getenv("PATH"), filepath.Dir(node)),
	}
	if release.SkipSetupEnv != "" {
		environment[release.SkipSetupEnv] = "1"
	}
	output, err := runToolProcess(ctx, npmToolProcess(node, npm, []string{
		"pack",
		release.PackageName + "@" + release.Version,
		"--json",
		"--ignore-scripts",
		"--pack-destination",
		stage,
	}, environment))
	if err != nil {
		return toolInstallation{}, err
	}
	var packed []struct {
		Filename string `json:"filename"`
	}
	if json.Unmarshal(output, &packed) != nil || len(packed) != 1 ||
		filepath.Base(packed[0].Filename) != packed[0].Filename {
		return toolInstallation{}, errors.New("npm package response is invalid")
	}
	tarball := filepath.Join(stage, packed[0].Filename)
	if err := verifyToolTarball(tarball, release.Integrity); err != nil {
		return toolInstallation{}, err
	}
	prefix := filepath.Join(stage, "prefix")
	if _, err := runToolProcess(ctx, npmToolProcess(node, npm, []string{
		"install", "--global", "--prefix", prefix, "--no-audit", "--no-fund", "--ignore-scripts", tarball,
	}, environment)); err != nil {
		return toolInstallation{}, err
	}
	if _, err := inspectToolInstallation(ctx, prefix, node, release); err != nil {
		return toolInstallation{}, err
	}
	// Never replace a published prefix: an older daemon or active Run may be
	// using it. An incomplete staging tree is discarded on every failure.
	if err := os.Rename(prefix, final); err != nil {
		if installed, probeErr := inspectToolInstallation(ctx, final, node, release); probeErr == nil {
			return installed, nil
		}
		return toolInstallation{}, fmt.Errorf("publish verified tool installation: %w", err)
	}
	return inspectToolInstallation(ctx, final, node, release)
}

func verifyToolTarball(filename, integrity string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha512.New()
	count, err := io.Copy(digest, io.LimitReader(file, 32<<20+1))
	if err != nil {
		return err
	}
	if count > 32<<20 || "sha512-"+base64.StdEncoding.EncodeToString(digest.Sum(nil)) != integrity {
		return errors.New("npm package integrity differs from reviewed release")
	}
	return nil
}

func toolPackageDirectory(prefix string, release toolRelease) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(prefix, "node_modules", filepath.FromSlash(release.PackageName))
	}
	return filepath.Join(prefix, "lib", "node_modules", filepath.FromSlash(release.PackageName))
}

func inspectToolInstallation(
	ctx context.Context,
	prefix, node string,
	release toolRelease,
) (toolInstallation, error) {
	packageRoot := toolPackageDirectory(prefix, release)
	raw, err := os.ReadFile(filepath.Join(packageRoot, "package.json"))
	if err != nil {
		return toolInstallation{}, err
	}
	var manifest struct {
		Name, Version string
		Bin           map[string]string
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Name != release.PackageName ||
		manifest.Version != release.Version {
		return toolInstallation{}, errors.New("installed npm package differs from release")
	}
	entrypoint := strings.TrimPrefix(manifest.Bin[release.Command], "./")
	if !safeToolSkillPath(entrypoint) {
		return toolInstallation{}, errors.New("installed npm package differs from release")
	}
	installed := toolInstallation{
		Prefix:     prefix,
		Node:       node,
		Entrypoint: filepath.Join(packageRoot, filepath.FromSlash(entrypoint)),
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := runToolProcess(
		probeCtx,
		toolProcess{Executable: node, Args: []string{installed.Entrypoint, "--version"}, Env: map[string]string{
			"HYPERFRAMES_SKIP_SKILLS":     "1",
			"HYPERFRAMES_NO_UPDATE_CHECK": "1",
			"HYPERFRAMES_NO_AUTO_INSTALL": "1",
			"HYPERFRAMES_NO_TELEMETRY":    "1",
		}},
	)
	if err != nil || strings.TrimSpace(string(output)) != release.Version {
		return toolInstallation{}, errors.New("installed tool version probe failed")
	}
	if release.Skill.ID == "" {
		return installed, nil
	}
	stage, err := os.MkdirTemp(filepath.Dir(prefix), ".verify-")
	if err != nil {
		return toolInstallation{}, err
	}
	defer os.RemoveAll(stage)
	_, err = runToolProcess(probeCtx, toolProcess{Executable: node, Args: []string{
		installed.Entrypoint,
		"--json", "integrations", "export", "boardly", "--output", filepath.Join(stage, "export"),
	}})
	if err != nil {
		return toolInstallation{}, err
	}
	if err := verifyExportedToolSkill(filepath.Join(stage, "export"), release); err != nil {
		return toolInstallation{}, err
	}
	return installed, nil
}

func verifyExportedToolSkill(root string, release toolRelease) error {
	// Compare exported files to the reviewed release, including the file set.
	// Never accept a manifest that merely asserts its own content hash.
	bundle, err := resolveToolSkill(release.Skill)
	if err != nil {
		return err
	}
	skillRoot := filepath.Join(root, "skills", release.Skill.Name)
	expected := make(map[string]string)
	for _, file := range toolSkillFiles(bundle) {
		expected[file.Path] = file.Content
	}
	err = filepath.WalkDir(skillRoot, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("skill export contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(skillRoot, filename)
		if err != nil {
			return err
		}
		content, ok := expected[filepath.ToSlash(relative)]
		if !ok {
			return errors.New("skill export contains an unreviewed file")
		}
		actual, err := os.ReadFile(filename)
		if err != nil || string(actual) != content {
			return errors.New("skill export content differs from reviewed release")
		}
		delete(expected, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return err
	}
	if len(expected) != 0 {
		return errors.New("skill export is incomplete")
	}
	return nil
}
