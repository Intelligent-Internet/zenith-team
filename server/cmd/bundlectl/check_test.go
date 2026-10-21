package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"

	"github.com/Intelligent-Internet/zenith-team/server/internal/platformbundle"
)

// unpackPin returns the files and the lock of the pinned zenith-workflows
// bundle.
func unpackPin(t *testing.T) (map[string][]byte, contentbundle.Lock) {
	t.Helper()
	bundles, err := platformbundle.Load()
	if err != nil {
		t.Fatal(err)
	}
	var pinned *contentbundle.Bundle
	for index := range bundles {
		if bundles[index].Lock.Plugin == testPlugin {
			pinned = &bundles[index]
		}
	}
	if pinned == nil {
		t.Fatalf("%s is not pinned", testPlugin)
	}
	files := map[string][]byte{}
	if err := fs.WalkDir(pinned.Files, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		files[name], err = fs.ReadFile(pinned.Files, name)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files, pinned.Lock
}

// The pinned bundle, unpacked into a plugin folder, passes check and builds
// into the very artifact the server loads: the validator and production
// apply the same rules.
func TestCheckAcceptsThePinnedBundle(t *testing.T) {
	t.Parallel()
	files, lock := unpackPin(t)
	pinned := &contentbundle.Bundle{Lock: lock}
	dir := writePlugin(t, t.TempDir(), testPlugin, pinned.Lock.Version, files, pinned.Lock.RetiredKeys)

	stdout, stderr, code := runCommand(t, "check", dir)
	want := fmt.Sprintf("ok %s: %s %s, %d Workflows in %d files", filepath.ToSlash(dir), testPlugin,
		pinned.Lock.Version, pinned.Lock.FileCount/2, pinned.Lock.FileCount)
	if code != 0 || !strings.Contains(stdout, want) {
		t.Fatalf("check exit %d, want %q\n%s%s", code, want, stdout, stderr)
	}
	source := readPluginDir(t.Context(), dir, filepath.ToSlash(dir))
	source.identity = pinned.Lock
	components, problems := buildPlugins([]pluginSource{source}, nil)
	if len(problems) != 0 || len(components) != 1 {
		t.Fatalf("buildPlugins() problems = %v", problems)
	}
	if built := components[0].bundle.Lock; built.Artifact != pinned.Lock.Artifact ||
		built.TreeHash != pinned.Lock.TreeHash {
		t.Fatalf("the unpacked plugin builds %+v, the pin is %+v", built.Artifact, pinned.Lock.Artifact)
	}
}

// A folder of a plugin this build pins is held to the release rules against
// that pin: a Workflow it drops must be retired, and its version never goes
// back. Its version may still equal the pinned one while it is unreleased.
func TestCheckHoldsAPinnedPluginToTheReleaseRules(t *testing.T) {
	t.Parallel()
	files, lock := unpackPin(t)
	dropped := "account-brief"
	if _, ok := files[dropped+"/playbook.md"]; !ok {
		t.Fatalf("the pin no longer ships %s; pick another Workflow", dropped)
	}
	delete(files, dropped+"/playbook.md")
	delete(files, dropped+"/workflow.yaml")
	root := t.TempDir()

	dir := writePlugin(t, root, testPlugin, lock.Version, files, []string{})
	stdout, _, code := runCommand(t, "check", dir)
	want := `Workflow "` + dropped + `" was removed but is not retired`
	if code != 1 || !hasProblem(stdout, filepath.ToSlash(dir)+"/.boardly-plugin/retired-keys.json", want) {
		t.Fatalf("check of a dropped Workflow exit %d, want %q\n%s", code, want, stdout)
	}

	writePlugin(t, root, testPlugin, lock.Version, files, []string{dropped})
	if stdout, stderr, code := runCommand(t, "check", dir); code != 0 {
		t.Fatalf("check of a retired Workflow exit %d\n%s%s", code, stdout, stderr)
	}

	writePlugin(t, root, testPlugin, "0.9.0", files, []string{dropped})
	stdout, _, code = runCommand(t, "check", dir)
	want = "zenith-workflows 0.9.0 is lower than the pinned " + lock.Version
	if code != 1 || !hasProblem(stdout, filepath.ToSlash(dir)+"/.boardly-plugin/plugin.json", want) {
		t.Fatalf("check of a lower version exit %d, want %q\n%s", code, want, stdout)
	}
}

// Inside a git work tree, check reads what a commit would carry, so a file
// git ignores, such as a Finder .DS_Store, is no problem; one it would commit
// still is.
func TestCheckLeavesOutFilesGitIgnores(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := samplePlugin(t, 2)
	dir := writePlugin(t, repo.dir, checkPlugin, "1.0.0", files, []string{})
	writeFile(t, filepath.Join(repo.dir, ".gitignore"), []byte(".DS_Store\n"))
	writeFile(t, contentPath(dir, ".DS_Store"), []byte("\x00\x01"))
	writeFile(t, contentPath(dir, keys[0], ".DS_Store"), []byte("\x00\x01"))
	if stdout, stderr, code := runCommand(t, "check", dir); code != 0 {
		t.Fatalf("check with ignored files exit %d\n%s%s", code, stdout, stderr)
	}
	writeFile(t, contentPath(dir, keys[0], "notes.md"), []byte("# Notes\n"))
	stdout, _, code := runCommand(t, "check", dir)
	want := `"` + keys[0] + `/notes.md" is not <workflow-key>`
	if code != 1 || !hasProblem(stdout, filepath.ToSlash(dir)+"/boardly/workflows/"+keys[0]+"/notes.md", want) {
		t.Fatalf("check with an untracked stray file exit %d, want %q\n%s", code, want, stdout)
	}
}

// checkPlugin is the plugin the check cases build: not one this build pins,
// so only the problem each case plants is reported.
const checkPlugin = "sample-plugin"

// samplePlugin copies the first count pinned Workflows under new keys and
// names, so a plugin other than zenith-workflows can ship them.
func samplePlugin(t *testing.T, count int) (map[string][]byte, []string) {
	t.Helper()
	pinned, pinnedKeys := sampleWorkflows(t, count)
	files := map[string][]byte{}
	keys := make([]string, 0, count)
	for index, key := range pinnedKeys {
		renamed := fmt.Sprintf("sample-flow-%d", index+1)
		keys = append(keys, renamed)
		for name, contents := range renameWorkflow(t, pinned, key, renamed) {
			files[name] = contents
		}
	}
	return files, keys
}

// checkCase breaks a valid three-Workflow plugin in one way and names the
// problem check must report: the file (inside the plugin) and the message.
type checkCase struct {
	name    string
	breakIt func(t *testing.T, dir string, keys []string)
	file    func(keys []string) string
	message func(keys []string) string
}

func contentPath(dir string, parts ...string) string {
	return filepath.Join(append([]string{dir, "boardly", "workflows"}, parts...)...)
}

func rewrite(t *testing.T, name string, edit func([]byte) []byte) {
	t.Helper()
	contents, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, name, edit(contents))
}

func constant(value string) func([]string) string { return func([]string) string { return value } }

var checkCases = []checkCase{
	{
		name: "dot file",
		breakIt: func(t *testing.T, dir string, _ []string) {
			t.Helper()
			writeFile(t, contentPath(dir, ".DS_Store"), []byte("\x00\x01"))
		},
		file:    constant("boardly/workflows/.DS_Store"),
		message: constant(`".DS_Store" is not <workflow-key>/workflow.yaml or <workflow-key>/playbook.md`),
	},
	{
		name: "CRLF",
		breakIt: func(t *testing.T, dir string, keys []string) {
			t.Helper()
			rewrite(t, contentPath(dir, keys[0], "playbook.md"), func(contents []byte) []byte {
				return bytes.ReplaceAll(contents, []byte("\n"), []byte("\r\n"))
			})
		},
		file:    func(keys []string) string { return "boardly/workflows/" + keys[0] + "/playbook.md" },
		message: func(keys []string) string { return keys[0] + "/playbook.md has a CR byte; content is LF only" },
	},
	{
		name: "BOM",
		breakIt: func(t *testing.T, dir string, keys []string) {
			t.Helper()
			rewrite(t, contentPath(dir, keys[1], "workflow.yaml"), func(contents []byte) []byte {
				return append([]byte("\xef\xbb\xbf"), contents...)
			})
		},
		file:    func(keys []string) string { return "boardly/workflows/" + keys[1] + "/workflow.yaml" },
		message: func(keys []string) string { return keys[1] + "/workflow.yaml starts with a byte order mark" },
	},
	{
		name: "unknown icon",
		breakIt: func(t *testing.T, dir string, keys []string) {
			t.Helper()
			rewrite(t, contentPath(dir, keys[2], "workflow.yaml"), func(contents []byte) []byte {
				return regexp.MustCompile(`(?m)^icon: .*$`).
					ReplaceAll(contents, []byte("icon: no-such-icon"))
			})
		},
		file: func(keys []string) string { return "boardly/workflows/" + keys[2] + "/workflow.yaml" },
		message: func(keys []string) string {
			return `workflow "` + keys[2] + `": icon "no-such-icon" is not a bundled workflow icon`
		},
	},
	{
		name: "missing playbook",
		breakIt: func(t *testing.T, dir string, keys []string) {
			t.Helper()
			if err := os.Remove(contentPath(dir, keys[1], "playbook.md")); err != nil {
				t.Fatal(err)
			}
		},
		file: func(keys []string) string { return "boardly/workflows/" + keys[1] },
		message: func(keys []string) string {
			return `workflow "` + keys[1] + `": directory must hold exactly playbook.md and workflow.yaml`
		},
	},
	{
		name: "stray root file",
		breakIt: func(t *testing.T, dir string, _ []string) {
			t.Helper()
			writeFile(t, contentPath(dir, "README.md"), []byte("# Workflows\n"))
		},
		file:    constant("boardly/workflows/README.md"),
		message: constant(`"README.md" is not <workflow-key>`),
	},
	{
		name: "retired key reused",
		breakIt: func(t *testing.T, dir string, keys []string) {
			t.Helper()
			writeFile(
				t,
				filepath.Join(dir, ".boardly-plugin", "retired-keys.json"),
				retiredKeysFor(keys[0]),
			)
		},
		file: func(keys []string) string { return "boardly/workflows/" + keys[0] },
		message: func(keys []string) string {
			return `Workflow "` + keys[0] + `" is listed in .boardly-plugin/retired-keys.json; a retired key is never reused`
		},
	},
	{
		name: "symlink",
		breakIt: func(t *testing.T, dir string, keys []string) {
			t.Helper()
			if err := os.Symlink("playbook.md", contentPath(dir, keys[0], "notes.md")); err != nil {
				t.Fatal(err)
			}
		},
		file:    func(keys []string) string { return "boardly/workflows/" + keys[0] + "/notes.md" },
		message: constant("is a symlink; plugin files are regular files"),
	},
	{
		name: "executable",
		breakIt: func(t *testing.T, dir string, keys []string) {
			t.Helper()
			if err := os.Chmod(contentPath(dir, keys[2], "playbook.md"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		file:    func(keys []string) string { return "boardly/workflows/" + keys[2] + "/playbook.md" },
		message: constant("is executable; plugin files are mode 644"),
	},
	{
		name: "executable manifest",
		breakIt: func(t *testing.T, dir string, _ []string) {
			t.Helper()
			if err := os.Chmod(filepath.Join(dir, ".boardly-plugin", "plugin.json"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		file:    constant(".boardly-plugin/plugin.json"),
		message: constant("is executable; plugin files are mode 644"),
	},
	{
		name: "manifest field in another case",
		breakIt: func(t *testing.T, dir string, _ []string) {
			t.Helper()
			rewrite(t, filepath.Join(dir, ".boardly-plugin", "plugin.json"), func(contents []byte) []byte {
				return bytes.Replace(contents, []byte(`"version": "1.0.0",`),
					[]byte(`"version": "1.0.0", "Version": "9.9.9",`), 1)
			})
		},
		file:    constant(".boardly-plugin/plugin.json"),
		message: constant(`json: unknown field "Version"; field names are case-sensitive`),
	},
	{
		name: "unknown manifest field",
		breakIt: func(t *testing.T, dir string, _ []string) {
			t.Helper()
			rewrite(t, filepath.Join(dir, ".boardly-plugin", "plugin.json"), func(contents []byte) []byte {
				return bytes.Replace(
					contents,
					[]byte(`"format": 1,`),
					[]byte(`"format": 1, "homepage": "x",`),
					1,
				)
			})
		},
		file:    constant(".boardly-plugin/plugin.json"),
		message: constant(`json: unknown field "homepage"`),
	},
	{
		name: "manifest names another plugin",
		breakIt: func(t *testing.T, dir string, _ []string) {
			t.Helper()
			writeFile(
				t,
				filepath.Join(dir, ".boardly-plugin", "plugin.json"),
				manifestFor("other-plugin", "1.0.0"),
			)
		},
		file:    constant(".boardly-plugin/plugin.json"),
		message: constant(`name "other-plugin" must be the plugin's name "sample-plugin"`),
	},
	{
		name: "unsorted retired keys",
		breakIt: func(t *testing.T, dir string, _ []string) {
			t.Helper()
			writeFile(
				t,
				filepath.Join(dir, ".boardly-plugin", "retired-keys.json"),
				retiredKeysFor("b-key", "a-key"),
			)
		},
		file:    constant(".boardly-plugin/retired-keys.json"),
		message: constant("retiredKeys must be sorted and unique"),
	},
}

func TestCheckRejects(t *testing.T) {
	t.Parallel()
	files, keys := samplePlugin(t, 3)
	for _, test := range checkCases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := writePlugin(t, t.TempDir(), checkPlugin, "1.0.0", files, []string{})
			test.breakIt(t, dir, keys)
			stdout, stderr, code := runCommand(t, "check", dir)
			want := filepath.ToSlash(dir) + "/" + test.file(keys)
			if code != 1 || !hasProblem(stdout, want, test.message(keys)) {
				t.Fatalf(
					"check exit %d, want a problem at %s: %s\n%s%s",
					code,
					want,
					test.message(keys),
					stdout,
					stderr,
				)
			}
		})
	}
}

// hasProblem reports whether text output names the file (with or without a
// line) and the message on one line.
func hasProblem(output, file, message string) bool {
	for line := range strings.SplitSeq(output, "\n") {
		if (strings.HasPrefix(line, file+": ") || strings.HasPrefix(line, file+":")) &&
			strings.Contains(line, message) {
			return true
		}
	}
	return false
}

// Every problem in a plugin is reported in one run, and --format github
// annotates each with its file and, when known, its line.
func TestCheckReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()
	files, keys := samplePlugin(t, 3)
	root := t.TempDir()
	dir := writePlugin(t, root, checkPlugin, "1.0.0", files, []string{})
	form := contentPath(dir, keys[0], "workflow.yaml")
	extraLine := bytes.Count(files[keys[0]+"/workflow.yaml"], []byte("\n")) + 1
	rewrite(t, form, func(contents []byte) []byte { return append(contents, "extra: 1\n"...) })
	var combined []checkCase
	for _, test := range checkCases {
		switch test.name {
		case "dot file", "CRLF", "BOM", "unknown icon", "stray root file", "symlink", "unknown manifest field":
			test.breakIt(t, dir, keys)
			combined = append(combined, test)
		}
	}

	stdout, stderr, code := runCommand(t, "check", dir)
	if code != 1 || !strings.Contains(stderr, fmt.Sprintf("%d problems found", len(combined)+1)) {
		t.Fatalf("check exit %d, want %d problems\n%s%s", code, len(combined)+1, stdout, stderr)
	}
	for _, test := range combined {
		if !hasProblem(stdout, filepath.ToSlash(dir)+"/"+test.file(keys), test.message(keys)) {
			t.Fatalf(
				"check did not report %q (%s) alongside the others:\n%s",
				test.name,
				test.message(keys),
				stdout,
			)
		}
	}

	// Run from the repository root with a relative folder, as a plugins
	// pull request does, so annotations carry repository paths.
	relative, err := filepath.Rel(mustGetwd(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runCommand(t, "check", "--format", "github", relative)
	annotation := fmt.Sprintf(
		"::error file=%s/boardly/workflows/%s/workflow.yaml,line=%d::workflow \"%s\": workflow.yaml: line %d: field extra not found in type workflow.document",
		filepath.ToSlash(relative),
		keys[0],
		extraLine,
		keys[0],
		extraLine,
	)
	if code != 1 || !strings.Contains(stdout, annotation+"\n") {
		t.Fatalf("github annotations lack %q:\n%s", annotation, stdout)
	}
	iconLine := regexp.MustCompile(
		`::error file=[^,]*/` + keys[2] + `/workflow\.yaml,line=\d+::workflow "` + keys[2] +
			`": icon "no-such-icon"`,
	)
	if !iconLine.MatchString(stdout) {
		t.Fatalf("the unknown icon is not annotated at its line:\n%s", stdout)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
		if !strings.HasPrefix(line, "::error ") {
			t.Fatalf("github output line %q is not an annotation", line)
		}
	}
}

// A second plugin can neither ship a key another plugin pins, nor reuse a key
// one of them retired, nor retire a key another one ships.
func TestCheckRefusesAKeyAnotherPluginShips(t *testing.T) {
	t.Parallel()
	files, keys := sampleWorkflows(t, 1)
	dir := writePlugin(t, t.TempDir(), "sample-plugin", "1.0.0", files, []string{})
	stdout, _, code := runCommand(t, "check", dir)
	want := `Workflow "` + keys[0] + `" is also shipped by plugin zenith-workflows; keys are unique across plugins`
	if code != 1 || !hasProblem(stdout, filepath.ToSlash(dir)+"/boardly/workflows/"+keys[0], want) {
		t.Fatalf("check exit %d, want %q\n%s", code, want, stdout)
	}

	root := t.TempDir()
	first := writePlugin(t, root, "alpha-plugin", "1.0.0", renameWorkflow(t, files, keys[0], "alpha-flow"),
		[]string{"retired-flow"})
	second := writePlugin(t, root, "beta-plugin", "1.0.0", renameWorkflow(t, files, keys[0], "retired-flow"),
		[]string{})
	if stdout, stderr, code := runCommand(t, "check", first); code != 0 {
		t.Fatalf("check of a renamed copy exit %d\n%s%s", code, stdout, stderr)
	}
	// Which plugin is wrong depends on history check cannot see, so it
	// reports the conflict at both ends and nothing else.
	stdout, _, code = runCommand(t, "check", first, second)
	want = `Workflow "retired-flow" is retired by plugin alpha-plugin; a retired key is never reused`
	retires := `retires Workflow "retired-flow", which plugin beta-plugin ships; a plugin retires only its own keys`
	if code != 1 || !hasProblem(stdout, filepath.ToSlash(second)+"/boardly/workflows/retired-flow", want) ||
		!hasProblem(stdout, filepath.ToSlash(first)+"/.boardly-plugin/retired-keys.json", retires) ||
		strings.Count(strings.TrimSpace(stdout), "\n") != 1 {
		t.Fatalf("check exit %d, want only %q and %q\n%s", code, want, retires, stdout)
	}

	// Nor can a plugin retire a key the pinned catalog still ships, which
	// would break every later bump of the plugin that owns it.
	squatter := writePlugin(t, t.TempDir(), "beta-plugin", "1.0.0",
		renameWorkflow(t, files, keys[0], "beta-flow"), []string{keys[0]})
	stdout, _, code = runCommand(t, "check", squatter)
	want = `retires Workflow "` + keys[0] + `", which plugin zenith-workflows ships`
	if code != 1 || !hasProblem(stdout, filepath.ToSlash(squatter)+"/.boardly-plugin/retired-keys.json", want) {
		t.Fatalf("check of a plugin retiring a pinned key exit %d, want %q\n%s", code, want, stdout)
	}
}

// renameWorkflow copies one Workflow under a new key and a new name, so it
// can ship beside the pinned catalog.
func renameWorkflow(t *testing.T, files map[string][]byte, key, renamed string) map[string][]byte {
	t.Helper()
	form := files[key+"/workflow.yaml"]
	form = regexp.MustCompile(`(?m)^key: .*$`).ReplaceAll(form, []byte("key: "+renamed))
	form = regexp.MustCompile(`(?m)^name: (.*)$`).ReplaceAll(form, []byte("name: $1 ("+renamed+")"))
	return map[string][]byte{renamed + "/workflow.yaml": form, renamed + "/playbook.md": files[key+"/playbook.md"]}
}

func TestCheckUsage(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"check"}, {"check", "--format", "json", "."}, {"check", ".", "--format", "github"},
	} {
		if _, _, code := runCommand(t, args...); code != 2 {
			t.Fatalf("bundlectl %q exit %d, want 2", args, code)
		}
	}
	if _, _, code := runCommand(t, "check", filepath.Join(t.TempDir(), "zenith-workflows")); code != 1 {
		t.Fatalf("check of a missing folder exit %d, want 1", code)
	}
}

func TestGitHubAnnotationsEscapeTheirFields(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	printProblems(&output, "github", []problem{
		{file: "plugins/a,b:c/x.yaml", message: "100% wrong\nsecond line: yaml: line 7: bad"},
		{message: "no file"},
	})
	want := "::error file=plugins/a%2Cb%3Ac/x.yaml,line=7::100%25 wrong%0Asecond line: yaml: line 7: bad\n::error::no file\n"
	if output.String() != want {
		t.Fatalf("annotations = %q, want %q", output.String(), want)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// The manifest and the retired keys are read strictly: encoding/json alone
// would take a field in another case, or let a repeated one win, and another
// reader of the file could then see a different manifest.
func TestDecodeStrictRequiresExactFieldNames(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		`{"retiredKeys": [], "RETIREDKEYS": ["zzz"]}`:       `unknown field "RETIREDKEYS"`,
		`{"retiredKeys": [], "retiredKeys": ["zzz"]}`:       `field "retiredKeys" appears more than once`,
		`{"retiredKeys": ["a-key"], "homepage": "x"}`:       `unknown field "homepage"`,
		"{\n  \"retiredKeys\": [\n    \"a-key\",\n  ]\n}\n": "line 4",
	} {
		var file retiredKeysFile
		if err := contentbundle.DecodeJSON(
			[]byte(raw),
			&file,
		); err == nil ||
			!strings.Contains(err.Error(), want) {
			t.Fatalf("contentbundle.DecodeJSON(%s) error = %v, want %q", raw, err, want)
		}
	}
	var manifest pluginManifest
	raw := manifestFor("sample-plugin", "1.0.0")
	if err := contentbundle.DecodeJSON(
		raw,
		&manifest,
	); err != nil ||
		manifest.Workflows.Path != "./boardly/workflows" {
		t.Fatalf("contentbundle.DecodeJSON(manifest) = %+v, %v", manifest, err)
	}
	nested := bytes.Replace(raw, []byte(`"path"`), []byte(`"PATH"`), 1)
	if err := contentbundle.DecodeJSON(nested, &manifest); err == nil ||
		!strings.Contains(err.Error(), `unknown field "workflows.PATH"`) {
		t.Fatalf("contentbundle.DecodeJSON(nested case) error = %v", err)
	}
}
