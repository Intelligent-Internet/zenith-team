import { skipToken } from "@reduxjs/toolkit/query";
import { useMemo } from "react";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import {
  useGetTaskLatestRunQuery,
  useGetTasksLatestRunsQuery,
} from "@/features/company-plan/api/task-detail-api";
import { useProjectAccessLoss } from "@/features/company-plan/hooks/use-project-access-loss";
import {
  runLeftWorkToReview,
  taskAwaitsViewerReview,
} from "@/features/company-plan/model/task-execution";
import { taskWorkStatusLabel } from "@/features/company-plan/model/task-work-state";
import { useListWorkspaceTasksQuery } from "@/shared/api/workspace-api";
import type { TaskDto } from "@/shared/api/types";
import { useCan } from "@/shared/permissions";

// The Tasks among `tasks` that wait on this viewer's review: an Agent's Task
// in review that the viewer decides (`taskAwaitsViewerReview`) and whose
// latest Run completed, so there is a result to review
// (`runLeftWorkToReview`). Only those few Tasks' latest Runs are read.
// Everyone else reads the Tasks' plain status, so a reader is never told a
// review is theirs.
export function useViewerReviewTaskIds(
  companyId: string,
  tasks: readonly TaskDto[] | undefined,
) {
  const viewerUserId = useSessionSnapshot().userId;
  const canAssign = useCan("tasks.assign");
  const candidateIds = useMemo(
    () =>
      (tasks ?? [])
        .filter((task) => taskAwaitsViewerReview(task, viewerUserId, canAssign))
        .map((task) => task.id)
        .sort(),
    [canAssign, tasks, viewerUserId],
  );
  const runs = useGetTasksLatestRunsQuery(
    candidateIds.length > 0 ? { companyId, taskIds: candidateIds } : skipToken,
  );
  // The last answer stays while a newer set loads, so a badge never blinks.
  const latestRuns = runs.isError ? undefined : runs.data;
  const taskIds = useMemo(
    () =>
      new Set(
        candidateIds.filter((taskId) =>
          runLeftWorkToReview(latestRuns?.[taskId]),
        ),
      ),
    [candidateIds, latestRuns],
  );
  const hasCandidates = candidateIds.length > 0;
  return {
    taskIds,
    pending: hasCandidates && !runs.data,
    isError: runs.isError,
    retry: () => {
      if (hasCandidates) void runs.refetch();
    },
  };
}

// The Project's Tasks that wait on this viewer's review
// (`useViewerReviewTaskIds`). A Project the viewer just lost is no error
// here: the access check takes them away (ADR 0017).
export function useProjectTaskReviews(companyId: string, projectId: string) {
  const query = useListWorkspaceTasksQuery(
    { companyId, options: { project_id: projectId } },
    { refetchOnMountOrArgChange: true },
  );
  const checkingAccess = useProjectAccessLoss(
    companyId,
    projectId,
    query.error,
  );
  const tasks = query.isError ? undefined : query.currentData?.items;
  const reviews = useViewerReviewTaskIds(companyId, tasks);
  return {
    taskIds: reviews.taskIds,
    count: tasks && !reviews.pending ? reviews.taskIds.size : undefined,
    isLoading:
      checkingAccess ||
      (!query.currentData && !query.isError) ||
      (reviews.pending && !reviews.isError),
    isError: (query.isError && !checkingAccess) || reviews.isError,
    retry: () => {
      void query.refetch();
      reviews.retry();
    },
  };
}

// Whether one Task waits on this viewer's review, as the board's
// `useProjectTaskReviews` decides it, for a screen that shows one Task: the
// Task panel and the expanded Task page.
export function useTaskAwaitsViewerReview(task: TaskDto): boolean {
  const viewerUserId = useSessionSnapshot().userId;
  const canAssign = useCan("tasks.assign");
  const decides = taskAwaitsViewerReview(task, viewerUserId, canAssign);
  const latestRun = useGetTaskLatestRunQuery(
    decides ? { companyId: task.companyId, taskId: task.id } : skipToken,
  );
  return (
    decides && !latestRun.isError && runLeftWorkToReview(latestRun.currentData)
  );
}

// One Task's plain status for this viewer: "Needs review" only when its
// review waits on them (`useTaskAwaitsViewerReview`), else "In review".
export function useTaskWorkStatusLabel(task: TaskDto): string {
  return taskWorkStatusLabel(task, useTaskAwaitsViewerReview(task));
}
