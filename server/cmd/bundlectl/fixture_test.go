package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"

	"github.com/Intelligent-Internet/zenith-team/server/internal/platformbundle"
)

// runCommand runs bundlectl in-process and returns what it printed and its
// exit status.
func runCommand(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

// sampleWorkflows returns the files of the first count Workflows of the
// pinned catalog, which pass the real parser, keyed as the component keys
// them.
func sampleWorkflows(t *testing.T, count int) (map[string][]byte, []string) {
	t.Helper()
	catalog, _, err := platformbundle.WorkflowCatalog()
	if err != nil {
		t.Fatal(err)
	}
	forms, err := fs.Glob(catalog, "*/workflow.yaml")
	if err != nil || len(forms) < count {
		t.Fatalf("pinned forms = %d, %v", len(forms), err)
	}
	files := map[string][]byte{}
	keys := make([]string, 0, count)
	for _, form := range forms[:count] {
		key := path.Dir(form)
		keys = append(keys, key)
		for _, name := range []string{"workflow.yaml", "playbook.md"} {
			contents, err := fs.ReadFile(catalog, key+"/"+name)
			if err != nil {
				t.Fatal(err)
			}
			files[key+"/"+name] = contents
		}
	}
	return files, keys
}

func manifestFor(name, version string) []byte {
	return fmt.Appendf(nil, `{
  "format": 1,
  "name": %q,
  "version": %q,
  "description": "Workflows for a bundlectl test",
  "workflows": {
    "path": "./boardly/workflows"
  }
}
`, name, version)
}

func retiredKeysFor(keys ...string) []byte {
	quoted := make([]string, 0, len(keys))
	for _, key := range keys {
		quoted = append(quoted, fmt.Sprintf("%q", key))
	}
	return fmt.Appendf(nil, "{\n  \"retiredKeys\": [%s]\n}\n", strings.Join(quoted, ", "))
}

func writeFile(t *testing.T, name string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writePlugin lays a plugin out at root/plugins/<name>, replacing any
// content already there, and returns its folder. retired nil writes no
// retired-keys file.
func writePlugin(t *testing.T, root, name, version string, files map[string][]byte, retired []string) string {
	t.Helper()
	dir := filepath.Join(root, "plugins", name)
	if err := os.RemoveAll(filepath.Join(dir, "boardly")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".boardly-plugin", "plugin.json"), manifestFor(name, version))
	retiredPath := filepath.Join(dir, ".boardly-plugin", "retired-keys.json")
	if retired == nil {
		os.Remove(retiredPath)
	} else {
		writeFile(t, retiredPath, retiredKeysFor(retired...))
	}
	for relative, contents := range files {
		writeFile(t, filepath.Join(dir, "boardly", "workflows", filepath.FromSlash(relative)), contents)
	}
	return dir
}

// fixtureRepo is a zenith-team-plugins repository in a temporary directory. Its
// git commands ignore the machine's configuration and carry their own
// identity, so a signing or hook setting cannot leak in.
type fixtureRepo struct {
	t   *testing.T
	dir string
}

func newFixtureRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	repo := &fixtureRepo{t: t, dir: t.TempDir()}
	repo.git("init", "--quiet", "-b", "main")
	repo.git("config", "user.name", "Bundle Fixture")
	repo.git("config", "user.email", "fixture@example.com")
	repo.git("config", "commit.gpgsign", "false")
	repo.git("config", "tag.gpgsign", "false")
	repo.git("config", "core.autocrlf", "false")
	return repo
}

func (repo *fixtureRepo) git(args ...string) string {
	repo.t.Helper()
	command := exec.Command("git", append([]string{"-C", repo.dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		repo.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// commitPlugin writes a plugin and commits everything.
func (repo *fixtureRepo) commitPlugin(name, version string, files map[string][]byte, retired []string) string {
	repo.t.Helper()
	writePlugin(repo.t, repo.dir, name, version, files, retired)
	return repo.commit("plugin " + name + " " + version)
}

func (repo *fixtureRepo) commit(message string) string {
	repo.t.Helper()
	repo.git("add", "--all")
	repo.git("commit", "--quiet", "--allow-empty", "-m", message)
	return repo.git("rev-parse", "HEAD")
}

func (repo *fixtureRepo) tag(name string) {
	repo.t.Helper()
	repo.git("tag", "-a", name, "-m", name)
}

// readPinned reads and verifies the plugin's pin in a pin directory.
func readPinned(t *testing.T, out, plugin string) contentbundle.Bundle {
	t.Helper()
	pinned, err := readPin(out, plugin)
	if err != nil || pinned == nil {
		t.Fatalf("readPin(%s) = %v, %v", plugin, pinned, err)
	}
	return *pinned
}

func digestOf(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
