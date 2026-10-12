package company

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// The access report's rows arrive one per Project and person, in Project
// order; the fold opens a new entry at each Project boundary, keeps the
// reasons in the order the query sorted them, and always answers arrays.
func TestGroupProjectAccessReport(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	web, docs := uuid.New(), uuid.New()
	ann, bob, cat := uuid.New(), uuid.New(), uuid.New()
	row := func(project uuid.UUID, key string, user uuid.UUID, reasons ...string) generated.ListProjectAccessReportRow {
		return generated.ListProjectAccessReportRow{ProjectID: project, Key: key, Title: key + " title", UserID: user, Reasons: reasons}
	}
	person := func(user uuid.UUID, reasons ...ProjectAccessReportReason) ProjectAccessReportPerson {
		return ProjectAccessReportPerson{UserID: user, Reasons: reasons}
	}
	for name, test := range map[string]struct {
		rows []generated.ListProjectAccessReportRow
		want []ProjectAccessReportEntry
	}{
		"no rows": {nil, []ProjectAccessReportEntry{}},
		"one Project, two people": {
			[]generated.ListProjectAccessReportRow{
				row(web, "web", ann, "run_authorizer", "task_assignee"),
				row(web, "web", bob, "task_assignee"),
			},
			[]ProjectAccessReportEntry{{ProjectID: web, Key: "web", Title: "web title", People: []ProjectAccessReportPerson{
				person(ann, ProjectAccessReportReasonRunAuthorizer, ProjectAccessReportReasonTaskAssignee),
				person(bob, ProjectAccessReportReasonTaskAssignee),
			}}},
		},
		"two Projects": {
			[]generated.ListProjectAccessReportRow{
				row(docs, "docs", cat, "run_authorizer"),
				row(web, "web", ann, "task_assignee"),
				row(web, "web", cat, "run_authorizer"),
			},
			[]ProjectAccessReportEntry{
				{ProjectID: docs, Key: "docs", Title: "docs title", People: []ProjectAccessReportPerson{
					person(cat, ProjectAccessReportReasonRunAuthorizer),
				}},
				{ProjectID: web, Key: "web", Title: "web title", People: []ProjectAccessReportPerson{
					person(ann, ProjectAccessReportReasonTaskAssignee),
					person(cat, ProjectAccessReportReasonRunAuthorizer),
				}},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := groupProjectAccessReport(since, test.rows)
			if !got.Since.Equal(since) || got.Items == nil {
				t.Fatalf("report = %+v", got)
			}
			if !slices.EqualFunc(got.Items, test.want, func(a, b ProjectAccessReportEntry) bool {
				return a.ProjectID == b.ProjectID && a.Key == b.Key && a.Title == b.Title && a.People != nil &&
					slices.EqualFunc(a.People, b.People, func(x, y ProjectAccessReportPerson) bool {
						return x.UserID == y.UserID && slices.Equal(x.Reasons, y.Reasons)
					})
			}) {
				t.Errorf("items = %+v, want %+v", got.Items, test.want)
			}
		})
	}

	raw, err := json.Marshal(groupProjectAccessReport(since, nil))
	if err != nil || string(raw) != `{"since":"2026-09-01T00:00:00Z","items":[]}` {
		t.Errorf("empty report JSON = %s (%v)", raw, err)
	}
}

// The reason vocabulary lives in three places: the Go constants, the
// projects.yaml enum and the literals ListProjectAccessReport writes. A
// rename in one of them must fail here, not drift.
func TestProjectAccessReportReasonsMatchContractAndQuery(t *testing.T) {
	t.Parallel()
	reasons := []string{
		string(ProjectAccessReportReasonRunAuthorizer),
		string(ProjectAccessReportReasonTaskAssignee),
	}

	raw, err := os.ReadFile("../../../docs/api/spec/projects.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Schemas map[string]struct {
			Enum []string `yaml:"enum"`
		} `yaml:"schemas"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if got := document.Schemas["ProjectAccessReportReason"].Enum; !slices.Equal(got, reasons) {
		t.Errorf("projects.yaml ProjectAccessReportReason = %v, want %v", got, reasons)
	}

	sql, err := os.ReadFile("../../pkg/db/queries/project_access.sql")
	if err != nil {
		t.Fatal(err)
	}
	query := regexp.MustCompile(`(?s)-- name: ListProjectAccessReport :many\n(.*?)(?:\n-- name: |\z)`).FindSubmatch(sql)
	if query == nil {
		t.Fatal("ListProjectAccessReport not found in project_access.sql")
	}
	var literals []string
	for _, match := range regexp.MustCompile(`'([a-z_]+)'::text AS reason`).FindAllSubmatch(query[1], -1) {
		literals = append(literals, string(match[1]))
	}
	slices.Sort(literals)
	if !slices.Equal(literals, reasons) {
		t.Errorf("ListProjectAccessReport reason literals = %v, want %v", literals, reasons)
	}
}
