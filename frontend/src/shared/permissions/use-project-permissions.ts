import { useMemo } from "react";

import type { components as ApiComponents } from "@/shared/api/generated/api.generated";

import {
  hasProjectPermission,
  type ProjectPermission,
} from "./project-permissions";

type ProjectAccessDto = ApiComponents["schemas"]["ProjectAccess"];
type TaskAccessDto = ApiComponents["schemas"]["TaskAccess"];

export type ProjectAccessView = {
  permissions: readonly ProjectPermission[];
  /** The caller reads the Project's content (holds `project.read`). */
  reads: boolean;
  /** The caller holds the management permissions through their role. */
  management: boolean;
  /** The Project's access revision, sent back on every access change. */
  revision: number | null;
  can: (permission: ProjectPermission) => boolean;
};

const NONE: readonly ProjectPermission[] = [];

function view(
  permissions: readonly ProjectPermission[] | undefined,
  management = false,
  revision: number | null = null,
): ProjectAccessView {
  const list = permissions ?? NONE;
  const context = { permissions: list };
  return {
    permissions: list,
    reads: hasProjectPermission(context, "project.read"),
    management,
    revision,
    can: (permission) => hasProjectPermission(context, permission),
  };
}

/**
 * The caller's standing on a Project, straight from the Project read's
 * `access`. Nothing here looks at the session or a role.
 */
export function useProjectAccess(
  project: { access?: ProjectAccessDto | null } | null | undefined,
): ProjectAccessView {
  const access = project?.access;
  return useMemo(
    () => view(access?.permissions, access?.management, access?.revision),
    [access?.permissions, access?.management, access?.revision],
  );
}

/**
 * The caller's permissions on a Task's Project, from the Task read. Realtime
 * Task frames leave `access` out, so a Task that has none yet grants nothing.
 */
export function useTaskAccess(
  task: { access?: TaskAccessDto | null } | null | undefined,
): ProjectAccessView {
  const permissions = task?.access?.permissions;
  return useMemo(() => view(permissions), [permissions]);
}
