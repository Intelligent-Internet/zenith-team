package contentbundle

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolAssetsMatchPinnedDescriptor(t *testing.T) {
	t.Parallel()
	catalog, err := ReadManagedTools(NewFS(managedFiles(t)))
	if err != nil {
		t.Fatal(err)
	}
	ref := catalog.Releases[0].Skill
	skill, err := ReadSkill(
		NewFS(
			map[string][]byte{
				"skills/zenith-studio-cli/SKILL.md": []byte(
					"---\nname: zenith-studio-cli\ndescription: Media tools\n---\n\n# Studio\n",
				),
			},
		),
		ref,
	)
	if err != nil {
		t.Fatal(err)
	}
	assets := []ToolAsset{{ToolKey: "zenith-studio", License: "MIT\n", Skill: skill}}
	raw, err := EncodeToolAssets(catalog, assets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeToolAssets(catalog, raw); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*ManagedTools, *[]ToolAsset)
	}{
		{"changed source", func(c *ManagedTools, _ *[]ToolAsset) { c.Releases[0].Integrity = "sha512-other" }},
		{"changed file", func(_ *ManagedTools, a *[]ToolAsset) { (*a)[0].Skill.Content += "modified" }},
		{"missing license", func(_ *ManagedTools, a *[]ToolAsset) { (*a)[0].License = "" }},
		{"missing skill", func(_ *ManagedTools, a *[]ToolAsset) { *a = nil }},
		{"duplicate skill", func(_ *ManagedTools, a *[]ToolAsset) { *a = append(*a, (*a)[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copyCatalog, err := ReadManagedTools(NewFS(managedFiles(t)))
			if err != nil {
				t.Fatal(err)
			}
			var document toolAssets
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			test.change(&copyCatalog, &document.Assets)
			changed, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeToolAssets(copyCatalog, changed); err == nil {
				t.Fatal("accepted stale or corrupt generated content")
			}
		})
	}
	if strings.Contains(string(raw), "skillSources") {
		t.Fatal("generated content duplicates the descriptor")
	}
}
