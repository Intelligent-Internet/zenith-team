package task

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Task writes inside a Project (ADR 0017). The Project permission is the only
// check for work inside a Project; the Company catalog is not consulted,
// except that handing a Task to another member still needs tasks.assign or
// being the current assignee (ADR 0014). Every check here runs after the
// Project row is locked (FOR UPDATE on create, FOR SHARE on every other
// write), so a concurrent grant or visibility change, which locks the same
// row FOR UPDATE first, waits for the write and the write never acts on a
// stale grant.

// lockTaskProjectAccess holds the Task's Project FOR SHARE and resolves the
// caller's standing on it. Lock order: company -> member -> project ->
// conversation -> run, so the caller takes it after its Company and member
// locks and before any Conversation lock. A Project the caller cannot read
// answers ErrNotFound, the Task surface's own not-found: the management
// bridge never opens a Task.
func lockTaskProjectAccess(
	ctx context.Context,
	tx *generated.Queries,
	principal auth.UserPrincipal,
	projectID uuid.UUID,
) (auth.ProjectAccess, error) {
	access, err := auth.LockProjectAccess(ctx, tx, principal, projectID)
	if errors.Is(err, auth.ErrProjectNotVisible) {
		return auth.ProjectAccess{}, ErrNotFound
	}
	if err != nil {
		return auth.ProjectAccess{}, err
	}
	if access.Via == "" {
		return auth.ProjectAccess{}, ErrNotFound
	}
	return access, nil
}

// requireProjectReadable refuses a Task list filtered by a Project the caller
// cannot read with auth.ErrProjectNotVisible (404 project_not_found), exactly
// as for a Project that does not exist, instead of an empty list that would
// confirm it exists. A projects.manage holder who only manages the Project
// reads none of its Tasks, so they are refused too.
func requireProjectReadable(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	projectID uuid.UUID,
) error {
	access, err := auth.ResolveProjectAccess(ctx, q, principal, projectID)
	if err != nil {
		return err
	}
	if access.Via == "" {
		return auth.ErrProjectNotVisible
	}
	return nil
}

// requireProjectPermission is the decision on a resolved standing: a
// non-reader cannot see the Task at all (ErrNotFound), and a reader without
// the permission gets auth.ErrProjectPermissionRequired.
func requireProjectPermission(access auth.ProjectAccess, permission auth.ProjectPermission) error {
	if access.Via == "" {
		return ErrNotFound
	}
	if !access.Can(permission) {
		return auth.ErrProjectPermissionRequired
	}
	return nil
}

// requireAssigneeReads refuses a Task assigned or handed to a member who
// cannot read its Project: they would be responsible for work they cannot
// see. The refusal names nobody.
func requireAssigneeReads(access auth.ProjectAccess) error {
	if access.Via == "" {
		return ErrAssigneeOutsideProject
	}
	return nil
}

// checkAssigneeReadsProject resolves the assignee's standing on the Project
// and applies requireAssigneeReads. The caller holds the Project lock.
func checkAssigneeReadsProject(
	ctx context.Context,
	tx *generated.Queries,
	companyID, projectID, assigneeID uuid.UUID,
) error {
	access, err := auth.ResolveMemberProjectAccess(ctx, tx, companyID, assigneeID, projectID)
	if errors.Is(err, auth.ErrProjectNotVisible) {
		return ErrAssigneeOutsideProject
	}
	if err != nil {
		return fmt.Errorf("resolve the assignee's Project access: %w", err)
	}
	return requireAssigneeReads(access)
}

// requireExecutePermission refuses a Task Run whose assignee lacks
// project.tasks.execute. Only the assignee enqueues a Task Run (ADR 0014),
// so access is the assignee's own standing and assigneeID the caller. The
// refusal names the executing Agent, never the Project.
func requireExecutePermission(
	ctx context.Context,
	tx *generated.Queries,
	access auth.ProjectAccess,
	assigneeID, agentID uuid.UUID,
) error {
	if access.Can(auth.ProjectPermissionTasksExecute) {
		return nil
	}
	return executeRefusal(ctx, tx, access.CompanyID, assigneeID, agentID)
}

// requireAssigneeExecuteInTx is the same decision for a Task Run a chat
// message starts or a queued prompt is promoted into: the Run's authorizer is
// always the Task's assignee (ADR 0014), and the check reads through
// project_permission_held without taking the Project lock, because the
// caller already holds the Conversation lock that must follow it. A human
// Task runs too: its chat's Run is the assisting Agent's (task_run_agents)
// on the assignee's computer, so it needs the same permission.
func requireAssigneeExecuteInTx(ctx context.Context, q *generated.Queries, row generated.Task) error {
	held, err := auth.HoldsProjectPermission(
		ctx, q, row.CompanyID, row.ProjectID, row.AssigneeUserID, auth.ProjectPermissionTasksExecute,
	)
	if err != nil {
		return err
	}
	if held {
		return nil
	}
	return taskRunRefusal(ctx, q, row.CompanyID, row.ID, row.AssigneeUserID)
}

// taskRunRefusal builds the execute refusal for a Task's Run, naming the
// Agent its Run uses: the executing Agent, or for a human Task the Agent that
// assists it (task_run_agents). A Task with no such Agent names none.
func taskRunRefusal(ctx context.Context, q *generated.Queries, companyID, taskID, assigneeID uuid.UUID) error {
	agent, err := q.GetTaskRunAgent(ctx, generated.GetTaskRunAgentParams{CompanyID: companyID, TaskID: taskID})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return &execution.ExecutePermissionError{AssigneeID: assigneeID}
	case err != nil:
		return fmt.Errorf("read the Task's Run Agent for the execute refusal: %w", err)
	}
	return executeRefusal(ctx, q, companyID, assigneeID, agent.ID)
}

// executeRefusal builds the execution.ExecutePermissionError for the Task's
// Agent: the kernel owns the refusal, since it refuses every Task Run the
// same way (ADR 0017).
func executeRefusal(ctx context.Context, q *generated.Queries, companyID, assigneeID, agentID uuid.UUID) error {
	return execution.ExecutePermissionRefusal(ctx, q, companyID, assigneeID, agentID)
}

// projectWriteFence names the person whose Project permission a Task write
// statement checks again through project_permission_held (ADR 0017
// decision 7): the service decides under the Project lock, and the statement
// refuses the write if the person no longer holds the permission when it
// runs, whatever path the change took (a grant, a visibility flip, a
// disabled account). The zero value is a system writer acting on a Task it
// already holds, and the statement skips the fence.
type projectWriteFence struct {
	userID     uuid.UUID
	permission auth.ProjectPermission
}

func personWriteFence(userID uuid.UUID, permission auth.ProjectPermission) projectWriteFence {
	return projectWriteFence{userID: userID, permission: permission}
}

// params are the five statement arguments of the fence.
func (f projectWriteFence) params() (uuid.NullUUID, string, auth.ProjectPermissionSets) {
	if f.userID == uuid.Nil {
		return uuid.NullUUID{}, "", auth.ProjectPermissionSets{
			CompanyDefaultPermissions: []string{},
			ManagementPermissions:     []string{},
			ManagementRoles:           []string{},
		}
	}
	return uuid.NullUUID{UUID: f.userID, Valid: true}, string(f.permission), auth.ProjectPermissionHeldSets()
}

// explainFencedMiss tells a write the fence refused apart from the
// statement's other zero-row outcomes (a moved revision, an inactive
// assignee, an archived Agent): when the fenced person no longer holds the
// permission, the write is refused with auth.ErrProjectPermissionRequired;
// otherwise the original error stands.
func explainFencedMiss(
	ctx context.Context,
	q *generated.Queries,
	fence projectWriteFence,
	companyID, projectID uuid.UUID,
	err error,
) error {
	if fence.userID == uuid.Nil || !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	held, checkErr := auth.HoldsProjectPermission(ctx, q, companyID, projectID, fence.userID, fence.permission)
	if checkErr != nil {
		return checkErr
	}
	if !held {
		return auth.ErrProjectPermissionRequired
	}
	return err
}

// assigneeOutsideProject lists the Task when its assignee cannot read its
// Project. An assignee who loses access keeps the Task until someone hands
// it over (ADR 0014 d.13); every write receipt says so. It reads the
// projected Task's own assigneeOutsideProject, so the receipt and every Task
// read follow one rule.
func assigneeOutsideProject(value Response) []uuid.UUID {
	if !value.AssigneeOutsideProject {
		return nil
	}
	return []uuid.UUID{value.ID}
}
