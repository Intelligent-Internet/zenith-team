//go:build integration

package usage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// ADR 0017 on Analytics: usage.read keeps Company-wide totals, but a
// Project the caller does not read is never named. An admin without a grant
// sees the restricted Project's tokens in every total and in one unnamed
// "Other work" row, never its id, key or title, and cannot filter by it or
// open its Task's usage. The owner, who reads it, sees it by name. A member
// sees only the Runs they authorized, and their own Run in a Project they do
// not read is theirs to count but not to name.
func TestAnalyticsNamesOnlyProjectsTheCallerReads(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewUsageService(store, nil)
	if err != nil {
		t.Fatal(err)
	}

	// ProjectA is restricted; only the owner holds grants on it.
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET title = 'Secret Launch' WHERE id = $1`, f.ProjectA)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET title = 'Secret Plan', key = 'secret_key' WHERE id = $1`,
		f.TaskA,
	)
	admin, member := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name) VALUES
			($1, 'analytics-admin@example.com', 'Analytics Admin'),
			($2, 'analytics-member@example.com', 'Analytics Member')`, admin, member)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'admin'), ($1, $3, 'member')`,
		f.CompanyA, admin, member)

	now := time.Now().UTC()
	// Yesterday, so the whole window has elapsed and the summary counts all of it.
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	finished := start.Add(time.Minute)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET finished_at = NULL WHERE company_id = $1`, f.CompanyA)
	// The owner's Task Run in the restricted Project: 100 input tokens.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET execution_authorized_by_user_id = $2, started_at = $3, finished_at = $3
		WHERE id = $1`, f.RunA, f.UserA, finished)
	insertRun := func(authorizer uuid.UUID, taskID *uuid.UUID, conversationID uuid.UUID, runType string, input int) {
		t.Helper()
		var runID uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id, status, run_type,
			                  execution_authorized_by_user_id, started_at, finished_at)
			VALUES ($1, $2, $3, $4, $5, 'completed', $6, $7, $8, $8) RETURNING id`,
			f.CompanyA, f.AgentA, taskID, conversationID, f.RuntimeA, runType, authorizer, finished,
		).Scan(&runID); err != nil {
			t.Fatalf("insert run: %v", err)
		}
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO run_usage (run_id, provider, model, input_tokens, output_tokens, raw_json)
			VALUES ($1, 'codex', 'gpt-5.6-luna', $2, 1, '{}')`, runID, input)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO run_usage (run_id, provider, model, input_tokens, output_tokens, raw_json)
		VALUES ($1, 'codex', 'gpt-5.6-luna', 100, 1, '{}')`, f.RunA)
	insertRun(admin, nil, f.DirectConversationA, "direct_chat", 10)
	// The member's own Run on the restricted Project's Task, then a chat Run.
	insertRun(member, &f.TaskA, f.ConversationA, "task", 7)
	insertRun(member, nil, f.DirectConversationA, "direct_chat", 3)

	query := AnalyticsQuery{
		Scope:    AnalyticsScopeCompany,
		StartAt:  start,
		EndAt:    start.AddDate(0, 0, 1),
		TimeZone: "UTC",
	}
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	adminPrincipal := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: admin, Role: auth.CompanyRoleAdmin}
	memberPrincipal := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: member, Role: auth.CompanyRoleMember}
	hidden := []string{f.ProjectA.String(), f.TaskA.String(), "Secret Launch", "Secret Plan", "secret_key"}
	assertNamesNothing := func(name string, value any) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range hidden {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("%s names hidden work %q: %s", name, secret, encoded)
			}
		}
	}
	breakdown := func(p auth.UserPrincipal, q AnalyticsQuery, by string) AnalyticsBreakdown {
		t.Helper()
		q.By, q.Sort, q.Limit = by, "tokens", 100
		report, err := service.GetAnalyticsBreakdown(ctx, p, q)
		if err != nil {
			t.Fatalf("breakdown by %s: %v", by, err)
		}
		sum := "0"
		for _, row := range report.Rows {
			sum = addDecimalStrings(sum, row.Tokens.Input)
		}
		if sum != report.Total.Tokens.Input {
			t.Fatalf("breakdown by %s rows add to %s, total is %s", by, sum, report.Total.Tokens.Input)
		}
		return report
	}
	rowFor := func(report AnalyticsBreakdown, synthetic string) *AnalyticsBreakdownRow {
		for i := range report.Rows {
			if report.Rows[i].Key.Synthetic != nil && *report.Rows[i].Key.Synthetic == synthetic {
				return &report.Rows[i]
			}
		}
		return nil
	}

	// The admin: every total, no name.
	summary, err := service.GetAnalyticsSummary(ctx, adminPrincipal, query)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Current.Tokens.Input != "120" || summary.Current.Runs.Total != "4" {
		t.Fatalf("admin totals = %+v, want all 4 Runs and 120 input tokens", summary.Current)
	}
	for _, by := range analyticsDimensions {
		report := breakdown(adminPrincipal, query, by)
		assertNamesNothing("admin breakdown by "+by, report)
		if by != "project" && by != "task" {
			continue
		}
		other := rowFor(report, "other_work")
		if other == nil || other.Key.ID != nil || other.Key.Label != "Other work" ||
			other.Tokens.Input != "107" {
			t.Fatalf(
				"admin breakdown by %s = %+v, want one unnamed Other work row of 107 tokens",
				by,
				report.Rows,
			)
		}
	}
	series, err := service.GetAnalyticsSeries(ctx, adminPrincipal, AnalyticsQuery{
		Scope: query.Scope, StartAt: query.StartAt, EndAt: query.EndAt, TimeZone: query.TimeZone,
		Metric: "tokens", Bucket: BucketDay,
	})
	if err != nil || len(series.Points) != 1 || *series.Points[0].Parts["input"] != "120" {
		t.Fatalf("admin series = %+v, %v; want one day of 120 input tokens", series, err)
	}
	heatmap, err := service.GetAnalyticsHeatmap(ctx, adminPrincipal, query)
	if err != nil {
		t.Fatal(err)
	}
	failures, err := service.GetAnalyticsFailures(ctx, adminPrincipal, query)
	if err != nil || failures.Succeeded != "4" {
		t.Fatalf("admin failures = %+v, %v", failures, err)
	}
	assertNamesNothing("admin series", series)
	assertNamesNothing("admin heatmap", heatmap)
	assertNamesNothing("admin failures", failures)
	filtered := query
	filtered.ProjectID = &f.ProjectA
	if _, err := service.GetAnalyticsSummary(
		ctx,
		adminPrincipal,
		filtered,
	); !errors.Is(
		err,
		auth.ErrProjectNotVisible,
	) {
		t.Fatalf("admin filter by the hidden Project error = %v, want project not visible", err)
	}
	filtered = query
	filtered.TaskID = &f.TaskA
	if _, err := service.GetAnalyticsSummary(
		ctx,
		adminPrincipal,
		filtered,
	); !errors.Is(
		err,
		auth.ErrTaskNotVisible,
	) {
		t.Fatalf("admin filter by the hidden Task error = %v, want task not visible", err)
	}
	if _, err := service.GetTaskUsage(ctx, adminPrincipal, f.TaskA); !errors.Is(err, ErrTaskUsageNotFound) {
		t.Fatalf("admin Task usage of the hidden Task error = %v, want not found", err)
	}

	// The owner reads the Project, so it carries its name and opens.
	projects := breakdown(owner, query, "project")
	if rowFor(projects, "other_work") != nil {
		t.Fatalf("owner project breakdown has an Other work row: %+v", projects.Rows)
	}
	named := false
	for _, row := range projects.Rows {
		if row.Key.ID != nil && *row.Key.ID == f.ProjectA && row.Key.Label == "Secret Launch" &&
			row.Tokens.Input == "107" {
			named = true
		}
	}
	if !named {
		t.Fatalf("owner project breakdown = %+v, want the Project by name", projects.Rows)
	}
	tasks := breakdown(owner, query, "task")
	named = false
	for _, row := range tasks.Rows {
		if row.Key.ID != nil && *row.Key.ID == f.TaskA && row.Key.Label == "secret_key · Secret Plan" {
			named = true
		}
	}
	if !named {
		t.Fatalf("owner task breakdown = %+v, want the Task by key and title", tasks.Rows)
	}
	filtered = query
	filtered.ProjectID = &f.ProjectA
	ownerProject, err := service.GetAnalyticsSummary(ctx, owner, filtered)
	if err != nil || ownerProject.Current.Tokens.Input != "107" {
		t.Fatalf("owner Project filter = %+v, %v; want 107 input tokens", ownerProject.Current, err)
	}
	if usage, err := service.GetTaskUsage(ctx, owner, f.TaskA); err != nil || usage.Runs != "2" {
		t.Fatalf("owner Task usage = %+v, %v; want both Task Runs", usage, err)
	}

	// The member: only their own Runs, and their hidden-Project Run unnamed.
	mine := query
	mine.Scope = AnalyticsScopeMine
	memberSummary, err := service.GetAnalyticsSummary(ctx, memberPrincipal, mine)
	if err != nil || memberSummary.Current.Tokens.Input != "10" || memberSummary.Current.Runs.Total != "2" {
		t.Fatalf(
			"member Mine totals = %+v, %v; want their 2 Runs and 10 input tokens",
			memberSummary.Current,
			err,
		)
	}
	for _, by := range []string{"agent", "model", "project", "task", "run_type", "runtime"} {
		report := breakdown(memberPrincipal, mine, by)
		assertNamesNothing("member breakdown by "+by, report)
		if by == "project" {
			if other := rowFor(report, "other_work"); other == nil || other.Tokens.Input != "7" {
				t.Fatalf(
					"member project breakdown = %+v, want their own hidden Run as Other work",
					report.Rows,
				)
			}
		}
	}
	if _, err := service.GetAnalyticsSummary(ctx, memberPrincipal, query); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member Company scope error = %v, want forbidden", err)
	}
	if _, err := service.GetTaskUsage(ctx, memberPrincipal, f.TaskA); !errors.Is(err, ErrTaskUsageNotFound) {
		t.Fatalf("member Task usage before a grant error = %v, want not found", err)
	}

	// A grant names the work for the member, still within Mine.
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, member)
	report := breakdown(memberPrincipal, mine, "project")
	if rowFor(report, "other_work") != nil || len(report.Rows) != 2 {
		t.Fatalf("granted member project breakdown = %+v, want the Project and No Project", report.Rows)
	}
	usage, err := service.GetTaskUsage(ctx, memberPrincipal, f.TaskA)
	if err != nil || usage.Runs != "2" || !usage.UsageRestricted {
		t.Fatalf("granted member Task usage = %+v, %v", usage, err)
	}
	for _, run := range usage.Items {
		if run.UsageRestricted != (run.ID == f.RunA) {
			t.Fatalf(
				"member sees usage of Run %s restricted=%t; want only their own Run open",
				run.ID,
				run.UsageRestricted,
			)
		}
	}

	// "Active now": one live Run on the restricted Project's Task and one in
	// a direct chat. The Company count holds both; the owner's Project
	// filter reaches the Task through task_access and counts one.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id, status, run_type,
		                  execution_authorized_by_user_id)
		VALUES ($1, $2, $3, $4, $5, 'running', 'task', $6),
		       ($1, $2, NULL, $7, $5, 'running', 'direct_chat', $6)`,
		f.CompanyA, f.AgentA, f.TaskA, f.ConversationA, f.RuntimeA, f.UserA, f.DirectConversationA)
	for _, tt := range []struct {
		name      string
		p         auth.UserPrincipal
		projectID *uuid.UUID
		want      string
	}{
		{"owner, every Project", owner, nil, "2"},
		{"owner, the Project", owner, &f.ProjectA, "1"},
		{"admin, every Project", adminPrincipal, nil, "2"},
	} {
		q := query
		q.ProjectID = tt.projectID
		active, err := service.GetAnalyticsSummary(ctx, tt.p, q)
		if err != nil || active.ActiveNow != tt.want {
			t.Fatalf("%s active now = %q, %v; want %s", tt.name, active.ActiveNow, err, tt.want)
		}
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET status = 'completed' WHERE company_id = $1 AND finished_at IS NULL`,
		f.CompanyA,
	)

	// A deleted Task's Runs stay in its Project, as in Run history: the
	// Task chat keeps project_id. Readers still see the Project by name;
	// everyone else still sees Other work.
	dbtest.MustExec(t, ctx, pool, `DELETE FROM tasks WHERE id = $1`, f.TaskA)
	projects = breakdown(owner, query, "project")
	named = false
	for _, row := range projects.Rows {
		if row.Key.ID != nil && *row.Key.ID == f.ProjectA && row.Key.Label == "Secret Launch" &&
			row.Tokens.Input == "107" {
			named = true
		}
	}
	if noProject := rowFor(projects, "no_project"); !named || noProject == nil || noProject.Tokens.Input != "13" {
		t.Fatalf(
			"owner project breakdown after the Task is deleted = %+v, want the Project by name",
			projects.Rows,
		)
	}
	filtered = query
	filtered.ProjectID = &f.ProjectA
	if ownerProject, err := service.GetAnalyticsSummary(
		ctx,
		owner,
		filtered,
	); err != nil ||
		ownerProject.Current.Tokens.Input != "107" {
		t.Fatalf(
			"owner Project filter after the Task is deleted = %+v, %v; want 107 input tokens",
			ownerProject.Current,
			err,
		)
	}
	tasks = breakdown(owner, query, "task")
	if deleted := rowFor(tasks, "deleted_tasks"); deleted == nil || deleted.Tokens.Input != "107" {
		t.Fatalf("owner task breakdown after the Task is deleted = %+v, want one Deleted Tasks row", tasks.Rows)
	}
	for _, by := range []string{"project", "task"} {
		report := breakdown(adminPrincipal, query, by)
		assertNamesNothing("admin breakdown after the Task is deleted by "+by, report)
		if other := rowFor(report, "other_work"); other == nil || other.Tokens.Input != "107" {
			t.Fatalf(
				"admin breakdown by %s after the Task is deleted = %+v, want Other work of 107 tokens",
				by,
				report.Rows,
			)
		}
	}
}
