//go:build integration

package usage

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// analyticsExplainRecorder runs every statement against the pool and keeps
// the last one's text and arguments, so a test can EXPLAIN exactly what sqlc
// sent.
type analyticsExplainRecorder struct {
	pool *pgxpool.Pool
	sql  string
	args []any
}

func (r *analyticsExplainRecorder) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.sql, r.args = sql, args
	return r.pool.Exec(ctx, sql, args...)
}

func (r *analyticsExplainRecorder) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.sql, r.args = sql, args
	return r.pool.Query(ctx, sql, args...)
}

func (r *analyticsExplainRecorder) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	r.sql, r.args = sql, args
	return r.pool.QueryRow(ctx, sql, args...)
}

// The Analytics reads decide which Projects they may name in the statement
// (ADR 0017). They must check each Project the window touches once, through
// an indexed project_access lookup naming the Project and the viewer: never
// a scan of every grant, never a hashed subplan building the viewer's
// readable set, and never one probe per Run. Same shape as
// execution.TestProjectAccessListsProbeReadersPerRow.
func TestAnalyticsGroupsProbeProjectAccessPerProject(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO projects (company_id, key, title)
		SELECT $1, 'bulk_' || n, 'Bulk ' || n FROM generate_series(1, 40) AS n`, fixture.CompanyA)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			WITH seeded AS MATERIALIZED (
				SELECT project.id AS project_id, gen_random_uuid() AS task_id,
				       gen_random_uuid() AS conversation_id,
				       row_number() OVER (ORDER BY project.id, series.n) AS n
				FROM projects AS project
				CROSS JOIN generate_series(1, 50) AS series(n)
				WHERE project.company_id = $1 AND project.key LIKE 'bulk_%'
			), conversation_rows AS (
				INSERT INTO conversations (id, company_id, kind, task_id, project_id, title)
				SELECT conversation_id, $1, 'task', task_id, project_id, 'Bulk' FROM seeded
			), task_rows AS (
				INSERT INTO tasks (id, company_id, conversation_id, project_id, milestone_type,
				                   number, key, title, assignee_user_id, creator_user_id)
				SELECT task_id, $1, conversation_id, project_id, 'product',
				       1000 + n, 'bulk_task_' || n, 'Bulk ' || n, $2, $2
				FROM seeded
			)
			SELECT 1`, fixture.CompanyA, fixture.UserA)
	})
	dbtest.MustExec(t, ctx, pool, `
		WITH inserted AS (
			INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id,
			                  execution_authorized_by_user_id, status, run_type, started_at, finished_at)
			SELECT $1, $2, tasks.id, tasks.conversation_id, $3, $4, 'completed', 'task',
			       $5::timestamptz, $5::timestamptz + interval '1 minute'
			FROM tasks CROSS JOIN generate_series(1, 2)
			WHERE tasks.company_id = $1 AND tasks.key LIKE 'bulk_task_%'
			RETURNING id
		)
		INSERT INTO run_usage (run_id, provider, model, input_tokens, output_tokens, raw_json)
		SELECT id, 'codex', 'gpt-5.6-luna', 10, 1, '{}' FROM inserted`,
		fixture.CompanyA, fixture.AgentA, fixture.RuntimeA, fixture.UserA, start.Add(time.Hour))
	memberID := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'analytics-explain@example.com', 'Explain')`,
		memberID,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'admin')`,
		fixture.CompanyA,
		memberID,
	)
	var readable string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM projects WHERE company_id = $1 AND key = 'bulk_7'`,
		fixture.CompanyA).Scan(&readable); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
		VALUES ($1, $2::uuid, $3, 'project.read', $3)`, fixture.CompanyA, readable, memberID)
	// Colleagues with grants on every Project, so a scan of project_grants
	// would show.
	dbtest.MustExec(t, ctx, pool, `
		WITH colleague AS (
			INSERT INTO users (email, display_name)
			SELECT 'analytics-colleague-' || n || '@example.com', 'Colleague ' || n FROM generate_series(1, 40) AS n
			RETURNING id
		), membership AS (
			INSERT INTO company_members (company_id, user_id, role)
			SELECT $1, colleague.id, 'member' FROM colleague
			RETURNING user_id
		)
		INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
		SELECT $1, project.id, membership.user_id, permission, $2
		FROM membership
		CROSS JOIN projects AS project
		CROSS JOIN unnest(ARRAY['project.read', 'project.tasks.write', 'project.files.manage']) AS permission
		WHERE project.company_id = $1`, fixture.CompanyA, fixture.UserA)
	// Forty Groups of forty more colleagues, each Group holding grants on
	// every Project, so a scan of company_group_members or
	// project_group_grants would show too (000101).
	dbtest.SeedGroupColleagues(t, ctx, pool, fixture.CompanyA, "analytics-group-colleague", 40,
		"project.tasks.write", "project.files.manage")
	dbtest.MustExec(t, ctx, pool, `ANALYZE`)

	recorder := &analyticsExplainRecorder{pool: pool}
	queries := generated.New(recorder)
	viewer := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: memberID, Role: auth.CompanyRoleAdmin}
	query := AnalyticsQuery{
		Scope: AnalyticsScopeCompany, StartAt: start, EndAt: start.AddDate(0, 0, 1), TimeZone: "UTC",
	}
	grantScan := regexp.MustCompile(
		`(?m)Seq Scan on (project_grants|company_group_members|project_group_grants)( |$)`,
	)
	for _, by := range []string{"project", "task"} {
		groups, err := analyticsGroups(ctx, queries,
			analyticsGroupsParams(viewer, query, query.StartAt, query.EndAt, by, "tokens", 101, 0))
		// One named row for the readable Project (or its 50 Tasks) and one
		// Other work row for the other 39 Projects.
		if want := map[string]int{"project": 2, "task": 51}[by]; err != nil || len(groups) != want {
			t.Fatalf("by %s = %d rows, %v; want %d", by, len(groups), err, want)
		}
		plan, err := pool.Query(ctx, `EXPLAIN (COSTS OFF) `+recorder.sql, recorder.args...)
		if err != nil {
			t.Fatalf("by %s explain: %v", by, err)
		}
		lines, err := pgx.CollectRows(plan, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(lines, "\n")
		if grantScan.MatchString(text) {
			t.Errorf("by %s scans all of a grant or Group table:\n%s", by, text)
		}
		if strings.Contains(text, "hashed SubPlan") {
			t.Errorf("by %s builds the readable set instead of probing per Project:\n%s", by, text)
		}
		// The project_access probe lives in readable_projects, over the
		// distinct Projects of the window, not over the Runs.
		if !grantsOnlyInside(lines, "CTE readable_projects") {
			t.Errorf("by %s reads grants outside readable_projects:\n%s", by, text)
		}
	}
}

// grantsOnlyInside reports whether an EXPLAIN plan reads project_grants and
// does so, with the Group tables, only under the plan node named node: the
// lines after it that are indented deeper than it.
func grantsOnlyInside(lines []string, node string) bool {
	indent := func(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }
	inside, nodeIndent, found := false, 0, false
	for _, line := range lines {
		switch {
		case strings.TrimSpace(line) == node:
			inside, nodeIndent = true, indent(line)
			continue
		case inside && indent(line) <= nodeIndent:
			inside = false
		}
		if strings.Contains(line, "company_group_members") || strings.Contains(line, "project_group_grants") {
			if !inside {
				return false
			}
		}
		if strings.Contains(line, "project_grants") {
			if !inside {
				return false
			}
			found = true
		}
	}
	return found
}
