// Package platformbundle holds the platform content zenith-team pins from
// Intelligent-Internet/boardly-plugins (ADR 0016): one canonical USTAR tar and
// one lock per pinned plugin component, plus the harness files zenith-team owns
// beside them, today the zenith-workflows SKILL.md. Every read verifies a tar
// against its lock before any of its bytes are served, and the pin is
// committed, so no build or test needs a credential or the network.
//
// It is its own package because cmd/zen links internal/mcp/tools and must
// never link the bundle bytes (the cmd/zen link set; make go-shape checks it).
// The server and zenith-team-mcp are the only shipped binaries that link it;
// cmd/bundlectl, the maintainer tool that writes the pin, reads it too. It
// imports only internal/mcp/tools and the asset-free internal/contentbundle
// verifier, so zenith-team-mcp stays sealed.
package platformbundle

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"
	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
)

//go:embed *.bundle.tar *.lock.json harness
var pinned embed.FS

// The zenith-workflows skill's name and description sit beside its harness
// SKILL.md, whose frontmatter must repeat them byte for byte. The daemon's
// prompts and the app quote the description.
const (
	workflowsSkillName        = "Zenith workflows"
	workflowsSkillDescription = "Use when your Task was launched from a Zenith Team Workflow: its description names a workflow key and a playbook path under this skill. Load that exact playbook and follow it. Each playbook is the procedure, quality bar and deliverable contract for one ready-to-run workflow."
)

// Load verifies the embedded Workflow release pins once per process.
func Load() ([]contentbundle.Bundle, error) { return loadPinned() }

var loadPinned = sync.OnceValues(func() ([]contentbundle.Bundle, error) {
	return contentbundle.LoadFS(pinned)
})

// WorkflowCatalog returns the Workflow catalog every Company sees: the files
// of every pinned workflows component, merged, with the harness SKILL.md at
// ".". That is the zenith-workflows skill root byte for byte. Any component
// that fails verification is an error, so the server refuses to start rather
// than serve part of the catalog.
func WorkflowCatalog() (fs.FS, []contentbundle.Lock, error) {
	bundles, err := Load()
	if err != nil {
		return nil, nil, err
	}
	return MergeWorkflowCatalog(bundles)
}

// MergeWorkflowCatalog lays the harness SKILL.md over the files of the given
// workflows components. A Workflow key pinned by two plugins, or pinned by one
// and retired by another, is an error: keys are unique across plugins, never
// move between them, and a retired key is never reused by any plugin. contentbundle.Decode
// already refuses a component that ships a key it retired itself.
func MergeWorkflowCatalog(bundles []contentbundle.Bundle) (fs.FS, []contentbundle.Lock, error) {
	return mergeWorkflowCatalog(pinned, bundles)
}

func mergeWorkflowCatalog(harness fs.FS, bundles []contentbundle.Bundle) (fs.FS, []contentbundle.Lock, error) {
	instructions, err := fs.ReadFile(harness, "harness/zenith-workflows/SKILL.md")
	if err != nil {
		return nil, nil, err
	}
	files := map[string][]byte{"SKILL.md": instructions}
	owners := map[string]string{}
	retiredBy := map[string]string{}
	for _, bundle := range bundles {
		if bundle.Lock.Component == contentbundle.ComponentTypeWorkflows {
			for _, key := range bundle.Lock.RetiredKeys {
				retiredBy[key] = bundle.Lock.Plugin
			}
		}
	}
	var locks []contentbundle.Lock
	for _, bundle := range bundles {
		if bundle.Lock.Component != contentbundle.ComponentTypeWorkflows {
			continue
		}
		err := fs.WalkDir(bundle.Files, ".", func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			key, _, _ := strings.Cut(name, "/")
			if owner, taken := owners[key]; taken && owner != bundle.Lock.Plugin {
				return fmt.Errorf(
					"%w: Workflow %q is pinned by both %s and %s",
					contentbundle.ErrInvalid,
					key,
					owner,
					bundle.Lock.Plugin,
				)
			}
			if retirer, retired := retiredBy[key]; retired && retirer != bundle.Lock.Plugin {
				return fmt.Errorf(
					"%w: Workflow %q is pinned by %s but retired by %s",
					contentbundle.ErrInvalid,
					key,
					bundle.Lock.Plugin,
					retirer,
				)
			}
			owners[key] = bundle.Lock.Plugin
			contents, err := fs.ReadFile(bundle.Files, name)
			files[name] = contents
			return err
		})
		if err != nil {
			return nil, nil, err
		}
		locks = append(locks, bundle.Lock)
	}
	if len(locks) == 0 {
		return nil, nil, fmt.Errorf("%w: no Workflow catalog is pinned", contentbundle.ErrInvalid)
	}
	return contentbundle.NewFS(files), locks, nil
}

// WorkflowsSkill describes the zenith-workflows platform skill over a merged
// catalog. The forms are the page's input, parsed by internal/workflow; a Run
// reads only SKILL.md and the playbooks.
func WorkflowsSkill(catalog fs.FS) tools.PlatformBundle {
	return tools.PlatformBundle{
		Key:         tools.ZenithWorkflowsSkillKey,
		Name:        workflowsSkillName,
		Description: workflowsSkillDescription,
		Files:       catalog,
		Exclude:     func(relativePath string) bool { return path.Base(relativePath) == "workflow.yaml" },
	}
}

// PlatformSkills returns the skills zenith-team-mcp serves from pinned content,
// with the locks they came from: the zenith-workflows skill, built from every
// workflows component that verified. A component that failed is left out and
// named in the error, so zenith-team-mcp keeps serving zenith-platform. The
// skills returned are usable whenever the slice is non-empty, error or not.
func PlatformSkills() ([]tools.PlatformBundle, []contentbundle.Lock, error) {
	bundles, loadErr := Load()
	catalog, locks, err := MergeWorkflowCatalog(bundles)
	if err != nil {
		return nil, nil, errors.Join(loadErr, err)
	}
	return []tools.PlatformBundle{WorkflowsSkill(catalog)}, locks, loadErr
}

// PlatformSkillSummaries lists every platform skill a product surface shows,
// compiled and pinned, in search order. It is strict, like the server, and
// built once per process; the result is read-only.
func PlatformSkillSummaries() ([]tools.PlatformSkillSummary, error) {
	return platformSkillSummaries()
}

var platformSkillSummaries = sync.OnceValues(func() ([]tools.PlatformSkillSummary, error) {
	catalog, _, err := WorkflowCatalog()
	if err != nil {
		return nil, err
	}
	return tools.PlatformSkills([]tools.PlatformBundle{WorkflowsSkill(catalog)})
})
