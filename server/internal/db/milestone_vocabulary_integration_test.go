//go:build integration

package db

import (
	"context"
	"regexp"
	"sort"
	"testing"
	"time"

	taskmilestone "github.com/Intelligent-Internet/zenith-team/server/internal/task/milestone"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// The milestone vocabulary is written out in more than one CHECK constraint,
// and the Go catalog is its authority. Rather than push the list into a
// domain or a lookup table — which would move the vocabulary out of versioned
// code and still leave the array column unconstrained — the duplication is
// allowed and diffed here, the same way handler.errorCodeRegistry is diffed
// against docs/api/errors.md in both directions.
var milestoneVocabularyConstraints = []struct {
	table      string
	constraint string
}{
	{table: "tasks", constraint: "tasks_milestone_type_check"},
	{
		table:      "company_milestone_attestations",
		constraint: "company_milestone_attestations_milestone_type_check",
	},
}

func TestMilestoneCheckConstraintsMatchTheGoCatalog(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}

	want := make([]string, 0, len(taskmilestone.Catalog()))
	for _, definition := range taskmilestone.Catalog() {
		want = append(want, string(definition.Type))
	}
	sort.Strings(want)

	literal := regexp.MustCompile(`'([a-z_]+)'`)
	for _, target := range milestoneVocabularyConstraints {
		t.Run(target.constraint, func(t *testing.T) {
			var definition string
			if err := pool.QueryRow(ctx, `
				SELECT pg_get_constraintdef(oid)
				FROM pg_constraint
				WHERE conname = $1
				  AND conrelid = $2::regclass`,
				target.constraint, target.table,
			).Scan(&definition); err != nil {
				t.Fatalf("read %s: %v", target.constraint, err)
			}

			matches := literal.FindAllStringSubmatch(definition, -1)
			got := make([]string, 0, len(matches))
			for _, match := range matches {
				got = append(got, match[1])
			}
			sort.Strings(got)

			if len(got) != len(want) {
				t.Fatalf(
					"%s admits %d milestone values, catalog v%d defines %d:\n%s",
					target.constraint,
					len(got),
					taskmilestone.CatalogVersion,
					len(want),
					definition,
				)
			}
			for index := range want {
				if got[index] != want[index] {
					t.Errorf(
						"%s value %d = %q, catalog has %q",
						target.constraint, index, got[index], want[index],
					)
				}
			}
		})
	}
}
