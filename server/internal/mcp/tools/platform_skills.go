package tools

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"unicode/utf8"
)

// Each platform skill carries the product namespace in its key:
// zenith-<name>.
// A compiled skill's name is the directory under builtin/ and the `name:` in
// its SKILL.md; the description is pinned here because the daemon's prompts
// and the app quote it, and search_skills matches queries against it.
const (
	zenithIntelligentUISkillKey         = "zenith-intelligent-ui"
	zenithIntelligentUISkillDescription = "Author clear, beautiful, interactive inline responses using the supported component grammar, state, visualizations, grounded references, and host actions."
	zenithPlatformSkillKey              = "zenith-platform"
	zenithPlatformSkillName             = "Zenith platform"
	zenithPlatformSkillDescription      = "Use for Zenith Team platform actions inside a Run that the system prompt does not fully cover: the zen CLI, your Task and its dependencies, the Conversation, Project Pages and overview contributions, presenting result files, founder Questions, Task failure, the Company and its Agents, and Mail drafts. Not for the product code you are working on."
	// The Workflow playbooks ship as a second platform skill. A Task launched
	// from the Workflows page names its playbook in the description; the Run
	// loads <workflow-key>/playbook.md from it. Its files, name and
	// description are pinned in internal/platformbundle (ADR 0016), so the zen
	// CLI, which links this package, never links them; the key stays reserved
	// here whatever is linked.
	zenithWorkflowsSkillKey = "zenith-workflows"
)

// ZenithWorkflowsSkillKey is the reserved key of the platform skill that
// carries one playbook per Workflow.
const ZenithWorkflowsSkillKey = zenithWorkflowsSkillKey

// ZenithPlatformSkillKey is the reserved key of the compiled platform skill
// every attended Run may load for the zen CLI and the product contracts.
const ZenithPlatformSkillKey = zenithPlatformSkillKey

// reservedPlatformSkillKeys is the static floor of reserved keys. A Run's
// Skill snapshot can never take one, whether or not that skill is linked into
// this binary or verified at start.
var reservedPlatformSkillKeys = []string{zenithPlatformSkillKey, zenithIntelligentUISkillKey, zenithWorkflowsSkillKey}

// platformSkill names one release-owned procedure zenith-team-mcp serves.
// Keys are reserved: a custom Skill can never shadow one.
type platformSkill struct {
	Key string
	// Name is what a person reads on the agent page and in search_skills;
	// the key stays the slug an Agent passes to load_skill_reference.
	Name        string
	Description string
	// Files is the skill root: SKILL.md at "." and its references.
	Files fs.FS
	// Exclude drops files that ship beside the skill but are not
	// references an Agent loads, such as the Workflow forms.
	Exclude func(relativePath string) bool
}

// compiledPlatformSkills is the catalog compiled into this package, in
// search order. Pinned skills (PlatformBundle) follow it.
var compiledPlatformSkills = []platformSkill{
	{
		Key: zenithPlatformSkillKey, Name: zenithPlatformSkillName,
		Description: zenithPlatformSkillDescription, Files: compiledSkillFiles("builtin/zenith-platform"),
	},
	{
		Key:         zenithIntelligentUISkillKey,
		Name:        "Zenith Intelligent UI",
		Description: zenithIntelligentUISkillDescription,
		Files:       compiledSkillFiles("builtin/zenith-intelligent-ui"),
	},
}

// PlatformBundle is a release-owned platform skill whose files zenith-team pins
// outside this package, in internal/platformbundle, so the zen CLI never links
// them. Its key must be reserved and not a compiled skill's; its SKILL.md
// must carry `name: <key>` and `description: <Description>` byte for byte.
type PlatformBundle struct {
	Key         string
	Name        string
	Description string
	Files       fs.FS
	Exclude     func(relativePath string) bool
}

// PlatformSkillSummary is what a product surface shows for one platform
// skill: its reserved key, its pinned description and the reference files
// an Agent can load beside SKILL.md.
type PlatformSkillSummary struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	References  []string `json:"references"`
}

// PlatformSkills lists the platform skills in search order, the compiled
// ones then the pinned bundles, read from their files so the app and the MCP
// server can never disagree about what ships. It is strict: a bundle that
// does not load is an error, as the server refuses to start on one.
func PlatformSkills(bundles []PlatformBundle) ([]PlatformSkillSummary, error) {
	definitions := slices.Clone(compiledPlatformSkills)
	for _, bundle := range bundles {
		if err := checkPlatformBundle(bundle, definitions); err != nil {
			return nil, err
		}
		definitions = append(definitions, platformSkill(bundle))
	}
	summaries := make([]PlatformSkillSummary, 0, len(definitions))
	for _, definition := range definitions {
		skill, _, err := loadPlatformSkill(definition)
		if err != nil {
			return nil, fmt.Errorf("platform skill %q: %w", definition.Key, err)
		}
		references := make([]string, 0, len(skill.Files))
		for _, file := range skill.Files {
			if file.Path != "SKILL.md" {
				references = append(references, file.Path)
			}
		}
		summaries = append(summaries, PlatformSkillSummary{
			Key: skill.Key, Name: skill.Name, Description: skill.Description, References: references,
		})
	}
	return summaries, nil
}

// platformSkillFiles is release-owned and compiled into zenith-team-mcp so Task
// Runs can load security-critical platform procedures without a Run-scoped
// Skill snapshot or a network dependency. Only explicitly named skill
// directories can join the compiled catalog.
//
//go:embed all:builtin/zenith-platform all:builtin/zenith-intelligent-ui
var platformSkillFiles embed.FS

// IntelligentUIInstructions supplies the same release-owned guide to Runs that
// cannot call skill tools, including Page writing and restricted document Runs.
// Reading this compiled content grants no tool or product authority.
func IntelligentUIInstructions() string {
	content, err := platformSkillFiles.ReadFile("builtin/zenith-intelligent-ui/SKILL.md")
	if err != nil {
		panic(err) // the file is required by the compile-time embed above
	}
	return string(content)
}

func compiledSkillFiles(root string) fs.FS {
	files, err := fs.Sub(platformSkillFiles, root)
	if err != nil {
		panic(err) // the root is a compile-time constant under //go:embed
	}
	return files
}

func isPlatformSkillKey(key string) bool {
	return slices.Contains(reservedPlatformSkillKeys, key)
}

// checkPlatformBundle refuses a bundle that would shadow a compiled skill,
// repeat another bundle, or take a key that is not reserved.
func checkPlatformBundle(bundle PlatformBundle, loaded []platformSkill) error {
	if !isPlatformSkillKey(bundle.Key) || bundle.Files == nil ||
		slices.ContainsFunc(loaded, func(skill platformSkill) bool { return skill.Key == bundle.Key }) {
		return fmt.Errorf("%w: platform bundle %q is not a reserved, unclaimed key",
			ErrSkillSnapshotUnavailable, bundle.Key)
	}
	return nil
}

// OpenTaskSkillService merges release-owned platform Skills with an optional
// frozen Run-scoped Skill snapshot. Reserved platform keys always win, so a
// custom Skill cannot replace a security-critical procedure.
//
// The compiled skills and the snapshot must load, or the Run has no skill
// tools. A pinned bundle that does not load is dropped and reported through
// dropped instead: its key stays reserved, and zenith-platform stays served.
func OpenTaskSkillService(
	snapshotRoot string,
	bundles []PlatformBundle,
	dropped func(key string, err error),
) (*DefaultSkillService, error) {
	service := &DefaultSkillService{
		skills:        make(map[string]skillSnapshotSkill, len(reservedPlatformSkillKeys)),
		embeddedFiles: make(map[string]map[string][]byte, len(reservedPlatformSkillKeys)),
		order:         make([]string, 0, len(reservedPlatformSkillKeys)),
	}
	add := func(skill skillSnapshotSkill, contentsByPath map[string][]byte) {
		service.skills[skill.Key] = skill
		service.embeddedFiles[skill.Key] = contentsByPath
		service.order = append(service.order, skill.Key)
	}
	definitions := slices.Clone(compiledPlatformSkills)
	for _, definition := range definitions {
		skill, contentsByPath, err := loadPlatformSkill(definition)
		if err != nil {
			return nil, err
		}
		add(skill, contentsByPath)
	}
	for _, bundle := range bundles {
		err := checkPlatformBundle(bundle, definitions)
		if err == nil {
			definitions = append(definitions, platformSkill(bundle))
			var skill skillSnapshotSkill
			var contentsByPath map[string][]byte
			if skill, contentsByPath, err = loadPlatformSkill(platformSkill(bundle)); err == nil {
				add(skill, contentsByPath)
				continue
			}
		}
		if dropped != nil {
			dropped(bundle.Key, err)
		}
	}
	if snapshotRoot == "" {
		return service, nil
	}
	local, err := NewSkillService(snapshotRoot)
	if err != nil {
		return nil, err
	}
	service.root = local.root
	for _, key := range local.order {
		if isPlatformSkillKey(key) {
			continue
		}
		service.skills[key] = local.skills[key]
		service.order = append(service.order, key)
	}
	return service, nil
}

func loadPlatformSkill(definition platformSkill) (skillSnapshotSkill, map[string][]byte, error) {
	indexedFiles := make([]skillSnapshotFile, 0, 8)
	contentsByPath := map[string][]byte{}
	var totalSize int64
	err := fs.WalkDir(definition.Files, ".", func(
		name string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxSkillRegularFileBytes {
			return ErrSkillSnapshotUnavailable
		}
		if definition.Exclude != nil && definition.Exclude(name) {
			return nil
		}
		if validateSkillRelativePath(name) != nil {
			return ErrSkillSnapshotUnavailable
		}
		contents, err := fs.ReadFile(definition.Files, name)
		if err != nil || int64(len(contents)) != info.Size() || !utf8.Valid(contents) {
			return ErrSkillSnapshotUnavailable
		}
		totalSize += info.Size()
		if totalSize > maxSkillPackageBytes || len(indexedFiles) == maxSkillFileCount {
			return ErrSkillSnapshotUnavailable
		}
		digest := sha256.Sum256(contents)
		indexedFiles = append(indexedFiles, skillSnapshotFile{
			Path: name,
			Hash: hex.EncodeToString(digest[:]),
			Size: info.Size(),
			Text: true,
		})
		contentsByPath[name] = contents
		return nil
	})
	if err != nil {
		return skillSnapshotSkill{}, nil, errors.Join(ErrSkillSnapshotUnavailable, err)
	}
	slices.SortFunc(indexedFiles, func(left, right skillSnapshotFile) int {
		return strings.Compare(left.Path, right.Path)
	})
	instructions, ok := contentsByPath["SKILL.md"]
	if !ok || !bytes.Contains(instructions, []byte("name: "+definition.Key+"\n")) ||
		!bytes.Contains(instructions, []byte("description: "+definition.Description+"\n")) {
		return skillSnapshotSkill{}, nil, fmt.Errorf(
			"%w: SKILL.md must carry the lines \"name: %s\" and the pinned \"description: ...\" byte for byte",
			ErrSkillSnapshotUnavailable,
			definition.Key,
		)
	}
	packageHash := sha256.New()
	for _, indexedFile := range indexedFiles {
		if err := writeSkillPackageHashMetadata(packageHash, indexedFile); err != nil {
			return skillSnapshotSkill{}, nil, err
		}
		if _, err := packageHash.Write(contentsByPath[indexedFile.Path]); err != nil {
			return skillSnapshotSkill{}, nil, ErrSkillSnapshotUnavailable
		}
	}
	skill := skillSnapshotSkill{
		Key:         definition.Key,
		Name:        definition.Name,
		Description: definition.Description,
		ContentHash: hex.EncodeToString(packageHash.Sum(nil)),
		Files:       indexedFiles,
	}
	if !validSkillDescription(skill.Description) || len(skill.Files) == 0 {
		return skillSnapshotSkill{}, nil, ErrSkillSnapshotUnavailable
	}
	return skill, contentsByPath, nil
}
