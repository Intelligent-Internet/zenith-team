import type { RunDto, TaskDto } from "@/shared/api/types";
import { hasProjectPermission } from "@/shared/permissions";

// Who runs a Task, and where (ADR 0014). A Task belongs to one member, who
// runs it on their own computer; the work is done by an Agent they have set
// up there, or by the member themselves. Nobody else can start it, and the
// board never names another member's machine — only whether the member whose
// Task it is can start it right now.

export type TaskRunAction =
  | { kind: "run"; label: string }
  | { kind: "retry"; label: string }
  | { kind: "set_up"; label: string; agentName: string }
  | { kind: "not_yours"; label: string; assigneeName: string }
  | { kind: "unavailable"; label: string; reason: string };

// taskRunAction is the primary button on a Task, for one viewer. When the
// Task is someone else's there is no button, and the label is the line
// that says who can start it and where.
export function taskRunAction(
  task: Pick<TaskDto, "assignee" | "executedBy" | "execution" | "status">,
  viewerUserId: string | undefined,
): TaskRunAction {
  const mine = Boolean(viewerUserId) && task.assignee.id === viewerUserId;
  if (!mine) {
    return {
      kind: "not_yours",
      label: onlyAssigneeStartsLabel(task),
      assigneeName: task.assignee.name,
    };
  }
  const agent = task.execution.agent;
  if (task.execution.unreadyReason === "not_bound" && agent) {
    return {
      kind: "set_up",
      label: `Set ${agent.name} up on my computer`,
      agentName: agent.name,
    };
  }
  if (!task.execution.assigneeReady) {
    return {
      kind: "unavailable",
      label: "Can't run yet",
      reason: unreadyReasonLabel(task, viewerUserId),
    };
  }
  return task.status === "error"
    ? { kind: "retry", label: "Retry on my computer" }
    : { kind: "run", label: "Run task" };
}

export type TaskStartAction = Extract<
  TaskRunAction,
  { kind: "run" | "retry" | "set_up" }
>;

// The button that starts a Run, when this viewer has one: the Task is
// theirs, an Agent executes or assists it, and it is waiting to start or its
// last Run hit a technical error. Null otherwise.
export function taskStartAction(
  task: Pick<TaskDto, "assignee" | "executedBy" | "execution" | "status">,
  viewerUserId: string | undefined,
): TaskStartAction | null {
  if (
    task.status !== "backlog" &&
    task.status !== "todo" &&
    task.status !== "error"
  ) {
    return null;
  }
  const action = taskRunAction(task, viewerUserId);
  return action.kind === "run" ||
    action.kind === "retry" ||
    action.kind === "set_up"
    ? action
    : null;
}

// Task actions need a note only when this viewer cannot start the work.
export function taskStartNotes(
  task: Pick<TaskDto, "assignee" | "executedBy" | "execution" | "status">,
  viewerUserId: string | undefined,
): string[] {
  if (!task.execution.assigneeReady) {
    return [unreadyReasonLabel(task, viewerUserId)];
  }
  return task.assignee.id !== viewerUserId
    ? [onlyAssigneeStartsLabel(task)]
    : [];
}

// Who starts someone else's Task: only its assignee, on their own computer
// (ADR 0014). One short line, like `unreadyReasonLabel`.
function onlyAssigneeStartsLabel(task: Pick<TaskDto, "assignee">): string {
  return `Only ${firstName(task.assignee.name)} can start this`;
}

// What the board shows in the "executed by" column.
export function taskExecutedByLabel(
  task: Pick<TaskDto, "assignee" | "executedBy">,
): string {
  return task.executedBy.kind === "agent"
    ? task.executedBy.name
    : assigneeDoesItLabel(task.assignee.name);
}

// The executor that is no Agent: the assignee, doing the work themself.
export function assigneeDoesItLabel(assigneeName: string): string {
  return `${assigneeName} (does it themself)`;
}

// The failure reasons whose server sentence ends with a step only the person
// at the computer that ran the Run can take: sign in there, start the
// daemon, fix its network, install or update the CLI. The copy is
// server/pkg/runfailure/message.go. Every other reason reads the same for
// everyone — a provider outage, a rate limit, a stopped run, a model setting
// on the Agent, or a request to narrow — so its sentence is left alone.
const MACHINE_LOCAL_FAILURE_REASONS = new Set([
  "queued_expired",
  "runtime_offline",
  "skill_bundle_unavailable",
  "run_context_invalid",
  "agent_error.provider_auth_or_access",
  "agent_error.provider_network",
  "agent_error.missing_config",
  "agent_error.runtime_version_unsupported",
  "agent_error.runtime_missing_executable",
]);

// A failed Run's sentence, as one viewer should read it. The server writes
// "what happened, then who can act" (server/pkg/runfailure). When the fix is
// on the computer that ran it, the second half speaks to the person sitting
// there. That is the viewer only when the Run provably ran on a computer of
// theirs: a Run read names its `runtimeId` to the runtime's registrant alone
// (ADR 0014). The Task's current assignee proves nothing — a handoff keeps
// the earlier Runs, which ran on the previous assignee's computer. So anyone
// else keeps what happened and reads that the fix is on the computer that
// ran it, instead of steps meant for somebody else's machine. The person at
// that computer, and every other reason, get the server's sentence
// unchanged.
export function runFailureMessageForViewer(
  failure: { message: string; reason: string | null | undefined },
  ranOnViewerComputer: boolean,
): string {
  if (
    ranOnViewerComputer ||
    !failure.reason ||
    !MACHINE_LOCAL_FAILURE_REASONS.has(failure.reason)
  ) {
    return failure.message;
  }
  const trimmed = failure.message.trim();
  const end = trimmed.indexOf(". ");
  const first = end === -1 ? trimmed : trimmed.slice(0, end + 1);
  const whatHappened = /[.!?]$/.test(first) ? first : `${first}.`;
  return `${whatHappened} Only someone at the computer that ran it can fix this.`;
}

// Marking a Task done by hand is for work a person does: the assignee, or
// somebody who may reassign it, closes it when it is finished.
export function taskCanMarkDone(
  task: Pick<TaskDto, "assignee" | "executedBy" | "status">,
  viewerUserId: string | undefined,
  canAssign: boolean,
): boolean {
  if (
    task.executedBy.kind !== "user" ||
    !["backlog", "todo", "blocked", "error", "in_review"].includes(task.status)
  ) {
    return false;
  }
  return canAssign || task.assignee.id === viewerUserId;
}

// Whether this viewer decides this Task's review: an Agent's Task in review
// that the viewer may take out of review, the server's rule — its assignee
// or a tasks.assign holder, with "Create and edit tasks" (mark done) or
// "Run tasks with agents" (merge) on its Project (ADR 0014, ADR 0017). It
// gates the review actions. "Needs your review" also needs a result to
// review (`runLeftWorkToReview`); everyone else sees the plain status.
export function taskAwaitsViewerReview(
  task: Pick<TaskDto, "access" | "assignee" | "executedBy" | "status">,
  viewerUserId: string | undefined,
  canAssign: boolean,
): boolean {
  return (
    task.status === "in_review" &&
    task.executedBy.kind === "agent" &&
    (canAssign ||
      (Boolean(viewerUserId) && task.assignee.id === viewerUserId)) &&
    (hasProjectPermission(task.access, "project.tasks.write") ||
      hasProjectPermission(task.access, "project.tasks.execute"))
  );
}

// Whether a Task in review holds a result to review: its latest Run
// completed, so that Run's answer, changes and saved outputs wait for a
// decision. A Task moved into review by hand, with no Run or after a Run
// that failed or was stopped, holds none and shows its plain status.
export function runLeftWorkToReview(
  latestRun: Pick<RunDto, "status"> | null | undefined,
): boolean {
  return latestRun?.status === "completed";
}

// Why a Task can't start for someone without "Run tasks with agents" on its
// Project (ADR 0017), in the fewest words.
export function cannotRunTasksHereLabel(name?: string): string {
  return name ? `${name} can't run tasks here` : "You can't run tasks here";
}

// Whether the viewer can fix `assignee_cannot_run` themself: they manage the
// Project's access, through a grant or as an owner or admin (the Task's
// access carries the management names for them). They get a link to
// Project access beside the reason instead of being told to ask someone.
export function taskOffersAccessFix(
  task: Pick<TaskDto, "access" | "execution">,
): boolean {
  return (
    task.execution.unreadyReason === "assignee_cannot_run" &&
    hasProjectPermission(task.access, "project.grants.manage")
  );
}

// The short lines about a Task name a person once, by first name, so each
// fits one line at 390 px.
export function firstName(name: string): string {
  return name.trim().split(/\s+/)[0] || name;
}

// Why a Task's assignee cannot run it yet, for whoever is reading it: Next
// up, the Task panel and the Task chat show it as one short line, six words
// at most with a name counted as one. A fix for `assignee_cannot_run` is a
// link beside it for whoever manages the Project's access
// (`taskOffersAccessFix`), never a sentence.
export function unreadyReasonLabel(
  task: Pick<TaskDto, "assignee" | "executedBy" | "execution">,
  viewerUserId: string | undefined,
): string {
  const mine = task.assignee.id === viewerUserId;
  const assignee = firstName(task.assignee.name);
  const agent = task.execution.agent?.name ?? "the agent";
  switch (task.execution.unreadyReason) {
    case "not_bound":
      return mine
        ? `Set up ${agent} first`
        : `${assignee} hasn't set up ${agent}`;
    case "runtime_offline":
      return mine
        ? "Your computer is offline"
        : `${assignee}'s computer is offline`;
    case "runtime_unusable":
      return mine
        ? `Your computer can't run ${agent}`
        : `${assignee}'s computer can't run ${agent}`;
    case "runtime_signed_out":
      return mine
        ? "Sign in to your provider again"
        : `${assignee} needs to sign in again`;
    case "agent_archived":
      return `${task.execution.agent?.name ?? "The agent"} is archived`;
    case "assignee_inactive":
      return mine
        ? "Your membership isn't active"
        : `${assignee} is no longer a member`;
    case "assignee_cannot_run":
      return cannotRunTasksHereLabel(mine ? undefined : assignee);
    default:
      return "Can't start yet";
  }
}
