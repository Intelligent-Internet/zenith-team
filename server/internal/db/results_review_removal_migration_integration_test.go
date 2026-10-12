//go:build integration

package db

import (
	"slices"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// 000102 (D31 revision, 2026-10-07) removes project.results.review: every
// person's and Group's row holding it goes, every other row stays, each
// Project that lost a row advances its access revision, and neither grant
// table accepts the permission again. Rolling back lets the tables store it
// again but brings no deleted row back.
func TestResultsReviewRemovalDeletesTheRowsAndRefusesNewOnes(t *testing.T) {
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
	const review = "project.results.review"
	type revision struct {
		version int64
		changed time.Time
	}
	revisionOf := func(projectID uuid.UUID) revision {
		t.Helper()
		var found revision
		if err := pool.QueryRow(ctx, `SELECT access_version, access_changed_at FROM projects WHERE id = $1`,
			projectID).Scan(&found.version, &found.changed); err != nil {
			t.Fatal(err)
		}
		return found
	}
	personGrants := func(projectID, userID uuid.UUID) []string {
		t.Helper()
		var permissions []string
		if err := pool.QueryRow(ctx, `SELECT COALESCE(array_agg(permission ORDER BY permission), '{}')
			FROM project_grants WHERE project_id = $1 AND user_id = $2`, projectID, userID).
			Scan(&permissions); err != nil {
			t.Fatal(err)
		}
		return permissions
	}
	groupGrants := func(projectID, groupID uuid.UUID) []string {
		t.Helper()
		var permissions []string
		if err := pool.QueryRow(ctx, `SELECT COALESCE(array_agg(permission ORDER BY permission), '{}')
			FROM project_group_grants WHERE project_id = $1 AND group_id = $2`, projectID, groupID).
			Scan(&permissions); err != nil {
			t.Fatal(err)
		}
		return permissions
	}

	migrateTo(101)
	// The fixture gives each owner every permission the CHECK lists, review
	// included. A member holds review on a Project of their own; a Group
	// holds it on Project A. A third Project nobody holds review on keeps
	// its revision.
	f := dbtest.SeedFixture(t, ctx, pool)
	member := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
		member, "reviewer@removal.example.com")
	dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		f.CompanyA, member)
	untouched := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, 'quiet', 'Quiet')`,
		untouched, f.CompanyA)
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, untouched, member)
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, member, "project.tasks.write", review)
	finance := dbtest.SeedGroup(t, ctx, pool, f.CompanyA, "Finance")
	dbtest.AddGroupMembers(t, ctx, pool, f.CompanyA, finance, member)
	dbtest.GrantGroupProject(t, ctx, pool, f.CompanyA, f.ProjectA, finance, "project.approvals.resolve", review)
	if !slices.Contains(personGrants(f.ProjectA, f.UserA), review) {
		t.Fatal("before 000102 the owner does not hold review")
	}
	before := map[uuid.UUID]revision{}
	for _, project := range []uuid.UUID{f.ProjectA, f.ProjectB, untouched} {
		before[project] = revisionOf(project)
	}

	migrateTo(102)
	nine := []string{
		"project.approvals.resolve", "project.archive", "project.files.manage", "project.grants.manage",
		"project.read", "project.settings.update", "project.tasks.execute", "project.tasks.write",
		"project.visibility.update",
	}
	if got := personGrants(f.ProjectA, f.UserA); !slices.Equal(got, nine) {
		t.Errorf("owner's grant after 000102 = %v, want %v", got, nine)
	}
	if got, want := personGrants(f.ProjectA, member), []string{"project.read", "project.tasks.write"}; !slices.Equal(got, want) {
		t.Errorf("member's grant after 000102 = %v, want %v", got, want)
	}
	if got, want := personGrants(untouched, member),
		[]string{"project.read", "project.tasks.execute", "project.tasks.write"}; !slices.Equal(got, want) {
		t.Errorf("member's grant on the untouched Project = %v, want %v", got, want)
	}
	if got, want := groupGrants(f.ProjectA, finance),
		[]string{"project.approvals.resolve", "project.read"}; !slices.Equal(got, want) {
		t.Errorf("Group's grant after 000102 = %v, want %v", got, want)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM project_grants WHERE permission = $1)
		+ (SELECT count(*) FROM project_group_grants WHERE permission = $1)`, review).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("after 000102 %d rows still hold %s", left, review)
	}
	for _, project := range []uuid.UUID{f.ProjectA, f.ProjectB} {
		after := revisionOf(project)
		if after.version != before[project].version+1 || !after.changed.After(before[project].changed) {
			t.Errorf("Project %s revision %+v, want one past %+v with a newer change time",
				project, after, before[project])
		}
	}
	if after := revisionOf(untouched); after != before[untouched] {
		t.Errorf("untouched Project revision %+v, want %+v", after, before[untouched])
	}
	dbtest.AssertStatementRejected(t, ctx, pool, "23514",
		`INSERT INTO project_grants (company_id, project_id, user_id, permission) VALUES ($1, $2, $3, $4)`,
		f.CompanyA, f.ProjectA, member, review)
	dbtest.AssertStatementRejected(t, ctx, pool, "23514",
		`INSERT INTO project_group_grants (company_id, project_id, group_id, permission) VALUES ($1, $2, $3, $4)`,
		f.CompanyA, f.ProjectA, finance, review)

	migrateTo(101)
	if got := personGrants(f.ProjectA, f.UserA); slices.Contains(got, review) {
		t.Errorf("rolling back restored the owner's review row: %v", got)
	}
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, member, review)
	dbtest.GrantGroupProject(t, ctx, pool, f.CompanyA, f.ProjectA, finance, review)

	migrateTo(102)
	if got, want := personGrants(f.ProjectA, member), []string{"project.read", "project.tasks.write"}; !slices.Equal(got, want) {
		t.Errorf("member's grant after migrating up again = %v, want %v", got, want)
	}
	if got, want := groupGrants(f.ProjectA, finance),
		[]string{"project.approvals.resolve", "project.read"}; !slices.Equal(got, want) {
		t.Errorf("Group's grant after migrating up again = %v, want %v", got, want)
	}
}
