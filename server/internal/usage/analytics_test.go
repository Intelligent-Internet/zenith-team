package usage

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
)

func TestParseAnalyticsQuery(t *testing.T) {
	const window = "scope=company&start_at=2026-09-01T00%3A00%3A00%2B07%3A00&end_at=2026-09-08T00%3A00%3A00%2B07%3A00&time_zone=Asia%2FHo_Chi_Minh"
	tests := []struct {
		name     string
		endpoint string
		query    string
		wantErr  bool
	}{
		{name: "summary", endpoint: "summary", query: window},
		{name: "channel agent", endpoint: "summary", query: window + "&run_type=channel_agent"},
		{name: "series", endpoint: "series", query: window + "&metric=cost&bucket=day"},
		{name: "breakdown", endpoint: "breakdown", query: window + "&by=model&sort=cost&limit=10"},
		{name: "unknown parameter", endpoint: "summary", query: window + "&extra=1", wantErr: true},
		{name: "repeated parameter", endpoint: "summary", query: window + "&scope=mine", wantErr: true},
		{name: "non-midnight", endpoint: "summary", query: "scope=company&start_at=2026-09-01T01%3A00%3A00%2B07%3A00&end_at=2026-09-08T00%3A00%3A00%2B07%3A00&time_zone=Asia%2FHo_Chi_Minh", wantErr: true},
		{name: "mine member filter", endpoint: "summary", query: "scope=mine&start_at=2026-09-01T00%3A00%3A00%2B07%3A00&end_at=2026-09-08T00%3A00%3A00%2B07%3A00&time_zone=Asia%2FHo_Chi_Minh&member_id=00000000-0000-4000-8000-000000000001", wantErr: true},
		{name: "breakdown missing limit", endpoint: "breakdown", query: window + "&by=model&sort=cost", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseAnalyticsQuery(tt.query, tt.endpoint)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAnalyticsSummaryRejectsInvalidWindowBeforeComparison(t *testing.T) {
	t.Parallel()
	service := &DefaultUsageService{}
	if _, err := service.GetAnalyticsSummary(context.Background(), auth.UserPrincipal{}, AnalyticsQuery{TimeZone: "invalid/zone"}); err != ErrInvalidInput {
		t.Fatalf("error = %v, want invalid input", err)
	}
}

func TestAnalyticsDecimalTotalsPreserveLargeValues(t *testing.T) {
	t.Parallel()
	if got := addDecimalStrings("9223372036854775807", "1"); got != "9223372036854775808" {
		t.Fatalf("sum = %s, want 9223372036854775808", got)
	}
}

func TestAnalyticsComparisonUsesElapsedLocalWindow(t *testing.T) {
	q, err := ParseAnalyticsQuery("scope=company&start_at=2026-09-01T00%3A00%3A00%2B07%3A00&end_at=2026-09-08T00%3A00%3A00%2B07%3A00&time_zone=Asia%2FHo_Chi_Minh", "summary")
	if err != nil {
		t.Fatal(err)
	}
	comparisonStart, comparisonEnd, cutoff := analyticsComparison(q, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	if got, want := comparisonStart.Format(time.RFC3339), "2026-08-24T17:00:00Z"; got != want {
		t.Fatalf("comparison start = %s, want %s", got, want)
	}
	if got, want := comparisonEnd.Format(time.RFC3339), "2026-08-28T12:00:00Z"; got != want {
		t.Fatalf("comparison end = %s, want %s", got, want)
	}
	if got, want := cutoff.Format(time.RFC3339), "2026-09-04T12:00:00Z"; got != want {
		t.Fatalf("cutoff = %s, want %s", got, want)
	}
}

// Mine always reads the caller's own Runs (D20); Company scope reads every
// Run unless a member filter narrows it.
func TestAnalyticsAuthorizerBindsTheCallerOnMine(t *testing.T) {
	t.Parallel()
	caller, other := uuid.New(), uuid.New()
	p := auth.UserPrincipal{UserID: caller, CompanyID: uuid.New()}
	if got := analyticsAuthorizer(p, AnalyticsQuery{Scope: AnalyticsScopeMine}); !got.Valid || got.UUID != caller {
		t.Fatalf("mine authorizer = %+v, want the caller", got)
	}
	if got := analyticsAuthorizer(p, AnalyticsQuery{Scope: AnalyticsScopeCompany}); got.Valid {
		t.Fatalf("company authorizer = %+v, want none", got)
	}
	if got := analyticsAuthorizer(p, AnalyticsQuery{Scope: AnalyticsScopeCompany, MemberID: &other}); !got.Valid || got.UUID != other {
		t.Fatalf("member filter authorizer = %+v, want the member", got)
	}
}

// The viewer that decides which Projects and Runtimes may be named is always
// the caller, never the member a Company read filters by.
func TestAnalyticsGroupsParamsViewIsTheCallers(t *testing.T) {
	t.Parallel()
	caller, other := uuid.New(), uuid.New()
	p := auth.UserPrincipal{UserID: caller, CompanyID: uuid.New()}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	params := analyticsGroupsParams(p, AnalyticsQuery{Scope: AnalyticsScopeCompany, MemberID: &other, TimeZone: "UTC"},
		start, start.AddDate(0, 0, 1), "project", "tokens", 26, 25)
	if params.ViewerUserID != caller || params.AuthorizerUserID.UUID != other || params.CompanyID != p.CompanyID {
		t.Fatalf("params = %+v, want the caller as viewer and the member as authorizer", params)
	}
	if params.GroupBy != "project" || params.SortBy != "tokens" || params.RowLimit != 26 || params.RowOffset != 25 {
		t.Fatalf("params = %+v, want the grouping and page", params)
	}
	if params.Bucket != string(BucketDay) {
		t.Fatalf("bucket = %q, want a valid unit when the read has none", params.Bucket)
	}
}

// The heatmap and failure codes read only counts, tokens and cost, so they
// skip the time percentiles and unpriced models; every other grouping keeps
// the full metric set.
func TestAnalyticsGroupsParamsDetail(t *testing.T) {
	t.Parallel()
	p := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New()}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		groupBy string
		want    bool
	}{
		{analyticsGroupNone, true},
		{analyticsGroupBucket, true},
		{"agent", true},
		{"project", true},
		{analyticsGroupHeatmap, false},
		{analyticsGroupFailureReason, false},
	} {
		params := analyticsGroupsParams(p, AnalyticsQuery{Scope: AnalyticsScopeCompany, TimeZone: "UTC"},
			start, start.AddDate(0, 0, 1), tt.groupBy, "", analyticsAllRows, 0)
		if params.Detail != tt.want {
			t.Errorf("group %q detail = %t, want %t", tt.groupBy, params.Detail, tt.want)
		}
	}
}

func TestAnalyticsHeatmapCostAddsReportedAndEstimated(t *testing.T) {
	t.Parallel()
	reported, estimated := "40", "2"
	for _, tt := range []struct {
		cost AnalyticsCost
		want string
	}{
		{AnalyticsCost{}, ""},
		{AnalyticsCost{ReportedUsdTicks: &reported}, "40"},
		{AnalyticsCost{EstimatedUsdTicks: &estimated}, "2"},
		{AnalyticsCost{ReportedUsdTicks: &reported, EstimatedUsdTicks: &estimated}, "42"},
	} {
		if got := analyticsHeatmapCost(tt.cost); got != tt.want {
			t.Errorf("analyticsHeatmapCost(%+v) = %q, want %q", tt.cost, got, tt.want)
		}
	}
}
