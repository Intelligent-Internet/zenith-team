package auth

import (
	"errors"
	"fmt"
	"slices"
)

// ProjectPermission names one action a person may hold on one Project
// (ADR 0017). It is the Project-level twin of Permission: Company roles
// stay owner|admin|member, and access inside a Project is a set of these
// granted to a person, one project_grants row each. The closed set below is
// mirrored by the project_grants_permission_check and
// project_group_grants_permission_check CHECKs and pinned by a test.
// "Review task results" (project.results.review) was removed by migration
// 000102 (D31 revision, ruled 2026-10-07, built 2026-10-08): nothing
// checked it.
type ProjectPermission string

const (
	ProjectPermissionRead             ProjectPermission = "project.read"
	ProjectPermissionTasksWrite       ProjectPermission = "project.tasks.write"
	ProjectPermissionTasksExecute     ProjectPermission = "project.tasks.execute"
	ProjectPermissionFilesManage      ProjectPermission = "project.files.manage"
	ProjectPermissionApprovalsResolve ProjectPermission = "project.approvals.resolve"
	ProjectPermissionVisibilityUpdate ProjectPermission = "project.visibility.update"
	ProjectPermissionSettingsUpdate   ProjectPermission = "project.settings.update"
	ProjectPermissionGrantsManage     ProjectPermission = "project.grants.manage"
	ProjectPermissionArchive          ProjectPermission = "project.archive"
)

// ProjectVisibility is who sees a Project without a grant row: nobody
// (restricted, the default) or every active member (company), who then holds
// CompanyProjectDefaultPermissions.
type ProjectVisibility string

const (
	ProjectVisibilityCompany    ProjectVisibility = "company"
	ProjectVisibilityRestricted ProjectVisibility = "restricted"
)

// ErrProjectPermissionUnknown rejects a name outside the catalog. The
// error NormalizeProjectGrant returns is a *ProjectPermissionUnknownError
// that matches it and names the permission.
var ErrProjectPermissionUnknown = errors.New("unknown project permission")

// ProjectPermissionUnknownError names the first permission a request asked
// for that is not in the catalog, such as project.results.review, which
// migration 000102 removed and a desktop built before it still sends.
type ProjectPermissionUnknownError struct {
	Name string
}

func (e *ProjectPermissionUnknownError) Error() string {
	return fmt.Sprintf("%s: %q", ErrProjectPermissionUnknown, e.Name)
}

// Is matches ErrProjectPermissionUnknown.
func (e *ProjectPermissionUnknownError) Is(target error) bool {
	return target == ErrProjectPermissionUnknown
}

// ProjectPermissions lists the whole catalog, sorted.
func ProjectPermissions() []ProjectPermission {
	return []ProjectPermission{
		ProjectPermissionApprovalsResolve,
		ProjectPermissionArchive,
		ProjectPermissionFilesManage,
		ProjectPermissionGrantsManage,
		ProjectPermissionRead,
		ProjectPermissionSettingsUpdate,
		ProjectPermissionTasksExecute,
		ProjectPermissionTasksWrite,
		ProjectPermissionVisibilityUpdate,
	}
}

// Valid reports whether p is in the catalog.
func (p ProjectPermission) Valid() bool {
	return slices.Contains(ProjectPermissions(), p)
}

// ManagementProjectPermissions are the three permissions Company
// projects.manage grants on every Project, including one the holder cannot
// read. They manage the Project and never open its content: reading, files,
// deciding and visibility are always explicit grants.
func ManagementProjectPermissions() []ProjectPermission {
	return []ProjectPermission{
		ProjectPermissionArchive,
		ProjectPermissionGrantsManage,
		ProjectPermissionSettingsUpdate,
	}
}

// CompanyProjectDefaultPermissions is the floor every active member holds on
// a Company-visible Project.
func CompanyProjectDefaultPermissions() []ProjectPermission {
	return []ProjectPermission{
		ProjectPermissionRead,
		ProjectPermissionTasksExecute,
		ProjectPermissionTasksWrite,
	}
}

// ProjectPermissionStrings converts a set to the []string a sqlc text[]
// parameter binds. The result is never nil.
func ProjectPermissionStrings(permissions []ProjectPermission) []string {
	out := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		out = append(out, string(permission))
	}
	return out
}

// NormalizeProjectGrant turns requested names into the set a grant stores:
// every name must be in the catalog, duplicates collapse, the result is
// sorted, and project.read is added to any non-empty set so a grant row and
// project_access always agree. An empty request stays empty (remove the
// person).
func NormalizeProjectGrant(names []string) ([]ProjectPermission, error) {
	out := make([]ProjectPermission, 0, len(names)+1)
	for _, name := range names {
		permission := ProjectPermission(name)
		if !permission.Valid() {
			return nil, &ProjectPermissionUnknownError{Name: name}
		}
		out = append(out, permission)
	}
	if len(out) > 0 {
		out = append(out, ProjectPermissionRead)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
