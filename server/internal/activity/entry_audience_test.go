package activity

import (
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// idRows is a one-column result of ids for recordingDBTX.lists.
type idRows struct {
	ids  []uuid.UUID
	next int
}

func (r *idRows) Close()                                       {}
func (r *idRows) Err() error                                   { return nil }
func (r *idRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *idRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *idRows) Values() ([]any, error)                       { return nil, errors.New("not scripted") }
func (r *idRows) RawValues() [][]byte                          { return nil }
func (r *idRows) Conn() *pgx.Conn                              { return nil }
func (r *idRows) TypeMap() *pgtype.Map                         { return nil }

func (r *idRows) Next() bool {
	r.next++
	return r.next <= len(r.ids)
}

func (r *idRows) Scan(dest ...any) error {
	*dest[0].(*uuid.UUID) = r.ids[r.next-1]
	return nil
}

func sortedIDs(ids ...uuid.UUID) []uuid.UUID {
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return sorted
}

// entryAudience sends each activity.created frame to exactly the members
// whose feed shows the row (ListActivity, ADR 0017): every branch of the
// feed's visibility predicate, without PostgreSQL.
func TestEntryAudienceMatchesTheFeed(t *testing.T) {
	t.Parallel()
	companyID, projectID := uuid.New(), uuid.New()
	reader, other, manager, outsider := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	nobody := &auth.ProjectAudience{UserIDs: []uuid.UUID{}}
	project := uuid.NullUUID{UUID: projectID, Valid: true}
	addressed := func(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: true} }

	for _, test := range []struct {
		name       string
		entityType string
		project    uuid.NullUUID
		addressee  uuid.NullUUID
		visibility string // the Project's
		want       *auth.ProjectAudience
	}{
		{name: "Company-wide Task work with no Project reaches nobody", entityType: "task", want: nobody},
		{name: "Company-wide chat work with no Project reaches nobody", entityType: "message", want: nobody},
		{name: "an addressed row with no Project keeps its route", entityType: "task", addressee: addressed(reader)},
		{name: "a Company row that is not Project work keeps its route", entityType: "agent"},
		{
			name: "a project_grant row with no Project reaches the managers", entityType: "project_grant",
			want: &auth.ProjectAudience{UserIDs: []uuid.UUID{manager}},
		},
		{
			name:       "an addressed project_grant row with no Project reaches its addressee and the managers",
			entityType: "project_grant", addressee: addressed(outsider),
			want: &auth.ProjectAudience{UserIDs: sortedIDs(manager, outsider)},
		},
		{
			name: "a Company-wide row in a restricted Project reaches its readers", entityType: "task",
			project: project, visibility: "restricted",
			want: &auth.ProjectAudience{UserIDs: sortedIDs(reader, other)},
		},
		{
			name: "a row in a Company-visible Project reaches the Company", entityType: "task",
			project: project, visibility: "company", want: &auth.ProjectAudience{CompanyWide: true},
		},
		{
			name: "an addressed row reaches its addressee who reads the Project", entityType: "approval",
			project: project, addressee: addressed(reader), visibility: "restricted",
			want: &auth.ProjectAudience{UserIDs: []uuid.UUID{reader}},
		},
		{
			name:       "an addressed row reaches nobody when the addressee cannot read the Project",
			entityType: "approval", project: project, addressee: addressed(outsider), visibility: "restricted",
			want: nobody,
		},
		{
			name: "an addressed row in a Company-visible Project reaches its addressee", entityType: "question",
			project: project, addressee: addressed(outsider), visibility: "company",
			want: &auth.ProjectAudience{UserIDs: []uuid.UUID{outsider}},
		},
		{
			name: "a project_grant row reaches the readers and the managers", entityType: "project_grant",
			project: project, visibility: "restricted",
			want: &auth.ProjectAudience{UserIDs: sortedIDs(reader, other, manager)},
		},
		{
			name:       "an addressed project_grant row reaches the readers and the managers",
			entityType: "project_grant", project: project, addressee: addressed(outsider), visibility: "restricted",
			want: &auth.ProjectAudience{UserIDs: sortedIDs(reader, other, manager)},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &recordingDBTX{
				rows: map[string]func(dest ...any) error{
					"GetProjectAccessFacts": func(dest ...any) error {
						if test.visibility == "" {
							return pgx.ErrNoRows
						}
						*dest[0].(*uuid.UUID) = projectID
						*dest[1].(*string) = test.visibility
						*dest[2].(*int64) = 1
						return nil
					},
				},
				lists: map[string][]uuid.UUID{
					"ListProjectReaderIDs":              sortedIDs(reader, other),
					"ListActiveCompanyMemberIDsByRoles": {manager},
				},
			}
			got, err := entryAudience(context.Background(), db.New(database), db.ActivityEvent{
				CompanyID: companyID, EntityType: test.entityType,
				ProjectID: test.project, VisibilityUserID: test.addressee,
			})
			if err != nil {
				t.Fatalf("entryAudience() error = %v", err)
			}
			if (got == nil) != (test.want == nil) || got != nil &&
				(got.CompanyWide != test.want.CompanyWide || !slices.Equal(got.UserIDs, test.want.UserIDs)) {
				t.Fatalf("entryAudience() = %+v, want %+v", got, test.want)
			}
			for _, call := range database.calls {
				if call.name == "ListActiveCompanyMemberIDsByRoles" &&
					!hasArg(call.args, auth.RolesWith(auth.PermissionProjectsManage)) {
					t.Fatalf(
						"managers read with %v, want the roles holding projects.manage",
						call.args,
					)
				}
			}
		})
	}
}

// projectWorkEntity restates the entity types activity.sql hides when a
// Company-wide row names no Project; the two lists must stay the same, or a
// frame would reach members whose feed never shows the row.
func TestProjectWorkEntitiesMatchTheFeed(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../pkg/db/queries/activity.sql")
	if err != nil {
		t.Fatal(err)
	}
	lists := regexp.MustCompile(`entity_type NOT IN \(\s*('task'[^)]*)\)`).FindAllStringSubmatch(string(raw), -1)
	if len(lists) == 0 {
		t.Fatal("activity.sql names no Project-work entity list")
	}
	want := slices.Sorted(slices.Values(projectWorkEntities))
	for _, list := range lists {
		var entities []string
		for _, quoted := range strings.Split(list[1], ",") {
			entities = append(entities, strings.Trim(strings.TrimSpace(quoted), "'"))
		}
		slices.Sort(entities)
		if !slices.Equal(entities, want) {
			t.Errorf("activity.sql hides %v, projectWorkEntities names %v", entities, want)
		}
	}
}
