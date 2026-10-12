package db

import (
	"strings"
	"testing"
)

func TestAccountRunCancellationListsConversationsBeforeMutation(t *testing.T) {
	t.Parallel()

	runs := queryFileSource(t, "runs.sql")
	candidates := normalizedQuery(t, runs, "ListActiveRunConversationIDsForCompanyUser")
	for _, fragment := range []string{
		"with recursive authored_run_lineage",
		"select distinct runs.conversation_id",
		"runs.status in ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')",
		"order by runs.conversation_id",
	} {
		if !strings.Contains(candidates, fragment) {
			t.Errorf("account cancellation candidate query lacks %q: %s", fragment, candidates)
		}
	}
	if strings.Contains(candidates, "update runs") || strings.Contains(candidates, "for update") {
		t.Fatalf(
			"account cancellation candidate query mutates or locks Runs before Conversations: %s",
			candidates,
		)
	}

	cancel := normalizedQuery(t, runs, "CancelRunsForCompanyUser")
	if !strings.Contains(cancel, "update runs") {
		t.Fatalf("account cancellation mutation no longer updates Runs: %s", cancel)
	}
	for _, query := range []string{candidates, cancel} {
		for _, fragment := range []string{
			"conversations.kind = 'direct'",
			"conversations.created_by_user_id = sqlc.narg(author_id)::uuid",
			"approvals.continuation_run_id = runs.id",
			"approvals.decided_by_user_id = sqlc.narg(author_id)::uuid",
			"questions.continuation_run_id = runs.id",
			"questions.resolved_by_user_id = sqlc.narg(author_id)::uuid",
		} {
			if !strings.Contains(query, fragment) {
				t.Errorf("account cancellation query lacks human-input root %q: %s", fragment, query)
			}
		}
	}
}

func TestAccountDeletionRevokesMembershipAndAbandonsPendingContinuations(t *testing.T) {
	t.Parallel()

	accountDeletion := queryFileSource(t, "account_deletion.sql")
	authority := normalizedQuery(t, accountDeletion, "LockAccountDeletionAuthority")
	for _, fragment := range []string{
		"for no key update of users",
		"for update of auth_sessions",
	} {
		if !strings.Contains(authority, fragment) {
			t.Errorf("account deletion authority lacks %q: %s", fragment, authority)
		}
	}
	companies := normalizedQuery(t, accountDeletion, "LockAccountCompaniesForDeletion")
	for _, fragment := range []string{
		"approvals.origin_user_id = sqlc.arg(user_id)::uuid",
		"approvals.decided_by_user_id = sqlc.arg(user_id)::uuid",
		"approvals.status = 'pending'",
		"approvals.continuation_status = 'pending'",
		"questions.origin_user_id = sqlc.arg(user_id)::uuid",
		"questions.resolved_by_user_id = sqlc.arg(user_id)::uuid",
		"questions.status = 'pending'",
		"questions.continuation_status = 'pending'",
	} {
		if !strings.Contains(companies, fragment) {
			t.Errorf("account Company discovery lacks pending continuation %q: %s", fragment, companies)
		}
	}

	revoke := normalizedQuery(t, accountDeletion, "RevokeActiveMembershipsForAccountDeletion")
	for _, fragment := range []string{
		"update company_members",
		"status = 'revoked'",
		"revoked_by_user_id = sqlc.arg(user_id)::uuid",
		"company_members.status = 'active'",
		"version = company_members.version + 1",
		"returning company_members.*",
		"select * from revoked",
		"order by company_id, user_id",
	} {
		if !strings.Contains(revoke, fragment) {
			t.Errorf("account membership revocation lacks %q: %s", fragment, revoke)
		}
	}

	abandon := normalizedQuery(t, accountDeletion, "AbandonPendingContinuationsForAccountDeletion")
	for _, fragment := range []string{
		"update approvals",
		"approvals.origin_user_id = sqlc.arg(user_id)::uuid",
		"approvals.decided_by_user_id = sqlc.arg(user_id)::uuid",
		"update questions",
		"questions.origin_user_id = sqlc.arg(user_id)::uuid",
		"questions.resolved_by_user_id = sqlc.arg(user_id)::uuid",
		"continuation_status = 'abandoned'",
		"continuation_status = 'pending'",
		"'approval'::text as entity_type",
		"'question'::text as entity_type",
		"as entity_id",
		"as task_id",
		"as origin_user_id",
	} {
		if !strings.Contains(abandon, fragment) {
			t.Errorf("account continuation abandonment lacks %q: %s", fragment, abandon)
		}
	}
}
