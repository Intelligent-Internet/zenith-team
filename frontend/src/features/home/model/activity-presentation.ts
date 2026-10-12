import { orderedPermissions } from "@/features/company-plan/projects/access/project-access-model";
import { accessLabel } from "@/features/company-plan/projects/access/project-access-permission-icons";
import type { ActivityEventSummary } from "@/shared/api/types";
import type { ProjectPermission } from "@/shared/permissions";
import { activityActorLabel } from "@/shared/lib/activity-actor";
import { activitySnapshotFieldLabel } from "@/shared/ui/activity-snapshot-details";
import {
  Bell,
  Bot,
  Building2,
  CalendarDays,
  CircleCheck,
  CircleHelp,
  FolderOpen,
  ListTodo,
  MessageSquareText,
  Monitor,
  NotebookText,
  PackageCheck,
  Play,
  ShieldCheck,
  User,
  UserPlus,
  UsersRound,
  type IconComponent,
} from "@/shared/ui/icons";

export const activitySubjects: Record<
  string,
  { label: string; plural: string; icon: IconComponent }
> = {
  task: { label: "Task", plural: "Tasks", icon: ListTodo },
  run: { label: "Run", plural: "Runs", icon: Play },
  conversation: {
    label: "Conversation",
    plural: "Conversations",
    icon: MessageSquareText,
  },
  message: { label: "Message", plural: "Messages", icon: MessageSquareText },
  project: { label: "Project", plural: "Projects", icon: FolderOpen },
  project_grant: {
    label: "Project access",
    plural: "Project access changes",
    icon: ShieldCheck,
  },
  agent: { label: "Agent", plural: "Agents", icon: Bot },
  group: { label: "Group", plural: "Groups", icon: UsersRound },
  invitation: { label: "Invitation", plural: "Invitations", icon: UserPlus },
  member: { label: "Member", plural: "Members", icon: User },
  approval: { label: "Approval", plural: "Approvals", icon: CircleCheck },
  question: { label: "Question", plural: "Questions", icon: CircleHelp },
  runtime: { label: "Runtime", plural: "Runtimes", icon: Monitor },
  resource: { label: "Resource", plural: "Resources", icon: FolderOpen },
  deliverable: { label: "Result", plural: "Results", icon: PackageCheck },
  company: { label: "Company", plural: "Company", icon: Building2 },
  user: { label: "Person", plural: "People", icon: User },
  inbox: { label: "Notification", plural: "Notifications", icon: Bell },
  calendar: { label: "Calendar", plural: "Calendar", icon: CalendarDays },
};

/**
 * Names the feed can put in a sentence: the Team's members, the Company's
 * Groups and the Projects the viewer can see, as they read now. Each
 * answers undefined for someone or something it does not know.
 */
export type ActivityNames = {
  person: (userId: string) => string | undefined;
  group: (groupId: string) => string | undefined;
  project: (projectId: string) => string | undefined;
};

export function activityPresentation(
  event: ActivityEventSummary,
  names?: ActivityNames,
) {
  const [domain, ...actionParts] = event.action.split(".");
  const actor = activityActorLabel({
    actor: event.actor,
    onBehalfOf: event.on_behalf_of,
  });
  const group = groupActivity(event, names);
  if (group) return { icon: UsersRound, actor, ...group };
  const own = selfGrantActivity(event, names);
  if (own) return { icon: ShieldCheck, actor, ...own };
  const subject = activitySubjects[event.entity.type] ??
    activitySubjects[domain] ?? {
      label: activityFieldLabel(event.entity.type),
      icon: NotebookText,
    };
  let action = actionParts.join(" ").replaceAll("_", " ");
  // Some event names already contain the subject, e.g. message.message_created.
  for (const prefix of [event.entity.type, domain]) {
    if (action.startsWith(`${prefix} `))
      action = action.slice(prefix.length + 1);
  }
  return {
    icon: subject.icon,
    title:
      event.action === "runtime.work_available"
        ? "Work available"
        : `${subject.label}${action ? ` ${action}` : ""}`,
    context:
      grantContext(event, names) ??
      (event.entity.title || event.entity.key || subject.label),
    actor,
  };
}

// What an access row carries (Access TECH, the trail). A Group row: the
// Group's id and the name it had then; who joined or left; on a
// project_grant row the Project, and either the Group's permissions before
// and after (group_changed) or everyone whose access there moved
// (group_member_added and _removed, one row per Project and change). Rows
// written before 2026-10-07 named one person (userId) instead. A person's
// own grant row (changed, joined): the Project, the person and what they
// held before and after; a visibility row has no person. Since 2026-10-08
// every project_grant row also carries the Project's title, and a Group's
// row on a Project the Group's name, as they were at the change, so the row
// still reads after the Group is deleted or the Project archived; older
// rows fall back to the names the feed knows now.
type AccessActivityPayload = {
  groupId?: string;
  name?: string;
  groupName?: string;
  userId?: string | null;
  userIds?: string[];
  count?: number;
  projectId?: string;
  projectTitle?: string;
  before?: string[];
  after?: string[];
};

function accessPayload(event: ActivityEventSummary): AccessActivityPayload {
  const details = event.data as { payload?: unknown };
  const payload = details.payload;
  return payload && typeof payload === "object"
    ? (payload as AccessActivityPayload)
    : {};
}

/** The row's Project: its title then, the feed's name now, or "a project". */
function accessProjectName(
  payload: AccessActivityPayload,
  names?: ActivityNames,
): string {
  return (
    payload.projectTitle ||
    (payload.projectId ? names?.project(payload.projectId) : undefined) ||
    "a project"
  );
}

/**
 * The second line of a person's own grant row, and of a visibility row:
 * whom it was for and on which Project, "Mia Member · Payroll", or the
 * Project alone (Access DESIGN 5). Null for every other row; Group rows
 * have their own sentence.
 */
function grantContext(
  event: ActivityEventSummary,
  names?: ActivityNames,
): string | null {
  if (!event.action.startsWith("project_grant.")) return null;
  const payload = accessPayload(event);
  const projectName = accessProjectName(payload, names);
  if (!payload.userId) return projectName;
  const personName = names?.person(payload.userId) ?? "Someone";
  return `${personName} · ${projectName}`;
}

/**
 * Whether the row's person changed their own access: a person acting
 * (not an Agent for someone) on exactly themself.
 */
function actsOnSelf(
  event: ActivityEventSummary,
  userIds: readonly string[],
): boolean {
  return (
    event.actor.kind === "user" &&
    !event.on_behalf_of &&
    Boolean(event.actor.id) &&
    userIds.length === 1 &&
    userIds[0] === event.actor.id
  );
}

/**
 * A person's own grant row about themself (Access DESIGN 5 and 23): "Joined
 * project" for a Join or giving themself access, "Left project" for taking
 * their own away, "Changed own access" for anything else, over the Project.
 * The By column names them, so neither line does. Null for every other row,
 * which keeps "Project access changed" over "{Person} · {Project}".
 */
function selfGrantActivity(
  event: ActivityEventSummary,
  names?: ActivityNames,
): { title: string; context: string } | null {
  if (
    event.action !== "project_grant.joined" &&
    event.action !== "project_grant.changed"
  ) {
    return null;
  }
  const payload = accessPayload(event);
  if (!payload.userId || !actsOnSelf(event, [payload.userId])) return null;
  const context = accessProjectName(payload, names);
  const before = payload.before ?? [];
  const after = payload.after ?? [];
  if (event.action === "project_grant.joined" || before.length === 0) {
    return { title: "Joined project", context };
  }
  if (after.length === 0) return { title: "Left project", context };
  return { title: "Changed own access", context };
}

/**
 * A set as the access list labels it: the highest preset it holds and how
 * many more ("View", "Edit and run +1"), or how many permissions it holds.
 * Permissions no longer in the catalog are left out.
 */
function levelLabel(permissions: readonly string[] | undefined): string {
  return accessLabel(knownPermissions(permissions));
}

function knownPermissions(
  permissions: readonly string[] | undefined,
): ProjectPermission[] {
  return orderedPermissions((permissions ?? []) as ProjectPermission[]);
}

/** Everyone a Group's row on a Project names, one id or several. */
function movedUserIds(payload: AccessActivityPayload): string[] {
  return payload.userIds ?? (payload.userId ? [payload.userId] : []);
}

/**
 * Who a Project's group_member_added or _removed row names: one person by
 * name ("someone" when the roster does not know them), several as "3
 * people" (the row's details list them).
 */
function movedPeopleLabel(
  payload: AccessActivityPayload,
  names?: ActivityNames,
): string {
  const userIds = movedUserIds(payload);
  const count = payload.count ?? userIds.length;
  if (count === 1 && userIds.length === 1)
    return names?.person(userIds[0]) ?? "someone";
  return `${count} people`;
}

/**
 * A Group row (Access DESIGN 5 and 23). The By column names who did it, so
 * the title never does, and the second line holds the rest: a group row
 * reads "Added Mia Member" over the Group, a Group's row on a Project "Gave
 * Finance View" or "Added 3 people to Finance" over the Project. Someone who
 * adds or takes out only themself "Joined" or "Left". Levels read as the
 * access list labels them. Null for every other row.
 */
export function groupActivity(
  event: ActivityEventSummary,
  names?: ActivityNames,
): { title: string; context: string } | null {
  const isGroupRow = event.action.startsWith("group.");
  const isGroupGrantRow = event.action.startsWith("project_grant.group_");
  if (!isGroupRow && !isGroupGrantRow) return null;
  const payload = accessPayload(event);
  const groupName =
    (isGroupRow ? payload.name : payload.groupName) ||
    (payload.groupId ? names?.group(payload.groupId) : undefined) ||
    "a group";
  // Someone the roster does not name (gone, or not loaded yet) is never
  // called gone before we know.
  const personName =
    (payload.userId ? names?.person(payload.userId) : undefined) ?? "someone";
  const after = payload.after ?? [];
  const before = payload.before ?? [];
  // Someone who added or took out only themself joined or left.
  const self = actsOnSelf(event, movedUserIds(payload));
  const title = (() => {
    switch (event.action) {
      case "group.created":
        return "Created group";
      case "group.updated":
        return "Updated group";
      case "group.deleted":
        return "Deleted group";
      case "group.member_added":
        return self ? "Joined group" : `Added ${personName}`;
      case "group.member_removed":
        return self ? "Left group" : `Removed ${personName}`;
      case "project_grant.group_changed":
        if (after.length === 0) return `Removed ${groupName}`;
        if (before.length === 0)
          return `Gave ${groupName} ${levelLabel(after)}`;
        return `Changed ${groupName} to ${levelLabel(after)}`;
      case "project_grant.group_member_added":
        return self
          ? `Joined ${groupName}`
          : `Added ${movedPeopleLabel(payload, names)} to ${groupName}`;
      case "project_grant.group_member_removed":
        return self
          ? `Left ${groupName}`
          : `Removed ${movedPeopleLabel(payload, names)} from ${groupName}`;
      default:
        return null;
    }
  })();
  if (!title) return null;
  return {
    title,
    context: isGroupRow ? groupName : accessProjectName(payload, names),
  };
}

/**
 * What an access row's details show (Access DESIGN 23): the permissions a
 * grant added and took away; a visibility change's two settings; the people
 * a Group's row counts ("3 people"). Null for a row that is not about
 * access, and for an access row with nothing to show beyond its title.
 */
export type AccessActivityChange =
  | {
      kind: "permissions";
      added: ProjectPermission[];
      removed: ProjectPermission[];
    }
  | { kind: "visibility"; before: string; after: string }
  | { kind: "people"; userIds: string[] };

export function accessActivityChange(
  event: ActivityEventSummary,
): AccessActivityChange | null {
  const payload = accessPayload(event);
  switch (event.action) {
    case "project_grant.changed":
    case "project_grant.joined":
    case "project_grant.group_changed": {
      const before = knownPermissions(payload.before);
      const after = knownPermissions(payload.after);
      const added = after.filter((permission) => !before.includes(permission));
      const removed = before.filter(
        (permission) => !after.includes(permission),
      );
      return added.length > 0 || removed.length > 0
        ? { kind: "permissions", added, removed }
        : null;
    }
    case "project_grant.visibility_changed": {
      const before = payload.before?.[0];
      const after = payload.after?.[0];
      return before && after ? { kind: "visibility", before, after } : null;
    }
    case "project_grant.group_member_added":
    case "project_grant.group_member_removed": {
      // One person is named in the title already.
      const userIds = movedUserIds(payload);
      return userIds.length > 1 ? { kind: "people", userIds } : null;
    }
    default:
      return null;
  }
}

/** Whether a row is about access: a Group, or who holds what on a Project. */
export function isAccessActivity(event: ActivityEventSummary): boolean {
  return (
    event.action.startsWith("group.") ||
    event.action.startsWith("project_grant.")
  );
}

// The same plain words the snapshot details use (an old assigneeAgentId
// reads "executed by", ADR 0014), capitalised for a label.
export function activityFieldLabel(value: string) {
  const words = activitySnapshotFieldLabel(value).replaceAll("_", " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

// Snapshot fields that hold a member or Agent id (assigneeUserId,
// executedByAgentId, an old assigneeAgentId, the creator ids, a Run's
// agentId) name the actor instead of printing the id.
export function activityFieldActorKind(field: string) {
  if (/(^u|U)serId$/.test(field)) return "user";
  if (/(^a|A)gentId$/.test(field)) return "agent";
  return undefined;
}

export const activityPeriods = [
  { value: "all", label: "All time" },
  { value: "today", label: "Today" },
  { value: "week", label: "Last 7 days" },
  { value: "month", label: "Last 30 days" },
] as const;

export type ActivityPeriod = (typeof activityPeriods)[number]["value"];

export function activityPeriodBounds(period: ActivityPeriod) {
  if (period === "all") return {};
  const from = new Date();
  from.setHours(0, 0, 0, 0);
  const to = new Date(from);
  to.setDate(to.getDate() + 1);
  if (period === "week") from.setDate(from.getDate() - 6);
  if (period === "month") from.setDate(from.getDate() - 29);
  return { from: from.toISOString(), to: to.toISOString() };
}

export function formatActivityDate(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Unknown date";
  return date.toLocaleDateString(undefined, {
    weekday: "long",
    day: "numeric",
    month: "short",
    year: "numeric",
  });
}
