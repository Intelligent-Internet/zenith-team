package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// Project access errors (ADR 0017). A Project or Task the caller cannot see
// answers 404 (ErrProjectNotVisible on Project surfaces, ErrTaskNotVisible on
// Task surfaces, so each maps beside its own not-found error); a visible one
// missing the permission answers 403 project_permission_required.
var (
	ErrProjectNotVisible         = errors.New("project not visible")
	ErrTaskNotVisible            = errors.New("task not visible")
	ErrProjectPermissionRequired = errors.New("project permission required")
	// ErrNoEligibleResolver refuses a Task-origin Approval or Question that
	// nobody may decide: no reader of the Task chat's Project holds the
	// deciding permission (409 no_eligible_resolver).
	ErrNoEligibleResolver = errors.New("no eligible resolver")
)

// ProjectAccessVia says why a person reads a Project: their own grant row,
// a Group they are in, or the Company-visible floor, in that order when more
// than one holds. The zero value means they hold no project.read (at most the
// management bridge). It mirrors project_access.via.
type ProjectAccessVia string

const (
	ProjectAccessViaGrant   ProjectAccessVia = "grant"
	ProjectAccessViaGroup   ProjectAccessVia = "group"
	ProjectAccessViaCompany ProjectAccessVia = "company"
)

// ProjectAccess is one person's resolved standing on one Project. Permissions
// is the effective set, the union of every source: their own granted rows,
// the rows of each Group they are in, the Company default set on a
// Company-visible Project, and the management permissions when the person's
// Company role holds projects.manage. A content permission (anything outside
// ManagementProjectPermissions) counts only with a project_access row, so a
// bridge holder who cannot read sees exactly the three management names.
type ProjectAccess struct {
	ProjectID   uuid.UUID
	CompanyID   uuid.UUID
	Visibility  ProjectVisibility
	Permissions []ProjectPermission
	// Via is ProjectAccessViaGrant, ProjectAccessViaGroup,
	// ProjectAccessViaCompany or "" when the person holds no project.read.
	Via        ProjectAccessVia
	Management bool
	Revision   int64
	// Granted is the person's own fenced grant rows, sorted and never nil:
	// what the person grant editor shows and changes. It is empty unless the
	// person reads the Project.
	Granted []ProjectPermission
	// GroupGranted is what the person's Groups hold on the Project through
	// memberships written since their latest (re)join, sorted, without
	// duplicates and never nil. Like Granted it is empty unless the person
	// reads the Project; leaving a Group never touches Granted.
	GroupGranted []ProjectPermission
}

// Can reports whether the effective set holds the permission.
func (a ProjectAccess) Can(p ProjectPermission) bool {
	return slices.Contains(a.Permissions, p)
}

// Visible reports whether the person may see the Project at all: a reader,
// or a projects.manage holder through the management bridge.
func (a ProjectAccess) Visible() bool {
	return a.Via != "" || a.Management
}

// ResolveProjectAccess reads the principal's standing on one Project in the
// principal's resolved Company. A principal without a resolved Company holds
// nothing. A Project that does not exist in the Company answers
// ErrProjectNotVisible; one that exists but is invisible to the principal
// comes back with Visible() false, for callers that must tell the two apart
// only internally.
func ResolveProjectAccess(
	ctx context.Context,
	q *generated.Queries,
	principal UserPrincipal,
	projectID uuid.UUID,
) (ProjectAccess, error) {
	return ResolveMemberProjectAccess(ctx, q, principal.CompanyID, principal.UserID, projectID)
}

// ResolveMemberProjectAccess reads one person's standing on one Project by
// id. It is the core ResolveProjectAccess wraps, for checks about someone
// other than the caller (a Task assignee at enqueue, the target of a grant
// change, a decider or Inbox recipient): it answers what that person holds
// and authorizes nobody, so no second principal is built. A nil Company or
// person holds nothing; the other results match ResolveProjectAccess.
func ResolveMemberProjectAccess(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID, projectID uuid.UUID,
) (ProjectAccess, error) {
	if companyID == uuid.Nil || userID == uuid.Nil {
		return ProjectAccess{}, ErrProjectNotVisible
	}
	row, err := q.ResolveProjectAccess(ctx, generated.ResolveProjectAccessParams{
		CompanyID:       companyID,
		ActorUserID:     userID,
		ProjectID:       projectID,
		ManagementRoles: RolesWith(PermissionProjectsManage),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAccess{}, ErrProjectNotVisible
	}
	if err != nil {
		return ProjectAccess{}, fmt.Errorf("resolve project access: %w", err)
	}
	return projectAccessFromRow(row), nil
}

// ResolvePeopleProjectAccess is ResolveMemberProjectAccess for many people on
// many Projects in one read (ResolvePeopleProjectAccess the query), for a
// change that moves several people's access on several Projects at once. The
// answer is by Project, then person: every Project of the Company named is
// there with every person named, and each pair holds exactly what
// ResolveMemberProjectAccess answers for it. A Project not in the Company is
// left out; a nil Company, or no Project or no person, answers an empty map.
func ResolvePeopleProjectAccess(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	projectIDs, userIDs []uuid.UUID,
) (map[uuid.UUID]map[uuid.UUID]ProjectAccess, error) {
	access := map[uuid.UUID]map[uuid.UUID]ProjectAccess{}
	if companyID == uuid.Nil || len(projectIDs) == 0 || len(userIDs) == 0 {
		return access, nil
	}
	compare := func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }
	projectIDs, userIDs = slices.Clone(projectIDs), slices.Clone(userIDs)
	slices.SortFunc(projectIDs, compare)
	slices.SortFunc(userIDs, compare)
	rows, err := q.ResolvePeopleProjectAccess(ctx, generated.ResolvePeopleProjectAccessParams{
		CompanyID:       companyID,
		ProjectIds:      slices.Compact(projectIDs),
		UserIds:         slices.Compact(userIDs),
		ManagementRoles: RolesWith(PermissionProjectsManage),
	})
	if err != nil {
		return nil, fmt.Errorf("resolve people's project access: %w", err)
	}
	for _, row := range rows {
		if access[row.ID] == nil {
			access[row.ID] = map[uuid.UUID]ProjectAccess{}
		}
		access[row.ID][row.UserID] = ProjectAccessOf(ProjectAccessFacts{
			ProjectID:          row.ID,
			CompanyID:          row.CompanyID,
			Visibility:         row.Visibility,
			Via:                row.Via,
			Management:         row.Management,
			Revision:           row.AccessVersion,
			GrantedPermissions: row.GrantedPermissions,
			GroupPermissions:   row.GroupPermissions,
		})
	}
	return access, nil
}

// ProjectPermissionSets are the three sets every project_permission_held call
// binds, as the []string a sqlc text[] parameter takes: the Company default
// set, the management set, and the Company roles holding projects.manage.
// Binding them from this one place keeps the write fence, the Run claim fence
// and HoldsProjectPermission on the same rule.
type ProjectPermissionSets struct {
	CompanyDefaultPermissions []string
	ManagementPermissions     []string
	ManagementRoles           []string
}

// ProjectPermissionHeldSets returns the sets project_permission_held binds.
func ProjectPermissionHeldSets() ProjectPermissionSets {
	return ProjectPermissionSets{
		CompanyDefaultPermissions: ProjectPermissionStrings(CompanyProjectDefaultPermissions()),
		ManagementPermissions:     ProjectPermissionStrings(ManagementProjectPermissions()),
		ManagementRoles:           RolesWith(PermissionProjectsManage),
	}
}

// HoldsProjectPermission reports whether the person holds the permission on
// the Project right now, through project_permission_held. Use it inside the
// write transaction as the fence for one person; lists join the views.
func HoldsProjectPermission(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID, userID uuid.UUID,
	permission ProjectPermission,
) (bool, error) {
	if companyID == uuid.Nil || userID == uuid.Nil {
		return false, nil
	}
	sets := ProjectPermissionHeldSets()
	held, err := q.ProjectPermissionHeld(ctx, generated.ProjectPermissionHeldParams{
		CompanyID:                 companyID,
		ProjectID:                 projectID,
		UserID:                    userID,
		Permission:                string(permission),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
	})
	if err != nil {
		return false, fmt.Errorf("check project permission: %w", err)
	}
	return held, nil
}

// RequireProjectPermissionHeld is HoldsProjectPermission as a refusal: a
// person who does not hold the permission right now gets
// ErrProjectPermissionRequired. Callers use it after they already showed the
// Project is readable (a reader-gated lock or read), under a Conversation
// lock where the Project lock may no longer be taken.
func RequireProjectPermissionHeld(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID, userID uuid.UUID,
	permission ProjectPermission,
) error {
	held, err := HoldsProjectPermission(ctx, q, companyID, projectID, userID, permission)
	if err != nil {
		return err
	}
	if !held {
		return ErrProjectPermissionRequired
	}
	return nil
}

// ProjectAccessFacts is one person's standing on one Project as a read that
// already decided access returns it: the Project's visibility and access
// revision, the person's project_access via ("" when none), whether their
// Company role holds the management bridge, their fenced grant rows, and
// what their Groups hold there through fenced memberships. Project list and
// read queries return these beside each row, so the rule stays inside the
// read and ProjectAccessOf only states the effective set.
type ProjectAccessFacts struct {
	ProjectID          uuid.UUID
	CompanyID          uuid.UUID
	Visibility         string
	Via                string
	Management         bool
	Revision           int64
	GrantedPermissions []string
	GroupPermissions   []string
}

// projectAccessFromRow applies the effective-set rule to one resolved row.
func projectAccessFromRow(row generated.ResolveProjectAccessRow) ProjectAccess {
	return ProjectAccessOf(ProjectAccessFacts{
		ProjectID:          row.ID,
		CompanyID:          row.CompanyID,
		Visibility:         row.Visibility,
		Via:                row.Via,
		Management:         row.Management,
		Revision:           row.AccessVersion,
		GrantedPermissions: row.GrantedPermissions,
		GroupPermissions:   row.GroupPermissions,
	})
}

// ProjectAccessOf applies the effective-set rule to facts a read returned.
// Grant rows, the person's own and their Groups', count only for a reader:
// project_grants_read_fkey and project_group_grants_read_fkey store no row
// without the grantee's project.read row, and project_access admits every
// person whose own read row, or a Group's read row through their membership,
// passes the rejoin fence. Without read the person holds at most the
// management bridge. Permissions, Granted and GroupGranted are never nil.
func ProjectAccessOf(facts ProjectAccessFacts) ProjectAccess {
	access := ProjectAccess{
		ProjectID:    facts.ProjectID,
		CompanyID:    facts.CompanyID,
		Visibility:   ProjectVisibility(facts.Visibility),
		Via:          ProjectAccessVia(facts.Via),
		Management:   facts.Management,
		Revision:     facts.Revision,
		Granted:      []ProjectPermission{},
		GroupGranted: []ProjectPermission{},
	}
	management := ManagementProjectPermissions()
	permissions := make([]ProjectPermission, 0,
		len(facts.GrantedPermissions)+len(facts.GroupPermissions)+len(management))
	if access.Via != "" {
		access.Granted = sortedProjectPermissions(facts.GrantedPermissions)
		access.GroupGranted = sortedProjectPermissions(facts.GroupPermissions)
		permissions = append(permissions, access.Granted...)
		permissions = append(permissions, access.GroupGranted...)
		if access.Visibility == ProjectVisibilityCompany {
			permissions = append(permissions, CompanyProjectDefaultPermissions()...)
		}
	}
	if access.Management {
		permissions = append(permissions, management...)
	}
	slices.Sort(permissions)
	access.Permissions = slices.Compact(permissions)
	return access
}

// sortedProjectPermissions turns the names a read returned into a sorted set
// without duplicates, never nil.
func sortedProjectPermissions(names []string) []ProjectPermission {
	permissions := make([]ProjectPermission, len(names))
	for i, name := range names {
		permissions[i] = ProjectPermission(name)
	}
	slices.Sort(permissions)
	return slices.Compact(permissions)
}

// LockProjectAccess holds the Project row FOR SHARE, then resolves the
// principal's standing under that lock, so a concurrent grant or visibility
// change (which takes FOR UPDATE) waits for this transaction. q must be bound
// to the caller's transaction. Lock order: company -> member -> project ->
// conversation -> run.
func LockProjectAccess(
	ctx context.Context,
	q *generated.Queries,
	principal UserPrincipal,
	projectID uuid.UUID,
) (ProjectAccess, error) {
	if principal.CompanyID == uuid.Nil || principal.UserID == uuid.Nil {
		return ProjectAccess{}, ErrProjectNotVisible
	}
	_, err := q.LockProjectForAccessRead(ctx, generated.LockProjectForAccessReadParams{
		CompanyID: principal.CompanyID,
		ProjectID: projectID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAccess{}, ErrProjectNotVisible
	}
	if err != nil {
		return ProjectAccess{}, fmt.Errorf("lock project access: %w", err)
	}
	return ResolveProjectAccess(ctx, q, principal, projectID)
}

// RequireProjectVisible resolves the principal's standing and answers
// ErrProjectNotVisible unless they read the Project or hold the management
// bridge.
func RequireProjectVisible(
	ctx context.Context,
	q *generated.Queries,
	principal UserPrincipal,
	projectID uuid.UUID,
) (ProjectAccess, error) {
	access, err := ResolveProjectAccess(ctx, q, principal, projectID)
	if err != nil {
		return ProjectAccess{}, err
	}
	if !access.Visible() {
		return ProjectAccess{}, ErrProjectNotVisible
	}
	return access, nil
}

// RequireProjectPermission answers ErrProjectNotVisible for a Project the
// principal cannot see and ErrProjectPermissionRequired for a visible one
// without the permission.
func RequireProjectPermission(
	ctx context.Context,
	q *generated.Queries,
	principal UserPrincipal,
	projectID uuid.UUID,
	permission ProjectPermission,
) (ProjectAccess, error) {
	access, err := RequireProjectVisible(ctx, q, principal, projectID)
	if err != nil {
		return ProjectAccess{}, err
	}
	return access, requirePermission(access, permission)
}

// RequireTaskPermission checks a permission on the Task's Project. A Task
// that does not exist, or whose Project the principal cannot read, answers
// ErrTaskNotVisible (Task surfaces map it beside their own not-found error):
// the management bridge never opens a Task.
func RequireTaskPermission(
	ctx context.Context,
	q *generated.Queries,
	principal UserPrincipal,
	taskID uuid.UUID,
	permission ProjectPermission,
) (ProjectAccess, error) {
	if principal.CompanyID == uuid.Nil || principal.UserID == uuid.Nil {
		return ProjectAccess{}, ErrTaskNotVisible
	}
	projectID, err := q.GetTaskProjectID(ctx, generated.GetTaskProjectIDParams{
		CompanyID: principal.CompanyID,
		TaskID:    taskID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAccess{}, ErrTaskNotVisible
	}
	if err != nil {
		return ProjectAccess{}, fmt.Errorf("read task project: %w", err)
	}
	return RequireProjectContentPermission(ctx, q, principal, projectID, permission)
}

// TaskChatProjectID returns the Project a Task chat belongs to. ok is false
// for any other kind of chat, for a chat that does not exist, and for a Task
// chat whose Task was deleted before migration 000086 and so recorded no
// Project (ADR 0017's legacy pocket): no Project governs those, and the
// caller applies its Company rule.
func TaskChatProjectID(
	ctx context.Context,
	q *generated.Queries,
	companyID, conversationID uuid.UUID,
) (uuid.UUID, bool, error) {
	if companyID == uuid.Nil || conversationID == uuid.Nil {
		return uuid.Nil, false, nil
	}
	row, err := q.GetConversationProjectID(ctx, generated.GetConversationProjectIDParams{
		CompanyID:      companyID,
		ConversationID: conversationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("read chat project: %w", err)
	}
	if row.Kind != "task" || !row.ProjectID.Valid {
		return uuid.Nil, false, nil
	}
	return row.ProjectID.UUID, true, nil
}

// RequireTaskChatPermission checks a permission on the Project a Task chat
// belongs to, for acting in that chat: deciding a Task-origin Approval or
// Question, or stopping the chat's Run. It keys on the chat, not the
// Task, so the answer holds after the Task is deleted (the chat keeps its
// Project). A caller who does not read the Project gets ErrTaskNotVisible,
// as on every Task surface, and the management bridge opens nothing here; a
// reader without the permission gets ErrProjectPermissionRequired. The
// Company catalog is not consulted. scoped is false when the chat has no
// Project (see TaskChatProjectID), and nothing was checked.
func RequireTaskChatPermission(
	ctx context.Context,
	q *generated.Queries,
	principal UserPrincipal,
	conversationID uuid.UUID,
	permission ProjectPermission,
) (access ProjectAccess, scoped bool, err error) {
	if principal.CompanyID == uuid.Nil || principal.UserID == uuid.Nil {
		return ProjectAccess{}, true, ErrTaskNotVisible
	}
	projectID, scoped, err := TaskChatProjectID(ctx, q, principal.CompanyID, conversationID)
	if err != nil || !scoped {
		return ProjectAccess{}, false, err
	}
	access, err = RequireProjectContentPermission(ctx, q, principal, projectID, permission)
	return access, true, err
}

// RequireProjectContentPermission checks a permission for work inside the
// Project, where the content is what is at stake (a Task, a Task chat, a
// Task's result): a caller who does not read the Project gets
// ErrTaskNotVisible, as on every Task surface, because the management bridge
// opens no content; a reader without the permission gets
// ErrProjectPermissionRequired.
func RequireProjectContentPermission(
	ctx context.Context,
	q *generated.Queries,
	principal UserPrincipal,
	projectID uuid.UUID,
	permission ProjectPermission,
) (ProjectAccess, error) {
	access, err := ResolveProjectAccess(ctx, q, principal, projectID)
	if errors.Is(err, ErrProjectNotVisible) {
		return ProjectAccess{}, ErrTaskNotVisible
	}
	if err != nil {
		return ProjectAccess{}, err
	}
	if access.Via == "" {
		return ProjectAccess{}, ErrTaskNotVisible
	}
	return access, requirePermission(access, permission)
}

// TaskChatDecidersInTx returns who may decide something raised in a Task
// chat: the readers of the chat's Project holding the permission
// (ProjectDecidersInTx, ADR 0017); they are also its recipients. scoped is
// false when the chat has no Project (TaskChatProjectID), and the caller
// keeps its Company rule. Like ProjectDecidersInTx it takes no lock.
func TaskChatDecidersInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, conversationID uuid.UUID,
	permission ProjectPermission,
) (deciders []uuid.UUID, scoped bool, err error) {
	projectID, scoped, err := TaskChatProjectID(ctx, q, companyID, conversationID)
	if err != nil || !scoped {
		return nil, false, err
	}
	deciders, err = ProjectDecidersInTx(ctx, q, companyID, projectID, permission)
	return deciders, true, err
}

// ProjectDecidersInTx lists, sorted, the readers of the Project who hold the
// deciding permission: the only people who may decide a Task-origin
// Approval or Question, and so their recipients
// (readers ∩ deciders, ADR 0017). The Company catalog qualifies nobody here.
// It takes no lock: callers already hold Conversation and Run locks, which
// the Project lock must precede.
func ProjectDecidersInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID uuid.UUID,
	permission ProjectPermission,
) ([]uuid.UUID, error) {
	sets := ProjectPermissionHeldSets()
	deciders, err := q.ListProjectDeciderIDs(ctx, generated.ListProjectDeciderIDsParams{
		CompanyID:                 companyID,
		ProjectID:                 projectID,
		Permission:                string(permission),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
	})
	if err != nil {
		return nil, fmt.Errorf("list project deciders: %w", err)
	}
	return deciders, nil
}

func requirePermission(access ProjectAccess, permission ProjectPermission) error {
	if !access.Can(permission) {
		return ErrProjectPermissionRequired
	}
	return nil
}

// ProjectAudienceInTx returns who may receive a frame about the Project. A
// Company-visible Project reaches every active member, so it answers
// companyWide with no ids. Otherwise it lists the readers, plus the active
// projects.manage holders when includeProjectsManageHolders is set (Project
// lifecycle and access frames only; Task-scoped frames never reach them),
// sorted and without duplicates.
//
// It takes no lock. Task-scoped producers call it while they already hold
// Conversation and Run locks, and a Project lock there would break the lock
// order (company -> member -> project -> conversation -> run). So an
// audience read just before a concurrent revocation commits still delivers
// that one frame, content included, to the person who lost access;
// project.access_changed then makes their client refetch, but cannot take
// the frame back. ADR 0017 accepts that window. A caller that needs the
// Project's access held steady takes LockProjectAccess first.
func ProjectAudienceInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID uuid.UUID,
	includeProjectsManageHolders bool,
) (userIDs []uuid.UUID, companyWide bool, err error) {
	project, err := q.GetProjectAccessFacts(ctx, generated.GetProjectAccessFactsParams{
		CompanyID: companyID,
		ProjectID: projectID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrProjectNotVisible
	}
	if err != nil {
		return nil, false, fmt.Errorf("read project audience: %w", err)
	}
	if ProjectVisibility(project.Visibility) == ProjectVisibilityCompany {
		return nil, true, nil
	}
	readers, err := q.ListProjectReaderIDs(ctx, generated.ListProjectReaderIDsParams{
		CompanyID: companyID,
		ProjectID: projectID,
	})
	if err != nil {
		return nil, false, fmt.Errorf("list project readers: %w", err)
	}
	userIDs = readers
	if includeProjectsManageHolders {
		managers, err := q.ListActiveCompanyMemberIDsByRoles(ctx, generated.ListActiveCompanyMemberIDsByRolesParams{
			CompanyID: companyID,
			Roles:     RolesWith(PermissionProjectsManage),
		})
		if err != nil {
			return nil, false, fmt.Errorf("list project managers: %w", err)
		}
		userIDs = append(userIDs, managers...)
	}
	slices.SortFunc(userIDs, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(userIDs), false, nil
}

// RemovedProjectGrant is one Project a Company revocation took the person's
// grant from, and the permissions that grant held (the rows that counted,
// sorted; empty when every row predated their latest rejoin).
type RemovedProjectGrant struct {
	ProjectID   uuid.UUID
	Permissions []ProjectPermission
}

// DeleteProjectGrantsForUserInTx removes every Project grant the person holds
// in the Company, advances each affected Project's access revision, and
// returns those Projects sorted by id with what the person held there. The
// Company revocation path calls it in its own transaction; staging events,
// recording Activity and cancelling Runs stay with the caller.
func DeleteProjectGrantsForUserInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID uuid.UUID,
) ([]RemovedProjectGrant, error) {
	rows, err := q.DeleteCompanyProjectGrantsForUser(ctx, generated.DeleteCompanyProjectGrantsForUserParams{
		CompanyID: companyID,
		UserID:    userID,
	})
	if err != nil {
		return nil, fmt.Errorf("delete project grants: %w", err)
	}
	removed := make([]RemovedProjectGrant, len(rows))
	for i, row := range rows {
		permissions := make([]ProjectPermission, len(row.Permissions))
		for j, name := range row.Permissions {
			permissions[j] = ProjectPermission(name)
		}
		removed[i] = RemovedProjectGrant{ProjectID: row.ID, Permissions: permissions}
	}
	return removed, nil
}
