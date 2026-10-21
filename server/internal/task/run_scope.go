package task

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// RunScope is what a Run may read of its Company's Tasks: only its own, the
// Tasks its Agent does for the member the Run works for. A Task runs only as
// its own Task Run, which its assignee starts on their computer with the
// Agent the Task names (ADR 0014); an Agent that could read a colleague's
// Task was one prompt away from acting it out on the wrong computer while the
// Task itself never moved.
//
// The reads and creates a Run sends through its channel carry the Run's
// proof, and the handler turns that proof into this scope. A member's own
// request carries no proof and a nil scope, and sees every Task as before.
//
// The reads themselves are made with the member's session, which the
// handler has checked is the Run's authorizer, so they already pass only
// the Projects that member reads (ADR 0017). What a Run may do in a Project
// is the delegate rule, which the database states once for every Run
// (run_permission_held): its Authorizer holds the permission
// and read there, never more, and a Task Run reads and files work only in
// its Task chat's Project, because only that Project's readers read the
// Task chat. A Run tagged in a channel or group DM uses its Authorizer's
// access alone: the person who tagged the Agent answers for what it shares.
// The rule reads all of that from the Run's own row, so the scope carries
// only the Run.
//
// One Run reads more: the Planner (ADR 0014 rule 14's one exception). The
// root Agent plans the Company, so in its person's direct
// chat, with its Company Plan tools on, it reads every Task its person
// reads. Its answer reaches only that person, and the delegate rule still
// applies.
type RunScope struct {
	AgentID uuid.UUID
	// UserID is the Run's authorizer, the member on whose binding it runs.
	UserID uuid.UUID
	// RunID is the Run whose proof made the request. The delegate rule reads
	// this Run's row, so a scope without a real Run reads no Project. A Task
	// it creates is recorded as the Agent's, for UserID, in this Run.
	RunID uuid.UUID
	// TaskID is a Task Run's own Task, nil for any other Run. A human Task the
	// Run's Agent assists is the Run's own only through it.
	TaskID *uuid.UUID
	// ProjectID is a Task Run's Project, the Project of the Task chat it
	// answers into; nil for any other Run. A Task Run's list that names no
	// Project lists this one (listOptions); the delegate rule keeps it there.
	ProjectID *uuid.UUID
	// Planner is set for the root Agent's Run in its person's direct chat,
	// with its Company Plan tools on, with no Task and not tagged in a chat:
	// the handler decides it from the proof. Its reads are not narrowed to its
	// own Tasks; every other Run's are. Creates are unchanged.
	Planner bool
}

// actor is who a write made under the scope is attributed to: the Run's
// Agent acting for its authorizer in the Run, or, with no scope, the
// member themself. The handler always names the Run; a scope built without
// one still names the Agent and its person.
func (scope *RunScope) actor(userID uuid.UUID) events.Actor {
	if scope == nil {
		return events.UserActor(userID.String())
	}
	if scope.RunID == uuid.Nil {
		return events.Actor{
			Type: events.ActorTypeAgent, ID: scope.AgentID.String(), OnBehalfOf: scope.UserID.String(),
		}
	}
	return events.RunActor(scope.AgentID.String(), scope.RunID.String(), scope.UserID.String())
}

// creatorAgentID is the Agent a Task created under the scope also names as
// its creator, beside the member; none without a scope.
func (scope *RunScope) creatorAgentID() uuid.NullUUID {
	if scope == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: scope.AgentID, Valid: true}
}

// admits says whether a Task is the Run's own: its assignee is the member
// the Run works for and the Run's Agent does the work, or assists this exact
// human Task. A nil scope — a member reading for themself — and the Planner
// admit every Task.
func (scope *RunScope) admits(value Response) bool {
	return scope.owns(value.Assignee, value.ExecutedBy) ||
		scope != nil && scope.TaskID != nil && *scope.TaskID == value.ID &&
			value.Assignee.ID == scope.UserID && value.ExecutedBy.Kind == ActorUser &&
			value.Execution.Agent != nil && value.Execution.Agent.ID == scope.AgentID
}

// ReadableProjects reports which of projectIDs the Run may read: the
// delegate rule asked for project.read (run_permission_held). A Task
// Run reads only its own Task chat's Project, and every Run only what its
// Authorizer reads. A nil scope is a member's own request and reads them
// all: the read itself already checked their access.
func (scope *RunScope) ReadableProjects(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	projectIDs []uuid.UUID,
) (map[uuid.UUID]bool, error) {
	return scope.permittedProjects(ctx, q, companyID, auth.ProjectPermissionRead, projectIDs)
}

// permittedProjects reports which of projectIDs the delegate rule lets the
// Run use permission on, in one statement with one call per Project
// (ListRunReadableProjects). A nil scope permits them all.
func (scope *RunScope) permittedProjects(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	permission auth.ProjectPermission,
	projectIDs []uuid.UUID,
) (map[uuid.UUID]bool, error) {
	permitted := make(map[uuid.UUID]bool, len(projectIDs))
	if scope == nil {
		for _, id := range projectIDs {
			permitted[id] = true
		}
		return permitted, nil
	}
	if len(projectIDs) == 0 {
		return permitted, nil
	}
	ids, err := q.ListRunReadableProjects(ctx, generated.ListRunReadableProjectsParams{
		ProjectIds:                projectIDs,
		CompanyID:                 companyID,
		RunID:                     scope.RunID,
		Permission:                string(permission),
		ReadPermission:            string(auth.ProjectPermissionRead),
		CompanyDefaultPermissions: auth.ProjectPermissionHeldSets().CompanyDefaultPermissions,
	})
	if err != nil {
		return nil, fmt.Errorf("read the Projects the Run may use: %w", err)
	}
	for _, id := range ids {
		permitted[id] = true
	}
	return permitted, nil
}

// AdmitProject refuses a read in a Project the delegate rule keeps the Run
// out of, with the plain ErrOutsideRunScope: the refusal names nothing, so
// the Agent says only that it cannot access that work.
func (scope *RunScope) AdmitProject(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID uuid.UUID,
) error {
	return scope.admitProject(ctx, q, companyID, projectID, auth.ProjectPermissionRead)
}

// admitProject is AdmitProject for any permission: a read asks project.read,
// a Task create project.tasks.write.
func (scope *RunScope) admitProject(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID uuid.UUID,
	permission auth.ProjectPermission,
) error {
	permitted, err := scope.permittedProjects(ctx, q, companyID, permission, []uuid.UUID{projectID})
	if err != nil {
		return err
	}
	if permitted[projectID] {
		return nil
	}
	return ErrOutsideRunScope
}

// filterProjects keeps the Tasks whose Project the Run may read; with no Run
// it returns tasks unchanged.
func (scope *RunScope) filterProjects(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	tasks []Response,
) ([]Response, error) {
	if scope == nil {
		return tasks, nil
	}
	projectIDs := make([]uuid.UUID, 0, len(tasks))
	for _, value := range tasks {
		if !slices.Contains(projectIDs, value.ProjectID) {
			projectIDs = append(projectIDs, value.ProjectID)
		}
	}
	readable, err := scope.ReadableProjects(ctx, q, companyID, projectIDs)
	if err != nil {
		return nil, err
	}
	kept := make([]Response, 0, len(tasks))
	for _, value := range tasks {
		if readable[value.ProjectID] {
			kept = append(kept, value)
		}
	}
	return kept, nil
}

// owns is admits for any view of a Task that names its assignee and who
// does its work, such as the other Task of a dependency. The Planner owns
// every Task it reads.
func (scope *RunScope) owns(assignee, executedBy ActorRef) bool {
	return scope == nil || scope.Planner ||
		assignee.ID == scope.UserID &&
			executedBy.Kind == ActorAgent && executedBy.ID == scope.AgentID
}

// dependencies keeps what a Run may read of a readable Task's dependencies:
// every prerequisite, and only the dependents that are the Run's own. A
// prerequisite is context for the Task's work, the same summary a Task
// Run's prompt carries; a dependent is later work, often another member's,
// and a Run learns nothing of another member's Task. Both sit in the
// readable Task's Project, since dependencies never cross Projects. The
// Planner keeps every dependent, as its person reads them.
func (scope *RunScope) dependencies(set DependencySet) DependencySet {
	if scope == nil || scope.Planner {
		return set
	}
	blocks := make([]DependencyResponse, 0, len(set.Blocks))
	for _, dependent := range set.Blocks {
		if scope.owns(dependent.Task.Assignee, dependent.Task.ExecutedBy) {
			blocks = append(blocks, dependent)
		}
	}
	set.Blocks = blocks
	return set
}

// listOptions narrows a list to the Run's own Tasks. The scope replaces the
// caller's assignee and Agent filters rather than joining them, so no filter
// reaches past it; every other filter still narrows within it. A Task Run
// that names no Project lists its own Project; one that names another gets
// nothing back, because the list is filtered by Project afterwards. The
// Planner's list keeps the caller's filters, as its person's list does.
func (scope *RunScope) listOptions(options ListOptions) ListOptions {
	if scope == nil {
		return options
	}
	if !scope.Planner {
		userID, agentID := scope.UserID, scope.AgentID
		options.AssigneeUserID = &userID
		options.ExecutedByAgentID = &agentID
	}
	if options.ProjectID == nil && scope.ProjectID != nil {
		projectID := *scope.ProjectID
		options.ProjectID = &projectID
	}
	options.assistedTaskID = scope.TaskID
	return options
}

// admitRead refuses a Run's read of a Task that is not its own with the
// plain ErrOutsideRunScope. The refusal carries nothing of the Task — not
// its description, its label, its assignee or its Agent — so a Run learns
// only that the Task is not its own. The Run's own Task in a Project the
// delegate rule keeps it out of — another Project, for a Task Run — is
// refused the same way.
func (scope *RunScope) admitRead(ctx context.Context, q *generated.Queries, value Response) error {
	if !scope.admits(value) {
		return ErrOutsideRunScope
	}
	return scope.AdmitProject(ctx, q, value.CompanyID, value.ProjectID)
}

// admitCreate refuses a Run's create of a Task that would not be its
// member's, or in a Project the Run may not read. An ordinary create names
// the member it works for. A scheduled create may name another member, but
// waits for that assignee's acceptance and executes only on their binding.
// Both require the delegate rule to admit the Project (run_permission_held):
// a Task Run only in its own Task's Project, any other Run where its
// Authorizer may write. Either refusal is
// the plain ErrOutsideRunScope a read gets, asked before the Project is
// locked or the member's own access to it is read, so a create in a Project
// the Run may not read learns nothing of it, not even whether its member
// sees it. The CLI never sends such a create; this holds for a Run that
// calls its channel by hand.
func (scope *RunScope) admitCreate(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	input CreateInput,
) error {
	if scope == nil {
		return nil
	}
	if input.Assignee.ID != scope.UserID && input.Schedule == nil {
		return ErrOutsideRunScope
	}
	return scope.AdmitProject(ctx, q, companyID, input.ProjectID)
}

// admitCreateProject asks the delegate rule for project.tasks.write
// (run_permission_held) under the Project lock, after Create's own
// check of the member's project.tasks.write there, so a member without it
// is told project_permission_required in a Project the Run may read. It is
// the create's deciding check; admitCreate's read only refuses early. The
// refusal is the plain ErrOutsideRunScope; the handler made sure the member
// is the Run's authorizer.
func (scope *RunScope) admitCreateProject(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID uuid.UUID,
) error {
	return scope.admitProject(ctx, q, companyID, projectID, auth.ProjectPermissionTasksWrite)
}

// ResolveReference finds the Task a person or Agent names instead of its id:
// its key, or the {taskPrefix}-{number} label every surface shows. A Run
// needs it most: it cannot list a Task that is not its own to find the id,
// and the read that follows must still refuse it the same way as by id. Only
// Tasks in Projects the caller reads are candidates (ADR 0017). It returns
// only the id; the read applies the caller's visibility and scope.
func (s *DefaultTaskService) ResolveReference(
	ctx context.Context,
	principal auth.UserPrincipal,
	reference string,
) (uuid.UUID, error) {
	prefix, number, _ := util.ParseTaskLabel(reference)
	id, err := s.store.Queries().ResolveTaskReference(ctx, generated.ResolveTaskReferenceParams{
		ActorUserID: principal.UserID,
		CompanyID:   principal.CompanyID,
		Reference:   reference,
		LabelNumber: number,
		LabelPrefix: prefix,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve Task reference: %w", err)
	}
	return id, nil
}
