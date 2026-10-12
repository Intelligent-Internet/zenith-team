package db

import (
	"strings"
	"testing"
)

func TestFreshTaskRunAdvancesGenerationAndRetryInherits(t *testing.T) {
	t.Parallel()

	runs := queryFileSource(t, "runs.sql")
	fresh := normalizedQuery(t, runs, "CreateRun")
	for _, fragment := range []string{
		"update tasks set execution_generation = tasks.execution_generation + 1",
		"tasks.conversation_id = sqlc.arg(conversation_id)",
		"task_execution_generation, run_type, revision, invocation_id, source_task_id, studio_enabled ) select",
		"(select execution_generation from advanced_task)",
	} {
		if !strings.Contains(fresh, fragment) {
			t.Errorf("CreateRun does not contain %q: %s", fragment, fresh)
		}
	}

	retry := normalizedQuery(t, runs, "CreateRetryRun")
	if !strings.Contains(retry, "p.task_execution_generation") {
		t.Fatalf("CreateRetryRun does not inherit the parent generation: %s", retry)
	}
	if strings.Contains(retry, "update tasks") || strings.Contains(retry, "execution_generation + 1") {
		t.Fatalf("CreateRetryRun advances the Task generation: %s", retry)
	}
}

func TestTaskRetryRequiresCurrentExecutableAssignment(t *testing.T) {
	t.Parallel()

	retry := normalizedQuery(t, queryFileSource(t, "runs.sql"), "CreateRetryRun")
	for _, fence := range []string{
		"runs.status = 'failed'",
		"p.task_id is null or exists",
		"tasks.company_id = p.company_id",
		"tasks.id = p.task_id",
		"tasks.conversation_id = p.conversation_id",
		"tasks.execution_generation = p.task_execution_generation",
		"tasks.status = 'in_progress'",
		"exists (select 1 from task_run_agents where task_id = tasks.id and agent_id = p.agent_id)",
		"(tasks.executed_by_agent_id is null) = coalesce( (p.context->'executionprofile'->'task'->>'humanwork')::boolean, false)",
		"tasks.assignee_user_id = p.execution_authorized_by_user_id",
		// The retry runs on the authorizer's CURRENT binding of the agent, not
		// the parent's runtime: no binding, no retry (ADR 0014).
		"binding.user_id = p.execution_authorized_by_user_id",
		"exists (select 1 from current_binding)",
		"prerequisite.status <> 'done'",
	} {
		if !strings.Contains(retry, fence) {
			t.Errorf("CreateRetryRun lacks eligibility fence %q: %s", fence, retry)
		}
	}
}

func TestFailedTaskReopenGuardIncludesEveryNonterminalRunState(t *testing.T) {
	t.Parallel()

	query := normalizedQuery(
		t,
		queryFileSource(t, "tasks.sql"),
		"TaskHasNonterminalRunForCurrentExecutionGeneration",
	)
	if !strings.Contains(query, "runs.task_execution_generation = tasks.execution_generation") {
		t.Fatalf("Task reopen guard is not generation-fenced: %s", query)
	}
	for _, status := range []string{
		"queued", "dispatched", "waiting_local_directory", "running", "deferred",
	} {
		if !strings.Contains(query, "'"+status+"'") {
			t.Errorf("Task reopen guard omits nonterminal status %q: %s", status, query)
		}
	}
}

func normalizedQuery(t *testing.T, source, name string) string {
	t.Helper()
	return strings.ToLower(strings.Join(strings.Fields(
		statementsOnly(namedQuerySource(t, source, name)),
	), " "))
}
