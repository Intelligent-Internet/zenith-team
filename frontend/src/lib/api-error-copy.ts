// Plain words for refusals whose server `detail` is generic: the ones
// Project access (ADR 0017) added, and an archive the Project's open work
// blocks. The server's `detail` stays the source of truth wherever it says
// something specific, such as a Run start that names the Agent ("Eli didn't
// start: you can't run tasks here."). Task and run screens get a plain sentence for
// the generic ones; the access screens get a few words (`accessErrorCopy`).

const GENERIC_PROJECT_PERMISSION_DETAIL =
  "Your access to this Project does not allow this action";

/** Removing someone from a Project that is open to the whole Company. */
export const PROJECT_VISIBILITY_FLOOR_COPY = "Limit access first";

/**
 * The self-grant cap: a level, a switch or a save beyond what the viewer
 * holds on the Project. The Access page greys the same things with it.
 */
export const PROJECT_GRANT_EXCEEDS_OWN_COPY = "Beyond your access";

/**
 * A Group save that lost a race (a stale revision), or a name another Group
 * already has: the server answers both with `group_conflict`.
 */
export const GROUP_CONFLICT_COPY = "Name taken or group changed";

/** Someone else changed the Project's access first. */
export const ACCESS_CHANGED_COPY = "Access changed. Try again.";

/** Only active Company members can hold access or be in a Group. */
export const NOT_ACTIVE_MEMBER_COPY = "Not an active member";

/**
 * Archive refused: the Project still has open Tasks, a running Task Run or
 * a merge that has not finished (`project_open_tasks`).
 */
const PROJECT_OPEN_TASKS_COPY =
  "Finish or cancel this project's open tasks and running work first.";

/** A decision nobody who reads the Project may take stopped a run. */
export const NO_ELIGIBLE_RESOLVER_COPY =
  "Nobody who can see this project may decide what the agent asked, so the run stopped. Give someone the permission, then reply in the task chat.";

const PLAIN_COPY: Record<string, (detail: string | null) => string> = {
  project_permission_required: (detail) =>
    detail && !detail.startsWith(GENERIC_PROJECT_PERMISSION_DETAIL)
      ? detail
      : "You can't do this here",
  project_member_required: (detail) =>
    detail?.toLowerCase().includes("active company member")
      ? "Only active members of the company can be given access."
      : "This person can't see this project. Give them access first, or pick someone who can.",
  project_grant_revision_conflict: () => ACCESS_CHANGED_COPY,
  project_grant_exceeds_own: () => PROJECT_GRANT_EXCEEDS_OWN_COPY,
  project_visibility_floor: () => PROJECT_VISIBILITY_FLOOR_COPY,
  project_open_tasks: () => PROJECT_OPEN_TASKS_COPY,
  no_eligible_resolver: () => NO_ELIGIBLE_RESOLVER_COPY,
  task_outside_run_scope: () =>
    "This run can only work on its own task. Use the task chat or a direct message instead.",
  // Groups: short labels, since only the access
  // screens show them.
  group_not_found: () => "Group no longer exists",
  group_conflict: () => GROUP_CONFLICT_COPY,
  group_member_required: () => NOT_ACTIVE_MEMBER_COPY,
  group_limit_reached: () => "Group limit reached",
  group_grant_permission_required: () => "Ask an owner or admin",
};

/** The message a person sees for an API refusal, or null to keep the detail. */
export function plainApiErrorCopy(
  code: string | null | undefined,
  detail: string | null | undefined,
): string | null {
  const copy = code ? PLAIN_COPY[code] : undefined;
  return copy ? copy(detail?.trim() || null) : null;
}

// The access screens (Team › Project access and the Group dialogs) say at
// most a few words when something fails. Most codes already have short
// labels above; these three, which task and run screens share, keep their
// longer wording there and get a short one here.
const ACCESS_OVERRIDES: Record<string, string> = {
  project_permission_required: "You can't change access",
  project_member_required: NOT_ACTIVE_MEMBER_COPY,
  project_conflict: "Project changed. Try again.",
};

// The codes an access screen may show with PLAIN_COPY's own short label.
const ACCESS_SHARED_CODES = new Set([
  "project_grant_revision_conflict",
  "project_grant_exceeds_own",
  "project_visibility_floor",
  "group_not_found",
  "group_conflict",
  "group_member_required",
  "group_limit_reached",
  "group_grant_permission_required",
]);

/** The short words an access screen shows for a refusal, or null. */
export function accessErrorCopy(
  code: string | null | undefined,
): string | null {
  if (!code) return null;
  return (
    ACCESS_OVERRIDES[code] ??
    (ACCESS_SHARED_CODES.has(code) ? plainApiErrorCopy(code, null) : null)
  );
}
