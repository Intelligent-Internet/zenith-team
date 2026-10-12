package daemon

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sync"

	"boardly.dev/boardly/server/internal/contentbundle"
)

// These wire types are shared with bundlectl, which verifies the same
// managed Tool contract before a plugin release can be pinned.
type (
	toolRelease     = contentbundle.ToolRelease
	toolSkillRef    = contentbundle.SkillRef
	toolSkillBundle = contentbundle.Skill
	toolSkillFile   = contentbundle.SkillFile
)

// Only managed Tool assets reach the daemon and zen; Workflow bytes stay
// with platformbundle, the server and boardly-mcp.
//
//go:embed tool_releases/*.lock.json tool_releases/generated/*.json
var skillAssets embed.FS

type toolCatalog struct {
	releases []toolRelease
	skills   map[string]toolSkillBundle
	locks    []contentbundle.Lock
}

var loadReviewedToolCatalog = sync.OnceValues(func() (toolCatalog, error) {
	root, err := fs.Sub(skillAssets, "tool_releases")
	if err != nil {
		return toolCatalog{}, err
	}
	return readToolCatalog(root)
})

func readToolCatalog(root fs.FS) (toolCatalog, error) {
	result := toolCatalog{skills: map[string]toolSkillBundle{}}
	bundles, err := contentbundle.LoadFS(root)
	if err != nil {
		return result, fmt.Errorf("load managed tool bundles: %w", err)
	}
	keys, commands := map[string]bool{}, map[string]bool{}
	for _, bundle := range bundles {
		if bundle.Lock.Component != contentbundle.ComponentTypeManagedTools {
			return result, errors.New("daemon bundle must contain managedTools")
		}
		catalog, err := contentbundle.ReadManagedTools(bundle.Files)
		if err != nil {
			return result, fmt.Errorf("plugin %s: %w", bundle.Lock.Plugin, err)
		}
		result.locks = append(result.locks, bundle.Lock)
		prepared, err := fs.ReadFile(root, "generated/"+bundle.Lock.Plugin+".json")
		if err != nil {
			return result, fmt.Errorf("generated Tool assets unavailable; run make tools-prepare: %w", err)
		}
		assets, err := contentbundle.DecodeToolAssets(catalog, prepared)
		if err != nil {
			return result, err
		}
		for _, asset := range assets {
			if _, exists := result.skills[asset.Skill.ID]; exists {
				return result, fmt.Errorf("managed skill %s is pinned more than once", asset.Skill.ID)
			}
			result.skills[asset.Skill.ID] = asset.Skill
		}
		for _, release := range catalog.Releases {
			if keys[release.Key] || commands[release.Command] {
				return result, fmt.Errorf("managed tool %s is pinned more than once", release.Key)
			}
			keys[release.Key], commands[release.Command] = true, true
			result.releases = append(result.releases, release)
		}
	}
	if !keys["zenith-studio"] || !keys["hyperframes"] {
		return result, errors.New("Studio and its renderer must be pinned")
	}
	return result, nil
}

// The daemon constructor refuses a bad catalog before exposing any Tool.
func reviewedToolReleases() []toolRelease {
	catalog, err := loadReviewedToolCatalog()
	if err != nil {
		return nil
	}
	releases := slices.Clone(catalog.releases)
	for i := range releases {
		releases[i].Dependencies = slices.Clone(releases[i].Dependencies)
		releases[i].SkillSources = slices.Clone(releases[i].SkillSources)
	}
	return releases
}

func findToolRelease(key string) (toolRelease, bool) {
	for _, release := range reviewedToolReleases() {
		if release.Key == key {
			return release, true
		}
	}
	return toolRelease{}, false
}

func toolReleaseForSkill(ref toolSkillRef) (toolRelease, bool) {
	if ref.ID == "" {
		return toolRelease{}, false
	}
	for _, release := range reviewedToolReleases() {
		if release.Skill == ref {
			return release, true
		}
	}
	return toolRelease{}, false
}

// toolReleasesForRun selects executables from enabled, frozen Skill references. Dependencies
// are exact release identities and carry no additional authoring instructions.
func toolReleasesForRun(refs []toolSkillRef) ([]toolRelease, error) {
	var result []toolRelease
	seen := make(map[string]bool)
	var add func(toolRelease) error
	add = func(release toolRelease) error {
		if seen[release.ID()] {
			return nil
		}
		seen[release.ID()] = true
		result = append(result, release)
		for _, id := range release.Dependencies {
			found := false
			for _, dependency := range reviewedToolReleases() {
				if dependency.ID() != id {
					continue
				}
				found = true
				if err := add(dependency); err != nil {
					return err
				}
				break
			}
			if !found {
				return fmt.Errorf("reviewed tool dependency %s is unavailable", id)
			}
		}
		return nil
	}
	for _, ref := range refs {
		release, ok := toolReleaseForSkill(ref)
		if !ok {
			if ref.Source == "managed_tool" {
				return nil, fmt.Errorf("managed tool skill %s is unavailable", ref.ID)
			}
			continue
		}
		if err := add(release); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func resolveToolSkill(ref toolSkillRef) (toolSkillBundle, error) {
	catalog, err := loadReviewedToolCatalog()
	if err != nil {
		return toolSkillBundle{}, err
	}
	if _, ok := toolReleaseForSkill(ref); !ok {
		return toolSkillBundle{}, errors.New("skill release is unavailable")
	}
	bundle, ok := catalog.skills[ref.ID]
	if !ok {
		return toolSkillBundle{}, errors.New("skill release is unavailable")
	}
	bundle.Files = slices.Clone(bundle.Files)
	return bundle, nil
}

func validateToolSkill(ref toolSkillRef, bundle toolSkillBundle) error {
	return contentbundle.ValidateSkill(ref, bundle)
}

func toolSkillFiles(bundle toolSkillBundle) []toolSkillFile { return contentbundle.SkillFiles(bundle) }

func safeToolSkillPath(value string) bool { return contentbundle.SafeSkillPath(value) }
