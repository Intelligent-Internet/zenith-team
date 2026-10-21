package companyplan

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseActionsNormalizesAndDigestsSemanticContent(t *testing.T) {
	t.Parallel()
	first := []byte(`kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - id: update_task
    operation: update
    entity: task
    key: task
    set:
      priority: urgent
`)
	second := []byte(`# formatting does not matter
kind: company_plan_transaction
format_version: 5
company: "acme"
actions:
  - id: update_task
    operation: update
    entity: task
    key: task
    set: {priority: urgent}
`)
	a, digestA, err := ParseActions(first)
	if err != nil {
		t.Fatalf("ParseActions(first) error = %v", err)
	}
	_, digestB, err := ParseActions(second)
	if err != nil {
		t.Fatalf("ParseActions(second) error = %v", err)
	}
	if digestA != digestB || a.Company != "acme" || len(a.Actions) != 1 {
		t.Fatalf("digests/document = %q %q %+v", digestA, digestB, a)
	}
}

func TestParseActionsNormalizesClippedDescriptions(t *testing.T) {
	t.Parallel()

	clipped := `kind: company_plan_transaction
format_version: 5
company: acme
description: |
  Create the launch plan.
actions:
  - id: create_task
    operation: create
    entity: task
    key: launch
    values:
      project_key: work
      agent_id: 0198aa00-0000-7000-8000-00000000a002
      milestone_type: product
      priority: high
      title: Launch
      description: |
        Ship the first customer-ready release.
`
	stripped := strings.ReplaceAll(clipped, "description: |\n", "description: |-\n")

	clippedDocument, clippedDigest, err := ParseActions([]byte(clipped))
	if err != nil {
		t.Fatalf("ParseActions(clipped) error = %v", err)
	}
	strippedDocument, strippedDigest, err := ParseActions([]byte(stripped))
	if err != nil {
		t.Fatalf("ParseActions(stripped) error = %v", err)
	}
	if clippedDigest != strippedDigest {
		t.Fatalf("ParseActions() digests = (%q, %q), want equal", clippedDigest, strippedDigest)
	}
	if clippedDocument.Description != strippedDocument.Description ||
		strings.HasSuffix(clippedDocument.Description, "\n") {
		t.Fatal("ParseActions() document descriptions differ or retain a trailing newline")
	}
	clippedDescription := clippedDocument.Actions[0].Values.Description
	strippedDescription := strippedDocument.Actions[0].Values.Description
	if clippedDescription == nil || strippedDescription == nil ||
		*clippedDescription != *strippedDescription || strings.HasSuffix(*clippedDescription, "\n") {
		t.Fatal("ParseActions() action descriptions differ or retain a trailing newline")
	}
}

func TestParseActionsReportsInvalidCreateField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		values   string
		contains string
	}{
		{
			name:     "missing Task title",
			values:   `entity: task, key: task, values: {project_key: work, agent_id: 0198aa00-0000-7000-8000-00000000a002, milestone_type: product, description: "Do the task.", priority: high}`,
			contains: "create Task title is invalid or missing",
		},
		{
			name:     "invalid agent id",
			values:   `entity: task, key: task, values: {project_key: work, agent_id: legal, milestone_type: product, title: Task, description: "Do the task.", priority: high}`,
			contains: `create Task agent_id has unsupported value "legal"`,
		},
		{
			name:     "retired execution type",
			values:   `entity: task, key: task, values: {project_key: work, agent_id: 0198aa00-0000-7000-8000-00000000a002, milestone_type: product, execution_type: robot, title: Task, description: "Do the task.", priority: high}`,
			contains: `field execution_type not found`,
		},
		{
			name:     "missing priority",
			values:   `entity: task, key: task, values: {project_key: work, agent_id: 0198aa00-0000-7000-8000-00000000a002, milestone_type: product, title: Task, description: "Do the task."}`,
			contains: "create Task priority is invalid or missing",
		},
		{
			name:     "invalid priority",
			values:   `entity: task, key: task, values: {project_key: work, agent_id: 0198aa00-0000-7000-8000-00000000a002, milestone_type: product, title: Task, description: "Do the task.", priority: someday}`,
			contains: `create Task priority has unsupported value "someday"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yaml := fmt.Sprintf(
				"kind: company_plan_transaction\nformat_version: 5\ncompany: acme\nactions:\n  - {id: change, operation: create, %s}\n",
				test.values,
			)
			_, _, err := ParseActions([]byte(yaml))
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("ParseActions() error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestApplyActionsCreatesAndReplacesTaskDependencies(t *testing.T) {
	t.Parallel()
	transaction, _, err := ParseActions([]byte(`kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - id: create_dependent
    operation: create
    entity: task
    key: dependent
    values:
      project_key: work
      title: Dependent
      description: Build the dependent task.
      priority: high
      agent_id: 0198aa00-0000-7000-8000-00000000a002
      milestone_type: product
      depends_on:
        - {task_key: prerequisite}
  - id: clear_existing
    operation: update
    entity: task
    key: existing
    set:
      depends_on: []
`))
	if err != nil {
		t.Fatalf("ParseActions() error = %v", err)
	}
	current := Document{
		Projects:      []Project{{Key: "work", Title: "Work"}},
		FormatVersion: FormatVersion, Company: "acme", Tasks: []Task{
			{
				ProjectKey: "work",
				Key:        "prerequisite",
				Title:      "Prerequisite",
				Status:     "todo",
				Priority:   "high",
				DependsOn:  []TaskDependency{},
			},
			{
				ProjectKey: "work",
				Key:        "existing",
				Title:      "Existing",
				Status:     "todo",
				Priority:   "high",
				DependsOn:  []TaskDependency{{TaskKey: "prerequisite"}},
			},
		},
	}
	desired, err := ApplyActions(current, transaction)
	if err != nil {
		t.Fatalf("ApplyActions() error = %v", err)
	}
	flat := flatten(desired)
	if got := flat.Tasks["dependent"].DependsOn; len(got) != 1 || got[0].TaskKey != "prerequisite" {
		t.Fatalf("created depends_on = %+v", got)
	}
	if got := flat.Tasks["existing"].DependsOn; got == nil || len(got) != 0 {
		t.Fatalf("cleared depends_on = %#v, want non-nil empty list", got)
	}
}

func TestValidateAddedDependenciesRejectsNewCancelledPrerequisiteButAllowsExistingEdge(t *testing.T) {
	t.Parallel()
	current := dependencyGraphDocument()
	current.Tasks[0].Status = "cancelled"
	desired := dependencyGraphDocument()
	desired.Tasks[0].Status = "cancelled"
	desired.Tasks[1].DependsOn = []TaskDependency{{TaskKey: "product_spec"}}
	if err := validateAddedDependencies(
		current,
		desired,
	); err == nil ||
		!strings.Contains(err.Error(), "dependency_prerequisite_cancelled: engineering_build -> product_spec") {
		t.Fatalf("validateAddedDependencies(new edge) error = %v", err)
	}
	current = desired
	if err := validateAddedDependencies(current, desired); err != nil {
		t.Fatalf("validateAddedDependencies(existing edge) error = %v", err)
	}
}

func TestParseActionsRejectsInvalidDependencyShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, fields, contains string
	}{
		{
			"unsupported class",
			"entity: task, key: task, values: {project_key: work, agent_id: 0198aa00-0000-7000-8000-00000000a002, milestone_type: product, title: Task, description: 'Do the task.', priority: high, depends_on: [{task_key: prerequisite, class: hard}]}",
			"field class",
		},
		{
			"missing key",
			"entity: task, key: task, values: {project_key: work, agent_id: 0198aa00-0000-7000-8000-00000000a002, milestone_type: product, title: Task, description: 'Do the task.', priority: high, depends_on: [{task_key: ''}]}",
			"task_key",
		},
		{
			"duplicate",
			"entity: task, key: task, values: {project_key: work, agent_id: 0198aa00-0000-7000-8000-00000000a002, milestone_type: product, title: Task, description: 'Do the task.', priority: high, depends_on: [{task_key: prerequisite}, {task_key: prerequisite}]}",
			"duplicate dependency",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yaml := fmt.Sprintf(
				"kind: company_plan_transaction\nformat_version: 5\ncompany: acme\nactions:\n  - {id: change, operation: create, %s}\n",
				test.fields,
			)
			if _, _, err := ParseActions(
				[]byte(yaml),
			); err == nil ||
				!strings.Contains(err.Error(), test.contains) {
				t.Fatalf("ParseActions() error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestParseActionsRejectsUnknownStatusDeleteAndInvalidShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, yaml, contains string
	}{
		{
			"unknown",
			"kind: company_plan_transaction\nformat_version: 5\ncompany: acme\nactions: []\nextra: true\n",
			"field extra",
		},
		{"duplicate target", `kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - {id: one, operation: cancel, entity: task, key: task}
  - {id: two, operation: cancel, entity: task, key: task}
`, "duplicate target"},
		{"empty update", `kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - {id: one, operation: update, entity: task, key: task, set: {}}
`, "non-empty set"},
		{"status is read only", `kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - {id: one, operation: update, entity: task, key: task, set: {status: done}}
`, "field status not found"},
		{"create status is read only", `kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - id: one
    operation: create
    entity: task
    key: task
    values: {project_key: work, title: Task, description: "Do the task.", status: todo}
`, "field status not found"},
		{"delete is unavailable", `kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - {id: one, operation: delete, entity: task, key: task}
`, "operation is invalid"},
		{"cascade is unavailable", `kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - {id: one, operation: cancel, entity: task, key: task, cascade: true}
`, "field cascade not found"},
		{
			"alias",
			"kind: company_plan_transaction\nformat_version: 5\ncompany: acme\nactions: &actions []\ndescription: *actions\n",
			"aliases are not supported",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := ParseActions([]byte(test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("ParseActions() error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestApplyActionsCreateOrderAndBackendDefaults(t *testing.T) {
	t.Parallel()
	transaction, _, err := ParseActions([]byte(`kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - id: task_first
    operation: create
    entity: task
    key: task
    values: {project_key: work, agent_id: 0198aa00-0000-7000-8000-00000000a002, milestone_type: product, title: Task, description: "Do the task.", priority: medium, depends_on: [{task_key: prerequisite}]}
  - id: prerequisite_last
    operation: create
    entity: task
    key: prerequisite
    values: {project_key: work, agent_id: 0198aa00-0000-7000-8000-00000000a003, milestone_type: product, title: Prerequisite, description: "Define the work.", priority: medium}
`))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := ApplyActions(
		Document{
			Projects:      []Project{{Key: "work", Title: "Work"}},
			FormatVersion: FormatVersion,
			Company:       "acme",
			Tasks:         []Task{},
		},
		transaction,
	)
	if err != nil {
		t.Fatalf("ApplyActions() error = %v", err)
	}
	flat := flatten(desired)
	if flat.Tasks["prerequisite"].Status != "backlog" || flat.Tasks["task"].Status != "backlog" {
		t.Fatalf("default statuses = %+v", flat.Tasks)
	}
	if got := flat.Tasks["task"].DependsOn; len(got) != 1 || got[0].TaskKey != "prerequisite" {
		t.Fatalf("created prerequisite = %+v", got)
	}
}

func TestApplyActionsUsesCreateUpdateAndCancel(t *testing.T) {
	t.Parallel()
	current := Document{
		Projects:      []Project{{Key: "work", Title: "Work"}},
		FormatVersion: FormatVersion, Company: "acme", Tasks: []Task{
			{ProjectKey: "work", Key: "keep", Title: "Keep", Status: "todo", Priority: "medium"},
			{ProjectKey: "work", Key: "cancel", Title: "Cancel", Status: "todo", Priority: "low"},
		},
	}
	transaction, _, err := ParseActions([]byte(`kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - id: create_task
    operation: create
    entity: task
    key: next
    values: {project_key: work, agent_id: 0198aa00-0000-7000-8000-00000000a002, milestone_type: product, title: Next, description: "Create the next deliverable.", priority: high}
  - id: update_task
    operation: update
    entity: task
    key: keep
    set: {priority: urgent}
  - {id: cancel_task, operation: cancel, entity: task, key: cancel}
`))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := ApplyActions(current, transaction)
	if err != nil {
		t.Fatalf("ApplyActions() error = %v", err)
	}
	flat := flatten(desired)
	if len(flat.Tasks) != 3 || flat.Tasks["next"].Status != "backlog" ||
		flat.Tasks["keep"].Priority != "urgent" ||
		flat.Tasks["cancel"].Status != "cancelled" {
		t.Fatalf("desired = %+v", desired)
	}
}

func TestApplyActionsUpdateEligibilityByState(t *testing.T) {
	t.Parallel()
	base := func(taskStatus string) Document {
		return Document{
			Projects:      []Project{{Key: "work", Title: "Work"}},
			FormatVersion: FormatVersion,
			Company:       "acme",
			Tasks: []Task{
				{
					ProjectKey: "work",
					Key:        "task",
					Title:      "Task",
					Status:     taskStatus,
					Priority:   "medium",
				},
			},
		}
	}
	allowed := []struct {
		name, entity, key, set string
		document               Document
	}{
		{"backlog task", "task", "task", "priority: high", base("backlog")},
		{"todo task", "task", "task", "priority: high", base("todo")},
	}
	for _, test := range allowed {
		t.Run("allows "+test.name, func(t *testing.T) {
			transaction, _, err := ParseActions(
				[]byte(
					fmt.Sprintf(
						"kind: company_plan_transaction\nformat_version: 5\ncompany: acme\nactions:\n  - {id: revise, operation: update, entity: %s, key: %s, set: {%s}}\n",
						test.entity,
						test.key,
						test.set,
					),
				),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyActions(test.document, transaction); err != nil {
				t.Fatalf("ApplyActions() error = %v", err)
			}
		})
	}

	rejected := []struct {
		name, entity, key, set, contains string
		document                         Document
	}{
		{
			"in progress task",
			"task",
			"task",
			"priority: high",
			"backlog or todo",
			base("in_progress"),
		},
		{
			"in review task",
			"task",
			"task",
			"priority: high",
			"backlog or todo",
			base("in_review"),
		},
		{
			"blocked task",
			"task",
			"task",
			"priority: high",
			"backlog or todo",
			base("blocked"),
		},
		{"done task", "task", "task", "priority: high", "backlog or todo", base("done")},
		{
			"cancelled task",
			"task",
			"task",
			"priority: high",
			"backlog or todo",
			base("cancelled"),
		},
	}
	for _, test := range rejected {
		t.Run("rejects "+test.name, func(t *testing.T) {
			transaction, _, err := ParseActions(
				[]byte(
					fmt.Sprintf(
						"kind: company_plan_transaction\nformat_version: 5\ncompany: acme\nactions:\n  - {id: revise, operation: update, entity: %s, key: %s, set: {%s}}\n",
						test.entity,
						test.key,
						test.set,
					),
				),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyActions(
				test.document,
				transaction,
			); err == nil ||
				!strings.Contains(err.Error(), test.contains) {
				t.Fatalf("ApplyActions() error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestApplyActionsAllowsOnlyDependencyRemovalForFailedAndErrorTasks(t *testing.T) {
	t.Parallel()
	base := func(status string) Document {
		return Document{
			Projects:      []Project{{Key: "work", Title: "Work"}},
			FormatVersion: FormatVersion, Company: "acme", Tasks: []Task{
				{
					ProjectKey:    "work",
					Key:           "prerequisite",
					AgentID:       "0198aa00-0000-7000-8000-00000000a003",
					MilestoneType: "product",
					Title:         "Prerequisite",
					Description:   "Define the work.",
					Status:        "done",
					Priority:      "medium",
					DependsOn:     []TaskDependency{},
				},
				{
					ProjectKey:    "work",
					Key:           "other",
					AgentID:       "0198aa00-0000-7000-8000-00000000a002",
					MilestoneType: "product",
					Title:         "Other",
					Description:   "Do other work.",
					Status:        "todo",
					Priority:      "medium",
					DependsOn:     []TaskDependency{},
				},
				{
					ProjectKey:    "work",
					Key:           "task",
					AgentID:       "0198aa00-0000-7000-8000-00000000a002",
					MilestoneType: "product",
					Title:         "Task",
					Description:   "Do the task.",
					Status:        status,
					Priority:      "medium",
					DependsOn:     []TaskDependency{{TaskKey: "prerequisite"}},
				},
			},
		}
	}
	transaction := func(t *testing.T, set string) ActionDocument {
		t.Helper()
		document, _, err := ParseActions([]byte(fmt.Sprintf(
			"kind: company_plan_transaction\nformat_version: 5\ncompany: acme\nactions:\n  - {id: revise, operation: update, entity: task, key: task, set: {%s}}\n",
			set,
		)))
		if err != nil {
			t.Fatal(err)
		}
		return document
	}

	for _, status := range []string{"failed", "error"} {

		t.Run("allows dependency removal from "+status, func(t *testing.T) {
			t.Parallel()
			desired, err := ApplyActions(base(status), transaction(t, "depends_on: []"))
			if err != nil {
				t.Fatalf("ApplyActions() error = %v", err)
			}
			if got := flatten(desired).Tasks["task"].DependsOn; got == nil || len(got) != 0 {
				t.Fatalf("depends_on = %#v, want non-nil empty list", got)
			}
		})

		rejected := []struct {
			name     string
			set      string
			contains string
		}{
			{
				name:     "dependency addition",
				set:      "depends_on: [{task_key: prerequisite}, {task_key: other}]",
				contains: "cannot add dependencies",
			},
			{
				name:     "routing change with dependency removal",
				set:      "agent_id: 0198aa00-0000-7000-8000-00000000a003, depends_on: []",
				contains: "can only remove dependencies",
			},
		}
		for _, test := range rejected {
			t.Run("rejects "+test.name+" from "+status, func(t *testing.T) {
				t.Parallel()
				_, err := ApplyActions(base(status), transaction(t, test.set))
				if err == nil || !strings.Contains(err.Error(), test.contains) {
					t.Fatalf("ApplyActions() error = %v, want containing %q", err, test.contains)
				}
			})
		}
	}
}

func TestApplyActionsRejectsCancellingTerminalEntity(t *testing.T) {
	t.Parallel()
	current := Document{
		Projects:      []Project{{Key: "work", Title: "Work"}},
		FormatVersion: FormatVersion,
		Company:       "acme", Tasks: []Task{{ProjectKey: "work", Key: "done", Title: "Done", Status: "done"}},
	}
	transaction, _, err := ParseActions([]byte(`kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - {id: cancel_done, operation: cancel, entity: task, key: done}
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyActions(current, transaction); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("ApplyActions() error = %v", err)
	}
}

func TestParseActionsAcceptsIndependentTasks(t *testing.T) {
	t.Parallel()

	transaction, _, err := ParseActions([]byte(`kind: company_plan_transaction
format_version: 5
company: zenith-team
actions:
  - id: build_product
    operation: create
    entity: task
    key: build_product
    values:
      project_key: work
      agent_id: 0198aa00-0000-7000-8000-00000000a002
      milestone_type: product
      title: Build the product
      description: Produce a working customer-ready product.
      priority: high
`))
	if err != nil || len(transaction.Actions) != 1 {
		t.Fatalf("ParseActions() = %+v, error = %v", transaction, err)
	}
	desired, err := ApplyActions(
		Document{
			Projects:      []Project{{Key: "work", Title: "Work"}},
			FormatVersion: FormatVersion,
			Company:       "zenith-team",
			Tasks:         []Task{},
		},
		transaction,
	)
	if err != nil {
		t.Fatalf("ApplyActions() error = %v", err)
	}
	task := desired.Tasks[0]
	if task.AgentID != "0198aa00-0000-7000-8000-00000000a002" || task.MilestoneType != "product" ||
		task.Status != "backlog" ||
		task.DependsOn == nil {
		t.Fatalf("ApplyActions() Task = %+v", task)
	}
}

func TestParseActionsRejectsRetiredPlanningFields(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, action, want string }{
		{"retired entity", "{id: old, operation: cancel, entity: goal, key: old}", "entity is invalid"},
		{"retired parent field", "{id: old, operation: update, entity: task, key: task, set: {goal_key: old}}", "field goal_key not found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := ParseActions(
				[]byte(
					"kind: company_plan_transaction\nformat_version: 5\ncompany: zenith-team\nactions:\n  - " + test.action + "\n",
				),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseActions() error = %v, want %q", err, test.want)
			}
		})
	}
}
