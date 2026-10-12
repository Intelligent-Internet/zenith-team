package schedule

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestComputeReviewDigestIsDeterministicAndBindsFrozenSources(t *testing.T) {
	t.Parallel()
	agentID := uuid.Must(uuid.NewV7())
	connectorID := uuid.Must(uuid.NewV7())
	expression := "0 8 * * *"
	timeZone := "UTC"
	preview := PreviewResult{
		AgentID: agentID, ReviewedProvider: "codex", Name: "Daily brief",
		Instructions: "Summarize.", Trigger: Trigger{
			Type: TriggerTypeCron, Expression: &expression, TimeZone: &timeZone,
		},
		Sources: []Source{{
			ConnectorID: connectorID, PolicyRevision: 7, Provider: "codex",
			Source: "codex_app", NativeID: "gmail",
			ReviewedContractDigest: scheduleTestDigest, Position: 0,
		}},
	}
	first, err := computeReviewDigest(preview)
	if err != nil {
		t.Fatalf("computeReviewDigest() error = %v", err)
	}
	second, err := computeReviewDigest(preview)
	if err != nil || first != second || !digestPattern.MatchString(first) {
		t.Fatalf("repeat digest = %q, %v, want %q", second, err, first)
	}
	preview.Sources[0].PolicyRevision++
	changed, err := computeReviewDigest(preview)
	if err != nil {
		t.Fatalf("computeReviewDigest(changed policy) error = %v", err)
	}
	if changed == first {
		t.Fatal("review digest did not bind the source policy revision")
	}
}

func TestEncodeSourcesUsesCamelCase(t *testing.T) {
	t.Parallel()

	companyID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	connectorID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	encoded, err := encodeSources(companyID, []Source{{
		ConnectorID: connectorID, PolicyRevision: 7, Provider: "codex",
		Source: "codex_app", NativeID: "gmail",
		ReviewedContractDigest: scheduleTestDigest,
	}})
	if err != nil {
		t.Fatalf("encodeSources() error = %v", err)
	}
	want := `[{"connectorId":"22222222-2222-4222-8222-222222222222",` +
		`"companyId":"11111111-1111-4111-8111-111111111111","provider":"codex",` +
		`"source":"codex_app","nativeId":"gmail",` +
		`"reviewedContractDigest":"` + scheduleTestDigest + `",` +
		`"policyRevision":7,"position":0}]`
	if string(encoded) != want {
		t.Fatalf("encoded Schedule sources = %s, want %s", encoded, want)
	}
}

func TestNormalizeRunNowInputRequiresRequestKey(t *testing.T) {
	t.Parallel()
	if _, err := normalizeRunNowInput(RunNowInput{}); err == nil {
		t.Fatal("normalizeRunNowInput(empty request key) error = nil")
	}
	valid := RunNowInput{RequestKey: uuid.Must(uuid.NewV7())}
	if _, err := normalizeRunNowInput(valid); err != nil {
		t.Fatalf("normalizeRunNowInput(valid) error = %v", err)
	}
}

func TestOneShotPreviewRequiresMinutePrecision(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, time.August, 17, 0, 0, 0, 0, time.UTC)
	runAt := after.Add(time.Minute)
	if _, err := PreviewTrigger(Trigger{Type: TriggerTypeOnce, RunAt: &runAt}, after, 1); err != nil {
		t.Fatalf("PreviewTrigger(minute precision) error = %v", err)
	}
}
