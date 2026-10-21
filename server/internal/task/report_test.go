package task

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestBuildTaskReportMessageLeavesOverflowRowsPending(t *testing.T) {
	t.Parallel()

	rows := make([]generated.TaskReportEvent, 200)
	for index := range rows {
		rows[index] = generated.TaskReportEvent{
			ID:        uuid.Must(uuid.NewV7()),
			TaskID:    uuid.Must(uuid.NewV7()),
			TaskKey:   "TASK-" + strings.Repeat("K", 32),
			TaskTitle: strings.Repeat("T", 128),
			CompletedAt: pgtype.Timestamptz{
				Time:  time.Unix(int64(index+1), 0).UTC(),
				Valid: true,
			},
			ResultSummary: strings.Repeat("S", maxReportSummaryRunes),
		}
	}

	content, included, err := buildTaskReportMessage(uuid.Must(uuid.NewV7()), rows)
	if err != nil {
		t.Fatalf("buildTaskReportMessage() error = %v", err)
	}
	if included < 1 || included >= len(rows) {
		t.Fatalf("included rows = %d, want between 1 and %d", included, len(rows)-1)
	}
	if strings.Contains(content, "<omitted") {
		t.Fatal("buildTaskReportMessage() encoded unsent rows as delivered omissions")
	}
	if len(content) > maxReportMessageBytes {
		t.Fatalf("message bytes = %d, want at most %d", len(content), maxReportMessageBytes)
	}
}

func TestBuildTaskReportMessageDistinguishesDoneAndFailedOutcomes(t *testing.T) {
	t.Parallel()

	completedAt := time.Unix(1, 0).UTC()
	rows := []generated.TaskReportEvent{
		{
			ID:               uuid.Must(uuid.NewV7()),
			TaskID:           uuid.Must(uuid.NewV7()),
			TaskKey:          "TASK-1",
			TaskTitle:        "Completed Task",
			CompletionSource: "review_accepted",
			CompletedAt:      pgtype.Timestamptz{Time: completedAt, Valid: true},
			ResultSummary:    "Delivered",
		},
		{
			ID:               uuid.Must(uuid.NewV7()),
			TaskID:           uuid.Must(uuid.NewV7()),
			TaskKey:          "TASK-2",
			TaskTitle:        "Failed Task",
			CompletionSource: "agent_business_failure",
			CompletedAt:      pgtype.Timestamptz{Time: completedAt, Valid: true},
			ResultSummary:    "Contract requires <forbidden> delivery",
		},
	}

	content, included, err := buildTaskReportMessage(uuid.Must(uuid.NewV7()), rows)
	if err != nil {
		t.Fatalf("buildTaskReportMessage() error = %v", err)
	}
	if included != len(rows) {
		t.Fatalf("included rows = %d, want %d", included, len(rows))
	}
	for _, fragment := range []string{
		`<task-updates version="2"`,
		`<outcome>done</outcome>`,
		`<outcome>failed</outcome>`,
		`<result-summary>Contract requires &lt;forbidden&gt; delivery</result-summary>`,
	} {
		if !strings.Contains(content, fragment) {
			t.Errorf("Task report content missing %q: %s", fragment, content)
		}
	}
}
