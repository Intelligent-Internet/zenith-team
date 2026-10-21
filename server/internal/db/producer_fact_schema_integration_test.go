//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestProducerFactsHaveNoLateDirectCompanyForeignKeys(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT source.relname, constraint_row.conname
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS source ON source.oid = constraint_row.conrelid
		WHERE constraint_row.contype = 'f'
		  AND constraint_row.confrelid = 'companies'::regclass
		  AND source.relname = ANY($1::text[])
		ORDER BY source.relname, constraint_row.conname`,
		[]string{"schedule_firings", "task_report_batches", "task_report_events"})
	if err != nil {
		t.Fatalf("read producer Company foreign keys: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, name string
		if err := rows.Scan(&table, &name); err != nil {
			t.Fatalf("scan producer Company foreign key: %v", err)
		}
		t.Errorf("%s retains late direct Company foreign key %s", table, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate producer Company foreign keys: %v", err)
	}

	wantOwners := map[string]string{
		"schedule_firings_schedule_fkey":        "foreign key (company_id, schedule_id)",
		"task_report_batches_conversation_fkey": "foreign key (company_id, conversation_id)",
		"task_report_events_task_fkey":          "foreign key (company_id, task_id)",
	}
	for name, fragment := range wantOwners {
		var definition string
		if err := pool.QueryRow(ctx, `
			SELECT pg_get_constraintdef(oid)
			FROM pg_constraint
			WHERE conname = $1`, name).Scan(&definition); err != nil {
			t.Fatalf("read owner foreign key %s: %v", name, err)
		}
		if !strings.Contains(strings.ToLower(definition), fragment) {
			t.Errorf("owner foreign key %s = %q, want %q", name, definition, fragment)
		}
	}
}
