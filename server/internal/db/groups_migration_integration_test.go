//go:build integration

package db

import (
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// 000101 adds Groups: three tables, a Group arm in
// project_permission_held, project_access (via 'group') and output_access,
// the project_access_paths view, and the Inbox kind project_access_granted
// with the entity project. On a copy with data, a Group member reads a
// restricted Project through the Group alone. Rolling back removes the
// Groups and the Inbox items only Groups write, restores the function, the
// two views and both Inbox CHECKs exactly as the previous release reads
// them, and keeps every other row; migrating up again restores no
// membership.
func TestGroupsMigrationAddsTheGroupArmAndRollsBack(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	migrateTo := func(version uint) {
		t.Helper()
		if err := runMigration(
			ctx,
			pool,
			"migrate",
			func(m *migrate.Migrate) error { return m.Migrate(version) },
		); err != nil {
			t.Fatalf("migrate to %d: %v", version, err)
		}
	}
	// Every definition the rollback restores, by name.
	definitions := func() map[string]string {
		t.Helper()
		found := map[string]string{}
		for name, query := range map[string]string{
			"project_permission_held": `SELECT pg_get_functiondef(oid) FROM pg_proc
				WHERE proname = 'project_permission_held' AND pronamespace = 'public'::regnamespace`,
			"project_access":       `SELECT pg_get_viewdef('project_access')`,
			"output_access":        `SELECT pg_get_viewdef('output_access')`,
			"attachment_access":    `SELECT pg_get_viewdef('attachment_access')`,
			"inbox_items_kind":     `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'inbox_items_kind_check'`,
			"inbox_items_entities": `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'inbox_items_entity_type_check'`,
		} {
			var definition string
			if err := pool.QueryRow(ctx, query).Scan(&definition); err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			found[name] = definition
		}
		return found
	}
	rows := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM projects) + (SELECT count(*) FROM project_grants)
			     + (SELECT count(*) FROM company_members) + (SELECT count(*) FROM inbox_items)
			     + (SELECT count(*) FROM outputs)`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	// The Company and the restricted Project the checks below ask about, set
	// once the fixture is seeded. The package cannot import auth (auth
	// imports it), so the test names the catalog's permissions itself.
	var company, project uuid.UUID
	held := func(userID uuid.UUID, permission string) bool {
		t.Helper()
		var found bool
		if err := pool.QueryRow(ctx, `SELECT project_permission_held($1, $2, $3, $4, '{}', '{}', '{}')`,
			company, project, userID, permission).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	via := func(userID uuid.UUID) string {
		t.Helper()
		var found string
		if err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT via FROM project_access
			WHERE project_id = $1 AND user_id = $2), '')`, project, userID).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}

	migrateTo(100)
	f := dbtest.SeedFixture(t, ctx, pool)
	company, project = f.CompanyA, f.ProjectA
	member := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'finance@groups.example.com', 'Finance')`,
		member,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		f.CompanyA,
		member,
	)
	// A Company Artifact from Project A's Task chat: only its readers see it.
	var output uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs
		(company_id, run_id, title, summary, presentation_fingerprint, owner_user_id, visibility)
		VALUES ($1, $2, 'Report', 'Summary', md5(gen_random_uuid()::text), $3, 'company') RETURNING id`,
		f.CompanyA, f.RunA, f.UserA).Scan(&output); err != nil {
		t.Fatal(err)
	}
	sees := func() bool {
		t.Helper()
		var found bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM output_access WHERE output_id = $1 AND user_id = $2)`,
			output, member).
			Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	previous := definitions()
	if held(member, "project.read") || via(member) != "" || sees() {
		t.Fatal("before 000101 the member reads the restricted Project")
	}
	before := rows()

	migrateTo(101)
	finance := dbtest.SeedGroup(t, ctx, pool, f.CompanyA, "Finance")
	dbtest.AddGroupMembers(t, ctx, pool, f.CompanyA, finance, member)
	dbtest.GrantGroupProject(t, ctx, pool, f.CompanyA, f.ProjectA, finance, "project.tasks.execute")
	if !held(member, "project.read") || !held(member, "project.tasks.execute") ||
		held(member, "project.tasks.write") {
		t.Error("project_permission_held does not follow the Group's grant")
	}
	if got := via(member); got != "group" {
		t.Errorf("project_access via = %q, want group", got)
	}
	if !sees() {
		t.Error("output_access does not reach a Group reader of the source Project")
	}
	var paths int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_access_paths
		WHERE project_id = $1 AND user_id = $2 AND via = 'group' AND group_id = $3`,
		f.ProjectA, member, finance).Scan(&paths); err != nil || paths != 2 {
		t.Errorf("project_access_paths Group rows = %d, %v; want read and execute", paths, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO inbox_items (company_id, recipient_user_id, kind, entity_type, entity_id,
		title, body, dedupe_key, project_id)
		VALUES ($1, $2, 'project_access_granted', 'project', $3::uuid::text, 'Finance can now see A', 'Added',
		        'project:' || $3::uuid::text || ':group_member', $3)`,
		f.CompanyA,
		f.UserA,
		f.ProjectA,
	)
	if after := rows(); after != before+1 {
		t.Fatalf("after 000101: %d rows, want the %d from before and one Inbox item", after, before)
	}

	migrateTo(100)
	for _, table := range []string{"company_groups", "company_group_members", "project_group_grants"} {
		assertTableDoesNotExist(t, ctx, pool, table)
	}
	var pathsView *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('project_access_paths')::text`).
		Scan(&pathsView); err != nil ||
		pathsView != nil {
		t.Errorf("after rollback project_access_paths = %v, %v; want gone", pathsView, err)
	}
	restored := definitions()
	for name, definition := range previous {
		if restored[name] != definition {
			t.Errorf(
				"after rollback %s differs from 000100:\n%s\nwant\n%s",
				name,
				restored[name],
				definition,
			)
		}
	}
	if held(member, "project.read") || via(member) != "" || sees() {
		t.Error("after rollback the former Group member still reads the restricted Project")
	}
	if after := rows(); after != before {
		t.Fatalf(
			"after rollback: %d rows, want the %d from before (the Group's Inbox item gone)",
			after,
			before,
		)
	}

	migrateTo(101)
	var memberships int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM company_group_members`).
		Scan(&memberships); err != nil ||
		memberships != 0 {
		t.Errorf("after migrating up again: %d Group memberships, %v; want none", memberships, err)
	}
	if held(member, "project.read") {
		t.Error("migrating up again restored a Group grant")
	}
}
