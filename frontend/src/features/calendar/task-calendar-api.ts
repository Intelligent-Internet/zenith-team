import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";

type Schemas = components["schemas"];
export type TaskCalendarOccurrence = Schemas["TaskCalendarOccurrence"];
export type TaskCalendarSchedule = Schemas["TaskCalendarSchedule"];
export type TaskScheduleTrigger = TaskCalendarSchedule["trigger"];
type TaskArgs = { companyId: string; taskId: string };
const path = ({ companyId, taskId }: TaskArgs) =>
  `/companies/${companyId}/tasks/${taskId}`;
const tags = (companyId: string) => [
  { type: "Task" as const, id: companyId },
  { type: "Calendar" as const, id: `${companyId}:tasks` },
];
const api = boardlyApi.injectEndpoints({
  endpoints: (build) => ({
    calendarTasks: build.query<
      Schemas["TaskCalendarPage"],
      { companyId: string; from: string; to: string }
    >({
      queryFn: ({ companyId, ...params }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Schemas["TaskCalendarPage"]>(
              `/companies/${companyId}/calendar/tasks`,
              { params },
            ),
          "Could not load scheduled tasks.",
        ),
      providesTags: (_data, _error, { companyId }) => tags(companyId),
      keepUnusedDataFor: 120,
    }),
    taskSchedule: build.query<TaskCalendarSchedule, TaskArgs>({
      queryFn: (args) =>
        executeBoardlyQuery(
          () => apiClient.get<TaskCalendarSchedule>(`${path(args)}/schedule`),
          "Could not load the task schedule.",
        ),
      providesTags: (_data, _error, { companyId }) => tags(companyId),
    }),
    saveTaskSchedule: build.mutation<
      Schemas["TaskMutationResult"],
      TaskArgs & { input: Schemas["TaskScheduleInput"] }
    >({
      queryFn: ({ input, ...args }) =>
        executeBoardlyQuery(
          () =>
            apiClient.put<Schemas["TaskMutationResult"]>(
              `${path(args)}/schedule`,
              input,
            ),
          "Could not save the task schedule.",
        ),
      invalidatesTags: (_data, _error, { companyId }) => tags(companyId),
    }),
    changeTaskOccurrence: build.mutation<
      Schemas["TaskMutationResult"],
      TaskArgs & { input: Schemas["TaskOccurrenceInput"] }
    >({
      queryFn: ({ input, ...args }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Schemas["TaskMutationResult"]>(
              `${path(args)}/schedule/occurrences`,
              input,
            ),
          "Could not change this occurrence.",
        ),
      invalidatesTags: (_data, _error, { companyId }) => tags(companyId),
    }),
    decideTaskAssignment: build.mutation<
      Schemas["TaskMutationResult"],
      TaskArgs & { input: Schemas["TaskAssignmentInput"] }
    >({
      queryFn: ({ input, ...args }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Schemas["TaskMutationResult"]>(
              `${path(args)}/assignment`,
              input,
            ),
          "Could not update the assignment.",
        ),
      invalidatesTags: (_data, _error, { companyId }) => tags(companyId),
    }),
  }),
});
export const {
  useCalendarTasksQuery,
  useTaskScheduleQuery,
  useSaveTaskScheduleMutation,
  useChangeTaskOccurrenceMutation,
  useDecideTaskAssignmentMutation,
} = api;
