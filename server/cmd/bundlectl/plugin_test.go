package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/contentbundle"
)

func writeManagedPlugin(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "plugins", "zenith-studio")
	writeFile(
		t,
		filepath.Join(dir, manifestPath),
		[]byte(
			`{"format":1,"name":"zenith-studio","version":"1.0.0","description":"Studio tools","managedTools":{"path":"./boardly/managed-tools"}}`,
		),
	)
	content := []byte("---\nname: zenith-studio-cli\ndescription: Studio tools\n---\n\n# Studio\n")
	ref := contentbundle.SkillRef{
		ToolKey:     "zenith-studio",
		ToolVersion: "0.1.0-beta.1",
		ID:          "zenith-studio@0.1.0-beta.1",
		Source:      "managed_tool",
		Name:        "zenith-studio-cli",
		Description: "Studio tools",
		Hash:        contentbundle.TreeHash(map[string][]byte{"SKILL.md": content}),
		FileCount:   1,
		SizeBytes:   int64(len(content)),
	}
	catalog := contentbundle.ManagedTools{
		ContractVersion: 1,
		Releases: []contentbundle.ToolRelease{
			{
				Key:              "zenith-studio",
				Name:             "Studio",
				PackageName:      "@zenith-studio/cli",
				Version:          ref.ToolVersion,
				Command:          "zenith-studio",
				MinimumNodeMajor: 22,
				HomeEnv:          "ZENITH_STUDIO_CLI_HOME",
				APIURLEnv:        "ZENITH_STUDIO_CLI_API_URL",
				APIURL:           "https://studio.example",
				Integrity:        "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64)),
				Skill:            ref,
				SkipSetupEnv:     "ZENITH_STUDIO_CLI_SKIP_SETUP",
				SkillSources: []string{
					"skills/canonical/zenith-studio-cli",
					"skills/adapters/zenith-team/zenith-studio-cli",
				},
				Dependencies: []string{"hyperframes@0.8.36"},
			},
			{
				Key:              "hyperframes",
				Name:             "HyperFrames",
				PackageName:      "hyperframes",
				Version:          "0.8.36",
				Command:          "hyperframes",
				MinimumNodeMajor: 22,
				Integrity:        "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64)),
			},
		},
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"release.json": raw} {
		writeFile(t, filepath.Join(dir, "boardly/managed-tools", name), data)
	}
	return dir
}

func TestManagedToolPluginRelease(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	dir := writeManagedPlugin(t, repo.dir)
	if stdout, stderr, code := runCommand(t, "check", dir); code != 0 || !strings.Contains(stdout, "managedTools") {
		t.Fatalf("check = %d: %s%s", code, stdout, stderr)
	}
	repo.commit("Studio plugin")
	repo.tag("zenith-studio--v1.0.0")
	out := t.TempDir()
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
		t.Fatalf("import = %d: %s%s", code, stdout, stderr)
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 1 || entries[0].Name() != "zenith-studio.lock.json" {
		t.Fatalf("pin output = %v: %v; want one JSON lock", entries, err)
	}
	bundle := readPinned(t, out, "zenith-studio")
	if bundle.Lock.Component != contentbundle.ComponentTypeManagedTools ||
		bundle.Lock.ContentPath != "plugins/zenith-studio/boardly/managed-tools" {
		t.Fatalf("lock = %+v", bundle.Lock)
	}
	if _, err := contentbundle.ReadManagedTools(bundle.Files); err != nil {
		t.Fatal(err)
	}
	change, err := comparePins(nil, &bundle)
	if err != nil {
		t.Fatal(err)
	}
	var summary bytes.Buffer
	writePluginMarkdown(&summary, change)
	if strings.Contains(summary.String(), "Artifact") || !strings.Contains(summary.String(), bundle.Lock.TreeHash) {
		t.Fatalf("managed Tool summary must show content hash without archive fields: %s", summary.String())
	}
	if stdout, stderr, code := runCommand(t, "import", "--verify", "--repo", repo.dir, "--out", out); code != 0 {
		t.Fatalf("verify = %d: %s%s", code, stdout, stderr)
	}
	archive := filepath.Join(out, "zenith-studio.bundle.tar")
	writeFile(t, archive, []byte("obsolete archive"))
	if stdout, stderr, code := runCommand(t, "import", "--verify", "--repo", repo.dir, "--out", out); code == 0 {
		t.Fatalf("verify accepted redundant archive: %s%s", stdout, stderr)
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "boardly/managed-tools/skills/zenith-studio-cli/SKILL.md"), []byte("changed"))
	if _, _, code := runCommand(t, "check", dir); code == 0 {
		t.Fatal("check accepted changed skill without updating reviewed hash")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "boardly/managed-tools/skills/escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCommand(t, "check", dir); code == 0 {
		t.Fatal("check accepted symlink")
	}
}

func TestManagedToolPluginRejectsUndeclaredContent(t *testing.T) {
	t.Parallel()
	dir := writeManagedPlugin(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "boardly/workflows/unclaimed/playbook.md"), []byte("unclaimed\n"))
	if stdout, stderr, code := runCommand(t, "check", dir); code == 0 {
		t.Fatalf("accepted undeclared content: %s%s", stdout, stderr)
	}
}
