package auth

import (
	"slices"
	"sort"

	"github.com/google/uuid"
)

// Permission names one action the server checks. Roles grant permissions and
// code checks permissions: a handler, service or query asks Can(permission),
// never "is this an owner", so the product policy — which role may do what —
// is stated once in the grants table below. Adding a role or a permission is
// a row here, and the resolved set travels to the client on every Company
// read, so the client never re-derives policy from the role name.
//
// Three rules deliberately live outside this table. Runtime ownership
// (ADR 0014) belongs to the registrant of a machine — it executes only their
// own work and nobody else sees it — and no role overrides it. A Task's
// assignee alone starts its Runs, because they execute on the assignee's
// own machine; tasks.assign is the one permission touching assignment, and it
// admits assigning a Task to someone else and handing one off. Actor type —
// a signed-in person versus a Run acting through its proof — is a property
// of the request, not of the role (middleware.RequireHumanActor).
type Permission string

const (
	PermissionConversationsDiscuss       Permission = "conversations.discuss"
	PermissionChannelsManage             Permission = "channels.manage"
	PermissionCompanyProfileUpdate       Permission = "company.profile.update"
	PermissionCompanyDelete              Permission = "company.delete"
	PermissionCompanyPlanApply           Permission = "company.plan.apply"
	PermissionCompanyProactiveModeManage Permission = "company.proactive_mode.manage"
	PermissionCompanyResourcesManage     Permission = "company.resources.manage"
	PermissionUsageRead                  Permission = "usage.read"
	PermissionMembersInvite              Permission = "members.invite"
	PermissionMembersManage              Permission = "members.manage"
	PermissionMembersOwnersManage        Permission = "members.owners.manage"
	PermissionAgentsManage               Permission = "agents.manage"
	PermissionTasksAssign                Permission = "tasks.assign"
	PermissionConnectorsManage           Permission = "connectors.manage"
	PermissionPluginsManage              Permission = "plugins.manage"
	PermissionProjectsManage             Permission = "projects.manage"
	PermissionWikiPublish                Permission = "wiki.publish"
	PermissionCalendarManage             Permission = "calendar.manage"
	PermissionSchedulesManage            Permission = "schedules.manage"
	PermissionApprovalsResolve           Permission = "approvals.resolve"
	PermissionResultsReview              Permission = "results.review"
	PermissionResultsPublish             Permission = "results.publish"
	PermissionMailboxManage              Permission = "mailbox.manage"
	PermissionCommerceRead               Permission = "commerce.read"
	PermissionCommerceManage             Permission = "commerce.manage"
)

var (
	ownerOnly     = []CompanyRole{CompanyRoleOwner}
	ownerAndAdmin = []CompanyRole{CompanyRoleOwner, CompanyRoleAdmin}
	everyRole     = []CompanyRole{CompanyRoleOwner, CompanyRoleAdmin, CompanyRoleMember}
)

// grants is the one table. An owner holds every permission; an admin holds
// everything except deleting the company, managing owners, managing commerce, using Mailbox
// and publishing results. A member holds conversations.discuss and
// plugins.manage, and works through the other member-gated routes.
// plugins.manage installs plugins and connects apps on the holder's own
// computers, the only computers any Plugins action reaches (ADR 0014), so
// every role holds it (owner decision, 2026-09-29); reviewing an app's
// changed tools and the Company's Connector policy stay with
// connectors.manage. The order matches the catalog above; every accessor
// sorts its output.
var grants = []struct {
	permission Permission
	roles      []CompanyRole
}{
	{PermissionConversationsDiscuss, everyRole},
	{PermissionChannelsManage, ownerAndAdmin},
	{PermissionCompanyProfileUpdate, ownerAndAdmin},
	{PermissionCompanyDelete, ownerOnly},
	{PermissionCompanyPlanApply, ownerAndAdmin},
	{PermissionCompanyProactiveModeManage, ownerAndAdmin},
	{PermissionCompanyResourcesManage, ownerAndAdmin},
	{PermissionUsageRead, ownerAndAdmin},
	{PermissionMembersInvite, ownerAndAdmin},
	{PermissionMembersManage, ownerAndAdmin},
	{PermissionMembersOwnersManage, ownerOnly},
	{PermissionAgentsManage, ownerAndAdmin},
	{PermissionTasksAssign, ownerAndAdmin},
	{PermissionConnectorsManage, ownerAndAdmin},
	{PermissionPluginsManage, everyRole},
	{PermissionProjectsManage, ownerAndAdmin},
	{PermissionWikiPublish, ownerAndAdmin},
	{PermissionCalendarManage, ownerAndAdmin},
	{PermissionSchedulesManage, ownerAndAdmin},
	{PermissionApprovalsResolve, ownerAndAdmin},
	{PermissionResultsReview, ownerAndAdmin},
	{PermissionResultsPublish, ownerOnly},
	{PermissionMailboxManage, ownerOnly},
	{PermissionCommerceRead, ownerAndAdmin},
	{PermissionCommerceManage, ownerOnly},
}

// Valid reports whether r is one of the three catalog roles.
func (r CompanyRole) Valid() bool {
	return r == CompanyRoleOwner || r == CompanyRoleAdmin || r == CompanyRoleMember
}

// Can reports whether the role holds the permission.
func (r CompanyRole) Can(p Permission) bool {
	for _, grant := range grants {
		if grant.permission == p {
			return slices.Contains(grant.roles, r)
		}
	}
	return false
}

// Can reports whether the principal's resolved company role holds the
// permission. A principal the company middleware has not resolved holds
// nothing, whatever its Role field says.
func (p UserPrincipal) Can(permission Permission) bool {
	return p.CompanyID != uuid.Nil && p.Role.Can(permission)
}

// Permissions lists the whole catalog, sorted.
func Permissions() []Permission {
	all := make([]Permission, 0, len(grants))
	for _, grant := range grants {
		all = append(all, grant.permission)
	}
	slices.Sort(all)
	return all
}

// PermissionsOf resolves a role to its sorted permission set. The result is
// never nil so a member's set serializes as [] rather than null.
func PermissionsOf(r CompanyRole) []Permission {
	permissions := make([]Permission, 0, len(grants))
	for _, grant := range grants {
		if slices.Contains(grant.roles, r) {
			permissions = append(permissions, grant.permission)
		}
	}
	slices.Sort(permissions)
	return permissions
}

// AllRoles lists every catalog role as sorted strings, the shape a sqlc
// text[] parameter binds directly — for a query whose role arm must admit
// every active member.
func AllRoles() []string {
	return []string{string(CompanyRoleAdmin), string(CompanyRoleMember), string(CompanyRoleOwner)}
}

// RolesWith lists the roles holding the permission as sorted strings, the
// shape a sqlc text[] parameter binds directly.
func RolesWith(p Permission) []string {
	roles := []string{}
	for _, grant := range grants {
		if grant.permission != p {
			continue
		}
		for _, role := range grant.roles {
			roles = append(roles, string(role))
		}
	}
	sort.Strings(roles)
	return roles
}
