//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestRunHistoryUsesDatabaseUUIDIdentity(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var dataType, defaultExpression string
	err := pool.QueryRow(ctx, `
		SELECT udt_name, column_default FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'runs' AND column_name = 'id'
	`).Scan(&dataType, &defaultExpression)
	if err != nil {
		t.Fatal(err)
	}
	if dataType != "uuid" || !strings.Contains(defaultExpression, "gen_random_uuid()") {
		t.Fatalf("Run identity = %s DEFAULT %s, want database-generated UUID", dataType, defaultExpression)
	}
	var numericReferences int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND udt_name = 'int8'
		  AND (column_name = 'run_id' OR column_name LIKE '%\_run_id' ESCAPE '\')
	`).Scan(&numericReferences); err != nil {
		t.Fatal(err)
	}
	if numericReferences != 0 {
		t.Fatalf("numeric Run reference columns = %d, want 0", numericReferences)
	}
}
