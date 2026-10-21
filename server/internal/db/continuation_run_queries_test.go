package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A terminal Approval or Question used to take its continuation identity from
// runs_id_seq inline, in the same UPDATE that made it terminal — but the Run is
// materialized later, and that pass can decline (a duplicate pending Run defers
// it or an archived agent or closed Task abandons it). The row then kept an id
// naming a Run that was never created and never would be.
//
// These are source-shape assertions rather than database ones so they run in
// the unit lane, where the terminal statements are the whole surface: if none
// of them can write the column, no terminal transition can leave a dangling id
// behind, whatever the reconciler later decides.
func TestTerminalHumanInputStatementsNameNoContinuationRun(t *testing.T) {
	t.Parallel()

	source := approvalQueriesSource(t)
	for _, name := range []string{
		"ResolveApproval", "ExpireDueApprovals", "ResolveQuestion", "ExpireDueQuestions",
		"DeferApprovalContinuation", "DeferQuestionContinuation",
		"AbandonApprovalContinuation", "AbandonQuestionContinuation",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			query := statementsOnly(namedQuerySource(t, source, name))
			if strings.Contains(query, "continuation_run_id") {
				t.Errorf("%s writes continuation_run_id; only delivery may name a Run", name)
			}
		})
	}
}

// Delivery is the one statement allowed to name the Run, because it runs in the
// transaction that INSERTed it: the stamp and the receipt commit together.
func TestContinuationDeliveryStampsTheRunItCommitsWith(t *testing.T) {
	t.Parallel()

	source := approvalQueriesSource(t)
	for _, test := range []struct {
		name  string
		fence string
	}{
		{"DeliverApprovalContinuation", "id = sqlc.arg(approval_id)::uuid"},
		{"DeliverQuestionContinuation", "id = sqlc.arg(question_id)::uuid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			query := namedQuerySource(t, source, test.name)
			for _, want := range []string{
				"continuation_run_id = sqlc.arg(continuation_run_id)::uuid",
				"continuation_status = 'delivered'",
				test.fence,
				"AND continuation_status = 'pending'",
			} {
				if !strings.Contains(query, want) {
					t.Errorf("%s does not contain %q", test.name, want)
				}
			}
		})
	}
}

// Nothing anywhere takes a Run identity ahead of the INSERT that creates it.
func TestNoQueryPreallocatesARunIdentity(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("..", "..", "pkg", "db", "queries")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read queries directory: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if strings.Contains(statementsOnly(string(raw)), "runs_id_seq") {
			t.Errorf("%s reaches into runs_id_seq; runs.id comes from the INSERT", entry.Name())
		}
	}
}

// The schema's backstop is one invariant, not an enumeration of every
// continuation shape: a pending receipt has no continuation and a resolved one
// does. The finer rule -- continuation_run_id is non-NULL in exactly the
// delivered shape -- is the writer's now, held by the queries this file
// already checks: the id is only ever written beside the INSERT that creates
// its Run, in the same transaction. That is a deliberate move. A CHECK
// enumerating column combinations restated the state machine in a language
// nothing can test, and every new continuation state meant editing the
// constraint instead of the code that owns the transition.
func TestContinuationPresenceChecksBindContinuationToResolution(t *testing.T) {
	t.Parallel()

	sql := normalizedBaselineSQL(t)
	want := "check ((status = 'pending') = (continuation_status is null))"
	if strings.Count(sql, want) != 2 {
		t.Errorf("continuation presence check missing on Approvals and Questions: %q", want)
	}
	if strings.Contains(sql, "nextval('runs_id_seq')") {
		t.Error("the baseline still reserves a Run identity from runs_id_seq")
	}
}

// statementsOnly drops whole-line `--` comments so a shape assertion reads the
// SQL a query actually executes, not the prose explaining it.
func statementsOnly(source string) string {
	var kept []string
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func approvalQueriesSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "approvals.sql"))
	if err != nil {
		t.Fatalf("read approvals.sql: %v", err)
	}
	return string(raw)
}
