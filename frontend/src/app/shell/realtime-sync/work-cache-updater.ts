import type { TaskDto, TaskListOptions } from "@/shared/api/types";
import { boardlyApi } from "@/shared/api/boardly-api";
import { workspaceApi } from "@/shared/api/workspace-api";
import { taskLatestRunTag } from "@/features/company-plan/api/task-detail-api";
import {
  workActiveRunsTag,
  workNextUpApi,
} from "@/features/company-plan/api/work-next-up-api";
import {
  realtimeEventConversationId,
  type BoardlyRealtimeEvent,
} from "@/shared/realtime/realtime-client.service";
import {
  removeVersioned,
  replaceVersioned,
  type RealtimeSyncContext,
} from "@/app/shell/realtime-sync/realtime-sync-context";

const runSnapshotEvents = new Set([
  "run.queued",
  "run.dispatched",
  "run.auth_wait_changed",
  "run.running",
  "run.waiting_local_directory",
  "run.completed",
  "run.failed",
  "run.cancelled",
]);
const terminalRunEvents = new Set([
  "run.completed",
  "run.failed",
  "run.cancelled",
]);
// The transitions that take or free one of an Agent's task slots. A fresh
// run.queued takes none, but a dispatched Run put back after a failed claim
// also arrives as run.queued, and that frees its slot.
const slotRunEvents = new Set([
  "run.queued",
  "run.dispatched",
  "run.running",
  "run.waiting_local_directory",
  ...terminalRunEvents,
]);

export function applyWorkCacheEvent(
  context: RealtimeSyncContext,
  event: BoardlyRealtimeEvent,
) {
  switch (event.type) {
    case "task.created":
    case "task.updated":
      updateTask(context, event.payload as TaskDto, event.type);
      context.requestRefresh(
        "tasks",
        "taskDependencies",
        "taskWorkspaces",
        "artifacts",
      );
      return true;
    case "deliverable.created":
    case "deliverable.reviewed":
    case "deliverable.superseded": {
      context.dispatch(
        boardlyApi.util.invalidateTags([
          { type: "Artifact" },
          { type: "Deliverable" },
          { type: "Run" },
          { id: context.companyId, type: "Inbox" },
          { id: `${context.companyId}:counts`, type: "Inbox" },
        ]),
      );
      return true;
    }
    default:
      if (runSnapshotEvents.has(event.type)) {
        const runId = event.payload.id;
        const taskId = event.payload.taskId;
        const conversationId = realtimeEventConversationId(event);
        if (typeof runId === "string") {
          context.dispatch(
            boardlyApi.util.invalidateTags([
              { id: context.companyId, type: "Run" },
              { id: String(runId), type: "Run" },
              ...(typeof taskId === "string" ? [taskLatestRunTag(taskId)] : []),
              ...(event.payload.runType === "schedule"
                ? [{ id: context.companyId, type: "Schedule" as const }]
                : []),
              ...(conversationId
                ? [{ id: conversationId, type: "Conversation" as const }]
                : []),
              ...(terminalRunEvents.has(event.type)
                ? [{ type: "Artifact" as const }]
                : []),
            ]),
          );
        }
        refreshRunAggregates(context, event.type, event.payload.runType);
        return true;
      }
      return false;
  }
}

function updateTask(
  context: RealtimeSyncContext,
  task: TaskDto,
  eventType: "task.created" | "task.updated",
) {
  let previousStatus: TaskDto["status"] | undefined;
  const state = context.getState();
  for (const argument of workspaceApi.util.selectCachedArgsForQuery(
    state,
    "listWorkspaceTasks",
  )) {
    if (argument.companyId !== context.companyId) continue;
    context.dispatch(
      workspaceApi.util.updateQueryData(
        "listWorkspaceTasks",
        argument,
        (page) => {
          const tasks = page.items;
          const cached = tasks.find((candidate) => candidate.id === task.id);
          previousStatus ??= cached?.status;
          if (taskMatches(task, argument.options)) {
            // Task frames reach other people and leave the viewer's
            // `access` out (ADR 0017); keep what the read said until the
            // refetch this frame asks for lands.
            replaceVersioned(
              tasks,
              task.access || !cached?.access
                ? task
                : { ...task, access: cached.access },
            );
            tasks.sort((left, right) => right.number - left.number);
          } else {
            removeVersioned(tasks, task.id, task.revision);
          }
        },
      ),
    );
  }

  if (context.pathname === "/app") {
    const previousWasActive = previousStatus === "in_progress";
    const taskIsActive = task.status === "in_progress";
    if (eventType === "task.created" || previousStatus !== undefined) {
      const difference = Number(taskIsActive) - Number(previousWasActive);
      if (difference !== 0) {
        context.dispatch(
          workspaceApi.util.updateQueryData(
            "getWorkspaceCockpit",
            context.companyId,
            (cockpit) => {
              if (!cockpit) return;
              cockpit.counters.active_tasks = Math.max(
                0,
                cockpit.counters.active_tasks + difference,
              );
            },
          ),
        );
      }
    } else {
      context.requestRefresh("cockpit");
    }
  }
}

// The reads built from Runs refresh apart from the Run itself. Task carries
// no run-derived field. The Agent roster does: `activeRunCount` counts the
// viewer's own busy task slots (ADR 0014), and a frame never says whose
// computer a Run is on, so the roster is read again when a slot is taken or
// freed. A direct chat never holds a slot.
function refreshRunAggregates(
  context: RealtimeSyncContext,
  eventType: string,
  runType: unknown,
) {
  context.dispatch(
    workNextUpApi.util.invalidateTags([workActiveRunsTag(context.companyId)]),
  );
  if (runType === "direct_chat") {
    context.requestRefresh("directConversations");
  }
  if (slotRunEvents.has(eventType) && runType === "task") {
    context.requestRefresh("agents");
  }
  if (terminalRunEvents.has(eventType)) {
    context.requestRefresh("analytics");
  }
}

function taskMatches(task: TaskDto, options?: TaskListOptions) {
  if (!options) return true;
  if (options.project_id && task.projectId !== options.project_id) return false;
  if (options.status?.length && !options.status.includes(task.status)) {
    return false;
  }
  if (options.priority?.length && !options.priority.includes(task.priority)) {
    return false;
  }
  if (options.assignee_id && task.assignee.id !== options.assignee_id) {
    return false;
  }
  // The executing Agent is its own filter now (ADR 0014).
  if (
    options.executed_by_agent_id &&
    task.executedBy.id !== options.executed_by_agent_id
  ) {
    return false;
  }
  return (
    options.dependencies_resolved === undefined ||
    task.dependenciesResolved === options.dependencies_resolved
  );
}
