package usage

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestActivityInsightUsesTheDocumentedFlatWireShape(t *testing.T) {
	t.Parallel()
	const document = `{
		"mostUsedProvider":"codex",
		"mostUsedModel":"gpt-5.6-sol",
		"successfulTasks":"87",
		"totalSessions":"214",
		"completedRuns":"289",
		"failedRuns":"31",
		"totalRuns":"326",
		"lastActiveAt":"2026-08-16T08:41:19.220417Z"
	}`
	var insight ActivityInsight
	if err := json.Unmarshal([]byte(document), &insight); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(insight)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(document), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ActivityInsight JSON = %#v, want %#v", got, want)
	}
}

func TestNewCostAttributionPreservesCoverageStates(t *testing.T) {
	t.Parallel()
	zero := "0"
	tests := []struct {
		name              string
		reported, missing int64
		ticks             *string
		wantState         CostCoverageState
	}{
		{name: "empty is missing", wantState: CostCoverageMissing},
		{name: "missing", missing: 2, wantState: CostCoverageMissing},
		{name: "partial", reported: 1, missing: 2, ticks: &zero, wantState: CostCoveragePartial},
		{name: "complete including reported zero", reported: 3, ticks: &zero, wantState: CostCoverageComplete},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := newCostAttribution(tt.reported, tt.missing, tt.ticks)
			if err != nil {
				t.Fatalf("newCostAttribution() error = %v", err)
			}
			if got.CostCoverageState != tt.wantState || got.ProviderCostUSDTicks != tt.ticks {
				t.Fatalf("newCostAttribution() = %+v", got)
			}
		})
	}
}

func TestNewCostAttributionRejectsNegativeStoreData(t *testing.T) {
	t.Parallel()
	if _, err := newCostAttribution(-1, 0, nil); err == nil {
		t.Fatal("newCostAttribution(-1, 0) succeeded")
	}
	if _, err := newCostAttribution(0, -1, nil); err == nil {
		t.Fatal("newCostAttribution(0, -1) succeeded")
	}
}
