package dbtest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedGroup creates a Group in the Company (migration 000101) and returns its
// id. The name must be unique in the Company, ignoring case.
func SeedGroup(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
	name string,
) uuid.UUID {
	t.Helper()
	var groupID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO company_groups (company_id, name) VALUES ($1, $2) RETURNING id`,
		companyID, name).Scan(&groupID); err != nil {
		t.Fatalf("seed Group %q: %v", name, err)
	}
	return groupID
}

// AddGroupMembers puts Team members in a Group, as a membership change
// would: each row is stamped now, so it passes the rejoin fence of a member
// who has not rejoined since. Someone already in the Group is left as they
// are.
func AddGroupMembers(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, groupID uuid.UUID,
	userIDs ...uuid.UUID,
) {
	t.Helper()
	MustExec(t, ctx, pool, `
		INSERT INTO company_group_members (company_id, group_id, user_id)
		SELECT $1, $2, member FROM unnest($3::uuid[]) AS added(member)
		ON CONFLICT DO NOTHING`,
		companyID, groupID, userIDs)
}

// GrantGroupProject stores a Group's grant rows on a Project, as GrantProject
// does for a person: project.read is always written with any other
// permission, since project_group_grants_read_fkey refuses a row without it,
// and with no permissions it grants read, write and execute.
func GrantGroupProject(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, projectID, groupID uuid.UUID,
	permissions ...string,
) {
	t.Helper()
	if len(permissions) == 0 {
		permissions = []string{"project.tasks.write", "project.tasks.execute"}
	}
	permissions = append([]string{"project.read"}, permissions...)
	MustExec(t, ctx, pool, `
		INSERT INTO project_group_grants (company_id, project_id, group_id, permission)
		SELECT $1, $2, $3, requested.permission
		FROM (SELECT DISTINCT permission FROM unnest($4::text[]) AS listed(permission)) AS requested
		ON CONFLICT (company_id, project_id, group_id, permission) DO UPDATE SET updated_at = clock_timestamp()`,
		companyID, projectID, groupID, permissions)
}

// SeedGroupColleagues adds count Team members and count Groups, puts every
// one of those members in every one of those Groups, and grants each Group
// project.read plus permissions on every Project the Company has now. The
// members read every Project through their Groups alone. EXPLAIN pins use it
// so that company_group_members and project_group_grants are large enough
// for a scan of either to show. prefix keeps the emails and Group names of
// two calls in one database apart.
func SeedGroupColleagues(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
	prefix string,
	count int,
	permissions ...string,
) {
	t.Helper()
	permissions = append([]string{"project.read"}, permissions...)
	MustExec(t, ctx, pool, `
		WITH colleague AS (
			INSERT INTO users (email, display_name)
			SELECT $2 || '-' || n || '@example.com', $2 || ' ' || n FROM generate_series(1, $3::int) AS n
			RETURNING id
		), membership AS (
			INSERT INTO company_members (company_id, user_id, role)
			SELECT $1, colleague.id, 'member' FROM colleague
			RETURNING user_id
		), seeded_group AS (
			INSERT INTO company_groups (company_id, name)
			SELECT $1, $2 || ' Group ' || n FROM generate_series(1, $3::int) AS n
			RETURNING id
		), group_membership AS (
			INSERT INTO company_group_members (company_id, group_id, user_id)
			SELECT $1, seeded_group.id, membership.user_id FROM seeded_group CROSS JOIN membership
		)
		INSERT INTO project_group_grants (company_id, project_id, group_id, permission)
		SELECT $1, project.id, seeded_group.id, requested.permission
		FROM seeded_group
		CROSS JOIN projects AS project
		CROSS JOIN (SELECT DISTINCT permission FROM unnest($4::text[]) AS listed(permission)) AS requested
		WHERE project.company_id = $1`,
		companyID, prefix, count, permissions)
}
