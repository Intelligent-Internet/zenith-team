package db

import (
	"strings"
	"testing"
)

func TestCompanyCreateFounderFenceConflictsWithAccountDeletion(t *testing.T) {
	t.Parallel()

	query := normalizedQuery(
		t,
		queryFileSource(t, "companies.sql"),
		"LockEnabledUserForMembershipMutation",
	)
	for _, fragment := range []string{
		"id = sqlc.arg(user_id)::uuid",
		"disabled_at is null",
		"for share",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("Company founder fence lacks %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "for key share") {
		t.Fatalf("Company founder fence no longer conflicts with account deletion: %s", query)
	}
}
