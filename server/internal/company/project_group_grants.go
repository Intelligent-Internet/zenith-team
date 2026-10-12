package company

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// Group grants on a Project (Access TECH D4): a Group holds permissions on a
// Project as one person does, from the same catalog, and every member holds
// them too. A change follows a person grant change (changeGrant): under the
// Company, member, Group (FOR SHARE) and Project locks, with
// project.grants.manage, the access revision and the self-grant cap, it
// resolves every member before and after (one read each,
// auth.ResolvePeopleProjectAccess), advances the revision, records
// project_grant.group_changed, stages project.access_changed and sweeps
// whoever lost read, write or execute. It sends no Inbox notice: like a
// person grant, it is recorded on the Project.

// ProjectGroupGrant is one Group's grant on a Project: what it holds there,
// who wrote its most recent row and when it last changed, and how many
// members it has.
type ProjectGroupGrant struct {
	GroupID     uuid.UUID                `json:"groupId"`
	Name        string                   `json:"name"`
	Granted     []auth.ProjectPermission `json:"granted"`
	GrantedBy   *uuid.UUID               `json:"grantedBy"`
	UpdatedAt   time.Time                `json:"updatedAt"`
	MemberCount int64                    `json:"memberCount"`
}

// ProjectGroupGrantInput replaces one Group's grant on a Project.
// Permissions is normalized as a person grant's: project.read is added to a
// non-empty set, and an empty set removes the Group.
type ProjectGroupGrantInput struct {
	GroupID          uuid.UUID
	Permissions      []string
	ExpectedRevision int64
}

// ProjectGroupGrantResult is the Group's grant after the change (nil when it
// holds none) and the Project's new access revision.
type ProjectGroupGrantResult struct {
	Grant    *ProjectGroupGrant `json:"grant"`
	Revision int64              `json:"revision"`
}

// listProjectGroupGrants reads the Groups holding grants on the Project, or
// the one named.
func listProjectGroupGrants(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID uuid.UUID,
	groupID uuid.NullUUID,
) ([]ProjectGroupGrant, error) {
	rows, err := q.ListProjectGroupGrants(ctx, generated.ListProjectGroupGrantsParams{
		CompanyID: companyID, ProjectID: projectID, GroupID: groupID,
	})
	if err != nil {
		return nil, fmt.Errorf("list Project Group grants: %w", err)
	}
	grants := make([]ProjectGroupGrant, len(rows))
	for i, row := range rows {
		grants[i] = ProjectGroupGrant{
			GroupID: row.GroupID, Name: row.Name, Granted: storedProjectPermissions(row.Permissions),
			UpdatedAt: row.UpdatedAt.Time, MemberCount: row.MemberCount,
		}
		if row.GrantedByUserID.Valid {
			grantedBy := row.GrantedByUserID.UUID
			grants[i].GrantedBy = &grantedBy
		}
	}
	return grants, nil
}

// SetGroupGrant makes one Group's stored permissions on a Project exactly the
// normalized set. It needs project.grants.manage and, for someone who
// manages only through their own grant, the self-grant cap; an empty set
// removes the Group. A Project holds at most maxProjectGroupGrants Groups.
func (s *DefaultProjectService) SetGroupGrant(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	input ProjectGroupGrantInput,
) (ProjectGroupGrantResult, error) {
	permissions, err := auth.NormalizeProjectGrant(input.Permissions)
	if err != nil {
		return ProjectGroupGrantResult{}, err
	}
	if len(permissions) == 0 {
		return s.RemoveGroupGrant(ctx, principal, id, input.GroupID, input.ExpectedRevision)
	}
	if id == uuid.Nil || input.ExpectedRevision < 1 {
		return ProjectGroupGrantResult{}, ErrProjectInvalidInput
	}
	if input.GroupID == uuid.Nil {
		return ProjectGroupGrantResult{}, ErrGroupNotFound
	}
	return s.changeGroupGrant(ctx, principal, id, input.GroupID, input.ExpectedRevision, permissions,
		func(q *generated.Queries, _ generated.Project, current []auth.ProjectPermission) (bool, error) {
			if slices.Equal(current, permissions) {
				return false, nil
			}
			if len(current) == 0 {
				count, err := q.CountProjectGroupGrants(ctx, generated.CountProjectGroupGrantsParams{
					CompanyID: principal.CompanyID, ProjectID: id,
				})
				if err != nil {
					return false, fmt.Errorf("count Project Group grants: %w", err)
				}
				if count >= maxProjectGroupGrants {
					return false, ErrGroupLimitReached
				}
			}
			kept, err := q.ReplaceProjectGroupGrants(ctx, generated.ReplaceProjectGroupGrantsParams{
				CompanyID: principal.CompanyID, ProjectID: id, GroupID: input.GroupID,
				ActorUserID: principal.UserID, Permissions: auth.ProjectPermissionStrings(permissions),
			})
			if err != nil {
				return false, fmt.Errorf("replace Project Group grants: %w", err)
			}
			if len(kept) == 0 {
				return false, ErrGroupNotFound
			}
			return true, nil
		})
}

// RemoveGroupGrant deletes every permission the Group holds on the Project.
// On a Company-visible Project nobody can be taken below the default set, so
// removal needs the Project restricted first. Under the self-grant cap only
// the grant removed is checked.
func (s *DefaultProjectService) RemoveGroupGrant(
	ctx context.Context,
	principal auth.UserPrincipal,
	id, groupID uuid.UUID,
	expectedRevision int64,
) (ProjectGroupGrantResult, error) {
	if id == uuid.Nil || expectedRevision < 1 {
		return ProjectGroupGrantResult{}, ErrProjectInvalidInput
	}
	if groupID == uuid.Nil {
		return ProjectGroupGrantResult{}, ErrGroupNotFound
	}
	return s.changeGroupGrant(ctx, principal, id, groupID, expectedRevision, nil,
		func(q *generated.Queries, project generated.Project, _ []auth.ProjectPermission) (bool, error) {
			if err := checkProjectFloor(auth.ProjectVisibility(project.Visibility)); err != nil {
				return false, err
			}
			removed, err := q.DeleteProjectGroupGrants(ctx, generated.DeleteProjectGroupGrantsParams{
				CompanyID: principal.CompanyID, ProjectID: id, GroupID: groupID,
			})
			if err != nil {
				return false, fmt.Errorf("delete Project Group grants: %w", err)
			}
			return len(removed) > 0, nil
		})
}

// changeGroupGrant is the transaction SetGroupGrant and RemoveGroupGrant
// share, in changeGrant's order: the Company and member locks, the Group FOR
// SHARE (404 group_not_found outside the Company), the Project FOR UPDATE,
// project.grants.manage, the revision, the self-grant cap with the Group's
// current set as the grant changed, every member before (one read), the
// write, every member after (one read); when the stored set moved, the revision advances, the
// project_grant.group_changed row is written, project.access_changed is
// staged and each member who lost read, write or execute is swept. write
// gets the Group's current set and reports whether it stored anything.
func (s *DefaultProjectService) changeGroupGrant(
	ctx context.Context,
	principal auth.UserPrincipal,
	id, groupID uuid.UUID,
	expectedRevision int64,
	requested []auth.ProjectPermission,
	write func(*generated.Queries, generated.Project, []auth.ProjectPermission) (bool, error),
) (ProjectGroupGrantResult, error) {
	var pending []events.Event
	var notices []inbox.CreateResult
	var result ProjectGroupGrantResult
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending, notices = nil, nil
		if err := lockProjectMutation(ctx, q, principal, ""); err != nil {
			return err
		}
		group, err := q.LockCompanyGroupForShare(ctx, generated.LockCompanyGroupForShareParams{
			CompanyID: principal.CompanyID, GroupID: groupID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGroupNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Group: %w", err)
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
		stored, err := q.GetProjectGroupGrant(ctx, generated.GetProjectGroupGrantParams{
			CompanyID: principal.CompanyID, ProjectID: id, GroupID: groupID,
		})
		if err != nil {
			return fmt.Errorf("read Project Group grant: %w", err)
		}
		current := storedProjectPermissions(stored)
		if !grantWithinOwn(access, current, requested) {
			return ErrProjectGrantExceedsOwn
		}
		members, err := groupMemberIDs(ctx, q, principal.CompanyID, groupID)
		if err != nil {
			return err
		}
		change, err := beginGroupAccessChange(ctx, q, principal, groupReach{
			projects: []generated.Project{project},
			sets:     map[uuid.UUID][]auth.ProjectPermission{id: current},
		}, members)
		if err != nil {
			return err
		}
		written, err := write(q, project, current)
		if err != nil {
			return err
		}
		result.Revision = project.AccessVersion
		if written {
			if err := change.resolveAfter(ctx, q); err != nil {
				return err
			}
			after := auth.ProjectPermissionStrings(requested)
			if err := change.advance(ctx, q, &pending, func(project *groupProjectChange) error {
				result.Revision = project.access.revision
				return recordProjectGroupGrantInTx(
					ctx,
					q,
					&pending,
					principal,
					projectGrantGroupChanged,
					id,
					projectGroupGrantChange{
						ProjectID:    id,
						ProjectTitle: project.project.Title,
						GroupID:      groupID,
						GroupName:    group.Name,
						Before:       auth.ProjectPermissionStrings(current),
						After:        after,
					},
				)
			}); err != nil {
				return err
			}
			if err := change.sweep(ctx, q, &pending, &notices); err != nil {
				return err
			}
		}
		grants, err := listProjectGroupGrants(
			ctx,
			q,
			principal.CompanyID,
			id,
			uuid.NullUUID{UUID: groupID, Valid: true},
		)
		if err != nil {
			return err
		}
		if len(grants) > 0 {
			result.Grant = &grants[0]
		}
		return nil
	})
	if err != nil {
		return ProjectGroupGrantResult{}, err
	}
	s.publish(ctx, pending)
	s.present(notices)
	return result, nil
}

// PreviewGroupGrant answers what setting the Group's grant on the Project to
// permissions would do (an empty set removes it), as the save would do it,
// with the Project's access revision. It writes nothing, takes no lock, and
// refuses what the save would refuse.
func (s *DefaultProjectService) PreviewGroupGrant(
	ctx context.Context,
	principal auth.UserPrincipal,
	id, groupID uuid.UUID,
	permissionNames []string,
) (GroupPreview, error) {
	permissions, err := auth.NormalizeProjectGrant(permissionNames)
	if err != nil {
		return GroupPreview{}, err
	}
	if id == uuid.Nil {
		return GroupPreview{}, ErrProjectInvalidInput
	}
	if groupID == uuid.Nil {
		return GroupPreview{}, ErrGroupNotFound
	}
	q := s.store.Queries()
	access, err := auth.RequireProjectPermission(ctx, q, principal, id, auth.ProjectPermissionGrantsManage)
	if err != nil {
		return GroupPreview{}, err
	}
	if _, err := q.GetCompanyGroup(ctx, generated.GetCompanyGroupParams{
		CompanyID: principal.CompanyID, GroupID: groupID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return GroupPreview{}, ErrGroupNotFound
	} else if err != nil {
		return GroupPreview{}, fmt.Errorf("read Group: %w", err)
	}
	stored, err := q.GetProjectGroupGrant(ctx, generated.GetProjectGroupGrantParams{
		CompanyID: principal.CompanyID, ProjectID: id, GroupID: groupID,
	})
	if err != nil {
		return GroupPreview{}, fmt.Errorf("read Project Group grant: %w", err)
	}
	current := storedProjectPermissions(stored)
	if len(permissions) == 0 {
		if err := checkProjectFloor(access.Visibility); err != nil {
			return GroupPreview{}, err
		}
	}
	if !grantWithinOwn(access, current, permissions) {
		return GroupPreview{}, ErrProjectGrantExceedsOwn
	}
	if len(current) == 0 && len(permissions) > 0 {
		count, err := q.CountProjectGroupGrants(ctx, generated.CountProjectGroupGrantsParams{
			CompanyID: principal.CompanyID, ProjectID: id,
		})
		if err != nil {
			return GroupPreview{}, fmt.Errorf("count Project Group grants: %w", err)
		}
		if count >= maxProjectGroupGrants {
			return GroupPreview{}, ErrGroupLimitReached
		}
	}
	members, err := groupMemberIDs(ctx, q, principal.CompanyID, groupID)
	if err != nil {
		return GroupPreview{}, err
	}
	after := make(map[uuid.UUID][]auth.ProjectPermission, len(members))
	for _, member := range members {
		after[member] = permissions
	}
	return previewGroupChange(ctx, q, principal, groupID, access.Revision, []groupPreviewTarget{{
		projectID: id, visibility: access.Visibility, visible: true, people: members, after: after,
	}})
}
