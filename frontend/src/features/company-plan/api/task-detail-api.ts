import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import { apiClient } from "@/lib/api-client";
import {
  activityService,
  type ActivityPage,
} from "@/shared/api/activity.service";
import { runService } from "@/shared/api/run.service";
import type { AnalyticsTaskUsage, RunDto } from "@/shared/api/types";

type TaskDetailRequest = { companyId: string; taskId: string };

// One Task's latest Run, for `getTasksLatestRuns`: a `run.*` frame of that
// Task invalidates it, so a Run elsewhere in the Company never refetches
// the batch.
export function taskLatestRunTag(taskId: string) {
  return { type: "Run" as const, id: `task:${taskId}:latest` };
}

const taskDetailApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getTaskLatestRun: builder.query<RunDto | null, TaskDetailRequest>({
      providesTags: (_result, _error, { companyId }) => [
        { type: "Run", id: companyId },
      ],
      queryFn: ({ companyId, taskId }) =>
        executeBoardlyQuery(async () => {
          const page = await runService.listRuns(companyId, {
            taskId,
            limit: 1,
          });
          return page.items[0] ?? null;
        }, "The latest run could not be loaded."),
    }),
    // The latest Run of each named Task, by Task id (null when it never
    // ran): the same read as `getTaskLatestRun`, for a board's few Tasks
    // that wait in review, so it shows "Needs your review" only where a
    // Run left work to review. It is tagged per Task (`taskLatestRunTag`),
    // not by the Company, so it refetches only when one of these Tasks'
    // Runs changes.
    getTasksLatestRuns: builder.query<
      Record<string, RunDto | null>,
      { companyId: string; taskIds: readonly string[] }
    >({
      providesTags: (_result, _error, { taskIds }) =>
        taskIds.map(taskLatestRunTag),
      queryFn: ({ companyId, taskIds }) =>
        executeBoardlyQuery(async () => {
          const pages = await Promise.all(
            taskIds.map((taskId) =>
              runService.listRuns(companyId, { taskId, limit: 1 }),
            ),
          );
          return Object.fromEntries(
            taskIds.map((taskId, index) => [
              taskId,
              pages[index]?.items[0] ?? null,
            ]),
          );
        }, "The latest runs could not be loaded."),
    }),
    getTaskActivity: builder.query<ActivityPage, TaskDetailRequest>({
      providesTags: (_result, _error, { companyId }) => [
        { type: "Activity", id: companyId },
      ],
      queryFn: ({ companyId, taskId }) =>
        executeBoardlyQuery(
          () =>
            activityService.listCompanyActivityPage(companyId, {
              entityType: "task",
              entityId: taskId,
              limit: 10,
            }),
          "Task activity could not be loaded.",
        ),
    }),
    getTaskUsage: builder.query<AnalyticsTaskUsage, TaskDetailRequest>({
      providesTags: (_result, _error, { companyId, taskId }) => [
        { type: "Analytics", id: companyId },
        { type: "Task", id: taskId },
      ],
      queryFn: ({ companyId, taskId }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<AnalyticsTaskUsage>(
              `/companies/${encodeURIComponent(companyId)}/tasks/${encodeURIComponent(taskId)}/usage`,
            ),
          "Task usage could not be loaded.",
        ),
    }),
  }),
});

export const {
  useGetTaskActivityQuery,
  useGetTaskLatestRunQuery,
  useGetTasksLatestRunsQuery,
  useGetTaskUsageQuery,
} = taskDetailApi;
