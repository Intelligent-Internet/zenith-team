package handler

import (
	"net/http"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

// Project access refusals (ADR 0017), shared by every surface inside a
// Project (Projects, Tasks, Task chats, Runs, Agent Mail, Approvals and
// Questions): a reader without the permission the action needs, and a
// Task-origin request nobody may decide, whose Run the server has stopped.
var (
	projectPermissionRequiredMapping = errorMapping{
		auth.ErrProjectPermissionRequired,
		http.StatusForbidden,
		"Your access to this Project does not allow this action",
		"project_permission_required",
	}
	noEligibleResolverMapping = errorMapping{
		auth.ErrNoEligibleResolver,
		http.StatusConflict,
		"No one who can see this Project may decide this, so this Run has been stopped",
		"no_eligible_resolver",
	}
)
