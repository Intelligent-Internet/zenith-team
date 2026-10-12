import { useMemo } from "react";

import { useSessionSnapshot } from "@/app/session/use-session-snapshot";

import {
  hasPermission,
  type CompanyPermission,
  type CompanyRole,
} from "./company-permissions";

export type CompanyPermissions = {
  companyId?: string;
  userId?: string;
  role?: CompanyRole;
  permissions: readonly CompanyPermission[];
  can: (permission: CompanyPermission) => boolean;
};

const NO_PERMISSIONS: readonly CompanyPermission[] = [];

/**
 * The active company's role and resolved permissions, read reactively from
 * the session snapshot so a pushed role change re-renders every gate.
 */
export function useCompanyPermissions(): CompanyPermissions {
  const session = useSessionSnapshot();
  const company = session.company;
  const userId = session.userId;
  return useMemo(() => {
    const permissions = company?.permissions ?? NO_PERMISSIONS;
    const context = { role: company?.role, permissions };
    return {
      companyId: company?.id,
      userId,
      role: company?.role,
      permissions,
      can: (permission: CompanyPermission) =>
        hasPermission(context, permission),
    };
  }, [company?.id, company?.role, company?.permissions, userId]);
}

export function useCan(permission: CompanyPermission): boolean {
  return useCompanyPermissions().can(permission);
}
