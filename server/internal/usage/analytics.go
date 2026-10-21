package usage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

type AnalyticsScope string

const (
	AnalyticsScopeCompany AnalyticsScope = "company"
	AnalyticsScopeMine    AnalyticsScope = "mine"
)

type AnalyticsQuery struct {
	Scope     AnalyticsScope
	StartAt   time.Time
	EndAt     time.Time
	TimeZone  string
	Bucket    Bucket
	Provider  string
	Model     string
	AgentID   *uuid.UUID
	MemberID  *uuid.UUID
	ProjectID *uuid.UUID
	TaskID    *uuid.UUID
	Metric    string
	By        string
	Sort      string
	Limit     int
	Cursor    string
	RunType   string
	RuntimeID *uuid.UUID
}

type AnalyticsTokens struct {
	Input      string  `json:"input"`
	Output     string  `json:"output"`
	CacheRead  string  `json:"cacheRead"`
	CacheWrite string  `json:"cacheWrite"`
	Reasoning  *string `json:"reasoning"`
}

type AnalyticsModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type AnalyticsCost struct {
	PlanCoveredTokens string            `json:"planCoveredTokens"`
	UnpricedModels    []AnalyticsModel  `json:"unpricedModels"`
	ReportedUsdTicks  *string           `json:"reportedUsdTicks"`
	EstimatedUsdTicks *string           `json:"estimatedUsdTicks"`
	CoverageState     CostCoverageState `json:"coverageState"`
	CostedFactCount   string            `json:"costedFactCount"`
	UncostedFactCount string            `json:"uncostedFactCount"`
}

type AnalyticsRuns struct {
	Total         string `json:"total"`
	Completed     string `json:"completed"`
	Failed        string `json:"failed"`
	Cancelled     string `json:"cancelled"`
	Task          string `json:"task"`
	DirectChat    string `json:"directChat"`
	ChannelAgent  string `json:"channelAgent"`
	Schedule      string `json:"schedule"`
	WithoutUsage  string `json:"withoutUsage"`
	WithoutTiming string `json:"withoutTiming"`
	Retries       string `json:"retries"`
}

type AnalyticsTime struct {
	RunTimeMs        *string `json:"runTimeMs"`
	P50RunTimeMs     *string `json:"p50RunTimeMs"`
	P90RunTimeMs     *string `json:"p90RunTimeMs"`
	P50QueueWaitMs   *string `json:"p50QueueWaitMs"`
	P90QueueWaitMs   *string `json:"p90QueueWaitMs"`
	P50PreparationMs *string `json:"p50PreparationMs"`
}

type AnalyticsMetrics struct {
	Tokens AnalyticsTokens `json:"tokens"`
	Cost   AnalyticsCost   `json:"cost"`
	Runs   AnalyticsRuns   `json:"runs"`
	Time   AnalyticsTime   `json:"time"`
}

type AnalyticsSummary struct {
	Scope             AnalyticsScope   `json:"scope"`
	TimeZone          string           `json:"timeZone"`
	PeriodStartAt     time.Time        `json:"periodStartAt"`
	PeriodEndAt       time.Time        `json:"periodEndAt"`
	ComparisonStartAt time.Time        `json:"comparisonStartAt"`
	ComparisonEndAt   time.Time        `json:"comparisonEndAt"`
	AsOf              time.Time        `json:"asOf"`
	ActiveNow         string           `json:"activeNow"`
	Current           AnalyticsMetrics `json:"current"`
	Comparison        AnalyticsMetrics `json:"comparison"`
}

type AnalyticsSeriesPoint struct {
	StartAt time.Time          `json:"startAt"`
	EndAt   time.Time          `json:"endAt"`
	Parts   map[string]*string `json:"parts"`
}

type AnalyticsSeries struct {
	Scope  AnalyticsScope         `json:"scope"`
	Metric string                 `json:"metric"`
	Bucket Bucket                 `json:"bucket"`
	Points []AnalyticsSeriesPoint `json:"points"`
}

type AnalyticsHeatmapCell struct {
	Weekday      int    `json:"weekday"`
	Hour         int    `json:"hour"`
	Runs         string `json:"runs"`
	Tokens       string `json:"tokens"`
	CostUsdTicks string `json:"costUsdTicks"`
}

type AnalyticsHeatmap struct {
	Scope string                 `json:"scope"`
	Cells []AnalyticsHeatmapCell `json:"cells"`
}

type AnalyticsBreakdownKey struct {
	Kind      string     `json:"kind"`
	ID        *uuid.UUID `json:"id"`
	Label     string     `json:"label"`
	Synthetic *string    `json:"synthetic"`
}

type AnalyticsBreakdownRow struct {
	Key AnalyticsBreakdownKey `json:"key"`
	AnalyticsMetrics
}

type AnalyticsBreakdown struct {
	By         string                  `json:"by"`
	Rows       []AnalyticsBreakdownRow `json:"rows"`
	Total      AnalyticsMetrics        `json:"total"`
	NextCursor *string                 `json:"nextCursor"`
}

type AnalyticsFailureAgent struct {
	Key      AnalyticsBreakdownKey `json:"key"`
	Failed   string                `json:"failed"`
	Finished string                `json:"finished"`
	Rankable bool                  `json:"rankable"`
}

type AnalyticsFailures struct {
	Agents    []AnalyticsFailureAgent `json:"agents"`
	Succeeded string                  `json:"succeeded"`
	ByClass   map[string]string       `json:"byClass"`
	ByCode    map[string]string       `json:"byCode"`
}

type AnalyticsTaskUsageRun struct {
	ID              uuid.UUID  `json:"id"`
	Status          string     `json:"status"`
	Agent           string     `json:"agent"`
	Model           *string    `json:"model"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
	DurationMs      *string    `json:"durationMs"`
	QueueWaitMs     *string    `json:"queueWaitMs"`
	Tokens          *string    `json:"tokens"`
	CostUsdTicks    *string    `json:"costUsdTicks"`
	UsageRestricted bool       `json:"usageRestricted"`
	FailureClass    *string    `json:"failureClass"`
}

type AnalyticsTaskUsage struct {
	TaskID          uuid.UUID               `json:"taskId"`
	Runs            string                  `json:"runs"`
	AgentTimeMs     *string                 `json:"agentTimeMs"`
	LeadTimeMs      *string                 `json:"leadTimeMs"`
	Tokens          *string                 `json:"tokens"`
	CostUsdTicks    *string                 `json:"costUsdTicks"`
	UsageRestricted bool                    `json:"usageRestricted"`
	Items           []AnalyticsTaskUsageRun `json:"items"`
}

// AnalyticsBudget is a soft monthly spending target. The value is kept as
// decimal USD ticks (1e-10 dollars) so the API never rounds a large total
// through a floating-point number.
type AnalyticsBudget struct {
	ID                   uuid.UUID  `json:"id"`
	OwnerKind            string     `json:"ownerKind"`
	AgentID              *uuid.UUID `json:"agentId,omitempty"`
	MonthlyLimitUsdTicks string     `json:"monthlyLimitUsdTicks"`
	Enabled              bool       `json:"enabled"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

type AnalyticsBudgetPage struct {
	Items []AnalyticsBudget `json:"items"`
}

type AnalyticsBudgetInput struct {
	OwnerKind            string
	AgentID              *uuid.UUID
	MonthlyLimitUsdTicks string
	Enabled              bool
}

var (
	ErrAnalyticsBusy     = errors.New("analytics query timed out")
	ErrTaskUsageNotFound = errors.New("task usage task not found")
)

func ParseAnalyticsQuery(rawQuery, endpoint string) (AnalyticsQuery, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return AnalyticsQuery{}, ErrInvalidInput
	}
	allowed := map[string]bool{
		"scope":      true,
		"start_at":   true,
		"end_at":     true,
		"time_zone":  true,
		"provider":   true,
		"model":      true,
		"agent_id":   true,
		"member_id":  true,
		"project_id": true,
		"task_id":    true,
		"run_type":   true,
		"runtime_id": true,
	}
	switch endpoint {
	case "summary", "failures", "heatmap":
	case "series":
		allowed["metric"] = true
		allowed["bucket"] = true
	case "breakdown":
		for _, key := range []string{"by", "sort", "limit", "cursor"} {
			allowed[key] = true
		}
	default:
		return AnalyticsQuery{}, ErrInvalidInput
	}
	for key, v := range values {
		if !allowed[key] || len(v) != 1 || strings.TrimSpace(v[0]) == "" {
			return AnalyticsQuery{}, ErrInvalidInput
		}
	}
	q := AnalyticsQuery{
		Scope:    AnalyticsScope(values.Get("scope")),
		TimeZone: values.Get("time_zone"),
		Provider: values.Get("provider"),
		Model:    values.Get("model"),
		RunType:  values.Get("run_type"),
		Metric:   values.Get("metric"),
		Bucket:   Bucket(values.Get("bucket")),
		By:       values.Get("by"),
		Sort:     values.Get("sort"),
		Cursor:   values.Get("cursor"),
	}
	q.StartAt, err = time.Parse(time.RFC3339Nano, values.Get("start_at"))
	if err != nil {
		return q, ErrInvalidInput
	}
	q.EndAt, err = time.Parse(time.RFC3339Nano, values.Get("end_at"))
	if err != nil {
		return q, ErrInvalidInput
	}
	for key, target := range map[string]**uuid.UUID{"agent_id": &q.AgentID, "member_id": &q.MemberID, "project_id": &q.ProjectID, "task_id": &q.TaskID, "runtime_id": &q.RuntimeID} {
		if value := values.Get(key); value != "" {
			id, e := uuid.Parse(value)
			if e != nil || id == uuid.Nil {
				return q, ErrInvalidInput
			}
			*target = &id
		}
	}
	if endpoint == "series" &&
		(!oneOf(q.Metric, "cost", "tokens", "runs", "run_time", "failures") || !oneOf(string(q.Bucket), "day", "week", "month")) {
		return q, ErrInvalidInput
	}
	if endpoint == "breakdown" {
		q.Limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || q.Limit < 1 || q.Limit > 100 ||
			!oneOf(q.By, "agent", "model", "member", "project", "task", "run_type", "runtime") ||
			!oneOf(q.Sort, "tokens", "cost", "runs", "run_time", "failures") ||
			(q.Scope == AnalyticsScopeMine && q.By == "member") {
			return q, ErrInvalidInput
		}
	}
	if err := validateAnalyticsQuery(q); err != nil {
		return q, err
	}
	return q, nil
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateAnalyticsQuery(q AnalyticsQuery) error {
	loc, err := time.LoadLocation(q.TimeZone)
	if err != nil || q.TimeZone == "" || q.TimeZone == "Local" || !oneOf(string(q.Scope), "company", "mine") ||
		!q.EndAt.After(q.StartAt) ||
		!isExactLocalMidnight(q.StartAt, loc) ||
		!isExactLocalMidnight(q.EndAt, loc) ||
		q.EndAt.After(q.StartAt.In(loc).AddDate(0, 0, 367)) {
		return ErrInvalidInput
	}
	if q.Provider != "" && !oneOf(q.Provider, "claude", "codex") ||
		q.RunType != "" && !oneOf(q.RunType, "task", "direct_chat", "channel_agent", "schedule") ||
		len(q.Model) > 255 ||
		q.Scope == AnalyticsScopeMine && q.MemberID != nil {
		return ErrInvalidInput
	}
	return nil
}

func analyticsComparison(q AnalyticsQuery, now time.Time) (time.Time, time.Time, time.Time) {
	loc, _ := time.LoadLocation(q.TimeZone)
	start, end := q.StartAt.In(loc), q.EndAt.In(loc)
	days := 0
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		days++
	}
	cutoff := now.In(loc)
	if cutoff.After(end) {
		cutoff = end
	}
	if cutoff.Before(start) {
		cutoff = start
	}
	return start.AddDate(0, 0, -days).UTC(), cutoff.AddDate(0, 0, -days).UTC(), cutoff.UTC()
}

// analyticsRead holds authorization and all aggregates in one database
// snapshot. A project_id or task_id filter must name work the caller reads
// (ADR 0017): totals of one hidden Project or Task would reveal it, so such a
// filter answers like a missing one.
func (s *DefaultUsageService) analyticsRead(
	ctx context.Context,
	p auth.UserPrincipal,
	q AnalyticsQuery,
	read func(*generated.Queries) error,
) error {
	if err := validateAnalyticsQuery(q); err != nil {
		return err
	}
	if p.UserID == uuid.Nil || p.CompanyID == uuid.Nil {
		return ErrForbidden
	}
	if q.Scope == AnalyticsScopeCompany && !p.Can(auth.PermissionUsageRead) {
		return ErrForbidden
	}
	err := s.store.WithinRawTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx generated.DBTX, queries *generated.Queries) error {
			if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '5s'"); err != nil {
				return err
			}
			var active bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM company_members m JOIN companies c ON c.id=m.company_id WHERE m.company_id=$1 AND m.user_id=$2 AND m.status='active' AND c.deleting_at IS NULL AND ($3::boolean=false OR m.role=ANY($4::text[])))`, p.CompanyID, p.UserID, q.Scope == AnalyticsScopeCompany, auth.RolesWith(auth.PermissionUsageRead)).
				Scan(&active); err != nil {
				return err
			}
			if !active {
				return ErrForbidden
			}
			if err := analyticsFilterReadable(ctx, queries, p, q); err != nil {
				return err
			}
			return read(queries)
		},
	)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "57014" {
		return ErrAnalyticsBusy
	}
	if err == nil || errors.Is(err, ErrForbidden) || errors.Is(err, ErrInvalidInput) ||
		errors.Is(err, auth.ErrProjectNotVisible) || errors.Is(err, auth.ErrTaskNotVisible) {
		return err
	}
	return fmt.Errorf("%w: analytics: %w", ErrStore, err)
}

// analyticsFilterReadable refuses a Project or Task filter the caller does
// not read: auth.ErrProjectNotVisible (404 project_not_found) for a Project,
// auth.ErrTaskNotVisible (404 task_not_found) for a Task, the same answers as
// for one that does not exist. The management bridge reads no work, so it
// does not open a Project's numbers either.
func analyticsFilterReadable(
	ctx context.Context,
	queries *generated.Queries,
	p auth.UserPrincipal,
	q AnalyticsQuery,
) error {
	if q.ProjectID != nil {
		access, err := auth.ResolveProjectAccess(ctx, queries, p, *q.ProjectID)
		if err != nil {
			return err
		}
		if access.Via == "" {
			return auth.ErrProjectNotVisible
		}
	}
	if q.TaskID != nil {
		if _, err := auth.RequireTaskPermission(
			ctx,
			queries,
			p,
			*q.TaskID,
			auth.ProjectPermissionRead,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *DefaultUsageService) GetAnalyticsSummary(
	ctx context.Context,
	p auth.UserPrincipal,
	q AnalyticsQuery,
) (AnalyticsSummary, error) {
	if err := validateAnalyticsQuery(q); err != nil {
		return AnalyticsSummary{}, err
	}
	q.By = ""
	a, b, cutoff := analyticsComparison(q, time.Now())
	result := AnalyticsSummary{
		Scope:             q.Scope,
		TimeZone:          q.TimeZone,
		PeriodStartAt:     q.StartAt,
		PeriodEndAt:       q.EndAt,
		ComparisonStartAt: a,
		ComparisonEndAt:   b,
		AsOf:              time.Now().UTC(),
	}
	err := s.analyticsRead(ctx, p, q, func(queries *generated.Queries) error {
		var err error
		result.Current, err = analyticsTotals(ctx, queries, p, q, q.StartAt, cutoff)
		if err != nil {
			return err
		}
		result.Comparison, err = analyticsTotals(ctx, queries, p, q, a, b)
		if err != nil {
			return err
		}
		result.ActiveNow, err = queries.CountAnalyticsActiveRuns(ctx, generated.CountAnalyticsActiveRunsParams{
			CompanyID: p.CompanyID, ViewerUserID: p.UserID,
			AuthorizerUserID: analyticsAuthorizer(p, q), AgentID: analyticsNullableUUID(q.AgentID),
			ProjectID: analyticsNullableUUID(q.ProjectID), TaskID: analyticsNullableUUID(q.TaskID),
			RunType: q.RunType, RuntimeID: analyticsNullableUUID(q.RuntimeID),
			Provider: q.Provider, Model: q.Model,
		})
		if err != nil {
			return fmt.Errorf("analytics active runs: %w", err)
		}
		return nil
	})
	return result, err
}

func (s *DefaultUsageService) GetAnalyticsSeries(
	ctx context.Context,
	p auth.UserPrincipal,
	q AnalyticsQuery,
) (AnalyticsSeries, error) {
	q.By = ""
	result := AnalyticsSeries{Scope: q.Scope, Metric: q.Metric, Bucket: q.Bucket, Points: []AnalyticsSeriesPoint{}}
	if !oneOf(q.Metric, "cost", "tokens", "runs", "run_time", "failures") ||
		!oneOf(string(q.Bucket), "day", "week", "month") {
		return result, ErrInvalidInput
	}
	err := s.analyticsRead(ctx, p, q, func(queries *generated.Queries) error {
		groups, err := analyticsGroups(ctx, queries,
			analyticsGroupsParams(p, q, q.StartAt, q.EndAt, analyticsGroupBucket, "", analyticsAllRows, 0))
		if err != nil {
			return err
		}
		byStart := map[int64]AnalyticsMetrics{}
		for _, group := range groups {
			var start int64
			if err := json.Unmarshal(group.Key, &start); err != nil {
				return fmt.Errorf("decode analytics bucket: %w", err)
			}
			byStart[start] = group.Metrics
		}
		loc, _ := time.LoadLocation(q.TimeZone)
		start := q.StartAt.In(loc)
		switch q.Bucket {
		case "week":
			start = start.AddDate(0, 0, -(int(start.Weekday())+6)%7)
		case "month":
			start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, loc)
		}
		for start.Before(q.EndAt) {
			end := nextAnalyticsBucket(start, q.Bucket)
			m, exists := byStart[start.Unix()]
			if !exists {
				m = emptyAnalyticsMetrics()
			}
			a, b := start, end
			if a.Before(q.StartAt) {
				a = q.StartAt
			}
			if b.After(q.EndAt) {
				b = q.EndAt
			}
			result.Points = append(
				result.Points,
				AnalyticsSeriesPoint{
					StartAt: a.UTC(),
					EndAt:   b.UTC(),
					Parts:   analyticsParts(m, q.Metric),
				},
			)
			start = end
		}
		return nil
	})
	return result, err
}

func (s *DefaultUsageService) GetAnalyticsHeatmap(
	ctx context.Context,
	p auth.UserPrincipal,
	q AnalyticsQuery,
) (AnalyticsHeatmap, error) {
	q.By = ""
	result := AnalyticsHeatmap{Scope: string(q.Scope), Cells: make([]AnalyticsHeatmapCell, 0, 168)}
	// Dense output makes the client a pure renderer and keeps empty weekday/hour
	// buckets explicit. The same statement and statement timeout as every
	// other analytics read preserve identical filters and privacy.
	err := s.analyticsRead(ctx, p, q, func(queries *generated.Queries) error {
		groups, err := analyticsGroups(ctx, queries,
			analyticsGroupsParams(p, q, q.StartAt, q.EndAt, analyticsGroupHeatmap, "", analyticsAllRows, 0))
		if err != nil {
			return err
		}
		values := make(map[[2]int]AnalyticsHeatmapCell)
		for _, group := range groups {
			var at [2]int
			if err := json.Unmarshal(group.Key, &at); err != nil {
				return fmt.Errorf("decode analytics heatmap cell: %w", err)
			}
			m := group.Metrics
			values[at] = AnalyticsHeatmapCell{
				Weekday: at[0], Hour: at[1], Runs: m.Runs.Total,
				Tokens:       addDecimalStrings(m.Tokens.Input, m.Tokens.Output),
				CostUsdTicks: analyticsHeatmapCost(m.Cost),
			}
		}
		for weekday := range 7 {
			for hour := range 24 {
				cell, ok := values[[2]int{weekday, hour}]
				if !ok {
					cell = AnalyticsHeatmapCell{
						Weekday:      weekday,
						Hour:         hour,
						Runs:         "0",
						Tokens:       "0",
						CostUsdTicks: "0",
					}
				}
				result.Cells = append(result.Cells, cell)
			}
		}
		return nil
	})
	return result, err
}

func nextAnalyticsBucket(start time.Time, bucket Bucket) time.Time {
	switch bucket {
	case "week":
		return start.AddDate(0, 0, 7)
	case "month":
		return start.AddDate(0, 1, 0)
	default:
		return start.AddDate(0, 0, 1)
	}
}

func emptyAnalyticsMetrics() AnalyticsMetrics {
	return AnalyticsMetrics{
		Tokens: AnalyticsTokens{Input: "0", Output: "0", CacheRead: "0", CacheWrite: "0"},
		Cost: AnalyticsCost{
			CoverageState:     CostCoverageMissing,
			PlanCoveredTokens: "0",
			CostedFactCount:   "0",
			UncostedFactCount: "0",
			UnpricedModels:    []AnalyticsModel{},
		},
		Runs: AnalyticsRuns{
			Total:         "0",
			Completed:     "0",
			Failed:        "0",
			Cancelled:     "0",
			Task:          "0",
			DirectChat:    "0",
			ChannelAgent:  "0",
			Schedule:      "0",
			WithoutUsage:  "0",
			WithoutTiming: "0",
			Retries:       "0",
		},
	}
}

func stringPointer(s string) *string { return &s }
func analyticsParts(m AnalyticsMetrics, metric string) map[string]*string {
	switch metric {
	case "cost":
		return map[string]*string{"reported": m.Cost.ReportedUsdTicks, "estimated": m.Cost.EstimatedUsdTicks}
	case "runs":
		return map[string]*string{
			"completed": stringPointer(m.Runs.Completed),
			"failed":    stringPointer(m.Runs.Failed),
			"cancelled": stringPointer(m.Runs.Cancelled),
		}
	case "run_time":
		return map[string]*string{"runTimeMs": m.Time.RunTimeMs}
	case "failures":
		return map[string]*string{
			"failed":    stringPointer(m.Runs.Failed),
			"succeeded": stringPointer(m.Runs.Completed),
		}
	default:
		return map[string]*string{
			"input":      stringPointer(m.Tokens.Input),
			"output":     stringPointer(m.Tokens.Output),
			"cacheRead":  stringPointer(m.Tokens.CacheRead),
			"cacheWrite": stringPointer(m.Tokens.CacheWrite),
		}
	}
}

// GetAnalyticsBreakdown lists one dimension's rows. Every row of a Project
// the caller does not read, by Project or by Task, is one unnamed row
// (synthetic other_work) so the rows still add up to total.
func (s *DefaultUsageService) GetAnalyticsBreakdown(
	ctx context.Context,
	p auth.UserPrincipal,
	q AnalyticsQuery,
) (AnalyticsBreakdown, error) {
	result := AnalyticsBreakdown{By: q.By, Rows: []AnalyticsBreakdownRow{}}
	if q.Scope == AnalyticsScopeMine && q.By == "member" {
		return result, ErrInvalidInput
	}
	if !oneOf(q.By, analyticsDimensions...) || !oneOf(q.Sort, analyticsSorts...) || q.Limit < 1 || q.Limit > 100 {
		return result, ErrInvalidInput
	}
	offset, err := analyticsCursorOffset(q)
	if err != nil {
		return result, err
	}
	err = s.analyticsRead(ctx, p, q, func(queries *generated.Queries) error {
		var err error
		result.Total, err = analyticsTotals(ctx, queries, p, q, q.StartAt, q.EndAt)
		if err != nil {
			return err
		}
		groups, err := analyticsGroups(ctx, queries,
			analyticsGroupsParams(p, q, q.StartAt, q.EndAt, q.By, q.Sort, q.Limit+1, offset))
		if err != nil {
			return err
		}
		for _, group := range groups {
			row := AnalyticsBreakdownRow{AnalyticsMetrics: group.Metrics}
			if err := json.Unmarshal(group.Key, &row.Key); err != nil {
				return fmt.Errorf("decode analytics %s key: %w", q.By, err)
			}
			result.Rows = append(result.Rows, row)
		}
		if len(result.Rows) > q.Limit {
			result.Rows = result.Rows[:q.Limit]
			cursor := analyticsCursor(q, offset+q.Limit)
			result.NextCursor = &cursor
		}
		return nil
	})
	return result, err
}

func (s *DefaultUsageService) GetAnalyticsFailures(
	ctx context.Context,
	p auth.UserPrincipal,
	q AnalyticsQuery,
) (AnalyticsFailures, error) {
	q.By = ""
	result := AnalyticsFailures{
		Agents:  []AnalyticsFailureAgent{},
		ByClass: map[string]string{},
		ByCode:  map[string]string{},
	}
	err := s.analyticsRead(ctx, p, q, func(queries *generated.Queries) error {
		// Failures read only Run counts, so no grouping here computes the time
		// percentiles or unpriced models.
		totalsParams := analyticsGroupsParams(
			p,
			q,
			q.StartAt,
			q.EndAt,
			analyticsGroupNone,
			"",
			analyticsAllRows,
			0,
		)
		totalsParams.Detail = false
		totals, err := analyticsGroups(ctx, queries, totalsParams)
		if err != nil {
			return fmt.Errorf("read analytics successes: %w", err)
		}
		result.Succeeded = "0"
		if len(totals) > 0 {
			result.Succeeded = totals[0].Metrics.Runs.Completed
		}
		codes, err := analyticsGroups(
			ctx,
			queries,
			analyticsGroupsParams(
				p,
				q,
				q.StartAt,
				q.EndAt,
				analyticsGroupFailureReason,
				"failures",
				analyticsAllRows,
				0,
			),
		)
		if err != nil {
			return fmt.Errorf("read analytics failure codes: %w", err)
		}
		for _, group := range codes {
			var reason string
			if err := json.Unmarshal(group.Key, &reason); err != nil {
				return fmt.Errorf("decode analytics failure code: %w", err)
			}
			// failed counts failed Runs that were not cancelled; a code
			// only completed or cancelled Runs carry is no failure.
			count := group.Metrics.Runs.Failed
			if count == "0" {
				continue
			}
			result.ByCode[reason] = count
			class := runfailure.Class(runfailure.Reason(reason))
			result.ByClass[class] = addDecimalStrings(result.ByClass[class], count)
		}
		agentParams := analyticsGroupsParams(p, q, q.StartAt, q.EndAt, "agent", "failures", analyticsAllRows, 0)
		agentParams.Detail = false
		agents, err := analyticsGroups(ctx, queries, agentParams)
		if err != nil {
			return fmt.Errorf("read analytics failure agents: %w", err)
		}
		var deleted *AnalyticsFailureAgent
		for _, group := range agents {
			var key AnalyticsBreakdownKey
			if err := json.Unmarshal(group.Key, &key); err != nil {
				return fmt.Errorf("decode analytics failure agent: %w", err)
			}
			agent := AnalyticsFailureAgent{
				Key:      key,
				Failed:   group.Metrics.Runs.Failed,
				Finished: addDecimalStrings(group.Metrics.Runs.Completed, group.Metrics.Runs.Failed),
			}
			// Archived and deleted Agents are one unranked row, listed last.
			if key.Synthetic != nil {
				deleted = &agent
				continue
			}
			agent.Rankable = decimalAtLeast(agent.Finished, "10")
			result.Agents = append(result.Agents, agent)
		}
		if deleted != nil {
			result.Agents = append(result.Agents, *deleted)
		}
		return nil
	})
	return result, err
}

// GetTaskUsage reads one Task's Runs and totals. The Task must be in a
// Project the caller reads (ADR 0017); otherwise it answers like a missing
// Task. Tokens and cost show for usage.read holders, and otherwise only on
// the Runs the caller authorized.
func (s *DefaultUsageService) GetTaskUsage(
	ctx context.Context,
	p auth.UserPrincipal,
	taskID uuid.UUID,
) (AnalyticsTaskUsage, error) {
	result := AnalyticsTaskUsage{TaskID: taskID, Items: []AnalyticsTaskUsageRun{}}
	if taskID == uuid.Nil || p.UserID == uuid.Nil || p.CompanyID == uuid.Nil {
		return result, ErrInvalidInput
	}
	err := s.store.WithinRawTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx generated.DBTX, queries *generated.Queries) error {
			if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '5s'"); err != nil {
				return err
			}
			task, err := queries.GetTaskUsageTaskForReader(ctx, generated.GetTaskUsageTaskForReaderParams{
				CompanyID: p.CompanyID, UserID: p.UserID, TaskID: taskID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTaskUsageNotFound
			} else if err != nil {
				return err
			}
			canReadCompanyUsage := p.Can(auth.PermissionUsageRead) &&
				auth.CompanyRole(task.Role).Can(auth.PermissionUsageRead)
			if task.CompletedAt.Valid {
				value := durationMillis(task.CreatedAt.Time, task.CompletedAt.Time)
				result.LeadTimeMs = &value
			} else {
				value := durationMillis(task.CreatedAt.Time, time.Now().UTC())
				result.LeadTimeMs = &value
			}
			rows, err := queries.ListTaskUsageRuns(ctx, generated.ListTaskUsageRunsParams{
				CompanyID: p.CompanyID, TaskID: taskID, UserID: p.UserID,
			})
			if err != nil {
				return err
			}
			agentTime, tokens, cost := "0", "0", "0"
			var hasTiming, hasTokens, hasCost bool
			for _, row := range rows {
				authorizer := row.ExecutionAuthorizedByUserID
				restricted := !canReadCompanyUsage && (!authorizer.Valid || authorizer.UUID != p.UserID)
				item := AnalyticsTaskUsageRun{
					ID:              row.ID,
					Status:          row.Status,
					Agent:           row.AgentName,
					UsageRestricted: restricted,
				}
				if row.Model != "" {
					model := row.Model
					item.Model = &model
				}
				if row.StartedAt.Valid {
					value := row.StartedAt.Time.UTC()
					item.StartedAt = &value
				}
				if row.FinishedAt.Valid {
					value := row.FinishedAt.Time.UTC()
					item.FinishedAt = &value
				}
				if row.QueuedAt.Valid && row.ClaimedAt.Valid {
					value := durationMillis(row.QueuedAt.Time, row.ClaimedAt.Time)
					item.QueueWaitMs = &value
				}
				if row.StartedAt.Valid && row.FinishedAt.Valid {
					value := durationMillis(row.StartedAt.Time, row.FinishedAt.Time)
					item.DurationMs = &value
					agentTime = addDecimalStrings(agentTime, value)
					hasTiming = true
				}
				if !restricted && row.FactCount != "0" {
					if row.Tokens != "" {
						value := row.Tokens
						item.Tokens = &value
						tokens = addDecimalStrings(tokens, value)
						hasTokens = true
					}
					if row.CostUsdTicks != "" {
						value := row.CostUsdTicks
						item.CostUsdTicks = &value
						cost = addDecimalStrings(cost, value)
						hasCost = true
					}
				}
				result.Items = append(result.Items, item)
			}
			result.Runs = strconv.Itoa(len(result.Items))
			if hasTiming {
				result.AgentTimeMs = &agentTime
			}
			if hasTokens {
				result.Tokens = &tokens
			}
			if hasCost {
				result.CostUsdTicks = &cost
			}
			result.UsageRestricted = !canReadCompanyUsage
			return nil
		},
	)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "57014" {
		return result, ErrAnalyticsBusy
	}
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrTaskUsageNotFound) || errors.Is(err, ErrInvalidInput) {
		return result, err
	}
	if err != nil {
		return result, fmt.Errorf("%w: task usage: %w", ErrStore, err)
	}
	return result, nil
}

func (s *DefaultUsageService) GetAnalyticsBudgets(
	ctx context.Context,
	p auth.UserPrincipal,
) ([]AnalyticsBudget, error) {
	result := []AnalyticsBudget{}
	err := s.store.WithinRawTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(_ generated.DBTX, q *generated.Queries) error {
			if err := authorizeAnalyticsCompany(ctx, q, p); err != nil {
				return err
			}
			rows, err := q.ListAnalyticsBudgets(ctx, p.CompanyID)
			if err != nil {
				return err
			}
			for _, row := range rows {
				result = append(result, analyticsBudgetFromRow(row))
			}
			return nil
		},
	)
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrInvalidInput) {
		return result, err
	}
	if err != nil {
		return result, fmt.Errorf("%w: analytics budgets: %w", ErrStore, err)
	}
	return result, nil
}

func (s *DefaultUsageService) UpsertAnalyticsBudget(
	ctx context.Context,
	p auth.UserPrincipal,
	input AnalyticsBudgetInput,
) (AnalyticsBudget, error) {
	if input.OwnerKind != "company" && input.OwnerKind != "agent" {
		return AnalyticsBudget{}, ErrInvalidInput
	}
	if input.OwnerKind == "company" && input.AgentID != nil ||
		input.OwnerKind == "agent" && (input.AgentID == nil || *input.AgentID == uuid.Nil) {
		return AnalyticsBudget{}, ErrInvalidInput
	}
	limit, err := strconv.ParseInt(input.MonthlyLimitUsdTicks, 10, 64)
	if err != nil || limit <= 0 {
		return AnalyticsBudget{}, ErrInvalidInput
	}
	var result AnalyticsBudget
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockAnalyticsBudgetMembership(ctx, q, p); err != nil {
			return err
		}
		if input.AgentID != nil {
			if _, err := q.LockActiveAgentParent(
				ctx,
				generated.LockActiveAgentParentParams{CompanyID: p.CompanyID, AgentID: *input.AgentID},
			); errors.Is(
				err,
				pgx.ErrNoRows,
			) {
				return ErrInvalidInput
			} else if err != nil {
				return err
			}
		}
		row, err := q.UpsertAnalyticsBudget(ctx, generated.UpsertAnalyticsBudgetParams{
			CompanyID: p.CompanyID, OwnerKind: input.OwnerKind,
			AgentID: analyticsNullableUUID(input.AgentID), MonthlyLimitUsdTicks: limit,
			Enabled: input.Enabled, CreatedByUserID: p.UserID,
		})
		if err != nil {
			return err
		}
		result = analyticsBudgetFromRow(row)
		return nil
	})
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrInvalidInput) {
		return result, err
	}
	if err != nil {
		return result, fmt.Errorf("%w: analytics budget: %w", ErrStore, err)
	}
	return result, nil
}

func authorizeAnalyticsCompany(ctx context.Context, q *generated.Queries, p auth.UserPrincipal) error {
	if p.UserID == uuid.Nil || p.CompanyID == uuid.Nil || !p.Can(auth.PermissionUsageRead) {
		return ErrForbidden
	}
	member, err := q.GetActiveCompanyMembership(
		ctx,
		generated.GetActiveCompanyMembershipParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	if !auth.CompanyRole(member.Role).Can(auth.PermissionUsageRead) {
		return ErrForbidden
	}
	return nil
}

func lockAnalyticsBudgetMembership(ctx context.Context, q *generated.Queries, p auth.UserPrincipal) error {
	if !p.Can(auth.PermissionUsageRead) {
		return ErrForbidden
	}
	if _, err := q.LockCompanyForMembershipMutation(ctx, p.CompanyID); errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	} else if err != nil {
		return err
	}
	member, err := q.GetCompanyMemberForUpdate(
		ctx,
		generated.GetCompanyMemberForUpdateParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) ||
		err == nil &&
			(member.Status != "active" || !auth.CompanyRole(member.Role).Can(auth.PermissionUsageRead)) {
		return ErrForbidden
	}
	return err
}

func analyticsNullableUUID(value *uuid.UUID) uuid.NullUUID {
	if value == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *value, Valid: true}
}

func analyticsBudgetFromRow(row generated.AnalyticsBudget) AnalyticsBudget {
	result := AnalyticsBudget{
		ID:                   row.ID,
		OwnerKind:            row.OwnerKind,
		MonthlyLimitUsdTicks: strconv.FormatInt(row.MonthlyLimitUsdTicks, 10),
		Enabled:              row.Enabled,
	}
	if row.AgentID.Valid {
		value := row.AgentID.UUID
		result.AgentID = &value
	}
	if row.CreatedAt.Valid {
		result.CreatedAt = row.CreatedAt.Time.UTC()
	}
	if row.UpdatedAt.Valid {
		result.UpdatedAt = row.UpdatedAt.Time.UTC()
	}
	return result
}

func addDecimalStrings(a, b string) string {
	x, y := analyticsDecimal(a), analyticsDecimal(b)
	return x.Add(x, y).String()
}

// Aggregates are PostgreSQL numeric integers, which can exceed int64. An empty
// accumulator represents zero; database values themselves are already typed.
func analyticsDecimal(value string) *big.Int {
	result := new(big.Int)
	if value != "" {
		result.SetString(value, 10)
	}
	return result
}

func decimalAtLeast(value, threshold string) bool {
	return analyticsDecimal(value).Cmp(analyticsDecimal(threshold)) >= 0
}

func durationMillis(start, end time.Time) string {
	if end.Before(start) {
		return "0"
	}
	return strconv.FormatInt(end.Sub(start).Milliseconds(), 10)
}

func analyticsCursor(q AnalyticsQuery, offset int) string {
	q.Cursor = ""
	encoded, _ := json.Marshal(q)
	hash := sha256.Sum256(encoded)
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%x", offset, hash[:8])))
}

func analyticsCursorOffset(q AnalyticsQuery) (int, error) {
	if q.Cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(q.Cursor)
	if err != nil {
		return 0, ErrInvalidInput
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 {
		return 0, ErrInvalidInput
	}
	offset, err := strconv.Atoi(parts[0])
	if err != nil || offset < 0 || offset > 100000 || analyticsCursor(q, offset) != q.Cursor {
		return 0, ErrInvalidInput
	}
	return offset, nil
}
