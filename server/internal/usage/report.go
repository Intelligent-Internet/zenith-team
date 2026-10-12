package usage

import (
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Tokens carries the four stored counters; input+output is the client's sum,
// never a served field.
type Tokens struct {
	InputTokens      string `json:"inputTokens"`
	OutputTokens     string `json:"outputTokens"`
	CacheReadTokens  string `json:"cacheReadTokens"`
	CacheWriteTokens string `json:"cacheWriteTokens"`
}

type CostCoverageState string

const (
	CostCoverageComplete CostCoverageState = "complete"
	CostCoveragePartial  CostCoverageState = "partial"
	CostCoverageMissing  CostCoverageState = "missing"
)

// CostAttribution is flattened into the token slice it qualifies on the wire.
type CostAttribution struct {
	ProviderCostUSDTicks  *string           `json:"providerCostUsdTicks"`
	CostCoverageState     CostCoverageState `json:"costCoverageState"`
	CostReportedFactCount string            `json:"costReportedFactCount"`
	CostMissingFactCount  string            `json:"costMissingFactCount"`
}

type Totals struct {
	Tokens
	CostAttribution
	RunCount string `json:"runCount"`
}

type SeriesModel struct {
	Tokens
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type SeriesPoint struct {
	Tokens
	CostAttribution
	StartAt  time.Time     `json:"startAt"`
	EndAt    time.Time     `json:"endAt"`
	RunCount string        `json:"runCount"`
	Models   []SeriesModel `json:"models"`
}

type ModelSlice struct {
	Tokens
	CostAttribution
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type TopTaskItem struct {
	TaskID  string `json:"taskId"`
	TaskKey string `json:"taskKey"`
	// TaskNumber is what every surface displays as {taskPrefix}-{number}.
	TaskNumber       int64  `json:"taskNumber"`
	Title            string `json:"title"`
	Status           string `json:"status"`
	InputTokens      string `json:"inputTokens"`
	OutputTokens     string `json:"outputTokens"`
	ShareBasisPoints int    `json:"shareBasisPoints"`
}

type AgentUsageReport struct {
	PeriodStartAt                  time.Time     `json:"periodStartAt"`
	PeriodEndAt                    time.Time     `json:"periodEndAt"`
	TimeZone                       string        `json:"timeZone"`
	Bucket                         Bucket        `json:"bucket"`
	Totals                         Totals        `json:"totals"`
	Series                         []SeriesPoint `json:"series"`
	Models                         []ModelSlice  `json:"models"`
	TopTasks                       []TopTaskItem `json:"topTasks"`
	TopTasksTotalInputOutputTokens string        `json:"topTasksTotalInputOutputTokens"`
}

type CompanyTotals struct {
	Tokens
	RunCount string `json:"runCount"`
}

type AgentUsage struct {
	CompanyTotals
	AgentID   uuid.UUID `json:"agentId"`
	Name      string    `json:"name"`
	Title     string    `json:"title"`
	AvatarKey *string   `json:"avatarKey"`
}

type CompanyUsageReport struct {
	PeriodStartAt     time.Time     `json:"periodStartAt"`
	PeriodEndAt       time.Time     `json:"periodEndAt"`
	ComparisonStartAt time.Time     `json:"comparisonStartAt"`
	ComparisonEndAt   time.Time     `json:"comparisonEndAt"`
	TimeZone          string        `json:"timeZone"`
	Totals            CompanyTotals `json:"totals"`
	ComparisonTotals  CompanyTotals `json:"comparisonTotals"`
	Agents            []AgentUsage  `json:"agents"`
	Models            []ModelSlice  `json:"models"`
}

type ActivityInsight struct {
	MostUsedProvider *string    `json:"mostUsedProvider"`
	MostUsedModel    *string    `json:"mostUsedModel"`
	SuccessfulTasks  string     `json:"successfulTasks"`
	TotalSessions    string     `json:"totalSessions"`
	CompletedRuns    string     `json:"completedRuns"`
	FailedRuns       string     `json:"failedRuns"`
	TotalRuns        string     `json:"totalRuns"`
	LastActiveAt     *time.Time `json:"lastActiveAt"`
}

func newCostAttribution(reported, missing int64, ticks *string) (CostAttribution, error) {
	if reported < 0 || missing < 0 {
		return CostAttribution{}, errors.New("negative provider cost coverage")
	}
	state := CostCoverageMissing
	if reported > 0 && missing == 0 {
		state = CostCoverageComplete
	} else if reported > 0 {
		state = CostCoveragePartial
	}
	return CostAttribution{
		ProviderCostUSDTicks:  ticks,
		CostCoverageState:     state,
		CostReportedFactCount: strconv.FormatInt(reported, 10),
		CostMissingFactCount:  strconv.FormatInt(missing, 10),
	}, nil
}
