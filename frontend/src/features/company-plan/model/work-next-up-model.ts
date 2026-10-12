import type {
  RunDto,
  RunStatusDto,
  TaskDependencyGraphDto,
  TaskDto,
  TaskStatusDto,
} from "@/shared/api/types";
import { unreadyReasonLabel } from "@/features/company-plan/model/task-execution";

export type WorkNextUpCategory =
  "ready" | "attention" | "running" | "waiting" | "completed" | "cancelled";

export type WorkNextUpAction =
  "start" | "retry" | "review" | "founder" | "inspect" | "none";

export type WorkNextUpRelatedTask = {
  id: string;
  key: string;
  title: string;
  status: TaskStatusDto | null;
  task: TaskDto | undefined;
};

export type WorkNextUpDependentTask = WorkNextUpRelatedTask & {
  dependencyDataLoaded: boolean;
  // These are current prerequisites; the selected task is excluded from the
  // forecast count, without duplicating this array for every incoming edge.
  remainingPrerequisites: WorkNextUpRelatedTask[];
  remainingPrerequisiteCount: number | null;
  prerequisitesCompleteAfterCompletion: boolean;
  readyAfterCompletion: boolean;
  reason: string;
};

export type WorkNextUpItem = {
  task: TaskDto;
  category: WorkNextUpCategory;
  action: WorkNextUpAction;
  actionLabel: string;
  reason: string;
  // The inspector's status chip in Next up, from the same disposition that
  // files the Task: "Ready to start", "Can't start yet", or none.
  readiness: WorkNextUpReadiness | null;
  runStatus: RunStatusDto | null;
  prerequisites: WorkNextUpRelatedTask[];
  dependents: WorkNextUpDependentTask[];
  dependencyDataLoaded: boolean;
  remainingPrerequisiteCount: number | null;
  readyAfterCompletionCount: number;
  recommendationReason: string | null;
};

// A Task that can't start yet is drawn like one waiting for prerequisites,
// never like one ready to start.
export type WorkNextUpReadiness = { label: string; waiting: boolean };

export type WorkNextUpModel = {
  items: WorkNextUpItem[];
  byTaskId: Map<string, WorkNextUpItem>;
  ready: WorkNextUpItem[];
  attention: WorkNextUpItem[];
  running: WorkNextUpItem[];
  waiting: WorkNextUpItem[];
  completed: WorkNextUpItem[];
  cancelled: WorkNextUpItem[];
};

export type WorkCompletionUpdate = {
  completedTaskIds: string[];
  newlyReadyTaskIds: string[];
  affectedWaitingTaskIds: string[];
};

type WorkNextUpInput = {
  // Build from the company task set before applying display filters.
  tasks: readonly TaskDto[];
  dependencyGraph?: TaskDependencyGraphDto;
  activeRuns?: readonly Pick<RunDto, "taskId" | "status">[];
  // Tasks with a pending question or approval the viewer can see. The work
  // waits on it, so none of them is offered to start.
  pendingRequestTaskIds?: ReadonlySet<string>;
  // Those the viewer may answer (the decide view, ADR 0017). Only these ask
  // for their response; for the rest the Task just waits.
  respondTaskIds?: ReadonlySet<string>;
  // Who is reading: a Task only its assignee can start reads differently
  // for them (ADR 0014).
  viewerUserId?: string;
};

type TaskRequests = {
  pending: ReadonlySet<string>;
  respond: ReadonlySet<string>;
};

type TaskDisposition = Pick<
  WorkNextUpItem,
  "category" | "action" | "actionLabel" | "reason" | "readiness" | "runStatus"
>;

type DependencyContext = {
  tasksById: Map<string, TaskDto>;
  relatedTasksById: Map<string, WorkNextUpRelatedTask>;
  prerequisitesByTaskId: Map<string, WorkNextUpRelatedTask[]>;
  dependentsByTaskId: Map<string, string[]>;
  unresolvedByTaskId: Map<string, WorkNextUpRelatedTask[]>;
  completeTaskIds: Set<string>;
};

export function createWorkNextUpModel({
  tasks,
  dependencyGraph,
  activeRuns = [],
  pendingRequestTaskIds = new Set(),
  respondTaskIds = new Set(),
  viewerUserId,
}: WorkNextUpInput): WorkNextUpModel {
  const requests: TaskRequests = {
    pending: pendingRequestTaskIds,
    respond: respondTaskIds,
  };
  const dependencies = createDependencyContext(tasks, dependencyGraph);
  const activeRunStatuses = new Map<string, RunStatusDto>();
  for (const run of activeRuns) {
    if (run.taskId && !isTerminalRunStatus(run.status)) {
      const current = activeRunStatuses.get(run.taskId);
      if (!current || run.status === "running") {
        activeRunStatuses.set(run.taskId, run.status);
      }
    }
  }

  const items = [...dependencies.tasksById.values()].map((task) => {
    const runStatus = activeRunStatuses.get(task.id) ?? null;
    const disposition = taskDisposition(
      task,
      runStatus,
      requests,
      viewerUserId,
    );
    const dependents = (dependencies.dependentsByTaskId.get(task.id) ?? []).map(
      (dependentId) =>
        forecastDependent({
          selectedTask: task,
          dependentId,
          dependencies,
          activeRunStatuses,
          requests,
          viewerUserId,
        }),
    );
    const readyAfterCompletionCount = dependents.filter(
      (dependent) => dependent.readyAfterCompletion,
    ).length;
    const dependencyDataLoaded = dependencies.completeTaskIds.has(task.id);

    return {
      task,
      ...disposition,
      prerequisites: dependencies.prerequisitesByTaskId.get(task.id) ?? [],
      dependents,
      dependencyDataLoaded,
      remainingPrerequisiteCount: task.dependenciesResolved
        ? 0
        : dependencyDataLoaded
          ? (dependencies.unresolvedByTaskId.get(task.id)?.length ?? 0)
          : null,
      readyAfterCompletionCount,
      recommendationReason:
        disposition.category === "ready"
          ? recommendationReason(task, readyAfterCompletionCount)
          : null,
    };
  });
  items.sort(compareNextUpItems);

  const model: WorkNextUpModel = {
    items,
    byTaskId: new Map(),
    ready: [],
    attention: [],
    running: [],
    waiting: [],
    completed: [],
    cancelled: [],
  };
  for (const item of items) {
    model.byTaskId.set(item.task.id, item);
    model[item.category].push(item);
  }
  return model;
}

export function createWorkCompletionUpdate(
  previous: WorkNextUpModel,
  current: WorkNextUpModel,
  completionTaskIds?: readonly string[],
): WorkCompletionUpdate | null {
  const completionIds = completionTaskIds && new Set(completionTaskIds);
  const completedTaskIds = current.completed
    .filter((item) => {
      const previousTask = previous.byTaskId.get(item.task.id)?.task;
      return (
        previousTask &&
        previousTask.status !== "done" &&
        (!completionIds || completionIds.has(item.task.id))
      );
    })
    .map((item) => item.task.id);
  if (completedTaskIds.length === 0) return null;

  const completedIds = new Set(completedTaskIds);
  const newlyReadyTaskIds: string[] = [];
  const affectedWaitingTaskIds: string[] = [];
  for (const item of current.items) {
    if (!item.prerequisites.some((task) => completedIds.has(task.id))) {
      continue;
    }
    const previousItem = previous.byTaskId.get(item.task.id);
    if (
      previousItem &&
      !previousItem.task.dependenciesResolved &&
      item.task.dependenciesResolved &&
      !isActionable(previousItem) &&
      isActionable(item)
    ) {
      newlyReadyTaskIds.push(item.task.id);
    } else if (item.category === "waiting") {
      affectedWaitingTaskIds.push(item.task.id);
    }
  }

  return { completedTaskIds, newlyReadyTaskIds, affectedWaitingTaskIds };
}

function createDependencyContext(
  tasks: readonly TaskDto[],
  graph: TaskDependencyGraphDto | undefined,
): DependencyContext {
  const tasksById = new Map(tasks.map((task) => [task.id, task]));
  const relatedTasksById = new Map<string, WorkNextUpRelatedTask>();
  for (const task of graph?.tasks ?? []) {
    relatedTasksById.set(task.id, { ...task, task: undefined });
  }
  for (const task of tasksById.values()) {
    relatedTasksById.set(task.id, {
      id: task.id,
      key: task.key,
      title: task.title,
      status: task.status,
      task,
    });
  }

  const prerequisitesByTaskId = new Map<string, WorkNextUpRelatedTask[]>();
  const dependentsByTaskId = new Map<string, string[]>();
  const seenEdges = new Set<string>();
  for (const edge of graph?.items ?? []) {
    const edgeKey = `${edge.dependentTaskId}:${edge.prerequisiteTaskId}`;
    if (seenEdges.has(edgeKey)) continue;
    seenEdges.add(edgeKey);
    const prerequisite = relatedTasksById.get(edge.prerequisiteTaskId) ?? {
      id: edge.prerequisiteTaskId,
      key: "Task",
      title: "Task details unavailable",
      status: null,
      task: undefined,
    };
    const prerequisites = prerequisitesByTaskId.get(edge.dependentTaskId) ?? [];
    prerequisites.push(prerequisite);
    prerequisitesByTaskId.set(edge.dependentTaskId, prerequisites);
    const dependents = dependentsByTaskId.get(edge.prerequisiteTaskId) ?? [];
    dependents.push(edge.dependentTaskId);
    dependentsByTaskId.set(edge.prerequisiteTaskId, dependents);
  }

  const unresolvedByTaskId = new Map<string, WorkNextUpRelatedTask[]>();
  for (const [taskId, prerequisites] of prerequisitesByTaskId) {
    unresolvedByTaskId.set(
      taskId,
      prerequisites.filter((task) => task.status !== "done"),
    );
  }
  const completeTaskIds = new Set<string>();
  for (const task of tasksById.values()) {
    const prerequisites = prerequisitesByTaskId.get(task.id) ?? [];
    if (
      graph &&
      prerequisites.length === task.blockedByCount &&
      prerequisites.every((prerequisite) => prerequisite.status !== null)
    ) {
      completeTaskIds.add(task.id);
    }
  }

  return {
    tasksById,
    relatedTasksById,
    prerequisitesByTaskId,
    dependentsByTaskId,
    unresolvedByTaskId,
    completeTaskIds,
  };
}

function forecastDependent({
  selectedTask,
  dependentId,
  dependencies,
  activeRunStatuses,
  requests,
  viewerUserId,
}: {
  selectedTask: TaskDto;
  dependentId: string;
  dependencies: DependencyContext;
  activeRunStatuses: Map<string, RunStatusDto>;
  requests: TaskRequests;
  viewerUserId: string | undefined;
}): WorkNextUpDependentTask {
  const related = dependencies.relatedTasksById.get(dependentId) ?? {
    id: dependentId,
    key: "Task",
    title: "Task details unavailable",
    status: null,
    task: undefined,
  };
  const remainingPrerequisites =
    dependencies.unresolvedByTaskId.get(dependentId) ?? [];
  const dependencyDataLoaded = dependencies.completeTaskIds.has(dependentId);
  const remainingPrerequisiteCount = dependencyDataLoaded
    ? remainingPrerequisites.length - (selectedTask.status === "done" ? 0 : 1)
    : null;
  const prerequisitesCompleteAfterCompletion = remainingPrerequisiteCount === 0;
  const task = related.task;
  const forecastDisposition = task
    ? taskDisposition(
        { ...task, dependenciesResolved: true },
        activeRunStatuses.get(task.id) ?? null,
        requests,
        viewerUserId,
      )
    : undefined;
  const readyAfterCompletion = Boolean(
    selectedTask.status !== "done" &&
    task &&
    !task.dependenciesResolved &&
    prerequisitesCompleteAfterCompletion &&
    forecastDisposition &&
    isActionable(forecastDisposition),
  );

  return {
    ...related,
    dependencyDataLoaded,
    remainingPrerequisites,
    remainingPrerequisiteCount,
    prerequisitesCompleteAfterCompletion,
    readyAfterCompletion,
    reason: dependentReason({
      task,
      remainingPrerequisiteCount,
      readyAfterCompletion,
      selectedTaskCompleted: selectedTask.status === "done",
      forecastDisposition,
    }),
  };
}

function dependentReason({
  task,
  remainingPrerequisiteCount,
  readyAfterCompletion,
  selectedTaskCompleted,
  forecastDisposition,
}: {
  task: TaskDto | undefined;
  remainingPrerequisiteCount: number | null;
  readyAfterCompletion: boolean;
  selectedTaskCompleted: boolean;
  forecastDisposition: TaskDisposition | undefined;
}) {
  if (task?.status === "done") return "Already completed.";
  if (task?.status === "cancelled") return "Cancelled; it will not start.";
  if (remainingPrerequisiteCount === null) {
    return "Open this task to check all of its prerequisites.";
  }
  if (remainingPrerequisiteCount > 0) {
    return `Still needs ${remainingPrerequisiteCount} other prerequisite${remainingPrerequisiteCount === 1 ? "" : "s"}.`;
  }
  if (readyAfterCompletion) {
    return forecastDisposition?.action === "founder"
      ? "Your work can begin after this task is completed."
      : forecastDisposition?.action === "retry"
        ? "Prerequisites will be complete; retry is available."
        : "Ready to start after this task is completed.";
  }
  if (selectedTaskCompleted && task?.dependenciesResolved) {
    return forecastDisposition?.reason ?? "All prerequisites are complete.";
  }
  return `Prerequisites will be complete. ${forecastDisposition?.reason ?? "Open the task to check its next action."}`;
}

function taskDisposition(
  task: TaskDto,
  runStatus: RunStatusDto | null,
  requests: TaskRequests,
  viewerUserId: string | undefined,
): TaskDisposition {
  const disposition = (
    category: WorkNextUpCategory,
    action: WorkNextUpAction,
    actionLabel: string,
    reason: string,
    readiness: WorkNextUpReadiness | null = category === "ready"
      ? { label: "Ready to start", waiting: false }
      : null,
  ): TaskDisposition => ({
    category,
    action,
    actionLabel,
    reason,
    readiness,
    runStatus,
  });
  const cannotStart = { label: "Can't start yet", waiting: true };

  if (task.status === "done") {
    return disposition(
      "completed",
      "inspect",
      "View result",
      "Task completed.",
    );
  }
  if (task.status === "cancelled") {
    return disposition("cancelled", "inspect", "View task", "Task cancelled.");
  }
  if (requests.respond.has(task.id)) {
    return disposition(
      "attention",
      "inspect",
      "Respond",
      "An open question or approval needs your response before work can continue.",
    );
  }
  if (runStatus || task.status === "in_progress") {
    return disposition("running", "inspect", "Open task", runReason(runStatus));
  }
  // Someone else answers it (ADR 0017): the Task waits, and nothing starts
  // it, reviews it or retries it until they do.
  if (requests.pending.has(task.id)) {
    return disposition(
      "waiting",
      "inspect",
      "Open task",
      "Waiting for an answer to an open question or approval.",
      cannotStart,
    );
  }
  if (task.status === "in_review") {
    return disposition(
      "attention",
      "review",
      "Review",
      task.dependenciesResolved
        ? "Review the result and mark the task complete when it meets the criteria."
        : "Review the result; prerequisites must be completed before acceptance.",
    );
  }
  if (task.status === "failed") {
    return disposition(
      "attention",
      "inspect",
      "Review outcome",
      "The agent reported an unsuccessful outcome. Open the task to decide how to proceed.",
    );
  }
  if (!task.dependenciesResolved) {
    return disposition(
      "waiting",
      "inspect",
      "See what’s needed",
      "Waiting for all prerequisite tasks to be completed.",
    );
  }
  if (task.status === "blocked") {
    return disposition(
      "attention",
      "inspect",
      "Resolve blocker",
      "Prerequisites are complete, but this task is marked blocked. Check what needs resolving.",
    );
  }
  // Only the assignee starts it, on their own computer (ADR 0014). An
  // assignee who left, an archived Agent, or an assignee who may not run
  // tasks in the project (ADR 0017) will not change on its own: someone has
  // to give the Task another member or Agent, or let the assignee run tasks.
  if (
    task.execution.unreadyReason === "assignee_inactive" ||
    task.execution.unreadyReason === "agent_archived" ||
    task.execution.unreadyReason === "assignee_cannot_run"
  ) {
    return disposition(
      "attention",
      "inspect",
      "View assignment",
      unreadyReasonLabel(task, viewerUserId),
      cannotStart,
    );
  }
  // Until the Agent can run on the assignee's computer, the Task waits and
  // says what for.
  if (!task.execution.assigneeReady) {
    return disposition(
      "waiting",
      "inspect",
      "See what’s needed",
      unreadyReasonLabel(task, viewerUserId),
      cannotStart,
    );
  }
  if (task.status === "error") {
    return disposition(
      "attention",
      "retry",
      "Retry task",
      "The previous run had a technical error. Prerequisites are complete, so it can be retried.",
    );
  }
  return disposition(
    "ready",
    "start",
    "Start task",
    task.blockedByCount === 0
      ? "No prerequisites. This task can start independently."
      : task.blockedByCount === 1
        ? "The prerequisite is complete."
        : `All ${task.blockedByCount} prerequisites are complete.`,
  );
}

function runReason(status: RunStatusDto | null) {
  if (status === "running") return "The agent is working on this task.";
  if (status === "waiting_local_directory") {
    // A park on a busy path, not a request for input (docs/api/runs.md):
    // another run holds this machine's working directory.
    return "Waiting for this machine's working directory to become free.";
  }
  if (status === "deferred") return "Queued for a later run attempt.";
  if (status === "dispatched") return "The run is starting.";
  if (status === "queued") return "Queued; waiting for the run to start.";
  return "Task execution is in progress. Open the task for run details.";
}

function isTerminalRunStatus(status: RunStatusDto) {
  return ["completed", "failed", "cancelled"].includes(status);
}

function isActionable(item: Pick<TaskDisposition, "action">) {
  return ["start", "retry", "founder"].includes(item.action);
}

function recommendationReason(
  task: TaskDto,
  readyAfterCompletionCount: number,
) {
  const priority = `${task.priority[0].toUpperCase()}${task.priority.slice(1)} priority`;
  return readyAfterCompletionCount > 0
    ? `${priority} · makes ${readyAfterCompletionCount} task${readyAfterCompletionCount === 1 ? "" : "s"} actionable when completed`
    : `${priority} · prerequisites complete`;
}

function compareNextUpItems(left: WorkNextUpItem, right: WorkNextUpItem) {
  const priorityOrder: Record<TaskDto["priority"], number> = {
    urgent: 0,
    high: 1,
    medium: 2,
    low: 3,
  };
  return (
    priorityOrder[left.task.priority] - priorityOrder[right.task.priority] ||
    right.readyAfterCompletionCount - left.readyAfterCompletionCount ||
    left.task.number - right.task.number ||
    left.task.id.localeCompare(right.task.id)
  );
}
