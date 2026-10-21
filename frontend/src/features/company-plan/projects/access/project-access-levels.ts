import { PROJECT_GRANT_EXCEEDS_OWN_COPY } from "@/lib/api-error-copy";
import {
  PROJECT_COMPANY_FLOOR,
  PROJECT_PERMISSIONS,
  type ProjectPermission,
  type ProjectVisibility,
} from "@/shared/permissions";

import {
  samePermissions,
  withinGiver,
  type AccessGiver,
} from "./project-access-model";

// Access levels are quick presets in the access picker only: picking one
// sets the selection to its exact permissions, which are stored as the
// person's raw grant rows. The server and the API know nothing about
// levels. A row names the highest level a set holds and counts the rest
// ("View +1"), never a made-up name.
// A new permission joins the levels that should include it.

export type ProjectAccessLevelId = "view" | "work" | "decide" | "full";

export type ProjectAccessLevel = {
  id: ProjectAccessLevelId;
  /** Everywhere a level is named: the label, the preset row, Activity. */
  shortLabel: string;
  /** What the level gives, in at most four words (its tooltip). */
  description: string;
  permissions: readonly ProjectPermission[];
};

const WORK: readonly ProjectPermission[] = [
  "project.read",
  "project.tasks.write",
  "project.tasks.execute",
];

const DECIDE: readonly ProjectPermission[] = [
  ...WORK,
  "project.approvals.resolve",
];

/** Lowest first; each level holds everything the one before it does. */
export const PROJECT_ACCESS_LEVELS: readonly ProjectAccessLevel[] = [
  {
    id: "view",
    shortLabel: "View",
    description: "See tasks and files",
    permissions: ["project.read"],
  },
  {
    id: "work",
    shortLabel: "Edit and run",
    description: "Create and run tasks",
    permissions: WORK,
  },
  {
    id: "decide",
    shortLabel: "Approve",
    description: "Plus answering approvals",
    permissions: DECIDE,
  },
  {
    id: "full",
    shortLabel: "Manage",
    description: "Everything",
    permissions: PROJECT_PERMISSIONS,
  },
];

/** The level a set is exactly, or null when it matches none. */
export function accessLevelFor(
  permissions: readonly ProjectPermission[],
): ProjectAccessLevel | null {
  return (
    PROJECT_ACCESS_LEVELS.find((level) =>
      samePermissions(level.permissions, permissions),
    ) ?? null
  );
}

/**
 * The highest level a set holds all of, or null when it holds none (no
 * View). A row names it, then counts what the set holds beyond it.
 */
export function containedLevel(
  permissions: readonly ProjectPermission[],
): ProjectAccessLevel | null {
  return (
    [...PROJECT_ACCESS_LEVELS]
      .reverse()
      .find((level) =>
        level.permissions.every((permission) =>
          permissions.includes(permission),
        ),
      ) ?? null
  );
}

/**
 * Why a level can't be picked for this person, or null when it can: on a
 * Project open to the Company the levels below the floor, and under the
 * self-grant cap every level the viewer does not fully hold.
 */
export function levelBlockedReason(
  level: ProjectAccessLevel,
  visibility: ProjectVisibility,
  giver: AccessGiver,
): string | null {
  if (
    visibility === "company" &&
    PROJECT_COMPANY_FLOOR.some(
      (permission) => !level.permissions.includes(permission),
    )
  ) {
    return "Everyone has this";
  }
  if (!withinGiver(level.permissions, giver)) {
    return PROJECT_GRANT_EXCEEDS_OWN_COPY;
  }
  return null;
}
