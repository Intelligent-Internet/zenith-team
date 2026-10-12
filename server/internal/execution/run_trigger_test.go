package execution

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
)

// The execute refusal is the plain Project permission refusal for every
// caller that matches on it, with the Agent attached for the boundary.
func TestExecutePermissionErrorIsTheProjectPermissionRefusal(t *testing.T) {
	t.Parallel()

	var err error = &ExecutePermissionError{AgentName: "Builder"}
	if !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Fatalf("errors.Is(%v, ErrProjectPermissionRequired) = false", err)
	}
	var refusal *ExecutePermissionError
	if !errors.As(err, &refusal) || refusal.AgentName != "Builder" {
		t.Fatalf("errors.As = %+v", refusal)
	}
}

// The claim's Project access arm asks the delegate rule (run_permission_held,
// D31) for the execute permission, binding it, read and the Company default
// set from the catalog, so SQL names no permission (ADR 0017). A delegate
// never manages, so no management set or role is bound.
func TestClaimCandidatesBindTheExecutePermissionFromTheCatalog(t *testing.T) {
	t.Parallel()

	runtimeID := uuid.New()
	params := claimCandidateParams(runtimeID)
	sets := auth.ProjectPermissionHeldSets()
	if params.ExecutePermission != string(auth.ProjectPermissionTasksExecute) ||
		params.ReadPermission != string(auth.ProjectPermissionRead) {
		t.Fatalf("execute and read permissions = %q, %q", params.ExecutePermission, params.ReadPermission)
	}
	if !slices.Equal(params.RuntimeIds, []uuid.UUID{runtimeID}) ||
		!slices.Equal(params.CompanyDefaultPermissions, sets.CompanyDefaultPermissions) {
		t.Fatalf(
			"claim candidate params = %+v, want the catalog default set %+v",
			params,
			sets.CompanyDefaultPermissions,
		)
	}
}
