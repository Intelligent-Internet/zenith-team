import type { BaseQueryApi } from "@reduxjs/toolkit/query";

import {
  activityService,
  type ActivityCalendarRequest,
  type ActivityCalendarSummary,
  type ActivityListOptions,
  type ActivityPage,
} from "@/shared/api/activity.service";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import { companyService } from "@/shared/api/company.service";
import { attachmentService } from "@/shared/api/attachment.service";
import { cockpitService } from "@/shared/api/cockpit.service";
import { conversationService } from "@/shared/api/conversation.service";
import { companySearchService } from "@/shared/api/company-search.service";
import { companyResourceService } from "@/shared/api/company-resource.service";
import {
  taskService,
  type TaskWorkspaceResponse,
} from "@/shared/api/task.service";
import type {
  ActivityInsight,
  CompanyDetail,
  CompanyPage,
  CompanySettingsProfileRequest,
  CompanySearchPage,
  CompanySearchRequest,
  CompanyUsagePeriod,
  CompanyUsageReport,
  ConversationStateDto,
  ConversationStateUpdateRequest,
  EntityRevisionRequest,
  CompanyResourceCreateRequest,
  CompanyResourceDto,
  CompanyResourceUpdateRequest,
  ConversationDisplayItemSummary,
  ConversationHistoryWindow,
  SendConversationMessageRequest,
  ProactiveModeDto,
  UpdateProactiveModeRequest,
  TaskCreateRequest,
  TaskDto,
  TaskDependencySetDto,
  TaskDependencyGraphDto,
  TaskListOptions,
  TaskStartExecutionRequest,
  TaskUpdateRequest,
  DirectConversationDto,
  RenameDirectConversationRequest,
  AgentCreateRequest,
  AgentDto,
  AgentUpdateRequest,
  AgentUsageReport,
  AnalyticsSummary,
  AnalyticsSeries,
  AnalyticsBreakdown,
  AnalyticsFailures,
  AnalyticsTaskUsage,
  AnalyticsBudget,
  AnalyticsBudgetInput,
  AnalyticsBudgetPage,
  AnalyticsHeatmap,
} from "@/shared/api/types";
import { agentDirectoryService } from "@/shared/api/agent-directory.service";

export const workspaceApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    listWorkspaceConversationStates: builder.query({
      queryFn: (companyId: string) =>
        executeBoardlyQuery(
          () => conversationService.listStates(companyId),
          "Chat filing states could not be loaded.",
        ),
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "Conversation" },
      ],
    }),
    listWorkspaceChatConversations: builder.query({
      queryFn: (companyId: string) =>
        executeBoardlyQuery(
          () => conversationService.listDirectConversations(companyId, true),
          "Chats could not be loaded.",
        ),
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "Conversation" },
      ],
    }),
    listWorkspaceConversationPins: builder.query({
      queryFn: (companyId: string) =>
        executeBoardlyQuery(
          () => conversationService.listPins(companyId),
          "Pinned chats could not be loaded.",
        ),
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "Conversation" },
      ],
    }),
    createWorkspaceChatSession: builder.mutation<
      DirectConversationDto,
      { companyId: string; agentId: string; requestKey: string }
    >({
      queryFn: ({ companyId, agentId, requestKey }) =>
        executeBoardlyQuery(
          () =>
            conversationService.createDirectConversation(companyId, {
              agentId,
              requestKey,
            }),
          "The new chat could not be created.",
        ),
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Conversation" },
      ],
    }),
    renameWorkspaceConversation: builder.mutation<
      DirectConversationDto,
      {
        companyId: string;
        conversationId: string;
        request: RenameDirectConversationRequest;
      }
    >({
      queryFn: ({ companyId, conversationId, request }) =>
        executeBoardlyQuery(
          () =>
            conversationService.renameDirectConversation(
              companyId,
              conversationId,
              request,
            ),
          "The conversation could not be renamed.",
        ),
      invalidatesTags: (_result, _error, { companyId, conversationId }) => [
        { id: companyId, type: "Conversation" },
        { id: conversationId, type: "Conversation" },
      ],
      async onQueryStarted(
        { companyId, conversationId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          for (const endpoint of [
            "listWorkspaceChatConversations",
            "listWorkspaceDirectConversations",
          ] as const) {
            dispatch(
              workspaceApi.util.updateQueryData(endpoint, companyId, (page) => {
                const conversation = page?.items.find(
                  (item) => item.id === conversationId,
                );
                if (conversation && conversation.revision <= data.revision) {
                  conversation.title = data.title;
                  conversation.revision = data.revision;
                  conversation.updatedAt = data.updatedAt;
                }
              }),
            );
          }
        } catch {
          // Failed saves retain the draft; invalidation refreshes the saved title.
        }
      },
    }),
    startWorkspaceChatSession: builder.mutation<
      DirectConversationDto,
      {
        companyId: string;
        agentId: string;
        requestKey: string;
        messageKey: string;
        content: string;
      }
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Conversation" },
      ],
      queryFn: (
        { companyId, agentId, requestKey, messageKey, content },
        { dispatch },
      ) =>
        executeBoardlyQuery(async () => {
          const conversation =
            await conversationService.createDirectConversation(companyId, {
              agentId,
              requestKey,
            });
          await conversationService.postMessage(conversation.id, {
            requestKey: messageKey,
            content,
          });
          await refreshWorkspaceChatConversations(dispatch, companyId);
          return conversation;
        }, "The new chat could not be started."),
    }),
    listWorkspaceCompanies: builder.query<CompanyPage, void>({
      queryFn: () =>
        executeBoardlyQuery(
          () => companyService.listCompanies(),
          "Companies could not be loaded.",
        ),
      providesTags: ["Company"],
    }),
    getWorkspaceCompany: builder.query({
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "Company" },
      ],
      queryFn: (companyId: string) =>
        executeBoardlyQuery(
          () => companyService.getCompanyById(companyId),
          "Company could not be loaded.",
        ),
    }),
    getWorkspaceCompanyAvatar: builder.query<
      string,
      { companyId: string; attachmentId: string }
    >({
      keepUnusedDataFor: 60,
      queryFn: ({ companyId, attachmentId }, { signal }) =>
        executeBoardlyQuery(async () => {
          const blob = await attachmentService.download(
            companyId,
            attachmentId,
            signal,
          );
          return URL.createObjectURL(blob);
        }, "Company avatar could not be loaded."),
      async onCacheEntryAdded(_arg, { cacheDataLoaded, cacheEntryRemoved }) {
        try {
          const { data: url } = await cacheDataLoaded;
          await cacheEntryRemoved;
          URL.revokeObjectURL(url);
        } catch {
          // No object URL exists when the image request failed or was cancelled.
        }
      },
    }),
    deleteWorkspaceCompany: builder.mutation<
      void,
      { companyId: string; expectedRevision: number }
    >({
      invalidatesTags: ["Company"],
      queryFn: ({ companyId, expectedRevision }) =>
        executeBoardlyQuery(
          () => companyService.deleteCompany(companyId, expectedRevision),
          "Company could not be deleted.",
        ),
    }),
    getWorkspaceProactiveMode: builder.query<ProactiveModeDto, string>({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "ProactiveMode" },
      ],
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => companyService.getProactiveMode(companyId),
          "Proactive Mode could not be loaded.",
        ),
    }),
    updateWorkspaceProactiveMode: builder.mutation<
      ProactiveModeDto,
      { companyId: string; request: UpdateProactiveModeRequest }
    >({
      invalidatesTags: (_result, error, { companyId }) =>
        error ? [{ id: companyId, type: "ProactiveMode" }] : [],
      async onQueryStarted({ companyId }, { dispatch, queryFulfilled }) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            workspaceApi.util.upsertQueryData(
              "getWorkspaceProactiveMode",
              companyId,
              data,
            ),
          );
        } catch {
          // The failed setting is invalidated above so active readers refetch.
        }
      },
      queryFn: ({ companyId, request }) =>
        executeBoardlyQuery(
          () => companyService.updateProactiveMode(companyId, request),
          "Proactive Mode could not be updated.",
        ),
    }),
    updateWorkspaceCompanyProfile: builder.mutation<
      CompanyDetail,
      { companyId: string; payload: CompanySettingsProfileRequest }
    >({
      invalidatesTags: ["Company"],
      queryFn: ({ companyId, payload }) =>
        executeBoardlyQuery(
          () => companyService.updateCompanyProfile(companyId, payload),
          "Company settings could not be saved.",
        ),
    }),
    getWorkspaceAgentUsage: builder.query<
      AgentUsageReport,
      {
        companyId: string;
        agentId: string;
        startAt: string;
        endAt: string;
        timeZone: string;
        provider?: string;
        model?: string;
        memberId?: string;
        projectId?: string;
        taskId?: string;
        runType?: string;
        runtimeId?: string;
        bucket: "day" | "month";
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Run" },
        { id: companyId, type: "Agent" },
      ],
      queryFn: ({ companyId, agentId, ...period }) =>
        executeBoardlyQuery(
          () => companyService.getAgentUsage(companyId, agentId, period),
          "Compute usage could not be loaded.",
        ),
    }),
    getWorkspaceCompanyUsage: builder.query<
      CompanyUsageReport,
      { companyId: string; period: CompanyUsagePeriod; timeZone: string }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Run" },
        { id: companyId, type: "Agent" },
      ],
      queryFn: ({ companyId, period, timeZone }) =>
        executeBoardlyQuery(
          () => companyService.getCompanyUsage(companyId, period, timeZone),
          "Company usage could not be loaded.",
        ),
    }),
    getWorkspaceActivityInsight: builder.query<ActivityInsight, string>({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "Run" },
      ],
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => companyService.getActivityInsight(companyId),
          "Activity insight could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsSummary: builder.query<
      AnalyticsSummary,
      Parameters<typeof companyService.getAnalyticsSummary>[1] & {
        companyId: string;
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsSummary(companyId, query),
          "Analytics summary could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsSeries: builder.query<
      AnalyticsSeries,
      Parameters<typeof companyService.getAnalyticsSeries>[1] & {
        companyId: string;
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsSeries(companyId, query),
          "Analytics trend could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsBreakdown: builder.query<
      AnalyticsBreakdown,
      Parameters<typeof companyService.getAnalyticsBreakdown>[1] & {
        companyId: string;
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsBreakdown(companyId, query),
          "Analytics breakdown could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsFailures: builder.query<
      AnalyticsFailures,
      Parameters<typeof companyService.getAnalyticsFailures>[1] & {
        companyId: string;
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsFailures(companyId, query),
          "Analytics reliability could not be loaded.",
        ),
    }),
    getWorkspaceTaskUsage: builder.query<
      AnalyticsTaskUsage,
      { companyId: string; taskId: string }
    >({
      providesTags: (_result, _error, { companyId, taskId }) => [
        { id: companyId, type: "Analytics" },
        { id: taskId, type: "Task" },
      ],
      queryFn: ({ companyId, taskId }) =>
        executeBoardlyQuery(
          () => companyService.getTaskUsage(companyId, taskId),
          "Task usage could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsBudgets: builder.query<AnalyticsBudgetPage, string>({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsBudgets(companyId),
          "Analytics budgets could not be loaded.",
        ),
    }),
    getWorkspaceAnalyticsHeatmap: builder.query<
      AnalyticsHeatmap,
      Parameters<typeof companyService.getAnalyticsHeatmap>[1] & {
        companyId: string;
      }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeBoardlyQuery(
          () => companyService.getAnalyticsHeatmap(companyId, query),
          "Analytics heatmap could not be loaded.",
        ),
    }),
    putWorkspaceAnalyticsBudget: builder.mutation<
      AnalyticsBudget,
      { companyId: string; payload: AnalyticsBudgetInput }
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Analytics" },
      ],
      queryFn: ({ companyId, payload }) =>
        executeBoardlyQuery(
          () => companyService.putAnalyticsBudget(companyId, payload),
          "Analytics budget could not be saved.",
        ),
    }),
    getWorkspaceCockpit: builder.query({
      providesTags: (_result, _error, companyId: string) => [
        { id: `${companyId}:cockpit`, type: "Company" },
        { id: companyId, type: "Company" },
        { id: companyId, type: "Agent" },
        { id: companyId, type: "Task" },
        { id: companyId, type: "Conversation" },
      ],
      queryFn: (companyId: string) =>
        executeBoardlyQuery(
          () => cockpitService.getCockpit(companyId),
          `Cockpit for Company ${companyId} could not be loaded.`,
        ),
    }),
    listWorkspaceActivity: builder.query<
      Awaited<ReturnType<typeof activityService.listCompanyActivity>>,
      string
    >({
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "Activity" },
      ],
      queryFn: (companyId: string) =>
        executeBoardlyQuery(
          () => activityService.listCompanyActivity(companyId),
          "Activity could not be loaded.",
        ),
    }),
    listWorkspaceActivityPages: builder.infiniteQuery<
      ActivityPage,
      { companyId: string; options: Omit<ActivityListOptions, "cursor"> },
      string | null
    >({
      infiniteQueryOptions: {
        initialPageParam: null,
        getNextPageParam: (lastPage) => lastPage.nextCursor ?? undefined,
      },
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Activity" },
      ],
      queryFn: ({ queryArg: { companyId, options }, pageParam }) =>
        executeBoardlyQuery(
          () =>
            activityService.listCompanyActivityPage(companyId, {
              ...options,
              cursor: pageParam ?? undefined,
            }),
          "Activity could not be loaded.",
        ),
    }),
    getWorkspaceActivityCalendar: builder.query<
      ActivityCalendarSummary,
      ActivityCalendarRequest
    >({
      keepUnusedDataFor: 86_400,
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Activity" },
      ],
      queryFn: (request) =>
        executeBoardlyQuery(
          () => activityService.getCompanyActivityCalendar(request),
          "Activity calendar could not be loaded.",
        ),
    }),
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
    listWorkspaceDirectConversations: builder.query<
      Awaited<ReturnType<typeof conversationService.listDirectConversations>>,
      string
    >({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "Conversation" },
      ],
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => conversationService.listDirectConversations(companyId),
          "Conversations could not be loaded.",
        ),
    }),
    getWorkspaceConversationState: builder.query<ConversationStateDto, string>({
      providesTags: (_result, _error, conversationId) => [
        { id: conversationId, type: "Conversation" },
      ],
      queryFn: (conversationId) =>
        executeBoardlyQuery(
          () => conversationService.getConversationState(conversationId),
          "Chat state could not be loaded.",
        ),
    }),
    searchWorkspaceConversations: builder.query<
      CompanySearchPage,
      { companyId: string; request: CompanySearchRequest }
    >({
      queryFn: ({ companyId, request }) =>
        executeBoardlyQuery(
          () => companySearchService.search(companyId, request),
          "Chats could not be searched.",
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
    getWorkspaceConversationHistory: builder.query<
      ConversationHistoryWindow,
      { companyId: string; conversationId: string }
    >({
      keepUnusedDataFor: 15 * 60,
      providesTags: (_result, _error, { conversationId }) => [
        { id: conversationId, type: "Message" },
      ],
      queryFn: ({ conversationId }) =>
        executeBoardlyQuery(
          () => conversationService.loadLatestMessages(conversationId),
          "Messages could not be loaded.",
        ),
      // A message frame can arrive while this HTTP snapshot is in flight.
      // Keep those newer rows until a later read includes them.
      merge: (current, incoming) => ({
        ...incoming,
        items: [
          ...incoming.items,
          ...current.items.filter(
            (item) => item.sequence > incoming.throughSeq,
          ),
        ],
      }),
    }),
    listWorkspaceMessages: builder.query({
      providesTags: (_result, _error, conversationId: string) => [
        { id: conversationId, type: "Message" },
      ],
      queryFn: (conversationId: string) =>
        executeBoardlyQuery(
          () => conversationService.listMessages(conversationId),
          "Messages could not be loaded.",
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
    listWorkspaceResources: builder.query<
      Awaited<ReturnType<typeof companyResourceService.listResources>>,
      string
    >({
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "ProjectResource" },
      ],
      queryFn: (companyId: string) =>
        executeBoardlyQuery(
          () => companyResourceService.listResources(companyId),
          "Resources could not be loaded.",
        ),
    }),
    createWorkspaceAgent: builder.mutation<
      Awaited<ReturnType<typeof agentDirectoryService.createAgent>>,
      { companyId: string; request: AgentCreateRequest }
    >({
      invalidatesTags: (_result, _error, argument) => [
        { id: argument.companyId, type: "Agent" },
      ],
      queryFn: (argument) =>
        executeBoardlyQuery(
          () =>
            agentDirectoryService.createAgent(
              argument.companyId,
              argument.request,
            ),
          "Agent could not be created.",
        ),
    }),
    updateWorkspaceAgent: builder.mutation<
      Awaited<ReturnType<typeof agentDirectoryService.updateAgent>>,
      {
        companyId: string;
        request: AgentUpdateRequest;
        agentId: string;
      }
    >({
      invalidatesTags: (_result, _error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "Agent" },
      ],
      queryFn: (argument: {
        companyId: string;
        request: AgentUpdateRequest;
        agentId: string;
      }) =>
        executeBoardlyQuery(
          () =>
            agentDirectoryService.updateAgent(
              argument.companyId,
              argument.agentId,
              argument.request,
            ),
          "Agent configuration could not be saved.",
        ),
    }),
    archiveWorkspaceAgent: agentLifecycleMutation(builder, "archive"),
    restoreWorkspaceAgent: agentLifecycleMutation(builder, "restore"),
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
    sendWorkspaceMessage: builder.mutation({
      invalidatesTags: (
        _result,
        _error,
        argument: { conversationId: string },
      ) => [{ id: argument.conversationId, type: "Message" }],
      queryFn: (argument: {
        conversationId: string;
        request: SendConversationMessageRequest;
      }) =>
        executeBoardlyQuery(
          () =>
            conversationService.postMessage(
              argument.conversationId,
              argument.request,
            ),
          "Message could not be sent.",
        ),
    }),
    updateWorkspaceConversationState: builder.mutation<
      ConversationStateDto,
      {
        companyId: string;
        conversationId: string;
        request: ConversationStateUpdateRequest;
      }
    >({
      invalidatesTags: (_result, _error, { companyId, conversationId }) => [
        { id: conversationId, type: "Conversation" },
        { id: companyId, type: "Conversation" },
      ],
      async onQueryStarted(
        { companyId, conversationId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            workspaceApi.util.upsertQueryData(
              "getWorkspaceConversationState",
              conversationId,
              data,
            ),
          );
          dispatch(
            workspaceApi.util.updateQueryData(
              "listWorkspaceConversationStates",
              companyId,
              (states) => {
                if (!states) return;
                const index = states.items.findIndex(
                  (item) => item.conversationId === conversationId,
                );
                if (index >= 0) states.items[index] = data;
                else states.items.push(data);
              },
            ),
          );
          dispatch(
            workspaceApi.util.updateQueryData(
              "listWorkspaceChatConversations",
              companyId,
              (conversations) => {
                if (!conversations) return;
                const conversation = conversations.items.find(
                  (item) => item.id === conversationId,
                );
                if (conversation) {
                  conversation.pinnedAt = data.pinnedAt;
                  conversation.archivedAt = data.archivedAt;
                }
              },
            ),
          );
          dispatch(
            workspaceApi.util.updateQueryData(
              "listWorkspaceDirectConversations",
              companyId,
              (conversations) => {
                const conversation = conversations.items.find(
                  (item) => item.id === conversationId,
                );
                if (conversation) conversation.pinnedAt = data.pinnedAt;
              },
            ),
          );
        } catch {
          // Invalidation refreshes the state and list after a conflict or failure.
        }
      },
      queryFn: (argument) =>
        executeBoardlyQuery(
          () =>
            conversationService.updateConversationState(
              argument.conversationId,
              argument.request,
            ),
          "Chat state could not be updated.",
        ),
    }),
    createWorkspaceResource: builder.mutation<
      CompanyResourceDto,
      {
        companyId: string;
        request: CompanyResourceCreateRequest;
      }
    >({
      invalidatesTags: (_result, _error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "ProjectResource" },
      ],
      queryFn: (argument: {
        companyId: string;
        request: CompanyResourceCreateRequest;
      }) =>
        executeBoardlyQuery(
          () =>
            companyResourceService.createResource(
              argument.companyId,
              argument.request,
            ),
          "Resource could not be created.",
        ),
    }),
    updateWorkspaceResource: builder.mutation<
      Awaited<ReturnType<typeof companyResourceService.updateResource>>,
      {
        companyId: string;
        request: CompanyResourceUpdateRequest;
        resourceId: string;
      }
    >({
      invalidatesTags: (_result, _error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "ProjectResource" },
      ],
      queryFn: (argument: {
        companyId: string;
        request: CompanyResourceUpdateRequest;
        resourceId: string;
      }) =>
        executeBoardlyQuery(
          () =>
            companyResourceService.updateResource(
              argument.companyId,
              argument.resourceId,
              argument.request,
            ),
          "Resource could not be updated.",
        ),
    }),
    deleteWorkspaceResource: builder.mutation<
      Awaited<ReturnType<typeof companyResourceService.deleteResource>>,
      {
        companyId: string;
        request: EntityRevisionRequest;
        resourceId: string;
      }
    >({
      invalidatesTags: (_result, _error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "ProjectResource" },
      ],
      queryFn: (argument: {
        companyId: string;
        request: EntityRevisionRequest;
        resourceId: string;
      }) =>
        executeBoardlyQuery(
          () =>
            companyResourceService.deleteResource(
              argument.companyId,
              argument.resourceId,
              argument.request,
            ),
          "Resource could not be deleted.",
        ),
    }),
  }),
  overrideExisting: false,
});

export async function refreshWorkspaceChatConversations(
  dispatch: BaseQueryApi["dispatch"],
  companyId: string,
) {
  // The route checks this catalogue before opening a conversation. A read
  // already in flight may predate creation, so let it finish before refetching.
  await dispatch(
    workspaceApi.util.getRunningQueryThunk(
      "listWorkspaceChatConversations",
      companyId,
    ),
  );
  await dispatch(
    workspaceApi.endpoints.listWorkspaceChatConversations.initiate(companyId, {
      forceRefetch: true,
      subscribe: false,
    }),
  ).unwrap();
}

function agentLifecycleMutation(
  builder: Parameters<
    typeof boardlyApi.injectEndpoints
  >[0]["endpoints"] extends (builder: infer T) => unknown
    ? T
    : never,
  operation: "archive" | "restore",
) {
  return builder.mutation<
    AgentDto,
    { companyId: string; agentId: string; request: EntityRevisionRequest }
  >({
    invalidatesTags: (_result, _error, argument: { companyId: string }) => [
      { id: argument.companyId, type: "Agent" },
    ],
    queryFn: (argument: {
      companyId: string;
      request: EntityRevisionRequest;
      agentId: string;
    }) =>
      executeBoardlyQuery(
        () =>
          operation === "archive"
            ? agentDirectoryService.archiveAgent(
                argument.companyId,
                argument.agentId,
                argument.request,
              )
            : agentDirectoryService.restoreAgent(
                argument.companyId,
                argument.agentId,
                argument.request,
              ),
        `Agent could not be ${operation === "archive" ? "archived" : "restored"}.`,
      ),
  });
}

export const {
  useListWorkspaceConversationPinsQuery,
  useListWorkspaceConversationStatesQuery,
  useListWorkspaceChatConversationsQuery,
  useStartWorkspaceChatSessionMutation,
  useCreateWorkspaceChatSessionMutation,
  useRenameWorkspaceConversationMutation,
  useListWorkspaceCompaniesQuery,
  useLazyListWorkspaceCompaniesQuery,
  useArchiveWorkspaceAgentMutation,
  useCreateWorkspaceResourceMutation,
  useCreateWorkspaceTaskMutation,
  useCreateWorkspaceAgentMutation,
  useDeleteWorkspaceResourceMutation,
  useDeleteWorkspaceCompanyMutation,
  useGetWorkspaceCompanyQuery,
  useGetWorkspaceCompanyAvatarQuery,
  useLazyGetWorkspaceCompanyQuery,
  useGetWorkspaceProactiveModeQuery,
  useGetWorkspaceActivityCalendarQuery,
  useGetWorkspaceActivityInsightQuery,
  useGetWorkspaceAnalyticsSummaryQuery,
  useGetWorkspaceAnalyticsSeriesQuery,
  useGetWorkspaceAnalyticsBreakdownQuery,
  useGetWorkspaceAnalyticsFailuresQuery,
  useGetWorkspaceAnalyticsBudgetsQuery,
  useGetWorkspaceAnalyticsHeatmapQuery,
  usePutWorkspaceAnalyticsBudgetMutation,
  useGetWorkspaceTaskUsageQuery,
  useGetWorkspaceCompanyUsageQuery,
  useGetWorkspaceAgentUsageQuery,
  useGetWorkspaceTaskQuery,
  useGetWorkspaceTaskWorkspaceQuery,
  useGetWorkspaceTaskDependenciesQuery,
  useGetWorkspaceTaskDependencyGraphQuery,
  useLazyGetWorkspaceActivityCalendarQuery,
  useLazyGetWorkspaceConversationStateQuery,
  useUpdateWorkspaceCompanyProfileMutation,
  useUpdateWorkspaceProactiveModeMutation,
  useGetWorkspaceCockpitQuery,
  useLazyListWorkspaceResourcesQuery,
  useLazyListWorkspaceTasksQuery,
  useLazySearchWorkspaceConversationsQuery,
  useListWorkspaceActivityQuery,
  useListWorkspaceActivityPagesInfiniteQuery,
  useListWorkspaceDirectConversationsQuery,
  useListWorkspaceMessagesQuery,
  useListWorkspaceTaskMessagesQuery,
  useListWorkspaceResourcesQuery,
  useListWorkspaceTasksQuery,
  useRestoreWorkspaceAgentMutation,
  useSendWorkspaceMessageMutation,
  useStartWorkspaceTaskExecutionMutation,
  useUpdateWorkspaceConversationStateMutation,
  useUpdateWorkspaceResourceMutation,
  useUpdateWorkspaceTaskMutation,
  useUpdateWorkspaceAgentMutation,
} = workspaceApi;
