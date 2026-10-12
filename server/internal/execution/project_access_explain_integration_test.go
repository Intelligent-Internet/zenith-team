//go:build integration

package execution

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	internaldb "boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

// explainRecorder runs every statement against the pool and keeps the last
// one's text and arguments, so a test can EXPLAIN exactly what sqlc sent.
type explainRecorder struct {
	pool *pgxpool.Pool
	sql  string
	args []any
}

func (r *explainRecorder) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.sql, r.args = sql, args
	return r.pool.Exec(ctx, sql, args...)
}

func (r *explainRecorder) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.sql, r.args = sql, args
	return r.pool.Query(ctx, sql, args...)
}

func (r *explainRecorder) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	r.sql, r.args = sql, args
	return r.pool.QueryRow(ctx, sql, args...)
}

// Run history and the Approval and Question lists decide Project access in
// the statement (ADR 0017). They reach conversation_readers through a
// per-row scalar lookup that must stay one probe per row (an EXISTS there
// became a hashed subplan that scanned every chat). All must read the
// person's grants and Groups through an index as the Company grows. Which rows
// each list walks is the planner's choice and not pinned here.
func TestProjectAccessListsProbeReadersPerRow(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := internaldb.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
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
		INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id,
		                  execution_authorized_by_user_id, status, run_type)
		SELECT $1, $2, tasks.id, tasks.conversation_id, $3, $4, 'completed', 'task'
		FROM tasks CROSS JOIN generate_series(1, 2)
		WHERE tasks.company_id = $1 AND tasks.key LIKE 'bulk_task_%'`,
		fixture.CompanyA, fixture.AgentA, fixture.RuntimeA, fixture.UserA)
	// An Approval and a Question per seeded Run, each raised in its Task chat.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO approvals (company_id, rule_key, title, preview_markdown, requested_by_agent_id,
		                       origin_run_id, request_key, task_id, conversation_id, canonical_action,
		                       request_sha256, action_sha256, expires_at)
		SELECT runs.company_id, 'company_plan_apply', 'Apply', '', runs.agent_id,
		       runs.id, gen_random_uuid(), runs.task_id, runs.conversation_id, '{}'::jsonb,
		       decode(repeat('11', 32), 'hex'), decode(repeat('11', 32), 'hex'), now() + interval '1 day'
		FROM runs WHERE runs.company_id = $1 AND runs.task_id IS NOT NULL`, fixture.CompanyA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO questions (company_id, title, asked_by_agent_id, origin_run_id, request_key,
		                       task_id, conversation_id, prompts, request_sha256, expires_at)
		SELECT runs.company_id, 'Choose', runs.agent_id, runs.id, gen_random_uuid(),
		       runs.task_id, runs.conversation_id, '[]'::jsonb, decode(repeat('11', 32), 'hex'),
		       now() + interval '1 day'
		FROM runs WHERE runs.company_id = $1 AND runs.task_id IS NOT NULL`, fixture.CompanyA)
	memberID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'explain@example.com', 'Explain')`, memberID)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`, fixture.CompanyA, memberID)
	var readable uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM projects WHERE company_id = $1 AND key = 'bulk_7'`,
		fixture.CompanyA).Scan(&readable); err != nil {
		t.Fatal(err)
	}
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, readable, memberID, "project.files.manage")
	// Colleagues with grants on every Project, so a scan of project_grants
	// would show.
	dbtest.MustExec(t, ctx, pool, `
		WITH colleague AS (
			INSERT INTO users (email, display_name)
			SELECT 'colleague-' || n || '@example.com', 'Colleague ' || n FROM generate_series(1, 40) AS n
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
	dbtest.SeedGroupColleagues(t, ctx, pool, fixture.CompanyA, "group-colleague", 40,
		"project.tasks.write", "project.files.manage")
	// One Wiki page every seeded Run read, for the page's Agent reads list.
	wikiPage, wikiRevision := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO wiki_pages (id, company_id, slug, title, current_revision_id, created_by_user_id)
			VALUES ($1, $2, 'explain', 'Explain', $3, $4)`, wikiPage, fixture.CompanyA, wikiRevision, fixture.UserA)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO wiki_revisions (id, company_id, page_id, number, title, body, author_user_id)
			VALUES ($1, $2, $3, 1, 'Explain', 'Body', $4)`, wikiRevision, fixture.CompanyA, wikiPage, fixture.UserA)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO run_wiki_reads (company_id, run_id, agent_id, kind, page_id, revision_id, revision_number, title)
			SELECT runs.company_id, runs.id, runs.agent_id, 'read', $2, $3, 1, 'Explain'
			FROM runs WHERE runs.company_id = $1`, fixture.CompanyA, wikiPage, wikiRevision)
	})
	dbtest.MustExec(t, ctx, pool, `ANALYZE`)

	recorder := &explainRecorder{pool: pool}
	queries := db.New(recorder)
	grantScan := regexp.MustCompile(
		`(?m)Seq Scan on (project_grants|company_group_members|project_group_grants)( |$)`,
	)
	chatScan := regexp.MustCompile(`(?m)Seq Scan on conversations( |$)`)
	assertGrantsByIndex := func(name string, perRow bool, onceComputed ...string) string {
		t.Helper()
		plan, err := pool.Query(ctx, `EXPLAIN (COSTS OFF) `+recorder.sql, recorder.args...)
		if err != nil {
			t.Fatalf("%s explain: %v", name, err)
		}
		lines, err := pgx.CollectRows(plan, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(lines, "\n")
		if grantScan.MatchString(text) {
			t.Errorf("%s scans all of a grant or Group table:\n%s", name, text)
		}
		// A per-row lookup must stay one probe per row: no hashed subplan
		// building the person's whole readable set from a scan of every chat.
		// Only a statement naming onceComputed may keep a hashed subplan,
		// and only over those CTEs: the Projects a decide view decides in,
		// a small set computed once, not a scan.
		if perRow && (hashedSubplanOutside(text, onceComputed) || chatScan.MatchString(text)) {
			t.Errorf("%s builds the readable set instead of probing per row:\n%s", name, text)
		}
		return text
	}

	member := auth.UserPrincipal{UserID: memberID, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	runs := &DefaultRunService{Queries: queries}
	page, err := runs.List(ctx, member, ListOptions{Limit: 50})
	if err != nil || len(page.Items) != 50 {
		t.Fatalf("Run history = %d Runs, %v; want a full page from the readable Project", len(page.Items), err)
	}
	assertGrantsByIndex("Run history", true)

	approvals, err := queries.ListApprovalProjections(ctx, db.ListApprovalProjectionsParams{
		CompanyID: fixture.CompanyA, ActorUserID: memberID, Statuses: []string{}, PageSize: 50,
	})
	if err != nil || len(approvals) != 50 {
		t.Fatalf("Approval list = %d, %v; want a full page from the readable Project", len(approvals), err)
	}
	assertGrantsByIndex("the Approval list", true)
	questions, err := queries.ListQuestionProjections(ctx, db.ListQuestionProjectionsParams{
		CompanyID: fixture.CompanyA, ActorUserID: memberID, Statuses: []string{}, PageSize: 50,
	})
	if err != nil || len(questions) != 50 {
		t.Fatalf("Question list = %d, %v; want a full page from the readable Project", len(questions), err)
	}
	assertGrantsByIndex("the Question list", true)

	// The decide views read the caller's deciding Projects once, then keep
	// the per-row reader probe: no scan of every chat or every grant.
	sets := auth.ProjectPermissionHeldSets()
	if _, err := queries.ListApprovalProjections(ctx, db.ListApprovalProjectionsParams{
		CompanyID: fixture.CompanyA, ActorUserID: memberID, Statuses: []string{}, PageSize: 50,
		DecideView: true, DecidePermission: string(auth.ProjectPermissionApprovalsResolve),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions, ManagementPermissions: sets.ManagementPermissions,
		ManagementRoles: sets.ManagementRoles, ActorRoles: auth.RolesWith(auth.PermissionApprovalsResolve),
	}); err != nil {
		t.Fatalf("Approval decide view: %v", err)
	}
	assertGrantsByIndex("the Approval decide view", true, "readable")
	if _, err := queries.ListQuestionProjections(ctx, db.ListQuestionProjectionsParams{
		CompanyID: fixture.CompanyA, ActorUserID: memberID, Statuses: []string{}, PageSize: 50,
		DecideView: true, DecidePermission: string(auth.ProjectPermissionApprovalsResolve),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions, ManagementPermissions: sets.ManagementPermissions,
		ManagementRoles: sets.ManagementRoles,
	}); err != nil {
		t.Fatalf("Question decide view: %v", err)
	}
	assertGrantsByIndex("the Question decide view", true, "decidable")
	// The stats total the Approval decide view, so they keep its reader probe
	// per row and hash only the once-computed deciding Projects. They count
	// every row since a time, so joining each row's origin chat may scan the
	// Company's chats; that is a join, not a readable set.
	if _, err := queries.ApprovalStatsByRule(ctx, db.ApprovalStatsByRuleParams{
		CompanyID: fixture.CompanyA, ActorUserID: memberID,
		Since:                     pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		DecidePermission:          string(auth.ProjectPermissionApprovalsResolve),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions, ManagementPermissions: sets.ManagementPermissions,
		ManagementRoles: sets.ManagementRoles, ActorRoles: auth.RolesWith(auth.PermissionApprovalsResolve),
	}); err != nil {
		t.Fatalf("Approval stats: %v", err)
	}
	if text := assertGrantsByIndex("the Approval stats", false); hashedSubplanOutside(text, []string{"readable"}) {
		t.Errorf("the Approval stats build a readable set instead of probing per row:\n%s", text)
	}

	// A Wiki page's Agent reads list each Run's chat once to find the Run, so
	// joining the chats may scan them; the reader check stays one probe per
	// Run, never a hashed readable set, and only the readable Project's Runs
	// are listed.
	reads, err := queries.ListWikiPageAgentReads(ctx, db.ListWikiPageAgentReadsParams{
		CompanyID: fixture.CompanyA, ActorUserID: memberID, PageID: wikiPage, ResultLimit: 50,
	})
	if err != nil || len(reads) != 50 || reads[0].Total != 100 || !reads[0].TaskReadable {
		t.Fatalf("Wiki page Agent reads = %d rows, %v; want a full page of the readable Project's 100 Runs", len(reads), err)
	}
	if text := assertGrantsByIndex("the Wiki page's Agent reads", false); hashedSubplanOutside(text, nil) {
		t.Errorf("the Wiki page's Agent reads build a readable set instead of probing per row:\n%s", text)
	}
}

// The claim asks the delegate rule (run_permission_held, B4) once for each
// queued candidate in a Task chat. Neither delegate function is inlined, so
// the claim's plan names run_permission_held once and reads no grant itself:
// it never builds anyone's readable set from project_grants or the chats. The
// call count is read from the transaction's own function statistics, with
// every candidate refused so the claim looks at each one.
func TestClaimCallsTheDelegateRuleOncePerCandidate(t *testing.T) {
	t.Parallel()
	ctx, pool, fixture := startProjectAccessRunFixture(t)
	const candidates = 30
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			WITH seeded AS MATERIALIZED (
				SELECT gen_random_uuid() AS task_id, gen_random_uuid() AS conversation_id, n
				FROM generate_series(1, $4::int) AS n
			), conversation_rows AS (
				INSERT INTO conversations (id, company_id, kind, task_id, project_id, title)
				SELECT conversation_id, $1, 'task', task_id, $2, 'Claim ' || n FROM seeded
			), task_rows AS (
				INSERT INTO tasks (id, company_id, conversation_id, project_id, milestone_type,
				                   number, key, title, assignee_user_id, executed_by_agent_id, creator_user_id)
				SELECT task_id, $1, conversation_id, $2, 'product', 100 + n, 'claim_' || n, 'Claim ' || n, $3, $5, $3
				FROM seeded
			)
			SELECT 1`, fixture.CompanyA, fixture.ProjectA, fixture.UserA, candidates, fixture.AgentA)
	})
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id,
		                  execution_authorized_by_user_id, status, run_type, context)
		SELECT $1, $2, tasks.id, tasks.conversation_id, $3, $4, 'queued', 'task', '{}'::jsonb
		FROM tasks WHERE tasks.company_id = $1 AND tasks.key LIKE 'claim_%'`,
		fixture.CompanyA, fixture.AgentA, fixture.RuntimeA, fixture.UserA)
	// The runtime's owner may no longer execute in the Project: every
	// candidate passes the ownership arm and is refused by the Project arm.
	revokeExecute(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `ANALYZE`)

	fence := claimCandidateParams()
	claim := db.SelectClaimableRunParams{
		AgentID: fixture.AgentA, RuntimeID: fixture.RuntimeA, TaskLaneOpen: true,
		ExecutePermission: fence.ExecutePermission, ReadPermission: fence.ReadPermission,
		CompanyDefaultPermissions: fence.CompanyDefaultPermissions,
	}
	recorder := &explainRecorder{pool: pool}
	if _, err := db.New(recorder).SelectClaimableRun(ctx, claim); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("claim = %v, want no claimable Run", err)
	}
	plan, err := pool.Query(ctx, `EXPLAIN (COSTS OFF) `+recorder.sql, recorder.args...)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(plan, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	if strings.Count(text, "run_permission_held(") != 1 || strings.Contains(text, "project_permission_held") ||
		strings.Contains(text, "project_grants") || hashedSubplanOutside(text, nil) {
		t.Errorf("the claim does not ask the delegate rule as one opaque call per candidate:\n%s", text)
	}

	// The backend keeps function counts it has not reported yet across
	// transactions, so a call is counted as the difference around it.
	counts := func(tx pgx.Tx) map[string]int64 {
		t.Helper()
		rows, err := tx.Query(ctx, `
			SELECT funcname::text, calls FROM pg_stat_xact_user_functions
			WHERE funcname IN ('run_permission_held', 'delegate_permission_held')`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		counted := map[string]int64{}
		for rows.Next() {
			var function string
			var count int64
			if err := rows.Scan(&function, &count); err != nil {
				t.Fatal(err)
			}
			counted[function] = count
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return counted
	}
	calls := func(name string, run func(*db.Queries) error) map[string]int64 {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, `SET LOCAL track_functions = 'all'`); err != nil {
			t.Fatal(err)
		}
		before := counts(tx)
		if err := run(db.New(tx)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		made := counts(tx)
		for function, count := range before {
			made[function] -= count
		}
		return made
	}
	want := map[string]int64{"run_permission_held": candidates, "delegate_permission_held": candidates}
	for name, run := range map[string]func(*db.Queries) error{
		"SelectClaimableRun": func(q *db.Queries) error {
			if _, err := q.SelectClaimableRun(ctx, claim); !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("claim = %w, want no claimable Run", err)
			}
			return nil
		},
		"ListQueuedClaimCandidatesByRuntimes": func(q *db.Queries) error {
			listed, err := q.ListQueuedClaimCandidatesByRuntimes(ctx, claimCandidateParams(fixture.RuntimeA))
			if err == nil && len(listed) != 0 {
				err = fmt.Errorf("candidates = %v, want none", listed)
			}
			return err
		},
	} {
		if got := calls(name, run); !maps.Equal(got, want) {
			t.Errorf("%s calls %v, want one of each per candidate %v", name, got, want)
		}
	}
}

// hashedSubplanOutside reports whether an EXPLAIN text holds a hashed
// subplan whose body is anything but a scan of one of the named CTEs. With no
// names, any hashed subplan counts.
func hashedSubplanOutside(text string, ctes []string) bool {
	lines := strings.Split(text, "\n")
	for _, match := range regexp.MustCompile(`hashed SubPlan (\d+)`).FindAllStringSubmatch(text, -1) {
		definition := regexp.MustCompile(`^\s*SubPlan ` + match[1] + `$`)
		overCTE := false
		for index, line := range lines {
			if !definition.MatchString(line) || index+1 >= len(lines) {
				continue
			}
			for _, cte := range ctes {
				if strings.Contains(lines[index+1], "CTE Scan on "+cte+" ") ||
					strings.HasSuffix(lines[index+1], "CTE Scan on "+cte) {
					overCTE = true
				}
			}
		}
		if !overCTE {
			return true
		}
	}
	return false
}
