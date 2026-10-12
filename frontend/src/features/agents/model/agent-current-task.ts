import { compareTaskReadiness } from "@/features/company-plan/model/task-work-state";
import type { RunDto, TaskDto } from "@/shared/api/types";

// The Run states that hold one of an Agent's task slots. They are the states
// its `activeRunCount` counts, so "Running · 1" and the Current task beside
// it always name the same work.
const SLOT_RUN_STATUSES: ReadonlySet<RunDto["status"]> = new Set([
  "dispatched",
  "running",
  "waiting_local_directory",
]);

// The one Task an Agent's "Current task" names for the viewer: the Task it
// is running on the viewer's own computer, then one waiting on the viewer's
// response, then the most ready unfinished Task. A Run's `runtimeId` reaches
// only the member who registered that runtime (ADR 0014), so a Run that
// carries one is running on the viewer's computer.
export function agentCurrentTask({
  agentId,
  tasks,
  activeRuns,
  attentionTaskIds,
}: {
  agentId: string;
  tasks: readonly TaskDto[];
  activeRuns: readonly RunDto[];
  attentionTaskIds: ReadonlySet<string>;
}): TaskDto | undefined {
  const runningTaskIds = new Set(
    activeRuns.flatMap((run) =>
      run.agentId === agentId &&
      run.taskId &&
      run.runtimeId &&
      SLOT_RUN_STATUSES.has(run.status)
        ? [run.taskId]
        : [],
    ),
  );
  const rank = (task: TaskDto) =>
    runningTaskIds.has(task.id) ? 0 : attentionTaskIds.has(task.id) ? 1 : 2;
  return tasks
    .filter(
      (task) =>
        task.executedBy.kind === "agent" &&
        task.executedBy.id === agentId &&
        task.status !== "done" &&
        task.status !== "cancelled",
    )
    .sort((a, b) => rank(a) - rank(b) || compareTaskReadiness(a, b))[0];
}
