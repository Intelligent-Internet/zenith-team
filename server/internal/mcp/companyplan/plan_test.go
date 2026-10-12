package companyplan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// planAgentID is the Agent the fixture Tasks name.
const planAgentID = "0198aa00-0000-7000-8000-00000000a002"

func TestPlanBuildsVersionedArtifactAndChanges(t *testing.T) {
	t.Parallel()
	companyID := uuid.Must(uuid.NewV7()).String()
	api := fakeAPI{responses: map[string]any{
		"/companies": companyCollection{
			Items: []companyResponse{{ID: companyID, Slug: "acme"}},
		},
		"/companies/" + companyID + "/tasks": taskPage{Items: []taskResponse{}},
	}}
	desired := Document{Projects: []Project{{Key: "work", Title: "Work"}},
		FormatVersion: FormatVersion, Company: "acme", Tasks: []Task{{ProjectKey: "work",
			Key: "task", AgentID: planAgentID, MilestoneType: "product", Title: "Task", Description: "Do the task.", Status: "todo", Priority: "high",
			DependsOn: []TaskDependency{},
		}},
	}
	now := time.Date(2026, time.July, 21, 10, 0, 0, 0, time.UTC)
	artifact, err := Plan(context.Background(), api, desired, now)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if artifact.CompanyID != companyID || artifact.CreatedAt != now || len(artifact.Changes) != 1 ||
		len(artifact.Base.Tasks) != 0 {
		t.Fatalf("Plan() artifact = %+v", artifact)
	}
	if err := ValidateArtifact(artifact); err != nil {
		t.Fatalf("ValidateArtifact() error = %v", err)
	}
	// The change list is preview material for the human, never a request
	// field, and the server recomputes the authoritative diff — so the wire
	// artifact carries neither a change list nor a content digest.
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("encode artifact: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode artifact wire object: %v", err)
	}
	for _, retired := range []string{"changes", "desiredSha256"} {
		if _, exists := wire[retired]; exists {
			t.Fatalf("wire artifact carries retired field %q: %s", retired, encoded)
		}
	}
	var base, desiredWire map[string]json.RawMessage
	if err := json.Unmarshal(wire["base"], &base); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire["desired"], &desiredWire); err != nil {
		t.Fatal(err)
	}
	if _, exists := base["goals"]; exists {
		t.Fatalf("base carries retired collection: %s", wire["base"])
	}
	if _, exists := desiredWire["goals"]; exists {
		t.Fatalf("desired carries retired collection: %s", wire["desired"])
	}
	if string(desiredWire["formatVersion"]) != "5" || len(desiredWire["tasks"]) == 0 {
		t.Fatalf("desired is not a Project v5 document: %s", wire["desired"])
	}
}

func TestPlanBuildsDependencyBaseFromCurrentTaskDependencyResponse(t *testing.T) {
	t.Parallel()
	companyID := uuid.Must(uuid.NewV7()).String()
	prerequisiteID := uuid.Must(uuid.NewV7()).String()
	dependentID := uuid.Must(uuid.NewV7()).String()
	agentID := uuid.Must(uuid.NewV7()).String()
	api := fakeAPI{responses: map[string]any{
		"/companies": companyCollection{
			Items: []companyResponse{{ID: companyID, Slug: "acme"}},
		},
		"/companies/" + companyID + "/tasks": servedTaskPage{Items: []servedTask{
			agentTask(taskResponse{
				ProjectID:     "0198aa00-0000-7000-8000-00000000c001",
				ID:            prerequisiteID,
				Key:           "design_frontend",
				MilestoneType: "product",
				Title:         "Design frontend",
				Description:   "Design the public website.",
				Status:        "done",
				Priority:      "high",
				Revision:      3,
			}, agentID),
			agentTask(taskResponse{
				ProjectID:     "0198aa00-0000-7000-8000-00000000c001",
				ID:            dependentID,
				Key:           "build_backend",
				MilestoneType: "product",
				Title:         "Build backend",
				Description:   "Build the contact form API.",
				Status:        "backlog",
				Priority:      "high",
				Revision:      5,
			}, agentID),
		}},
		"/companies/" + companyID + "/tasks/" + prerequisiteID + "/dependencies": map[string]any{
			"blockedBy": []any{},
			"blocks": []any{map[string]any{
				"task":      map[string]any{"id": dependentID, "key": "build_backend"},
				"createdAt": "2026-08-26T08:00:00Z",
				"revision":  7,
			}},
		},
		"/companies/" + companyID + "/tasks/" + dependentID + "/dependencies": map[string]any{
			"blockedBy": []any{map[string]any{
				"task":      map[string]any{"id": prerequisiteID, "key": "design_frontend"},
				"createdAt": "2026-08-26T08:00:00Z",
				"revision":  7,
			}},
			"blocks": []any{},
		},
	}}
	desired := Document{Projects: []Project{{Key: "work", Title: "Work"}},
		FormatVersion: FormatVersion, Company: "acme", Tasks: []Task{
			{ProjectKey: "work",
				Key:           "design_frontend",
				AgentID:       agentID,
				MilestoneType: "product", Title: "Design frontend",
				Description: "Design the public website.",
				Status:      "done",
				Priority:    "high",
				DependsOn:   []TaskDependency{},
			},
			{ProjectKey: "work",
				Key:           "build_backend",
				AgentID:       agentID,
				MilestoneType: "product", Title: "Build backend",
				Description: "Build the contact form API.",
				Status:      "backlog",
				Priority:    "high",
				DependsOn:   []TaskDependency{{TaskKey: "design_frontend"}},
			},
		},
	}

	artifact, err := Plan(context.Background(), api, desired, time.Now())
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	// Restating the current plan, the done Task and its Agent included,
	// changes nothing.
	if len(artifact.Changes) != 0 {
		t.Fatalf("Plan() changes = %+v, want none", artifact.Changes)
	}
	want := BaseDependency{
		DependentTaskID: dependentID, PrerequisiteTaskID: prerequisiteID, Revision: 7,
	}
	if len(artifact.Base.Tasks) != 2 || artifact.Base.Tasks[0].Revision != 3 ||
		artifact.Base.Tasks[1].Revision != 5 {
		t.Fatalf("Plan() Task base = %+v", artifact.Base.Tasks)
	}
	if len(artifact.Base.Dependencies) != 1 || artifact.Base.Dependencies[0] != want {
		t.Fatalf("Plan() dependency base = %+v, want [%+v]", artifact.Base.Dependencies, want)
	}
}

// The export carries each Task's executing Agent as agent_id: after ADR 0014
// the assignee is always a member and executedBy names the Agent. So a
// transaction that only creates one Task leaves every existing Task's Agent
// in the desired document the server applies. Lost there, the apply would
// take the Agent off each of them as a confirmed handoff.
func TestPlanActionsKeepsTheAgentOfEveryExistingTask(t *testing.T) {
	t.Parallel()
	companyID := uuid.Must(uuid.NewV7()).String()
	buildID := uuid.Must(uuid.NewV7()).String()
	brandID := uuid.Must(uuid.NewV7()).String()
	eliID := uuid.Must(uuid.NewV7()).String()
	lyraID := uuid.Must(uuid.NewV7()).String()
	api := fakeAPI{responses: map[string]any{
		"/companies": companyCollection{Items: []companyResponse{{ID: companyID, Slug: "acme"}}},
		"/companies/" + companyID + "/tasks": servedTaskPage{Items: []servedTask{
			agentTask(taskResponse{
				ProjectID:     "0198aa00-0000-7000-8000-00000000c001",
				ID:            buildID,
				Key:           "HB-1",
				MilestoneType: "product",
				Title:         "Build the store",
				Description:   "Build the storefront.",
				Status:        "in_progress",
				Priority:      "high",
				Revision:      4,
			}, eliID),
			agentTask(taskResponse{
				ProjectID:     "0198aa00-0000-7000-8000-00000000c001",
				ID:            brandID,
				Key:           "HB-2",
				MilestoneType: "brand",
				Title:         "Draw the brand",
				Description:   "Draw the brand.",
				Status:        "done",
				Priority:      "medium",
				Revision:      2,
			}, lyraID),
		}},
		"/companies/" + companyID + "/tasks/" + buildID + "/dependencies": dependencySetResponse{
			BlockedBy: []dependencyResponse{},
		},
		"/companies/" + companyID + "/tasks/" + brandID + "/dependencies": dependencySetResponse{
			BlockedBy: []dependencyResponse{},
		},
	}}
	transaction, _, err := ParseActions([]byte(`kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - id: create_storefront_plan
    operation: create
    entity: task
    key: storefront_plan
    reason: Lyra owns the product plan.
    values: {project_key: work, agent_id: ` + lyraID + `, milestone_type: product, title: Plan the storefront, description: "Plan it.", priority: high}
`))
	if err != nil {
		t.Fatal(err)
	}

	artifact, err := PlanActions(context.Background(), api, transaction, time.Now())
	if err != nil {
		t.Fatalf("PlanActions() error = %v", err)
	}
	if len(artifact.Changes) != 1 || artifact.Changes[0].Operation != "create" ||
		artifact.Changes[0].Key != "storefront_plan" {
		t.Fatalf("PlanActions() changes = %+v, want one create", artifact.Changes)
	}
	desired := flatten(artifact.Desired)
	for key, wantAgentID := range map[string]string{"HB-1": eliID, "HB-2": lyraID, "storefront_plan": lyraID} {
		if got := desired.Tasks[key].AgentID; got != wantAgentID {
			t.Fatalf("desired Task %s agent_id = %q, want %q", key, got, wantAgentID)
		}
	}
	if err := ValidateArtifact(artifact); err != nil {
		t.Fatalf("ValidateArtifact() error = %v", err)
	}
}

func TestDiffPlansProtectsTerminalEntities(t *testing.T) {
	t.Parallel()
	current := flatten(Document{Projects: []Project{{Key: "work", Title: "Work"}}, Tasks: []Task{{ProjectKey: "work",
		Key: "done", Title: "Done", Status: "done", Priority: "medium",
	}}})
	omitted := flatten(Document{Projects: []Project{{Key: "work", Title: "Work"}}, Tasks: []Task{}})
	changes, err := diffPlans(current, omitted)
	if err != nil || len(changes) != 0 {
		t.Fatalf("diffPlans(terminal omitted) = %+v, %v", changes, err)
	}
	modified := omitted
	modified.Tasks["done"] = Task{ProjectKey: "work", Key: "done", Title: "Changed", Status: "done", Priority: "medium"}
	if _, err := diffPlans(current, modified); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("diffPlans(terminal modified) error = %v", err)
	}
}

func TestDiffPlansAllowsOnlyDependencyRemovalFromFailedTask(t *testing.T) {
	t.Parallel()
	plans := func() (FlatPlan, FlatPlan) {
		currentTask := Task{ProjectKey: "work",
			Key: "failed", AgentID: planAgentID, MilestoneType: "product", Title: "Failed", Description: "Attempt the work.",
			Status: "failed", Priority: "medium",
			DependsOn: []TaskDependency{{TaskKey: "prerequisite"}},
		}
		desiredTask := currentTask
		desiredTask.DependsOn = []TaskDependency{}
		current := flatten(Document{Projects: []Project{{Key: "work", Title: "Work"}}, FormatVersion: FormatVersion, Company: "acme", Tasks: []Task{currentTask}})
		desired := flatten(Document{Projects: []Project{{Key: "work", Title: "Work"}}, FormatVersion: FormatVersion, Company: "acme", Tasks: []Task{desiredTask}})
		return current, desired
	}

	current, desired := plans()
	changes, err := diffPlans(current, desired)
	if err != nil {
		t.Fatalf("diffPlans() error = %v", err)
	}
	if len(changes) != 1 || changes[0].Operation != "update" || changes[0].Key != "failed" ||
		len(changes[0].Fields) != 1 || changes[0].Fields[0].Field != "depends_on" {
		t.Fatalf("diffPlans() changes = %+v, want one failed Task dependency update", changes)
	}

	tests := []struct {
		name   string
		mutate func(Task) Task
	}{
		{
			name: "dependency addition",
			mutate: func(task Task) Task {
				task.DependsOn = append(task.DependsOn, TaskDependency{TaskKey: "other"})
				return task
			},
		},
		{
			name: "AgentID change",
			mutate: func(task Task) Task {
				task.AgentID = "0198aa00-0000-7000-8000-00000000a003"
				return task
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			current, desired := plans()
			desired.Tasks["failed"] = test.mutate(desired.Tasks["failed"])
			if _, err := diffPlans(
				current,
				desired,
			); err == nil ||
				!strings.Contains(err.Error(), "terminal") {
				t.Fatalf("diffPlans() error = %v, want terminal immutability error", err)
			}
		})
	}
}

func TestBaseDependenciesAreDeterministicAndVersioned(t *testing.T) {
	t.Parallel()
	dependentA := uuid.Must(uuid.NewV7()).String()
	dependentB := uuid.Must(uuid.NewV7()).String()
	prerequisite := uuid.Must(uuid.NewV7()).String()
	base := baseDependencies(map[string][]dependencyResponse{
		dependentB: {{Task: dependencyTaskResponse{ID: prerequisite}, Revision: 4}},
		dependentA: {{Task: dependencyTaskResponse{ID: prerequisite}, Revision: 2}},
	})
	if len(base) != 2 || base[0].DependentTaskID > base[1].DependentTaskID ||
		base[0].Revision+base[1].Revision != 6 {
		t.Fatalf("baseDependencies() = %+v", base)
	}
	desired := Document{Projects: []Project{{Key: "work", Title: "Work"}}, FormatVersion: FormatVersion, Company: "acme", Tasks: []Task{}}
	artifact := Artifact{
		ArtifactVersion: 1, CompanyID: uuid.Must(uuid.NewV7()).String(), CompanySlug: "acme",
		CreatedAt: time.Now().UTC(), Desired: desired,
		Base: BaseSnapshot{Dependencies: base},
	}
	if err := ValidateArtifact(artifact); err != nil {
		t.Fatalf("ValidateArtifact() error = %v", err)
	}
	artifact.Base.Dependencies[0].Revision = 0
	if err := ValidateArtifact(artifact); err == nil || !strings.Contains(err.Error(), "dependency base") {
		t.Fatalf("ValidateArtifact(invalid dependency base) error = %v", err)
	}
}

func TestApplyUsesCanonicalCompanyEndpoint(t *testing.T) {
	t.Parallel()
	companyID := uuid.Must(uuid.NewV7()).String()
	desired := Document{Projects: []Project{{Key: "work", Title: "Work"}}, FormatVersion: FormatVersion, Company: "acme", Tasks: []Task{}}
	artifact := Artifact{
		ArtifactVersion: 1,
		CompanyID:       companyID,
		CompanySlug:     "acme",
		CreatedAt:       time.Now().UTC(),
		Desired:         desired,
	}
	api := &fakeApplyAPI{}
	result, err := Apply(context.Background(), api, artifact, "request-key")
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if api.path != "/companies/"+companyID+"/company-plan/apply" {
		t.Fatalf("Apply() path = %q", api.path)
	}
	if api.requestKey != "request-key" {
		t.Fatalf("Apply() request key = %q", api.requestKey)
	}
	if result.Approval.ID != "approval-id" || result.Approval.Status != "pending" {
		t.Fatalf("Apply() receipt = %+v", result)
	}
}

type fakeApplyAPI struct {
	path       string
	requestKey string
}

func (a *fakeApplyAPI) PostRunJSON(
	_ context.Context,
	path, requestKey string,
	_, destination any,
) error {
	a.path, a.requestKey = path, requestKey
	// The 202 body is the bare durable Approval; the client decodes the
	// fields it reads straight off the top level.
	receipt := destination.(*ApprovalSummary)
	*receipt = ApprovalSummary{ID: "approval-id", Status: "pending"}
	return nil
}
