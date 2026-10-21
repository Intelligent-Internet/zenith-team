package companyplan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/apiclient"
	"github.com/Intelligent-Internet/zenith-team/server/internal/runchannel"
)

// exportAgentID is the Agent that does the fixture Tasks' work, and
// exportMemberID the member they are assigned to (ADR 0014).
const (
	exportAgentID  = "0198aa00-0000-7000-8000-00000000a002"
	exportMemberID = "0198aa00-0000-7000-8000-00000000aaaa"
)

// servedTask is a Task as GET /companies/{id}/tasks serves it (ADR 0014):
// the assignee is always a member, and executedBy names the Agent that does
// the work, or the assignee again when they do it themself.
type servedTask struct {
	taskResponse
	Assignee actorRef `json:"assignee"`
}

type servedTaskPage struct {
	Items []servedTask `json:"items"`
}

// agentTask serves a Task assigned to exportMemberID that agentID does.
func agentTask(task taskResponse, agentID string) servedTask {
	task.ExecutedBy = actorRef{Kind: "agent", ID: agentID}
	return servedTask{taskResponse: task, Assignee: actorRef{Kind: "user", ID: exportMemberID}}
}

func TestExportBuildsTaskOnlyStableYAML(t *testing.T) {
	t.Parallel()
	api := fakeAPI{responses: map[string]any{
		"/companies": companyCollection{Items: []companyResponse{{ID: "company-id", Slug: "zenith-team"}}},
		"/companies/company-id/tasks": servedTaskPage{Items: []servedTask{
			agentTask(taskResponse{
				ProjectID:     "0198aa00-0000-7000-8000-00000000c001",
				ID:            "task-prerequisite-id",
				Key:           "design_api",
				MilestoneType: "product",
				Title:         "Design API",
				Description:   "Design the API.",
				Status:        "done",
				Priority:      "high",
			}, exportAgentID),
			agentTask(taskResponse{
				ProjectID:     "0198aa00-0000-7000-8000-00000000c001",
				ID:            "task-dependent-id",
				Key:           "implement_api",
				MilestoneType: "product",
				Title:         "Implement API",
				Description:   "Add endpoints.",
				Status:        "todo",
				Priority:      "high",
			}, exportAgentID),
		}},
		"/companies/company-id/tasks/task-prerequisite-id/dependencies": dependencySetResponse{
			BlockedBy: []dependencyResponse{},
		},
		"/companies/company-id/tasks/task-dependent-id/dependencies": map[string]any{
			"blockedBy": []any{map[string]any{
				"task": map[string]any{
					"id":       "task-prerequisite-id",
					"key":      "design_api",
					"title":    "Design API",
					"status":   "done",
					"revision": 3,
				},
				"createdAt": "2026-08-26T08:00:00Z",
				"revision":  4,
			}},
			"blocks": []any{},
		},
	}}

	result, err := Export(context.Background(), api, "zenith-team")
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	want := `format_version: 5
company: zenith-team
projects:
  - key: work
    title: Work
    description: ""
    archived: false
tasks:
  - key: design_api
    project_key: work
    agent_id: 0198aa00-0000-7000-8000-00000000a002
    milestone_type: product
    title: Design API
    description: Design the API.
    status: done
    priority: high
    depends_on: []
  - key: implement_api
    project_key: work
    agent_id: 0198aa00-0000-7000-8000-00000000a002
    milestone_type: product
    title: Implement API
    description: Add endpoints.
    status: todo
    priority: high
    depends_on:
      - task_key: design_api
`
	if string(result) != want {
		t.Fatalf("Export() YAML mismatch\n--- got ---\n%s--- want ---\n%s", result, want)
	}
}

func TestExportReadsCompleteTaskCollection(t *testing.T) {
	t.Parallel()
	api := fakeAPI{responses: map[string]any{
		"/companies": companyCollection{
			Items: []companyResponse{{ID: "company-id", Slug: "zenith-team"}},
		},
		"/companies/company-id/tasks": servedTaskPage{
			Items: []servedTask{
				agentTask(taskResponse{
					ProjectID:     "0198aa00-0000-7000-8000-00000000c001",
					ID:            "task-2-id",
					Key:           "task_2",
					MilestoneType: "product",
					Title:         "Task 2",
					Description:   "Do task 2.",
					Status:        "todo",
					Priority:      "medium",
				}, exportAgentID),
				agentTask(taskResponse{
					ProjectID:     "0198aa00-0000-7000-8000-00000000c001",
					ID:            "task-1-id",
					Key:           "task_1",
					MilestoneType: "product",
					Title:         "Task 1",
					Description:   "Do task 1.",
					Status:        "done",
					Priority:      "low",
				}, exportAgentID),
			},
		},
		"/companies/company-id/tasks/task-1-id/dependencies": dependencySetResponse{
			BlockedBy: []dependencyResponse{},
		},
		"/companies/company-id/tasks/task-2-id/dependencies": dependencySetResponse{
			BlockedBy: []dependencyResponse{},
		},
	}}

	result, err := Export(context.Background(), api, "zenith-team")
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if !strings.Contains(string(result), "key: task_2") || !strings.Contains(string(result), "key: task_1") {
		t.Fatalf("Export() did not include every Task:\n%s", result)
	}
}

// A Task its assignee does themself names no Agent: executedBy is that
// member again, so its agent_id is empty.
func TestExportLeavesATaskItsAssigneeDoesWithoutAnAgent(t *testing.T) {
	t.Parallel()
	member := actorRef{Kind: "user", ID: exportMemberID}
	api := fakeAPI{responses: map[string]any{
		"/companies": companyCollection{
			Items: []companyResponse{{ID: "company-id", Slug: "zenith-team"}},
		},
		"/companies/company-id/tasks": servedTaskPage{
			Items: []servedTask{{
				taskResponse: taskResponse{
					ProjectID:     "0198aa00-0000-7000-8000-00000000c001",
					ID:            "orphan-id",
					Key:           "orphan",
					MilestoneType: "product",
					Title:         "Orphan",
					Status:        "todo",
					Priority:      "medium",
					ExecutedBy:    member,
				},
				Assignee: member,
			}},
		},
		"/companies/company-id/tasks/orphan-id/dependencies": dependencySetResponse{
			BlockedBy: []dependencyResponse{},
		},
	}}

	result, err := Export(context.Background(), api, "zenith-team")
	if err != nil || !strings.Contains(string(result), `agent_id: ""`) {
		t.Fatalf("Export() result = %s, error = %v", result, err)
	}
}

func TestExportAcceptsEmptyTaskCollection(t *testing.T) {
	t.Parallel()
	api := fakeAPI{responses: map[string]any{
		"/companies": companyCollection{
			Items: []companyResponse{{ID: "company-id", Slug: "zenith-team"}},
		},
		"/companies/company-id/tasks": taskPage{Items: []taskResponse{}},
	}}
	result, err := Export(context.Background(), api, "zenith-team")
	if err != nil ||
		string(
			result,
		) != "format_version: 5\ncompany: zenith-team\nprojects:\n  - key: work\n    title: Work\n    description: \"\"\n    archived: false\ntasks: []\n" {
		t.Fatalf("Export() result = %s, error = %v", result, err)
	}
}

// A Project the reader may read but not write is exported read_only, and a
// plan that would change its Tasks is refused before it reaches the server,
// which would leave them as they are (ADR 0017). The Project the reader
// writes still plans as usual.
func TestExportMarksReadOnlyProjectsAndPlansLeaveThemAlone(t *testing.T) {
	t.Parallel()
	const sharedID = "0198aa00-0000-7000-8000-00000000c002"
	api := fakeAPI{responses: map[string]any{
		"/companies": companyCollection{Items: []companyResponse{{ID: "company-id", Slug: "zenith-team"}}},
		"/companies/company-id/projects?include_archived=true": map[string]any{"items": []any{
			map[string]any{
				"id": "0198aa00-0000-7000-8000-00000000c001", "key": "work", "title": "Work",
				"description": "", "revision": 1, "access": map[string]any{
					"permissions": []string{"project.read", "project.tasks.write"},
				},
			},
			map[string]any{
				"id": sharedID, "key": "shared", "title": "Shared", "description": "", "revision": 1,
				"access": map[string]any{"permissions": []string{"project.read"}},
			},
		}},
		"/companies/company-id/tasks": servedTaskPage{Items: []servedTask{
			agentTask(taskResponse{
				ProjectID:     sharedID,
				ID:            "task-shared-id",
				Key:           "shared_notes",
				MilestoneType: "product",
				Title:         "Shared notes",
				Description:   "",
				Status:        "todo",
				Priority:      "medium",
				Revision:      1,
			}, exportAgentID),
		}},
		"/companies/company-id/tasks/task-shared-id/dependencies": dependencySetResponse{
			BlockedBy: []dependencyResponse{},
		},
	}}
	exported, err := Export(context.Background(), api, "zenith-team")
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if !strings.Contains(
		string(exported),
		"  - key: shared\n    title: Shared\n    description: \"\"\n    archived: false\n    read_only: true\n",
	) ||
		strings.Contains(
			string(exported),
			"key: work\n    title: Work\n    description: \"\"\n    archived: false\n    read_only",
		) {
		t.Fatalf("Export() did not mark only the shared Project read-only:\n%s", exported)
	}

	transaction := func(action string) ActionDocument {
		var document ActionDocument
		if err := json.Unmarshal([]byte(action), &document); err != nil {
			t.Fatal(err)
		}
		return document
	}
	_, err = PlanActions(context.Background(), api, transaction(`{
		"kind": "company_plan_transaction", "formatVersion": 5, "company": "zenith-team",
		"actions": [{"id": "rename", "operation": "update", "entity": "task", "key": "shared_notes",
			"set": {"title": "Renamed notes"}}]}`), time.Now())
	if err == nil || !strings.Contains(err.Error(), `you may read Project "shared" but not change its Tasks`) {
		t.Fatalf("PlanActions(update in a read-only Project) error = %v", err)
	}
	artifact, err := PlanActions(context.Background(), api, transaction(`{
		"kind": "company_plan_transaction", "formatVersion": 5, "company": "zenith-team",
		"actions": [{"id": "add", "operation": "create", "entity": "task", "key": "work_item",
			"values": {"projectKey": "work", "title": "Work item", "description": "Do it.", "priority": "medium",
				"agentId": "`+exportAgentID+`", "milestoneType": "product", "dependsOn": []}}]}`), time.Now())
	if err != nil || len(artifact.Changes) != 1 || artifact.Changes[0].Key != "work_item" {
		t.Fatalf("PlanActions(create in the written Project) = %+v, %v", artifact.Changes, err)
	}
	// The base says which Project the plan was read without changing, so
	// the apply leaves it alone even if its applier may change it by then.
	readOnly := map[string]bool{}
	for _, entity := range artifact.Base.Projects {
		readOnly[entity.Key] = entity.ReadOnly
	}
	if !readOnly["shared"] || readOnly["work"] || len(readOnly) != 2 {
		t.Fatalf("artifact base Projects read-only = %v, want shared alone", readOnly)
	}
}

type fakeAPI struct {
	responses map[string]any
}

func (a fakeAPI) FetchRunJSON(_ context.Context, path string, destination any) error {
	_, served := a.responses[path]
	if strings.HasSuffix(path, "/projects?include_archived=true") && !served {
		return json.Unmarshal(
			[]byte(
				`{"items":[{"id":"0198aa00-0000-7000-8000-00000000c001","key":"work","title":"Work","description":"","revision":1,"archivedAt":null,"access":{"permissions":["project.read","project.tasks.write"]}}]}`,
			),
			destination,
		)
	}

	response, ok := a.responses[path]
	if !ok {
		return fmt.Errorf("unexpected request %s", path)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, destination)
}

// The export reads as the Run, over the daemon's Run channel: zenith-team-mcp
// sends the channel capability and never a bearer or the Run token, and the
// daemon adds the proof the server bounds the export by. The fake channel
// stands in for the daemon and answers each read the way the daemon relays
// the server's.
func TestExportReadsEveryPlanInputThroughTheRunChannel(t *testing.T) {
	t.Parallel()
	const capability = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	responses := map[string]any{
		"/companies": companyCollection{Items: []companyResponse{{ID: "company-id", Slug: "zenith-team"}}},
		"/companies/company-id/projects?include_archived=true": map[string]any{"items": []any{map[string]any{
			"id": "0198aa00-0000-7000-8000-00000000c001", "key": "work", "title": "Work", "description": "",
			"revision": 1,
			"access":   map[string]any{"permissions": []string{"project.read", "project.tasks.write"}},
		}}},
		"/companies/company-id/tasks": servedTaskPage{Items: []servedTask{agentTask(taskResponse{
			ProjectID: "0198aa00-0000-7000-8000-00000000c001", ID: "task-id", Key: "ship",
			MilestoneType: "launch", Title: "Ship", Status: "todo", Priority: "high",
		}, exportAgentID)}},
		"/companies/company-id/tasks/task-id/dependencies": dependencySetResponse{
			BlockedBy: []dependencyResponse{},
		},
	}
	var mu sync.Mutex
	var reads []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != runchannel.AuthorizationScheme+" "+capability {
			t.Errorf("Authorization = %q, want the Run capability and never a bearer", got)
		}
		if r.Header.Get("X-Zenith-Team-Run-Token") != "" {
			t.Error("zenith-team-mcp sent the Run token")
		}
		var request runchannel.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode channel request: %v", err)
		}
		mu.Lock()
		reads = append(reads, request.Method+" "+request.Path)
		mu.Unlock()
		body, ok := responses[request.Path]
		status := http.StatusOK
		if !ok {
			status, body = http.StatusNotFound, map[string]string{
				"code": "not_found", "detail": "Not found.",
			}
		}
		encoded, _ := json.Marshal(body)
		_ = json.NewEncoder(w).Encode(runchannel.Response{SchemaVersion: 1, Status: status, Body: encoded})
	}))
	t.Cleanup(server.Close)
	api := apiclient.NewRunChannel(runchannel.RunControlConfig{
		SchemaVersion: 1, Endpoint: server.URL, Capability: capability,
	})

	result, err := ExportOnlyCompany(t.Context(), api)
	if err != nil {
		t.Fatalf("ExportOnlyCompany() error = %v", err)
	}
	if !strings.Contains(string(result), "key: ship") {
		t.Fatalf("ExportOnlyCompany() = %s", result)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /companies",
		"GET /companies/company-id/projects?include_archived=true",
		"GET /companies/company-id/tasks",
		"GET /companies/company-id/tasks/task-id/dependencies",
	}
	if strings.Join(reads, "\n") != strings.Join(want, "\n") {
		t.Fatalf("channel reads = %v, want %v", reads, want)
	}
}
