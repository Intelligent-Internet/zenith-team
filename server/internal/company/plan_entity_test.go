package company

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"

	taskdomain "github.com/Intelligent-Internet/zenith-team/server/internal/task"
)

func TestCompanyPlanArtifactAcceptsTasksWithoutGoals(t *testing.T) {
	companyID, agentID := uuid.New(), uuid.New()
	var desired PlanDocument
	if err := json.Unmarshal(
		[]byte(
			`{"formatVersion":5,"company":"acme","projects":[{"key":"work","title":"Work","description":"","archived":false}],"tasks":[{"projectKey":"work","key":"ship","agentId":"`+agentID.String()+`","milestoneType":"product","title":"Ship","description":"","status":"todo","priority":"high","dependsOn":[]}]}`,
		),
		&desired,
	); err != nil {
		t.Fatal(err)
	}
	artifact := PlanArtifact{
		ArtifactVersion: 1, CompanyID: companyID.String(), CompanySlug: "acme",
		CreatedAt: time.Now().UTC(), Desired: desired,
	}
	if err := validateCompanyPlanArtifact(artifact, companyID); err != nil {
		t.Fatalf("task-only Company Plan rejected: %v", err)
	}
}

func TestValidCompanyPlanDependencyGraph(t *testing.T) {
	t.Parallel()
	document := PlanDocument{
		Projects: []PlanProject{{Key: "work", Title: "Work"}},
		Tasks: []PlanTask{
			{ProjectKey: "work", Key: "design", DependsOn: []PlanTaskDependency{}},
			{ProjectKey: "work", Key: "build", DependsOn: []PlanTaskDependency{{TaskKey: "design"}}},
		},
	}
	if !validCompanyPlanDependencyGraph(document) {
		t.Fatal("validCompanyPlanDependencyGraph(valid) = false")
	}
	tests := []struct {
		name       string
		dependency PlanTaskDependency
	}{
		{name: "missing", dependency: PlanTaskDependency{TaskKey: "missing"}},
		{name: "self", dependency: PlanTaskDependency{TaskKey: "build"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := document
			invalid.Tasks = append([]PlanTask(nil), document.Tasks...)
			invalid.Tasks[1].DependsOn = []PlanTaskDependency{test.dependency}
			if validCompanyPlanDependencyGraph(invalid) {
				t.Fatal("validCompanyPlanDependencyGraph(invalid) = true")
			}
		})
	}
	cycle := document
	cycle.Tasks = append([]PlanTask(nil), document.Tasks...)
	cycle.Tasks[0].DependsOn = []PlanTaskDependency{{TaskKey: "build"}}
	if validCompanyPlanDependencyGraph(cycle) {
		t.Fatal("validCompanyPlanDependencyGraph(cycle) = true")
	}
}

func TestCompanyPlanTaskNamesAnAgentForAgentWorkOnly(t *testing.T) {
	t.Parallel()

	document := PlanDocument{
		Projects:      []PlanProject{{Key: "work", Title: "Work"}},
		FormatVersion: planFormatVersion,
		Company:       "acme",
		Tasks: []PlanTask{
			{
				ProjectKey: "work",
				Key:        "ship",
				Title:      "Ship",
				Status:     "todo",
				Priority:   "high",
				AgentID: uuid.New().
					String(),
				MilestoneType: taskdomain.MilestoneTypeProduct,
				DependsOn:     []PlanTaskDependency{},
			},
		},
	}
	if !validCompanyPlanDependencyGraph(document) {
		t.Fatal("valid Company Plan task dependency graph = false")
	}
	if !validCompanyPlanTask(document.Tasks[0]) {
		t.Fatal("validCompanyPlanTask(agent work naming an Agent) = false")
	}
	document.Tasks[0].AgentID = ""
	if !validCompanyPlanTask(document.Tasks[0]) {
		t.Fatal("validCompanyPlanTask(founder-assigned work without an Agent) = false")
	}
	document.Tasks[0].AgentID = "not-an-agent-id"
	if validCompanyPlanTask(document.Tasks[0]) {
		t.Fatal("validCompanyPlanTask(malformed agentId) = true")
	}
}

func TestCompanyPlanPreviewRetainsOmittedTerminalDependencies(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                string
		dependentStatus     string
		prerequisiteStatus  string
		includeDependent    bool
		includePrerequisite bool
		want                []PlanChange
	}{
		{name: "both terminal endpoints omitted", dependentStatus: "done", prerequisiteStatus: "done", want: []PlanChange{}},
		{name: "terminal dependent omitted", dependentStatus: "failed", prerequisiteStatus: "todo", includePrerequisite: true, want: []PlanChange{}},
		{name: "terminal prerequisite omitted", dependentStatus: "todo", prerequisiteStatus: "cancelled", includeDependent: true, want: []PlanChange{}},
		{name: "explicit endpoint removal", dependentStatus: "failed", prerequisiteStatus: "done", includeDependent: true, includePrerequisite: true, want: []PlanChange{{EntityType: "dependency", Operation: "delete", Key: "dependent -> prerequisite"}}},
		{name: "omitted active endpoint deleted", dependentStatus: "done", prerequisiteStatus: "todo", want: []PlanChange{{EntityType: "dependency", Operation: "delete", Key: "dependent -> prerequisite"}, {EntityType: "task", Operation: "delete", Key: "prerequisite"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dependentID, prerequisiteID, agentID := uuid.New(), uuid.New(), uuid.New()
			executedBy := uuid.NullUUID{UUID: agentID, Valid: true}
			tasks := map[string]persistedTask{
				"dependent": {
					ID:                dependentID,
					Key:               "dependent",
					Status:            test.dependentStatus,
					ExecutedByAgentID: executedBy,
				},
				"prerequisite": {
					ID:                prerequisiteID,
					Key:               "prerequisite",
					Status:            test.prerequisiteStatus,
					ExecutedByAgentID: executedBy,
				},
			}
			document := PlanDocument{
				Projects:      []PlanProject{{Key: "work", Title: "Work"}},
				FormatVersion: planFormatVersion,
				Company:       "acme",
				Tasks:         []PlanTask{},
			}
			if test.includeDependent {
				document.Tasks = append(
					document.Tasks,
					PlanTask{
						ProjectKey: "work",
						Key:        "dependent",
						Status:     test.dependentStatus,
						AgentID:    agentID.String(),
						DependsOn:  []PlanTaskDependency{},
					},
				)
			}
			if test.includePrerequisite {
				document.Tasks = append(
					document.Tasks,
					PlanTask{
						ProjectKey: "work",
						Key:        "prerequisite",
						Status:     test.prerequisiteStatus,
						AgentID:    agentID.String(),
						DependsOn:  []PlanTaskDependency{},
					},
				)
			}
			dependencies := map[string]persistedDependency{
				dependencyIdentity(dependentID, prerequisiteID): {
					DependentTaskID:    dependentID,
					PrerequisiteTaskID: prerequisiteID,
					Version:            1,
				},
			}
			if got := computeCompanyPlanChanges(
				document,
				tasks,
				dependencies,
			); !reflect.DeepEqual(
				got,
				test.want,
			) {
				t.Fatalf("computeCompanyPlanChanges() = %+v, want %+v", got, test.want)
			}
			changed := changedPlanDependencyTaskKeys(tasks, desiredTasksByKey(document), dependencies)
			if got := changed["dependent"]; got != (len(test.want) > 0) {
				t.Fatalf("dependency mutation fence = %v, want %v", got, len(test.want) > 0)
			}
		})
	}
}

func TestExecuteCompanyPlanApplySkipsFrozenV2Approval(t *testing.T) {
	t.Parallel()
	service := &DefaultPlanService{}
	var pending []events.Event
	result, err := service.ExecuteCompanyPlanApply(
		context.Background(),
		nil,
		&pending,
		approval.CompanyPlanActor{Principal: auth.UserPrincipal{}},
		json.RawMessage(
			`{"artifactVersion":1,"desired":{"formatVersion":2,"company":"acme","goals":[{"key":"launch","tasks":[{"key":"ship"}]}]}}`,
		),
	)
	if err != nil || result.SkippedReason == nil || !strings.Contains(*result.SkippedReason, "format changed") {
		t.Fatalf("ExecuteCompanyPlanApply() = %+v, %v, want skipped format", result, err)
	}
	if len(result.Result) != 0 || len(pending) != 0 {
		t.Fatalf("skipped approval produced execution result or events: %+v, %+v", result, pending)
	}
}

// The wire contract marks conversationId optional on a base entity and a
// Task's Conversation never changes, so it must not decide staleness: only
// id, key and revision do.
func TestBaseMatchesIgnoresConversationID(t *testing.T) {
	t.Parallel()
	taskID, conversationID := uuid.New(), uuid.New()
	persisted := []PlanBaseEntity{
		{ID: taskID.String(), Key: "ship", Revision: 3, ConversationID: conversationID.String()},
	}
	tests := map[string]struct {
		submitted []PlanBaseEntity
		want      bool
	}{
		"without conversationId": {
			submitted: []PlanBaseEntity{{ID: taskID.String(), Key: "ship", Revision: 3}},
			want:      true,
		},
		"with conversationId": {
			submitted: persisted,
			want:      true,
		},
		"stale revision": {
			submitted: []PlanBaseEntity{
				{
					ID:             taskID.String(),
					Key:            "ship",
					Revision:       2,
					ConversationID: conversationID.String(),
				},
			},
			want: false,
		},
		"missing task": {
			submitted: []PlanBaseEntity{},
			want:      false,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := baseMatches(test.submitted, persisted); got != test.want {
				t.Fatalf("baseMatches() = %v, want %v", got, test.want)
			}
		})
	}
}
