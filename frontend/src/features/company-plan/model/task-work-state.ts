import type { TaskDto, TaskStatusDto } from "@/shared/api/types";

type TaskWorkState = Pick<TaskDto, "status" | "dependenciesResolved">;

export type TaskChatActivity = {
  /** Requests the viewer can see waiting in the chat. */
  hasPendingApproval?: boolean;
  hasPendingQuestion?: boolean;
  /**
   * Whether the viewer may answer them (ADR 0017): `project.approvals.resolve`
   * on the Task's Project, the server's decide view.
   */
  canRespond?: boolean;
  running?: boolean;
};

// Two signals, kept apart. Whether the work waits on a request (the status,
// `live`) reads what the viewer can see; whether it waits on the viewer
// (the "your input" label, `needsViewer`) only what they may answer. A
// reader who may not answer sees the plain status.
export function taskChatState(task: TaskWorkState, activity: TaskChatActivity) {
  const unfinished = task.status !== "done" && task.status !== "cancelled";
  const waitingForInput =
    (task.status === "in_progress" || task.status === "in_review") &&
    Boolean(activity.hasPendingQuestion || activity.hasPendingApproval);
  const needsViewer = waitingForInput && Boolean(activity.canRespond);
  const status =
    task.status === "in_review" && waitingForInput
      ? "in_progress"
      : task.status;
  // A completed Run remains operational until a person finishes reviewing its
  // deliverables, so in-review task chats stay visible beside active Runs.
  const live =
    task.status === "in_review" ||
    (unfinished && Boolean(activity.running || waitingForInput));
  const label =
    needsViewer && activity.hasPendingQuestion
      ? "Needs your input"
      : needsViewer && activity.hasPendingApproval
        ? "Awaiting approval"
        : unfinished && activity.running
          ? "Running"
          : status === "in_review"
            ? "Ready for review"
            : taskWorkStatusLabel({ ...task, status });
  return { label, live, needsViewer, status };
}

// The plain status, the same for everyone. "Needs review" is only for the
// person the review waits on (`taskWorkStatusLabel`).
export const taskStatusLabels: Record<TaskStatusDto, string> = {
  backlog: "Backlog",
  todo: "To do",
  in_progress: "In progress",
  in_review: "In review",
  done: "Done",
  blocked: "Blocked",
  failed: "Failed",
  error: "Technical error",
  cancelled: "Cancelled",
};

export function taskWaitsForPrerequisites(task: TaskWorkState) {
  return (
    !task.dependenciesResolved &&
    ["backlog", "todo", "blocked"].includes(task.status)
  );
}

export function taskNeedsAttention(task: TaskWorkState) {
  return (
    task.status === "failed" ||
    task.status === "error" ||
    task.status === "in_review" ||
    (task.status === "blocked" && !taskWaitsForPrerequisites(task))
  );
}

export function taskIsReady(task: TaskWorkState) {
  return task.status === "todo" && task.dependenciesResolved;
}

// A Task in review reads "Needs review" only to the person it waits on:
// `awaitsViewerReview` is the "Needs your review" rule
// (`useTaskAwaitsViewerReview`, `useProjectTaskReviews`). Everyone else, and
// any screen that does not know, reads "In review".
export function taskWorkStatusLabel(
  task: TaskWorkState,
  awaitsViewerReview = false,
) {
  if (taskWaitsForPrerequisites(task)) return "Waiting for prerequisites";
  return task.status === "in_review" && awaitsViewerReview
    ? "Needs review"
    : taskStatusLabels[task.status];
}

export function compareTaskReadiness(left: TaskDto, right: TaskDto) {
  return (
    taskReadinessOrder(left) - taskReadinessOrder(right) ||
    left.number - right.number ||
    left.id.localeCompare(right.id)
  );
}

function taskReadinessOrder(task: TaskWorkState) {
  if (taskNeedsAttention(task)) return 0;
  if (task.status === "in_progress") return 1;
  if (taskIsReady(task)) return 2;
  if (task.status === "backlog" && task.dependenciesResolved) return 3;
  if (taskWaitsForPrerequisites(task)) return 4;
  if (task.status === "done") return 5;
  return 6;
}
