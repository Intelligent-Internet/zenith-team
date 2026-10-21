package db

import (
	"strings"
	"testing"
)

func TestProducerFactsDeriveCompanyThroughTheirCompositeOwners(t *testing.T) {
	t.Parallel()

	source := upMigrationsSource(t)
	tests := []struct {
		table string
		owner string
	}{
		{table: "task_report_events", owner: "REFERENCES tasks(company_id, id) ON DELETE CASCADE"},
		{table: "task_report_batches", owner: "REFERENCES conversations(company_id, id) ON DELETE CASCADE"},
		{table: "schedule_firings", owner: "REFERENCES schedules(company_id, id) ON DELETE CASCADE"},
	}
	for _, test := range tests {
		t.Run(test.table, func(t *testing.T) {
			definition := migrationTableDefinition(t, source, test.table)
			if strings.Contains(definition, "REFERENCES companies(id)") {
				t.Fatalf("%s reacquires Company through a late direct foreign key", test.table)
			}
			if !strings.Contains(definition, test.owner) {
				t.Fatalf("%s no longer derives Company through owner edge %q", test.table, test.owner)
			}
		})
	}
}

func migrationTableDefinition(t *testing.T, source, table string) string {
	t.Helper()
	marker := "CREATE TABLE " + table + " ("
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("migration has no %s definition", table)
	}
	rest := source[start:]
	end := strings.Index(rest, "\n);")
	if end < 0 {
		t.Fatalf("migration does not close %s definition", table)
	}
	// A link to a table created in a later file is added there.
	definition := rest[:end]
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(line, "ALTER TABLE "+table+" ADD CONSTRAINT ") {
			definition += "\n" + line
		}
	}
	return definition
}
