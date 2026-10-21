package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/companyplan"
)

const testCompanyID = "019f8458-e642-73a3-8702-adbfa0beedb2"

type apiStub struct {
	responses  map[string]string
	postPath   string
	postKey    string
	postBody   any
	postStatus string
}

func (s *apiStub) FetchRunJSON(_ context.Context, path string, destination any) error {
	return json.Unmarshal([]byte(s.responses[path]), destination)
}

func (s *apiStub) PostRunJSON(
	_ context.Context,
	path, requestKey string,
	source, destination any,
) error {
	s.postPath, s.postKey, s.postBody = path, requestKey, source
	status := s.postStatus
	if status == "" {
		status = "pending"
	}
	encoded, _ := json.Marshal(map[string]any{"id": "approval-id", "status": status})
	return json.Unmarshal(encoded, destination)
}

func TestExportRequiresExactlyOneCompanyAndWritesYAML(t *testing.T) {
	t.Parallel()
	outputPath := filepath.Join(t.TempDir(), "company-plan.yaml")
	api := &apiStub{responses: emptyCompanyPlanResponses()}
	service := NewCompanyPlanService(api)

	result, err := service.Export(context.Background(), ExportInput{OutputPath: outputPath})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if result.OutputPath != outputPath || result.Tasks != 0 {
		t.Fatalf("Export() result = %+v", result)
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != "format_version: 5\ncompany: zenith-team-local\nprojects: []\ntasks: []\n" {
		t.Fatalf("exported YAML = %q", contents)
	}
	if _, err := service.Export(
		context.Background(),
		ExportInput{OutputPath: outputPath},
	); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second Export() error = %v", err)
	}
}

func TestExportPreservesTasksCreatedWithoutDescriptions(t *testing.T) {
	t.Parallel()
	responses := emptyCompanyPlanResponses()
	responses["/companies/"+testCompanyID+"/projects?include_archived=true"] = `{"items":[{"id":"project","key":"work","title":"Work","description":"","revision":1,"access":{"permissions":["project.read","project.tasks.write"]}}]}`
	// The Task as served since ADR 0014: a member assignee, and the Agent
	// that does the work in executedBy.
	responses["/companies/"+testCompanyID+"/tasks"] = `{"items":[{"projectId":"project","id":"task","key":"PAL-1","milestoneType":"product","title":"Draft landing page","description":"","status":"todo","priority":"medium","assignee":{"kind":"user","id":"0198aa00-0000-7000-8000-00000000aaaa"},"executedBy":{"kind":"agent","id":"0198aa00-0000-7000-8000-00000000a002"}}]}`
	responses["/companies/"+testCompanyID+"/tasks/task/dependencies"] = `{"blockedBy":[]}`
	service := NewCompanyPlanService(&apiStub{responses: responses})
	path := filepath.Join(t.TempDir(), "plan.yaml")
	result, err := service.Export(context.Background(), ExportInput{OutputPath: path})
	if err != nil || result.Tasks != 1 {
		t.Fatalf("export = %+v, %v", result, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `description: ""`) {
		t.Fatalf("export lost empty description: %s, %v", data, err)
	}
	if !strings.Contains(string(data), "agent_id: 0198aa00-0000-7000-8000-00000000a002") {
		t.Fatalf("export lost the executing Agent: %s", data)
	}
}

func TestExportRejectsMultipleCompanies(t *testing.T) {
	t.Parallel()
	responses := emptyCompanyPlanResponses()
	responses["/companies"] = `{"items":[{"id":"` + testCompanyID + `","slug":"one"},{"id":"019f8458-e642-73a3-8702-adbfa0beedb3","slug":"two"}]}`
	service := NewCompanyPlanService(&apiStub{responses: responses})

	_, err := service.Export(context.Background(), ExportInput{OutputPath: filepath.Join(t.TempDir(), "plan.yaml")})
	if err == nil || !strings.Contains(err.Error(), "multiple Companies") {
		t.Fatalf("Export() error = %v", err)
	}
}

func TestListAgentsReturnsActiveAgentsRootFirst(t *testing.T) {
	t.Parallel()
	service := NewCompanyPlanService(&apiStub{responses: map[string]string{
		"/companies": `{"items":[{"id":"` + testCompanyID + `","slug":"zenith-team-local"}]}`,
		"/companies/" + testCompanyID + "/agents": `{"items":[
			{"id":"0198aa00-0000-7000-8000-00000000a002","name":"Eli","title":"Engineering Agent","instructions":"Ship the product.","reportsTo":{"id":"0198aa00-0000-7000-8000-00000000a001","name":"Andrew"},"runtimeAvailable":true,"archivedAt":null},
			{"id":"0198aa00-0000-7000-8000-00000000a003","name":"Lily","title":"Finance Agent","instructions":"","reportsTo":{"id":"0198aa00-0000-7000-8000-00000000a001","name":"Andrew"},"runtimeAvailable":true,"archivedAt":"2026-09-01T00:00:00Z"},
			{"id":"0198aa00-0000-7000-8000-00000000a001","name":"Andrew","title":"CEO","instructions":"Run the company.","reportsTo":null,"runtimeAvailable":false,"archivedAt":null}
		]}`,
	}})

	result, err := service.ListAgents(context.Background(), ListAgentsInput{})
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if result.CompanyID != testCompanyID || result.CompanySlug != "zenith-team-local" ||
		result.TotalCount != 2 || len(result.Agents) != 2 ||
		result.Agents[0].AgentID != "0198aa00-0000-7000-8000-00000000a001" || !result.Agents[0].IsRoot ||
		result.Agents[0].RuntimeAvailable ||
		result.Agents[1].Name != "Eli" || result.Agents[1].IsRoot ||
		result.Agents[1].Instructions != "Ship the product." || !result.Agents[1].RuntimeAvailable {
		t.Fatalf("ListAgents() result = %+v", result)
	}
}

func TestPreviewRejectsNonCanonicalActionsFilename(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	actionsPath := filepath.Join(directory, "alternate-actions.yaml")
	if err := os.WriteFile(
		actionsPath,
		[]byte("kind: company_plan_transaction\nformat_version: 5\ncompany: zenith-team-local\nactions: []\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	service := NewCompanyPlanService(&apiStub{responses: emptyCompanyPlanResponses()})
	_, err := service.Preview(context.Background(), PreviewInput{ActionsPath: actionsPath})
	if err == nil || !strings.Contains(err.Error(), "company-plan-actions.yaml") {
		t.Fatalf("Preview(non-canonical path) error = %v", err)
	}
}

func TestPreviewRequiresDraftInsideConfiguredWorkspace(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	outsideRoot := t.TempDir()
	outsidePath := filepath.Join(outsideRoot, companyPlanActionsFilename)
	contents := []byte(
		"kind: company_plan_transaction\nformat_version: 5\ncompany: zenith-team-local\nactions: []\n",
	)
	if err := os.WriteFile(outsidePath, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	service, err := NewCompanyPlanServiceForWorkspace(
		&apiStub{responses: emptyCompanyPlanResponses()},
		workspaceRoot,
	)
	if err != nil {
		t.Fatalf("NewCompanyPlanServiceForWorkspace() error = %v", err)
	}
	if _, err := service.Preview(
		context.Background(),
		PreviewInput{ActionsPath: outsidePath},
	); err == nil || !strings.Contains(err.Error(), workspaceRoot) {
		t.Fatalf("Preview(outside workspace) error = %v", err)
	}

	canonicalPath := filepath.Join(workspaceRoot, companyPlanActionsFilename)
	if err := os.WriteFile(canonicalPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Preview(
		context.Background(),
		PreviewInput{ActionsPath: canonicalPath},
	); err != nil {
		t.Fatalf("Preview(canonical workspace draft) error = %v", err)
	}
}

// With a workspace root the export lives beside the actions draft, never
// wherever the model points it: the Run environment owns both files and the
// repository the Agent works in must never read dirty because of them.
func TestExportWritesOnlyTheWorkspaceExportWhenRooted(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	service, err := NewCompanyPlanServiceForWorkspace(
		&apiStub{responses: emptyCompanyPlanResponses()},
		workspaceRoot,
	)
	if err != nil {
		t.Fatalf("NewCompanyPlanServiceForWorkspace() error = %v", err)
	}
	canonicalPath := filepath.Join(service.workspaceRoot, companyPlanExportFilename)
	for _, outside := range []string{
		filepath.Join(t.TempDir(), companyPlanExportFilename),
		filepath.Join(service.workspaceRoot, "plan.yaml"),
		filepath.Join(service.workspaceRoot, "nested", companyPlanExportFilename),
	} {
		if _, err := service.Export(
			context.Background(),
			ExportInput{OutputPath: outside},
		); err == nil || !strings.Contains(err.Error(), canonicalPath) {
			t.Fatalf("Export(%q) error = %v, want a refusal naming %q", outside, err, canonicalPath)
		}
	}
	result, err := service.Export(context.Background(), ExportInput{})
	if err != nil {
		t.Fatalf("Export(no path) error = %v", err)
	}
	if result.OutputPath != canonicalPath {
		t.Fatalf("Export(no path) wrote %q, want %q", result.OutputPath, canonicalPath)
	}
	if _, err := os.Stat(canonicalPath); err != nil {
		t.Fatalf("exported file: %v", err)
	}
	// Naming the canonical file, through the unresolved spelling the prompt
	// carries, is accepted as the same place.
	again, err := service.Export(context.Background(), ExportInput{
		OutputPath: filepath.Join(workspaceRoot, companyPlanExportFilename), Overwrite: true,
	})
	if err != nil || again.OutputPath != canonicalPath {
		t.Fatalf("Export(prompt spelling) = %+v, %v; want %q", again, err, canonicalPath)
	}
	unrooted := NewCompanyPlanService(&apiStub{responses: emptyCompanyPlanResponses()})
	if _, err := unrooted.Export(context.Background(), ExportInput{}); err == nil {
		t.Fatal("Export(no root, no path) accepted a missing path")
	}
}

func TestListAgentsRejectsMultipleCompanies(t *testing.T) {
	t.Parallel()
	service := NewCompanyPlanService(&apiStub{responses: map[string]string{
		"/companies": `{"items":[
			{"id":"` + testCompanyID + `","slug":"one"},
			{"id":"019f8458-e642-73a3-8702-adbfa0beedb3","slug":"two"}
		]}`,
	}})

	_, err := service.ListAgents(context.Background(), ListAgentsInput{})
	if err == nil || !strings.Contains(err.Error(), "multiple Companies") {
		t.Fatalf("ListAgents() error = %v", err)
	}
}

func TestPreviewAndApplyUseSameActionYAML(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	actionsPath := filepath.Join(directory, companyPlanActionsFilename)
	if err := os.WriteFile(
		actionsPath,
		[]byte("kind: company_plan_transaction\nformat_version: 5\ncompany: zenith-team-local\nactions: []\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	api := &apiStub{responses: emptyCompanyPlanResponses()}
	service := NewCompanyPlanService(api)
	previewed, err := service.Preview(context.Background(), PreviewInput{ActionsPath: actionsPath})
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if !previewed.Ready || previewed.TotalChanges != 0 || !strings.Contains(previewed.Preview, "No changes") {
		t.Fatalf("Preview() result = %+v", previewed)
	}
	result, err := service.Apply(context.Background(), ApplyInput{ActionsPath: actionsPath})
	if err != nil || result.ApprovalID != "approval-id" || result.ApprovalStatus != "pending" ||
		result.Instruction == "" || api.postKey == "" {
		t.Fatalf("Apply() = %+v, %v", result, err)
	}
	if _, err := service.Preview(
		context.Background(),
		PreviewInput{ActionsPath: "relative.yaml"},
	); err == nil ||
		!strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative Preview() error = %v", err)
	}
}

func TestApplyInstructionReflectsApprovalStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{
			name:   "pending human review",
			status: "pending",
			want:   "The Company Plan approval is pending human review. End this run without retrying or applying the plan again.",
		},
		{
			name:   "automatic approval completed",
			status: "approved",
			want:   "The Company Plan is approved, and the server has already handled the action. End this run without retrying or applying the plan again.",
		},
		{
			name:   "terminal approval did not apply",
			status: "rejected",
			want:   "The Company Plan approval is rejected. End this run without retrying or applying the plan again.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actionsPath := filepath.Join(t.TempDir(), companyPlanActionsFilename)
			if err := os.WriteFile(
				actionsPath,
				[]byte(
					"kind: company_plan_transaction\nformat_version: 5\ncompany: zenith-team-local\nactions: []\n",
				),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			api := &apiStub{responses: emptyCompanyPlanResponses(), postStatus: test.status}
			service := NewCompanyPlanService(api)
			if _, err := service.Preview(
				context.Background(),
				PreviewInput{ActionsPath: actionsPath},
			); err != nil {
				t.Fatalf("Preview() error = %v", err)
			}
			got, err := service.Apply(context.Background(), ApplyInput{ActionsPath: actionsPath})
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if got.Instruction != test.want {
				t.Fatalf("Apply() instruction = %q, want %q", got.Instruction, test.want)
			}
		})
	}
}

func TestPresentChangesExposesCancelInsteadOfStatusUpdate(t *testing.T) {
	t.Parallel()
	transaction, _, err := companyplan.ParseActions([]byte(`kind: company_plan_transaction
format_version: 5
company: zenith-team-local
actions:
  - {id: cancel_task, operation: cancel, entity: task, key: task}
`))
	if err != nil {
		t.Fatal(err)
	}
	changes := presentChanges(
		[]companyplan.Change{
			{
				EntityType: "task",
				Operation:  "update",
				Key:        "task",
				Fields: []companyplan.FieldChange{
					{Field: "status", Before: "todo", After: "cancelled"},
				},
			},
		},
		transaction,
	)
	if len(changes) != 1 || changes[0].Operation != "cancel" ||
		!strings.Contains(formatPreview(changes), "1 cancel") {
		t.Fatalf("presented changes = %+v", changes)
	}
}

func TestApplyRequiresUnchangedPreview(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	actionsPath := filepath.Join(directory, companyPlanActionsFilename)
	if err := os.WriteFile(
		actionsPath,
		[]byte("kind: company_plan_transaction\nformat_version: 5\ncompany: zenith-team-local\nactions: []\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	api := &apiStub{responses: emptyCompanyPlanResponses()}
	service := NewCompanyPlanService(api)
	if _, err := service.Apply(
		context.Background(),
		ApplyInput{ActionsPath: actionsPath},
	); err == nil ||
		!strings.Contains(err.Error(), "preview_required") {
		t.Fatalf("Apply(unpreviewed) error = %v", err)
	}
	if _, err := service.Preview(context.Background(), PreviewInput{ActionsPath: actionsPath}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		actionsPath,
		[]byte(
			"kind: company_plan_transaction\nformat_version: 5\ncompany: zenith-team-local\ndescription: changed\nactions: []\n",
		),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(
		context.Background(),
		ApplyInput{ActionsPath: actionsPath},
	); err == nil ||
		!strings.Contains(err.Error(), "preview_required") {
		t.Fatalf("Apply(changed) error = %v", err)
	}
}

func TestNewPreviewSupersedesOlderPreviewOnSamePath(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	actionsPath := filepath.Join(directory, companyPlanActionsFilename)
	first := []byte(
		"kind: company_plan_transaction\nformat_version: 5\ncompany: zenith-team-local\ndescription: first\nactions: []\n",
	)
	second := []byte(
		"kind: company_plan_transaction\nformat_version: 5\ncompany: zenith-team-local\ndescription: second\nactions: []\n",
	)
	if err := os.WriteFile(actionsPath, first, 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewCompanyPlanService(&apiStub{responses: emptyCompanyPlanResponses()})
	if _, err := service.Preview(context.Background(), PreviewInput{ActionsPath: actionsPath}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actionsPath, second, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Preview(context.Background(), PreviewInput{ActionsPath: actionsPath}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actionsPath, first, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(
		context.Background(),
		ApplyInput{ActionsPath: actionsPath},
	); err == nil ||
		!strings.Contains(err.Error(), "preview_required") {
		t.Fatalf("Apply(superseded preview) error = %v", err)
	}
}

func emptyCompanyPlanResponses() map[string]string {
	return map[string]string{
		"/companies": `{"items":[{"id":"` + testCompanyID + `","slug":"zenith-team-local"}]}`,
		"/companies/" + testCompanyID + "/projects?include_archived=true": `{"items":[]}`,
		"/companies/" + testCompanyID + "/tasks":                          `{"items":[]}`,
	}
}
