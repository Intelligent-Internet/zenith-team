package tools

import (
	"errors"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

const fixtureWorkflowsDescription = "Use when your Task was launched from a Workflow; load its playbook."

// workflowsBundleFixture stands in for internal/platformbundle's pinned
// zenith-workflows skill: this package holds no Workflow content of its own.
func workflowsBundleFixture(instructions string) PlatformBundle {
	return PlatformBundle{
		Key:         ZenithWorkflowsSkillKey,
		Name:        "Zenith workflows",
		Description: fixtureWorkflowsDescription,
		Files: fstest.MapFS{
			"SKILL.md":                    {Data: []byte(instructions)},
			"account-brief/workflow.yaml": {Data: []byte("key: account-brief\n")},
			"account-brief/playbook.md":   {Data: []byte("# Account brief\n\nResearch the account.\n")},
		},
		Exclude: func(relativePath string) bool { return path.Base(relativePath) == "workflow.yaml" },
	}
}

func validWorkflowsInstructions() string {
	return "---\nname: zenith-workflows\ndescription: " + fixtureWorkflowsDescription + "\n---\n\n# Workflows\n"
}

// The app lists the platform skills by the same catalog the MCP server
// searches: the compiled skills first, then the pinned bundles, with their
// pinned descriptions and the reference files beside SKILL.md.
func TestPlatformSkillsListsCompiledThenPinnedSkills(t *testing.T) {
	t.Parallel()
	skills, err := PlatformSkills([]PlatformBundle{workflowsBundleFixture(validWorkflowsInstructions())})
	if err != nil {
		t.Fatalf("PlatformSkills() error = %v", err)
	}
	keys := make([]string, 0, len(skills))
	for _, skill := range skills {
		keys = append(keys, skill.Key)
	}
	if want := []string{
		ZenithPlatformSkillKey,
		zenithIntelligentUISkillKey,
		ZenithWorkflowsSkillKey,
	}; !slices.Equal(
		keys,
		want,
	) {
		t.Fatalf("PlatformSkills() keys = %v, want %v", keys, want)
	}
	// Only playbooks are references; the forms stay the page's input.
	if workflows := skills[2]; workflows.Name != "Zenith workflows" ||
		!slices.Equal(workflows.References, []string{"account-brief/playbook.md"}) {
		t.Fatalf("zenith-workflows summary = %+v", workflows)
	}
	skill := skills[0]
	if skill.Name != "Zenith platform" || !strings.Contains(skill.Description, "zen CLI") ||
		!slices.Contains(skill.References, "references/outputs.md") ||
		slices.Contains(skill.References, "SKILL.md") {
		t.Fatalf("zenith-platform summary = %+v", skill)
	}
	// Without bundles the compiled catalog still lists.
	compiled, err := PlatformSkills(nil)
	if err != nil || len(compiled) != 2 || compiled[0].Key != ZenithPlatformSkillKey {
		t.Fatalf("PlatformSkills(nil) = %+v, %v", compiled, err)
	}
}

// The server fails fast, so the listing refuses any bundle the MCP server
// could not serve under its key.
func TestPlatformSkillsRefusesABundleItCannotServe(t *testing.T) {
	t.Parallel()
	valid := workflowsBundleFixture(validWorkflowsInstructions())
	unreserved := valid
	unreserved.Key = "custom-skill"
	shadowing := valid
	shadowing.Key = ZenithPlatformSkillKey
	withoutFiles := valid
	withoutFiles.Files = nil
	for name, bundles := range map[string][]PlatformBundle{
		"unreserved key":        {unreserved},
		"compiled key":          {shadowing},
		"repeated key":          {valid, valid},
		"no files":              {withoutFiles},
		"frontmatter drift":     {workflowsBundleFixture(strings.Replace(validWorkflowsInstructions(), "Workflow;", "workflow;", 1))},
		"name line missing":     {workflowsBundleFixture("---\ndescription: " + fixtureWorkflowsDescription + "\n---\n")},
		"instructions not text": {workflowsBundleFixture(validWorkflowsInstructions() + "\xff")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if skills, err := PlatformSkills(bundles); err == nil {
				t.Fatalf("PlatformSkills() = %+v, want an error", skills)
			}
		})
	}
}

func TestOpenTaskSkillServiceServesAPinnedBundle(t *testing.T) {
	t.Parallel()
	service, err := OpenTaskSkillService("", []PlatformBundle{workflowsBundleFixture(validWorkflowsInstructions())},
		func(key string, err error) { t.Errorf("dropped %s: %v", key, err) })
	if err != nil {
		t.Fatalf("OpenTaskSkillService() error = %v", err)
	}
	index, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: ZenithWorkflowsSkillKey, Path: "SKILL.md", IncludeReferences: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(index.References, []string{"account-brief/playbook.md"}) {
		t.Fatalf("Workflow references = %v, want the exact readable playbook path", index.References)
	}
	loaded, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: ZenithWorkflowsSkillKey, Path: "account-brief/playbook.md",
	})
	if err != nil || !strings.HasPrefix(loaded.Content, "# Account brief") {
		t.Fatalf("LoadSkillReference(playbook) = %+v, %v", loaded, err)
	}
	if _, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: ZenithWorkflowsSkillKey, Path: "account-brief/workflow.yaml",
	}); err == nil {
		t.Fatal("a Workflow form was served as a skill reference")
	}
}

// A pinned bundle that fails to load no longer takes down every skill tool:
// it is dropped and reported, zenith-platform stays served, and its key
// stays reserved against the Run's snapshot.
func TestOpenTaskSkillServiceDropsAFailingBundle(t *testing.T) {
	t.Parallel()
	root := newSkillSnapshotFixture(t, []skillFixture{
		{key: "incident-response", description: "Handle incidents."},
		{key: ZenithWorkflowsSkillKey, description: "Untrusted replacement."},
	})
	broken := workflowsBundleFixture(strings.Replace(validWorkflowsInstructions(), "name: zenith-workflows",
		"name: zenith-workflow", 1))
	var dropped []string
	service, err := OpenTaskSkillService(root, []PlatformBundle{broken}, func(key string, err error) {
		// The reason names the frontmatter, so an operator can tell a drift
		// from a damaged file.
		if !errors.Is(err, ErrSkillSnapshotUnavailable) ||
			!strings.Contains(err.Error(), `"name: zenith-workflows"`) {
			t.Errorf("dropped %s with reason %v", key, err)
		}
		dropped = append(dropped, key)
	})
	if err != nil {
		t.Fatalf("OpenTaskSkillService() error = %v", err)
	}
	defer service.Close()
	if !slices.Equal(dropped, []string{ZenithWorkflowsSkillKey}) {
		t.Fatalf("dropped = %v, want [%s]", dropped, ZenithWorkflowsSkillKey)
	}
	procedure, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: ZenithPlatformSkillKey, Path: "SKILL.md",
	})
	if err != nil || !strings.Contains(procedure.Content, "Operating Zenith Team") {
		t.Fatalf("platform instructions = %v", err)
	}
	if _, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: ZenithWorkflowsSkillKey, Path: "SKILL.md",
	}); !errors.Is(err, ErrSkillReferenceUnavailable) {
		t.Fatalf("dropped key was served: %v", err)
	}
	result, err := service.SearchSkills(t.Context(), SearchSkillsInput{Query: "incident"})
	if err != nil || len(result.Skills) != 1 || result.Skills[0].Key != "incident-response" {
		t.Fatalf("snapshot merge = %+v, %v", result, err)
	}
}

// The reserved keys are a static floor: a snapshot entry named
// zenith-workflows is dropped even when no bundle is linked or verified.
func TestReservedKeysDoNotDependOnBundles(t *testing.T) {
	t.Parallel()
	root := newSkillSnapshotFixture(t, []skillFixture{
		{key: "incident-response", description: "Handle incidents."},
		{key: ZenithWorkflowsSkillKey, description: "Untrusted replacement."},
	})
	service, err := OpenTaskSkillService(root, nil, nil)
	if err != nil {
		t.Fatalf("OpenTaskSkillService() error = %v", err)
	}
	defer service.Close()
	if _, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: ZenithWorkflowsSkillKey, Path: "SKILL.md",
	}); !errors.Is(err, ErrSkillReferenceUnavailable) {
		t.Fatalf("snapshot took the reserved key: %v", err)
	}
	result, err := service.SearchSkills(t.Context(), SearchSkillsInput{Query: "replacement"})
	if err != nil || len(result.Skills) != 0 {
		t.Fatalf("snapshot entry under a reserved key is searchable: %+v, %v", result, err)
	}
}

func TestIntelligentUISkillIsAvailableWithoutCustomSkills(t *testing.T) {
	t.Parallel()
	service, err := OpenTaskSkillService("", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	found, err := service.SearchSkills(t.Context(), SearchSkillsInput{Query: "intelligent ui"})
	if err != nil || len(found.Skills) != 1 || found.Skills[0].Key != zenithIntelligentUISkillKey {
		t.Fatalf("search = %+v, %v", found, err)
	}
	loaded, err := service.LoadSkillReference(
		t.Context(),
		LoadSkillReferenceInput{SkillKey: found.Skills[0].Key, Path: "SKILL.md"},
	)
	if err != nil || loaded.Size == 0 || loaded.SHA256 == "" {
		t.Fatalf("load = %+v, %v", loaded, err)
	}
	if loaded.Content != IntelligentUIInstructions() {
		t.Fatal("restricted Runs and skill tools received different authoring guides")
	}
	if !isPlatformSkillKey(zenithIntelligentUISkillKey) {
		t.Fatal("custom snapshot could shadow authoring contract")
	}
}
