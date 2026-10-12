package daemon

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHyperframesIsPinnedWithoutAnAuthoringSkill(t *testing.T) {
	t.Parallel()
	renderer, ok := findToolRelease("hyperframes")
	if !ok || renderer.Version != "0.8.36" || renderer.PackageName != "hyperframes" || renderer.Integrity == "" {
		t.Fatalf("renderer = %+v, want reviewed HyperFrames CLI", renderer)
	}
	if renderer.Skill != (toolSkillRef{}) {
		t.Fatal("renderer adds an authoring skill or an authentication home")
	}
	if _, ok := toolReleaseForSkill(toolSkillRef{}); ok {
		t.Fatal("empty skill resolved to an executable-only tool")
	}
	studio, _ := findToolRelease("zenith-studio")
	releases, err := toolReleasesForRun([]toolSkillRef{studio.Skill})
	if err != nil || len(releases) != 2 || releases[1].ID() != renderer.ID() {
		t.Fatalf("run tools = %+v, %v; want Studio and its pinned renderer", releases, err)
	}
	releases, err = toolReleasesForRun(nil)
	if err != nil || len(releases) != 0 {
		t.Fatalf("disabled Studio exposes tools: %+v, %v", releases, err)
	}
}

func TestStudioReleaseMatchesReviewedExport(t *testing.T) {
	t.Parallel()
	release := reviewedToolReleases()[0]
	if release.PackageName != "@zenith-studio/cli" || release.Version != "0.1.15" {
		t.Fatalf("release = %+v, want reviewed Studio 0.1.15", release)
	}
	bundle, err := resolveToolSkill(release.Skill)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateToolSkill(release.Skill, bundle); err != nil {
		t.Fatal(err)
	}
	if release.Skill.FileCount != 422 || release.Skill.SizeBytes != 1585332 ||
		release.Skill.Hash != "e5a142a096eedf26a67c614fd8d6b4022d48f0c4cbc59d2ddb8f66430b9766d7" {
		t.Fatalf("skill = %+v, want exact reviewed export", release.Skill)
	}
	// Independently hashed from the published export with Team's entrypoint
	// and common/media/video adapters.
	if got := adaptStudioSkill(
		bundle,
	).Hash; got != "071aadf331fa95f9d94a0dd15c6e9922babf740ac64e614b3bc2940ee117dd6d" {
		t.Fatalf("Team Studio snapshot hash = %s, want the reviewed 0.1.15 snapshot", got)
	}
	if err := validateToolSkill(release.Skill, bundle); err != nil {
		t.Fatalf("Team adaptation mutated the reviewed source: %v", err)
	}
	if !strings.Contains(bundle.Content, "Single image requests") {
		t.Fatal("reviewed export is missing the single-image route")
	}
	for _, name := range []string{"references/media-generation.md", "references/assembly.md"} {
		found := false
		for _, file := range bundle.Files {
			if file.Path == name && file.SizeBytes > 0 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("reviewed Studio export is missing %s", name)
		}
	}
	bundle.Files[0].Content += "tampered"
	if validateToolSkill(release.Skill, bundle) == nil {
		t.Fatal("accepted tampered file")
	}
}

func TestBundleValidationRejectsSubstitution(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*toolSkillRef, *toolSkillBundle)
	}{
		{"hash", func(r *toolSkillRef, _ *toolSkillBundle) { r.Hash = "wrong" }},
		{"identity", func(_ *toolSkillRef, b *toolSkillBundle) { b.ID = "other" }},
		{"name", func(_ *toolSkillRef, b *toolSkillBundle) { b.Name = "other" }},
		{"root", func(_ *toolSkillRef, b *toolSkillBundle) { b.Content += "tampered" }},
		{"count", func(r *toolSkillRef, _ *toolSkillBundle) { r.FileCount++ }},
		{"size", func(r *toolSkillRef, _ *toolSkillBundle) { r.SizeBytes++ }},
		{"traversal", func(_ *toolSkillRef, b *toolSkillBundle) { b.Files[0].Path = "../outside" }},
		{"duplicate", func(_ *toolSkillRef, b *toolSkillBundle) { b.Files[1].Path = b.Files[0].Path }},
		{"nested instructions", func(_ *toolSkillRef, b *toolSkillBundle) { b.Files[0].Path = "references/SKILL.md" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ref := reviewedToolReleases()[0].Skill
			bundle, err := resolveToolSkill(ref)
			if err != nil {
				t.Fatal(err)
			}
			test.change(&ref, &bundle)
			if validateToolSkill(ref, bundle) == nil {
				t.Fatal("accepted invalid bundle")
			}
		})
	}
}

func TestForRunRejectsUnavailableManagedSkills(t *testing.T) {
	t.Parallel()
	for _, version := range []string{
		"0.1.8", "0.1.9", "0.1.10", "0.1.11", "0.1.12-beta", "0.1.13-beta",
		"0.1.14-beta", "0.1.14-beta.1", "0.1.14-beta.2", "0.1.14-beta.3",
		"0.1.14-beta.4", "0.1.14-beta.5", "0.1.14-beta.6", "0.1.14",
	} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			current, _ := findToolRelease("zenith-studio")
			ref := current.Skill
			ref.ID = "zenith-studio@" + version
			ref.ToolVersion = version
			if _, err := toolReleasesForRun([]toolSkillRef{ref}); err == nil {
				t.Fatal("unavailable managed skill silently omitted its required executable")
			}
		})
	}
	if releases, err := toolReleasesForRun(
		[]toolSkillRef{{ID: "custom-skill", Source: "custom"}},
	); err != nil ||
		len(releases) != 0 {
		t.Fatalf("custom skill = %+v, %v; want no managed executables", releases, err)
	}
}

func TestReviewedToolReleasesHaveNoRetiredVersions(t *testing.T) {
	t.Parallel()
	catalog, err := loadReviewedToolCatalog()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, release := range catalog.releases {
		if keys[release.Key] {
			t.Fatalf("multiple releases retained for %s", release.Key)
		}
		keys[release.Key] = true
	}
	if len(catalog.locks) != 1 || catalog.locks[0].Plugin != "zenith-studio" {
		t.Fatalf("pins = %+v", catalog.locks)
	}
}

// A development pin can exercise an unpublished plugin locally but cannot
// pass the release gate or masquerade as a reviewed tag.
func TestPinnedToolBundlesAreReleases(t *testing.T) {
	t.Parallel()
	catalog, err := loadReviewedToolCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, lock := range catalog.locks {
		if lock.Development() || lock.ContentTreeID == "" {
			t.Fatalf(
				"%s pins %s; replace it with make bundle-bump TAG=zenith-studio--v1.2.1 COMPONENT=managedTools before merge",
				lock.Plugin,
				lock.Version,
			)
		}
	}
}

func TestToolCatalogRejectsCorruptPin(t *testing.T) {
	t.Parallel()
	root, err := fs.Sub(skillAssets, "tool_releases")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for _, name := range []string{"zenith-studio.lock.json", "generated/zenith-studio.json"} {
		data, err := fs.ReadFile(root, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: data}
	}
	if _, err := readToolCatalog(files); err != nil {
		t.Fatal(err)
	}
	lock := files["zenith-studio.lock.json"]
	lock.Data = bytes.Replace(lock.Data, []byte(`"name": "Zenith Studio"`), []byte(`"name": "Changed Studio"`), 1)
	if _, err := readToolCatalog(files); err == nil {
		t.Fatal("accepted changed Tool metadata without updating its hash")
	}
}

func TestToolCatalogRequiresCurrentBuildAssets(t *testing.T) {
	t.Parallel()
	root, err := fs.Sub(skillAssets, "tool_releases")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for _, name := range []string{"zenith-studio.lock.json", "generated/zenith-studio.json"} {
		raw, err := fs.ReadFile(root, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: raw}
	}
	if _, err := readToolCatalog(files); err != nil {
		t.Fatal(err)
	}
	generated := files["generated/zenith-studio.json"]
	delete(files, "generated/zenith-studio.json")
	if _, err := readToolCatalog(files); err == nil {
		t.Fatal("accepted missing generated assets")
	}
	generated.Data = []byte(strings.Replace(string(generated.Data), `"releaseHash":"`, `"releaseHash":"stale`, 1))
	files["generated/zenith-studio.json"] = generated
	if _, err := readToolCatalog(files); err == nil {
		t.Fatal("accepted stale generated assets")
	}
}
