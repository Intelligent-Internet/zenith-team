package workflow

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"

	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/internal/platformbundle"
)

// Every shipped Workflow must parse, and its playbook must be a reference a
// Task Run can load from the pinned zenith-workflows skill.
func TestShippedCatalogIsValidAndEveryPlaybookIsLoadable(t *testing.T) {
	summaries, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) == 0 {
		t.Fatal("List() returned an empty catalog")
	}
	skills, err := platformbundle.PlatformSkillSummaries()
	if err != nil {
		t.Fatalf("PlatformSkillSummaries() error = %v", err)
	}
	var references []string
	for _, skill := range skills {
		if skill.Key == tools.ZenithWorkflowsSkillKey {
			references = skill.References
		}
	}
	for index, summary := range summaries {
		if index > 0 && strings.ToLower(summaries[index-1].Name) > strings.ToLower(summary.Name) {
			t.Fatalf("List() is not ordered by name at %q", summary.Name)
		}
		workflow, err := Get(summary.Key)
		if err != nil {
			t.Fatalf("Get(%q) error = %v", summary.Key, err)
		}
		if workflow.Playbook.SkillKey != tools.ZenithWorkflowsSkillKey ||
			!slices.Contains(references, workflow.Playbook.Path) {
			t.Fatalf("Get(%q).Playbook = %+v is not a loadable reference", summary.Key, workflow.Playbook)
		}
		if workflow.Recurring != (workflow.Schedule != nil) {
			t.Fatalf("Get(%q).Recurring disagrees with its Schedule", summary.Key)
		}
	}
}

// Each shipped directory parses on its own, so an author can check one
// Workflow with -run 'TestEachShippedWorkflowParses/<key>' while others are
// still being written.
func TestEachShippedWorkflowParses(t *testing.T) {
	root, _, err := platformbundle.WorkflowCatalog()
	if err != nil {
		t.Fatalf("WorkflowCatalog() error = %v", err)
	}
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			if _, err := parseWorkflow(root, entry.Name()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGetUnknownWorkflowIsNotFound(t *testing.T) {
	if _, err := Get("no-such-workflow"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
}

const validWorkflowYAML = `schemaVersion: 1
key: sample
name: Sample workflow
description: Does one useful thing.
categories: [research]
icon: search-zoom-in-1
milestoneType: positioning
highlights: [One, Two, Three]
deliverables: [report]
submitLabel: Run it
titleField: company
pages:
  - title: Details
    fields:
      - { key: company, type: text, label: Company, required: true }
      - key: depth
        type: select
        label: Depth
        required: true
        default: quick
        options:
          - { value: quick, label: Quick }
          - { value: deep, label: Deep }
brief: |
  Research {{company}} at {{ depth }} depth.
`

func catalogFS(files map[string]string) fstest.MapFS {
	result := fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: zenith-workflows\n---\n")}}
	for name, contents := range files {
		result[name] = &fstest.MapFile{Data: []byte(contents)}
	}
	return result
}

func TestParseCatalogAcceptsAWellFormedWorkflow(t *testing.T) {
	catalog, err := parseCatalog(catalogFS(map[string]string{
		"sample/workflow.yaml": validWorkflowYAML,
		"sample/playbook.md":   "# Sample\n",
	}))
	if err != nil {
		t.Fatalf("parseCatalog() error = %v", err)
	}
	got := catalog[0]
	if got.Key != "sample" || got.Recurring || got.Playbook.Path != "sample/playbook.md" ||
		got.MilestoneType != "positioning" || len(got.Connectors) != 0 || got.Connectors == nil {
		t.Fatalf("parseCatalog() = %+v", got)
	}
}

// CheckCatalog reports every problem at once, one per failing Workflow plus
// catalog-level ones, while the server's parse still stops at the first.
func TestCheckCatalogReportsEveryFailingWorkflow(t *testing.T) {
	badIcon := strings.Replace(validWorkflowYAML, "icon: search-zoom-in-1", "icon: rocket", 1)
	root := catalogFS(map[string]string{
		"sample/workflow.yaml":   validWorkflowYAML,
		"sample/playbook.md":     "# Sample\n",
		"no-title/workflow.yaml": strings.ReplaceAll(validWorkflowYAML, "sample", "no-title"),
		"no-title/playbook.md":   "Sample without a title line\n",
		"bad-icon/workflow.yaml": strings.ReplaceAll(badIcon, "sample", "bad-icon"),
		"bad-icon/playbook.md":   "# Bad icon\n",
		"README.md":              "stray\n",
	})
	problems := CheckCatalog(root)
	if len(problems) != 3 {
		t.Fatalf("CheckCatalog() = %v, want 3 problems", problems)
	}
	for index, want := range []string{`unexpected file "README.md"`, `workflow "bad-icon"`, `workflow "no-title"`} {
		if !strings.Contains(problems[index].Error(), want) {
			t.Fatalf("CheckCatalog()[%d] = %v, want it to mention %s", index, problems[index], want)
		}
	}
	if _, err := parseCatalog(root); err == nil || err.Error() != problems[0].Error() {
		t.Fatalf("parseCatalog() error = %v, want the first problem %v", err, problems[0])
	}
	if problems := CheckCatalog(catalogFS(map[string]string{
		"sample/workflow.yaml": validWorkflowYAML,
		"sample/playbook.md":   "# Sample\n",
	})); len(problems) != 0 {
		t.Fatalf("CheckCatalog(valid) = %v", problems)
	}
}

// Each malformed catalog must be refused with a message naming the problem,
// so an author sees exactly what to fix.
func TestParseCatalogRefusesMalformedWorkflows(t *testing.T) {
	replace := func(from, to string) string { return strings.Replace(validWorkflowYAML, from, to, 1) }
	sample := func(yaml string) map[string]string {
		return map[string]string{"sample/workflow.yaml": yaml, "sample/playbook.md": "# S\n"}
	}
	withSchedule := func(cron, instructions string) map[string]string {
		return sample(validWorkflowYAML + "schedule:\n  cron: \"" + cron + "\"\n  instructions: " + instructions + "\n")
	}
	// withApps gives the sample two connectors plus the requirement lines.
	withApps := func(requires string) string {
		return replace("submitLabel: Run it\n", "connectors: [gmail, google_drive]\n"+requires+"submitLabel: Run it\n")
	}
	manyFields := strings.Repeat("      - { key: extra_%d, type: text, label: Extra, required: false }\n", 1)
	var sixteenMore strings.Builder
	for index := range 15 {
		sixteenMore.WriteString(strings.Replace(manyFields, "%d", string(rune('a'+index)), 1))
	}
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"unknown yaml field", sample(validWorkflowYAML + "extra: true\n"), "field extra not found"},
		{"second yaml document", sample(validWorkflowYAML + "---\nkey: other\n"), "exactly one YAML document"},
		{"schema version", sample(replace("schemaVersion: 1", "schemaVersion: 2")), "schemaVersion must be 1"},
		{"key differs from directory", map[string]string{"other/workflow.yaml": validWorkflowYAML, "other/playbook.md": "# S\n"}, "must equal its directory"},
		{"missing playbook", map[string]string{"sample/workflow.yaml": validWorkflowYAML}, "exactly playbook.md and workflow.yaml"},
		{"stray file", map[string]string{"sample/workflow.yaml": validWorkflowYAML, "sample/playbook.md": "# S\n", "sample/notes.txt": "x"}, "exactly playbook.md and workflow.yaml"},
		{"playbook without title", map[string]string{"sample/workflow.yaml": validWorkflowYAML, "sample/playbook.md": "no title"}, `"# " title line`},
		{"playbook too large", map[string]string{"sample/workflow.yaml": validWorkflowYAML, "sample/playbook.md": "# S\n" + strings.Repeat("x", 64<<10)}, "UTF-8 text of 1 to"},
		{"placeholder names no field", sample(replace("{{company}}", "{{firm}}")), "{{firm}} names no field"},
		{"field never placed", sample(replace("{{ depth }}", "some")), `field "depth" is never placed`},
		{"placeholder across lines", sample(replace("{{ depth }}", "{{\n    depth\n    }}")), "stray braces"},
		{"unbalanced braces", sample(replace("depth.", "depth {{company}.")), "stray braces"},
		{"unknown icon", sample(replace("search-zoom-in-1", "lucide-radar")), "not a bundled workflow icon"},
		{"unknown category", sample(replace("[research]", "[health]")), "categories"},
		{"too many deliverables", sample(replace("[report]", "[report, pdf, csv, image, video]")), "deliverables must hold 1 to 4"},
		{"unknown milestone", sample(replace("positioning", "fundraising")), "not a Task milestone"},
		{"two highlights", sample(replace("[One, Two, Three]", "[One, Two]")), "exactly 3"},
		{"untrimmed label", sample(replace("label: Deep }", "label: \" Deep\" }")), "one trimmed line"},
		{"control character", sample(replace("label: Deep }", "label: \"De\\u0007ep\" }")), "one trimmed line"},
		{"default outside options", sample(replace("default: quick", "default: slow")), "must be one of its options"},
		{"one option", sample(replace("          - { value: deep, label: Deep }\n", "")), "needs 2 to 30 options"},
		{"duplicate option value", sample(replace("value: deep", "value: quick")), "unique lowercase code"},
		{"defaults on a select", sample(replace("default: quick", "defaults: [quick]")), "distinct options of a multi_select"},
		{"repeated multi_select defaults", sample(strings.Replace(replace("type: select", "type: multi_select"), "default: quick", "defaults: [quick, quick]", 1)), "distinct options of a multi_select"},
		{"impossible date default", sample(replace("type: text, label: Company, required: true", "type: text, label: Company, required: true }\n      - { key: due, type: date, label: Due, required: false, default: 2024-13-45")), "real YYYY-MM-DD date"},
		{"non-web url default", sample(replace("type: text, label: Company, required: true", "type: text, label: Company, required: true }\n      - { key: site, type: url, label: Site, required: false, default: not-a-url")), "absolute http or https URL"},
		{"duplicate field key", sample(replace("key: depth", "key: company")), "duplicate key"},
		{"too many fields", sample(replace("      - key: depth\n", sixteenMore.String()+"      - key: depth\n")), "at most 16 fields"},
		{"optional title field", sample(replace("label: Company, required: true", "label: Company, required: false")), "must name a required field"},
		{"title field names no field", sample(replace("titleField: company", "titleField: firm")), "must name a text, url or select field"},
		{"cron shorthand", withSchedule("@daily", "Do {{company}}."), "not a valid Schedule cron"},
		{"cron day names", withSchedule("0 8 * * MON", "Do {{company}}."), "not a valid Schedule cron"},
		{"cron every minute", withSchedule("* * * * *", "Do {{company}}."), "not a valid Schedule cron"},
		{"cron never fires", withSchedule("0 8 31 2 *", "Do {{company}}."), "not a valid Schedule cron"},
		{"file field type", sample(replace("type: text", "type: file")), `type "file" is not supported`},
		{"files on a text field", sample(replace("label: Company, required: true", "label: Company, required: true, acceptsFiles: true")), "acceptsFiles belongs to a textarea field"},
		{"files on a recurring workflow", sample(strings.Replace(validWorkflowYAML, "      - key: depth\n",
			"      - { key: notes, type: textarea, label: Notes, required: false, acceptsFiles: true }\n      - key: depth\n", 1) +
			"schedule:\n  cron: \"0 8 * * 1\"\n  instructions: Check {{company}} weekly.\n"), "a recurring workflow cannot take files"},
		{"requirement app not a connector", sample(withApps("requires:\n  - anyOf: [slack]\n")), "must be one of the workflow's connectors"},
		{"requirement with no app", sample(withApps("requires:\n  - anyOf: []\n")), "anyOf must hold 1 to 4"},
		{"app in two requirements", sample(withApps("requires:\n  - anyOf: [gmail]\n  - anyOf: [gmail, google_drive]\n")), "already belongs to another requirement"},
		{"four requirements", sample(withApps("requires:\n  - anyOf: [gmail]\n  - anyOf: [google_drive]\n  - anyOf: [slack]\n  - anyOf: [notion]\n")), "at most 3 requirements"},
		{"requirement orField names no field", sample(withApps("requires:\n  - { anyOf: [gmail], orField: export }\n")), `orField "export" names no field`},
		{"requirement orField is required", sample(withApps("requires:\n  - { anyOf: [gmail], orField: company }\n")), "must name an optional field"},
		{"requirement orField is a choice", sample(strings.Replace(withApps("requires:\n  - { anyOf: [gmail], orField: depth }\n"), "required: true\n        default: quick", "required: false\n        default: quick", 1)), "must name a textarea or url field"},
		{"label too long for the Task label", map[string]string{
			"a-very-long-workflow-key-that-cannot-fit/workflow.yaml": replace("key: sample", "key: a-very-long-workflow-key-that-cannot-fit"),
			"a-very-long-workflow-key-that-cannot-fit/playbook.md":   "# S\n",
		}, "at most 39 characters"},
		{"duplicate name", map[string]string{
			"sample/workflow.yaml": validWorkflowYAML, "sample/playbook.md": "# S\n",
			"second/workflow.yaml": replace("key: sample", "key: second"), "second/playbook.md": "# S\n",
		}, "already used"},
		{"stray top-level file", map[string]string{"README.md": "x", "sample/workflow.yaml": validWorkflowYAML, "sample/playbook.md": "# S\n"}, "unexpected file"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseCatalog(catalogFS(testCase.files))
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("parseCatalog() error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func asStrings[T ~string](values []T) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

// The closed sets the parser enforces are the OpenAPI enums the client is
// generated from, in the same order (the order is the page order), and every
// icon is a glyph the client actually bundles.
func TestClosedSetsMatchTheAPIContract(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/api/spec/workflows.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var spec struct {
		Schemas map[string]struct {
			Enum []string `yaml:"enum"`
		} `yaml:"schemas"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	for schema, got := range map[string][]string{
		"WorkflowCategory":        asStrings(categoryTypes),
		"WorkflowDeliverableType": asStrings(deliverableTypes),
		"WorkflowConnectorType":   asStrings(connectorTypes),
		"WorkflowFieldType":       asStrings(fieldTypes),
		"WorkflowIconType":        asStrings(iconTypes),
	} {
		if want := spec.Schemas[schema].Enum; !slices.Equal(got, want) {
			t.Errorf("%s: parser has %v, spec has %v", schema, got, want)
		}
	}
	for _, icon := range iconTypes {
		if _, err := os.Stat("../../../frontend/src/assets/iconsax/" + string(icon) + ".svg"); err != nil {
			t.Errorf("icon %q has no bundled Iconsax glyph: %v", icon, err)
		}
	}
}

func TestParseCatalogAcceptsARecurringWorkflow(t *testing.T) {
	catalog, err := parseCatalog(catalogFS(map[string]string{
		"sample/workflow.yaml": validWorkflowYAML + "schedule:\n  cron: \"0 8 * * 1\"\n  instructions: Check {{company}} weekly.\n",
		"sample/playbook.md":   "# Sample\n",
	}))
	if err != nil {
		t.Fatalf("parseCatalog() error = %v", err)
	}
	if !catalog[0].Recurring || catalog[0].Schedule.Cron != "0 8 * * 1" {
		t.Fatalf("parseCatalog() = %+v", catalog[0])
	}
}

// A textarea may take files in place of, or beside, the pasted text; the
// launched Task carries them to its Runs.
func TestParseCatalogAcceptsATextareaThatTakesFiles(t *testing.T) {
	yaml := strings.Replace(validWorkflowYAML, "      - key: depth\n",
		"      - { key: contract, type: textarea, label: Contract, required: true, acceptsFiles: true }\n      - key: depth\n", 1)
	yaml = strings.Replace(yaml, "brief: |\n  Research {{company}} at {{ depth }} depth.\n",
		"brief: |\n  Research {{company}} at {{ depth }} depth.\n  Contract: {{contract}}\n", 1)
	catalog, err := parseCatalog(catalogFS(map[string]string{
		"sample/workflow.yaml": yaml,
		"sample/playbook.md":   "# Sample\n",
	}))
	if err != nil {
		t.Fatalf("parseCatalog() error = %v", err)
	}
	fields := catalog[0].Pages[0].Fields
	if len(fields) != 3 || fields[1].Key != "contract" || !fields[1].AcceptsFiles || fields[0].AcceptsFiles {
		t.Fatalf("Fields = %+v", fields)
	}
}

func TestParseCatalogAcceptsRequirements(t *testing.T) {
	yaml := strings.Replace(validWorkflowYAML, "submitLabel: Run it\n",
		"connectors: [gmail, google_drive, slack]\n"+
			"requires:\n  - anyOf: [gmail]\n  - { anyOf: [google_drive], orField: notes }\n"+
			"submitLabel: Run it\n", 1)
	yaml = strings.Replace(yaml, "brief: |\n  Research {{company}} at {{ depth }} depth.\n",
		"brief: |\n  Research {{company}} at {{ depth }} depth.\n  Notes: {{notes}}\n", 1)
	yaml = strings.Replace(yaml, "      - key: depth\n",
		"      - { key: notes, type: textarea, label: Notes, required: false }\n      - key: depth\n", 1)
	catalog, err := parseCatalog(catalogFS(map[string]string{
		"sample/workflow.yaml": yaml,
		"sample/playbook.md":   "# Sample\n",
	}))
	if err != nil {
		t.Fatalf("parseCatalog() error = %v", err)
	}
	got := catalog[0].Requirements
	if len(got) != 2 || !slices.Equal(got[0].AnyOf, []ConnectorType{"gmail"}) || got[0].OrField != "" ||
		!slices.Equal(got[1].AnyOf, []ConnectorType{"google_drive"}) || got[1].OrField != "notes" {
		t.Fatalf("Requirements = %+v", got)
	}
}
