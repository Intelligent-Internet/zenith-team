import type { components as ApiComponents } from "@/shared/api/generated/api.generated";

/**
 * One action the server checks. The server resolves a member's set from
 * their role and publishes it on every Company read and on the
 * `auth.company_access_changed` frame. Gate every affordance on the set,
 * never on the role name, so client and server cannot drift.
 */
export type CompanyPermission = ApiComponents["schemas"]["CompanyPermission"];
export type CompanyRole = ApiComponents["schemas"]["CompanyRole"];

export const COMPANY_ROLES: readonly CompanyRole[] = [
  "owner",
  "admin",
  "member",
];

export const ROLE_LABELS: Record<CompanyRole, string> = {
  owner: "Owner",
  admin: "Admin",
  member: "Member",
};

/** One line per role for pickers and the roles explainer. */
export const ROLE_DESCRIPTIONS: Record<CompanyRole, string> = {
  owner:
    "Everything an Admin can do, plus deleting the company, managing owners.",
  admin:
    "Manages people, agents, settings and the company plan. Can't delete the company or manage owners.",
  member:
    "Does the work: tasks, chat, their own runtimes and the plugins and apps on them. No company settings.",
};

export function roleLabel(role?: CompanyRole | string | null): string {
  if (role === "owner" || role === "admin") return ROLE_LABELS[role];
  return ROLE_LABELS.member;
}

/**
 * Whatever carries a role and its resolved permissions: the session's
 * company draft, a `Company` DTO, or an `auth.company_access_changed` frame.
 */
export type PermissionContext = {
  role?: CompanyRole | null;
  permissions?: readonly CompanyPermission[] | null;
};

export function hasPermission(
  context: PermissionContext | null | undefined,
  permission: CompanyPermission,
): boolean {
  return context?.permissions?.includes(permission) ?? false;
}

/**
 * The rows of the roles explainer. Each row names one permission (or the
 * pair it stands in for) so the matrix stays a projection of the server's
 * catalog rather than a second copy of it.
 */
export const ROLE_MATRIX: readonly {
  label: string;
  permission: CompanyPermission;
}[] = [
  {
    label: "Edit the company profile and plan",
    permission: "company.profile.update",
  },
  { label: "Invite people", permission: "members.invite" },
  { label: "Change roles and remove members", permission: "members.manage" },
  { label: "Manage agents", permission: "agents.manage" },
  // A computer belongs to whoever registered it (ADR 0014), so there is no
  // company permission for runtimes; assigning work is the one that moves
  // a Task to somebody else's computer.
  { label: "Assign tasks to other people", permission: "tasks.assign" },
  // Plugins and apps live on each person's own computer (ADR 0014), so
  // every role installs and connects there; accepting an app's changed tools
  // is the Company's (owner decision, 2026-09-29).
  {
    label: "Install plugins and connect apps on their own computer",
    permission: "plugins.manage",
  },
  {
    label: "Review apps whose tools changed",
    permission: "connectors.manage",
  },
  { label: "Manage projects and schedules", permission: "projects.manage" },
  {
    label: "Resolve approvals",
    permission: "approvals.resolve",
  },
  {
    label: "Turn Proactive Mode on or off",
    permission: "company.proactive_mode.manage",
  },
  { label: "See usage", permission: "usage.read" },
  { label: "Use Mailbox and Agent Mail", permission: "mailbox.manage" },
  {
    label: "Manage owners",
    permission: "members.owners.manage",
  },
  { label: "Delete the company", permission: "company.delete" },
];

export function isPermissionDenied(
  error: { code?: string | null } | null | undefined,
): boolean {
  return error?.code === "company_permission_required";
}
