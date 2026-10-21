package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Every Analytics page read is the sqlc query ListAnalyticsGroups grouped one
// way. These are the groupings that are not breakdown dimensions.
const (
	analyticsGroupNone          = "none"
	analyticsGroupBucket        = "bucket"
	analyticsGroupHeatmap       = "heatmap"
	analyticsGroupFailureReason = "failure_reason"
)

// analyticsDimensions are the breakdown groupings (the "by" key), and
// analyticsSorts the breakdown orders.
var (
	analyticsDimensions = []string{"agent", "model", "member", "project", "task", "run_type", "runtime"}
	analyticsSorts      = []string{"tokens", "cost", "runs", "run_time", "failures"}
)

// analyticsAllRows is the row limit of a grouping that is not paged: one row,
// at most 368 buckets, 168 heatmap cells, a few failure codes, or the
// Company's Agents.
const analyticsAllRows = 100000

// analyticsGroup is one row of ListAnalyticsGroups: the group key as JSON and
// its metric set.
type analyticsGroup struct {
	Key     json.RawMessage
	Metrics AnalyticsMetrics
}

// analyticsAuthorizer is the Runs' authorizer filter: the caller on Mine,
// otherwise the optional member filter.
func analyticsAuthorizer(p auth.UserPrincipal, q AnalyticsQuery) uuid.NullUUID {
	if q.Scope == AnalyticsScopeMine {
		return uuid.NullUUID{UUID: p.UserID, Valid: true}
	}
	return analyticsNullableUUID(q.MemberID)
}

// analyticsGroupsParams maps one read onto ListAnalyticsGroups. The viewer is
// always the caller: it decides which Runtimes and which Projects' work may
// be named.
func analyticsGroupsParams(
	p auth.UserPrincipal,
	q AnalyticsQuery,
	start, end time.Time,
	groupBy, sortBy string,
	limit, offset int,
) generated.ListAnalyticsGroupsParams {
	bucket := string(q.Bucket)
	if bucket == "" {
		// The statement evaluates date_trunc only for the bucket grouping;
		// any valid unit keeps the other groupings well-formed.
		bucket = string(BucketDay)
	}
	// The heatmap and failure codes read only counts, tokens and cost, so
	// they skip the time percentiles and unpriced models.
	detail := groupBy != analyticsGroupHeatmap && groupBy != analyticsGroupFailureReason
	return generated.ListAnalyticsGroupsParams{
		CompanyID:        p.CompanyID,
		ViewerUserID:     p.UserID,
		StartAt:          pgtype.Timestamptz{Time: start, Valid: true},
		EndAt:            pgtype.Timestamptz{Time: end, Valid: true},
		AuthorizerUserID: analyticsAuthorizer(p, q),
		AgentID:          analyticsNullableUUID(q.AgentID),
		ProjectID:        analyticsNullableUUID(q.ProjectID),
		TaskID:           analyticsNullableUUID(q.TaskID),
		RunType:          q.RunType,
		RuntimeID:        analyticsNullableUUID(q.RuntimeID),
		Provider:         q.Provider,
		Model:            q.Model,
		GroupBy:          groupBy,
		Detail:           detail,
		SortBy:           sortBy,
		TimeZone:         q.TimeZone,
		Bucket:           bucket,
		RowLimit:         int32(limit),
		RowOffset:        int32(offset),
	}
}

// analyticsGroups runs one grouping and decodes its rows.
func analyticsGroups(
	ctx context.Context,
	queries *generated.Queries,
	params generated.ListAnalyticsGroupsParams,
) ([]analyticsGroup, error) {
	rows, err := queries.ListAnalyticsGroups(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("analytics %s query: %w", params.GroupBy, err)
	}
	groups := make([]analyticsGroup, 0, len(rows))
	for _, row := range rows {
		group := analyticsGroup{Key: json.RawMessage(row.Key)}
		if err := json.Unmarshal(row.Metrics, &group.Metrics); err != nil {
			return nil, fmt.Errorf("decode analytics %s metrics: %w", params.GroupBy, err)
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// analyticsTotals is the metric set of every Run in scope: one row grouped
// by nothing. An empty window still answers one row of zeros.
func analyticsTotals(
	ctx context.Context,
	queries *generated.Queries,
	p auth.UserPrincipal,
	q AnalyticsQuery,
	start, end time.Time,
) (AnalyticsMetrics, error) {
	groups, err := analyticsGroups(ctx, queries,
		analyticsGroupsParams(p, q, start, end, analyticsGroupNone, "", analyticsAllRows, 0))
	if err != nil {
		return AnalyticsMetrics{}, err
	}
	if len(groups) == 0 {
		return emptyAnalyticsMetrics(), nil
	}
	return groups[0].Metrics, nil
}

// analyticsHeatmapCost is a cell's cost: reported plus estimated ticks, or
// "" when no fact in the cell has a cost.
func analyticsHeatmapCost(cost AnalyticsCost) string {
	if cost.ReportedUsdTicks == nil && cost.EstimatedUsdTicks == nil {
		return ""
	}
	var total string
	for _, part := range []*string{cost.ReportedUsdTicks, cost.EstimatedUsdTicks} {
		if part != nil {
			total = addDecimalStrings(total, *part)
		}
	}
	return total
}
