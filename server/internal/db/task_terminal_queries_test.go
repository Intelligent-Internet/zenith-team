package db

import (
	"strings"
	"testing"
)

func TestTaskTerminalEffectIsGenerationAndAssignmentSafe(t *testing.T) {
	t.Parallel()

	queries := queryFileSource(t, "tasks.sql")
	query := normalizedQuery(t, queries, "UpdateTaskForRunTerminal")
	for _, fence := range []string{
		"runs.id = sqlc.arg(run_id)::uuid",
		"runs.task_execution_generation = tasks.execution_generation",
		// The Run's Agent must still execute or assist the work (ADR 0014); a
		// handoff to another member is fenced by the generation above, which
		// it advances in the same write. The Run's authorizer is not compared
		// with the assignee: a Run in flight across the private-runtime
		// cutover keeps whatever authorizer it was enqueued with, and its
		// Task must still hear its outcome.
		"exists (select 1 from task_run_agents where task_id = tasks.id and agent_id = runs.agent_id)",
		"tasks.status = 'in_progress'",
	} {
		if !strings.Contains(query, fence) {
			t.Fatalf("UpdateTaskForRunTerminal lacks %q: %s", fence, query)
		}
	}
}

func TestProactiveTaskMergeWaitsForTaskHumanInputContinuation(t *testing.T) {
	t.Parallel()

	queries := queryFileSource(t, "task_merge_jobs.sql")
	for _, queryName := range []string{
		"InsertProactiveTaskMergeJobForRun",
		"InsertNextProactiveTaskMergeJob",
		"GetNextTaskMergeJobClaimCandidate",
		"ClaimTaskMergeJob",
	} {
		query := normalizedQuery(t, queries, queryName)
		for _, table := range []string{"approvals", "questions"} {
			for _, fence := range []string{
				table + ".origin_run_id = runs.id",
				table + ".task_id = tasks.id",
				table + ".status = 'pending'",
				table + ".continuation_status = 'pending'",
			} {
				if !strings.Contains(query, fence) {
					t.Errorf("%s lacks %s human-input fence %q: %s", queryName, table, fence, query)
				}
			}
		}
	}
}
