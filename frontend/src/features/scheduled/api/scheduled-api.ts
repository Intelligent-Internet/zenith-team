import { runService } from "@/shared/api/run.service";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import { connectorService } from "@/shared/api/connector.service";
import { agentDirectoryService } from "@/shared/api/agent-directory.service";
import { pluginService } from "@/shared/api/plugin.service";
import {
  scheduleService,
  type ScheduleCreateRequest,
  type ScheduleLifecycleRequest,
  type ScheduleListRequest,
  type SchedulePage,
  type SchedulePreviewRequest,
  type SchedulePreviewResponse,
  type ScheduleRunListRequest,
  type ScheduleRunPage,
  type ScheduleRunRecord,
  type ScheduleRunNowRequest,
  type ScheduledTask,
  type ScheduleUpdateRequest,
} from "@/shared/api/schedule.service";
import type {
  AgentPage,
  RunDto,
  ConnectorCatalogResponseV1,
  ConnectorProvider,
  PluginCatalogResponseV1,
} from "@/shared/api/types";

type CompanyScheduleArguments = {
  companyId: string;
  scheduleId: string;
};

type ScheduleMutationArguments<Request> = CompanyScheduleArguments & {
  request: Request;
};

type RunScheduleArguments = CompanyScheduleArguments & {
  request: ScheduleRunNowRequest;
};

export const scheduledApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listSchedules: builder.query<
      SchedulePage,
      { companyId: string } & ScheduleListRequest
    >({
      providesTags: (result, _error, { companyId }) => [
        { id: companyId, type: "Schedule" },
        { id: `${companyId}:list`, type: "Schedule" },
        ...(result?.schedules.map((schedule) => ({
          id: schedule.id,
          type: "Schedule" as const,
        })) ?? []),
      ],
      queryFn: ({ companyId, ...request }) =>
        executeZenithTeamQuery(
          () => scheduleService.list(companyId, request),
          "Schedules could not be loaded.",
          { retry: true },
        ),
    }),
    getSchedule: builder.query<ScheduledTask, CompanyScheduleArguments>({
      providesTags: (_result, _error, { companyId, scheduleId }) => [
        { id: companyId, type: "Schedule" },
        { id: scheduleId, type: "Schedule" },
      ],
      queryFn: ({ companyId, scheduleId }) =>
        executeZenithTeamQuery(
          () => scheduleService.get(companyId, scheduleId),
          "Schedule could not be loaded.",
          { retry: true },
        ),
    }),
    previewSchedule: builder.mutation<
      SchedulePreviewResponse,
      { companyId: string; request: SchedulePreviewRequest }
    >({
      queryFn: ({ companyId, request }) =>
        executeZenithTeamQuery(
          () => scheduleService.preview(companyId, request),
          "Schedule preview could not be generated.",
        ),
    }),
    createSchedule: builder.mutation<
      ScheduledTask,
      { companyId: string; request: ScheduleCreateRequest }
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: `${companyId}:list`, type: "Schedule" },
      ],
      queryFn: ({ companyId, request }) =>
        executeZenithTeamQuery(
          () => scheduleService.create(companyId, request),
          "Schedule could not be created.",
        ),
    }),
    updateSchedule: builder.mutation<
      ScheduledTask,
      ScheduleMutationArguments<ScheduleUpdateRequest>
    >({
      invalidatesTags: scheduleMutationTags,
      queryFn: ({ companyId, request, scheduleId }) =>
        executeZenithTeamQuery(
          () => scheduleService.update(companyId, scheduleId, request),
          "Schedule could not be updated.",
        ),
    }),
    pauseSchedule: builder.mutation<
      ScheduledTask,
      ScheduleMutationArguments<ScheduleLifecycleRequest>
    >({
      invalidatesTags: scheduleMutationTags,
      queryFn: ({ companyId, request, scheduleId }) =>
        executeZenithTeamQuery(
          () => scheduleService.pause(companyId, scheduleId, request),
          "Schedule could not be paused.",
        ),
    }),
    resumeSchedule: builder.mutation<
      ScheduledTask,
      ScheduleMutationArguments<ScheduleLifecycleRequest>
    >({
      invalidatesTags: scheduleMutationTags,
      queryFn: ({ companyId, request, scheduleId }) =>
        executeZenithTeamQuery(
          () => scheduleService.resume(companyId, scheduleId, request),
          "Schedule could not be resumed.",
        ),
    }),
    cancelSchedule: builder.mutation<
      ScheduledTask,
      ScheduleMutationArguments<ScheduleLifecycleRequest>
    >({
      invalidatesTags: scheduleMutationTags,
      queryFn: ({ companyId, request, scheduleId }) =>
        executeZenithTeamQuery(
          () => scheduleService.cancel(companyId, scheduleId, request),
          "Schedule could not be cancelled.",
        ),
    }),
    cancelWaitingScheduleRun: builder.mutation<
      RunDto,
      CompanyScheduleArguments & { runId: string }
    >({
      invalidatesTags: scheduleMutationTags,
      queryFn: ({ companyId, runId }) =>
        executeZenithTeamQuery(
          () => runService.cancelRun(companyId, runId),
          "The waiting run could not be cancelled.",
        ),
    }),
    runScheduleNow: builder.mutation<ScheduleRunRecord, RunScheduleArguments>({
      invalidatesTags: (_result, _error, { companyId, scheduleId }) => [
        { id: scheduleId, type: "Schedule" },
        { id: `${companyId}:${scheduleId}:runs`, type: "Run" },
      ],
      queryFn: ({ companyId, request, scheduleId }) =>
        executeZenithTeamQuery(
          () => scheduleService.runNow(companyId, scheduleId, request),
          "Schedule could not be run now.",
        ),
    }),
    listScheduleRuns: builder.query<
      ScheduleRunPage,
      CompanyScheduleArguments & ScheduleRunListRequest
    >({
      providesTags: (_result, _error, { companyId, scheduleId }) => [
        { id: companyId, type: "Schedule" },
        { id: companyId, type: "Run" },
        { id: `${companyId}:${scheduleId}:runs`, type: "Run" },
      ],
      queryFn: ({ companyId, scheduleId, ...request }) =>
        executeZenithTeamQuery(
          () => scheduleService.listRuns(companyId, scheduleId, request),
          "Schedule history could not be loaded.",
          { retry: true },
        ),
    }),
    listScheduleSources: builder.query<
      ConnectorCatalogResponseV1,
      { companyId: string; provider: ConnectorProvider }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Connector" },
      ],
      queryFn: ({ companyId, provider }) =>
        executeZenithTeamQuery(
          () =>
            connectorService.listAllConnectors(companyId, {
              provider,
              status: "connected",
            }),
          "Connected sources could not be loaded.",
          { retry: true },
        ),
    }),
    listSchedulePlugins: builder.query<
      PluginCatalogResponseV1,
      { companyId: string; provider: ConnectorProvider }
    >({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Plugin" },
      ],
      queryFn: ({ companyId, provider }) =>
        executeZenithTeamQuery(
          () =>
            pluginService.listAllPlugins(companyId, {
              provider,
              status: "installed",
            }),
          "Plugin presentation details could not be loaded.",
          { retry: true },
        ),
    }),
    listScheduleAgents: builder.query<AgentPage, { companyId: string }>({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Agent" },
      ],
      queryFn: ({ companyId }) =>
        executeZenithTeamQuery(
          () =>
            agentDirectoryService.listAgents(companyId, {
              includeArchived: false,
            }),
          "Agents could not be loaded for this schedule.",
          { retry: true },
        ),
    }),
  }),
  overrideExisting: false,
});

function scheduleMutationTags(
  _result: unknown,
  _error: unknown,
  { companyId, scheduleId }: CompanyScheduleArguments,
) {
  return [
    { id: `${companyId}:${scheduleId}:runs`, type: "Run" as const },
    { id: `${companyId}:list`, type: "Schedule" as const },
    { id: scheduleId, type: "Schedule" as const },
  ];
}

export const {
  useCancelScheduleMutation,
  useCancelWaitingScheduleRunMutation,
  useCreateScheduleMutation,
  useGetScheduleQuery,
  useLazyListSchedulesQuery,
  useLazyListScheduleRunsQuery,
  useListScheduleAgentsQuery,
  useListSchedulePluginsQuery,
  useListScheduleRunsQuery,
  useListSchedulesQuery,
  useListScheduleSourcesQuery,
  usePauseScheduleMutation,
  usePreviewScheduleMutation,
  useResumeScheduleMutation,
  useRunScheduleNowMutation,
  useUpdateScheduleMutation,
} = scheduledApi;
