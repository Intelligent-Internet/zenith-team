import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import { conversationService } from "@/shared/api/conversation.service";
import {
  taskService,
  type TaskWorkspaceResponse,
} from "@/shared/api/task.service";
import type {
  ConversationDisplayItemSummary,
  TaskCreateRequest,
  TaskDto,
  TaskDependencySetDto,
  TaskDependencyGraphDto,
  TaskListOptions,
  TaskStartExecutionRequest,
  TaskUpdateRequest,
} from "@/shared/api/types";

export const taskApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listWorkspaceTasks: builder.query<
      Awaited<ReturnType<typeof taskService.listTasks>>,
      { companyId: string; options?: TaskListOptions }
    >({
      providesTags: (_result, _error, argument: { companyId: string }) => [
        { id: `${argument.companyId}:list`, type: "Task" },
        { id: argument.companyId, type: "Task" },
      ],
      queryFn: (
        argument: { companyId: string; options?: TaskListOptions },
        { signal },
      ) =>
        executeZenithTeamQuery(
          () =>
            taskService.listTasks(argument.companyId, argument.options, signal),
          "Tasks could not be loaded.",
        ),
    }),
    getWorkspaceTask: builder.query<
      TaskDto,
      { companyId: string; taskId: string }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: `${companyId}:workspace`, type: "Task" },
        { id: companyId, type: "Task" },
      ],
      queryFn: ({ companyId, taskId }, { signal }) =>
        executeZenithTeamQuery(
          () => taskService.getTask(companyId, taskId, signal),
          "Task could not be loaded.",
          { retry: true },
        ),
    }),
    getWorkspaceTaskWorkspace: builder.query<
      TaskWorkspaceResponse,
      { companyId: string; taskId: string }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: `${companyId}:workspace`, type: "Task" },
        { id: companyId, type: "Conversation" },
        { id: companyId, type: "Run" },
        { id: companyId, type: "Task" },
      ],
      queryFn: ({ companyId, taskId }) =>
        executeZenithTeamQuery(
          () => taskService.getWorkspace(companyId, taskId),
          "Task workspace could not be loaded.",
          { retry: true },
        ),
    }),
    getWorkspaceTaskDependencies: builder.query<
      TaskDependencySetDto,
      { companyId: string; taskId: string }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: `${companyId}:dependencies`, type: "Task" },
        { id: companyId, type: "Task" },
      ],
      queryFn: ({ companyId, taskId }) =>
        executeZenithTeamQuery(
          () => taskService.listDependencies(companyId, taskId),
          "Task dependencies could not be loaded.",
        ),
    }),
    getWorkspaceTaskDependencyGraph: builder.query<
      TaskDependencyGraphDto,
      { companyId: string }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: `${companyId}:dependencies`, type: "Task" },
        { id: companyId, type: "Task" },
      ],
      queryFn: ({ companyId }) =>
        executeZenithTeamQuery(
          () => taskService.listDependencyGraph(companyId),
          "Task dependency graph could not be loaded.",
        ),
    }),
    listWorkspaceTaskMessages: builder.query<
      ConversationDisplayItemSummary[],
      { taskId: string; conversationId: string }
    >({
      providesTags: (_result, _error, { conversationId }) => [
        { id: conversationId, type: "Message" },
      ],
      queryFn: ({ taskId, conversationId }) =>
        executeZenithTeamQuery(
          () =>
            conversationService.listTaskDisplayItems(taskId, conversationId),
          "Messages could not be loaded.",
        ),
    }),
    createWorkspaceTask: builder.mutation<
      Awaited<ReturnType<typeof taskService.createTask>>,
      { companyId: string; request: TaskCreateRequest }
    >({
      invalidatesTags: (_result, _error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "Task" },
      ],
      queryFn: (argument: { companyId: string; request: TaskCreateRequest }) =>
        executeZenithTeamQuery(
          () => taskService.createTask(argument.companyId, argument.request),
          "Task could not be created.",
        ),
    }),
    updateWorkspaceTask: builder.mutation<
      Awaited<ReturnType<typeof taskService.updateTask>>,
      {
        companyId: string;
        request: TaskUpdateRequest;
        taskId: string;
      }
    >({
      // A refusal for a missing Project permission means the caller's
      // access changed: reload the Projects too, so their actions follow.
      invalidatesTags: (_result, error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "Task" },
        ...(error?.code === "project_permission_required"
          ? [{ id: argument.companyId, type: "Project" as const }]
          : []),
      ],
      queryFn: (argument: {
        companyId: string;
        request: TaskUpdateRequest;
        taskId: string;
      }) =>
        executeZenithTeamQuery(
          () =>
            taskService.updateTask(
              argument.companyId,
              argument.taskId,
              argument.request,
            ),
          "Task could not be updated.",
        ),
    }),
    startWorkspaceTaskExecution: builder.mutation<
      Awaited<ReturnType<typeof taskService.startExecution>>,
      {
        companyId: string;
        request: TaskStartExecutionRequest;
        taskId: string;
      }
    >({
      invalidatesTags: (_result, _error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "Run" },
        { id: argument.companyId, type: "Task" },
      ],
      queryFn: (argument) =>
        executeZenithTeamQuery(
          () =>
            taskService.startExecution(
              argument.companyId,
              argument.taskId,
              argument.request,
            ),
          "Task execution could not be started.",
        ),
    }),
  }),
});

export const {
  useCreateWorkspaceTaskMutation,
  useGetWorkspaceTaskQuery,
  useGetWorkspaceTaskWorkspaceQuery,
  useGetWorkspaceTaskDependenciesQuery,
  useGetWorkspaceTaskDependencyGraphQuery,
  useLazyListWorkspaceTasksQuery,
  useListWorkspaceTaskMessagesQuery,
  useListWorkspaceTasksQuery,
  useStartWorkspaceTaskExecutionMutation,
  useUpdateWorkspaceTaskMutation,
} = taskApi;
