import type { ReactNode } from "react";

import {
  hasProjectPermission,
  type ProjectAccessContext,
  type ProjectPermission,
} from "./project-permissions";

type RequireProjectPermissionProps = {
  /** The Project's or Task's own `access`, as the read returned it. */
  access: ProjectAccessContext | null | undefined;
  permission: ProjectPermission;
  /** Rendered when the caller's access lacks the permission. */
  fallback?: ReactNode;
  children: ReactNode;
};

/**
 * Renders `children` only while the Project or Task read says the caller
 * holds `permission`. It never reads the session or a role.
 */
export function RequireProjectPermission({
  access,
  permission,
  fallback = null,
  children,
}: RequireProjectPermissionProps) {
  return <>{hasProjectPermission(access, permission) ? children : fallback}</>;
}
