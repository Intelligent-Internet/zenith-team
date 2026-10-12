import type { components as ApiComponents } from "@/shared/api/generated/api.generated";

import {
  hasPermission,
  type CompanyRole,
  type PermissionContext,
} from "./company-permissions";

/**
 * Pure mirrors of the server's membership rules. Every surface — a disabled
 * menu item, a tooltip, a hidden column — reads the same `reason`, so the
 * copy for a refusal lives in exactly one place.
 */
export type DecisionReason =
  | "not_permitted"
  | "self"
  | "last_owner"
  | "owner_target"
  | "owner_only"
  | "unchanged";

export type Decision =
  { allowed: true } | { allowed: false; reason: DecisionReason };

const ALLOW: Decision = { allowed: true };
const deny = (reason: DecisionReason): Decision => ({ allowed: false, reason });

export const DECISION_COPY: Record<DecisionReason, string> = {
  not_permitted: "You don't have permission to manage members.",
  self: "You can't change your own membership here. Ask another Owner to change your role, or use Leave team.",
  last_owner: "A company needs at least one Owner. Assign another Owner first.",
  owner_target: "Only an Owner can change another Owner.",
  owner_only: "Only an Owner can assign the Owner role.",
  unchanged: "This is already their role.",
};

export function decisionTitle(decision: Decision): string | undefined {
  return decision.allowed ? undefined : DECISION_COPY[decision.reason];
}

export type MembershipContext = PermissionContext & { userId?: string | null };

export type MembershipTarget = Pick<
  ApiComponents["schemas"]["ActiveMember"],
  "actor" | "role" | "accountDisabled"
>;

/** The roles the caller may assign to someone else. */
export function assignableRoles(context: MembershipContext): CompanyRole[] {
  if (!hasPermission(context, "members.manage")) return [];
  if (hasPermission(context, "members.owners.manage"))
    return ["owner", "admin", "member"];
  return ["admin", "member"];
}

export function canChangeMemberRole(
  target: MembershipTarget,
  context: MembershipContext,
  ownerCount: number,
  nextRole?: CompanyRole,
): Decision {
  if (!hasPermission(context, "members.manage")) return deny("not_permitted");
  if (target.actor.id === context.userId) return deny("self");
  if (
    target.role === "owner" &&
    !hasPermission(context, "members.owners.manage")
  ) {
    return deny("owner_target");
  }
  if (target.role === "owner" && ownerCount <= 1) return deny("last_owner");
  if (
    nextRole === "owner" &&
    !hasPermission(context, "members.owners.manage")
  ) {
    return deny("owner_only");
  }
  if (nextRole !== undefined && nextRole === target.role)
    return deny("unchanged");
  return ALLOW;
}

export function canRemoveMember(
  target: MembershipTarget,
  context: MembershipContext,
  ownerCount: number,
): Decision {
  if (!hasPermission(context, "members.manage")) return deny("not_permitted");
  if (target.actor.id === context.userId) return deny("self");
  if (
    target.role === "owner" &&
    !hasPermission(context, "members.owners.manage")
  ) {
    return deny("owner_target");
  }
  if (target.role === "owner" && ownerCount <= 1) return deny("last_owner");
  return ALLOW;
}

export function canLeave(
  context: MembershipContext,
  ownerCount: number,
): Decision {
  if (context.role === "owner" && ownerCount <= 1) return deny("last_owner");
  return ALLOW;
}
