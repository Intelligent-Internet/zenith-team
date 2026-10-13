import { useGetWorkspaceTaskDependenciesQuery } from "@/shared/api/task-api";
import { useMemo } from "react";

import type { TaskStatusDto } from "@/shared/api/types";
import { useTaskLabel } from "@/shared/lib/task-label";

export type TaskDependencyPresentationItem = {
  assigneeName: string;
  id: string;
  isUnresolved: boolean;
  taskId: string;
  /** The one label every surface shows for the task, {prefix}-{number}. */
  taskLabel: string;
  taskStatus: TaskStatusDto | "unknown";
  taskTitle: string;
};

export function useTaskDependencyPresentation(
  companyId: string,
  taskId: string | undefined,
) {
  const dependenciesQuery = useGetWorkspaceTaskDependenciesQuery(
    { companyId, taskId: taskId ?? "" },
    { skip: !companyId || !taskId },
  );
  const labelTask = useTaskLabel(companyId || undefined);
  const items = useMemo(() => {
    const dependencies = dependenciesQuery.currentData;
    if (!dependencies || !taskId) {
      return [];
    }
    return dependencies.blockedBy.map((dependency) => {
      const relatedTask = dependency.task;
      return {
        assigneeName: relatedTask.assignee.name,
        id: `${taskId}:${relatedTask.id}`,
        isUnresolved: relatedTask.status !== "done",
        taskId: relatedTask.id,
        taskLabel: labelTask(relatedTask),
        taskStatus: relatedTask.status,
        taskTitle: relatedTask.title,
      } satisfies TaskDependencyPresentationItem;
    });
  }, [dependenciesQuery.currentData, labelTask, taskId]);

  return {
    blockedBy: items,
    hasUnresolvedDependencies: items.some((item) => item.isUnresolved),
    isError: dependenciesQuery.isError,
    isLoading:
      dependenciesQuery.currentData === undefined &&
      dependenciesQuery.isFetching,
  };
}
