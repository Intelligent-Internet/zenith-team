package activity

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

// errStop ends a scripted run at the statement under test.
var errStop = errors.New("scripted stop")

// recordingDBTX is a db.DBTX that records each statement by its sqlc name
// with its arguments. Single-row reads answer from rows (a missing entry is
// pgx.ErrNoRows); a multi-row read answers from lists, else fails with
// errStop after the arguments are recorded. It pins what the Project
// access code (ADR 0017) binds without PostgreSQL.
type recordingDBTX struct {
	calls []recordedCall
	rows  map[string]func(dest ...any) error
	// lists answers a one-column list of ids by statement name.
	lists map[string][]uuid.UUID
}

type recordedCall struct {
	name string
	args []any
}

func (d *recordingDBTX) record(sql string, args []any) string {
	name, _, _ := strings.Cut(strings.TrimPrefix(sql, "-- name: "), " ")
	d.calls = append(d.calls, recordedCall{name: name, args: args})
	return name
}

func (d *recordingDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.record(sql, args)
	return pgconn.CommandTag{}, nil
}

func (d *recordingDBTX) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	if ids, ok := d.lists[d.record(sql, args)]; ok {
		return &idRows{ids: ids}, nil
	}
	return nil, errStop
}

func (d *recordingDBTX) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	return recordedRow{scan: d.rows[d.record(sql, args)]}
}

type recordedRow struct{ scan func(dest ...any) error }

func (r recordedRow) Scan(dest ...any) error {
	if r.scan == nil {
		return pgx.ErrNoRows
	}
	return r.scan(dest...)
}

// call returns the one recorded call of a statement.
func (d *recordingDBTX) call(t *testing.T, name string) recordedCall {
	t.Helper()
	var found []recordedCall
	for _, call := range d.calls {
		if call.name == name {
			found = append(found, call)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s ran %d times, want once; statements: %v", name, len(found), d.names())
	}
	return found[0]
}

func (d *recordingDBTX) names() []string {
	names := make([]string, len(d.calls))
	for index, call := range d.calls {
		names[index] = call.name
	}
	return names
}

func hasArg(args []any, want any) bool {
	return slices.ContainsFunc(args, func(arg any) bool { return reflect.DeepEqual(arg, want) })
}

// namesAnID reports whether any optional id argument is set.
func namesAnID(args []any) bool {
	return slices.ContainsFunc(args, func(arg any) bool {
		id, ok := arg.(uuid.NullUUID)
		return ok && id.Valid
	})
}

// The feed and the calendar let the members whose role holds
// projects.manage read project_grant rows; the roles come from the
// permission catalog, never a literal (ADR 0017).
func TestFeedBindsProjectManagementRoles(t *testing.T) {
	t.Parallel()
	managers := auth.RolesWith(auth.PermissionProjectsManage)
	if len(managers) == 0 {
		t.Fatal("no role holds projects.manage")
	}
	database := &recordingDBTX{}
	feed := NewFeed(db.New(database))
	companyID, userID := uuid.New(), uuid.New()

	if _, _, err := feed.List(context.Background(), companyID, ListOptions{UserID: userID, Limit: 10}); !errors.Is(err, errStop) {
		t.Fatalf("List() error = %v, want the scripted stop", err)
	}
	if list := database.call(t, "ListActivity"); !hasArg(list.args, managers) || !hasArg(list.args, userID) {
		t.Fatalf("ListActivity args = %v, want the reader %s and management roles %v", list.args, userID, managers)
	}
	now := time.Now().UTC()
	if _, err := feed.Calendar(context.Background(), companyID, CalendarOptions{
		UserID: userID, TimeZone: "UTC", Location: time.UTC, StartAt: now, EndAt: now.Add(time.Hour),
	}); !errors.Is(err, errStop) {
		t.Fatalf("Calendar() error = %v, want the scripted stop", err)
	}
	if count := database.call(t, "CountActivityByLocalDay"); !hasArg(count.args, managers) ||
		!hasArg(count.args, userID) {
		t.Fatalf("CountActivityByLocalDay args = %v, want the reader %s and management roles %v",
			count.args, userID, managers)
	}
}

// The bus recorder keeps the envelope's Task and chat on the row and stores
// the Project they resolve to.
func TestRecordStoresTheEnvelopeAndItsProject(t *testing.T) {
	t.Parallel()
	project := uuid.New()
	database := &recordingDBTX{rows: map[string]func(dest ...any) error{
		"ResolveActivityProject": func(dest ...any) error {
			*dest[0].(*uuid.UUID) = project
			return nil
		},
		"CreateActivity": func(...any) error { return errStop },
	}}
	companyID, taskID, conversationID, subject := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	err := record(context.Background(), nil, db.New(database), events.Event{
		EntityType: "attachment", Name: "attachment.deleted", CompanyID: companyID,
		TaskID: taskID, ConversationID: conversationID, Actor: events.SystemActor(),
		Payload: map[string]any{"id": subject.String()},
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("record() error = %v, want the scripted stop at CreateActivity", err)
	}
	resolve := database.call(t, "ResolveActivityProject")
	for _, want := range []any{
		uuid.NullUUID{UUID: taskID, Valid: true}, uuid.NullUUID{UUID: conversationID, Valid: true},
		uuid.NullUUID{UUID: subject, Valid: true},
	} {
		if !hasArg(resolve.args, want) {
			t.Errorf("ResolveActivityProject args = %v, want %v", resolve.args, want)
		}
	}
	create := database.call(t, "CreateActivity")
	for _, want := range []any{
		uuid.NullUUID{UUID: taskID, Valid: true}, uuid.NullUUID{UUID: conversationID, Valid: true},
		uuid.NullUUID{UUID: project, Valid: true},
	} {
		if !hasArg(create.args, want) {
			t.Errorf("CreateActivity args = %v, want %v", create.args, want)
		}
	}
}

// An event about no Company is about no Project: no lookup runs, and a
// subject that is not an id is passed as no subject.
func TestRecordResolvesNoProjectWithoutACompany(t *testing.T) {
	t.Parallel()
	database := &recordingDBTX{rows: map[string]func(dest ...any) error{
		"CreateActivity": func(...any) error { return errStop },
	}}
	err := record(context.Background(), nil, db.New(database), events.Event{
		EntityType: "account", Name: "account.updated", Actor: events.SystemActor(),
		Payload: map[string]any{"id": "not-an-id"},
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("record() error = %v, want the scripted stop at CreateActivity", err)
	}
	if slices.Contains(database.names(), "ResolveActivityProject") {
		t.Fatalf("statements = %v, want no Project lookup without a Company", database.names())
	}
	if create := database.call(t, "CreateActivity"); namesAnID(create.args) {
		t.Fatalf("CreateActivity args = %v, want no Project, Task or chat", create.args)
	}

	database = &recordingDBTX{rows: map[string]func(dest ...any) error{
		"CreateActivity": func(...any) error { return errStop },
	}}
	companyID := uuid.New()
	err = record(context.Background(), nil, db.New(database), events.Event{
		EntityType: "mailbox", Name: "mailbox.connected", CompanyID: companyID, Actor: events.SystemActor(),
		Payload: map[string]any{"id": "not-an-id"},
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("record() error = %v, want the scripted stop at CreateActivity", err)
	}
	if resolve := database.call(t, "ResolveActivityProject"); namesAnID(resolve.args) {
		t.Fatalf("ResolveActivityProject args = %v, want no subject id", resolve.args)
	}
}

// A domain receipt that names no Project reads it from its subject; one
// that names a Project stores it without a lookup.
func TestRecordEvidenceInTxReadsTheProjectFromItsSubject(t *testing.T) {
	t.Parallel()
	project, subject := uuid.New(), uuid.New()
	database := &recordingDBTX{rows: map[string]func(dest ...any) error{
		"ResolveActivityProject": func(dest ...any) error {
			*dest[0].(*uuid.UUID) = project
			return nil
		},
		"CreateActivity": func(...any) error { return errStop },
	}}
	var pending []events.Event
	input := EvidenceInput{
		CompanyID: uuid.New(), EntityID: subject, Action: "run.evidence_recorded",
		Actor: events.SystemActor(), Payload: map[string]any{},
	}
	if err := RecordEvidenceInTx(context.Background(), db.New(database), &pending, input); !errors.Is(err, errStop) {
		t.Fatalf("RecordEvidenceInTx() error = %v, want the scripted stop", err)
	}
	if resolve := database.call(t, "ResolveActivityProject"); !hasArg(resolve.args, "run") ||
		!hasArg(resolve.args, uuid.NullUUID{UUID: subject, Valid: true}) {
		t.Fatalf("ResolveActivityProject args = %v, want the run subject %s", resolve.args, subject)
	}
	if create := database.call(t, "CreateActivity"); !hasArg(create.args, uuid.NullUUID{UUID: project, Valid: true}) {
		t.Fatalf("CreateActivity args = %v, want Project %s", create.args, project)
	}

	named := uuid.New()
	database.calls = nil
	input.ProjectID = named
	if err := RecordEvidenceInTx(context.Background(), db.New(database), &pending, input); !errors.Is(err, errStop) {
		t.Fatalf("RecordEvidenceInTx() error = %v, want the scripted stop", err)
	}
	if slices.Contains(database.names(), "ResolveActivityProject") {
		t.Fatalf("statements = %v, want no lookup when the receipt names its Project", database.names())
	}
	if create := database.call(t, "CreateActivity"); !hasArg(create.args, uuid.NullUUID{UUID: named, Valid: true}) {
		t.Fatalf("CreateActivity args = %v, want Project %s", create.args, named)
	}
}
