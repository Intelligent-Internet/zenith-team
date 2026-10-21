package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/telemetry"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

var ErrRunUsageConflict = errors.New("run usage fact conflicts with the stored fact")

const maxRunUsageModelBytes = 255

// Usage-basis vocabulary, shared by the wire field and the usage_basis
// column's CHECK constraint.
const (
	// UsageBasisPerRun: the counters cover the run's own consumption. The
	// default when a report omits the field.
	UsageBasisPerRun = "per_run"
	// UsageBasisSessionCumulative: the provider reports totals for the whole
	// resumed session, so the fact supersedes rather than adds to the
	// session's earlier runs. New provider adapters normalize their facts to
	// UsageBasisPerRun; this value remains for legacy evidence.
	UsageBasisSessionCumulative = "session_cumulative"
	BillingModeAPI              = "api"
	BillingModeSubscription     = "subscription"
	BillingModeUnknown          = "unknown"
)

// RunUsageInput is one provider/model's final immutable fact for a run.
type RunUsageInput struct {
	Provider           string
	Model              string
	UsageBasis         string
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	CostUSDTicks       *int64
	ReasoningTokens    *int64
	Turns              *int32
	ProviderDurationMs *int64
	BillingMode        string
	RawJSON            json.RawMessage
}

// UsageService owns immutable run-usage ingestion and the Agent and Company
// reports derived directly from retained runs and their usage facts.
type UsageService interface {
	ReportRunUsage(context.Context, uuid.UUID, []RunUsageInput) error
	Get(context.Context, auth.UserPrincipal, uuid.UUID, Period) (AgentUsageReport, error)
	GetCompany(context.Context, auth.UserPrincipal, CompanyPeriod) (CompanyUsageReport, error)
	GetActivityInsight(context.Context, auth.UserPrincipal) (ActivityInsight, error)
	GetAnalyticsSummary(context.Context, auth.UserPrincipal, AnalyticsQuery) (AnalyticsSummary, error)
	GetAnalyticsSeries(context.Context, auth.UserPrincipal, AnalyticsQuery) (AnalyticsSeries, error)
	GetAnalyticsBreakdown(context.Context, auth.UserPrincipal, AnalyticsQuery) (AnalyticsBreakdown, error)
	GetAnalyticsFailures(context.Context, auth.UserPrincipal, AnalyticsQuery) (AnalyticsFailures, error)
	GetTaskUsage(context.Context, auth.UserPrincipal, uuid.UUID) (AnalyticsTaskUsage, error)
	GetAnalyticsHeatmap(context.Context, auth.UserPrincipal, AnalyticsQuery) (AnalyticsHeatmap, error)
	GetAnalyticsBudgets(context.Context, auth.UserPrincipal) ([]AnalyticsBudget, error)
	UpsertAnalyticsBudget(context.Context, auth.UserPrincipal, AnalyticsBudgetInput) (AnalyticsBudget, error)
}

var _ UsageService = (*DefaultUsageService)(nil)

// RunUsageObserver is the metrics seam: it receives one token delta for
// every usage fact the service has just inserted, and nothing for a replay.
// The telemetry Registry implements it in production.
type RunUsageObserver interface {
	ObserveRunUsageDelta(telemetry.RunUsageDeltaObservation)
}

type DefaultUsageService struct {
	store *db.Store
	// observer is optional: production wires the metrics registry so
	// /metrics counts every newly persisted token fact; nil is skipped.
	observer RunUsageObserver
}

func NewUsageService(store *db.Store, observer RunUsageObserver) (*DefaultUsageService, error) {
	if store == nil {
		return nil, errors.New("usage store is required")
	}
	return &DefaultUsageService{store: store, observer: observer}, nil
}

func (s *DefaultUsageService) ReportRunUsage(
	ctx context.Context,
	runID uuid.UUID,
	inputs []RunUsageInput,
) error {
	if s == nil || s.store == nil || ctx == nil {
		return ErrStore
	}
	// The zero UUID cannot identify a persisted Run.
	if runID == uuid.Nil {
		return ErrInvalidInput
	}
	normalized := make([]RunUsageInput, len(inputs))
	for i := range inputs {
		fact, err := normalizeRunUsageInput(inputs[i])
		if err != nil {
			return err
		}
		normalized[i] = fact
	}

	// The facts whose rows this transaction wrote, kept apart from exact
	// replays so a daemon retry of the same report is never counted twice.
	var inserted []RunUsageInput
	err := s.store.WithinRawTx(ctx, pgx.TxOptions{}, func(tx generated.DBTX, q *generated.Queries) error {
		for i := range normalized {
			fresh, err := insertImmutableRunUsage(ctx, tx, q, runID, normalized[i])
			if err != nil {
				return err
			}
			if fresh {
				inserted = append(inserted, normalized[i])
			}
		}
		return nil
	})
	if err == nil {
		s.observeInserted(inserted)
		return nil
	}
	if errors.Is(err, ErrRunUsageConflict) || errors.Is(err, ErrInvalidInput) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStore, err)
}

// observeInserted hands the committed facts to the metrics registry when one
// is wired.
func (s *DefaultUsageService) observeInserted(facts []RunUsageInput) {
	if s.observer == nil {
		return
	}
	for _, fact := range facts {
		s.observer.ObserveRunUsageDelta(telemetry.RunUsageDeltaObservation{
			Provider:         fact.Provider,
			InputTokens:      fact.InputTokens,
			OutputTokens:     fact.OutputTokens,
			CacheReadTokens:  fact.CacheReadTokens,
			CacheWriteTokens: fact.CacheWriteTokens,
		})
	}
}

func normalizeRunUsageInput(input RunUsageInput) (RunUsageInput, error) {
	// The provider is validated exactly, case-sensitively, against the
	// supported set — no lowercasing, trimming, or substitution — the same
	// rule registration applies.
	input.Model = strings.TrimSpace(input.Model)
	if input.UsageBasis == "" {
		input.UsageBasis = UsageBasisPerRun
	}
	if input.BillingMode == "" {
		input.BillingMode = BillingModeUnknown
	}
	if !protocol.IsSupportedProvider(input.Provider) || input.Model == "" ||
		len([]byte(input.Model)) > maxRunUsageModelBytes ||
		(input.UsageBasis != UsageBasisPerRun && input.UsageBasis != UsageBasisSessionCumulative) ||
		input.InputTokens < 0 || input.OutputTokens < 0 ||
		input.CacheReadTokens < 0 || input.CacheWriteTokens < 0 ||
		(input.CostUSDTicks != nil && *input.CostUSDTicks < 0) ||
		(input.ReasoningTokens != nil && *input.ReasoningTokens < 0) ||
		(input.Turns != nil && *input.Turns < 0) ||
		(input.ProviderDurationMs != nil && *input.ProviderDurationMs < 0) ||
		(input.BillingMode != BillingModeAPI && input.BillingMode != BillingModeSubscription && input.BillingMode != BillingModeUnknown) {
		return RunUsageInput{}, ErrInvalidInput
	}
	rawJSON, err := sanitizeRunUsageRawJSON(input.RawJSON)
	if err != nil {
		return RunUsageInput{}, ErrInvalidInput
	}
	input.RawJSON = rawJSON
	return input, nil
}

var retainedRawUsageKeys = map[string]struct{}{
	"input_tokens": {}, "output_tokens": {},
	"cached_input_tokens": {}, "cache_read_input_tokens": {}, "cache_read_tokens": {},
	"cache_creation_input_tokens": {}, "cache_write_tokens": {},
	"reasoning_output_tokens": {}, "cost_usd": {}, "cost_usd_ticks": {},
	"turns": {}, "duration_ms": {}, "provider_duration_ms": {},
	"inputTokens": {}, "outputTokens": {}, "cacheReadInputTokens": {},
	"cacheCreationInputTokens": {},
}

func sanitizeRunUsageRawJSON(rawJSON json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if len(rawJSON) == 0 || json.Unmarshal(rawJSON, &object) != nil || object == nil {
		return nil, errors.New("raw usage must be a JSON object")
	}
	for key := range object {
		if _, ok := retainedRawUsageKeys[key]; !ok {
			delete(object, key)
			continue
		}
		value, ok := canonicalRawUsageNumber(object[key])
		if !ok {
			delete(object, key)
			continue
		}
		object[key] = value
	}
	canonical, err := json.Marshal(object)
	// PostgreSQL's jsonb text representation inserts one space after each
	// colon and comma. Account for those bytes here so anything accepted by
	// the domain also satisfies the database's usage fact constraints.
	jsonbTextBytes := len(canonical)
	if len(object) > 0 {
		jsonbTextBytes += 2*len(object) - 1
	}
	if err != nil || jsonbTextBytes > 16*1024 {
		return nil, errors.New("raw usage exceeds the storage limit")
	}
	return canonical, nil
}

func canonicalRawUsageNumber(raw json.RawMessage) (json.RawMessage, bool) {
	if string(raw) == "null" {
		return json.RawMessage("null"), true
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return nil, false
	}
	value, err := decimal.NewFromString(number.String())
	if err != nil || value.IsNegative() {
		return nil, false
	}
	return json.RawMessage(value.String()), true
}

// insertImmutableRunUsage writes one fact and reports whether it wrote a new
// row: true on the insert path, false when the same fact was already there
// (an exact replay, which is idempotent). A different fact for the same
// (run, provider, model) is ErrRunUsageConflict.
func insertImmutableRunUsage(
	ctx context.Context,
	tx generated.DBTX,
	q *generated.Queries,
	runID uuid.UUID,
	fact RunUsageInput,
) (bool, error) {
	var costTicks pgtype.Int8
	if fact.CostUSDTicks != nil {
		costTicks = pgtype.Int8{Int64: *fact.CostUSDTicks, Valid: true}
	}
	var reasoningTokens pgtype.Int8
	if fact.ReasoningTokens != nil {
		reasoningTokens = pgtype.Int8{Int64: *fact.ReasoningTokens, Valid: true}
	}
	var turns pgtype.Int4
	if fact.Turns != nil {
		turns = pgtype.Int4{Int32: *fact.Turns, Valid: true}
	}
	var providerDurationMs pgtype.Int8
	if fact.ProviderDurationMs != nil {
		providerDurationMs = pgtype.Int8{Int64: *fact.ProviderDurationMs, Valid: true}
	}
	_, err := q.InsertRunUsage(ctx, generated.InsertRunUsageParams{
		RunID:              runID,
		Provider:           fact.Provider,
		Model:              fact.Model,
		InputTokens:        fact.InputTokens,
		OutputTokens:       fact.OutputTokens,
		CacheReadTokens:    fact.CacheReadTokens,
		CacheWriteTokens:   fact.CacheWriteTokens,
		UsageBasis:         fact.UsageBasis,
		CostUsdTicks:       costTicks,
		RawJson:            []byte(fact.RawJSON),
		ReasoningTokens:    reasoningTokens,
		Turns:              turns,
		ProviderDurationMs: providerDurationMs,
		BillingMode:        fact.BillingMode,
	})
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("insert run usage: %w", err)
	}

	// ON CONFLICT DO NOTHING returned no row: a fact already exists for this
	// (run, provider, model). An exact replay — usage basis included, it is
	// part of the fact — is idempotent; anything else conflicts.
	var unchanged bool
	err = tx.QueryRow(
		ctx, `
		SELECT input_tokens = $4
		   AND output_tokens = $5
		   AND cache_read_tokens = $6
		   AND cache_write_tokens = $7
		   AND provider_cost_usd IS NOT DISTINCT FROM ($8::bigint * 0.0000000001)
		   AND raw_json = $9::jsonb
		   AND usage_basis = $10
		   AND reasoning_tokens IS NOT DISTINCT FROM $11::bigint
		   AND turns IS NOT DISTINCT FROM $12::integer
		   AND provider_duration_ms IS NOT DISTINCT FROM $13::bigint
		   AND billing_mode = $14
		FROM run_usage
		WHERE run_id = $1 AND provider = $2 AND model = $3`,
		runID, fact.Provider, fact.Model, fact.InputTokens, fact.OutputTokens,
		fact.CacheReadTokens, fact.CacheWriteTokens, fact.CostUSDTicks, []byte(fact.RawJSON),
		fact.UsageBasis, fact.ReasoningTokens, fact.Turns, fact.ProviderDurationMs, fact.BillingMode,
	).Scan(&unchanged)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errors.New("run usage disappeared during conflict resolution")
	}
	if err != nil {
		return false, fmt.Errorf("read existing run usage: %w", err)
	}
	if !unchanged {
		return false, ErrRunUsageConflict
	}
	return false, nil
}

func (s *DefaultUsageService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	period Period,
) (AgentUsageReport, error) {
	if agentID == uuid.Nil || !validPeriod(period) {
		return AgentUsageReport{}, ErrInvalidInput
	}
	if s == nil || s.store == nil || ctx == nil {
		return AgentUsageReport{}, ErrStore
	}

	report := AgentUsageReport{
		PeriodStartAt:                  period.StartAt,
		PeriodEndAt:                    period.EndAt,
		TimeZone:                       period.TimeZone,
		Bucket:                         period.Bucket,
		Series:                         []SeriesPoint{},
		Models:                         []ModelSlice{},
		TopTasks:                       []TopTaskItem{},
		TopTasksTotalInputOutputTokens: "0",
	}
	err := s.store.WithinRawTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	}, func(tx generated.DBTX, _ *generated.Queries) error {
		if err := authorizeUsageOwner(ctx, tx, principal); err != nil {
			return err
		}
		if err := authorizeUsageAgent(ctx, tx, principal.CompanyID, agentID); err != nil {
			return err
		}
		if err := readUsageTotals(ctx, tx, principal, agentID, period, &report); err != nil {
			return err
		}
		if err := readUsageSeries(ctx, tx, principal, agentID, period, &report); err != nil {
			return err
		}
		if err := readUsageModels(ctx, tx, principal, &agentID, period, &report.Models); err != nil {
			return err
		}
		return readUsageTopTasks(ctx, tx, principal, agentID, period, &report)
	})
	if err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidInput) {
			return AgentUsageReport{}, err
		}
		return AgentUsageReport{}, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return report, nil
}

func (s *DefaultUsageService) GetCompany(
	ctx context.Context,
	principal auth.UserPrincipal,
	selection CompanyPeriod,
) (CompanyUsageReport, error) {
	if !validCompanyPeriod(selection) {
		return CompanyUsageReport{}, ErrInvalidInput
	}
	if s == nil || s.store == nil || ctx == nil {
		return CompanyUsageReport{}, ErrStore
	}

	var report CompanyUsageReport
	err := s.store.WithinRawTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	}, func(tx generated.DBTX, _ *generated.Queries) error {
		if err := authorizeUsageOwner(ctx, tx, principal); err != nil {
			return err
		}
		var asOf time.Time
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()::timestamptz`).Scan(&asOf); err != nil {
			return fmt.Errorf("read company usage clock: %w", err)
		}
		period, comparison, err := companyCalendarPeriods(selection, asOf)
		if err != nil {
			return err
		}
		report = CompanyUsageReport{
			PeriodStartAt: period.StartAt, PeriodEndAt: period.EndAt,
			ComparisonStartAt: comparison.StartAt, ComparisonEndAt: comparison.EndAt,
			TimeZone: selection.TimeZone,
			Totals:   zeroCompanyTotals(), ComparisonTotals: zeroCompanyTotals(),
			Agents: []AgentUsage{}, Models: []ModelSlice{},
		}
		if err := readCompanyUsageTotals(ctx, tx, principal, period, &report.Totals); err != nil {
			return err
		}
		if err := readCompanyUsageTotals(ctx, tx, principal, comparison, &report.ComparisonTotals); err != nil {
			return err
		}
		if err := readCompanyUsageAgents(ctx, tx, principal, period, &report.Agents); err != nil {
			return err
		}
		return readUsageModels(ctx, tx, principal, nil, period, &report.Models)
	})
	if err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrInvalidInput) {
			return CompanyUsageReport{}, err
		}
		return CompanyUsageReport{}, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return report, nil
}

func validCompanyPeriod(period CompanyPeriod) bool {
	validType := period.Type == CompanyPeriodWeek ||
		period.Type == CompanyPeriodMonth || period.Type == CompanyPeriodYear
	if !validType || period.TimeZone == "Local" {
		return false
	}
	_, err := time.LoadLocation(period.TimeZone)
	return err == nil
}

func validPeriod(period Period) bool {
	location, err := time.LoadLocation(period.TimeZone)
	if err != nil || (period.Bucket != BucketDay && period.Bucket != BucketMonth) ||
		!period.EndAt.After(period.StartAt) {
		return false
	}
	start := period.StartAt.In(location)
	end := period.EndAt.In(location)
	if start.Hour() != 0 || start.Minute() != 0 || start.Second() != 0 || start.Nanosecond() != 0 ||
		end.Hour() != 0 || end.Minute() != 0 || end.Second() != 0 || end.Nanosecond() != 0 {
		return false
	}
	return !period.EndAt.After(start.AddDate(0, 0, 367))
}

func authorizeUsageOwner(ctx context.Context, tx generated.DBTX, principal auth.UserPrincipal) error {
	var role, status string
	err := tx.QueryRow(ctx, `
		SELECT members.role, members.status
		FROM companies
		JOIN company_members AS members ON members.company_id = companies.id
		WHERE companies.id = $1 AND companies.deleting_at IS NULL AND members.user_id = $2`,
		principal.CompanyID, principal.UserID).Scan(&role, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("authorize usage report: %w", err)
	}
	if !auth.CompanyRole(role).Can(auth.PermissionUsageRead) || role != string(principal.Role) ||
		status != "active" {
		return ErrForbidden
	}
	return nil
}

func authorizeUsageAgent(
	ctx context.Context,
	tx generated.DBTX,
	companyID, agentID uuid.UUID,
) error {
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1 AND company_id = $2)`,
		agentID, companyID).Scan(&exists); err != nil {
		return fmt.Errorf("authorize usage agent: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// visibleRunsCTE is the window every usage reader aggregates over. Usage is a
// company-wide account read under usage.read: token and run counts of a
// member's private chat reveal nothing of the chat itself, so the creator
// rule that scopes the Run list (ADR 0008) does not apply here.
const visibleRunsCTE = `
	WITH visible_runs AS MATERIALIZED (
		SELECT runs.id, runs.agent_id, runs.task_id, runs.created_at,
		       conversations.kind AS conversation_kind
		FROM runs
		JOIN conversations ON conversations.id = runs.conversation_id
		WHERE runs.company_id = $1
		  AND ($2::uuid IS NULL OR runs.agent_id = $2)
		  AND runs.created_at >= $3 AND runs.created_at < $4
	)`

func readUsageTotals(
	ctx context.Context,
	tx generated.DBTX,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	period Period,
	report *AgentUsageReport,
) error {
	var tokens Tokens
	var runCount, costTicks, reported, missing string
	err := tx.QueryRow(
		ctx, visibleRunsCTE+`
		SELECT
			COALESCE(sum(run_usage.input_tokens), 0)::text,
			COALESCE(sum(run_usage.output_tokens), 0)::text,
			COALESCE(sum(run_usage.cache_read_tokens), 0)::text,
			COALESCE(sum(run_usage.cache_write_tokens), 0)::text,
			count(DISTINCT visible_runs.id)::text,
			COALESCE(trunc(sum(run_usage.provider_cost_usd / 0.0000000001)), 0)::text,
			count(run_usage.provider_cost_usd)::text,
			(count(run_usage.run_id) - count(run_usage.provider_cost_usd))::text
		FROM visible_runs
		LEFT JOIN run_usage ON run_usage.run_id = visible_runs.id`,
		principal.CompanyID, agentID, period.StartAt, period.EndAt,
	).Scan(
		&tokens.InputTokens, &tokens.OutputTokens,
		&tokens.CacheReadTokens, &tokens.CacheWriteTokens, &runCount,
		&costTicks, &reported, &missing,
	)
	if err != nil {
		return fmt.Errorf("read usage totals: %w", err)
	}
	attribution, err := costAttributionFromStrings(reported, missing, costTicks)
	if err != nil {
		return err
	}
	report.Totals = Totals{Tokens: tokens, CostAttribution: attribution, RunCount: runCount}
	return nil
}

type seriesAccumulator struct {
	point                        SeriesPoint
	models                       []SeriesModel
	costTicks, reported, missing string
}

func readUsageSeries(
	ctx context.Context,
	tx generated.DBTX,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	period Period,
	report *AgentUsageReport,
) error {
	accumulators, order, err := emptySeries(period)
	if err != nil {
		return err
	}
	rows, err := tx.Query(
		ctx, visibleRunsCTE+`
		SELECT
			(date_trunc($6, visible_runs.created_at AT TIME ZONE $7) AT TIME ZONE $7),
			run_usage.provider, run_usage.model,
			COALESCE(sum(run_usage.input_tokens), 0)::text,
			COALESCE(sum(run_usage.output_tokens), 0)::text,
			COALESCE(sum(run_usage.cache_read_tokens), 0)::text,
			COALESCE(sum(run_usage.cache_write_tokens), 0)::text,
			COALESCE(trunc(sum(run_usage.provider_cost_usd / 0.0000000001)), 0)::text,
			count(run_usage.provider_cost_usd)::text,
			(count(*) - count(run_usage.provider_cost_usd))::text
		FROM visible_runs
		JOIN run_usage ON run_usage.run_id = visible_runs.id
		GROUP BY 1, run_usage.provider, run_usage.model
		ORDER BY 1, sum(run_usage.input_tokens::numeric + run_usage.output_tokens::numeric) DESC,
		         run_usage.provider, run_usage.model`,
		principal.CompanyID, agentID, period.StartAt, period.EndAt,
		string(period.Bucket), period.TimeZone,
	)
	if err != nil {
		return fmt.Errorf("read usage series: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bucketAt time.Time
		var model SeriesModel
		var costTicks, reported, missing string
		if err := rows.Scan(
			&bucketAt, &model.Provider, &model.Model,
			&model.InputTokens, &model.OutputTokens,
			&model.CacheReadTokens, &model.CacheWriteTokens,
			&costTicks, &reported, &missing,
		); err != nil {
			return fmt.Errorf("scan usage series: %w", err)
		}
		accumulator := accumulators[bucketAt.UTC()]
		if accumulator == nil {
			return errors.New("usage series returned an out-of-period bucket")
		}
		if err := addTokens(&accumulator.point.Tokens, model.Tokens); err != nil {
			return err
		}
		for target, value := range map[*string]string{
			&accumulator.costTicks: costTicks,
			&accumulator.reported:  reported,
			&accumulator.missing:   missing,
		} {
			if err := addDecimalString(target, value); err != nil {
				return err
			}
		}
		accumulator.models = append(accumulator.models, model)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate usage series: %w", err)
	}

	runRows, err := tx.Query(
		ctx, visibleRunsCTE+`
		SELECT (date_trunc($6, created_at AT TIME ZONE $7) AT TIME ZONE $7), count(*)::text
		FROM visible_runs GROUP BY 1 ORDER BY 1`,
		principal.CompanyID, agentID, period.StartAt, period.EndAt,
		string(period.Bucket), period.TimeZone,
	)
	if err != nil {
		return fmt.Errorf("read usage run series: %w", err)
	}
	defer runRows.Close()
	for runRows.Next() {
		var bucketAt time.Time
		var count string
		if err := runRows.Scan(&bucketAt, &count); err != nil {
			return fmt.Errorf("scan usage run series: %w", err)
		}
		if accumulator := accumulators[bucketAt.UTC()]; accumulator != nil {
			accumulator.point.RunCount = count
		}
	}
	if err := runRows.Err(); err != nil {
		return fmt.Errorf("iterate usage run series: %w", err)
	}

	for _, startAt := range order {
		accumulator := accumulators[startAt]
		attribution, err := costAttributionFromStrings(
			accumulator.reported, accumulator.missing, accumulator.costTicks,
		)
		if err != nil {
			return err
		}
		accumulator.point.CostAttribution = attribution
		accumulator.point.Models = accumulator.models
		report.Series = append(report.Series, accumulator.point)
	}
	return nil
}

func emptySeries(period Period) (map[time.Time]*seriesAccumulator, []time.Time, error) {
	location, err := time.LoadLocation(period.TimeZone)
	if err != nil {
		return nil, nil, ErrInvalidInput
	}
	result := make(map[time.Time]*seriesAccumulator)
	order := make([]time.Time, 0)
	for local := period.StartAt.In(location); local.Before(period.EndAt); {
		next := local.AddDate(0, 0, 1)
		if period.Bucket == BucketMonth {
			next = local.AddDate(0, 1, 0)
		}
		startAt, endAt := local.UTC(), next.UTC()
		result[startAt] = &seriesAccumulator{
			point: SeriesPoint{
				Tokens: zeroUsageTokens(), StartAt: startAt, EndAt: endAt, RunCount: "0",
				Models: []SeriesModel{},
			},
			models: []SeriesModel{}, costTicks: "0", reported: "0", missing: "0",
		}
		order = append(order, startAt)
		local = next
	}
	return result, order, nil
}

func readUsageModels(
	ctx context.Context,
	tx generated.DBTX,
	principal auth.UserPrincipal,
	agentID *uuid.UUID,
	period Period,
	target *[]ModelSlice,
) error {
	var agent any
	if agentID != nil {
		agent = *agentID
	}
	rows, err := tx.Query(
		ctx, visibleRunsCTE+`
		SELECT run_usage.provider, run_usage.model,
		       COALESCE(sum(run_usage.input_tokens), 0)::text,
		       COALESCE(sum(run_usage.output_tokens), 0)::text,
		       COALESCE(sum(run_usage.cache_read_tokens), 0)::text,
		       COALESCE(sum(run_usage.cache_write_tokens), 0)::text,
		       COALESCE(trunc(sum(run_usage.provider_cost_usd / 0.0000000001)), 0)::text,
		       count(run_usage.provider_cost_usd)::text,
		       (count(*) - count(run_usage.provider_cost_usd))::text
		FROM visible_runs
		JOIN run_usage ON run_usage.run_id = visible_runs.id
		GROUP BY run_usage.provider, run_usage.model
		ORDER BY sum(run_usage.input_tokens::numeric + run_usage.output_tokens::numeric) DESC,
		         run_usage.provider, run_usage.model`,
		principal.CompanyID, agent, period.StartAt, period.EndAt,
	)
	if err != nil {
		return fmt.Errorf("read usage models: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item ModelSlice
		var costTicks, reported, missing string
		if err := rows.Scan(
			&item.Provider, &item.Model,
			&item.InputTokens, &item.OutputTokens,
			&item.CacheReadTokens, &item.CacheWriteTokens,
			&costTicks, &reported, &missing,
		); err != nil {
			return fmt.Errorf("scan usage model: %w", err)
		}
		attribution, err := costAttributionFromStrings(reported, missing, costTicks)
		if err != nil {
			return err
		}
		item.CostAttribution = attribution
		*target = append(*target, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate usage models: %w", err)
	}
	return nil
}

func readUsageTopTasks(
	ctx context.Context,
	tx generated.DBTX,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	period Period,
	report *AgentUsageReport,
) error {
	if err := tx.QueryRow(
		ctx, visibleRunsCTE+`
		SELECT COALESCE(sum(run_usage.input_tokens::numeric + run_usage.output_tokens::numeric), 0)::text
		FROM visible_runs
		JOIN run_usage ON run_usage.run_id = visible_runs.id
		WHERE visible_runs.conversation_kind = 'task'`,
		principal.CompanyID, agentID, period.StartAt, period.EndAt,
	).Scan(&report.TopTasksTotalInputOutputTokens); err != nil {
		return fmt.Errorf("read task usage total: %w", err)
	}
	// The share above counts every Task Run's tokens, which name no Task. The
	// list names Tasks, so it shows only Tasks in Projects the caller reads
	// (ADR 0017): usage.read is a Company permission and opens no content.
	rows, err := tx.Query(
		ctx, visibleRunsCTE+`
		SELECT tasks.id, tasks.key, tasks.number, tasks.title, tasks.status,
		       COALESCE(sum(run_usage.input_tokens), 0)::text,
		       COALESCE(sum(run_usage.output_tokens), 0)::text,
		       COALESCE(sum(run_usage.input_tokens::numeric + run_usage.output_tokens::numeric), 0)::text
		FROM visible_runs
		JOIN tasks ON tasks.id = visible_runs.task_id
		JOIN project_access
		  ON project_access.company_id = tasks.company_id
		 AND project_access.project_id = tasks.project_id
		 AND project_access.user_id = $5
		JOIN run_usage ON run_usage.run_id = visible_runs.id
		GROUP BY tasks.id, tasks.key, tasks.number, tasks.title, tasks.status
		HAVING sum(run_usage.input_tokens::numeric + run_usage.output_tokens::numeric) > 0
		ORDER BY sum(run_usage.input_tokens::numeric + run_usage.output_tokens::numeric) DESC,
		         tasks.id
		LIMIT 4`,
		principal.CompanyID, agentID, period.StartAt, period.EndAt, principal.UserID,
	)
	if err != nil {
		return fmt.Errorf("read top tasks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var taskID uuid.UUID
		var item TopTaskItem
		// The per-task input+output sum stays a scan-local numerator for the
		// share computation; the wire carries the two counters and the share.
		var inputOutputTokens string
		if err := rows.Scan(
			&taskID, &item.TaskKey, &item.TaskNumber, &item.Title, &item.Status,
			&item.InputTokens, &item.OutputTokens, &inputOutputTokens,
		); err != nil {
			return fmt.Errorf("scan top task: %w", err)
		}
		item.TaskID = taskID.String()
		share, err := basisPoints(inputOutputTokens, report.TopTasksTotalInputOutputTokens)
		if err != nil {
			return err
		}
		item.ShareBasisPoints = share
		report.TopTasks = append(report.TopTasks, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate top tasks: %w", err)
	}
	return nil
}

func readCompanyUsageTotals(
	ctx context.Context,
	tx generated.DBTX,
	principal auth.UserPrincipal,
	period Period,
	target *CompanyTotals,
) error {
	var agentID *uuid.UUID
	err := tx.QueryRow(
		ctx, visibleRunsCTE+`
		SELECT COALESCE(sum(run_usage.input_tokens), 0)::text,
		       COALESCE(sum(run_usage.output_tokens), 0)::text,
		       COALESCE(sum(run_usage.cache_read_tokens), 0)::text,
		       COALESCE(sum(run_usage.cache_write_tokens), 0)::text,
		       count(DISTINCT visible_runs.id)::text
		FROM visible_runs
		LEFT JOIN run_usage ON run_usage.run_id = visible_runs.id`,
		principal.CompanyID, agentID, period.StartAt, period.EndAt,
	).Scan(
		&target.InputTokens, &target.OutputTokens,
		&target.CacheReadTokens, &target.CacheWriteTokens, &target.RunCount,
	)
	if err != nil {
		return fmt.Errorf("read company usage totals: %w", err)
	}
	return nil
}

func readCompanyUsageAgents(
	ctx context.Context,
	tx generated.DBTX,
	principal auth.UserPrincipal,
	period Period,
	target *[]AgentUsage,
) error {
	var agentID *uuid.UUID
	rows, err := tx.Query(
		ctx, visibleRunsCTE+`
		SELECT agents.id, agents.name, agents.title,
		       COALESCE(agents.avatar_key, ''),
		       COALESCE(sum(run_usage.input_tokens), 0)::text,
		       COALESCE(sum(run_usage.output_tokens), 0)::text,
		       COALESCE(sum(run_usage.cache_read_tokens), 0)::text,
		       COALESCE(sum(run_usage.cache_write_tokens), 0)::text,
		       count(DISTINCT visible_runs.id)::text
		FROM visible_runs
		JOIN agents ON agents.id = visible_runs.agent_id AND agents.archived_at IS NULL
		LEFT JOIN run_usage ON run_usage.run_id = visible_runs.id
		GROUP BY agents.id, agents.name, agents.title, agents.avatar_key, agents.reports_to_agent_id
		ORDER BY COALESCE(sum(run_usage.input_tokens::numeric + run_usage.output_tokens::numeric), 0) DESC,
		         (agents.reports_to_agent_id IS NULL) DESC, lower(agents.name), agents.id`,
		principal.CompanyID, agentID, period.StartAt, period.EndAt,
	)
	if err != nil {
		return fmt.Errorf("read company usage agents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item AgentUsage
		var avatarKey string
		if err := rows.Scan(
			&item.AgentID, &item.Name, &item.Title, &avatarKey,
			&item.InputTokens, &item.OutputTokens,
			&item.CacheReadTokens, &item.CacheWriteTokens, &item.RunCount,
		); err != nil {
			return fmt.Errorf("scan company usage agent: %w", err)
		}
		if avatarKey != "" {
			item.AvatarKey = &avatarKey
		}
		*target = append(*target, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate company usage agents: %w", err)
	}
	return nil
}

func zeroCompanyTotals() CompanyTotals {
	return CompanyTotals{Tokens: zeroUsageTokens(), RunCount: "0"}
}

func zeroUsageTokens() Tokens {
	return Tokens{
		InputTokens: "0", OutputTokens: "0",
		CacheReadTokens: "0", CacheWriteTokens: "0",
	}
}

func addTokens(target *Tokens, value Tokens) error {
	for _, pair := range [][2]*string{
		{&target.InputTokens, &value.InputTokens},
		{&target.OutputTokens, &value.OutputTokens},
		{&target.CacheReadTokens, &value.CacheReadTokens},
		{&target.CacheWriteTokens, &value.CacheWriteTokens},
	} {
		if err := addDecimalString(pair[0], *pair[1]); err != nil {
			return err
		}
	}
	return nil
}

func addDecimalString(target *string, value string) error {
	left, ok := new(big.Int).SetString(*target, 10)
	if !ok {
		return errors.New("parse accumulated usage counter")
	}
	right, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return errors.New("parse usage counter")
	}
	*target = left.Add(left, right).String()
	return nil
}

func costAttributionFromStrings(reported, missing, ticks string) (CostAttribution, error) {
	reportedValue, ok := new(big.Int).SetString(reported, 10)
	if !ok {
		return CostAttribution{}, errors.New("parse reported provider cost count")
	}
	missingValue, ok := new(big.Int).SetString(missing, 10)
	if !ok {
		return CostAttribution{}, errors.New("parse missing provider cost count")
	}
	if _, ok := new(big.Int).SetString(ticks, 10); !ok {
		return CostAttribution{}, errors.New("parse provider cost")
	}
	state := CostCoverageMissing
	if reportedValue.Sign() > 0 && missingValue.Sign() == 0 {
		state = CostCoverageComplete
	} else if reportedValue.Sign() > 0 {
		state = CostCoveragePartial
	}
	var providerCostTicks *string
	if reportedValue.Sign() > 0 {
		providerCostTicks = &ticks
	}
	return CostAttribution{
		ProviderCostUSDTicks:  providerCostTicks,
		CostCoverageState:     state,
		CostReportedFactCount: reported,
		CostMissingFactCount:  missing,
	}, nil
}

func basisPoints(numerator, denominator string) (int, error) {
	n, ok := new(big.Int).SetString(numerator, 10)
	if !ok {
		return 0, errors.New("parse task usage numerator")
	}
	d, ok := new(big.Int).SetString(denominator, 10)
	if !ok {
		return 0, errors.New("parse task usage denominator")
	}
	if d.Sign() == 0 {
		return 0, nil
	}
	value := new(big.Int).Mul(n, big.NewInt(10000))
	value.Add(value, new(big.Int).Quo(new(big.Int).Set(d), big.NewInt(2)))
	value.Quo(value, d)
	return int(value.Int64()), nil
}
