package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"
)

func npmFixture(t *testing.T, change func(map[string][]byte)) (contentbundle.ManagedTools, []byte) {
	t.Helper()
	dir := writeManagedPlugin(t, t.TempDir())
	catalog, err := contentbundle.ReadManagedTools(os.DirFS(filepath.Join(dir, "boardly/managed-tools")))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"package/package.json": []byte(`{"name":"@zenith-studio/cli","version":"0.1.0-beta.1"}`),
		"package/LICENSE":      []byte("MIT\n"),
		"package/skills/adapters/zenith-team/zenith-studio-cli/SKILL.md": []byte(
			"---\nname: zenith-studio-cli\ndescription: Studio tools\n---\n\n# Studio\n",
		),
		"package/skills/canonical/zenith-studio-cli/references/example.md": []byte("example\n"),
	}
	skillFiles := map[string][]byte{
		"SKILL.md":              files["package/skills/adapters/zenith-team/zenith-studio-cli/SKILL.md"],
		"references/example.md": []byte("example\n"),
	}
	catalog.Releases[0].Skill.Hash = contentbundle.TreeHash(skillFiles)
	catalog.Releases[0].Skill.FileCount = 2
	catalog.Releases[0].Skill.SizeBytes += int64(len("example\n"))
	if change != nil {
		change(files)
	}
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	tarball := tar.NewWriter(compressed)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := tarball.WriteHeader(
			&tar.Header{Name: name, Mode: 0o644, Size: int64(len(files[name]))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tarball.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha512.Sum512(archive.Bytes())
	catalog.Releases[0].Integrity = "sha512-" + base64.StdEncoding.EncodeToString(digest[:])
	return catalog, archive.Bytes()
}

func TestPrepareToolsUsesVerifiedCacheOffline(t *testing.T) {
	t.Parallel()
	catalog, archive := npmFixture(t, nil)
	fetches := 0
	preparer := toolPreparer{cache: t.TempDir(), fetch: func(_ context.Context, address string) ([]byte, error) {
		fetches++
		if address != "https://registry.npmjs.org/@zenith-studio/cli/-/cli-0.1.0-beta.1.tgz" {
			t.Fatalf("unexpected source %s", address)
		}
		return archive, nil
	}}
	first, err := preparer.prepare(t.Context(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	preparer.fetch = func(context.Context, string) ([]byte, error) { return nil, errors.New("offline") }
	second, err := preparer.prepare(t.Context(), catalog)
	if err != nil || !bytes.Equal(first, second) || fetches != 1 {
		t.Fatalf("offline build differs: %v, downloads=%d", err, fetches)
	}
	if _, err := contentbundle.DecodeToolAssets(catalog, second); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(preparer.cache)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache entries = %v: %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(preparer.cache, entries[0].Name()), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := preparer.prepare(t.Context(), catalog); err == nil {
		t.Fatal("accepted corrupt cached package")
	}
}

func TestPrepareToolsRejectsUnreviewedDownloads(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(map[string][]byte)
	}{
		{"version", func(f map[string][]byte) {
			f["package/package.json"] = []byte(`{"name":"@zenith-studio/cli","version":"9.0.0"}`)
		}},
		{"missing file", func(f map[string][]byte) {
			delete(f, "package/skills/canonical/zenith-studio-cli/references/example.md")
		}},
		{"changed file", func(f map[string][]byte) {
			f["package/skills/canonical/zenith-studio-cli/references/example.md"] = []byte("changed")
		}},
		{"extra file", func(f map[string][]byte) { f["package/skills/canonical/zenith-studio-cli/extra.md"] = []byte("extra") }},
		{"collision", func(f map[string][]byte) {
			f["package/skills/canonical/zenith-studio-cli/SKILL.md"] = []byte("collision")
		}},
		{"traversal", func(f map[string][]byte) { f["package/../outside"] = []byte("escape") }},
		{"license", func(f map[string][]byte) { delete(f, "package/LICENSE") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			catalog, archive := npmFixture(t, test.change)
			preparer := toolPreparer{
				cache: t.TempDir(),
				fetch: func(context.Context, string) ([]byte, error) { return archive, nil },
			}
			if _, err := preparer.prepare(t.Context(), catalog); err == nil {
				t.Fatal("accepted invalid package")
			}
		})
	}
	catalog, archive := npmFixture(t, nil)
	archive[0] ^= 1
	preparer := toolPreparer{
		cache: t.TempDir(),
		fetch: func(context.Context, string) ([]byte, error) { return archive, nil },
	}
	if _, err := preparer.prepare(t.Context(), catalog); err == nil {
		t.Fatal("accepted npm integrity mismatch")
	}
}

func TestPrepareToolsWritesOnlyBuildOutput(t *testing.T) {
	t.Parallel()
	catalog, archive := npmFixture(t, nil)
	preparer := toolPreparer{
		cache: t.TempDir(),
		fetch: func(context.Context, string) ([]byte, error) { return archive, nil },
	}
	if _, err := preparer.prepare(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	repo := newFixtureRepo(t)
	dir := writeManagedPlugin(t, repo.dir)
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "boardly/managed-tools/release.json"), raw)
	out := t.TempDir()
	repo.commit("metadata-only Tool plugin")
	repo.tag("zenith-studio--v1.0.0")
	if stdout, stderr, code := runCommand(
		t,
		"import",
		"--tag",
		"zenith-studio--v1.0.0",
		"--repo",
		repo.dir,
		"--out",
		out,
	); code != 0 {
		t.Fatalf("import: %s%s", stdout, stderr)
	}
	before := readPinned(t, out, "zenith-studio")
	if before.Lock.FileCount != 1 {
		t.Fatal("pin must contain metadata only")
	}
	lockPath := filepath.Join(out, "zenith-studio.lock.json")
	beforeBytes, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	generated := t.TempDir()
	if stdout, stderr, code := runCommand(
		t,
		"prepare",
		"--pins",
		out,
		"--out",
		generated,
		"--cache",
		preparer.cache,
	); code != 0 {
		t.Fatalf("prepare: %s%s", stdout, stderr)
	}
	prepared, err := os.ReadFile(filepath.Join(generated, "zenith-studio.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contentbundle.DecodeToolAssets(catalog, prepared); err != nil {
		t.Fatal(err)
	}
	afterBytes, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("prepare modified the committed pin")
	}
}
