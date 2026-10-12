package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// testProjectFrame is a payload the stamp can decide for: scoped says whether
// it may belong to a Project, projectID whether it already names one.
type testProjectFrame struct {
	scoped    bool
	projectID uuid.UUID
	ProjectFrame
}

func (f testProjectFrame) ProjectFrameScope() (uuid.UUID, bool) { return f.projectID, f.scoped }

func (f testProjectFrame) WithProjectAudience(audience *ProjectAudience) any {
	f.Audience = audience
	return f
}

// audienceDB answers the four statements a frame audience reads, keyed by the
// id each one looks up (its second argument), and counts every statement.
// A missing row is pgx.ErrNoRows; any other statement fails the test.
type audienceDB struct {
	t             *testing.T
	projects      map[uuid.UUID]string // visibility
	readers       map[uuid.UUID][]uuid.UUID
	conversations map[uuid.UUID]generated.GetConversationProjectIDRow
	tasks         map[uuid.UUID]uuid.UUID
	counts        map[string]int
}

func (d *audienceDB) name(sql string, args []any) (string, uuid.UUID) {
	name, _, _ := strings.Cut(strings.TrimPrefix(sql, "-- name: "), " ")
	if d.counts == nil {
		d.counts = map[string]int{}
	}
	d.counts[name]++
	id, _ := args[1].(uuid.UUID)
	return name, id
}

func (d *audienceDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	d.t.Fatal("a frame audience writes nothing")
	return pgconn.CommandTag{}, nil
}

func (d *audienceDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	name, id := d.name(sql, args)
	if name != "ListProjectReaderIDs" {
		d.t.Fatalf("unexpected list statement %s", name)
	}
	return &idRows{ids: d.readers[id]}, nil
}

func (d *audienceDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	name, id := d.name(sql, args)
	switch name {
	case "GetProjectAccessFacts":
		visibility, ok := d.projects[id]
		if !ok {
			return scannedRow{}
		}
		return scannedRow{values: []any{id, visibility, int64(1), "Payroll"}}
	case "GetConversationProjectID":
		row, ok := d.conversations[id]
		if !ok {
			return scannedRow{}
		}
		return scannedRow{values: []any{row.Kind, row.ProjectID}}
	case "GetTaskProjectID":
		project, ok := d.tasks[id]
		if !ok {
			return scannedRow{}
		}
		return scannedRow{values: []any{project}}
	}
	d.t.Fatalf("unexpected statement %s", name)
	return scannedRow{}
}

// scannedRow scans values into the destinations in order; no values is
// pgx.ErrNoRows.
type scannedRow struct{ values []any }

func (r scannedRow) Scan(dest ...any) error {
	if r.values == nil {
		return pgx.ErrNoRows
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan %d destinations from %d values", len(dest), len(r.values))
	}
	for index, value := range r.values {
		reflect.ValueOf(dest[index]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

// idRows is a one-column result of ids.
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

func (r *idRows) Next() bool {
	r.next++
	return r.next <= len(r.ids)
}

func (r *idRows) Scan(dest ...any) error {
	*dest[0].(*uuid.UUID) = r.ids[r.next-1]
	return nil
}

// Every branch of the stamp, without PostgreSQL: where the Project comes
// from, what a Company-visible, missing or legacy Project reaches, and which
// frames it leaves alone. The integration test runs the same rules against
// real grants.
func TestStampProjectAudiencesDecidesEachBranch(t *testing.T) {
	t.Parallel()
	companyID := uuid.New()
	restricted, open, gone := uuid.New(), uuid.New(), uuid.New()
	reader, other := uuid.New(), uuid.New()
	taskChat, legacyChat, directChat, unknownChat := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	task, deletedTask := uuid.New(), uuid.New()
	stamped := &ProjectAudience{UserIDs: []uuid.UUID{other}}
	readers := &ProjectAudience{UserIDs: []uuid.UUID{reader}}
	company := &ProjectAudience{CompanyWide: true}
	nobody := &ProjectAudience{UserIDs: []uuid.UUID{}}

	for _, test := range []struct {
		name           string
		conversationID uuid.UUID
		taskID         uuid.UUID
		payload        testProjectFrame
		want           *ProjectAudience
	}{
		{"payload names a restricted Project", uuid.Nil, uuid.Nil,
			testProjectFrame{scoped: true, projectID: restricted}, readers},
		{"payload names a Company-visible Project", uuid.Nil, uuid.Nil,
			testProjectFrame{scoped: true, projectID: open}, company},
		{"payload names a Project that is gone", uuid.Nil, uuid.Nil,
			testProjectFrame{scoped: true, projectID: gone}, nobody},
		{"a Task chat's Project", taskChat, uuid.Nil, testProjectFrame{scoped: true}, readers},
		{"a legacy Task chat with no Project", legacyChat, uuid.Nil, testProjectFrame{scoped: true}, company},
		{"a direct chat falls back to the Task", directChat, task, testProjectFrame{scoped: true}, readers},
		{"a direct chat and no Task keeps its route", directChat, uuid.Nil, testProjectFrame{scoped: true}, nil},
		{"a chat that is gone falls back to the Task", unknownChat, task, testProjectFrame{scoped: true}, readers},
		{"the Task's Project", uuid.Nil, task, testProjectFrame{scoped: true}, readers},
		{"a Task that is gone keeps its route", uuid.Nil, deletedTask, testProjectFrame{scoped: true}, nil},
		{"an unscoped frame is left alone", taskChat, task, testProjectFrame{}, nil},
		{"an already stamped frame is kept", taskChat, task,
			testProjectFrame{scoped: true, ProjectFrame: ProjectFrame{Audience: stamped}}, stamped},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &audienceDB{
				t:        t,
				projects: map[uuid.UUID]string{restricted: "restricted", open: "company"},
				readers:  map[uuid.UUID][]uuid.UUID{restricted: {reader}},
				conversations: map[uuid.UUID]generated.GetConversationProjectIDRow{
					taskChat:   {Kind: "task", ProjectID: uuid.NullUUID{UUID: restricted, Valid: true}},
					legacyChat: {Kind: "task"},
					directChat: {Kind: "direct"},
				},
				tasks: map[uuid.UUID]uuid.UUID{task: restricted},
			}
			pending := []events.Event{{
				Name: "task.updated", CompanyID: companyID,
				ConversationID: test.conversationID, TaskID: test.taskID, Payload: test.payload,
			}}
			if err := StampProjectAudiencesInTx(context.Background(), generated.New(database), pending); err != nil {
				t.Fatalf("StampProjectAudiencesInTx() error = %v", err)
			}
			got := pending[0].Payload.(testProjectFrame).Audience
			if !sameAudience(got, test.want) {
				t.Fatalf("audience = %+v, want %+v", got, test.want)
			}
			if test.payload.Audience != nil && len(database.counts) != 0 {
				t.Fatalf("an already stamped frame ran %v", database.counts)
			}
		})
	}
}

// sameAudience compares two audiences by what they reach.
func sameAudience(a, b *ProjectAudience) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.CompanyWide == b.CompanyWide && slices.Equal(a.UserIDs, b.UserIDs)
}

// One stamping pass reads a Project's access once, however many of its frames
// it stamps and however they name it.
func TestStampProjectAudiencesReadsEachProjectOnce(t *testing.T) {
	t.Parallel()
	companyID, project, taskChat, task := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	reader := uuid.New()
	database := &audienceDB{
		t:        t,
		projects: map[uuid.UUID]string{project: "restricted"},
		readers:  map[uuid.UUID][]uuid.UUID{project: {reader}},
		conversations: map[uuid.UUID]generated.GetConversationProjectIDRow{
			taskChat: {Kind: "task", ProjectID: uuid.NullUUID{UUID: project, Valid: true}},
		},
		tasks: map[uuid.UUID]uuid.UUID{task: project},
	}
	pending := []events.Event{
		{Name: "task.updated", CompanyID: companyID, Payload: testProjectFrame{scoped: true, projectID: project}},
		{Name: "conversation.updated", CompanyID: companyID, ConversationID: taskChat, Payload: testProjectFrame{scoped: true}},
		{Name: "message.created", CompanyID: companyID, ConversationID: taskChat, Payload: testProjectFrame{scoped: true}},
		{Name: "task.report_changed", CompanyID: companyID, TaskID: task, Payload: testProjectFrame{scoped: true}},
	}
	if err := StampProjectAudiencesInTx(context.Background(), generated.New(database), pending); err != nil {
		t.Fatalf("StampProjectAudiencesInTx() error = %v", err)
	}
	for _, event := range pending {
		if got := event.Payload.(testProjectFrame).Audience; got == nil || !slices.Equal(got.UserIDs, []uuid.UUID{reader}) {
			t.Fatalf("%s audience = %+v, want the one reader", event.Name, got)
		}
	}
	want := map[string]int{
		"GetProjectAccessFacts": 1, "ListProjectReaderIDs": 1,
		"GetConversationProjectID": 1, "GetTaskProjectID": 1,
	}
	if !reflect.DeepEqual(database.counts, want) {
		t.Fatalf("statements = %v, want %v", database.counts, want)
	}
}
