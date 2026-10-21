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
	"github.com/Intelligent-Internet/zenith-team/server/migrations"
)

func TestAnalyticsSummaryCountsActiveRunsWithoutUsage(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
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
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	query := AnalyticsQuery{
		Scope:    AnalyticsScopeCompany,
		StartAt:  start,
		EndAt:    start.AddDate(0, 0, 1),
		TimeZone: "UTC",
	}
	for _, status := range []string{"queued", "deferred", "dispatched", "waiting_local_directory", "running"} {
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type,execution_authorized_by_user_id,context)
		 VALUES($1,$2,$3,$4,$5,'direct_chat',$6,'{"agent":{"model":"frozen-model"}}')`,
			f.CompanyA,
			f.AgentA,
			f.DirectConversationA,
			f.RuntimeA,
			status,
			f.UserA,
		)
	}
	// A provider default is resolved locally and cannot match an exact model filter.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type,execution_authorized_by_user_id)
	 VALUES($1,$2,$3,$4,'running','direct_chat',$5)`,
		f.CompanyA,
		f.AgentA,
		f.DirectConversationA,
		f.RuntimeA,
		f.UserA,
	)
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET model='changed-model' WHERE id=$1`, f.AgentA)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET status='running',context='{"agent":{"model":"frozen-model"}}' WHERE id=$1`,
		f.RunB,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	var otherRuntime uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO runtimes(company_id,daemon_id,name,provider,owner_user_id)
	 VALUES($1,$2,'Other computer','codex',$3) RETURNING id`, f.CompanyA, f.DaemonB, f.UserB).Scan(&otherRuntime); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type,execution_authorized_by_user_id,context)
	 VALUES($1,$2,$3,$4,'running','direct_chat',$5,'{"agent":{"model":"frozen-model"}}')`,
		f.CompanyA,
		f.AgentA,
		f.DirectConversationA,
		otherRuntime,
		f.UserB,
	)
	// Finished analytics must still use reported model facts, not the frozen request.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET finished_at=$2,context='{"agent":{"model":"frozen-model"}}' WHERE id=$1`,
		f.RunA,
		start.Add(time.Hour),
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO run_usage(run_id,provider,model,input_tokens,raw_json) VALUES($1,'codex','reported-model',100,'{}')`,
		f.RunA,
	)

	cases := []struct {
		name     string
		provider string
		model    string
		scope    AnalyticsScope
		member   *uuid.UUID
		runtime  *uuid.UUID
		want     string
	}{
		{name: "all active statuses outside the period", want: "7"},
		{name: "provider without usage", provider: "codex", want: "7"},
		{name: "other provider and company excluded", provider: "claude", want: "0"},
		{name: "frozen model without usage", model: "frozen-model", want: "6"},
		{name: "provider and frozen model", provider: "codex", model: "frozen-model", want: "6"},
		{name: "provider mismatch", provider: "claude", model: "frozen-model", want: "0"},
		{name: "later agent model excluded", model: "changed-model", want: "0"},
		{name: "terminal usage excluded", model: "reported-model", want: "0"},
		{name: "mine", provider: "codex", scope: AnalyticsScopeMine, want: "6"},
		{name: "member", provider: "codex", member: &f.UserB, want: "1"},
		{name: "own runtime", provider: "codex", runtime: &f.RuntimeA, want: "6"},
		{name: "private runtime excluded", provider: "codex", runtime: &otherRuntime, want: "0"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			q := query
			q.Provider, q.Model, q.MemberID, q.RuntimeID = tt.provider, tt.model, tt.member, tt.runtime
			if tt.scope != "" {
				q.Scope = tt.scope
			}
			summary, err := service.GetAnalyticsSummary(ctx, p, q)
			if err != nil {
				t.Fatal(err)
			}
			if summary.ActiveNow != tt.want {
				t.Errorf("activeNow = %s, want %s", summary.ActiveNow, tt.want)
			}
			if tt.model == "frozen-model" && summary.Current.Runs.Total != "0" {
				t.Errorf("finished runs for frozen model = %s, want 0", summary.Current.Runs.Total)
			}
			if tt.model == "reported-model" && summary.Current.Runs.Total != "1" {
				t.Errorf("finished runs for reported model = %s, want 1", summary.Current.Runs.Total)
			}
		})
	}
}

func TestAnalyticsReconcilesScopesPricesAndPrivateWork(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
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
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	q := AnalyticsQuery{
		Scope:    AnalyticsScopeCompany,
		StartAt:  start,
		EndAt:    start.AddDate(0, 1, 0),
		TimeZone: "UTC",
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET finished_at=$2 WHERE company_id=$1`,
		f.CompanyA,
		start.AddDate(0, 0, -1),
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO model_prices(provider,model,effective_from,input_usd_per_mtok,output_usd_per_mtok,cache_read_usd_per_mtok,cache_write_usd_per_mtok,source) VALUES ('codex','test-priced',$1,1,2,0.1,0,'fixture')`,
		start,
	)
	for i, billing := range []string{"api", "api", "subscription", "api", "unknown"} {
		var runID uuid.UUID
		var authorizer any = f.UserA
		if i == 4 {
			authorizer = nil
		}
		reason := ""
		status := "completed"
		if i == 4 {
			status = "failed"
			reason = "runtime_offline"
		}
		err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type,execution_authorized_by_user_id,queued_at,claimed_at,started_at,finished_at,failure_reason) VALUES($1,$2,$3,$4,$5,'direct_chat',$6,$7,$7::timestamptz+interval '2 minutes',$7::timestamptz+interval '3 minutes',$7::timestamptz+interval '10 minutes',NULLIF($8,'')) RETURNING id`, f.CompanyA, f.AgentA, f.DirectConversationA, f.RuntimeA, status, authorizer, start.Add(now.Sub(start)/2).Add(-10*time.Minute), reason).
			Scan(&runID)
		if err != nil {
			t.Fatal(err)
		}
		model := "test-priced"
		if i == 0 {
			model = "test-reported"
		}
		if i == 3 {
			model = "test-unpriced"
		}
		var reported any
		if i == 0 || i == 2 {
			reported = "0.42"
		}
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO run_usage(run_id,provider,model,input_tokens,output_tokens,cache_read_tokens,provider_cost_usd,billing_mode,raw_json) VALUES($1,'codex',$2,100,50,20,$3,$4,'{}')`,
			runID,
			model,
			reported,
			billing,
		)
	}
	summary, err := service.GetAnalyticsSummary(ctx, p, q)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Current.Runs.Total != "5" || summary.Current.Tokens.Input != "500" ||
		summary.Current.Cost.PlanCoveredTokens != "150" ||
		summary.Current.Cost.ReportedUsdTicks == nil ||
		*summary.Current.Cost.ReportedUsdTicks != "4200000000" ||
		summary.Current.Cost.EstimatedUsdTicks == nil ||
		*summary.Current.Cost.EstimatedUsdTicks != "2020000" {
		t.Fatalf("summary = %+v", summary.Current)
	}
	for _, by := range []string{"agent", "model", "member", "project", "task", "run_type"} {
		q.By = by
		q.Sort = "tokens"
		q.Limit = 100
		report, err := service.GetAnalyticsBreakdown(ctx, p, q)
		if err != nil {
			t.Fatalf("%s: %v", by, err)
		}
		sum := "0"
		for _, row := range report.Rows {
			sum = addDecimalStrings(sum, row.Tokens.Input)
			if by == "model" && row.Key.Label == "codex / test-priced" &&
				len(row.Cost.UnpricedModels) != 0 {
				t.Fatalf(
					"priced model includes other groups' missing prices: %+v",
					row.Cost.UnpricedModels,
				)
			}
		}
		if sum != report.Total.Tokens.Input {
			t.Fatalf("%s rows=%s total=%s", by, sum, report.Total.Tokens.Input)
		}
		encoded, _ := json.Marshal(report)
		if strings.Contains(string(encoded), "Owner chat") {
			t.Fatalf("private title leaked: %s", encoded)
		}
	}
	q.By = "runtime"
	q.Sort = "runs"
	q.Limit = 100
	runtimes, err := service.GetAnalyticsBreakdown(ctx, p, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimes.Rows) != 1 || runtimes.Rows[0].Key.Label == "Other computers" {
		t.Fatalf("runtime breakdown = %+v", runtimes.Rows)
	}
	heatmap, err := service.GetAnalyticsHeatmap(ctx, p, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(heatmap.Cells) != 168 {
		t.Fatalf("heatmap cells = %d, want 168", len(heatmap.Cells))
	}
	var heatmapTokens string
	for _, cell := range heatmap.Cells {
		heatmapTokens = addDecimalStrings(heatmapTokens, cell.Tokens)
	}
	if heatmapTokens != "750" {
		t.Fatalf("heatmap tokens = %s, want 750 (input + output)", heatmapTokens)
	}
	q.Model = "test-reported"
	reportedHeatmap, err := service.GetAnalyticsHeatmap(ctx, p, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, cell := range reportedHeatmap.Cells {
		if cell.Runs != "0" && cell.CostUsdTicks != "4200000000" {
			t.Fatalf("reported-only heatmap cost = %s, want 4200000000", cell.CostUsdTicks)
		}
	}
	q.Model = ""
	// Even administrators cannot name or isolate another member's computer.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'admin')`,
		f.CompanyA,
		f.UserB,
	)
	other := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleAdmin}
	private, err := service.GetAnalyticsBreakdown(ctx, other, q)
	if err != nil || len(private.Rows) != 1 || private.Rows[0].Key.ID != nil ||
		private.Rows[0].Key.Label != "Other computers" {
		t.Fatalf("private runtime projection = %+v, err = %v", private, err)
	}
	q.RuntimeID = &f.RuntimeA
	private, err = service.GetAnalyticsBreakdown(ctx, other, q)
	if err != nil || len(private.Rows) != 0 {
		t.Fatalf("private runtime filter = %+v, err = %v", private, err)
	}
	q.RuntimeID = nil
	q.By = ""
	q.Scope = AnalyticsScopeMine
	mine, err := service.GetAnalyticsSummary(ctx, p, q)
	if err != nil {
		t.Fatal(err)
	}
	if mine.Current.Runs.Total != "4" {
		t.Fatalf("mine runs=%s", mine.Current.Runs.Total)
	}
	q.Scope = AnalyticsScopeCompany
	failures, err := service.GetAnalyticsFailures(ctx, p, q)
	if err != nil {
		t.Fatal(err)
	}
	if failures.ByClass["computer_runtime"] != "1" || failures.Succeeded != "4" {
		t.Fatalf("failures=%+v", failures)
	}
	member := p
	member.Role = auth.CompanyRoleMember
	if _, err := service.GetAnalyticsSummary(ctx, member, q); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member company scope error=%v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE model_prices SET input_usd_per_mtok=2 WHERE model='test-priced'`)
	changed, err := service.GetAnalyticsSummary(ctx, p, q)
	if err != nil {
		t.Fatal(err)
	}
	if *changed.Current.Cost.EstimatedUsdTicks != "3020000" {
		t.Fatalf("repriced=%v", changed.Current.Cost.EstimatedUsdTicks)
	}
	budget, err := service.UpsertAnalyticsBudget(ctx, p, AnalyticsBudgetInput{
		OwnerKind: "company", MonthlyLimitUsdTicks: "4000000000", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewAnalyticsWorker(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var alerts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM analytics_budget_alerts WHERE budget_id=$1`, budget.ID).
		Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if alerts != 2 {
		t.Fatalf("budget alerts = %d, want 2", alerts)
	}
	if err := worker.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM analytics_budget_alerts WHERE budget_id=$1`, budget.ID).
		Scan(&alerts); err != nil ||
		alerts != 2 {
		t.Fatalf("replayed budget alerts = %d, err = %v, want 2", alerts, err)
	}
	if _, err := service.UpsertAnalyticsBudget(
		ctx,
		p,
		AnalyticsBudgetInput{
			OwnerKind:            "agent",
			AgentID:              &f.AgentB,
			MonthlyLimitUsdTicks: "100",
			Enabled:              true,
		},
	); !errors.Is(
		err,
		ErrInvalidInput,
	) {
		t.Fatalf("cross-company budget error = %v, want invalid input", err)
	}
}

func TestAnalyticsMigrationPreservesHistoricalEvidence(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	down, err := migrations.Files.ReadFile("000067_analytics_foundation.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, string(down))
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO run_usage(run_id,provider,model,input_tokens,output_tokens,cache_read_tokens,provider_cost_usd,raw_json) VALUES($1,'codex','historical',100,50,30,0.25,'{}')`,
		f.RunA,
	)
	var beforeRuns, beforeUsage string
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(r) ORDER BY id)::text FROM runs r`).
		Scan(&beforeRuns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(u) ORDER BY run_id,provider,model)::text FROM run_usage u`).
		Scan(&beforeUsage); err != nil {
		t.Fatal(err)
	}
	up, err := migrations.Files.ReadFile("000067_analytics_foundation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, string(up))
	var afterRuns, afterUsage string
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(r)-ARRAY['queued_at','claimed_at','started_at','finished_at'] ORDER BY id)::text FROM runs r`).
		Scan(&afterRuns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(u)-ARRAY['reasoning_tokens','turns','provider_duration_ms','billing_mode'] ORDER BY run_id,provider,model)::text FROM run_usage u`).
		Scan(&afterUsage); err != nil {
		t.Fatal(err)
	}
	if beforeRuns != afterRuns || beforeUsage != afterUsage {
		t.Fatal("Analytics migration changed existing Run or usage evidence")
	}
	var truthful bool
	if err := pool.QueryRow(ctx, `SELECT bool_and(queued_at IS NULL AND claimed_at IS NULL AND started_at IS NULL AND finished_at IS NULL) FROM runs WHERE status IN ('completed','failed','cancelled')`).
		Scan(&truthful); err != nil ||
		!truthful {
		t.Fatalf("historical timing preservation = %t, err = %v", truthful, err)
	}
	// Enough out-of-window rows make the optimizer choose a range scan without
	// disabling its normal cost decisions. This checks plan shape, not latency.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type,finished_at)
		SELECT $1,$2,$3,$4,'completed','direct_chat',now()-n*interval '1 day' FROM generate_series(1,2000) n`,
		f.CompanyA,
		f.AgentA,
		f.DirectConversationA,
		f.RuntimeA,
	)
	dbtest.MustExec(t, ctx, pool, `ANALYZE runs`)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	rows, err := conn.Query(
		ctx,
		`EXPLAIN SELECT id FROM runs WHERE company_id=$1 AND finished_at >= now()-interval '7 days' AND finished_at<now()`,
		f.CompanyA,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "Index Cond:") || !strings.Contains(plan.String(), "finished_at >=") {
		t.Fatalf("window scan does not use a finish-time range index: %s", plan.String())
	}
}
