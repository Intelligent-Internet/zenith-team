package task

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// errReportStop ends a scripted run once the statement under test recorded
// its arguments.
var errReportStop = errors.New("scripted stop")

// reportDBTX records each statement by its sqlc name with its arguments.
// Counts read 0; every multi-row read stops with errReportStop.
type reportDBTX struct {
	calls map[string][]any
}

func (d *reportDBTX) record(sql string, args []any) {
	name, _, _ := strings.Cut(strings.TrimPrefix(sql, "-- name: "), " ")
	if d.calls == nil {
		d.calls = map[string][]any{}
	}
	d.calls[name] = args
}

func (d *reportDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.record(sql, args)
	return pgconn.CommandTag{}, nil
}

func (d *reportDBTX) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	d.record(sql, args)
	return nil, errReportStop
}

func (d *reportDBTX) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	d.record(sql, args)
	return reportRow{}
}

type reportRow struct{}

func (reportRow) Scan(...any) error { return nil }

// A report batch lands in the asking member's direct chat, so the preview
// count, the preview page and the send lock all read only the reports whose
// Project that member reads: each binds the member as the reader (ADR 0017).
// Proactive delivery goes through the same send with the member who turned
// proactive mode on.
func TestPendingReportReadsBindTheAskingMember(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	agentID := uuid.New()
	database := &reportDBTX{}
	queries := generated.New(database)

	if _, _, err := readablePendingReports(context.Background(), queries, principal, agentID); !errors.Is(err, errReportStop) {
		t.Fatalf("readablePendingReports() error = %v, want the scripted stop", err)
	}
	if _, err := lockReadablePendingReports(context.Background(), queries, principal, agentID); !errors.Is(err, errReportStop) {
		t.Fatalf("lockReadablePendingReports() error = %v, want the scripted stop", err)
	}
	for _, name := range []string{
		"CountPendingTaskReportEvents", "ListPendingTaskReportEvents", "LockPendingTaskReportEvents",
	} {
		args, ran := database.calls[name]
		if !ran {
			t.Errorf("%s did not run", name)
			continue
		}
		if !slices.Contains(args, any(principal.UserID)) || !slices.Contains(args, any(agentID)) {
			t.Errorf("%s args = %v, want the reader %s and the Agent %s", name, args, principal.UserID, agentID)
		}
	}
}
