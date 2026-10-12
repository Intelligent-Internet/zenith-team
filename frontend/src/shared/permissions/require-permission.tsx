import type { ReactNode } from "react";

import type { CompanyPermission } from "./company-permissions";
import { useCan } from "./use-company-permissions";

type RequirePermissionProps = {
  permission: CompanyPermission;
  /** Rendered when the active company's role lacks the permission. */
  fallback?: ReactNode;
  children: ReactNode;
};

/**
 * Renders `children` only while the active company's resolved permissions
 * include `permission`. The router passes the access state it wants shown
 * as `fallback`, which keeps this module free of feature imports.
 */
export function RequirePermission({
  permission,
  fallback = null,
  children,
}: RequirePermissionProps) {
  const allowed = useCan(permission);
  return <>{allowed ? children : fallback}</>;
}
