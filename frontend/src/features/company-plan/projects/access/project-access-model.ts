import {
  PROJECT_COMPANY_FLOOR,
  PROJECT_PERMISSIONS,
  type ProjectPermission,
  type ProjectVisibility,
} from "@/shared/permissions";

// The Access section's rules, kept apart from the markup. A person's own
// set is their stored rows (`granted`); what they hold beyond it comes from
// their Groups or the Company floor and is shown, locked, with its source
// (docs/api/projects.md, "Grants").

/**
 * The viewer's own standing on the Project, as the self-grant cap reads it
 * (docs/api/projects.md, "The self-grant cap"): what they hold there, and
 * whether they manage it through the Company (owners and admins), which
 * lifts the cap.
 */
export type AccessGiver = {
  management: boolean;
  permissions: readonly ProjectPermission[];
};

/**
 * Whether the viewer may give, or change, a set: every permission in it is
 * theirs on this Project, or they manage it through the Company. The server
 * decides the same (`project_grant_exceeds_own`); this only greys things
 * out first.
 */
export function withinGiver(
  permissions: readonly ProjectPermission[],
  giver: AccessGiver,
): boolean {
  return (
    giver.management ||
    permissions.every((permission) => giver.permissions.includes(permission))
  );
}

/** A set in catalog order, so equal sets compare equal. */
export function orderedPermissions(
  permissions: Iterable<ProjectPermission>,
): ProjectPermission[] {
  const set = new Set(permissions);
  return PROJECT_PERMISSIONS.filter((permission) => set.has(permission));
}

export function samePermissions(
  left: readonly ProjectPermission[],
  right: readonly ProjectPermission[],
): boolean {
  const a = orderedPermissions(left);
  const b = orderedPermissions(right);
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

/**
 * Checks or clears one permission. Any permission brings View with it, as
 * the server stores it; View itself can be cleared only when nothing else
 * is checked, and clearing it leaves the selection empty.
 */
export function togglePermission(
  draft: readonly ProjectPermission[],
  permission: ProjectPermission,
  checked: boolean,
): ProjectPermission[] {
  const next = new Set(draft);
  if (checked) {
    next.add(permission);
    next.add("project.read");
  } else {
    next.delete(permission);
    if (permission === "project.read") next.clear();
  }
  return orderedPermissions(next);
}

/** View stays on while any other permission is. */
export function readLocked(draft: readonly ProjectPermission[]): boolean {
  return draft.some((permission) => permission !== "project.read");
}

// Taking View or Run tasks from someone can stop their runs in the Project
// (docs/api/projects.md, "Grants"), so the row asks the server's preview
// first (`grantAsks`); a change that takes neither saves without it. On a
// Project open to the Company everyone keeps both, so nothing stops there.
const RUN_STOPPING: readonly ProjectPermission[] = [
  "project.read",
  "project.tasks.execute",
];

export function stopsRuns(
  before: readonly ProjectPermission[],
  after: readonly ProjectPermission[],
  visibility: ProjectVisibility,
): boolean {
  return (
    visibility !== "company" &&
    RUN_STOPPING.some(
      (permission) =>
        before.includes(permission) && !after.includes(permission),
    )
  );
}

/**
 * Whether a change to one person's own grant asks before it saves, from
 * the server's preview (as `changeAsksFirst` for a Group): "loss" when
 * they lose the Project, "runs" when a run of theirs stops, or null to save
 * straight away.
 */
export function grantAsks(preview: {
  runsStopping: number;
  losesProject: boolean;
}): "loss" | "runs" | null {
  if (preview.losesProject) return "loss";
  return preview.runsStopping > 0 ? "runs" : null;
}

/** One source of a person's access, as the server lists it. */
export type AccessSource = {
  via: "grant" | "group" | "company" | "management";
  groupId?: string;
  permissions: readonly ProjectPermission[];
};

/** What a person's Groups give them on the Project, together. */
export function groupPermissions(
  sources: readonly AccessSource[],
): ProjectPermission[] {
  return orderedPermissions(
    sources
      .filter((source) => source.via === "group")
      .flatMap((source) => source.permissions),
  );
}

/** The Groups a person reaches the Project through, by id. */
export function groupSourceIds(sources: readonly AccessSource[]): string[] {
  return sources.flatMap((source) =>
    source.via === "group" && source.groupId ? [source.groupId] : [],
  );
}

/**
 * Where a permission someone holds without their own row comes from: the
 * Groups that give it, or everyone, on a Project open to the Company.
 */
export type InheritedPermission =
  { via: "group"; names: readonly string[] } | { via: "everyone" };

export type InheritedAccess = ReadonlyMap<
  ProjectPermission,
  InheritedPermission
>;

export const NOTHING_INHERITED: InheritedAccess = new Map();

/**
 * What a person or Group holds on the Project from somewhere other than
 * their own rows: each Group's permissions (named), and the Company floor
 * while the Project is open to everyone. A Group grant passes no sources.
 */
export function inheritedAccess(
  sources: readonly AccessSource[],
  groupName: (groupId: string) => string,
  visibility: ProjectVisibility,
): InheritedAccess {
  const names = new Map<ProjectPermission, string[]>();
  for (const source of sources) {
    if (source.via !== "group" || !source.groupId) continue;
    const name = groupName(source.groupId);
    for (const permission of source.permissions) {
      names.set(permission, [...(names.get(permission) ?? []), name]);
    }
  }
  const inherited = new Map<ProjectPermission, InheritedPermission>();
  for (const permission of PROJECT_PERMISSIONS) {
    const groups = names.get(permission);
    if (
      visibility === "company" &&
      PROJECT_COMPANY_FLOOR.includes(permission)
    ) {
      inherited.set(permission, { via: "everyone" });
    } else if (groups) {
      inherited.set(permission, { via: "group", names: groups });
    }
  }
  return inherited;
}

/** "From Finance", "From Finance +2", or "Everyone has this". */
export function inheritedNote(inherited: InheritedPermission): string {
  if (inherited.via === "everyone") return "Everyone has this";
  const [first = "a group", ...rest] = inherited.names;
  return rest.length > 0 ? `From ${first} +${rest.length}` : `From ${first}`;
}
