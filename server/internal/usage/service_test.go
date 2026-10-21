package usage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestEmptySeriesUsesLocalCalendarAcrossDSTAndMonthBuckets(t *testing.T) {
	t.Parallel()
	day := Period{
		StartAt:  time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC),
		EndAt:    time.Date(2026, 3, 10, 4, 0, 0, 0, time.UTC),
		TimeZone: "America/New_York", Bucket: BucketDay,
	}
	points, order, err := emptySeries(day)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || points[order[0]].point.EndAt.Sub(points[order[0]].point.StartAt) != 23*time.Hour ||
		points[order[1]].point.EndAt.Sub(points[order[1]].point.StartAt) != 24*time.Hour {
		t.Fatalf("DST day buckets = %+v", points)
	}

	month := Period{
		StartAt:  time.Date(2025, 12, 31, 17, 0, 0, 0, time.UTC),
		EndAt:    time.Date(2026, 12, 31, 17, 0, 0, 0, time.UTC),
		TimeZone: "Asia/Ho_Chi_Minh", Bucket: BucketMonth,
	}
	_, order, err = emptySeries(month)
	if err != nil || len(order) != 12 {
		t.Fatalf("month buckets = %d, %v", len(order), err)
	}
}

func TestCostAttributionKeepsReportedZeroDistinctFromMissing(t *testing.T) {
	t.Parallel()
	attribution, err := costAttributionFromStrings("1", "0", "0")
	if err != nil || attribution.CostCoverageState != CostCoverageComplete ||
		attribution.ProviderCostUSDTicks == nil || *attribution.ProviderCostUSDTicks != "0" {
		t.Fatalf("reported zero = %+v / %v", attribution, err)
	}
	attribution, err = costAttributionFromStrings("0", "3", "0")
	if err != nil || attribution.CostCoverageState != CostCoverageMissing ||
		attribution.ProviderCostUSDTicks != nil {
		t.Fatalf("missing = %+v / %v", attribution, err)
	}
	attribution, err = costAttributionFromStrings("2", "1", "12500000000")
	if err != nil || attribution.CostCoverageState != CostCoveragePartial ||
		attribution.ProviderCostUSDTicks == nil || *attribution.ProviderCostUSDTicks != "12500000000" {
		t.Fatalf("partial = %+v / %v", attribution, err)
	}
}

func TestNormalizeRunUsageInputSanitizesRawProviderFragment(t *testing.T) {
	t.Parallel()
	got, err := normalizeRunUsageInput(RunUsageInput{
		Provider: "codex", Model: " gpt-5 ", RawJSON: []byte(
			`{"input_tokens":4,"output_tokens":"secret","reasoning_output_tokens":2,"transcript":"secret"}`,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "codex" || got.Model != "gpt-5" ||
		string(got.RawJSON) != `{"input_tokens":4,"reasoning_output_tokens":2}` {
		t.Fatalf("normalized input = %+v", got)
	}
	if got.UsageBasis != UsageBasisPerRun {
		t.Fatalf("default usage basis = %q, want %q", got.UsageBasis, UsageBasisPerRun)
	}
}

func TestNormalizeRunUsageInputRejectsInvalidFact(t *testing.T) {
	t.Parallel()
	negativeCost := int64(-1)
	jsonbOversize := []byte(`{"cost_usd":` + strings.Repeat(
		"1",
		16*1024-len(`{"cost_usd":`)-len(`}`),
	) + `}`)
	for _, input := range []RunUsageInput{
		{Provider: "codex", Model: "gpt-5", InputTokens: -1, RawJSON: []byte(`{}`)},
		{Provider: "codex", Model: "gpt-5", RawJSON: []byte(`[]`)},
		{Provider: "", Model: "gpt-5", RawJSON: []byte(`{}`)},
		// Exact, case-sensitive provider match — never normalized.
		{Provider: "CODEX", Model: "gpt-5", RawJSON: []byte(`{}`)},
		{Provider: " codex ", Model: "gpt-5", RawJSON: []byte(`{}`)},
		{Provider: "codex", Model: "gpt-5", UsageBasis: "per-run", RawJSON: []byte(`{}`)},
		{Provider: "codex", Model: strings.Repeat("m", 256), RawJSON: []byte(`{}`)},
		{Provider: "codex", Model: "gpt-5", CostUSDTicks: &negativeCost, RawJSON: []byte(`{}`)},
		{Provider: "codex", Model: "gpt-5", RawJSON: jsonbOversize},
	} {
		if _, err := normalizeRunUsageInput(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("normalizeRunUsageInput(raw bytes=%d) error = %v", len(input.RawJSON), err)
		}
	}
}

func TestInsertImmutableRunUsageAcceptsExactReplayAndRejectsDifferentReplay(t *testing.T) {
	t.Parallel()
	runID := uuid.MustParse("00000000-0000-4000-8000-000000004711")
	fact := RunUsageInput{Provider: "codex", Model: "gpt-5", RawJSON: []byte(`{}`)}
	for _, test := range []struct {
		name      string
		unchanged bool
		wantErr   error
	}{
		{name: "exact replay", unchanged: true},
		{name: "different replay", wantErr: ErrRunUsageConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dbtx := &replayDBTX{unchanged: test.unchanged}
			inserted, err := insertImmutableRunUsage(
				context.Background(), dbtx, generated.New(dbtx), runID, fact,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("insertImmutableRunUsage() error = %v, want %v", err, test.wantErr)
			}
			// A replay, exact or not, inserted nothing: the metrics must not
			// count the fact a second time.
			if inserted {
				t.Fatal("insertImmutableRunUsage() reported a replay as inserted")
			}
			if dbtx.queryRowCalls != 2 {
				t.Fatalf("QueryRow() calls = %d, want 2", dbtx.queryRowCalls)
			}
		})
	}
}

type replayDBTX struct {
	unchanged     bool
	queryRowCalls int
}

func (*replayDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("unexpected Exec")
}

func (*replayDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected Query")
}

func (dbtx *replayDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	dbtx.queryRowCalls++
	if dbtx.queryRowCalls == 1 {
		return replayRow{err: pgx.ErrNoRows}
	}
	return replayRow{value: dbtx.unchanged}
}

type replayRow struct {
	value bool
	err   error
}

func (row replayRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	*destinations[0].(*bool) = row.value
	return nil
}

func TestBasisPointsRoundsAgainstAllTaskAttributedTokens(t *testing.T) {
	t.Parallel()
	got, err := basisPoints("50", "150")
	if err != nil || got != 3333 {
		t.Fatalf("basisPoints() = %d, %v", got, err)
	}
}
