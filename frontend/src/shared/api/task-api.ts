import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
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

export const taskApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    listWorkspaceTasks: builder.query<
      Awaited<ReturnType<typeof taskService.listTasks>>,
      { companyId: string; options?: TaskListOptions }
    >({
      providesTags: (_result, _error, argument: { companyId: string }) => [
        { id: `${argument.companyId}:list`, type: "Task" },
        { id: argument.companyId, type: "Task" },
      ],
      queryFn: (argument: { companyId: string; options?: TaskListOptions }) =>
        executeBoardlyQuery(
          () => taskService.listTasks(argument.companyId, argument.options),
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
      queryFn: ({ companyId, taskId }) =>
        executeBoardlyQuery(
          () => taskService.getTask(companyId, taskId),
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
        executeBoardlyQuery(
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
        executeBoardlyQuery(
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
        executeBoardlyQuery(
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
        executeBoardlyQuery(
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
        executeBoardlyQuery(
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
        executeBoardlyQuery(
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
        executeBoardlyQuery(
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
