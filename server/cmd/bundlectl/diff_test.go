package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/contentbundle"
)

// pinSpec is one plugin's pin for a diff test. The files need only pass the
// loader, not the Workflow parser.
type pinSpec struct {
	version string
	files   map[string][]byte
	retired []string
}

func workflowFiles(keys ...string) map[string][]byte {
	files := map[string][]byte{}
	for _, key := range keys {
		files[key+"/playbook.md"] = []byte("# " + key + "\n")
		files[key+"/workflow.yaml"] = []byte("key: " + key + "\n")
	}
	return files
}

// snapshotOf lays pins and a harness SKILL.md out as a pin directory holds
// them.
func snapshotOf(t *testing.T, pins map[string]pinSpec, harness string) pinSnapshot {
	t.Helper()
	snapshot := pinSnapshot{}
	for plugin, spec := range pins {
		artifact, err := contentbundle.Encode(contentbundle.ComponentTypeWorkflows, spec.files)
		if err != nil {
			t.Fatal(err)
		}
		retired := spec.retired
		if retired == nil {
			retired = []string{}
		}
		lock, err := contentbundle.MarshalLock(contentbundle.Lock{
			LockFormat:     1,
			Plugin:         plugin,
			Component:      contentbundle.ComponentTypeWorkflows,
			Version:        spec.version,
			Tag:            plugin + "--v" + spec.version,
			Commit:         strings.Repeat(spec.version[:1], 40),
			ManifestPath:   "plugins/" + plugin + "/.boardly-plugin/plugin.json",
			ManifestSHA256: strings.Repeat("b", 64),
			ContentPath:    "plugins/" + plugin + "/boardly/workflows",
			ContentTreeID:  strings.Repeat("c", 40),
			Artifact: contentbundle.Artifact{
				File:   plugin + contentbundle.ArtifactSuffix,
				SHA256: digestOf(artifact),
				Size:   int64(len(artifact)),
			},
			TreeHash:    contentbundle.TreeHash(spec.files),
			FileCount:   len(spec.files),
			RetiredKeys: retired,
		})
		if err != nil {
			t.Fatal(err)
		}
		snapshot[plugin+contentbundle.ArtifactSuffix] = artifact
		snapshot[plugin+contentbundle.LockSuffix] = lock
	}
	if harness != "" {
		snapshot["harness/zenith-workflows/SKILL.md"] = []byte(harness)
	}
	return snapshot
}

func writeSnapshot(t *testing.T, dir string, snapshot pinSnapshot) {
	t.Helper()
	for name, contents := range snapshot {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), contents)
	}
}

func TestDiffPinsComparesPluginsAndTheHarness(t *testing.T) {
	t.Parallel()
	changedFiles := workflowFiles("account-brief", "weekly-recap")
	changedFiles["account-brief/playbook.md"] = []byte("# account-brief\n\nNow with a checklist.\n")
	before := snapshotOf(t, map[string]pinSpec{
		"zenith-workflows": {
			version: "1.0.0",
			files:   workflowFiles("account-brief", "battle-card", "weekly-recap"),
		},
		"steady-plugin": {version: "2.0.0", files: workflowFiles("steady-flow")},
	}, "---\nname: zenith-workflows\n---\n")
	after := snapshotOf(t, map[string]pinSpec{
		"zenith-workflows": {
			version: "2.0.0", files: mergeFiles(changedFiles, workflowFiles("vendor-review")),
			retired: []string{"battle-card"},
		},
		"steady-plugin": {version: "2.0.0", files: workflowFiles("steady-flow")},
		"new-plugin":    {version: "1.0.0", files: workflowFiles("new-flow")},
	}, "---\nname: zenith-workflows\n---\n\nA new section.\n")

	difference, err := diffPins(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(difference.changes) != 2 || len(difference.unchanged) != 1 ||
		difference.unchanged[0].Plugin != "steady-plugin" {
		t.Fatalf("diffPins() = %d changes, unchanged %v", len(difference.changes), difference.unchanged)
	}
	added, workflows := difference.changes[0], difference.changes[1]
	if added.plugin != "new-plugin" || added.before != nil || !slices.Equal(added.added, []string{"new-flow"}) {
		t.Fatalf("new plugin change = %+v", added)
	}
	switch {
	case workflows.before.Version != "1.0.0" || workflows.after.Version != "2.0.0":
		t.Fatalf("versions %s -> %s", workflows.before.Version, workflows.after.Version)
	case !slices.Equal(workflows.added, []string{"vendor-review"}):
		t.Fatalf("added = %v", workflows.added)
	case !slices.Equal(workflows.changed, []string{"account-brief"}):
		t.Fatalf("changed = %v", workflows.changed)
	case !slices.Equal(workflows.removed, []string{"battle-card"}):
		t.Fatalf("removed = %v", workflows.removed)
	case !slices.Equal(workflows.newlyRetired, []string{"battle-card"}):
		t.Fatalf("newly retired = %v", workflows.newlyRetired)
	case len(difference.harness) != 1 || difference.harness[0] != harnessChange{"harness/zenith-workflows/SKILL.md", "changed"}:
		t.Fatalf("harness = %v", difference.harness)
	}

	var markdown, text bytes.Buffer
	writeDiffMarkdown(&markdown, "origin/main", difference)
	for _, want := range []string{
		"## Platform bundles against `origin/main`",
		"### zenith-workflows 1.0.0 → 2.0.0",
		"| Version | `1.0.0` | `2.0.0` |",
		"| Commit | `" + strings.Repeat("1", 40) + "` | `" + strings.Repeat("2", 40) + "` |",
		"- Added (1): `vendor-review`", "- Changed (1): `account-brief`", "- Removed (1): `battle-card`",
		"- Newly retired (1): `battle-card`",
		"### new-plugin none → 1.0.0", "| Version | — | `1.0.0` |",
		"Unchanged: `steady-plugin` 2.0.0.",
		"- `harness/zenith-workflows/SKILL.md` changed",
	} {
		if !strings.Contains(markdown.String(), want) {
			t.Fatalf("markdown lacks %q:\n%s", want, markdown.String())
		}
	}
	writeDiffText(&text, "origin/main", difference)
	for _, want := range []string{
		"zenith-workflows: 1.0.0 -> 2.0.0", "added (1)", "vendor-review",
		"new-plugin: none -> 1.0.0", "steady-plugin: unchanged at 2.0.0", "harness/zenith-workflows/SKILL.md: changed",
	} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text lacks %q:\n%s", want, text.String())
		}
	}

	same, err := diffPins(after, after)
	if err != nil || len(same.changes) != 0 || len(same.harness) != 0 {
		t.Fatalf("diffPins(same) = %+v, %v", same, err)
	}
	markdown.Reset()
	writeDiffMarkdown(&markdown, "HEAD", same)
	if !strings.Contains(markdown.String(), "No pinned bundle or harness file changed.") {
		t.Fatalf("markdown for no change:\n%s", markdown.String())
	}

	tampered := pinSnapshot{}
	for name, contents := range after {
		tampered[name] = contents
	}
	tampered["new-plugin"+contentbundle.ArtifactSuffix] = append(
		slices.Clone(tampered["new-plugin"+contentbundle.ArtifactSuffix]),
		0,
	)
	if _, err := diffPins(before, tampered); err == nil || !strings.Contains(err.Error(), "the pin after") {
		t.Fatalf("diffPins(tampered) error = %v", err)
	}
}

func mergeFiles(sets ...map[string][]byte) map[string][]byte {
	merged := map[string][]byte{}
	for _, set := range sets {
		for name, contents := range set {
			merged[name] = contents
		}
	}
	return merged
}

// diff reads the base side with git from the repository that holds the pin
// directory, and a revision from before the directory existed has no pin.
func TestDiffReadsTheBaseFromGit(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	out := filepath.Join(repo.dir, "server", "internal", "platformbundle")
	writeFile(t, filepath.Join(repo.dir, "README.md"), []byte("boardly\n"))
	repo.commit("before the pin")
	writeSnapshot(t, out, snapshotOf(t, map[string]pinSpec{
		"zenith-workflows": {version: "1.0.0", files: workflowFiles("account-brief", "battle-card")},
	}, "harness v1\n"))
	writeFile(t, filepath.Join(out, "platformbundle.go"), []byte("package platformbundle\n"))
	repo.commit("pin 1.0.0")
	writeSnapshot(t, out, snapshotOf(t, map[string]pinSpec{
		"zenith-workflows": {
			version: "1.1.0",
			files:   workflowFiles("account-brief", "battle-card", "vendor-review"),
		},
	}, "harness v1\n"))
	// //go:embed leaves these out, so they change nothing that ships.
	writeFile(t, filepath.Join(out, "harness", ".DS_Store"), []byte{0})
	writeFile(t, filepath.Join(out, "harness", "zenith-workflows", "_notes.md"), []byte("scratch\n"))

	stdout, stderr, code := runCommand(t, "diff", "--base", "HEAD", "--out", out, "--format", "markdown")
	if code != 0 {
		t.Fatalf("diff exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"### zenith-workflows 1.0.0 → 1.1.0", "- Added (1): `vendor-review`",
		"- Changed (0): none", "Harness: unchanged.",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("diff lacks %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, code = runCommand(t, "diff", "--base", "HEAD~1", "--out", out)
	if code != 0 {
		t.Fatalf("diff exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"zenith-workflows: none -> 1.1.0", "added (3)",
		"harness/zenith-workflows/SKILL.md: added",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("diff against a revision without a pin lacks %q:\n%s", want, stdout)
		}
	}

	if _, stderr, code := runCommand(t, "diff", "--base", "no-such-revision", "--out", out); code != 1 ||
		!strings.Contains(stderr, "has no revision no-such-revision") {
		t.Fatalf("diff against a missing revision exit %d\n%s", code, stderr)
	}
	for _, args := range [][]string{{"diff", "--out", out}, {"diff", "--base", "HEAD", "--format", "html", "--out", out}} {
		if _, _, code := runCommand(t, args...); code != 2 {
			t.Fatalf("bundlectl %q exit %d, want 2", args, code)
		}
	}
	// A pin that breaks the release rules against the base fails the diff,
	// however it was written.
	writeSnapshot(t, out, snapshotOf(t, map[string]pinSpec{
		"zenith-workflows": {version: "0.9.0", files: workflowFiles("account-brief", "battle-card")},
	}, "harness v1\n"))
	stdout, stderr, code = runCommand(t, "diff", "--base", "HEAD", "--out", out)
	if code != 1 || !strings.Contains(stdout, "problems:\n") ||
		!strings.Contains(stdout, "zenith-workflows 0.9.0 is lower than the pinned 1.0.0") ||
		!strings.Contains(stderr, "1 problem found") {
		t.Fatalf("diff of a pin that went back exit %d\n%s%s", code, stdout, stderr)
	}

	if err := os.Remove(filepath.Join(out, "zenith-workflows.lock.json")); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCommand(t, "diff", "--base", "HEAD", "--out", out); code != 1 ||
		!strings.Contains(stderr, "has no lock") {
		t.Fatalf("diff with a tar and no lock exit %d\n%s", code, stderr)
	}
}

// diff holds every changed plugin to the release rules: no lower version, a
// removed key retired in a MAJOR release, a retirement kept, the same version
// only for the same content, and no pinned plugin dropped.
func TestDiffPinsHoldsChangedPluginsToTheReleaseRules(t *testing.T) {
	t.Parallel()
	before := snapshotOf(t, map[string]pinSpec{
		"zenith-workflows": {
			version: "2.0.0", files: workflowFiles("account-brief", "battle-card"),
			retired: []string{"old-flow"},
		},
		"steady-plugin":  {version: "1.0.0", files: workflowFiles("steady-flow")},
		"dropped-plugin": {version: "1.0.0", files: workflowFiles("dropped-flow")},
	}, "")
	for name, test := range map[string]struct {
		after map[string]pinSpec
		want  []string
	}{
		"removed and unretired in a minor release": {
			after: map[string]pinSpec{
				"zenith-workflows": {version: "2.1.0", files: workflowFiles("account-brief")},
				"steady-plugin":    {version: "1.0.0", files: workflowFiles("steady-flow")},
			},
			want: []string{
				"takes a MAJOR release, and zenith-workflows 2.0.0 -> 2.1.0 is not one",
				`plugins/zenith-workflows/.boardly-plugin/retired-keys.json: Workflow "battle-card" was removed`,
				`retired key "old-flow" is no longer listed`,
				"dropped-plugin 1.0.0 is no longer pinned, so its 1 Workflows would disappear",
			},
		},
		"same version, other content": {
			after: map[string]pinSpec{
				"zenith-workflows": {
					version: "2.0.0", files: workflowFiles("account-brief", "battle-card", "vendor-review"),
					retired: []string{"old-flow"},
				},
				"steady-plugin":  {version: "1.0.0", files: workflowFiles("steady-flow")},
				"dropped-plugin": {version: "1.0.0", files: workflowFiles("dropped-flow")},
			},
			want: []string{"zenith-workflows 2.0.0 is already pinned from commit " + strings.Repeat("2", 40) +
				" with other content"},
		},
	} {
		difference, err := diffPins(before, snapshotOf(t, test.after, ""))
		if err != nil {
			t.Fatal(err)
		}
		var text bytes.Buffer
		writeDiffText(&text, "HEAD", difference)
		if len(difference.problems) != len(test.want) {
			t.Fatalf("%s: problems = %v, want %d", name, difference.problems, len(test.want))
		}
		for _, want := range test.want {
			if !strings.Contains(text.String(), want) {
				t.Fatalf("%s: diff lacks %q:\n%s", name, want, text.String())
			}
		}
	}

	retired := snapshotOf(t, map[string]pinSpec{
		"zenith-workflows": {
			version: "3.0.0", files: workflowFiles("account-brief"),
			retired: []string{"battle-card", "old-flow"},
		},
		"steady-plugin":  {version: "1.0.0", files: workflowFiles("steady-flow")},
		"dropped-plugin": {version: "1.0.0", files: workflowFiles("dropped-flow")},
	}, "")
	if difference, err := diffPins(before, retired); err != nil || len(difference.problems) != 0 {
		t.Fatalf("diffPins(a MAJOR retirement) problems = %v, %v", difference.problems, err)
	}
}
