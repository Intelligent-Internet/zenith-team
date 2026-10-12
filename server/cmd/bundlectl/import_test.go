package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/contentbundle"
)

const testPlugin = "zenith-workflows"

func TestImportPinsAReleaseTag(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := sampleWorkflows(t, 3)
	commit := repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	repo.tag(testPlugin + "--v1.0.0")
	out := t.TempDir()
	summaryPath := filepath.Join(t.TempDir(), "bundle-bump.md")

	stdout, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", repo.dir,
		"--out", out, "--summary", summaryPath)
	if code != 0 {
		t.Fatalf("import exit %d\n%s", code, stderr)
	}
	pinned := readPinned(t, out, testPlugin)
	lock := pinned.Lock
	manifest, err := os.ReadFile(filepath.Join(repo.dir, "plugins", testPlugin, ".boardly-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	contentTree := repo.git("rev-parse", testPlugin+"--v1.0.0:plugins/"+testPlugin+"/boardly/workflows")
	switch {
	case lock.Version != "1.0.0" || lock.Tag != testPlugin+"--v1.0.0":
		t.Fatalf("lock version %q, tag %q", lock.Version, lock.Tag)
	case lock.Commit != commit:
		t.Fatalf("lock commit %s, want %s", lock.Commit, commit)
	case lock.ContentTreeID != contentTree:
		t.Fatalf("lock contentTreeId %s, want git's %s", lock.ContentTreeID, contentTree)
	case lock.ManifestSHA256 != digestOf(manifest):
		t.Fatalf("lock manifestSha256 %s, want %s", lock.ManifestSHA256, digestOf(manifest))
	case lock.FileCount != len(files) || len(lock.RetiredKeys) != 0:
		t.Fatalf("lock fileCount %d, retiredKeys %v", lock.FileCount, lock.RetiredKeys)
	}
	for name, contents := range files {
		if got, err := fs.ReadFile(pinned.Files, name); err != nil || !bytes.Equal(got, contents) {
			t.Fatalf("pinned %s differs from the tag (%v)", name, err)
		}
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 2 {
		t.Fatalf("the pin directory holds %d entries (%v), want the tar and the lock", len(entries), err)
	}
	summary, err := os.ReadFile(summaryPath)
	if err != nil || string(summary) != stdout {
		t.Fatalf("--summary file differs from stdout (%v)", err)
	}
	for _, want := range append([]string{
		"### zenith-workflows none → 1.0.0", "`" + commit + "`",
		"`" + contentTree + "`", "`" + lock.Artifact.SHA256 + "`", "- Added (3)", "- Removed (0): none",
	}, keys...) {
		if !strings.Contains(stdout, want) {
			t.Fatalf("summary lacks %q:\n%s", want, stdout)
		}
	}
}

// A URL is cloned (partially, without a checkout) and reads the same bytes as
// the checkout read in place.
func TestImportClonesARepositoryURL(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.git("config", "uploadpack.allowFilter", "true")
	files, _ := sampleWorkflows(t, 2)
	repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	repo.tag(testPlugin + "--v1.0.0")
	inPlace, cloned := t.TempDir(), t.TempDir()
	for out, source := range map[string]string{inPlace: repo.dir, cloned: "file://" + repo.dir} {
		if _, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", source,
			"--out", out); code != 0 || strings.Contains(stderr, "could not prefetch") {
			t.Fatalf("import --repo %s exit %d\n%s", source, code, stderr)
		}
	}
	for _, name := range []string{testPlugin + ".bundle.tar", testPlugin + ".lock.json"} {
		left, leftErr := os.ReadFile(filepath.Join(inPlace, name))
		right, rightErr := os.ReadFile(filepath.Join(cloned, name))
		if leftErr != nil || rightErr != nil || !bytes.Equal(left, right) {
			t.Fatalf("%s differs between the checkout and the clone (%v, %v)", name, leftErr, rightErr)
		}
	}
}

func TestImportVerify(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := sampleWorkflows(t, 2)
	repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	repo.tag(testPlugin + "--v1.0.0")
	out := t.TempDir()
	if _, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", repo.dir,
		"--out", out); code != 0 {
		t.Fatalf("import exit %d\n%s", code, stderr)
	}
	stdout, stderr, code := runCommand(t, "import", "--verify", "--repo", repo.dir, "--out", out)
	if code != 0 || !strings.Contains(stdout, testPlugin+"--v1.0.0 verified") {
		t.Fatalf("verify exit %d\n%s%s", code, stdout, stderr)
	}

	artifactPath := filepath.Join(out, testPlugin+".bundle.tar")
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	artifact[len(artifact)/3] ^= 1
	writeFile(t, artifactPath, artifact)
	_, stderr, code = runCommand(t, "import", "--verify", "--repo", repo.dir, "--out", out)
	if code != 1 || !strings.Contains(stderr, "is not the artifact "+testPlugin+"--v1.0.0 produces") {
		t.Fatalf("verify of a changed tar exit %d\n%s", code, stderr)
	}

	// A tag that moved after the pin was taken no longer verifies either.
	artifact[len(artifact)/3] ^= 1
	writeFile(t, artifactPath, artifact)
	files[keys[0]+"/playbook.md"] = []byte("# Moved\n")
	repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	repo.git("tag", "-f", "-a", testPlugin+"--v1.0.0", "-m", "moved")
	_, stderr, code = runCommand(t, "import", "--verify", "--repo", repo.dir, "--out", out)
	if code != 1 || !strings.Contains(stderr, "does not match a fresh import") ||
		!strings.Contains(stderr, "commit is") {
		t.Fatalf("verify of a moved tag exit %d\n%s", code, stderr)
	}
}

func TestImportRefusesATagOffMain(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := sampleWorkflows(t, 2)
	repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	repo.git("checkout", "--quiet", "-b", "side")
	files[keys[0]+"/playbook.md"] = []byte("# Unreviewed\n")
	repo.commitPlugin(testPlugin, "1.1.0", files, nil)
	repo.tag(testPlugin + "--v1.1.0")
	repo.git("checkout", "--quiet", "main")
	out := t.TempDir()
	_, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.1.0", "--repo", repo.dir, "--out", out)
	if code != 1 || !strings.Contains(stderr, "is not reachable from refs/heads/main") {
		t.Fatalf("import of a tag off main exit %d\n%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(out, testPlugin+".lock.json")); err == nil {
		t.Fatal("a refused import wrote a lock")
	}
}

func TestImportRefusesAManifestVersionThatDisagreesWithTheTag(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, _ := sampleWorkflows(t, 2)
	repo.commitPlugin(testPlugin, "1.2.0", files, nil)
	repo.tag(testPlugin + "--v1.1.0")
	_, stderr, code := runCommand(
		t,
		"import",
		"--tag",
		testPlugin+"--v1.1.0",
		"--repo",
		repo.dir,
		"--out",
		t.TempDir(),
	)
	want := `plugins/zenith-workflows/.boardly-plugin/plugin.json: version "1.2.0" does not match the tag's version "1.1.0"`
	if code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("import exit %d, want %q\n%s", code, want, stderr)
	}
}

func TestImportRefusesSymlinksAndExecutables(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := sampleWorkflows(t, 3)
	writePlugin(t, repo.dir, testPlugin, "1.0.0", files, nil)
	content := filepath.Join(repo.dir, "plugins", testPlugin, "boardly", "workflows")
	if err := os.Remove(filepath.Join(content, keys[1], "playbook.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../"+keys[0]+"/playbook.md", filepath.Join(content, keys[1], "playbook.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(content, keys[2], "playbook.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.commit("symlink and executable")
	repo.tag(testPlugin + "--v1.0.0")
	_, stderr, code := runCommand(
		t,
		"import",
		"--tag",
		testPlugin+"--v1.0.0",
		"--repo",
		repo.dir,
		"--out",
		t.TempDir(),
	)
	for _, want := range []string{
		"plugins/zenith-workflows/boardly/workflows/" + keys[1] + "/playbook.md: is a symlink",
		"plugins/zenith-workflows/boardly/workflows/" + keys[2] + "/playbook.md: is executable (mode 100755)",
	} {
		if code != 1 || !strings.Contains(stderr, want) {
			t.Fatalf("import exit %d, want %q\n%s", code, want, stderr)
		}
	}
}

// Removing a Workflow takes a retirement; once retired, the key stays retired.
func TestImportRequiresRetiringARemovedKey(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := sampleWorkflows(t, 3)
	repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	repo.tag(testPlugin + "--v1.0.0")
	out := t.TempDir()
	if _, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", repo.dir,
		"--out", out); code != 0 {
		t.Fatalf("import 1.0.0 exit %d\n%s", code, stderr)
	}
	removed := keys[1]
	delete(files, removed+"/workflow.yaml")
	delete(files, removed+"/playbook.md")
	repo.commitPlugin(testPlugin, "2.0.0", files, nil)
	repo.tag(testPlugin + "--v2.0.0")
	_, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v2.0.0", "--repo", repo.dir, "--out", out)
	want := `plugins/zenith-workflows/.boardly-plugin/retired-keys.json: Workflow "` + removed +
		`" was removed but is not retired`
	if code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("import 2.0.0 exit %d, want %q\n%s", code, want, stderr)
	}
	if lock := readPinned(t, out, testPlugin).Lock; lock.Version != "1.0.0" {
		t.Fatalf("a refused import replaced the pin with %s", lock.Version)
	}

	repo.commitPlugin(testPlugin, "1.1.0", files, []string{removed})
	repo.tag(testPlugin + "--v1.1.0")
	_, stderr, code = runCommand(t, "import", "--tag", testPlugin+"--v1.1.0", "--repo", repo.dir, "--out", out)
	want = "removing or retiring a Workflow takes a MAJOR release, and zenith-workflows 1.0.0 -> 1.1.0 is not one"
	if code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("import 1.1.0 exit %d, want %q\n%s", code, want, stderr)
	}

	repo.commitPlugin(testPlugin, "3.0.0", files, []string{removed})
	repo.tag(testPlugin + "--v3.0.0")
	stdout, stderr, code := runCommand(
		t,
		"import",
		"--tag",
		testPlugin+"--v3.0.0",
		"--repo",
		repo.dir,
		"--out",
		out,
	)
	if code != 0 {
		t.Fatalf("import 3.0.0 exit %d\n%s", code, stderr)
	}
	if lock := readPinned(
		t,
		out,
		testPlugin,
	).Lock; lock.Version != "3.0.0" ||
		!slices.Equal(lock.RetiredKeys, []string{removed}) {
		t.Fatalf("pinned %s with retired keys %v", lock.Version, lock.RetiredKeys)
	}
	for _, want := range []string{
		"### zenith-workflows 1.0.0 → 3.0.0", "- Removed (1): `" + removed + "`",
		"- Newly retired (1): `" + removed + "`",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("summary lacks %q:\n%s", want, stdout)
		}
	}

	// Un-retiring the key is refused, and so is shipping it again.
	repo.commitPlugin(testPlugin, "4.0.0", files, []string{})
	repo.tag(testPlugin + "--v4.0.0")
	_, stderr, code = runCommand(t, "import", "--tag", testPlugin+"--v4.0.0", "--repo", repo.dir, "--out", out)
	if code != 1 || !strings.Contains(stderr, `retired key "`+removed+`" is no longer listed`) {
		t.Fatalf("import 4.0.0 exit %d\n%s", code, stderr)
	}
}

func TestImportRefusesALowerVersion(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := sampleWorkflows(t, 2)
	repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	repo.tag(testPlugin + "--v1.0.0")
	files[keys[0]+"/playbook.md"] = append(files[keys[0]+"/playbook.md"], "\nOne more line.\n"...)
	repo.commitPlugin(testPlugin, "1.1.0", files, nil)
	repo.git("tag", testPlugin+"--v1.1.0") // a lightweight tag resolves as an annotated one does
	out := t.TempDir()
	if _, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.1.0", "--repo", repo.dir,
		"--out", out); code != 0 {
		t.Fatalf("import 1.1.0 exit %d\n%s", code, stderr)
	}
	_, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", repo.dir, "--out", out)
	if code != 1 || !strings.Contains(stderr, "zenith-workflows 1.0.0 is lower than the pinned 1.1.0") {
		t.Fatalf("import of a lower version exit %d\n%s", code, stderr)
	}
	// Importing the pinned tag again is a no-op.
	stdout, stderr, code := runCommand(
		t,
		"import",
		"--tag",
		testPlugin+"--v1.1.0",
		"--repo",
		repo.dir,
		"--out",
		out,
	)
	if code != 0 || !strings.Contains(stdout, "1.1.0 → 1.1.0") || !strings.Contains(stdout, "- Changed (0)") {
		t.Fatalf("re-import exit %d\n%s%s", code, stdout, stderr)
	}
}

func TestImportRefusesAMissingTagAndBadUsage(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, _ := sampleWorkflows(t, 1)
	repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	out := t.TempDir()
	if _, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", repo.dir,
		"--out", out); code != 1 || !strings.Contains(stderr, "has no tag zenith-workflows--v1.0.0") {
		t.Fatalf("import of a missing tag exit %d\n%s", code, stderr)
	}
	for _, args := range [][]string{
		{"import", "--tag", "zenith-workflows-v1.0.0", "--out", out},
		{"import", "--tag", "Zenith--v1.0.0", "--out", out},
		{"import", "--tag", "zenith-workflows--v1.0", "--out", out},
		{"import", "--out", out},
		{"import", "--verify", "--tag", testPlugin + "--v1.0.0", "--out", out},
		{"import", "--dev", "--from", repo.dir, "--out", out},
		{"import", "--verify", "--plugin", testPlugin, "--out", out},
		{"import", "--tag", testPlugin + "--v1.0.0", "extra", "--out", out},
		{"import", "--unknown"},
		{"frobnicate"},
		{},
	} {
		if _, _, code := runCommand(t, args...); code != 2 {
			t.Fatalf("bundlectl %q exit %d, want 2", args, code)
		}
	}
	if _, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", repo.dir,
		"--out", filepath.Join(out, "missing")); code != 1 || !strings.Contains(stderr, "does not exist") {
		t.Fatalf("import into a missing directory exit %d\n%s", code, stderr)
	}
}

func TestImportDevPinsACheckout(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := sampleWorkflows(t, 2)
	head := repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	out := t.TempDir()

	_, stderr, code := runCommand(t, "import", "--dev", "--from", repo.dir, "--plugin", testPlugin, "--out", out)
	if code != 0 || !strings.Contains(stderr, "TestPinnedBundlesAreReleases") {
		t.Fatalf("import --dev exit %d\n%s", code, stderr)
	}
	lock := readPinned(t, out, testPlugin).Lock
	contentTree := repo.git("rev-parse", "HEAD:plugins/"+testPlugin+"/boardly/workflows")
	if !lock.Development() || lock.Version != "0.0.0-dev+"+head[:12] || lock.Tag != "" || lock.Commit != head ||
		lock.ContentTreeID != contentTree {
		t.Fatalf("clean development lock = %+v", lock)
	}

	edited := slices.Concat(files[keys[0]+"/playbook.md"], []byte("\nA local edit.\n"))
	writeFile(
		t,
		filepath.Join(repo.dir, "plugins", testPlugin, "boardly", "workflows", keys[0], "playbook.md"),
		edited,
	)
	stdout, stderr, code := runCommand(
		t,
		"import",
		"--dev",
		"--from",
		repo.dir,
		"--plugin",
		testPlugin,
		"--out",
		out,
	)
	if code != 0 {
		t.Fatalf("import --dev of a dirty checkout exit %d\n%s", code, stderr)
	}
	pinned := readPinned(t, out, testPlugin)
	if pinned.Lock.Version != "0.0.0-dev+"+head[:12]+".dirty" || pinned.Lock.ContentTreeID != "" {
		t.Fatalf("dirty development lock = %+v", pinned.Lock)
	}
	if got, err := fs.ReadFile(pinned.Files, keys[0]+"/playbook.md"); err != nil || !bytes.Equal(got, edited) {
		t.Fatalf("the dirty pin does not carry the edit (%v)", err)
	}
	if !strings.Contains(stdout, "- Changed (1): `"+keys[0]+"`") {
		t.Fatalf("summary does not name the edited Workflow:\n%s", stdout)
	}

	_, stderr, code = runCommand(t, "import", "--verify", "--repo", repo.dir, "--out", out)
	if code != 1 || !strings.Contains(stderr, "pins a development checkout (0.0.0-dev+"+head[:12]+".dirty)") {
		t.Fatalf("verify of a development pin exit %d\n%s", code, stderr)
	}
}

func TestImportDevReportsEveryProblemInTheCheckout(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := sampleWorkflows(t, 2)
	files["README.md"] = []byte("stray\n")
	files[keys[1]+"/playbook.md"] = []byte("# Title\r\n")
	repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	out := t.TempDir()
	_, stderr, code := runCommand(t, "import", "--dev", "--from", repo.dir, "--plugin", testPlugin, "--out", out)
	for _, want := range []string{
		"boardly/workflows/README.md: \"README.md\" is not <workflow-key>",
		"boardly/workflows/" + keys[1] + "/playbook.md: " + keys[1] + "/playbook.md has a CR byte",
	} {
		if code != 1 || !strings.Contains(stderr, want) {
			t.Fatalf("import --dev exit %d, want %q\n%s", code, want, stderr)
		}
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("a refused import wrote %d files", len(entries))
	}
}

// newPinRepo is a boardly repository in a temporary directory with an empty
// pin directory, so an import can compare with the release committed at HEAD.
func newPinRepo(t *testing.T) (*fixtureRepo, string) {
	t.Helper()
	repo := newFixtureRepo(t)
	out := filepath.Join(repo.dir, "server", "internal", "platformbundle")
	writeFile(t, filepath.Join(out, "platformbundle.go"), []byte("package platformbundle\n"))
	repo.commit("the pin directory")
	return repo, out
}

// A partial clone reads the plugin's blobs in one fetch rather than one
// request per file.
func TestPartialCloneFetchesThePluginInOneRequest(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.git("config", "uploadpack.allowFilter", "true")
	files, _ := sampleWorkflows(t, 2)
	commit := repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	var stderr bytes.Buffer
	clone, cleanup, err := openRepository(t.Context(), "file://"+repo.dir, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	missing := func() int {
		t.Helper()
		treeish := commit + ":plugins/" + testPlugin
		output, err := clone.git(t.Context(), "rev-list", "--objects", "--missing=print", treeish)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(output), "\n?")
	}
	if !clone.clone || missing() == 0 {
		t.Fatal("the clone is not partial, so the prefetch path is not exercised")
	}
	if err := clone.prefetch(t.Context(), commit+":plugins/"+testPlugin); err != nil {
		t.Fatalf("prefetch() error = %v", err)
	}
	if count := missing(); count != 0 {
		t.Fatalf("%d blobs are still missing after the prefetch", count)
	}
}

// A local checkout with an origin is held to what origin published: a release
// must be on origin's main, and its tag must be origin's.
func TestImportChecksALocalCheckoutAgainstItsOrigin(t *testing.T) {
	t.Parallel()
	upstream := newFixtureRepo(t)
	upstream.git("config", "receive.denyCurrentBranch", "updateInstead")
	files, keys := sampleWorkflows(t, 2)
	upstream.commitPlugin(testPlugin, "1.0.0", files, nil)
	upstream.tag(testPlugin + "--v1.0.0")
	local := &fixtureRepo{t: t, dir: filepath.Join(t.TempDir(), "boardly-plugins")}
	upstream.git("clone", "--quiet", upstream.dir, local.dir)
	for _, setting := range [][]string{
		{"user.name", "Bundle Fixture"},
		{"user.email", "fixture@example.com"},
		{"commit.gpgsign", "false"},
		{"tag.gpgsign", "false"},
	} {
		local.git(append([]string{"config"}, setting...)...)
	}
	if _, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", local.dir,
		"--out", t.TempDir()); code != 0 || strings.Contains(stderr, "has no origin remote") {
		t.Fatalf("import of a published tag exit %d\n%s", code, stderr)
	}

	files[keys[0]+"/playbook.md"] = append(slices.Clone(files[keys[0]+"/playbook.md"]), "\nUnpushed.\n"...)
	local.commitPlugin(testPlugin, "1.1.0", files, nil)
	local.tag(testPlugin + "--v1.1.0")
	_, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.1.0", "--repo", local.dir,
		"--out", t.TempDir())
	if code != 1 || !strings.Contains(stderr, "is not reachable from refs/remotes/origin/main") {
		t.Fatalf("import of an unpushed commit exit %d\n%s", code, stderr)
	}

	local.git("push", "--quiet", "origin", "main")
	local.git("fetch", "--quiet", "origin")
	_, stderr, code = runCommand(t, "import", "--tag", testPlugin+"--v1.1.0", "--repo", local.dir,
		"--out", t.TempDir())
	if code != 1 || !strings.Contains(stderr, "is not on "+local.dir+"'s origin remote") {
		t.Fatalf("import of an unpushed tag exit %d\n%s", code, stderr)
	}

	upstream.git("tag", "-a", testPlugin+"--v1.1.0", "-m", "published elsewhere", "main")
	_, stderr, code = runCommand(t, "import", "--tag", testPlugin+"--v1.1.0", "--repo", local.dir,
		"--out", t.TempDir())
	if code != 1 || !strings.Contains(stderr, "is not the one origin publishes") {
		t.Fatalf("import of a tag origin publishes differently exit %d\n%s", code, stderr)
	}

	local.git("push", "--quiet", "--force", "origin", "refs/tags/"+testPlugin+"--v1.1.0")
	if _, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.1.0", "--repo", local.dir,
		"--out", t.TempDir()); code != 0 {
		t.Fatalf("import of a pushed tag exit %d\n%s", code, stderr)
	}
}

// A plugin can retire only its own keys: retiring one the pinned catalog
// ships would break every later bump of the plugin that owns it.
func TestImportRefusesRetiringAKeyAnotherPluginShips(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := sampleWorkflows(t, 1)
	repo.commitPlugin("beta-plugin", "1.0.0", renameWorkflow(t, files, keys[0], "beta-flow"), []string{keys[0]})
	repo.tag("beta-plugin--v1.0.0")
	out := t.TempDir()
	_, stderr, code := runCommand(t, "import", "--tag", "beta-plugin--v1.0.0", "--repo", repo.dir, "--out", out)
	want := `plugins/beta-plugin/.boardly-plugin/retired-keys.json: retires Workflow "` + keys[0] +
		`", which plugin zenith-workflows ships`
	if code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("import exit %d, want %q\n%s", code, want, stderr)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("a refused import wrote %d files", len(entries))
	}
}

// Re-importing a pinned version from another commit is accepted only when
// the content is byte-identical, which is how a provisional commit gives way
// to the tagged one; other content under the same version is refused.
func TestImportRepinsAMovedCommitOnlyWithIdenticalContent(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := sampleWorkflows(t, 2)
	first := repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	repo.tag(testPlugin + "--v1.0.0")
	out := t.TempDir()
	if _, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", repo.dir,
		"--out", out); code != 0 {
		t.Fatalf("import exit %d\n%s", code, stderr)
	}
	artifact, err := os.ReadFile(filepath.Join(out, testPlugin+".bundle.tar"))
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(repo.dir, "README.md"), []byte("# boardly-plugins\n"))
	second := repo.commit("scaffold")
	repo.git("tag", "-f", "-a", testPlugin+"--v1.0.0", "-m", "retagged")
	stdout, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", repo.dir,
		"--out", out)
	want := "was pinned from commit " + first + "; its tag now names commit " + second
	if code != 0 || !strings.Contains(stderr, want) || !strings.Contains(stdout, "`"+second+"`") {
		t.Fatalf("re-import of a moved commit exit %d, want %q\n%s%s", code, want, stdout, stderr)
	}
	lock := readPinned(t, out, testPlugin).Lock
	if again, err := os.ReadFile(filepath.Join(out, testPlugin+".bundle.tar")); err != nil ||
		!bytes.Equal(again, artifact) || lock.Commit != second {
		t.Fatalf("the re-pin changed the tar or kept commit %s (%v)", lock.Commit, err)
	}

	files[keys[0]+"/playbook.md"] = []byte("# Rewritten\n")
	repo.commitPlugin(testPlugin, "1.0.0", files, nil)
	repo.git("tag", "-f", "-a", testPlugin+"--v1.0.0", "-m", "moved")
	_, stderr, code = runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", repo.dir, "--out", out)
	want = "zenith-workflows 1.0.0 is already pinned from commit " + second + " with other content"
	if code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("re-import with other content exit %d, want %q\n%s", code, want, stderr)
	}
}

// A development pin, or one that does not verify, is no reviewed release,
// so a bump over it is judged against the release committed at HEAD: a
// draft Workflow that only the development pin carried needs no retirement,
// and a broken pin is repaired by importing its tag again.
func TestImportJudgesABumpAgainstTheCommittedRelease(t *testing.T) {
	t.Parallel()
	boardly, out := newPinRepo(t)
	plugins := newFixtureRepo(t)
	files, keys := samplePlugin(t, 3)
	released := map[string][]byte{}
	for name, contents := range files {
		if !strings.HasPrefix(name, keys[2]+"/") {
			released[name] = contents
		}
	}
	plugins.commitPlugin(checkPlugin, "1.0.0", released, nil)
	plugins.tag(checkPlugin + "--v1.0.0")
	if _, stderr, code := runCommand(t, "import", "--tag", checkPlugin+"--v1.0.0", "--repo", plugins.dir,
		"--out", out); code != 0 {
		t.Fatalf("import 1.0.0 exit %d\n%s", code, stderr)
	}
	boardly.commit("pin 1.0.0")

	writePlugin(t, plugins.dir, checkPlugin, "1.0.0", files, nil)
	if _, stderr, code := runCommand(t, "import", "--dev", "--from", plugins.dir, "--plugin", checkPlugin,
		"--out", out); code != 0 {
		t.Fatalf("import --dev exit %d\n%s", code, stderr)
	}
	released[keys[0]+"/playbook.md"] = append(slices.Clone(released[keys[0]+"/playbook.md"]), "\nFixed.\n"...)
	plugins.commitPlugin(checkPlugin, "1.0.1", released, nil)
	plugins.tag(checkPlugin + "--v1.0.1")
	stdout, stderr, code := runCommand(t, "import", "--tag", checkPlugin+"--v1.0.1", "--repo", plugins.dir,
		"--out", out)
	if code != 0 || !strings.Contains(stderr, "judging the import against 1.0.0, committed at HEAD") ||
		!strings.Contains(stdout, "### sample-plugin 1.0.0 → 1.0.1") ||
		!strings.Contains(stdout, "- Removed (0)") {
		t.Fatalf("bump over a development pin exit %d\n%s%s", code, stdout, stderr)
	}
	boardly.commit("pin 1.0.1")
	committed, err := os.ReadFile(filepath.Join(out, checkPlugin+".bundle.tar"))
	if err != nil {
		t.Fatal(err)
	}

	broken := slices.Clone(committed)
	broken[len(broken)/2] ^= 1
	writeFile(t, filepath.Join(out, checkPlugin+".bundle.tar"), broken)
	_, stderr, code = runCommand(t, "import", "--tag", checkPlugin+"--v1.0.1", "--repo", plugins.dir, "--out", out)
	if code != 0 || !strings.Contains(stderr, "does not verify") {
		t.Fatalf("re-import over a broken pin exit %d\n%s", code, stderr)
	}
	if repaired, err := os.ReadFile(filepath.Join(out, checkPlugin+".bundle.tar")); err != nil ||
		!bytes.Equal(repaired, committed) {
		t.Fatalf("the re-import did not restore the committed tar (%v)", err)
	}

	// A committed development pin must not hide the preceding release or let
	// a Workflow disappear without retirement.
	writePlugin(t, plugins.dir, checkPlugin, "1.0.1", released, nil)
	if _, stderr, code := runCommand(t, "import", "--dev", "--from", plugins.dir, "--plugin", checkPlugin,
		"--out", out); code != 0 {
		t.Fatalf("import --dev exit %d\n%s", code, stderr)
	}
	boardly.commit("commit a development pin by mistake")
	dropped := map[string][]byte{}
	for name, contents := range released {
		if !strings.HasPrefix(name, keys[0]+"/") {
			dropped[name] = contents
		}
	}
	plugins.commitPlugin(checkPlugin, "2.0.0", dropped, nil)
	plugins.tag(checkPlugin + "--v2.0.0")
	_, stderr, code = runCommand(t, "import", "--tag", checkPlugin+"--v2.0.0", "--repo", plugins.dir, "--out", out)
	if code != 1 || !strings.Contains(stderr, "was removed but is not retired") {
		t.Fatalf("release import over a committed development pin exit %d\n%s", code, stderr)
	}
}

func TestImportPromotesCommittedManagedDevelopmentPins(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		previous bool
		shallow  bool
		corrupt  bool
		version  string
		want     string
	}{
		{name: "first release", version: "1.0.0"},
		{name: "existing release", previous: true, version: "1.1.0"},
		{name: "downgrade", previous: true, version: "0.9.0", want: "is lower than the pinned 1.0.0"},
		{name: "changed same version", previous: true, version: "1.0.0", want: "with other content"},
		{name: "incomplete history", shallow: true, version: "1.0.0", want: "shallow"},
		{name: "corrupt history", corrupt: true, version: "1.0.0", want: "release rules cannot be applied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			team := newFixtureRepo(t)
			out := filepath.Join(team.dir, "server", "internal", "daemon", "tool_releases")
			writeFile(t, filepath.Join(out, "README.md"), []byte("Managed Tool pins\n"))
			team.commit("empty Tool pins")
			plugins := newFixtureRepo(t)
			dir := writeManagedPlugin(t, plugins.dir)
			plugins.commit("Studio metadata")
			if test.previous {
				plugins.tag("zenith-studio--v1.0.0")
				if _, stderr, code := runCommand(t, "import", "--tag", "zenith-studio--v1.0.0",
					"--repo", plugins.dir, "--out", out); code != 0 {
					t.Fatalf("initial release import: %s", stderr)
				}
				team.commit("pin Studio release")
			}
			if test.corrupt {
				writeFile(t, filepath.Join(out, "zenith-studio.lock.json"), []byte("broken pin\n"))
				team.commit("corrupt Tool pin")
			}
			if _, stderr, code := runCommand(t, "import", "--dev", "--from", plugins.dir,
				"--plugin", "zenith-studio", "--out", out); code != 0 {
				t.Fatalf("development import: %s", stderr)
			}
			team.commit("development Tool pin")
			if test.shallow {
				clone := filepath.Join(t.TempDir(), "team")
				team.git("clone", "--quiet", "--depth=1", "file://"+filepath.ToSlash(team.dir), clone)
				out = filepath.Join(clone, "server", "internal", "daemon", "tool_releases")
			}
			pinPath := filepath.Join(out, "zenith-studio.lock.json")
			before, err := os.ReadFile(pinPath)
			if err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(dir, manifestPath)
			raw, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			raw = bytes.Replace(raw, []byte(`"version":"1.0.0"`), []byte(`"version":"`+test.version+`"`), 1)
			writeFile(
				t,
				manifest,
				bytes.Replace(raw, []byte("Studio tools"), []byte("Reviewed Studio tools"), 1),
			)
			plugins.commit("release Studio metadata")
			tag := "zenith-studio--v" + test.version
			if test.name == "changed same version" {
				plugins.git("tag", "-f", "-a", tag, "-m", tag)
			} else {
				plugins.tag(tag)
			}
			stdout, stderr, code := runCommand(
				t,
				"import",
				"--tag",
				tag,
				"--repo",
				plugins.dir,
				"--out",
				out,
			)
			if test.want != "" {
				if code != 1 || !strings.Contains(stderr, test.want) {
					t.Fatalf("import exit %d, want %q: %s%s", code, test.want, stdout, stderr)
				}
				after, err := os.ReadFile(pinPath)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("refused import changed the pin: %v", err)
				}
				return
			}
			if code != 0 {
				t.Fatalf("release import exit %d: %s%s", code, stdout, stderr)
			}
			pin := readPinned(t, out, "zenith-studio")
			if pin.Lock.Development() || pin.Lock.Tag != tag {
				t.Fatalf("promoted pin = %+v, want released %s", pin.Lock, tag)
			}
			if _, stderr, code := runCommand(
				t,
				"import",
				"--verify",
				"--repo",
				plugins.dir,
				"--out",
				out,
			); code != 0 {
				t.Fatalf("verify promoted pin: %s", stderr)
			}
		})
	}
}

// A development pin reads what git would commit: an ignored file in the
// checkout is neither pinned nor makes the pin dirty.
func TestImportDevLeavesOutIgnoredFiles(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, keys := samplePlugin(t, 3)
	committed := map[string][]byte{}
	for name, contents := range files {
		if !strings.HasPrefix(name, keys[2]+"/") {
			committed[name] = contents
		}
	}
	writeFile(t, filepath.Join(repo.dir, ".gitignore"), []byte(".DS_Store\n"+keys[2]+"/\n"))
	head := repo.commitPlugin(checkPlugin, "1.0.0", committed, nil)
	writePlugin(t, repo.dir, checkPlugin, "1.0.0", files, nil)
	writeFile(t, filepath.Join(repo.dir, "plugins", checkPlugin, "boardly", "workflows", ".DS_Store"), []byte{0})
	out := t.TempDir()
	if _, stderr, code := runCommand(t, "import", "--dev", "--from", repo.dir, "--plugin", checkPlugin,
		"--out", out); code != 0 {
		t.Fatalf("import --dev exit %d\n%s", code, stderr)
	}
	lock := readPinned(t, out, checkPlugin).Lock
	contentTree := repo.git("rev-parse", "HEAD:plugins/"+checkPlugin+"/boardly/workflows")
	if lock.Version != "0.0.0-dev+"+head[:12] || lock.FileCount != len(committed) ||
		lock.ContentTreeID != contentTree {
		t.Fatalf("development lock with ignored files = %+v, want HEAD's %d files and tree %s", lock,
			len(committed), contentTree)
	}
}

// Only the pin being replaced may fail to verify; any other still stops the
// import.
func TestSplitPinnedSetsAsideOnlyThePinBeingReplaced(t *testing.T) {
	t.Parallel()
	snapshot := snapshotOf(t, map[string]pinSpec{
		"alpha-plugin": {version: "1.0.0", files: workflowFiles("alpha-flow")},
		"beta-plugin":  {version: "1.0.0", files: workflowFiles("beta-flow")},
	}, "")
	snapshot["beta-plugin.bundle.tar"] = append(slices.Clone(snapshot["beta-plugin.bundle.tar"]), 0)
	bundles, err := contentbundle.LoadFS(contentbundle.NewFS(snapshot))
	if err == nil || len(bundles) != 1 {
		t.Fatalf("LoadFS() = %d bundles, %v", len(bundles), err)
	}
	others, own, problems := splitPinned(bundles, err, []string{"beta-plugin"})
	if len(problems) != 0 || len(others) != 1 || others[0].Lock.Plugin != "alpha-plugin" ||
		own["beta-plugin"] != nil {
		t.Fatalf("replacing beta-plugin: others %d, own %v, problems %v", len(others), own, problems)
	}
	others, own, problems = splitPinned(bundles, err, []string{"alpha-plugin"})
	if len(problems) != 1 || !strings.Contains(problems[0].message, "beta-plugin.lock.json") || len(others) != 0 ||
		own["alpha-plugin"] == nil {
		t.Fatalf("replacing alpha-plugin: others %d, own %v, problems %v", len(others), own, problems)
	}
}

func TestCompareVersionsOrdersAnyNumberOfDigits(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.10.0", "1.9.0", 1},
		{"1.0.18446744073709551616", "1.0.18446744073709551615", 1},
		{"2.0.0", "10.0.0", -1},
	} {
		if got := compareVersions(test.left, test.right); got != test.want {
			t.Fatalf("compareVersions(%s, %s) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestGitErrorsLeaveOutURLCredentials(t *testing.T) {
	t.Parallel()
	url := "https://maintainer:ghp_secret@github.com/Intelligent-Internet/boardly-plugins.git"
	err := &gitError{
		args:   []string{"clone", "--", url, "/tmp/clone"},
		err:    errors.New("exit status 128"),
		stderr: "fatal: repository '" + url + "' not found",
	}
	if message := err.Error(); strings.Contains(message, "ghp_secret") || strings.Contains(message, "maintainer") ||
		!strings.Contains(message, "https://github.com/Intelligent-Internet/boardly-plugins.git") {
		t.Fatalf("gitError = %q", message)
	}
	// A URL url.Parse rejects, such as one with a stray % escape, is still
	// redacted.
	for _, bad := range []string{"https://user:s3cr%zz@127.0.0.1:9/x.git", "https://user:s3cret@127.0.0.1:9/a%zz/x.git"} {
		if got := redactURL(bad); strings.Contains(got, "s3cr") || strings.Contains(got, "user") {
			t.Fatalf("redactURL(%q) = %q", bad, got)
		}
	}
	for _, arg := range []string{"--quiet", "git@github.com:org/repo.git", "/tmp/plugins", "refs/tags/x--v1.0.0"} {
		if redactURL(arg) != arg {
			t.Fatalf("redactURL(%q) = %q", arg, redactURL(arg))
		}
	}
}

// A Boardly folder that is a symlink at the tag is named as one, rather than
// read as an empty catalog.
func TestImportRefusesASymlinkedContentFolder(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	files, _ := sampleWorkflows(t, 1)
	writePlugin(t, repo.dir, testPlugin, "1.0.0", files, nil)
	plugin := filepath.Join(repo.dir, "plugins", testPlugin)
	content := filepath.Join(plugin, "boardly", "workflows")
	if err := os.Rename(content, filepath.Join(plugin, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../elsewhere", content); err != nil {
		t.Fatal(err)
	}
	repo.commit("symlinked content")
	repo.tag(testPlugin + "--v1.0.0")
	_, stderr, code := runCommand(t, "import", "--tag", testPlugin+"--v1.0.0", "--repo", repo.dir,
		"--out", t.TempDir())
	want := "plugins/zenith-workflows/boardly/workflows: has git mode 120000; a plugin's Boardly folders are " +
		"real directories"
	if code != 1 || !strings.Contains(stderr, want) || strings.Contains(stderr, "is missing") ||
		strings.Contains(stderr, "catalog is empty") {
		t.Fatalf("import exit %d, want %q\n%s", code, want, stderr)
	}
}
