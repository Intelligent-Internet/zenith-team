import type { ProjectPermission } from "@/shared/permissions";
import {
  Archive,
  Building2,
  CircleCheck,
  Eye,
  Folder,
  Globe,
  Key,
  Pencil,
  Play,
  Settings,
  UsersRound,
  type IconComponent,
} from "@/shared/ui/icons";

import {
  containedLevel,
  type ProjectAccessLevelId,
} from "./project-access-levels";
import {
  orderedPermissions,
  type InheritedAccess,
} from "./project-access-model";

// One icon per permission, on the picker's checklist. The catalog order of
// PROJECT_PERMISSIONS is the order they are listed in.
export const PROJECT_PERMISSION_ICONS: Record<
  ProjectPermission,
  IconComponent
> = {
  "project.read": Eye,
  "project.tasks.write": Pencil,
  "project.tasks.execute": Play,
  "project.files.manage": Folder,
  "project.approvals.resolve": CircleCheck,
  "project.settings.update": Settings,
  "project.grants.manage": Key,
  "project.archive": Archive,
  "project.visibility.update": Globe,
};

/** The preset buttons' icons, the same as the permission each one adds. */
export const PROJECT_ACCESS_LEVEL_ICONS: Record<
  ProjectAccessLevelId,
  IconComponent
> = {
  view: Eye,
  work: Pencil,
  decide: CircleCheck,
  full: Settings,
};

// The tooltips above dialogs and the picker (project-access.css).
export const ACCESS_TOOLTIP_CLASS = "project-access-tooltip";

/** Everything a person or Group holds here, own first-class. */
export function heldPermissions(
  own: readonly ProjectPermission[],
  inherited: InheritedAccess,
): ProjectPermission[] {
  return orderedPermissions([...own, ...inherited.keys()]);
}

/**
 * A set in one short label: the highest preset it holds and how many
 * permissions it has beyond that ("View +1"), or how many it has when it
 * holds no preset.
 */
export function accessLabel(held: readonly ProjectPermission[]): string {
  if (held.length === 0) return "No access";
  const level = containedLevel(held);
  if (!level) {
    return held.length === 1 ? "1 permission" : `${held.length} permissions`;
  }
  const extra = held.length - level.permissions.length;
  return extra > 0 ? `${level.shortLabel} +${extra}` : level.shortLabel;
}

/**
 * The accessible name of a row's access: "Mia Member: View +1". The Group
 * mark after the label is hidden from screen readers, so the name says it
 * instead: "Mia Member: Edit and run from Finance" when it all comes from
 * Groups, "…, partly from Finance" beside access of their own.
 */
export function accessName(
  subjectName: string,
  own: readonly ProjectPermission[],
  inherited: InheritedAccess,
): string {
  const name = `${subjectName}: ${accessLabel(heldPermissions(own, inherited))}`;
  const groups = borrowedGroups(own, inherited);
  if (groups.length === 0) return name;
  const from =
    groups.length > 1 ? `${groups[0]} +${groups.length - 1}` : groups[0];
  return own.length === 0
    ? `${name} from ${from}`
    : `${name}, partly from ${from}`;
}

/** The Groups that give what someone holds beyond their own, by name. */
function borrowedGroups(
  own: readonly ProjectPermission[],
  inherited: InheritedAccess,
): string[] {
  const names = new Set<string>();
  for (const [permission, source] of inherited) {
    if (own.includes(permission) || source.via !== "group") continue;
    for (const name of source.names) names.add(name);
  }
  return [...names];
}

/**
 * Where the part of a set that isn't their own comes from: a Group, the
 * Company floor (everyone), or nowhere. A Group wins, being the narrower
 * source to point at.
 */
function borrowedFrom(
  own: readonly ProjectPermission[],
  inherited: InheritedAccess,
): "group" | "everyone" | null {
  let everyone = false;
  for (const [permission, source] of inherited) {
    if (own.includes(permission)) continue;
    if (source.via === "group") return "group";
    everyone = true;
  }
  return everyone ? "everyone" : null;
}

// What someone can do, in one short label: the icon and name of the
// highest preset they hold, "+N" for what they hold beyond it, and one
// mark when part of it comes from a Group (or from everyone). The picker
// is the only place that lists the permissions.
export function AccessLabel({
  inherited,
  own,
}: {
  inherited: InheritedAccess;
  own: readonly ProjectPermission[];
}) {
  const held = heldPermissions(own, inherited);
  const level = containedLevel(held);
  const extra = level ? held.length - level.permissions.length : 0;
  const Icon = level ? PROJECT_ACCESS_LEVEL_ICONS[level.id] : null;
  const borrowed = borrowedFrom(own, inherited);
  return (
    <span
      className="inline-flex min-w-0 items-center gap-[5px]"
      aria-hidden="true"
    >
      {Icon ? <Icon className="project-access-perms__icon" size={14} /> : null}
      <span
        className="project-access-perms__label"
        data-empty={held.length === 0 ? "" : undefined}
      >
        {level ? level.shortLabel : accessLabel(held)}
      </span>
      {extra > 0 ? (
        <span className="project-access-perms__more">+{extra}</span>
      ) : null}
      {borrowed === "group" ? (
        <UsersRound className="flex-none text-workspace-muted" size={13} />
      ) : borrowed === "everyone" ? (
        <Building2 className="flex-none text-workspace-muted" size={13} />
      ) : null}
    </span>
  );
}

// The label without the button, where it can't be changed: the same pill
// without a chevron, and a short accessible name ("Payroll: View +1").
export function StaticPermissions({
  inherited,
  own,
  subjectName,
}: {
  inherited: InheritedAccess;
  own: readonly ProjectPermission[];
  subjectName: string;
}) {
  return (
    <span
      aria-label={accessName(subjectName, own, inherited)}
      className="project-access-perms"
      data-static=""
      role="img"
    >
      <AccessLabel inherited={inherited} own={own} />
    </span>
  );
}
