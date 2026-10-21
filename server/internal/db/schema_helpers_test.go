package db

// Shared helpers for the schema and migration tests. They live here rather
// than in one of the test files so a later reorganization of those files
// cannot break the others' build.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/migrations"
)

// normalizedBaselineSQL reads the named baseline migration files and returns
// their combined text lowercased with all whitespace runs collapsed to single
// spaces, so shape assertions are insensitive to statement formatting.
func normalizedBaselineSQL(t *testing.T, filenames ...string) string {
	t.Helper()
	var builder strings.Builder
	for _, filename := range filenames {
		content, err := migrations.Files.ReadFile(filename)
		if err != nil {
			t.Fatalf("read baseline migration %s: %v", filename, err)
		}
		builder.Write(content)
		builder.WriteByte('\n')
	}
	return strings.Join(strings.Fields(strings.ToLower(builder.String())), " ")
}

func assertStatementRejected(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	wantCode string,
	statement string,
	args ...any,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rejected-statement transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, statement, args...)
	if err == nil {
		_, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("statement error = %v, want PostgreSQL SQLSTATE %s", err, wantCode)
	}
	if pgErr.Code != wantCode {
		t.Fatalf("statement SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, wantCode)
	}
}
