package companyplan

import "testing"

func TestProjectActionsAndDependencyIsolation(t *testing.T) {
	raw := []byte(`kind: company_plan_transaction
format_version: 5
company: acme
actions:
  - id: website
    entity: project
    operation: create
    key: website
    values:
      title: Website
  - id: page
    entity: task
    operation: create
    key: page
    values:
      project_key: website
      title: Page
      description: Build page
      milestone_type: product
      priority: high
`)
	transaction, _, err := ParseActions(raw)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := ApplyActions(Document{FormatVersion: FormatVersion, Company: "acme", Projects: []Project{}, Tasks: []Task{}}, transaction)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateDocument(desired); err != nil {
		t.Fatal(err)
	}
	if len(desired.Projects) != 1 || desired.Tasks[0].ProjectKey != "website" {
		t.Fatalf("got %+v", desired)
	}
	desired.Projects = append(desired.Projects, Project{Key: "research", Title: "Research"})
	other := desired.Tasks[0]
	other.Key = "research"
	other.ProjectKey = "research"
	desired.Tasks = append(desired.Tasks, other)
	desired.Tasks[0].DependsOn = []TaskDependency{{TaskKey: "research"}}
	if _, err := validateDocument(desired); err == nil {
		t.Fatal("accepted cross-Project dependency")
	}
}
