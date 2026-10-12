package contentbundle

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// This also runs in the Windows content lane without importing the daemon,
// whose process control is platform-specific.
func TestShippedManagedToolsVerify(t *testing.T) {
	t.Parallel()
	bundles, err := LoadFS(os.DirFS("../daemon/tool_releases"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundles) != 1 || bundles[0].Lock.Plugin != "zenith-studio" ||
		bundles[0].Lock.Component != ComponentTypeManagedTools {
		t.Fatalf("shipped Tool pins = %+v", bundles)
	}
	catalog, err := ReadManagedTools(bundles[0].Files)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../daemon/tool_releases/generated/zenith-studio.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeToolAssets(catalog, raw); err != nil {
		t.Fatal(err)
	}
}

func managedFiles(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{
		"SKILL.md": []byte("---\nname: zenith-studio-cli\ndescription: Media tools\n---\n\n# Studio\n"),
	}
	ref := SkillRef{
		ToolKey:     "zenith-studio",
		ToolVersion: "0.1.0-beta.1",
		ID:          "zenith-studio@0.1.0-beta.1",
		Source:      "managed_tool",
		Name:        "zenith-studio-cli",
		Description: "Media tools",
		Hash:        TreeHash(files),
		SizeBytes:   int64(len(files["SKILL.md"])),
		FileCount:   1,
	}
	catalog := ManagedTools{
		ContractVersion: 1,
		Releases: []ToolRelease{
			{
				Key:              "zenith-studio",
				Name:             "Studio",
				PackageName:      "@zenith-studio/cli",
				Version:          ref.ToolVersion,
				Command:          "zenith-studio",
				Integrity:        "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64)),
				MinimumNodeMajor: 22,
				HomeEnv:          "ZENITH_STUDIO_CLI_HOME",
				APIURLEnv:        "ZENITH_STUDIO_CLI_API_URL",
				APIURL:           "https://studio.example",
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
	return map[string][]byte{
		"release.json": raw,
	}
}

func TestManagedToolsRoundTrip(t *testing.T) {
	t.Parallel()
	files := managedFiles(t)
	artifact, err := Encode(ComponentTypeManagedTools, files)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(artifact) {
		t.Fatal("managed tools must encode as JSON, not an archive")
	}
	var metadata ManagedTools
	if err := json.Unmarshal(artifact, &metadata); err != nil {
		t.Fatal(err)
	}
	lock := Lock{
		LockFormat:     1,
		Plugin:         "zenith-studio",
		Component:      ComponentTypeManagedTools,
		Version:        "1.0.0",
		Tag:            "zenith-studio--v1.0.0",
		Commit:         strings.Repeat("1", 40),
		ManifestPath:   "plugins/zenith-studio/.boardly-plugin/plugin.json",
		ManifestSHA256: strings.Repeat("2", 64),
		ContentPath:    "plugins/zenith-studio/boardly/managed-tools",
		ContentTreeID:  strings.Repeat("3", 40),
		ManagedTools:   &metadata,
		TreeHash:       TreeHash(map[string][]byte{"release.json": artifact}),
		FileCount:      len(files),
	}
	bundle, err := Decode(lock, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := ReadManagedTools(bundle.Files)
	if err != nil || len(catalog.Releases) != 2 {
		t.Fatalf("catalog = %+v, %v", catalog, err)
	}
	if lock.FileCount != 1 {
		t.Fatalf("pin has %d files, want metadata only", lock.FileCount)
	}
	raw, err := MarshalLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"artifact"`) {
		t.Fatal("managed lock still names an archive")
	}
	root := map[string][]byte{"zenith-studio.lock.json": raw}
	if bundles, err := LoadFS(NewFS(root)); err != nil || len(bundles) != 1 {
		t.Fatalf("single-file load: %v", err)
	}
	root["zenith-studio.bundle.tar"] = []byte("unnecessary archive")
	if _, err := LoadFS(NewFS(root)); err == nil {
		t.Fatal("accepted an orphan managed archive")
	}
	if _, err := Decode(lock, []byte("unnecessary archive")); err == nil {
		t.Fatal("accepted a managed archive")
	}
	lock.ManagedTools.Releases[0].APIURL = "https://changed.example"
	if _, err := Decode(lock, nil); err == nil {
		t.Fatal("accepted modified metadata without its reviewed hash")
	}
}

func TestManagedToolsRejectInvalidContent(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(map[string][]byte)
	}{
		{"contract", func(f map[string][]byte) {
			f["release.json"] = []byte(strings.Replace(string(f["release.json"]), `"contractVersion":1`, `"contractVersion":2`, 1))
		}},
		{"unknown field", func(f map[string][]byte) {
			f["release.json"] = []byte(strings.Replace(string(f["release.json"]), `"contractVersion":1`, `"contractVersion":1,"hooks":[]`, 1))
		}},
		{"duplicate field", func(f map[string][]byte) {
			f["release.json"] = []byte(strings.Replace(string(f["release.json"]), `"contractVersion":1`, `"contractVersion":1,"contractVersion":1`, 1))
		}},
		{"integrity", func(f map[string][]byte) {
			f["release.json"] = []byte(strings.Replace(string(f["release.json"]), "sha512-", "sha1-", 1))
		}},
		{"missing descriptor", func(f map[string][]byte) { delete(f, "release.json") }},
		{"changed skill", func(f map[string][]byte) { f["skills/zenith-studio-cli/SKILL.md"] = []byte("changed") }},
		{"extra skill", func(f map[string][]byte) { f["skills/other/SKILL.md"] = []byte("extra") }},
		{"unreviewed file", func(f map[string][]byte) { f["skills/zenith-studio-cli/extra.md"] = []byte("extra") }},
		{"traversal", func(f map[string][]byte) { f["skills/zenith-studio-cli/../escape.md"] = []byte("escape") }},
		{"missing dependency", func(f map[string][]byte) {
			f["release.json"] = []byte(strings.Replace(string(f["release.json"]), `"dependencies":["hyperframes@0.8.36"]`, `"dependencies":["hyperframes@0.8.37"]`, 1))
		}},
		{"cycle", func(f map[string][]byte) {
			f["release.json"] = []byte(strings.Replace(string(f["release.json"]), `"dependencies":["hyperframes@0.8.36"]`, `"dependencies":["zenith-studio@0.1.0-beta.1"]`, 1))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			files := managedFiles(t)
			test.change(files)
			if _, err := Encode(ComponentTypeManagedTools, files); err == nil {
				t.Fatal("accepted invalid managed tools")
			}
		})
	}
}

func TestManagedToolsPreserveHostContract(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*ManagedTools)
	}{
		{"unknown tool", func(c *ManagedTools) { c.Releases[1].Key = "other" }},
		{"Studio package", func(c *ManagedTools) { c.Releases[0].PackageName = "other" }},
		{"Studio command", func(c *ManagedTools) { c.Releases[0].Command = "other" }},
		{"Studio home", func(c *ManagedTools) { c.Releases[0].HomeEnv = "OTHER_HOME" }},
		{"Studio origin env", func(c *ManagedTools) { c.Releases[0].APIURLEnv = "OTHER_API_URL" }},
		{"unsafe source", func(c *ManagedTools) { c.Releases[0].SkillSources = []string{"../outside"} }},
		{"missing sources", func(c *ManagedTools) { c.Releases[0].SkillSources = nil }},
		{"Studio setup", func(c *ManagedTools) { c.Releases[0].SkipSetupEnv = "OTHER_SETUP" }},
		{"missing Studio skill", func(c *ManagedTools) { c.Releases[0].Skill = SkillRef{} }},
		{"missing renderer dependency", func(c *ManagedTools) { c.Releases[0].Dependencies = nil }},
		{"renderer package", func(c *ManagedTools) { c.Releases[1].PackageName = "other" }},
		{"renderer command", func(c *ManagedTools) { c.Releases[1].Command = "other" }},
		{"renderer setup", func(c *ManagedTools) { c.Releases[1].SkipSetupEnv = "OTHER_SETUP" }},
		{"renderer auth", func(c *ManagedTools) {
			c.Releases[1].HomeEnv = "RENDERER_HOME"
			c.Releases[1].APIURLEnv = "RENDERER_API_URL"
			c.Releases[1].APIURL = "https://renderer.example"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			files := managedFiles(t)
			var catalog ManagedTools
			if err := json.Unmarshal(files["release.json"], &catalog); err != nil {
				t.Fatal(err)
			}
			test.change(&catalog)
			raw, err := json.Marshal(catalog)
			if err != nil {
				t.Fatal(err)
			}
			files["release.json"] = raw
			if _, err := Encode(ComponentTypeManagedTools, files); err == nil {
				t.Fatal("accepted metadata incompatible with the host integration")
			}
		})
	}
}
