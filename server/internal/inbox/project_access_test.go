package inbox

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// scriptedDBTX is a generated.DBTX that records each statement by its sqlc
// name and answers from a script: rows answers single-row reads (a missing
// entry is pgx.ErrNoRows), many answers multi-row reads. It pins which
// statements the Project access branches (ADR 0017) run without PostgreSQL.
type scriptedDBTX struct {
	statements []string
	rows       map[string]func(dest ...any) error
	many       map[string][][]any
}

func statementName(sql string) string {
	name, _, _ := strings.Cut(strings.TrimPrefix(sql, "-- name: "), " ")
	return name
}

func (d *scriptedDBTX) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	d.statements = append(d.statements, statementName(sql))
	return pgconn.CommandTag{}, nil
}

func (d *scriptedDBTX) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	name := statementName(sql)
	d.statements = append(d.statements, name)
	return &scriptedRows{rows: d.many[name]}, nil
}

func (d *scriptedDBTX) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	name := statementName(sql)
	d.statements = append(d.statements, name)
	return scriptedRow{scan: d.rows[name]}
}

type scriptedRow struct{ scan func(dest ...any) error }

func (r scriptedRow) Scan(dest ...any) error {
	if r.scan == nil {
		return pgx.ErrNoRows
	}
	return r.scan(dest...)
}

// scriptedRows yields each scripted row, assigning its non-nil values to
// the destinations at the same positions.
type scriptedRows struct {
	rows [][]any
	next int
}

func (r *scriptedRows) Close()                                       {}
func (r *scriptedRows) Err() error                                   { return nil }
func (r *scriptedRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *scriptedRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *scriptedRows) RawValues() [][]byte                          { return nil }
func (r *scriptedRows) Conn() *pgx.Conn                              { return nil }
func (r *scriptedRows) TypeMap() *pgtype.Map                         { return nil }
func (r *scriptedRows) Values() ([]any, error)                       { return nil, errors.New("not scripted") }

func (r *scriptedRows) Next() bool {
	r.next++
	return r.next <= len(r.rows)
}

func (r *scriptedRows) Scan(dest ...any) error {
	assignColumns(dest, r.rows[r.next-1])
	return nil
}

// assignColumns stores each non-nil value into the destination at its index.
func assignColumns(dest, values []any) {
	for index, value := range values {
		if value != nil {
			reflect.ValueOf(dest[index]).Elem().Set(reflect.ValueOf(value))
		}
	}
}

func scanColumns(values ...any) func(dest ...any) error {
	return func(dest ...any) error {
		assignColumns(dest, values)
		return nil
	}
}

// activeRecipient scripts the Company and membership locks CreateInTx takes
// first: GetCompanyMemberForShare's fourth column is the status.
func activeRecipient() map[string]func(dest ...any) error {
	return map[string]func(dest ...any) error{
		"LockCompanyForMembershipRead": scanColumns(),
		"GetCompanyMemberForShare":     scanColumns(nil, nil, nil, "active"),
		"GetInboxCountsForRecipient":   scanColumns(),
	}
}

// A recipient who cannot read the Project the item is about is not told
// about it: nothing is inserted and nothing is staged (ADR 0017).
func TestCreateInTxSkipsARecipientWhoCannotReadTheProject(t *testing.T) {
	t.Parallel()
	database := &scriptedDBTX{rows: activeRecipient()}
	database.rows["ResolveInboxItemProject"] = scanColumns(uuid.New(), false)
	var pending []events.Event

	result, err := CreateInTx(context.Background(), generated.New(database), &pending, validItemCreateInput())
	if err != nil {
		t.Fatalf("CreateInTx() error = %v", err)
	}
	if !reflect.DeepEqual(result, CreateResult{}) || len(pending) != 0 {
		t.Fatalf("CreateInTx() = %+v with %d frames, want nothing", result, len(pending))
	}
	if slices.Contains(database.statements, "CreateInboxItem") {
		t.Fatalf("statements = %v, want no insert for a non-reader", database.statements)
	}
}

// A replayed create whose stored row the recipient no longer reads (its
// Project is hidden from them) is a skip, not an invariant violation.
func TestCreateInTxReplayOfAnItemTheRecipientNoLongerReadsIsASkip(t *testing.T) {
	t.Parallel()
	database := &scriptedDBTX{rows: activeRecipient()}
	var pending []events.Event

	result, err := CreateInTx(context.Background(), generated.New(database), &pending, validItemCreateInput())
	if err != nil {
		t.Fatalf("CreateInTx() error = %v", err)
	}
	if !reflect.DeepEqual(result, CreateResult{}) || len(pending) != 0 {
		t.Fatalf("CreateInTx() = %+v with %d frames, want nothing", result, len(pending))
	}
	want := []string{
		"LockCompanyForMembershipRead", "GetCompanyMemberForShare", "ResolveInboxItemProject",
		"CreateInboxItem", "GetInboxItemByDedupeKey",
	}
	if !slices.Equal(database.statements, want) {
		t.Fatalf("statements = %v, want %v", database.statements, want)
	}
}

// A personal item has no Company, so it is about no Project and no lookup
// runs.
func TestCreateInTxLooksUpNoProjectForAPersonalItem(t *testing.T) {
	t.Parallel()
	database := &scriptedDBTX{rows: map[string]func(dest ...any) error{
		"LockEnabledUserForMembershipMutation": scanColumns(),
	}}
	input := validItemCreateInput()
	input.CompanyID = uuid.Nil
	input.Type = TypeCalendarReminder
	var pending []events.Event

	if _, err := CreateInTx(context.Background(), generated.New(database), &pending, input); err != nil {
		t.Fatalf("CreateInTx() error = %v", err)
	}
	if slices.Contains(database.statements, "ResolveInboxItemProject") {
		t.Fatalf("statements = %v, want no Project lookup for a personal item", database.statements)
	}
}

// Closing an entity closes every copy, but a recipient who no longer reads
// the item's Project gets no frame, which would carry its title.
func TestCloseEntityInTxStagesNoFrameForAnUnreadableProject(t *testing.T) {
	t.Parallel()
	hidden, shown := uuid.New(), uuid.New()
	project := uuid.NullUUID{UUID: uuid.New(), Valid: true}
	row := func(id uuid.UUID, project uuid.NullUUID) []any {
		values := make([]any, 18)
		values[0], values[2], values[17] = id, uuid.New(), project
		return values
	}
	database := &scriptedDBTX{
		rows: map[string]func(dest ...any) error{
			"GetInboxCountsForRecipient": scanColumns(),
			"InboxRecipientReadsProject": scanColumns(false),
		},
		many: map[string][][]any{
			"CloseInboxEntityItems": {row(hidden, project), row(shown, uuid.NullUUID{})},
		},
	}
	var pending []events.Event

	if err := CloseEntityInTx(context.Background(), generated.New(database), &pending, uuid.New(),
		Entity{Type: protocol.EntityTask, ID: uuid.NewString()}, Actor{Kind: "system"}); err != nil {
		t.Fatalf("CloseEntityInTx() error = %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("staged %d frames, want only the item about no Project", len(pending))
	}
	if payload, ok := pending[0].Payload.(ItemWithCounts); !ok || payload.ID != shown {
		t.Fatalf("staged frame %+v, want the item %s", pending[0].Payload, shown)
	}
	if got := countOf(database.statements, "InboxRecipientReadsProject"); got != 1 {
		t.Fatalf("InboxRecipientReadsProject ran %d times, want once (only the item with a Project)", got)
	}
}

func countOf(statements []string, name string) int {
	count := 0
	for _, statement := range statements {
		if statement == name {
			count++
		}
	}
	return count
}
