//go:build integration

package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// D31 (000099): the delegate rule is two functions and nothing else, so the
// migration changes no row. Rolling back drops both and keeps every row;
// migrating up again restores them.
func TestDelegateRuleMigrationAddsTwoFunctionsAndRollsBack(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTo := func(version uint) {
		t.Helper()
		runner, err := newMigrationRunner(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Migrate(version); err != nil {
			t.Fatalf("migrate to %d: %v", version, err)
		}
		if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
			t.Fatalf("close migration to %d: %v, %v", version, sourceErr, databaseErr)
		}
	}
	functions := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_proc
			WHERE proname IN ('delegate_permission_held', 'run_permission_held')
			  AND pronamespace = 'public'::regnamespace`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	rows := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM runs) + (SELECT count(*) FROM project_grants)
			     + (SELECT count(*) FROM conversations)`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	migrateTo(98)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	before := rows()

	migrateTo(99)
	if got := functions(); got != 2 {
		t.Fatalf("after 000099: %d delegate functions, want 2", got)
	}
	// The package cannot import auth (auth imports it), so the test names
	// the catalog's read permission itself.
	var held bool
	if err := pool.QueryRow(ctx, `SELECT delegate_permission_held($1, $2, $3, NULL, $4, $5, $5, '{}', $6)`,
		fixture.CompanyA, fixture.UserA, fixture.ConversationA, fixture.ProjectA, "project.read",
		fixture.AgentA).Scan(&held); err != nil || !held {
		t.Fatalf("the owner reads their Task chat's Project as a delegate = %t, %v; want true", held, err)
	}

	migrateTo(98)
	if got := functions(); got != 0 {
		t.Fatalf("after rollback: %d delegate functions, want none", got)
	}
	if after := rows(); after != before {
		t.Fatalf("after rollback: %d rows, want the %d from before", after, before)
	}
	migrateTo(99)
	if got := functions(); got != 2 {
		t.Fatalf("after migrating up again: %d delegate functions, want 2", got)
	}
}

// D31, revised 2026-10-06 (000100): a Run uses its Authorizer's access, so
// delegate_permission_held loses the audience and Agent arguments,
// run_permission_held stops reading the Run's invocation, and
// conversation_audience_reads goes. The migration changes no row. Rolling
// back restores the three functions exactly as 000099 left them, which the
// previous release's queries call, and keeps every row; migrating up again
// replaces them once more.
func TestRunsUseTheirAuthorizersAccessMigrationRollsBack(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTo := func(version uint) {
		t.Helper()
		runner, err := newMigrationRunner(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Migrate(version); err != nil {
			t.Fatalf("migrate to %d: %v", version, err)
		}
		if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
			t.Fatalf("close migration to %d: %v, %v", version, sourceErr, databaseErr)
		}
	}
	// Each function's full definition, by its signature.
	definitions := func() map[string]string {
		t.Helper()
		rows, err := pool.Query(ctx, `
			SELECT oid::regprocedure::text, pg_get_functiondef(oid) FROM pg_proc
			WHERE proname IN ('delegate_permission_held', 'run_permission_held', 'conversation_audience_reads')
			  AND pronamespace = 'public'::regnamespace`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		found := map[string]string{}
		for rows.Next() {
			var signature, definition string
			if err := rows.Scan(&signature, &definition); err != nil {
				t.Fatal(err)
			}
			found[signature] = definition
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return found
	}
	signatures := func(found map[string]string) []string {
		names := make([]string, 0, len(found))
		for signature := range found {
			names = append(names, signature)
		}
		slices.Sort(names)
		return names
	}
	rows := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM runs) + (SELECT count(*) FROM project_grants)
			     + (SELECT count(*) FROM conversations) + (SELECT count(*) FROM channel_invocations)`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	migrateTo(99)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	channel := dbtest.SeedDiscussionChannel(t, ctx, pool, fixture.CompanyA, fixture.UserA, nil)
	tagged := dbtest.SeedTaggedRun(
		t,
		ctx,
		pool,
		fixture.CompanyA,
		channel,
		fixture.AgentA,
		fixture.RuntimeA,
		fixture.UserA,
	)
	before, previous := rows(), definitions()

	migrateTo(100)
	want := []string{
		"delegate_permission_held(uuid,uuid,uuid,uuid,text,text,text[])",
		"run_permission_held(uuid,uuid,uuid,text,text,text[])",
	}
	if got := signatures(definitions()); !slices.Equal(got, want) {
		t.Fatalf("after 000100: functions %v, want %v", got, want)
	}
	// The package cannot import auth (auth imports it), so the test names
	// the catalog's read permission itself.
	var held bool
	if err := pool.QueryRow(ctx, `SELECT delegate_permission_held($1, $2, $3, $4, $5, $5, '{}')`,
		fixture.CompanyA, fixture.UserA, fixture.ConversationA, fixture.ProjectA, "project.read",
	).Scan(&held); err != nil || !held {
		t.Fatalf("the owner reads their Task chat's Project as a delegate = %t, %v; want true", held, err)
	}
	// The tagged Run reads with its Authorizer's access: the owner reads
	// Project A, so the Run does.
	if err := pool.QueryRow(ctx, `SELECT run_permission_held($1, $2, $3, $4, $4, '{}')`,
		fixture.CompanyA, tagged, fixture.ProjectA, "project.read",
	).Scan(&held); err != nil || !held {
		t.Fatalf("the tagged Run reads its Authorizer's Project = %t, %v; want true", held, err)
	}
	if after := rows(); after != before {
		t.Fatalf("after 000100: %d rows, want the %d from before", after, before)
	}

	migrateTo(99)
	restored := definitions()
	if got, want := signatures(restored), signatures(previous); !slices.Equal(got, want) {
		t.Fatalf("after rollback: functions %v, want %v", got, want)
	}
	for signature, definition := range previous {
		if restored[signature] != definition {
			t.Errorf(
				"after rollback %s differs from 000099:\n%s\nwant\n%s",
				signature,
				restored[signature],
				definition,
			)
		}
	}
	if after := rows(); after != before {
		t.Fatalf("after rollback: %d rows, want the %d from before", after, before)
	}
	migrateTo(100)
	if got := signatures(definitions()); !slices.Equal(got, want) {
		t.Fatalf("after migrating up again: functions %v, want %v", got, want)
	}
}
