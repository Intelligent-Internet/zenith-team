package auth

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

func TestPermissionCatalogueIsNestedByRole(t *testing.T) {
	t.Parallel()

	all := Permissions()
	if len(all) == 0 {
		t.Fatal("Permissions() is empty")
	}
	if !slices.IsSorted(all) {
		t.Errorf("Permissions() = %v, want sorted", all)
	}
	owner := PermissionsOf(CompanyRoleOwner)
	admin := PermissionsOf(CompanyRoleAdmin)
	member := PermissionsOf(CompanyRoleMember)

	if !slices.Equal(owner, all) {
		t.Errorf("owner permissions = %v, want every catalog entry %v", owner, all)
	}
	for _, p := range admin {
		if !CompanyRoleOwner.Can(p) {
			t.Errorf("admin holds %q, which the owner lacks", p)
		}
	}
	for _, p := range member {
		if !CompanyRoleAdmin.Can(p) {
			t.Errorf("member holds %q, which the admin lacks", p)
		}
	}
	for _, p := range all {
		if !CompanyRoleOwner.Can(p) {
			t.Errorf("owner cannot %q", p)
		}
	}
}

func TestOwnerOnlyPermissionsArePinned(t *testing.T) {
	t.Parallel()

	want := []Permission{
		PermissionCommerceManage,
		PermissionCompanyDelete,
		PermissionMailboxManage,
		PermissionMembersOwnersManage,
		PermissionResultsPublish,
	}
	var got []Permission
	for _, p := range Permissions() {
		if CompanyRoleOwner.Can(p) && !CompanyRoleAdmin.Can(p) {
			got = append(got, p)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("owner-only permissions = %v, want %v", got, want)
	}
}

// A member discusses and installs plugins and connects apps on their own
// computers (owner decision, 2026-09-29); every Company-wide management
// permission, reviewing apps' changed tools included, stays above them.
func TestMemberHoldsDiscussionOwnComputerAndWhiteboardPermissions(t *testing.T) {
	t.Parallel()

	got := PermissionsOf(CompanyRoleMember)
	if got == nil {
		t.Fatal("PermissionsOf(member) = nil, want a non-nil permission list")
	}
	if !slices.Equal(
		got,
		[]Permission{
			PermissionConversationsDiscuss,
			PermissionPluginsManage,
			PermissionWhiteboardsCreate,
			PermissionWhiteboardsEdit,
			PermissionWhiteboardsRead,
		},
	) {
		t.Errorf(
			"PermissionsOf(member) = %v, want discussion, own-computer plugin, and collaborative Whiteboard permissions",
			got,
		)
	}
	if CompanyRoleMember.Can(PermissionConnectorsManage) {
		t.Error("member holds connectors.manage, which reviews apps for the whole Company")
	}
	if PermissionsOf(CompanyRole("bogus")) == nil {
		t.Error("PermissionsOf(unknown role) = nil, want an empty non-nil slice")
	}
}

func TestDiscussionPermissionPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		permission Permission
		want       []string
	}{
		{"conversations.discuss", []string{"admin", "member", "owner"}},
		{"channels.manage", []string{"admin", "owner"}},
		{"channels.access_all", []string{}},
	} {
		if got := RolesWith(tc.permission); !slices.Equal(got, tc.want) {
			t.Errorf("RolesWith(%q) = %v, want %v", tc.permission, got, tc.want)
		}
	}
}

func TestRolesWithIsSortedAndBindsAsText(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		permission Permission
		want       []string
	}{
		{PermissionAgentsManage, []string{"admin", "owner"}},
		{PermissionMailboxManage, []string{"owner"}},
		{PermissionCompanyDelete, []string{"owner"}},
		{Permission("bogus"), []string{}},
	} {
		got := RolesWith(testCase.permission)
		if got == nil {
			t.Fatalf(
				"RolesWith(%q) = nil, want a non-nil slice for the text[] parameter",
				testCase.permission,
			)
		}
		if !slices.Equal(got, testCase.want) {
			t.Errorf("RolesWith(%q) = %v, want %v", testCase.permission, got, testCase.want)
		}
	}
}

func TestCompanyRoleValid(t *testing.T) {
	t.Parallel()

	for role, want := range map[CompanyRole]bool{
		CompanyRoleOwner:  true,
		CompanyRoleAdmin:  true,
		CompanyRoleMember: true,
		"":                false,
		"Owner":           false,
		"viewer":          false,
	} {
		if got := role.Valid(); got != want {
			t.Errorf("CompanyRole(%q).Valid() = %t, want %t", role, got, want)
		}
	}
}

func TestUserPrincipalCanRequiresCompanyScope(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	sessionID := uuid.MustParse("0197f590-70bd-78c3-825f-5f6e4df341af")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	unresolved := UserPrincipal{UserID: userID, SessionID: sessionID, AuthKind: TokenKindSession}
	if unresolved.Can(PermissionAgentsManage) {
		t.Error("an unresolved principal can manage agents")
	}
	roleWithoutCompany := unresolved
	roleWithoutCompany.Role = CompanyRoleOwner
	if roleWithoutCompany.Can(PermissionAgentsManage) {
		t.Error("a role without a resolved company grants permissions")
	}
	admin := unresolved
	admin.CompanyID = companyID
	admin.Role = CompanyRoleAdmin
	if !admin.Can(PermissionAgentsManage) {
		t.Error("an admin cannot manage agents")
	}
	if admin.Can(PermissionCompanyDelete) {
		t.Error("an admin can delete the company")
	}
}
