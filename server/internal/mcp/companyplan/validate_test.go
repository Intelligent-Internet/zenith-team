package companyplan

import (
	"strings"
	"testing"
)

func TestParseAndValidate(t *testing.T) {
	t.Parallel()
	document, summary, err := ParseAndValidate([]byte(`format_version: 5
company: boardly
projects:
  - key: work
    title: Work
    description: ""
    archived: false
tasks:
  - key: implement_api
    project_key: work
    agent_id: 0198aa00-0000-7000-8000-00000000a002
    milestone_type: product
    title: Implement API
    description: Add endpoints.
    status: todo
    priority: high
    depends_on: []
`))
	if err != nil {
		t.Fatalf("ParseAndValidate() error = %v", err)
	}
	if document.Company != "boardly" || summary != (Summary{Tasks: 1}) {
		t.Fatalf("document = %+v summary = %+v", document, summary)
	}
}

func TestParseAndValidateRejectsUnknownField(t *testing.T) {
	t.Parallel()
	_, _, err := ParseAndValidate([]byte(`format_version: 5
company: boardly
unknown: true
projects:
  - key: work
    title: Work
    description: ""
    archived: false
tasks: []
`))
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("ParseAndValidate() error = %v", err)
	}
}

func TestParseAndValidateRejectsDuplicateTaskKeys(t *testing.T) {
	t.Parallel()
	document := dependencyGraphDocument()
	document.Tasks[1].Key = document.Tasks[0].Key
	contents, err := marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseAndValidate(
		contents,
	); err == nil ||
		!strings.Contains(err.Error(), `duplicate Task key "product_spec"`) {
		t.Fatalf("ParseAndValidate() error = %v", err)
	}
}

func TestParseAndValidateRejectsMissingTaskList(t *testing.T) {
	t.Parallel()
	_, _, err := ParseAndValidate([]byte("format_version: 5\ncompany: boardly\nprojects: []\n"))
	if err == nil || !strings.Contains(err.Error(), "tasks must be a list") {
		t.Fatalf("ParseAndValidate() error = %v", err)
	}
}

func TestParseAndValidateRejectsInvalidEnums(t *testing.T) {
	t.Parallel()
	_, _, err := ParseAndValidate([]byte(`format_version: 5
company: boardly
projects:
  - key: work
    title: Work
    description: ""
    archived: false
tasks:
  - key: build_mvp
    project_key: work
    agent_id: 0198aa00-0000-7000-8000-00000000a002
    milestone_type: product
    title: Build MVP
    description: Build it.
    status: todo
    priority: critical
    depends_on: []
`))
	if err == nil || !strings.Contains(err.Error(), `priority has unsupported value "critical"`) {
		t.Fatalf("ParseAndValidate() error = %v", err)
	}
}

func TestValidateDocumentAcceptsCrossAgentDependencyGraph(t *testing.T) {
	t.Parallel()
	document := dependencyGraphDocument()
	document.Tasks[1].DependsOn = []TaskDependency{{TaskKey: "product_spec"}}
	if _, err := validateDocument(document); err != nil {
		t.Fatalf("validateDocument() error = %v", err)
	}
}

func TestValidateDocumentRejectsInvalidDependencyGraphWithPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutate   func(*Document)
		contains string
	}{
		{
			name: "missing prerequisite",
			mutate: func(document *Document) {
				document.Tasks[1].DependsOn = []TaskDependency{
					{TaskKey: "missing"},
				}
			},
			contains: "dependency_reference_not_found: engineering_build -> missing",
		},
		{
			name: "self edge",
			mutate: func(document *Document) {
				document.Tasks[1].DependsOn = []TaskDependency{
					{TaskKey: "engineering_build"},
				}
			},
			contains: "dependency_self_reference: engineering_build -> engineering_build",
		},
		{
			name: "cycle",
			mutate: func(document *Document) {
				document.Tasks[0].DependsOn = []TaskDependency{
					{TaskKey: "engineering_build"},
				}
				document.Tasks[1].DependsOn = []TaskDependency{
					{TaskKey: "product_spec"},
				}
			},
			contains: "dependency_cycle: engineering_build -> product_spec -> engineering_build",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			document := dependencyGraphDocument()
			test.mutate(&document)
			if _, err := validateDocument(
				document,
			); err == nil ||
				!strings.Contains(err.Error(), test.contains) {
				t.Fatalf("validateDocument() error = %v, want %q", err, test.contains)
			}
		})
	}
}

func dependencyGraphDocument() Document {
	return Document{Projects: []Project{{Key: "work", Title: "Work"}},
		FormatVersion: FormatVersion, Company: "acme", Tasks: []Task{
			{ProjectKey: "work",
				Key:           "product_spec",
				AgentID:       "0198aa00-0000-7000-8000-00000000a003",
				MilestoneType: "product", Title: "Product spec",
				Description: "Specify the product.",
				Status:      "todo",
				Priority:    "high",
				DependsOn:   []TaskDependency{},
			},
			{ProjectKey: "work",
				Key:           "engineering_build",
				AgentID:       "0198aa00-0000-7000-8000-00000000a002",
				MilestoneType: "product", Title: "Build",
				Description: "Build the product.",
				Status:      "todo",
				Priority:    "high",
				DependsOn:   []TaskDependency{},
			},
		},
	}
}

func TestParseAndValidateAcceptsTaskOnlyFormat(t *testing.T) {
	t.Parallel()

	document, summary, err := ParseAndValidate([]byte(`format_version: 5
company: boardly
projects:
  - key: work
    title: Work
    description: ""
    archived: false
tasks:
  - key: launch_website
    project_key: work
    agent_id: 0198aa00-0000-7000-8000-00000000a002
    milestone_type: product
    title: Build and deploy the launch website
    description: Produce the deployed customer-facing launch website.
    status: backlog
    priority: high
    depends_on: []
`))
	if err != nil || summary.Tasks != 1 {
		t.Fatalf("ParseAndValidate() summary = %+v, error = %v", summary, err)
	}
	task := document.Tasks[0]
	if task.AgentID != "0198aa00-0000-7000-8000-00000000a002" || task.MilestoneType != "product" {
		t.Fatalf("ParseAndValidate() Task routing = %+v", task)
	}
}

func TestParseAndValidateRejectsRetiredPlanningDocument(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, document, want string }{
		{"retired goal format", "format_version: 2\ncompany: boardly\ntasks: []\n", "unsupported format_version 2"},
		{"retired department format", "format_version: 3\ncompany: boardly\ntasks: []\n", "unsupported format_version 3"},
		{"retired department field", "format_version: 5\ncompany: boardly\ntasks:\n  - key: ship\n    department_key: engineering\n", "field department_key not found"},
		{"retired collection", "format_version: 5\ncompany: boardly\ngoals: []\ntasks: []\n", "field goals not found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := ParseAndValidate([]byte(test.document))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseAndValidate() error = %v, want %q", err, test.want)
			}
		})
	}
}
