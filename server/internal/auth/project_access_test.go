package auth

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestProjectAccessEffectivePermissions(t *testing.T) {
	t.Parallel()

	management := ManagementProjectPermissions()
	for _, test := range []struct {
		name        string
		row         generated.ResolveProjectAccessRow
		want        []ProjectPermission
		wantVisible bool
	}{
		{
			name: "no row, no bridge holds nothing",
			row:  generated.ResolveProjectAccessRow{Visibility: "restricted", GrantedPermissions: []string{}},
			want: []ProjectPermission{},
		},
		{
			name: "granted rows on a restricted Project",
			row: generated.ResolveProjectAccessRow{
				Visibility: "restricted", Via: string(ProjectAccessViaGrant),
				GrantedPermissions: []string{"project.read", "project.files.manage"},
			},
			want:        []ProjectPermission{ProjectPermissionFilesManage, ProjectPermissionRead},
			wantVisible: true,
		},
		{
			name: "company floor adds the defaults",
			row: generated.ResolveProjectAccessRow{
				Visibility: "company", Via: string(ProjectAccessViaCompany), GrantedPermissions: []string{},
			},
			want:        CompanyProjectDefaultPermissions(),
			wantVisible: true,
		},
		{
			name: "company floor unions with grant rows",
			row: generated.ResolveProjectAccessRow{
				Visibility: "company", Via: string(ProjectAccessViaGrant),
				GrantedPermissions: []string{"project.read", "project.approvals.resolve"},
			},
			want: []ProjectPermission{
				ProjectPermissionApprovalsResolve, ProjectPermissionRead,
				ProjectPermissionTasksExecute, ProjectPermissionTasksWrite,
			},
			wantVisible: true,
		},
		{
			name: "bridge without read holds the management names only",
			row: generated.ResolveProjectAccessRow{
				Visibility: "restricted", Management: true, GrantedPermissions: []string{},
			},
			want:        management,
			wantVisible: true,
		},
		{
			name: "bridge with read adds management to the rows",
			row: generated.ResolveProjectAccessRow{
				Visibility: "restricted", Via: string(ProjectAccessViaGrant), Management: true,
				GrantedPermissions: []string{"project.read", "project.archive"},
			},
			want: []ProjectPermission{
				ProjectPermissionArchive, ProjectPermissionGrantsManage,
				ProjectPermissionRead, ProjectPermissionSettingsUpdate,
			},
			wantVisible: true,
		},
		{
			name: "a Group's rows alone make a reader",
			row: generated.ResolveProjectAccessRow{
				Visibility: "restricted", Via: string(ProjectAccessViaGroup), GrantedPermissions: []string{},
				GroupPermissions: []string{"project.read", "project.tasks.write"},
			},
			want:        []ProjectPermission{ProjectPermissionRead, ProjectPermissionTasksWrite},
			wantVisible: true,
		},
		{
			name: "own rows and a Group's rows are a union",
			row: generated.ResolveProjectAccessRow{
				Visibility: "restricted", Via: string(ProjectAccessViaGrant),
				GrantedPermissions: []string{"project.read", "project.approvals.resolve"},
				GroupPermissions:   []string{"project.read", "project.tasks.execute"},
			},
			want: []ProjectPermission{
				ProjectPermissionApprovalsResolve, ProjectPermissionRead, ProjectPermissionTasksExecute,
			},
			wantVisible: true,
		},
		{
			name: "Group rows without read count for nothing",
			row: generated.ResolveProjectAccessRow{
				Visibility: "restricted", GrantedPermissions: []string{},
				GroupPermissions: []string{"project.archive"},
			},
			want: []ProjectPermission{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			access := projectAccessFromRow(test.row)
			if access.Permissions == nil || !slices.Equal(access.Permissions, test.want) {
				t.Errorf("Permissions = %#v, want %v", access.Permissions, test.want)
			}
			if access.Visible() != test.wantVisible {
				t.Errorf("Visible() = %t, want %t", access.Visible(), test.wantVisible)
			}
			for _, permission := range ProjectPermissions() {
				if access.Can(permission) != slices.Contains(test.want, permission) {
					t.Errorf("Can(%q) = %t", permission, access.Can(permission))
				}
			}
		})
	}
}

// Granted is the person's own rows: what the person grant editor shows.
// GroupGranted is what their Groups hold. The floor and the bridge are never
// among them, a Group's rows never land in Granted, and a non-reader has
// neither.
func TestProjectAccessGrantedIsTheStoredRowsOfAReader(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		facts     ProjectAccessFacts
		want      []ProjectPermission
		wantGroup []ProjectPermission
	}{
		{
			name: "own and Group rows stay apart",
			facts: ProjectAccessFacts{
				Visibility: "restricted", Via: string(ProjectAccessViaGrant),
				GrantedPermissions: []string{"project.read"},
				GroupPermissions:   []string{"project.tasks.write", "project.read", "project.read"},
			},
			want:      []ProjectPermission{ProjectPermissionRead},
			wantGroup: []ProjectPermission{ProjectPermissionRead, ProjectPermissionTasksWrite},
		},
		{
			name: "a Group reader has no own rows",
			facts: ProjectAccessFacts{
				Visibility: "company", Via: string(ProjectAccessViaGroup), GrantedPermissions: []string{},
				GroupPermissions: []string{"project.read", "project.files.manage"},
			},
			want:      []ProjectPermission{},
			wantGroup: []ProjectPermission{ProjectPermissionFilesManage, ProjectPermissionRead},
		},
		{
			name: "reader's rows, sorted",
			facts: ProjectAccessFacts{
				Visibility: "company", Via: string(ProjectAccessViaGrant), Management: true,
				GrantedPermissions: []string{"project.read", "project.files.manage"},
			},
			want: []ProjectPermission{ProjectPermissionFilesManage, ProjectPermissionRead},
		},
		{
			name:  "floor only",
			facts: ProjectAccessFacts{Visibility: "company", Via: string(ProjectAccessViaCompany), GrantedPermissions: []string{}},
			want:  []ProjectPermission{},
		},
		{
			name: "bridge without read ignores stale rows",
			facts: ProjectAccessFacts{
				Visibility: "restricted", Management: true, GrantedPermissions: []string{"project.archive"},
			},
			want: []ProjectPermission{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			access := ProjectAccessOf(test.facts)
			if access.Granted == nil || !slices.Equal(access.Granted, test.want) {
				t.Fatalf("Granted = %#v, want %v", access.Granted, test.want)
			}
			wantGroup := test.wantGroup
			if wantGroup == nil {
				wantGroup = []ProjectPermission{}
			}
			if access.GroupGranted == nil || !slices.Equal(access.GroupGranted, wantGroup) {
				t.Fatalf("GroupGranted = %#v, want %v", access.GroupGranted, wantGroup)
			}
		})
	}
}

// A principal the company middleware has not resolved holds nothing, and the
// check answers before touching the database.
func TestProjectAccessChecksRefuseUnresolvedPrincipal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	principal := UserPrincipal{UserID: uuid.New()}
	projectID := uuid.New()
	if _, err := ResolveProjectAccess(ctx, nil, principal, projectID); !errors.Is(err, ErrProjectNotVisible) {
		t.Errorf("ResolveProjectAccess error = %v, want ErrProjectNotVisible", err)
	}
	if _, err := LockProjectAccess(ctx, nil, principal, projectID); !errors.Is(err, ErrProjectNotVisible) {
		t.Errorf("LockProjectAccess error = %v, want ErrProjectNotVisible", err)
	}
	if _, err := RequireProjectPermission(ctx, nil, principal, projectID, ProjectPermissionRead); !errors.Is(err, ErrProjectNotVisible) {
		t.Errorf("RequireProjectPermission error = %v, want ErrProjectNotVisible", err)
	}
	if _, err := RequireTaskPermission(ctx, nil, principal, uuid.New(), ProjectPermissionRead); !errors.Is(err, ErrTaskNotVisible) {
		t.Errorf("RequireTaskPermission error = %v, want ErrTaskNotVisible", err)
	}
	if _, err := ResolveMemberProjectAccess(ctx, nil, uuid.New(), uuid.Nil, projectID); !errors.Is(err, ErrProjectNotVisible) {
		t.Errorf("ResolveMemberProjectAccess error = %v, want ErrProjectNotVisible", err)
	}
	if held, err := HoldsProjectPermission(ctx, nil, uuid.Nil, projectID, principal.UserID, ProjectPermissionRead); held || err != nil {
		t.Errorf("HoldsProjectPermission = %t, %v, want false", held, err)
	}
	if err := RequireProjectPermissionHeld(ctx, nil, uuid.Nil, projectID, principal.UserID, ProjectPermissionRead); !errors.Is(err, ErrProjectPermissionRequired) {
		t.Errorf("RequireProjectPermissionHeld error = %v, want ErrProjectPermissionRequired", err)
	}
	// The Task chat checks refuse before reading anything, so an unresolved
	// principal never falls back to a Company rule.
	if _, scoped, err := RequireTaskChatPermission(ctx, nil, principal, uuid.New(), ProjectPermissionApprovalsResolve); !scoped || !errors.Is(err, ErrTaskNotVisible) {
		t.Errorf("RequireTaskChatPermission = scoped %t, %v, want scoped ErrTaskNotVisible", scoped, err)
	}
	if _, err := RequireProjectContentPermission(ctx, nil, principal, projectID, ProjectPermissionApprovalsResolve); !errors.Is(err, ErrTaskNotVisible) {
		t.Errorf("RequireProjectContentPermission error = %v, want ErrTaskNotVisible", err)
	}
	if id, scoped, err := TaskChatProjectID(ctx, nil, uuid.Nil, uuid.New()); id != uuid.Nil || scoped || err != nil {
		t.Errorf("TaskChatProjectID(no Company) = %v, %t, %v; want no Project", id, scoped, err)
	}
}
