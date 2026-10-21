//go:build integration

package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/telemetry"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// usageObserver stands in for the metrics registry and keeps every token
// delta the service hands over.
type usageObserver struct {
	deltas []telemetry.RunUsageDeltaObservation
}

func (o *usageObserver) ObserveRunUsageDelta(value telemetry.RunUsageDeltaObservation) {
	o.deltas = append(o.deltas, value)
}

// TestReportRunUsageObservesOnlyNewlyInsertedFacts pins the run_tokens_total
// producer: a fact reaches the registry once, when its row is inserted, and a
// daemon retry of the same report — exact or conflicting — adds nothing.
func TestReportRunUsageObservesOnlyNewlyInsertedFacts(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	observer := &usageObserver{}
	service, err := NewUsageService(store, observer)
	if err != nil {
		t.Fatal(err)
	}

	runID := uuid.MustParse("00000000-0000-4000-8000-000000009003")
	first := RunUsageInput{
		Provider: "codex", Model: "gpt-5", InputTokens: 120, OutputTokens: 30,
		CacheReadTokens: 7, CacheWriteTokens: 2, RawJSON: []byte(`{"input_tokens":120}`),
	}
	if err := service.ReportRunUsage(ctx, runID, []RunUsageInput{first}); err != nil {
		t.Fatalf("insert first fact: %v", err)
	}
	// A batch that mixes an exact replay with a fresh fact counts the fresh
	// fact alone.
	second := RunUsageInput{
		Provider: "claude", Model: "claude-sonnet", InputTokens: 5, RawJSON: []byte(`{"input_tokens":5}`),
	}
	if err := service.ReportRunUsage(ctx, runID, []RunUsageInput{first, second}); err != nil {
		t.Fatalf("replay first with second: %v", err)
	}
	// A conflicting replay is refused and its transaction rolls back, so
	// nothing new is counted.
	conflicting := first
	conflicting.OutputTokens++
	err = service.ReportRunUsage(ctx, runID, []RunUsageInput{conflicting})
	if !errors.Is(err, ErrRunUsageConflict) {
		t.Fatalf("conflicting replay error = %v, want conflict", err)
	}

	want := []telemetry.RunUsageDeltaObservation{
		{Provider: "codex", InputTokens: 120, OutputTokens: 30, CacheReadTokens: 7, CacheWriteTokens: 2},
		{Provider: "claude", InputTokens: 5},
	}
	if !reflect.DeepEqual(observer.deltas, want) {
		t.Fatalf("usage observations = %+v, want %+v", observer.deltas, want)
	}
}

// TestReportedTokensReachTheMetricsEndpoint scrapes the real registry — the
// handler behind /metrics — after one report and its exact replay, pinning
// the samples the dashboard's token panel queries and that a replay adds
// nothing to them.
func TestReportedTokensReachTheMetricsEndpoint(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	registry := telemetry.NewRegistry()
	service, err := NewUsageService(store, registry)
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.MustParse("00000000-0000-4000-8000-000000009004")
	fact := RunUsageInput{
		Provider: "codex", Model: "gpt-5", InputTokens: 120, OutputTokens: 30,
		CacheReadTokens: 7, RawJSON: []byte(`{"input_tokens":120}`),
	}
	for range 2 {
		if err := service.ReportRunUsage(ctx, runID, []RunUsageInput{fact}); err != nil {
			t.Fatalf("report usage: %v", err)
		}
	}

	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, want := range []string{
		`zenith_team_run_tokens_total{kind="input",provider="codex"} 120`,
		`zenith_team_run_tokens_total{kind="output",provider="codex"} 30`,
		`zenith_team_run_tokens_total{kind="cache_read",provider="codex"} 7`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestReportRunUsageKeepsReportedZeroCostDistinctFromUnreportedCost(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewUsageService(store, nil)
	if err != nil {
		t.Fatal(err)
	}

	zeroCost := int64(0)
	reportedZero := RunUsageInput{
		Provider: "codex", Model: "free-model", InputTokens: 1,
		CostUSDTicks: &zeroCost, RawJSON: []byte(`{"input_tokens":1}`),
	}
	// run_usage.run_id is a bare bigint with no foreign key, so a fact
	// outlives the Run it describes and these ids need no Run row.
	reportedZeroRunID := uuid.MustParse("00000000-0000-4000-8000-000000009001")
	if err := service.ReportRunUsage(ctx, reportedZeroRunID, []RunUsageInput{reportedZero}); err != nil {
		t.Fatalf("insert reported zero: %v", err)
	}
	if err := service.ReportRunUsage(ctx, reportedZeroRunID, []RunUsageInput{reportedZero}); err != nil {
		t.Fatalf("replay reported zero: %v", err)
	}
	var costPresent, costIsZero bool
	if err := pool.QueryRow(
		ctx, `
		SELECT provider_cost_usd IS NOT NULL, provider_cost_usd = 0
		FROM run_usage
		WHERE run_id = $1 AND provider = $2 AND model = $3`,
		reportedZeroRunID, reportedZero.Provider, reportedZero.Model,
	).Scan(&costPresent, &costIsZero); err != nil {
		t.Fatalf("read reported zero: %v", err)
	}
	if !costPresent || !costIsZero {
		t.Fatalf("reported zero persisted as present=%t zero=%t", costPresent, costIsZero)
	}

	unreportedReplay := reportedZero
	unreportedReplay.CostUSDTicks = nil
	if err := service.ReportRunUsage(
		ctx,
		reportedZeroRunID,
		[]RunUsageInput{unreportedReplay},
	); !errors.Is(err, ErrRunUsageConflict) {
		t.Fatalf("unreported replay over reported zero error = %v, want conflict", err)
	}

	unreported := RunUsageInput{
		Provider: "claude", Model: "unpriced-model", OutputTokens: 2,
		RawJSON: []byte(`{"output_tokens":2}`),
	}
	unreportedRunID := uuid.MustParse("00000000-0000-4000-8000-000000009002")
	if err := service.ReportRunUsage(ctx, unreportedRunID, []RunUsageInput{unreported}); err != nil {
		t.Fatalf("insert unreported cost: %v", err)
	}
	if err := service.ReportRunUsage(ctx, unreportedRunID, []RunUsageInput{unreported}); err != nil {
		t.Fatalf("replay unreported cost: %v", err)
	}
	if err := pool.QueryRow(
		ctx, `
		SELECT provider_cost_usd IS NULL
		FROM run_usage
		WHERE run_id = $1 AND provider = $2 AND model = $3`,
		unreportedRunID, unreported.Provider, unreported.Model,
	).Scan(&costPresent); err != nil {
		t.Fatalf("read unreported cost: %v", err)
	}
	if !costPresent {
		t.Fatal("unreported cost was not persisted as NULL")
	}

	reportedZeroReplay := unreported
	reportedZeroReplay.CostUSDTicks = &zeroCost
	if err := service.ReportRunUsage(
		ctx,
		unreportedRunID,
		[]RunUsageInput{reportedZeroReplay},
	); !errors.Is(err, ErrRunUsageConflict) {
		t.Fatalf("reported-zero replay over NULL error = %v, want conflict", err)
	}
}
