package company

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// A Company Plan works inside the Projects its actor may reach (ADR 0017).
// The apply locks only the Projects the applier reads, and of those changes
// only the ones in scope: where the applier holds project.tasks.write, that
// the plan read as writable, and, for a plan raised in a Task chat, that
// chat's own Project. A Project the applier cannot read is never locked or
// returned, so its key behaves in the plan like a key nobody has used. A
// Project the applier reads but that is outside the scope is left alone: the
// plan's entries for it and its Tasks are ignored, and omitting its Tasks
// deletes nothing. When the plan would have changed a Project the applier
// could change when the plan was read but no longer can, or would archive or
// empty a Project without project.archive, the apply leaves that Project
// alone and lists it in the result's skipped changes.

// planScopeRule says which Projects one Company Plan entry point reaches.
type planScopeRule struct {
	actor approval.CompanyPlanActor
	// base is the snapshot the plan was read from. It is set for an apply,
	// which always needs project.tasks.write, compares the scope with what
	// the plan read and checks project.archive. It is nil for the Approval
	// raise, which takes the snapshot itself.
	base *PlanBase
	// writeNeeded keeps the raise's scope to the Projects where the actor
	// holds project.tasks.write. Only the raise of a Task-chat plan previews
	// its Project for a reader without it; whoever approves it needs it.
	writeNeeded bool
}

// planProjectStanding says where a Project the actor reads stands in one
// Company Plan entry point.
type planProjectStanding int

const (
	// planProjectInScope: the entry point may change it.
	planProjectInScope planProjectStanding = iota
	// planProjectReadOnly: the actor may not write its Tasks, or the plan
	// read it or marks it read-only. Left alone.
	planProjectReadOnly
	// planProjectOutsideTask: a Task-chat plan reaches only that Task's
	// Project. Left alone.
	planProjectOutsideTask
	// planProjectUnlisted: the plan was read before the actor could read
	// it, or before it existed. Stale if the plan names it, left alone
	// otherwise.
	planProjectUnlisted
	// planProjectWriteLost: the plan read it as writable and the applier
	// can no longer change its Tasks. Skipped if the plan changes it, left
	// alone if the plan restates it as it is.
	planProjectWriteLost
)

// classifyPlanProject decides where one Project the actor reads stands.
// canWrite is the actor's project.tasks.write there, read is the plan's base
// entry for it (nil when the base does not list it or there is no base), and
// markedReadOnly is the desired document's readOnly flag for its key.
func classifyPlanProject(
	rule planScopeRule,
	projectID uuid.UUID,
	canWrite bool,
	read *PlanBaseEntity,
	markedReadOnly bool,
) planProjectStanding {
	taskProject := rule.actor.TaskProjectID
	switch {
	case taskProject.Valid && projectID != taskProject.UUID:
		return planProjectOutsideTask
	case markedReadOnly, read != nil && read.ReadOnly:
		return planProjectReadOnly
	case rule.base == nil && rule.writeNeeded && !canWrite:
		return planProjectReadOnly
	case rule.base == nil:
		return planProjectInScope
	case !canWrite && read == nil:
		return planProjectReadOnly
	case !canWrite:
		return planProjectWriteLost
	case read == nil:
		return planProjectUnlisted
	default:
		return planProjectInScope
	}
}

// errPlanStale says the plan names work in scope that changed since it was
// read in a way the base alone does not show. Each entry point decides what
// that means to its caller, as for errPlanSlugChanged.
var errPlanStale = errors.New("Company Plan was read before a change in its scope")

// lockedPlan is the scope of one Company Plan apply, locked, and the part of
// the desired document inside it.
type lockedPlan struct {
	// slug is the Company's live slug, equal to the artifact's once checked.
	slug string
	// projects are the Projects in scope by key, each row-locked. The apply
	// adds the Projects it creates.
	projects map[string]generated.Project
	// access is the actor's standing on each Project in scope.
	access map[string]auth.ProjectAccess
	// outside are the Projects the actor reads that the apply leaves alone.
	outside map[string]planProjectStanding
	// lost are the Projects the plan read as writable that the applier can
	// no longer write, locked with their Tasks until setAsideLostProjects
	// compares them with the plan.
	lost map[string]generated.Project
	// unreadable are the keys of Projects the plan read as writable that
	// the applier can no longer read.
	unreadable map[string]bool
	// lockIDs are the Projects whose Task chats, Tasks and dependency edges
	// are locked: the scope and the lost Projects.
	lockIDs      []uuid.UUID
	tasks        map[string]persistedTask
	dependencies map[string]persistedDependency
	// desired is the artifact's desired document narrowed to the scope.
	desired PlanDocument
	base    *PlanBase
	// skipped are the changes the apply leaves undone, for its result.
	skipped []PlanSkip
}

// lockPlanForApply is the preamble every Company Plan entry point shares, in
// the one order: read the live Company row (any failure is ErrPlanNotFound,
// so a deleting or vanished Company is indistinguishable from one the caller
// may not see), refuse a slug mismatch before taking any lock, take the
// scope's locks (lockPlanScope), then the Tasks and dependency edges of the
// Projects in scope, narrow the desired document to the scope and refuse new
// slugs in the server's label namespace. Prepare and Execute both go through
// it, so the rules a person approves against are the rules that apply. The
// caller holds the Company lock.
func lockPlanForApply(
	ctx context.Context,
	q *generated.Queries,
	rule planScopeRule,
	artifact PlanArtifact,
) (lockedPlan, error) {
	companyID := rule.actor.Principal.CompanyID
	company, err := q.GetCompanyByID(ctx, companyID)
	if err != nil {
		return lockedPlan{}, ErrPlanNotFound
	}
	if company.Slug != artifact.CompanySlug {
		return lockedPlan{}, errPlanSlugChanged
	}
	locked, err := lockPlanScope(ctx, q, rule, artifact.Desired)
	if err != nil {
		return lockedPlan{}, err
	}
	locked.slug = company.Slug
	locked.base = rule.base
	actorID, ids := rule.actor.Principal.UserID, locked.lockIDs
	taskRows, err := q.LockCompanyPlanTasks(ctx, generated.LockCompanyPlanTasksParams{
		ActorUserID: actorID, CompanyID: companyID, ProjectIds: ids,
	})
	if err != nil {
		return lockedPlan{}, fmt.Errorf("lock Company Plan Tasks: %w", err)
	}
	locked.tasks = make(map[string]persistedTask, len(taskRows))
	for _, row := range taskRows {
		locked.tasks[row.Key] = persistedTask(row)
	}
	edges, err := q.LockCompanyPlanTaskDependencies(ctx, generated.LockCompanyPlanTaskDependenciesParams{
		ActorUserID: actorID, CompanyID: companyID, ProjectIds: ids,
	})
	if err != nil {
		return lockedPlan{}, fmt.Errorf("lock Company Plan Task dependencies: %w", err)
	}
	locked.dependencies = make(map[string]persistedDependency, len(edges))
	for _, edge := range edges {
		locked.dependencies[dependencyIdentity(edge.DependentTaskID, edge.PrerequisiteTaskID)] = persistedDependency(
			edge,
		)
	}
	locked.setAsideLostProjects(artifact.Desired)
	locked.desired, err = locked.scopeDesired(artifact.Desired, rule.actor.TaskProjectID.Valid)
	if err != nil {
		return lockedPlan{}, err
	}
	if rule.base != nil {
		locked.holdUnpermittedArchives()
	}
	if err := refuseLabelShapedNewKeys(locked.desired, locked.tasks, company.TaskPrefix); err != nil {
		return lockedPlan{}, err
	}
	return locked, nil
}

// lockPlanScope takes the locks a Company Plan entry point takes before any
// Task row, in the ladder's order: the dependency-graph lock, the Projects
// the actor reads (sorted into the scope and the rest), then the Task chats
// of the Projects whose Tasks it will lock, before any Task row, which keeps
// the Conversation -> Run -> Task order of the terminal paths. The caller
// holds the Company and member locks and no Conversation or Run lock. The
// Approval raise takes these through LockCompanyPlanScope before it locks
// its origin chat and Run.
func lockPlanScope(
	ctx context.Context,
	q *generated.Queries,
	rule planScopeRule,
	desired PlanDocument,
) (lockedPlan, error) {
	companyID := rule.actor.Principal.CompanyID
	if err := q.LockTaskDependencyGraph(ctx, companyID); err != nil {
		return lockedPlan{}, fmt.Errorf("lock Task dependency graph: %w", err)
	}
	locked, err := lockPlanProjects(ctx, q, rule, desired)
	if err != nil {
		return lockedPlan{}, err
	}
	if _, err := q.LockCompanyPlanTaskConversations(ctx, generated.LockCompanyPlanTaskConversationsParams{
		ActorUserID: rule.actor.Principal.UserID, CompanyID: companyID, ProjectIds: locked.lockIDs,
	}); err != nil {
		return lockedPlan{}, fmt.Errorf("lock Company Plan Task Conversations: %w", err)
	}
	return locked, nil
}

// lockPlanProjects locks the Projects the actor reads and sorts them into the
// scope and the rest. An apply of a Task-chat plan whose base names another
// Project is stale: it was frozen before a Task-chat plan reached its Task's
// Project alone, and its preview showed changes this apply would not make.
func lockPlanProjects(
	ctx context.Context,
	q *generated.Queries,
	rule planScopeRule,
	desired PlanDocument,
) (lockedPlan, error) {
	taskProject := rule.actor.TaskProjectID
	read := map[uuid.UUID]PlanBaseEntity{}
	if rule.base != nil {
		for _, entity := range rule.base.Projects {
			id, err := util.ParseGoUUID(entity.ID)
			if err != nil {
				continue
			}
			if taskProject.Valid && id != taskProject.UUID {
				return lockedPlan{}, errPlanStale
			}
			read[id] = entity
		}
	}
	markedReadOnly := map[string]bool{}
	for _, project := range desired.Projects {
		markedReadOnly[project.Key] = project.ReadOnly
	}
	rows, err := q.LockCompanyPlanProjects(ctx, generated.LockCompanyPlanProjectsParams{
		CompanyID:       rule.actor.Principal.CompanyID,
		ActorUserID:     rule.actor.Principal.UserID,
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
	})
	if err != nil {
		return lockedPlan{}, fmt.Errorf("lock Company Plan Projects: %w", err)
	}
	locked := lockedPlan{
		projects:   map[string]generated.Project{},
		access:     map[string]auth.ProjectAccess{},
		outside:    map[string]planProjectStanding{},
		lost:       map[string]generated.Project{},
		unreadable: map[string]bool{},
		lockIDs:    []uuid.UUID{},
	}
	for _, row := range rows {
		project := row.Project
		access := auth.ProjectAccessOf(auth.ProjectAccessFacts{
			ProjectID: project.ID, CompanyID: project.CompanyID, Visibility: project.Visibility,
			Via: row.Via, Management: row.Management, Revision: project.AccessVersion,
			GrantedPermissions: row.GrantedPermissions, GroupPermissions: row.GroupPermissions,
		})
		var entity *PlanBaseEntity
		if value, listed := read[project.ID]; listed {
			entity = &value
		}
		delete(read, project.ID)
		standing := classifyPlanProject(
			rule,
			project.ID,
			access.Can(auth.ProjectPermissionTasksWrite),
			entity,
			markedReadOnly[project.Key],
		)
		switch standing {
		case planProjectInScope:
			locked.projects[project.Key] = project
			locked.access[project.Key] = access
		case planProjectWriteLost:
			locked.outside[project.Key] = standing
			locked.lost[project.Key] = project
		default:
			locked.outside[project.Key] = standing
			continue
		}
		locked.lockIDs = append(locked.lockIDs, project.ID)
	}
	// What the base still lists is a Project the applier read as writable
	// when the plan was read and can no longer read.
	for _, entity := range read {
		if !entity.ReadOnly {
			locked.unreadable[entity.Key] = true
		}
	}
	return locked, nil
}

// setAsideLostProjects takes the Projects the applier can no longer write
// out of the apply. One the plan changes is listed as skipped; one it
// restates as it is is left alone without a word.
func (l *lockedPlan) setAsideLostProjects(desired PlanDocument) {
	for _, key := range sortedKeys(l.lost) {
		project := l.lost[key]
		part := PlanDocument{Projects: []PlanProject{}, Tasks: []PlanTask{}}
		for _, planned := range desired.Projects {
			if planned.Key == key {
				part.Projects = append(part.Projects, planned)
			}
		}
		for _, planned := range desired.Tasks {
			if planned.ProjectKey == key {
				part.Tasks = append(part.Tasks, planned)
			}
		}
		tasks, dependencies := l.takeProjectRows(project.ID)
		if len(planProjectChanges(part, map[string]generated.Project{key: project})) > 0 ||
			len(computeCompanyPlanChanges(part, tasks, dependencies)) > 0 {
			l.skip(key, "tasks", auth.ProjectPermissionTasksWrite)
		}
	}
}

// takeProjectRows removes one Project's locked Tasks and dependency edges
// from the apply and returns them. An edge never leaves its Project.
func (l *lockedPlan) takeProjectRows(projectID uuid.UUID) (map[string]persistedTask, map[string]persistedDependency) {
	tasks := map[string]persistedTask{}
	ids := map[uuid.UUID]bool{}
	for key, value := range l.tasks {
		if value.ProjectID == projectID {
			tasks[key] = value
			ids[value.ID] = true
			delete(l.tasks, key)
		}
	}
	dependencies := map[string]persistedDependency{}
	for identity, edge := range l.dependencies {
		if ids[edge.DependentTaskID] {
			dependencies[identity] = edge
			delete(l.dependencies, identity)
		}
	}
	return tasks, dependencies
}

func (l *lockedPlan) skip(key, operation string, permission auth.ProjectPermission) {
	l.skipped = append(l.skipped, PlanSkip{
		EntityType: "project", Key: key, Operation: operation, Permission: permission,
	})
}

// scopeDesired narrows the desired document to the scope. Entries for a
// Project the actor reads outside the scope, and for its Tasks, are dropped:
// the apply neither changes nor deletes them. So are entries marked
// read-only. A key the actor cannot read is kept as a new Project, exactly
// like a key nobody has used; its create then fails on the key like any
// other taken key. The exception is a key the plan read as writable that
// the applier can no longer read: its entries are dropped and listed as
// skipped. A Task-chat plan creates no Project. A plan that names a Project
// in reach that it did not read, or restates a Task in scope that has gone,
// is stale.
func (l *lockedPlan) scopeDesired(desired PlanDocument, taskBound bool) (PlanDocument, error) {
	scoped := PlanDocument{
		FormatVersion: desired.FormatVersion, Company: desired.Company,
		Projects: []PlanProject{}, Tasks: []PlanTask{},
	}
	kept := map[string]bool{}
	for _, project := range desired.Projects {
		_, inScope := l.projects[project.Key]
		standing, readable := l.outside[project.Key]
		switch {
		case inScope:
		case readable && standing == planProjectUnlisted:
			return PlanDocument{}, errPlanStale
		case readable, project.ReadOnly, taskBound:
			continue
		case l.unreadable[project.Key]:
			l.skip(project.Key, "tasks", auth.ProjectPermissionRead)
			continue
		}
		kept[project.Key] = true
		scoped.Projects = append(scoped.Projects, project)
	}
	readTaskKeys := map[string]bool{}
	if l.base != nil {
		for _, entity := range l.base.Tasks {
			readTaskKeys[entity.Key] = true
		}
	}
	for _, planned := range desired.Tasks {
		current, exists := l.tasks[planned.Key]
		if exists && l.projects[planned.ProjectKey].ID != current.ProjectID {
			return PlanDocument{}, fmt.Errorf("%w: Task Project is immutable", ErrPlanInvalidInput)
		}
		if !kept[planned.ProjectKey] {
			continue
		}
		if !exists && readTaskKeys[planned.Key] {
			return PlanDocument{}, errPlanStale
		}
		scoped.Tasks = append(scoped.Tasks, planned)
	}
	return scoped, nil
}

// holdUnpermittedArchives leaves alone, and lists as skipped, every Project
// in scope the apply would archive, or empty by leaving it out of the plan
// while it has open Tasks, where the applier lacks project.archive (ADR
// 0017). Neither the Project nor any of its Tasks changes: the rest of the
// plan applies.
func (l *lockedPlan) holdUnpermittedArchives() {
	named := map[string]PlanProject{}
	for _, project := range l.desired.Projects {
		named[project.Key] = project
	}
	open := map[uuid.UUID]bool{}
	for _, value := range l.tasks {
		if !terminalTaskStatus(value.Status) {
			open[value.ProjectID] = true
		}
	}
	held := map[string]bool{}
	for _, key := range sortedKeys(l.projects) {
		project := l.projects[key]
		if project.ArchivedAt.Valid || l.access[key].Can(auth.ProjectPermissionArchive) {
			continue
		}
		planned, isNamed := named[key]
		if isNamed && !planned.Archived || !isNamed && !open[project.ID] {
			continue
		}
		l.skip(key, "archive", auth.ProjectPermissionArchive)
		l.takeProjectRows(project.ID)
		delete(l.projects, key)
		delete(l.access, key)
		l.outside[key] = planProjectReadOnly
		held[key] = true
	}
	if len(held) == 0 {
		return
	}
	scoped := PlanDocument{
		FormatVersion: l.desired.FormatVersion, Company: l.desired.Company,
		Projects: []PlanProject{}, Tasks: []PlanTask{},
	}
	for _, project := range l.desired.Projects {
		if !held[project.Key] {
			scoped.Projects = append(scoped.Projects, project)
		}
	}
	for _, planned := range l.desired.Tasks {
		if !held[planned.ProjectKey] {
			scoped.Tasks = append(scoped.Tasks, planned)
		}
	}
	l.desired = scoped
}

// matchesBase decides staleness over the scope: every Project in it at the
// revision the plan read, every Task in it read at its revision (and none
// added since), and exactly the dependency edges read among them. Base
// entries outside the scope are not compared: the apply does not touch them.
func (l lockedPlan) matchesBase() bool {
	if l.base == nil {
		return true
	}
	read := map[string]PlanBaseEntity{}
	for _, entity := range l.base.Projects {
		read[entity.ID] = entity
	}
	for key, project := range l.projects {
		entity, ok := read[project.ID.String()]
		if !ok || entity.Key != key || entity.Revision != project.Version {
			return false
		}
	}
	liveIDs := map[string]bool{}
	liveKeys := map[string]bool{}
	for key, value := range l.tasks {
		liveIDs[value.ID.String()] = true
		liveKeys[key] = true
	}
	var tasks []PlanBaseEntity
	for _, entity := range l.base.Tasks {
		if liveIDs[entity.ID] || liveKeys[entity.Key] {
			tasks = append(tasks, entity)
		}
	}
	var dependencies []PlanBaseDependency
	for _, edge := range l.base.Dependencies {
		if liveIDs[edge.DependentTaskID] {
			dependencies = append(dependencies, edge)
		}
	}
	return baseMatches(tasks, taskBase(l.tasks)) &&
		dependencyBaseMatches(dependencies, dependencyBase(l.dependencies))
}

// snapshot is the base an Approval freezes: the scope as the raise read it.
// It marks no Project read-only: the decider's own access decides the
// apply.
func (l lockedPlan) snapshot() PlanBase {
	return PlanBase{
		Projects:     projectBase(l.projects),
		Tasks:        taskBase(l.tasks),
		Dependencies: dependencyBase(l.dependencies),
	}
}

func projectBase(projects map[string]generated.Project) []PlanBaseEntity {
	result := make([]PlanBaseEntity, 0, len(projects))
	for _, key := range sortedKeys(projects) {
		p := projects[key]
		result = append(result, PlanBaseEntity{ID: p.ID.String(), Key: p.Key, Revision: p.Version})
	}
	return result
}

func validatePlanProjects(
	desired PlanDocument,
	projects map[string]generated.Project,
	tasks map[string]persistedTask,
) error {
	wanted := map[string]PlanProject{}
	for _, p := range desired.Projects {
		wanted[p.Key] = p
		old, exists := projects[p.Key]
		if !exists && p.Archived {
			return ErrPlanInvalidInput
		}
		if exists && old.ArchivedAt.Valid &&
			(!p.Archived || p.Title != old.Title || p.Description != old.Description) {
			return ErrPlanConflict
		}
	}
	for _, task := range desired.Tasks {
		project, exists := projects[task.ProjectKey]
		old, taskExists := tasks[task.Key]
		if taskExists && (!exists || old.ProjectID != project.ID) {
			return fmt.Errorf("%w: Task Project is immutable", ErrPlanInvalidInput)
		}
		if exists && project.ArchivedAt.Valid && !taskExists {
			return ErrPlanConflict
		}
		if wanted[task.ProjectKey].Archived && task.Status != "done" && task.Status != "cancelled" {
			return fmt.Errorf("%w: archived Project has open Tasks", ErrPlanConflict)
		}
	}
	return nil
}

func planProjectChanges(desired PlanDocument, projects map[string]generated.Project) []PlanChange {
	changes := []PlanChange{}
	for _, p := range desired.Projects {
		old, exists := projects[p.Key]
		change := PlanChange{EntityType: "project", Key: p.Key}
		switch {
		case !exists:
			change.Operation = "create"
		case p.Archived && !old.ArchivedAt.Valid:
			change.Operation = "archive"
		case p.Title != old.Title || p.Description != old.Description:
			change.Operation = "update"
		default:
			continue
		}
		for _, f := range []PlanFieldChange{{Field: "title", Before: old.Title, After: p.Title}, {Field: "description", Before: old.Description, After: p.Description}} {
			if f.Before != f.After {
				change.Fields = append(change.Fields, f)
			}
		}
		changes = append(changes, change)
	}
	return changes
}

// applyPlanProjects creates and renames the plan's Projects. Renaming needs
// project.settings.update, as a Project PATCH does; without it the Project
// keeps its title and description and the apply lists it as skipped.
func applyPlanProjects(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	applier planApplier,
	desired PlanDocument,
	locked lockedPlan,
	result *PlanApplyResult,
) error {
	principal := applier.principal
	projects := locked.projects
	for _, p := range desired.Projects {
		row, exists := projects[p.Key]
		before := row
		name := EventProjectUpdated
		var err error
		switch {
		case !exists:
			row, err = q.CreateProject(
				ctx,
				generated.CreateProjectParams{
					CompanyID:   principal.CompanyID,
					Key:         p.Key,
					Title:       p.Title,
					Description: p.Description,
				},
			)
			if err == nil {
				// The applier creates the Project and, like POST /projects, holds
				// every Project permission on it (ADR 0017).
				err = grantProjectCreatorInTx(ctx, q, pending, principal, applier.actor(), row)
			}
			name = EventProjectCreated
			result.Created++
		case row.Title == p.Title && row.Description == p.Description:
			continue
		case !locked.access[p.Key].Can(auth.ProjectPermissionSettingsUpdate):
			result.skip("project", p.Key, "update", auth.ProjectPermissionSettingsUpdate)
			continue
		default:
			row, err = q.UpdateProject(
				ctx,
				generated.UpdateProjectParams{
					CompanyID:        principal.CompanyID,
					ID:               row.ID,
					Title:            p.Title,
					Description:      p.Description,
					ExpectedRevision: row.Version,
				},
			)
			result.Updated++
		}
		if err != nil {
			return mapPlanMutationError("write Project", err)
		}
		if !exists {
			if err := canvas.EnsureOverviewInTx(ctx, q, principal, row); err != nil {
				return err
			}
		}
		projects[p.Key] = row
		var changed []string
		if exists {
			changed = projectChangedFields(before, row)
		}
		if err := stageProjectEventAsInTx(
			ctx, q, pending, principal.CompanyID, applier.actor(), name,
			projectResponse(row, auth.ProjectAccess{}), changed,
		); err != nil {
			return err
		}
	}
	return nil
}

// archivePlanProjects archives the Projects the plan marks archived. The
// applier holds project.archive on each: holdUnpermittedArchives took every
// other one out of the apply.
func archivePlanProjects(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	applier planApplier,
	desired PlanDocument,
	locked lockedPlan,
	result *PlanApplyResult,
) error {
	principal := applier.principal
	for _, p := range desired.Projects {
		row := locked.projects[p.Key]
		if !p.Archived || row.ArchivedAt.Valid {
			continue
		}
		open, err := q.ProjectHasOpenTasks(
			ctx,
			generated.ProjectHasOpenTasksParams{CompanyID: principal.CompanyID, ProjectID: row.ID},
		)
		if err != nil {
			return err
		}
		if open {
			return ErrPlanConflict
		}
		row, err = q.ArchiveProject(
			ctx,
			generated.ArchiveProjectParams{
				CompanyID:        principal.CompanyID,
				ID:               row.ID,
				ExpectedRevision: row.Version,
			},
		)
		if err != nil {
			return mapPlanMutationError("archive Project", err)
		}
		result.Updated++
		if err := stageProjectEventAsInTx(
			ctx, q, pending, principal.CompanyID, applier.actor(), EventProjectArchived,
			projectResponse(row, auth.ProjectAccess{}), nil,
		); err != nil {
			return err
		}
	}
	return nil
}
