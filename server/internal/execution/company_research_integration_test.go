//go:build integration

package execution

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	db "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/runfailure"
)

// seedResearchReceipt makes the running Run the founder's research Run.
func seedResearchReceipt(t *testing.T, h *runHarness, runID uuid.UUID) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(), `
		INSERT INTO company_initializations (company_id, created_by_user_id, conversation_id, research_run_id)
		SELECT company_id, execution_authorized_by_user_id, conversation_id, id FROM runs WHERE id = $1`, runID); err != nil {
		t.Fatalf("seed research receipt: %v", err)
	}
}

// seedResearchPage records a Wiki page the Run wrote under key.
func seedResearchPage(t *testing.T, h *runHarness, runID uuid.UUID, key string) uuid.UUID {
	t.Helper()
	var pageID uuid.UUID
	if err := h.pool.QueryRow(context.Background(), `
		INSERT INTO wiki_pages(company_id, slug, title, created_by_user_id) VALUES($1, $2, $2, $3) RETURNING id`,
		h.fixture.CompanyA, key+"-"+runID.String(), h.fixture.UserA).Scan(&pageID); err != nil {
		t.Fatalf("seed wiki page: %v", err)
	}
	if _, err := h.pool.Exec(context.Background(),
		`INSERT INTO run_wiki_writes (run_id, company_id, key, page_id) VALUES ($1, $2, $3, $4)`,
		runID, h.fixture.CompanyA, key, pageID); err != nil {
		t.Fatalf("seed run wiki write: %v", err)
	}
	return pageID
}

type researchNotice struct {
	kind, title, body, deepLink string
	count                       int
}

func readResearchNotice(t *testing.T, h *runHarness, runID uuid.UUID) researchNotice {
	t.Helper()
	var notice researchNotice
	if err := h.pool.QueryRow(context.Background(), `
		SELECT count(*), COALESCE(max(kind), ''), COALESCE(max(title), ''), COALESCE(max(body), ''), COALESCE(max(deep_link), '')
		FROM inbox_items WHERE company_id = $1 AND entity_type = 'run' AND entity_id = $2`,
		h.fixture.CompanyA, runID.String()).Scan(&notice.count, &notice.kind, &notice.title, &notice.body, &notice.deepLink); err != nil {
		t.Fatalf("read research notice: %v", err)
	}
	return notice
}

func researchCompletedAt(t *testing.T, h *runHarness) (completed bool) {
	t.Helper()
	if err := h.pool.QueryRow(context.Background(),
		`SELECT completed_at IS NOT NULL FROM company_initializations WHERE company_id = $1`, h.fixture.CompanyA).Scan(&completed); err != nil {
		t.Fatalf("read research receipt: %v", err)
	}
	return completed
}

func TestResearchRunCompletionSetsCompletedAtAndNotifiesTheFounder(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)
	seedResearchReceipt(t, h, runID)
	hub := seedResearchPage(t, h, runID, "company-overview")
	seedResearchPage(t, h, runID, "open-questions")

	if _, err := h.svc.CompleteRun(ctx, runID, emptyTerminalProof(), "", false, ""); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	if !researchCompletedAt(t, h) {
		t.Fatal("completed_at was not set by the terminal path")
	}
	notice := readResearchNotice(t, h, runID)
	if notice.count != 1 || notice.kind != string(inbox.TypeRunCompleted) || notice.title != "Your company wiki is ready" ||
		!strings.Contains(notice.body, "2 pages") || notice.deepLink != "/wiki/pages/"+hub.String() {
		t.Fatalf("research notice = %+v", notice)
	}
	if strings.Contains(notice.title, "Reply ready") || strings.Contains(notice.body, "finished its reply") {
		t.Fatalf("generic reply copy leaked into %+v", notice)
	}
}

func TestResearchRunWithoutAnOverviewOpensTheWiki(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)
	seedResearchReceipt(t, h, runID)
	if _, err := h.svc.CompleteRun(ctx, runID, emptyTerminalProof(), "", false, ""); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	notice := readResearchNotice(t, h, runID)
	if notice.deepLink != "/wiki" || notice.title != "Your company wiki is ready" {
		t.Fatalf("research notice = %+v", notice)
	}
}

func TestResearchRunFailureNotifiesWithRetryHint(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)
	seedResearchReceipt(t, h, runID)
	if _, err := h.svc.FailRun(ctx, runID, emptyTerminalProof(), "provider failed", "", string(runfailure.ReasonAgentUnknown), false, ""); err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	if !researchCompletedAt(t, h) {
		t.Fatal("a final failure must still record completed_at")
	}
	notice := readResearchNotice(t, h, runID)
	if notice.count != 1 || notice.kind != string(inbox.TypeRunFailed) || notice.title != "Company research could not finish" ||
		!strings.Contains(notice.body, "retry") || notice.deepLink != "/wiki" {
		t.Fatalf("research failure notice = %+v", notice)
	}
}

func TestResearchRunAutoRetryIsNotFinal(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)
	seedResearchReceipt(t, h, runID)
	seedResearchPage(t, h, runID, "company-overview")
	failed, err := h.svc.FailRun(ctx, runID, emptyTerminalProof(), "stream dropped", "", string(runfailure.ReasonAgentProviderNetwork), false, "")
	if err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	if failed.Status != "failed" {
		t.Fatalf("status = %s", failed.Status)
	}
	var childID uuid.UUID
	if err := h.pool.QueryRow(ctx, `SELECT id FROM runs WHERE parent_run_id = $1`, runID).Scan(&childID); err != nil {
		t.Fatalf("the transient failure created no retry child: %v", err)
	}
	if researchCompletedAt(t, h) {
		t.Fatal("completed_at was set while an automatic retry is pending")
	}
	if notice := readResearchNotice(t, h, runID); notice.count != 0 {
		t.Fatalf("a retried attempt notified the founder: %+v", notice)
	}

	// The child finishes the research: its page and its parent's both count.
	dbtestUpdateRunStatus(t, h, childID, "running")
	seedResearchPage(t, h, childID, "open-questions")
	if _, err := h.svc.CompleteRun(ctx, childID, emptyTerminalProof(), "", false, ""); err != nil {
		t.Fatalf("CompleteRun child: %v", err)
	}
	if !researchCompletedAt(t, h) {
		t.Fatal("the final attempt did not complete the receipt")
	}
	notice := readResearchNotice(t, h, childID)
	if notice.count != 1 || notice.title != "Your company wiki is ready" || !strings.Contains(notice.body, "2 pages") {
		t.Fatalf("final attempt notice = %+v", notice)
	}
}

func TestOrdinaryRunDoesNotTouchTheResearchReceipt(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	researchRun := seedRunningTranscriptRun(t, h)
	seedResearchReceipt(t, h, researchRun)
	var conversationID uuid.UUID
	if err := h.pool.QueryRow(ctx, `SELECT conversation_id FROM runs WHERE id = $1`, researchRun).Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	dbtestUpdateRunStatus(t, h, researchRun, "cancelled")
	other := h.seedQueuedRun(t, conversationID)
	dbtestUpdateRunStatus(t, h, other, "running")
	if _, err := h.svc.CompleteRun(ctx, other, emptyTerminalProof(), "", false, ""); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	if researchCompletedAt(t, h) {
		t.Fatal("an unrelated Run completed the research receipt")
	}
	if notice := readResearchNotice(t, h, other); notice.title != "Reply ready: Direct" {
		t.Fatalf("ordinary notice = %+v", notice)
	}
}

func TestResearchRunCancellationCompletesTheReceiptAndNotifiesTheFounder(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)
	seedResearchReceipt(t, h, runID)
	if _, err := h.svc.CancelRun(ctx, runID, "user stopped execution"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if !researchCompletedAt(t, h) {
		t.Fatal("a cancelled research Run left completed_at unset")
	}
	notice := readResearchNotice(t, h, runID)
	if notice.count != 1 || notice.kind != string(inbox.TypeRunFailed) || notice.title != "Company research could not finish" ||
		notice.deepLink != "/wiki" {
		t.Fatalf("cancellation notice = %+v", notice)
	}
}

func TestResearchRetryChildCancellationIsTheFinalAttempt(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)
	seedResearchReceipt(t, h, runID)
	if _, err := h.svc.FailRun(ctx, runID, emptyTerminalProof(), "stream dropped", "", string(runfailure.ReasonAgentProviderNetwork), false, ""); err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	var childID uuid.UUID
	if err := h.pool.QueryRow(ctx, `SELECT id FROM runs WHERE parent_run_id = $1`, runID).Scan(&childID); err != nil {
		t.Fatalf("no retry child: %v", err)
	}
	if researchCompletedAt(t, h) {
		t.Fatal("completed_at was set while the retry is pending")
	}
	if _, err := h.svc.CancelRun(ctx, childID, "user stopped execution"); err != nil {
		t.Fatalf("CancelRun child: %v", err)
	}
	if !researchCompletedAt(t, h) {
		t.Fatal("cancelling the retry child left completed_at unset")
	}
	if notice := readResearchNotice(t, h, childID); notice.count != 1 || notice.title != "Company research could not finish" {
		t.Fatalf("child cancellation notice = %+v", notice)
	}
	if notice := readResearchNotice(t, h, runID); notice.count != 0 {
		t.Fatalf("the retried parent notified: %+v", notice)
	}
}

func TestResearchRunBulkCancellationSettlesTheReceipt(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)
	seedResearchReceipt(t, h, runID)
	run, err := h.svc.Queries.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.runInTx(ctx, func(q *db.Queries) error {
		var pending []events.Event
		_, err := cancelRunCandidatesInTx(ctx, q, &pending, run.CompanyID, []db.Run{run}, "runtime_owner_inactive")
		return err
	}); err != nil {
		t.Fatalf("cancelRunCandidatesInTx: %v", err)
	}
	if !researchCompletedAt(t, h) {
		t.Fatal("a bulk-cancelled research Run left completed_at unset")
	}
	if notice := readResearchNotice(t, h, runID); notice.count != 1 || notice.title != "Company research could not finish" {
		t.Fatalf("bulk cancellation notice = %+v", notice)
	}
}
