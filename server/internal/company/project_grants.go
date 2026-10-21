package company

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Project grants (ADR 0017): permissions granted to one person on one
// Project. Every change here — a grant set, a removal, a Join, a visibility
// change, the creator's own grant — runs under the Company, member and
// Project locks, advances the Project's access revision, writes one Activity
// row naming the actor, the person and the sets before and after, stages
// project.access_changed for everyone whose view moved, and ends the Runs of
// anyone who lost project.read or project.tasks.execute.

// ProjectGrant is one person's access on a Project from every stored source:
// their own rows (Granted, empty when they reach the Project only through a
// Group), what they hold with every source (Effective: their rows, what
// their Groups hold there, the Company default set on a Company-visible
// Project, and the management permissions when their Company role holds
// projects.manage), and each source on its own (Sources). GrantedBy is
// the author of their most recent own row, nil without one; UpdatedAt is the
// latest change of any stored source.
type ProjectGrant struct {
	UserID    uuid.UUID                `json:"userId"`
	Granted   []auth.ProjectPermission `json:"granted"`
	Effective []auth.ProjectPermission `json:"effective"`
	Sources   []ProjectGrantSource     `json:"sources"`
	GrantedBy *uuid.UUID               `json:"grantedBy"`
	UpdatedAt time.Time                `json:"updatedAt"`
}

// ProjectGrantSourceVia names where one source of a person's access comes
// from.
type ProjectGrantSourceVia string

// The sources of a person's access on a Project, in the order they are
// listed.
const (
	ProjectGrantSourceGrant      ProjectGrantSourceVia = "grant"
	ProjectGrantSourceGroup      ProjectGrantSourceVia = "group"
	ProjectGrantSourceCompany    ProjectGrantSourceVia = "company"
	ProjectGrantSourceManagement ProjectGrantSourceVia = "management"
)

// ProjectGrantSource is one source of a person's access: their own grant, a
// Group they are in (GroupID), the Company default set on a Company-visible
// Project, or the management permissions of their Company role.
type ProjectGrantSource struct {
	Via         ProjectGrantSourceVia    `json:"via"`
	GroupID     *uuid.UUID               `json:"groupId,omitempty"`
	Permissions []auth.ProjectPermission `json:"permissions"`
}

// ProjectGrantList is GET /projects/{id}/grants: every person with a stored
// source on the Project, and every Group holding a grant there.
type ProjectGrantList struct {
	Items  []ProjectGrant      `json:"items"`
	Groups []ProjectGroupGrant `json:"groups"`
}

// ProjectGrantInput replaces one person's grant. Permissions is normalized:
// project.read is added to any non-empty set, and an empty set removes the
// person.
type ProjectGrantInput struct {
	UserID           uuid.UUID
	Permissions      []string
	ExpectedRevision int64
}

// ProjectGrantResult is the person's grant after the change (nil when they
// hold none) and the Project's new access revision.
type ProjectGrantResult struct {
	Grant    *ProjectGrant `json:"grant"`
	Revision int64         `json:"revision"`
}

// ProjectGrantPreview is what setting one person's own grant on a Project
// would do, read without writing: how many of their Runs there would stop,
// whether they would lose the Project, and the access revision to save
// with. The app asks before a save only when it stops Runs or takes the
// Project away.
type ProjectGrantPreview struct {
	Revision     int64 `json:"revision"`
	RunsStopping int   `json:"runsStopping"`
	LosesProject bool  `json:"losesProject"`
}

// projectGrantChange is the Activity payload of a project_grant row. UserID
// is the person whose grant changed, absent for a visibility change; Before
// and After are their stored permissions, or the visibility. ProjectTitle is
// the Project's title when the change was made, so the row still reads after
// the Project is archived or renamed; rows written before it carry none.
type projectGrantChange struct {
	ProjectID    uuid.UUID  `json:"projectId"`
	ProjectTitle string     `json:"projectTitle"`
	UserID       *uuid.UUID `json:"userId"`
	Before       []string   `json:"before"`
	After        []string   `json:"after"`
}

// Activity actions of project_grant rows.
const (
	projectGrantChanged           = "project_grant.changed"
	projectGrantJoined            = "project_grant.joined"
	projectGrantVisibilityChanged = "project_grant.visibility_changed"
)

func (s *DefaultProjectService) ListGrants(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
) (ProjectGrantList, error) {
	q := s.store.Queries()
	access, err := auth.RequireProjectVisible(ctx, q, principal, id)
	if err != nil {
		return ProjectGrantList{}, err
	}
	items, err := listProjectGrants(ctx, q, principal.CompanyID, id, access.Visibility)
	if err != nil {
		return ProjectGrantList{}, err
	}
	groups, err := listProjectGroupGrants(ctx, q, principal.CompanyID, id, uuid.NullUUID{})
	if err != nil {
		return ProjectGrantList{}, err
	}
	return ProjectGrantList{Items: items, Groups: groups}, nil
}

// listProjectGrants reads every person with a stored source on the Project:
// their own grant, a Group, or both. People who read it only through the
// Company-visible floor are not listed.
func listProjectGrants(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID uuid.UUID,
	visibility auth.ProjectVisibility,
) ([]ProjectGrant, error) {
	rows, err := q.ListProjectAccessPaths(ctx, generated.ListProjectAccessPathsParams{
		CompanyID: companyID, ProjectID: projectID,
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
	})
	if err != nil {
		return nil, fmt.Errorf("list Project grants: %w", err)
	}
	return foldProjectGrants(rows, visibility), nil
}

// foldProjectGrants states each listed person's access from their stored
// paths, which come one row per person and source, ordered by person with
// their own grant first. It never returns nil.
func foldProjectGrants(rows []generated.ListProjectAccessPathsRow, visibility auth.ProjectVisibility) []ProjectGrant {
	grants := []ProjectGrant{}
	for start := 0; start < len(rows); {
		end := start
		for end < len(rows) && rows[end].UserID == rows[start].UserID {
			end++
		}
		grants = append(grants, projectGrantOf(rows[start:end], visibility))
		start = end
	}
	return grants
}

// projectGrantOf states one person's access from their stored paths. Every
// listed person reads the Project: no grant row, theirs or a Group's, stands
// without its project.read row (project_grants_read_fkey,
// project_group_grants_read_fkey). Granted stays their own rows, the rows the
// person grant editor changes; Effective adds every other source.
func projectGrantOf(paths []generated.ListProjectAccessPathsRow, visibility auth.ProjectVisibility) ProjectGrant {
	grant := ProjectGrant{UserID: paths[0].UserID, Granted: []auth.ProjectPermission{}}
	facts := auth.ProjectAccessFacts{
		Visibility: string(visibility),
		Via:        string(auth.ProjectAccessViaGroup),
		Management: paths[0].Management,
	}
	var groups []ProjectGrantSource
	for _, path := range paths {
		if path.UpdatedAt.Time.After(grant.UpdatedAt) {
			grant.UpdatedAt = path.UpdatedAt.Time
		}
		permissions := storedProjectPermissions(path.Permissions)
		if path.Via == string(ProjectGrantSourceGrant) {
			facts.Via = string(auth.ProjectAccessViaGrant)
			facts.GrantedPermissions = path.Permissions
			grant.Granted = permissions
			if path.GrantedByUserID.Valid {
				grantedBy := path.GrantedByUserID.UUID
				grant.GrantedBy = &grantedBy
			}
			continue
		}
		facts.GroupPermissions = append(facts.GroupPermissions, path.Permissions...)
		groupID := path.GroupID
		groups = append(groups, ProjectGrantSource{
			Via: ProjectGrantSourceGroup, GroupID: &groupID, Permissions: permissions,
		})
	}
	held := auth.ProjectAccessOf(facts)
	grant.Effective = held.Permissions
	grant.Sources = []ProjectGrantSource{}
	if len(grant.Granted) > 0 {
		grant.Sources = append(grant.Sources, ProjectGrantSource{
			Via: ProjectGrantSourceGrant, Permissions: grant.Granted,
		})
	}
	grant.Sources = append(grant.Sources, groups...)
	if visibility == auth.ProjectVisibilityCompany {
		grant.Sources = append(grant.Sources, ProjectGrantSource{
			Via: ProjectGrantSourceCompany, Permissions: auth.CompanyProjectDefaultPermissions(),
		})
	}
	if held.Management {
		grant.Sources = append(grant.Sources, ProjectGrantSource{
			Via: ProjectGrantSourceManagement, Permissions: auth.ManagementProjectPermissions(),
		})
	}
	return grant
}

// storedProjectPermissions turns the names a read returned into a sorted set
// without duplicates, never nil.
func storedProjectPermissions(names []string) []auth.ProjectPermission {
	permissions := make([]auth.ProjectPermission, len(names))
	for i, name := range names {
		permissions[i] = auth.ProjectPermission(name)
	}
	slices.Sort(permissions)
	return slices.Compact(permissions)
}

// SetGrants makes one person's stored permissions exactly the normalized
// set. It needs project.grants.manage and, for someone who manages only
// through their own grant, the self-grant cap (grantWithinOwn); an empty set
// removes the person. On a Company-visible Project the set adds on top of
// the default set everyone holds there.
func (s *DefaultProjectService) SetGrants(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	input ProjectGrantInput,
) (ProjectGrantResult, error) {
	// A permission outside the catalog is refused by name
	// (auth.ProjectPermissionUnknownError, 422 project_permission_unknown).
	permissions, err := auth.NormalizeProjectGrant(input.Permissions)
	if err != nil {
		return ProjectGrantResult{}, err
	}
	if len(permissions) == 0 {
		return s.RemoveGrants(ctx, principal, id, input.UserID, input.ExpectedRevision)
	}
	if id == uuid.Nil || input.UserID == uuid.Nil || input.ExpectedRevision < 1 {
		return ProjectGrantResult{}, ErrProjectInvalidInput
	}
	var result ProjectGrantResult
	pending, notices, err := s.changeGrant(ctx, principal, id, input.UserID, input.ExpectedRevision, permissions,
		func(q *generated.Queries, _ generated.Project, before auth.ProjectAccess) (bool, error) {
			// The same set again writes nothing, so the stored author and time
			// stay those of the change Activity recorded. A person fenced out by
			// a Company rejoin holds no counted rows, so a re-grant still writes.
			if slices.Equal(before.Granted, permissions) {
				return false, nil
			}
			kept, err := q.ReplaceProjectGrants(ctx, generated.ReplaceProjectGrantsParams{
				CompanyID: principal.CompanyID, ProjectID: id, UserID: input.UserID,
				ActorUserID: principal.UserID, Permissions: auth.ProjectPermissionStrings(permissions),
			})
			if err != nil {
				return false, fmt.Errorf("replace Project grants: %w", err)
			}
			if len(kept) == 0 {
				return false, ErrProjectMemberRequired
			}
			return true, nil
		}, &result)
	if err != nil {
		return ProjectGrantResult{}, err
	}
	s.publish(ctx, pending)
	s.present(notices)
	return result, nil
}

// RemoveGrants deletes every stored permission the person holds on the
// Project. On a Company-visible Project nobody can be taken below the
// default set, so removal needs the Project restricted first. It asks for
// nothing, so under the self-grant cap only the grant removed is checked.
func (s *DefaultProjectService) RemoveGrants(
	ctx context.Context,
	principal auth.UserPrincipal,
	id, userID uuid.UUID,
	expectedRevision int64,
) (ProjectGrantResult, error) {
	if id == uuid.Nil || userID == uuid.Nil || expectedRevision < 1 {
		return ProjectGrantResult{}, ErrProjectInvalidInput
	}
	var result ProjectGrantResult
	pending, notices, err := s.changeGrant(ctx, principal, id, userID, expectedRevision, nil,
		func(q *generated.Queries, project generated.Project, _ auth.ProjectAccess) (bool, error) {
			if err := checkProjectFloor(auth.ProjectVisibility(project.Visibility)); err != nil {
				return false, err
			}
			removed, err := q.DeleteProjectGrantsForUser(ctx, generated.DeleteProjectGrantsForUserParams{
				CompanyID: principal.CompanyID, ProjectID: id, UserID: userID,
			})
			if err != nil {
				return false, fmt.Errorf("delete Project grants: %w", err)
			}
			return len(removed) > 0, nil
		}, &result)
	if err != nil {
		return ProjectGrantResult{}, err
	}
	s.publish(ctx, pending)
	s.present(notices)
	return result, nil
}

// PreviewGrant answers what setting the person's own grant on the Project to
// permissions would do (an empty set removes it), as SetGrants and
// RemoveGrants would do it: the Runs the change stops, counted with the
// sweep's own predicate as the Group preview counts them
// (CountActiveRunsForProjectAuthorizers, for a person who loses read or
// execute), and whether they lose read. What their Groups give, the
// Company-visible floor and their Company role stay as they are. It writes
// nothing, takes no lock, and refuses what the save refuses before it
// writes: project.grants.manage, the self-grant cap, and removal on a
// Company-visible Project.
func (s *DefaultProjectService) PreviewGrant(
	ctx context.Context,
	principal auth.UserPrincipal,
	id, userID uuid.UUID,
	permissionNames []string,
) (ProjectGrantPreview, error) {
	permissions, err := auth.NormalizeProjectGrant(permissionNames)
	if err != nil {
		return ProjectGrantPreview{}, err
	}
	if id == uuid.Nil || userID == uuid.Nil {
		return ProjectGrantPreview{}, ErrProjectInvalidInput
	}
	q := s.store.Queries()
	access, err := auth.RequireProjectPermission(ctx, q, principal, id, auth.ProjectPermissionGrantsManage)
	if err != nil {
		return ProjectGrantPreview{}, err
	}
	before, err := auth.ResolveMemberProjectAccess(ctx, q, principal.CompanyID, userID, id)
	if err != nil {
		return ProjectGrantPreview{}, err
	}
	if !grantWithinOwn(access, before.Granted, permissions) {
		return ProjectGrantPreview{}, ErrProjectGrantExceedsOwn
	}
	if len(permissions) == 0 {
		if err := checkProjectFloor(access.Visibility); err != nil {
			return ProjectGrantPreview{}, err
		}
	}
	after := accessWithOwnGrant(before, permissions)
	preview := ProjectGrantPreview{
		Revision:     access.Revision,
		LosesProject: before.Via != "" && after.Via == "",
	}
	if !projectWorkLost(before, after) {
		return preview, nil
	}
	rows, err := q.CountActiveRunsForProjectAuthorizers(ctx, generated.CountActiveRunsForProjectAuthorizersParams{
		CompanyID: principal.CompanyID, ProjectIds: []uuid.UUID{id}, UserIds: []uuid.UUID{userID},
	})
	if err != nil {
		return ProjectGrantPreview{}, fmt.Errorf("count Runs a grant change stops: %w", err)
	}
	for _, row := range rows {
		preview.RunsStopping += int(row.Runs)
	}
	return preview, nil
}

// accessWithOwnGrant states what a person would hold on a Project with their
// own grant replaced by own and everything else kept: their Groups' rows, the
// Company-visible floor and the management bridge. It applies the rule
// project_access and ProjectAccessOf apply, as simulatedAccess does for a
// Group change: read comes from an own row, then a Group row, then the
// Company-visible floor.
func accessWithOwnGrant(before auth.ProjectAccess, own []auth.ProjectPermission) auth.ProjectAccess {
	facts := auth.ProjectAccessFacts{
		ProjectID: before.ProjectID, CompanyID: before.CompanyID, Visibility: string(before.Visibility),
		Management: before.Management, Revision: before.Revision,
		GrantedPermissions: auth.ProjectPermissionStrings(own),
		GroupPermissions:   auth.ProjectPermissionStrings(before.GroupGranted),
	}
	switch {
	case slices.Contains(own, auth.ProjectPermissionRead):
		facts.Via = string(auth.ProjectAccessViaGrant)
	case slices.Contains(before.GroupGranted, auth.ProjectPermissionRead):
		facts.Via = string(auth.ProjectAccessViaGroup)
	case before.Visibility == auth.ProjectVisibilityCompany:
		facts.Via = string(auth.ProjectAccessViaCompany)
	}
	return auth.ProjectAccessOf(facts)
}

// changeGrant is the transaction SetGrants and RemoveGrants share: locks,
// project.grants.manage, the revision precondition, the person's standing
// before and after, the self-grant cap on requested (the set asked for, nil
// for a removal), the audience, the Activity row, the Run sweep and the
// access frame. write reports whether it stored anything; an unchanged
// grant advances nothing and records nothing. It returns the frames to
// publish and the Inbox items the Run sweep wrote, to present after commit.
func (s *DefaultProjectService) changeGrant(
	ctx context.Context,
	principal auth.UserPrincipal,
	id, userID uuid.UUID,
	expectedRevision int64,
	requested []auth.ProjectPermission,
	write func(*generated.Queries, generated.Project, auth.ProjectAccess) (bool, error),
	result *ProjectGrantResult,
) ([]events.Event, []inbox.CreateResult, error) {
	var pending []events.Event
	var notices []inbox.CreateResult
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending, notices = nil, nil
		if err := lockProjectMutation(ctx, q, principal, ""); err != nil {
			return err
		}
		project, access, err := lockProjectForChange(ctx, q, principal, id)
		if err != nil {
			return err
		}
		if err := requireProjectPermissions(access, auth.ProjectPermissionGrantsManage); err != nil {
			return err
		}
		if project.AccessVersion != expectedRevision {
			return ErrProjectGrantRevisionConflict
		}
		change, err := beginProjectAccessChange(ctx, q, principal, project)
		if err != nil {
			return err
		}
		before, err := auth.ResolveMemberProjectAccess(ctx, q, principal.CompanyID, userID, id)
		if err != nil {
			return err
		}
		if !grantWithinOwn(access, before.Granted, requested) {
			return ErrProjectGrantExceedsOwn
		}
		written, err := write(q, project, before)
		if err != nil {
			return err
		}
		after, err := auth.ResolveMemberProjectAccess(ctx, q, principal.CompanyID, userID, id)
		if err != nil {
			return err
		}
		result.Revision = project.AccessVersion
		if grantChangeRecorded(before, after, written) {
			if result.Revision, err = change.commit(
				ctx,
				q,
				&pending,
				projectGrantChanged,
				&userID,
				auth.ProjectPermissionStrings(before.Granted),
				auth.ProjectPermissionStrings(after.Granted),
			); err != nil {
				return err
			}
			if err := applyProjectAccessLossInTx(
				ctx,
				q,
				&pending,
				&notices,
				principal.CompanyID,
				id,
				userID,
				before,
				after,
			); err != nil {
				return err
			}
		}
		result.Grant, err = readProjectGrant(ctx, q, principal.CompanyID, id, userID, after.Visibility)
		return err
	})
	return pending, notices, err
}

// readProjectGrant reads back one person's access, or nil when they hold no
// stored source there: removing their own grant leaves them listed while a
// Group still gives them the Project.
func readProjectGrant(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID, userID uuid.UUID,
	visibility auth.ProjectVisibility,
) (*ProjectGrant, error) {
	grants, err := listProjectGrants(ctx, q, companyID, projectID, visibility)
	if err != nil {
		return nil, err
	}
	for _, grant := range grants {
		if grant.UserID == userID {
			return &grant, nil
		}
	}
	return nil, nil
}

// grantWithinOwn is the self-grant cap. Someone who
// manages a Project's access only through their own grant may set only
// permissions they hold there (requested), and may not change a grant that
// holds one they lack (target, the stored grant being changed). Only that
// stored grant counts: what its holder also gets from a Group or from the
// management bridge is not part of it. A caller who manages through Company
// projects.manage (owners, admins) is not capped, and Join never comes here.
func grantWithinOwn(caller auth.ProjectAccess, target, requested []auth.ProjectPermission) bool {
	if caller.Management {
		return true
	}
	for _, permission := range slices.Concat(target, requested) {
		if !caller.Can(permission) {
			return false
		}
	}
	return true
}

// Join grants the caller exactly project.read on a Project they manage
// through Company projects.manage but cannot read. The caller's role is
// rechecked under the member lock; a caller who already reads the Project
// changes nothing.
func (s *DefaultProjectService) Join(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	expectedRevision int64,
) (ProjectResponse, error) {
	if id == uuid.Nil || expectedRevision < 1 {
		return ProjectResponse{}, ErrProjectInvalidInput
	}
	var pending []events.Event
	var value ProjectResponse
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		if err := lockProjectMutation(ctx, q, principal, auth.PermissionProjectsManage); err != nil {
			return err
		}
		project, access, err := lockProjectForChange(ctx, q, principal, id)
		if err != nil {
			return err
		}
		if access.Via == "" {
			if project.AccessVersion != expectedRevision {
				return ErrProjectGrantRevisionConflict
			}
			change, err := beginProjectAccessChange(ctx, q, principal, project)
			if err != nil {
				return err
			}
			read := []string{string(auth.ProjectPermissionRead)}
			kept, err := q.ReplaceProjectGrants(ctx, generated.ReplaceProjectGrantsParams{
				CompanyID: principal.CompanyID, ProjectID: id, UserID: principal.UserID,
				ActorUserID: principal.UserID, Permissions: read,
			})
			if err != nil {
				return fmt.Errorf("join Project: %w", err)
			}
			if len(kept) == 0 {
				return ErrProjectMemberRequired
			}
			if project.AccessVersion, err = change.commit(ctx, q, &pending, projectGrantJoined,
				&principal.UserID, []string{}, read,
			); err != nil {
				return err
			}
			if access, err = auth.ResolveProjectAccess(ctx, q, principal, id); err != nil {
				return err
			}
		}
		value, err = readableProjectResponse(ctx, q, principal.UserID, project, access)
		return err
	})
	if err != nil {
		return ProjectResponse{}, err
	}
	s.publish(ctx, pending)
	return value, nil
}

// grantProjectCreatorInTx gives the creator of a new Project every Project
// permission and records it like any other grant.
func grantProjectCreatorInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	actor events.Actor,
	project generated.Project,
) error {
	all := auth.ProjectPermissionStrings(auth.ProjectPermissions())
	kept, err := q.ReplaceProjectGrants(ctx, generated.ReplaceProjectGrantsParams{
		CompanyID: principal.CompanyID, ProjectID: project.ID, UserID: principal.UserID,
		ActorUserID: principal.UserID, Permissions: all,
	})
	if err != nil {
		return fmt.Errorf("grant Project creator: %w", err)
	}
	if len(kept) == 0 {
		return ErrProjectMemberRequired
	}
	return recordProjectGrantAsInTx(ctx, q, pending, projectGrantAuthor{
		companyID: principal.CompanyID, actor: actor, recipient: principal.UserID,
	}, projectGrantChanged, project.ID, project.Title, &principal.UserID, []string{}, all)
}

// setProjectVisibilityInTx flips the Project between restricted and
// Company-visible. The caller holds the Company, member and Project locks
// and checked project.visibility.update. Members who lose project.read or
// project.tasks.execute (a Company-visible Project made restricted) have
// their Runs on it cancelled; the Inbox items that tells them go to notices.
func setProjectVisibilityInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	notices *[]inbox.CreateResult,
	principal auth.UserPrincipal,
	project generated.Project,
	visibility auth.ProjectVisibility,
) (generated.Project, error) {
	change, err := beginProjectAccessChange(ctx, q, principal, project)
	if err != nil {
		return generated.Project{}, err
	}
	// Only a member with a live Run or a queued item on the Project can lose
	// work to this change, so only they are resolved before and after.
	authorizers, err := q.ListProjectAccessRecheckUserIDs(ctx, generated.ListProjectAccessRecheckUserIDsParams{
		CompanyID: principal.CompanyID, ProjectID: project.ID,
	})
	if err != nil {
		return generated.Project{}, fmt.Errorf("list Project access re-check members: %w", err)
	}
	before := make(map[uuid.UUID]auth.ProjectAccess, len(authorizers))
	for _, userID := range authorizers {
		if before[userID], err = auth.ResolveMemberProjectAccess(
			ctx,
			q,
			principal.CompanyID,
			userID,
			project.ID,
		); err != nil {
			return generated.Project{}, err
		}
	}
	updated, err := q.SetProjectVisibility(ctx, generated.SetProjectVisibilityParams{
		CompanyID: principal.CompanyID, ProjectID: project.ID,
		Visibility: string(visibility), ExpectedRevision: project.AccessVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.Project{}, ErrProjectGrantRevisionConflict
	}
	if err != nil {
		return generated.Project{}, fmt.Errorf("set Project visibility: %w", err)
	}
	// SetProjectVisibility advanced the revision itself; the Activity row
	// and the frame name the revision it wrote.
	change.revision = updated.AccessVersion
	if err := change.record(ctx, q, pending, projectGrantVisibilityChanged, nil,
		[]string{project.Visibility}, []string{updated.Visibility},
	); err != nil {
		return generated.Project{}, err
	}
	for _, userID := range authorizers {
		after, err := auth.ResolveMemberProjectAccess(ctx, q, principal.CompanyID, userID, project.ID)
		if err != nil {
			return generated.Project{}, err
		}
		if err := applyProjectAccessLossInTx(
			ctx,
			q,
			pending,
			notices,
			principal.CompanyID,
			project.ID,
			userID,
			before[userID],
			after,
		); err != nil {
			return generated.Project{}, err
		}
	}
	project.Visibility = updated.Visibility
	project.AccessVersion = updated.AccessVersion
	return project, nil
}

// projectAccessChange collects one access change's frame audience: the
// readers before the change (taken when it begins), the readers after, the
// active projects.manage holders and the actor.
type projectAccessChange struct {
	principal   auth.UserPrincipal
	projectID   uuid.UUID
	title       string
	revision    int64
	recipients  []uuid.UUID
	companyWide bool
}

func beginProjectAccessChange(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	project generated.Project,
) (*projectAccessChange, error) {
	change := &projectAccessChange{
		principal: principal, projectID: project.ID, title: project.Title, revision: project.AccessVersion,
	}
	return change, change.addAudience(ctx, q)
}

func (c *projectAccessChange) addAudience(ctx context.Context, q *generated.Queries) error {
	ids, companyWide, err := auth.ProjectAudienceInTx(ctx, q, c.principal.CompanyID, c.projectID, true)
	if err != nil {
		return err
	}
	c.recipients = append(c.recipients, ids...)
	c.companyWide = c.companyWide || companyWide
	return nil
}

// commit advances the access revision the change was begun at, then records
// it. It returns the new revision.
func (c *projectAccessChange) commit(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	action string,
	userID *uuid.UUID,
	before, after []string,
) (int64, error) {
	revision, err := c.advance(ctx, q)
	if err != nil {
		return 0, err
	}
	return revision, c.record(ctx, q, pending, action, userID, before, after)
}

// advance moves the Project's access revision on from the one the change
// was begun at and returns the new one.
func (c *projectAccessChange) advance(ctx context.Context, q *generated.Queries) (int64, error) {
	revision, err := q.BumpProjectAccessVersion(ctx, generated.BumpProjectAccessVersionParams{
		CompanyID: c.principal.CompanyID, ProjectID: c.projectID, ExpectedRevision: c.revision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrProjectGrantRevisionConflict
	}
	if err != nil {
		return 0, fmt.Errorf("advance Project access revision: %w", err)
	}
	c.revision = revision
	return revision, nil
}

// record writes the Activity row and stages project.access_changed for the
// audience before and after the change.
func (c *projectAccessChange) record(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	action string,
	userID *uuid.UUID,
	before, after []string,
) error {
	if err := recordProjectGrantInTx(
		ctx,
		q,
		pending,
		c.principal,
		action,
		c.projectID,
		c.title,
		userID,
		before,
		after,
	); err != nil {
		return err
	}
	return c.stage(ctx, q, pending)
}

// stage adds the readers after the change to the audience and stages
// project.access_changed at the change's revision. A change that writes
// several Activity rows on one Project stages it once.
func (c *projectAccessChange) stage(ctx context.Context, q *generated.Queries, pending *[]events.Event) error {
	if err := c.addAudience(ctx, q); err != nil {
		return err
	}
	*pending = append(*pending, projectAccessChangedEvent(
		c.principal.CompanyID, events.UserActor(c.principal.UserID.String()),
		c.projectID, c.revision, append(c.recipients, c.principal.UserID), c.companyWide,
	))
	return nil
}

// projectAccessChangedEvent stages the opaque access frame. The recipients
// are sorted and deduplicated here, so every producer states them the same
// way.
func projectAccessChangedEvent(
	companyID uuid.UUID,
	actor events.Actor,
	projectID uuid.UUID,
	revision int64,
	recipients []uuid.UUID,
	companyWide bool,
) events.Event {
	recipients = slices.Clone(recipients)
	slices.SortFunc(recipients, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return events.Event{
		EntityType: TopicProject,
		Name:       EventProjectAccessChanged,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      actor,
		// The project_grant Activity row is written in the transaction; the
		// evidence sink must not copy the frame as a second one.
		ActivityRecorded: true,
		Payload: ProjectAccessChanged{
			ProjectID: projectID, Revision: revision,
			RecipientUserIDs: slices.Compact(recipients), CompanyWide: companyWide,
		},
	}
}

// recordProjectGrantInTx writes the project_grant Activity row for a change
// a person made. The feed reads it for the Project's readers and the
// members whose role holds projects.manage (ListActivity); the row is
// addressed to the actor, so its activity.created frame reaches only them
// until Activity frames get a Project audience.
func recordProjectGrantInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	action string,
	projectID uuid.UUID,
	projectTitle string,
	userID *uuid.UUID,
	before, after []string,
) error {
	return recordProjectGrantAsInTx(ctx, q, pending, projectGrantAuthor{
		companyID: principal.CompanyID,
		actor:     events.UserActor(principal.UserID.String()),
		recipient: principal.UserID,
	}, action, projectID, projectTitle, userID, before, after)
}

// projectGrantAuthor is who a project_grant row names as the actor, and the
// person it is addressed to (who also receives its frame). The Project's
// readers and projects.manage holders read it in the feed as well.
type projectGrantAuthor struct {
	companyID uuid.UUID
	actor     events.Actor
	recipient uuid.UUID
}

func recordProjectGrantAsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	author projectGrantAuthor,
	action string,
	projectID uuid.UUID,
	projectTitle string,
	userID *uuid.UUID,
	before, after []string,
) error {
	return activity.RecordEvidenceInTx(ctx, q, pending, activity.EvidenceInput{
		CompanyID:       author.companyID,
		EntityID:        projectID,
		ProjectID:       projectID,
		Action:          action,
		Actor:           author.actor,
		RecipientUserID: author.recipient,
		Payload: projectGrantChange{
			ProjectID: projectID, ProjectTitle: projectTitle, UserID: userID, Before: before, After: after,
		},
	})
}

// revokeProjectGrantsInTx is the Company revocation's Project step (a
// removal, a member leaving, or account deletion). It reads what the
// departing member held on each Project from every stored source first, then
// deletes their Group memberships (advancing each Group's revision and
// staging group.changed) and their own grants (advancing the revision of
// each Project that held one), and advances the revision of each Project
// they reached only through a Group. Each Project they reached is recorded as
// one project_grant.changed row (before = what they held there from every
// stored source, after = nothing), and project.access_changed is staged for
// it, addressed to its readers after the change, its projects.manage
// holders, the acting person and the member who left. A rejoin restores no
// Group: the rows are gone. The member's Runs end with the runtime-owner
// sweep the revocation already runs. The caller holds the Company and member
// locks and has not taken a Group, Project, Conversation or Run lock; the
// Groups are written before the Projects (company -> member -> group ->
// project).
func revokeProjectGrantsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	author projectGrantAuthor,
	userID uuid.UUID,
) error {
	paths, err := q.ListUserProjectPaths(ctx, generated.ListUserProjectPathsParams{
		CompanyID: author.companyID, UserID: userID,
	})
	if err != nil {
		return fmt.Errorf("list departing member's Project access: %w", err)
	}
	if err := leaveCompanyGroupsInTx(ctx, q, pending, author, userID); err != nil {
		return err
	}
	removed, err := auth.DeleteProjectGrantsForUserInTx(ctx, q, author.companyID, userID)
	if err != nil {
		return err
	}
	held := make(map[uuid.UUID][]string, len(paths)+len(removed))
	for _, grant := range removed {
		held[grant.ProjectID] = auth.ProjectPermissionStrings(grant.Permissions)
	}
	var groupOnly []uuid.UUID
	for _, path := range paths {
		if _, bumped := held[path.ProjectID]; !bumped {
			groupOnly = append(groupOnly, path.ProjectID)
		}
		held[path.ProjectID] = path.Permissions
	}
	if len(groupOnly) > 0 {
		if _, err := q.AdvanceProjectAccessVersions(ctx, generated.AdvanceProjectAccessVersionsParams{
			CompanyID: author.companyID, ProjectIds: groupOnly,
		}); err != nil {
			return fmt.Errorf("advance Project access revisions: %w", err)
		}
	}
	projects := make([]uuid.UUID, 0, len(held))
	for projectID := range held {
		projects = append(projects, projectID)
	}
	slices.SortFunc(projects, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	for _, projectID := range projects {
		facts, err := q.GetProjectAccessFacts(ctx, generated.GetProjectAccessFactsParams{
			CompanyID: author.companyID, ProjectID: projectID,
		})
		if err != nil {
			return fmt.Errorf("read Project access revision: %w", err)
		}
		if err := recordProjectGrantAsInTx(ctx, q, pending, author, projectGrantChanged, projectID,
			facts.Title, &userID, held[projectID], []string{},
		); err != nil {
			return err
		}
		recipients, companyWide, err := auth.ProjectAudienceInTx(ctx, q, author.companyID, projectID, true)
		if err != nil {
			return err
		}
		recipients = append(recipients, userID)
		if author.recipient != uuid.Nil {
			recipients = append(recipients, author.recipient)
		}
		*pending = append(*pending, projectAccessChangedEvent(
			author.companyID, author.actor, projectID, facts.AccessVersion, recipients, companyWide,
		))
	}
	return nil
}

// projectWorkLost reports whether a change took project.read or
// project.tasks.execute from a person: their Runs on the Project must end.
func projectWorkLost(before, after auth.ProjectAccess) bool {
	return projectAccessLost(before, after, auth.ProjectPermissionRead, auth.ProjectPermissionTasksExecute)
}

// projectPostingLost reports whether a change took project.read or
// project.tasks.write from a person: their queued items in the Project's
// Task chats must go.
func projectPostingLost(before, after auth.ProjectAccess) bool {
	return projectAccessLost(before, after, auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite)
}

// projectAccessLost reports whether the person held any of the permissions
// before the change and not after it.
func projectAccessLost(before, after auth.ProjectAccess, permissions ...auth.ProjectPermission) bool {
	for _, permission := range permissions {
		if before.Can(permission) && !after.Can(permission) {
			return true
		}
	}
	return false
}

// checkProjectFloor refuses removing a person from a Company-visible
// Project. Everyone holds the Company default set there without a row, so a
// stored set only adds on top of it and can never take anyone below it; a
// removal is refused so it is made on a restricted Project, where it takes
// effect. A restricted Project has no floor.
func checkProjectFloor(visibility auth.ProjectVisibility) error {
	if visibility == auth.ProjectVisibilityCompany {
		return ErrProjectVisibilityFloor
	}
	return nil
}

// grantChangeRecorded reports whether a grant change stored something new
// for the person: only then is the revision advanced, Activity written and
// the frame sent.
func grantChangeRecorded(before, after auth.ProjectAccess, written bool) bool {
	return written && !slices.Equal(before.Granted, after.Granted)
}

// applyProjectAccessLossInTx carries out what a person loses with an access
// change, as the membership revocation does for the whole Company: without
// project.read or project.tasks.write their queued items in the Project's
// Task chats are removed (they lock Conversations, so this comes first),
// then without project.read or project.tasks.execute their Runs on the
// Project are cancelled and each Run's Task takes its terminal transition.
// A person who still reads the Project is told in their Inbox; the items go
// to notices for the caller to present after commit.
func applyProjectAccessLossInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	notices *[]inbox.CreateResult,
	companyID, projectID, userID uuid.UUID,
	before, after auth.ProjectAccess,
) error {
	if projectPostingLost(before, after) {
		if err := conversation.RemoveProjectRunQueueItemsForAccessRevocationInTx(
			ctx, q, pending, companyID, projectID, userID,
		); err != nil {
			return err
		}
	}
	if !projectWorkLost(before, after) {
		return nil
	}
	runs, stopped, err := execution.CancelProjectAccessRevokedRunsInTx(
		ctx,
		q,
		pending,
		execution.ProjectAccessRevocation{
			CompanyID: companyID, ProjectID: projectID, UserID: userID,
		},
	)
	if err != nil {
		return err
	}
	*notices = append(*notices, stopped...)
	for _, run := range runs {
		if err := task.ApplyRunTerminalInTx(ctx, q, pending, run); err != nil {
			return err
		}
	}
	return nil
}

// ProjectAccessReport lists, per restricted Project, the members left
// outside work they were doing there (ADR 0017): the assignees of its open
// Tasks and the members who authorized a Run in its Task chats since Since,
// who cannot read the Project. Owners and admins repair access from it. It
// names people and Projects only, never a Task, chat or Run, because its
// readers manage Projects they may not read.
type ProjectAccessReport struct {
	Since time.Time                  `json:"since"`
	Items []ProjectAccessReportEntry `json:"items"`
}

// ProjectAccessReportEntry is one restricted Project and the people outside it.
type ProjectAccessReportEntry struct {
	ProjectID uuid.UUID                   `json:"projectId"`
	Key       string                      `json:"key"`
	Title     string                      `json:"title"`
	People    []ProjectAccessReportPerson `json:"people"`
}

// ProjectAccessReportPerson is one member outside a Project and why they are
// listed: one reason or both, sorted.
type ProjectAccessReportPerson struct {
	UserID  uuid.UUID                   `json:"userId"`
	Reasons []ProjectAccessReportReason `json:"reasons"`
}

// ProjectAccessReportReason says why a person is on the access report. The
// values are written as literals by ListProjectAccessReport and listed in
// projects.yaml; TestProjectAccessReportReasonsMatchContractAndQuery pins
// all three together.
type ProjectAccessReportReason string

const (
	// ProjectAccessReportReasonRunAuthorizer: they authorized a Run in one of
	// the Project's Task chats within the window.
	ProjectAccessReportReasonRunAuthorizer ProjectAccessReportReason = "run_authorizer"
	// ProjectAccessReportReasonTaskAssignee: they are the assignee of one of
	// the Project's open Tasks.
	ProjectAccessReportReasonTaskAssignee ProjectAccessReportReason = "task_assignee"
)

// AccessReport builds the report for a caller whose Company role holds
// projects.manage; the window is the last 30 days of Runs.
func (s *DefaultProjectService) AccessReport(
	ctx context.Context,
	principal auth.UserPrincipal,
) (ProjectAccessReport, error) {
	if !principal.Role.Can(auth.PermissionProjectsManage) {
		return ProjectAccessReport{}, ErrProjectForbidden
	}
	since := time.Now().UTC().AddDate(0, 0, -30)
	rows, err := s.store.Queries().ListProjectAccessReport(ctx, generated.ListProjectAccessReportParams{
		CompanyID: principal.CompanyID, Since: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return ProjectAccessReport{}, fmt.Errorf("list Project access report: %w", err)
	}
	return groupProjectAccessReport(since, rows), nil
}

// groupProjectAccessReport folds the query's rows, one per Project and
// person in Project order, into one entry per Project. Items and People are
// never nil, so the response always carries arrays.
func groupProjectAccessReport(since time.Time, rows []generated.ListProjectAccessReportRow) ProjectAccessReport {
	report := ProjectAccessReport{Since: since, Items: []ProjectAccessReportEntry{}}
	for _, row := range rows {
		if n := len(report.Items); n == 0 || report.Items[n-1].ProjectID != row.ProjectID {
			report.Items = append(report.Items, ProjectAccessReportEntry{
				ProjectID: row.ProjectID,
				Key:       row.Key,
				Title:     row.Title,
				People:    []ProjectAccessReportPerson{},
			})
		}
		reasons := make([]ProjectAccessReportReason, len(row.Reasons))
		for index, reason := range row.Reasons {
			reasons[index] = ProjectAccessReportReason(reason)
		}
		entry := &report.Items[len(report.Items)-1]
		entry.People = append(entry.People, ProjectAccessReportPerson{UserID: row.UserID, Reasons: reasons})
	}
	return report
}
